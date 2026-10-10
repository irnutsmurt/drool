// Package sshca is DROOL's OpenSSH user certificate authority (JIT SSH,
// ARCHITECTURE-ALPHA §33.2). For a host in certificate mode, every SSH
// connection DROOL opens authenticates with a fresh Ed25519 key that the CA
// certifies for minutes, for the host's login user only. Hosts trust the CA
// with TrustedUserCAKeys; OpenSSH enforces the expiry. The CA key is sealed
// with the master key; the ephemeral keys never leave memory.
package sshca

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"drool/internal/audit"
	"drool/internal/driver"
	"drool/internal/ids"
	"drool/internal/secrets"
	"drool/internal/store"
	"drool/internal/store/db"
)

// CA states.
const (
	StateActive   = "active"   // issues certificates
	StateNext     = "next"     // introduced for a rotation: trusted on hosts, not issuing yet
	StatePrevious = "previous" // replaced by a rotation, still trusted until retired
	StateRetired  = "retired"  // private key destroyed
)

const sealKind = "ssh-user-ca"

// Settings are the certificate parameters.
type Settings struct {
	// TTL is how long a certificate is valid after it is issued (it is
	// used once, for one handshake).
	TTL time.Duration
	// ClockSkew backdates certificates so a host whose clock is behind by
	// up to this much still accepts them.
	ClockSkew time.Duration
}

// DefaultSettings: valid from 5 minutes ago until 10 minutes from now.
var DefaultSettings = Settings{TTL: 10 * time.Minute, ClockSkew: 5 * time.Minute}

// Deps are the service's dependencies.
type Deps struct {
	Store      *store.Store
	Box        *secrets.Box
	Audit      *audit.Writer
	Controller string // this controller's name, in certificate key ids
	Settings   Settings
	Log        *slog.Logger
	Now        func() time.Time
}

// Service manages the CAs, issues certificates and runs host trust steps.
type Service struct {
	d Deps

	mu    sync.Mutex
	hosts Hosts // set after construction (inventory needs the service first)
	conn  Connector
}

// Settings returns the certificate parameters.
func (s *Service) Settings() Settings { return s.d.Settings }

// New creates the service.
func New(d Deps) *Service {
	if d.Settings.TTL == 0 {
		d.Settings = DefaultSettings
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Log == nil {
		d.Log = slog.New(slog.DiscardHandler)
	}
	return &Service{d: d}
}

// CA is one certificate authority as shown to administrators.
type CA struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	PublicKey   string     `json:"public_key"`
	Fingerprint string     `json:"fingerprint"`
	State       string     `json:"state"`
	HasKey      bool       `json:"has_key"` // false once retired, or after its key was discarded in a recovery
	CreatedAt   time.Time  `json:"created_at"`
	ActivatedAt *time.Time `json:"activated_at"`
	RetiredAt   *time.Time `json:"retired_at"`
	Issued      int64      `json:"issued"` // certificates issued so far
}

type caRow struct {
	CA
	ct, nonce []byte
	ver       sql.NullInt64
}

const caColumns = `id, name, public_key, fingerprint, state, key_ciphertext, key_nonce, key_version, next_serial, created_at, activated_at, retired_at`

func scanCA(r interface{ Scan(...any) error }) (*caRow, error) {
	var c caRow
	var created int64
	var activated, retired sql.NullInt64
	var next int64
	if err := r.Scan(&c.ID, &c.Name, &c.PublicKey, &c.Fingerprint, &c.State, &c.ct, &c.nonce, &c.ver, &next, &created, &activated, &retired); err != nil {
		return nil, err
	}
	c.HasKey = len(c.ct) > 0
	c.Issued = next - 1
	c.CreatedAt = time.UnixMilli(created).UTC()
	if activated.Valid {
		t := time.UnixMilli(activated.Int64).UTC()
		c.ActivatedAt = &t
	}
	if retired.Valid {
		t := time.UnixMilli(retired.Int64).UTC()
		c.RetiredAt = &t
	}
	return &c, nil
}

// List returns every CA, newest first.
func (s *Service) List(ctx context.Context) ([]CA, error) {
	rows, err := s.d.Store.Reader.QueryContext(ctx, `SELECT `+caColumns+` FROM ssh_user_cas ORDER BY created_at DESC, id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CA
	for rows.Next() {
		c, err := scanCA(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c.CA)
	}
	return out, rows.Err()
}

func (s *Service) get(ctx context.Context, q db.DBTX, where string, args ...any) (*caRow, error) {
	c, err := scanCA(q.QueryRowContext(ctx, `SELECT `+caColumns+` FROM ssh_user_cas WHERE `+where, args...))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNoCA
	}
	return c, err
}

// Errors.
var (
	ErrNoCA       = errors.New("there is no such SSH certificate authority")
	ErrNoActiveCA = errors.New("DROOL has no SSH certificate authority yet; create one under Credentials")
	ErrHasActive  = errors.New("an SSH certificate authority already exists; rotate it instead")
	ErrHasNext    = errors.New("a rotation is already in progress: activate or retire the new authority first")
	ErrState      = errors.New("not possible in the authority's current state")
)

// StateError explains a refused lifecycle step.
type StateError struct{ Msg string }

func (e *StateError) Error() string { return e.Msg }

// Active returns the CA that issues certificates.
func (s *Service) Active(ctx context.Context) (*CA, error) {
	c, err := s.get(ctx, s.d.Store.Reader, `state = 'active'`)
	if errors.Is(err, ErrNoCA) {
		return nil, ErrNoActiveCA
	}
	if err != nil {
		return nil, err
	}
	return &c.CA, nil
}

// generate makes a new CA key pair sealed for a new row.
func (s *Service) generate(name string) (id, pub, fp string, ct, nonce []byte, ver int, err error) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return
	}
	defer clear(priv)
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		return
	}
	id = ids.New("ssca")
	pub = strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey()))) + " drool-user-ca:" + name
	fp = ssh.FingerprintSHA256(signer.PublicKey())
	ct, nonce, ver, err = s.d.Box.Seal(id, sealKind, priv.Seed())
	return
}

func validName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		name = "drool"
	}
	if len(name) > 64 || strings.ContainsAny(name, " \t\r\n") {
		return "", &StateError{"the name must be at most 64 characters without spaces"}
	}
	return name, nil
}

// Create makes the first CA (active). The caller audits it in its
// transaction (q).
func (s *Service) CreateTx(ctx context.Context, q db.DBTX, name, userID string) (*CA, error) {
	name, err := validName(name)
	if err != nil {
		return nil, err
	}
	var n int
	if err := q.QueryRowContext(ctx, `SELECT count(*) FROM ssh_user_cas WHERE state IN ('active','next')`).Scan(&n); err != nil {
		return nil, err
	}
	if n > 0 {
		return nil, ErrHasActive
	}
	return s.insert(ctx, q, name, userID, StateActive)
}

func (s *Service) insert(ctx context.Context, q db.DBTX, name, userID, state string) (*CA, error) {
	id, pub, fp, ct, nonce, ver, err := s.generate(name)
	if err != nil {
		return nil, err
	}
	now := s.d.Now().UnixMilli()
	var activated sql.NullInt64
	if state == StateActive {
		activated = sql.NullInt64{Int64: now, Valid: true}
	}
	if _, err := q.ExecContext(ctx, `INSERT INTO ssh_user_cas (id, name, public_key, fingerprint, key_ciphertext, key_nonce, key_version, state,
		created_at, created_by, activated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, name, pub, fp, ct, nonce, ver, state, now, nullStr(userID), activated); err != nil {
		return nil, err
	}
	c, err := s.get(ctx, q, `id = ?`, id)
	if err != nil {
		return nil, err
	}
	return &c.CA, nil
}

func nullStr(v string) sql.NullString { return sql.NullString{String: v, Valid: v != ""} }

// IntroduceTx starts a rotation: a new CA that hosts are made to trust
// (state next) while the current one keeps issuing.
func (s *Service) IntroduceTx(ctx context.Context, q db.DBTX, name, userID string) (*CA, error) {
	name, err := validName(name)
	if err != nil {
		return nil, err
	}
	var active, next int
	if err := q.QueryRowContext(ctx, `SELECT count(*) FILTER (WHERE state = 'active'), count(*) FILTER (WHERE state = 'next') FROM ssh_user_cas`).Scan(&active, &next); err != nil {
		return nil, err
	}
	if active == 0 {
		return nil, ErrNoActiveCA
	}
	if next > 0 {
		return nil, ErrHasNext
	}
	return s.insert(ctx, q, name, userID, StateNext)
}

// Unverified lists the certificate-mode hosts that haven't proven (with a
// fresh connection) that they accept certificates from caID.
func (s *Service) Unverified(ctx context.Context, q db.DBTX, caID string) ([]string, error) {
	rows, err := q.QueryContext(ctx, `SELECT h.name FROM hosts h
		LEFT JOIN ssh_ca_host_trust t ON t.host_id = h.id AND t.ca_id = ?
		WHERE h.ssh_auth = 'certificate' AND (t.verified_at IS NULL OR (t.failed_at IS NOT NULL AND t.failed_at > t.verified_at))
		ORDER BY h.name`, caID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		rows.Scan(&n)
		out = append(out, n)
	}
	return out, rows.Err()
}

// ActivateTx switches issuance to the CA introduced for a rotation. Every
// host in certificate mode must have proven it accepts the new CA first:
// otherwise switching would strand it. The old CA stays trusted (previous)
// until retired.
func (s *Service) ActivateTx(ctx context.Context, q db.DBTX, caID string) (*CA, error) {
	c, err := s.get(ctx, q, `id = ?`, caID)
	if err != nil {
		return nil, err
	}
	if c.State != StateNext {
		return nil, &StateError{"only an authority introduced for a rotation can be activated"}
	}
	if !c.HasKey {
		return nil, &StateError{"this authority's key is not available"}
	}
	missing, err := s.Unverified(ctx, q, caID)
	if err != nil {
		return nil, err
	}
	if len(missing) > 0 {
		return nil, &StateError{fmt.Sprintf("%d host(s) in certificate mode haven't shown that they accept the new authority: %s. "+
			"Run \"Trust DROOL's SSH certificate authority\" on them first (it installs both authorities and checks each)",
			len(missing), strings.Join(firstN(missing, 10), ", "))}
	}
	now := s.d.Now().UnixMilli()
	if _, err := q.ExecContext(ctx, `UPDATE ssh_user_cas SET state = 'previous' WHERE state = 'active'`); err != nil {
		return nil, err
	}
	if _, err := q.ExecContext(ctx, `UPDATE ssh_user_cas SET state = 'active', activated_at = ? WHERE id = ?`, now, caID); err != nil {
		return nil, err
	}
	c, err = s.get(ctx, q, `id = ?`, caID)
	if err != nil {
		return nil, err
	}
	return &c.CA, nil
}

func firstN(s []string, n int) []string {
	if len(s) > n {
		return append(append([]string{}, s[:n]...), "…")
	}
	return s
}

// RetireTx destroys a CA's private key (previous, or an abandoned next).
// The active CA is never retired: that could leave hosts in certificate
// mode with no way in. Hosts keep listing its public key until the trust
// step runs again (it then lists only the remaining authorities).
func (s *Service) RetireTx(ctx context.Context, q db.DBTX, caID string) (*CA, error) {
	c, err := s.get(ctx, q, `id = ?`, caID)
	if err != nil {
		return nil, err
	}
	switch c.State {
	case StateActive:
		return nil, &StateError{"the active authority can't be retired: hosts in certificate mode depend on it. Rotate to a new one first"}
	case StateRetired:
		return nil, &StateError{"this authority is already retired"}
	}
	now := s.d.Now().UnixMilli()
	if _, err := q.ExecContext(ctx, `UPDATE ssh_user_cas SET state = 'retired', retired_at = ?, key_ciphertext = NULL, key_nonce = NULL,
		key_version = NULL WHERE id = ?`, now, caID); err != nil {
		return nil, err
	}
	c, err = s.get(ctx, q, `id = ?`, caID)
	if err != nil {
		return nil, err
	}
	return &c.CA, nil
}

// TrustedKeys are the public keys hosts should trust: every authority that
// isn't retired (the active one and, during a rotation, the other).
func (s *Service) TrustedKeys(ctx context.Context) ([]CA, error) {
	all, err := s.List(ctx)
	if err != nil {
		return nil, err
	}
	var out []CA
	for _, c := range all {
		if c.State != StateRetired {
			out = append(out, c)
		}
	}
	return out, nil
}

// --- issuance ---

// Issued describes one certificate (for audit and tests).
type Issued struct {
	Serial      uint64
	KeyID       string
	Principal   string
	ValidAfter  time.Time
	ValidBefore time.Time
	CA          string // fingerprint
}

// principal is the name a host's certificate carries: ssh_principal, or
// the SSH user.
func principal(h db.Host) string {
	if h.SshPrincipal != "" {
		return h.SshPrincipal
	}
	return h.SshUser
}

// jumpHost reports whether other hosts reach their targets through h
// (its certificate then allows port forwarding).
func jumpHost(ctx context.Context, q db.DBTX, h db.Host) bool {
	if h.JumpOnly != 0 {
		return true
	}
	var n int
	q.QueryRowContext(ctx, `SELECT count(*) FROM host_hops WHERE via_host_id = ?`, h.ID).Scan(&n)
	return n > 0
}

// Issue mints a certificate from caID (the active CA when "") for one
// connection to h: a fresh Ed25519 key, the CA's signature over it, the
// host's principal, valid from ClockSkew ago for TTL. The issuance is
// audited (never the key); the private key is only in the returned signer.
func (s *Service) Issue(ctx context.Context, h db.Host, caID string) (ssh.Signer, *Issued, error) {
	where, arg := `state = 'active'`, any(nil)
	if caID != "" {
		where, arg = `id = ?`, caID
	}
	var args []any
	if arg != nil {
		args = append(args, arg)
	}
	c, err := s.get(ctx, s.d.Store.Reader, where, args...)
	if errors.Is(err, ErrNoCA) && caID == "" {
		return nil, nil, ErrNoActiveCA
	}
	if err != nil {
		return nil, nil, err
	}
	if !c.HasKey || c.State == StateRetired {
		return nil, nil, fmt.Errorf("the SSH certificate authority %s has no private key (retired, or discarded during a recovery)", c.Name)
	}
	seed, err := s.d.Box.Open(c.ID, sealKind, c.ct, c.nonce, int(c.ver.Int64))
	if err != nil {
		return nil, nil, fmt.Errorf("the SSH certificate authority's key can't be decrypted: %w", err)
	}
	caKey := ed25519.NewKeyFromSeed(seed)
	clear(seed)
	defer clear(caKey)
	caSigner, err := ssh.NewSignerFromKey(caKey)
	if err != nil {
		return nil, nil, err
	}
	_, ephemeral, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	keySigner, err := ssh.NewSignerFromKey(ephemeral)
	if err != nil {
		return nil, nil, err
	}

	now := s.d.Now()
	iss := &Issued{Principal: principal(h), ValidAfter: now.Add(-s.d.Settings.ClockSkew).Truncate(time.Second),
		ValidBefore: now.Add(s.d.Settings.TTL).Truncate(time.Second), CA: c.Fingerprint}
	perms := ssh.Permissions{Extensions: map[string]string{}}
	if h.SshNeedsPty != 0 {
		perms.Extensions["permit-pty"] = ""
	}
	if jumpHost(ctx, s.d.Store.Reader, h) {
		perms.Extensions["permit-port-forwarding"] = ""
	}
	info := driver.StepInfoFrom(ctx)
	purpose := "background"
	switch {
	case info.JobID != "":
		purpose = "job"
	case audit.ActorFrom(ctx).Type == audit.ActorUser || audit.ActorFrom(ctx).Type == audit.ActorToken:
		purpose = "request"
	}
	// The serial and the audit record commit together: every issued
	// certificate is accounted for, even if the connection then fails.
	err = s.d.Store.InRawTx(ctx, func(tx *sql.Tx) error {
		var serial int64
		if err := tx.QueryRowContext(ctx, `UPDATE ssh_user_cas SET next_serial = next_serial + 1
			WHERE id = ? AND key_ciphertext IS NOT NULL RETURNING next_serial - 1`, c.ID).Scan(&serial); err != nil {
			return fmt.Errorf("allocate a certificate serial: %w", err)
		}
		iss.Serial = uint64(serial)
		iss.KeyID = fmt.Sprintf("drool:%s:%s:%d", s.d.Controller, h.Name, serial)
		ev := audit.Event{Action: "ssh.certificate.issue", TargetType: "host", TargetID: h.ID,
			Details: map[string]any{"host": h.Name, "principal": iss.Principal, "ca": c.Fingerprint, "ca_id": c.ID, "serial": serial,
				"key_id": iss.KeyID, "valid_after": iss.ValidAfter.UTC().Format(time.RFC3339), "valid_before": iss.ValidBefore.UTC().Format(time.RFC3339),
				"purpose": purpose, "job_id": info.JobID}}
		if audit.ActorFrom(ctx).Type == audit.ActorAnonymous {
			ev.Actor = &audit.System
		}
		_, err := s.d.Audit.RecordTx(ctx, db.New(tx), ev)
		return err
	})
	if err != nil {
		return nil, nil, err
	}
	cert := &ssh.Certificate{
		Key:             keySigner.PublicKey(),
		Serial:          iss.Serial,
		CertType:        ssh.UserCert,
		KeyId:           iss.KeyID,
		ValidPrincipals: []string{iss.Principal},
		ValidAfter:      uint64(iss.ValidAfter.Unix()),
		ValidBefore:     uint64(iss.ValidBefore.Unix()),
		Permissions:     perms,
	}
	if err := cert.SignCert(rand.Reader, caSigner); err != nil {
		return nil, nil, err
	}
	signer, err := ssh.NewCertSigner(cert, keySigner)
	if err != nil {
		return nil, nil, err
	}
	return signer, iss, nil
}
