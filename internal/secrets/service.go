package secrets

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"encoding/pem"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	"drool/internal/ids"
	"drool/internal/store"
	"drool/internal/store/db"
)

// Kinds of secret.
const (
	KindSSHKey       = "ssh_private_key"
	KindPassword     = "password"
	KindSudoPassword = "sudo_password"
)

// ControllerKeyName is the SSH key generated on first start and used as the
// default credential for new hosts.
const ControllerKeyName = "controller-default"

var (
	ErrNotFound  = errors.New("credential not found")
	ErrNameTaken = errors.New("a credential with this name already exists")
	// ErrControllerKey protects DROOL's own key: the controller finds it by
	// name, so deleting or renaming it would leave hosts with a key DROOL no
	// longer uses (and a new one would be generated on the next start).
	ErrControllerKey = errors.New("this is DROOL's own SSH key; it can't be deleted or renamed")
	ErrInvalidName   = errors.New("name must be 1-64 characters: letters, digits, '.', '_' or '-'")
	ErrInvalidKind   = errors.New("kind must be ssh_private_key, password or sudo_password")
	ErrEmptyValue    = errors.New("value must not be empty")
)

// InUseError lists hosts that still reference a credential.
type InUseError struct{ Hosts []string }

func (e *InUseError) Error() string {
	return "credential is used by hosts: " + strings.Join(e.Hosts, ", ")
}

// KeyError reports an unusable SSH private key.
type KeyError struct{ Err error }

func (e *KeyError) Error() string { return "invalid SSH private key: " + e.Err.Error() }
func (e *KeyError) Unwrap() error { return e.Err }

var nameRE = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

// Service manages credentials. Values are write-only through the API: only
// metadata and public parts are ever returned.
type Service struct {
	st  *store.Store
	box *Box
	now func() time.Time
}

func NewService(st *store.Store, box *Box) *Service {
	return &Service{st: st, box: box, now: time.Now}
}

// Input describes a new credential or a replacement value.
type Input struct {
	Name        string
	Kind        string
	Description string
	Value       string // private key PEM or password; empty with Generate
	Passphrase  string // decrypts an encrypted private key on import; not stored
	Generate    bool   // generate a new ed25519 key (ssh_private_key only)
	CreatedBy   string // user id, may be empty
}

// Prepared is a validated, encrypted credential ready to insert.
type Prepared struct {
	Row db.Secret
}

// Prepare validates input, normalizes private keys, and encrypts the value.
// It does not touch the database.
func (s *Service) Prepare(in Input) (*Prepared, error) {
	in.Name = strings.TrimSpace(in.Name)
	if !nameRE.MatchString(in.Name) {
		return nil, ErrInvalidName
	}
	id := ids.New(ids.Secret)
	value, public, err := normalizeValue(in, in.Name)
	if err != nil {
		return nil, err
	}
	ct, nonce, ver, err := s.box.Seal(id, in.Kind, value)
	if err != nil {
		return nil, err
	}
	now := s.now().UnixMilli()
	row := db.Secret{
		ID: id, Name: in.Name, Kind: in.Kind, Ciphertext: ct, Nonce: nonce, KeyVersion: int64(ver),
		PublicPart: public, Description: strings.TrimSpace(in.Description), CreatedAt: now, UpdatedAt: now,
	}
	if in.CreatedBy != "" {
		row.CreatedBy = sql.NullString{String: in.CreatedBy, Valid: true}
	}
	return &Prepared{Row: row}, nil
}

// InsertTx stores a prepared credential.
func (s *Service) InsertTx(ctx context.Context, q *db.Queries, p *Prepared) error {
	r := p.Row
	err := q.InsertSecret(ctx, db.InsertSecretParams{
		ID: r.ID, Name: r.Name, Kind: r.Kind, Ciphertext: r.Ciphertext, Nonce: r.Nonce,
		KeyVersion: r.KeyVersion, PublicPart: r.PublicPart, Description: r.Description,
		CreatedBy: r.CreatedBy, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	})
	if store.IsUniqueViolation(err) {
		return ErrNameTaken
	}
	return err
}

// Update describes changes to an existing credential.
type Update struct {
	Name        *string
	Description *string
	Value       *string
	Passphrase  string
	Generate    bool
}

// UpdateTx applies changes. Replacing the value re-encrypts it with the
// current master key version.
func (s *Service) UpdateTx(ctx context.Context, q *db.Queries, id string, up Update) (db.Secret, error) {
	cur, err := q.GetSecret(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return db.Secret{}, ErrNotFound
	}
	if err != nil {
		return db.Secret{}, err
	}
	now := s.now().UnixMilli()
	if up.Name != nil || up.Description != nil {
		name, desc := cur.Name, cur.Description
		if up.Name != nil {
			name = strings.TrimSpace(*up.Name)
			if cur.Name == ControllerKeyName && name != cur.Name {
				return db.Secret{}, ErrControllerKey
			}
			if !nameRE.MatchString(name) {
				return db.Secret{}, ErrInvalidName
			}
		}
		if up.Description != nil {
			desc = strings.TrimSpace(*up.Description)
		}
		err := q.UpdateSecretMeta(ctx, db.UpdateSecretMetaParams{Name: name, Description: desc, UpdatedAt: now, ID: id})
		if store.IsUniqueViolation(err) {
			return db.Secret{}, ErrNameTaken
		}
		if err != nil {
			return db.Secret{}, err
		}
		cur.Name, cur.Description = name, desc
	}
	if up.Value != nil || up.Generate {
		in := Input{Kind: cur.Kind, Passphrase: up.Passphrase, Generate: up.Generate}
		if up.Value != nil {
			in.Value = *up.Value
		}
		value, public, err := normalizeValue(in, cur.Name)
		if err != nil {
			return db.Secret{}, err
		}
		ct, nonce, ver, err := s.box.Seal(id, cur.Kind, value)
		if err != nil {
			return db.Secret{}, err
		}
		if err := q.UpdateSecretValue(ctx, db.UpdateSecretValueParams{
			Ciphertext: ct, Nonce: nonce, KeyVersion: int64(ver), PublicPart: public, UpdatedAt: now, ID: id,
		}); err != nil {
			return db.Secret{}, err
		}
		cur.PublicPart = public
	}
	cur.UpdatedAt = now
	return cur, nil
}

// DeleteTx removes a credential that no host references.
func (s *Service) DeleteTx(ctx context.Context, q *db.Queries, id string) (db.Secret, error) {
	cur, err := q.GetSecret(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return db.Secret{}, ErrNotFound
	}
	if err != nil {
		return db.Secret{}, err
	}
	if cur.Name == ControllerKeyName {
		return db.Secret{}, ErrControllerKey
	}
	users, err := q.ListHostsUsingSecret(ctx, id)
	if err != nil {
		return db.Secret{}, err
	}
	if len(users) > 0 {
		names := make([]string, len(users))
		for i, u := range users {
			names[i] = u.Name
		}
		return db.Secret{}, &InUseError{Hosts: names}
	}
	return cur, q.DeleteSecret(ctx, id)
}

// Reveal decrypts a credential for use by the controller (never for API
// responses).
func (s *Service) Reveal(ctx context.Context, q *db.Queries, id string) (db.Secret, []byte, error) {
	row, err := q.GetSecret(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return db.Secret{}, nil, ErrNotFound
	}
	if err != nil {
		return db.Secret{}, nil, err
	}
	pt, err := s.box.Open(row.ID, row.Kind, row.Ciphertext, row.Nonce, int(row.KeyVersion))
	return row, pt, err
}

// EnsureControllerKey creates the controller's default SSH key on first
// start. onCreate runs in the same transaction (for auditing). It returns the
// key's row either way.
func (s *Service) EnsureControllerKey(ctx context.Context, onCreate func(q *db.Queries, row db.Secret) error) (db.Secret, bool, error) {
	if row, err := s.st.R.GetSecretByName(ctx, ControllerKeyName); err == nil {
		return row, false, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return db.Secret{}, false, err
	}
	p, err := s.Prepare(Input{
		Name: ControllerKeyName, Kind: KindSSHKey, Generate: true,
		Description: "Generated by DROOL. Add the public key to ~/.ssh/authorized_keys on managed hosts.",
	})
	if err != nil {
		return db.Secret{}, false, err
	}
	err = s.st.InTx(ctx, func(q *db.Queries) error {
		if err := s.InsertTx(ctx, q, p); err != nil {
			return err
		}
		return onCreate(q, p.Row)
	})
	return p.Row, err == nil, err
}

// normalizeValue validates a value for its kind. Private keys are parsed,
// decrypted with the passphrase if needed, and re-encoded unencrypted in
// OpenSSH format (encryption at rest is provided by the Box).
func normalizeValue(in Input, name string) (value []byte, public string, err error) {
	switch in.Kind {
	case KindSSHKey:
		var key any
		if in.Generate {
			_, priv, err := ed25519.GenerateKey(rand.Reader)
			if err != nil {
				return nil, "", err
			}
			key = priv
		} else {
			if strings.TrimSpace(in.Value) == "" {
				return nil, "", ErrEmptyValue
			}
			raw := []byte(strings.TrimSpace(in.Value) + "\n")
			if in.Passphrase != "" {
				key, err = ssh.ParseRawPrivateKeyWithPassphrase(raw, []byte(in.Passphrase))
			} else {
				key, err = ssh.ParseRawPrivateKey(raw)
			}
			var missing *ssh.PassphraseMissingError
			if errors.As(err, &missing) {
				return nil, "", &KeyError{Err: errors.New("the key is encrypted; provide its passphrase")}
			}
			if err != nil {
				return nil, "", &KeyError{Err: err}
			}
		}
		signer, err := ssh.NewSignerFromKey(key)
		if err != nil {
			return nil, "", &KeyError{Err: err}
		}
		block, err := ssh.MarshalPrivateKey(key, "drool:"+name)
		if err != nil {
			return nil, "", &KeyError{Err: err}
		}
		pub := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey()))) + " drool:" + name
		return pem.EncodeToMemory(block), pub, nil
	case KindPassword, KindSudoPassword:
		if in.Generate {
			return nil, "", errors.New("generate is only supported for ssh_private_key")
		}
		if in.Value == "" {
			return nil, "", ErrEmptyValue
		}
		if len(in.Value) > 4096 {
			return nil, "", fmt.Errorf("value too long")
		}
		return []byte(in.Value), "", nil
	default:
		return nil, "", ErrInvalidKind
	}
}

// Signer parses a revealed SSH private key.
func Signer(value []byte) (ssh.Signer, error) {
	return ssh.ParsePrivateKey(value)
}

// KeyMismatchError means stored credentials cannot be decrypted with the
// master key in use.
type KeyMismatchError struct {
	Total, Failed int
	First         string // name of the first credential that failed
}

func (e *KeyMismatchError) Error() string {
	return fmt.Sprintf("%d of %d stored credentials cannot be decrypted with this master key (first: %s)", e.Failed, e.Total, e.First)
}

// VerifyBox decrypts every stored credential with box and returns how many
// there are. It never returns plaintext.
func VerifyBox(ctx context.Context, q *db.Queries, box *Box) (int, error) {
	rows, err := q.ListSecrets(ctx)
	if err != nil {
		return 0, err
	}
	var bad *KeyMismatchError
	for _, r := range rows {
		if _, err := box.Open(r.ID, r.Kind, r.Ciphertext, r.Nonce, int(r.KeyVersion)); err != nil {
			if bad == nil {
				bad = &KeyMismatchError{Total: len(rows), First: r.Name}
			}
			bad.Failed++
		}
	}
	// The Agent CA keys are sealed with the same master key.
	cas, err := q.ListAgentCA(ctx)
	if err != nil {
		return 0, err
	}
	for _, r := range cas {
		if len(r.KeyCiphertext) == 0 {
			continue // offline root, or discarded during recovery
		}
		if _, err := box.Open(r.ID, "agent-ca-"+r.Kind, r.KeyCiphertext, r.KeyNonce, int(r.KeyVersion.Int64)); err != nil {
			if bad == nil {
				bad = &KeyMismatchError{Total: len(rows) + len(cas), First: "Agent CA " + r.Kind + " key"}
			}
			bad.Failed++
		}
	}
	if bad != nil {
		return len(rows), bad
	}
	return len(rows), nil
}

// DiscardAllTx deletes every stored credential and detaches hosts from
// them. It is the recovery path when the master key is lost: hosts keep
// their pinned keys and history but need credentials assigned again. The
// controller's default SSH key is regenerated on the next start. It returns
// the names of hosts that had a credential.
func DiscardAllTx(ctx context.Context, tx *sql.Tx) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT name FROM hosts
		WHERE ssh_credential_id IS NOT NULL OR become_credential_id IS NOT NULL ORDER BY name`)
	if err != nil {
		return nil, err
	}
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			rows.Close()
			return nil, err
		}
		names = append(names, n)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, stmt := range []string{
		`UPDATE hosts SET ssh_credential_id = NULL, become_credential_id = NULL,
			status = CASE WHEN status IN ('pending_hostkey','hostkey_mismatch','disabled') THEN status ELSE 'untested' END,
			status_detail = CASE WHEN ssh_credential_id IS NOT NULL THEN 'credential discarded during recovery; assign a credential' ELSE status_detail END`,
		`DELETE FROM secrets`,
		// The Agent CA keys can't be decrypted either. Agents keep working
		// once a new intermediate is issued with the offline root key
		// (drool agent-ca rotate --root-key): they move to it through
		// certificate recovery, without re-enrolling.
		`UPDATE agent_ca SET key_ciphertext = NULL, key_nonce = NULL, key_version = NULL,
			key_removed_at = CAST(strftime('%s','now') AS INTEGER) * 1000 WHERE key_ciphertext IS NOT NULL`,
	} {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return nil, err
		}
	}
	return names, nil
}
