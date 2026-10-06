package backup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"time"

	"drool/internal/audit"
	"drool/internal/config"
	"drool/internal/secrets"
	"drool/internal/store"
	"drool/internal/store/db"
)

func bytesReader(b []byte) io.Reader   { return bytes.NewReader(b) }
func newQueries(d *sql.DB) *db.Queries { return db.New(d) }
func cliActor() audit.Actor            { return audit.Actor{Type: audit.ActorSystem, Name: "cli:" + osUser()} }
func osUser() string {
	if u, err := user.Current(); err == nil {
		return u.Username
	}
	return fmt.Sprint(os.Getuid())
}

// InvalidError means the archive was rejected before anything was changed.
type InvalidError struct{ Reason string }

func (e *InvalidError) Error() string { return "invalid backup: " + e.Reason }

func invalid(format string, args ...any) error {
	return &InvalidError{Reason: fmt.Sprintf(format, args...)}
}

// KeyError means the backup's credentials can't be decrypted with any
// available master key. Nothing was changed.
type KeyError struct {
	Manifest *Manifest
	Detail   string
}

func (e *KeyError) Error() string { return e.Detail }

// RestoreOptions controls Restore.
type RestoreOptions struct {
	// MasterKeyFile supplies the master key for a backup made without one.
	MasterKeyFile string
	// DiscardCredentials restores without credentials when no matching
	// master key is available (recovery after losing the key).
	DiscardCredentials bool
}

// RestoreResult describes a completed restore.
type RestoreResult struct {
	Manifest         *Manifest
	Migrations       []string
	Counts           map[string]int64
	KeySource        string // "backup", "file", "existing", or "" when credentials were discarded / none
	CredentialsOK    int
	DiscardedFor     []string // hosts whose credentials were discarded
	Discarded        bool
	PreviousDataDir  string // where the replaced files were moved
	ConfigRestoredTo string
	TLSRestored      bool
	SessionsCleared  int64
}

// staged is an extracted, verified archive.
type staged struct {
	dir      string
	manifest *Manifest
	has      map[string]bool
}

// Restore replaces the controller's data with the backup read from r. The
// controller must be stopped. Everything is extracted and verified in a
// staging directory first; the live files are only touched once the
// restored database has been checked, migrated and its credentials opened,
// and a failure while swapping puts the previous files back.
func Restore(ctx context.Context, cfg *config.Loaded, r io.Reader, opts RestoreOptions) (*RestoreResult, error) {
	release, err := store.LockDataDir(cfg.DataDir)
	if err != nil {
		if errors.Is(err, store.ErrLocked) {
			return nil, fmt.Errorf("%w; stop it before restoring (systemctl stop drool, or docker compose stop drool)", err)
		}
		return nil, err
	}
	defer release()

	st, err := extract(r, cfg.DataDir)
	if st != nil {
		defer os.RemoveAll(st.dir)
	}
	if err != nil {
		return nil, err
	}
	m := st.manifest
	res := &RestoreResult{Manifest: m}

	// Database: integrity, schema, forward migrations.
	dbPath := filepath.Join(st.dir, nameDB)
	if err := checkIntegrity(ctx, dbPath); err != nil {
		return nil, err
	}
	sdb, err := store.Open(dbPath)
	if err != nil {
		return nil, invalid("database cannot be opened: %v", err)
	}
	closed := false
	closeDB := func() {
		if !closed {
			sdb.Writer.ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`)
			sdb.Close()
			closed = true
		}
	}
	defer closeDB()
	schema, err := sdb.SchemaVersion(ctx)
	if err != nil {
		return nil, invalid("not a DROOL database: %v", err)
	}
	if schema != m.SchemaVersion {
		return nil, invalid("database schema version %d does not match the manifest (%d)", schema, m.SchemaVersion)
	}
	if latest := store.LatestVersion(); schema > latest {
		return nil, invalid("made by a newer DROOL (%s, schema %d); this binary supports schema %d. Restore with that version or newer",
			m.DroolVersion, schema, latest)
	}
	if res.Migrations, err = sdb.Migrate(ctx); err != nil {
		return nil, fmt.Errorf("upgrade restored database: %w", err)
	}

	// Credentials: find a master key that opens them. Count what is really
	// in the database rather than trusting the manifest.
	var nCred int64
	if err := sdb.Reader.QueryRowContext(ctx, `SELECT count(*) FROM secrets`).Scan(&nCred); err != nil {
		return nil, invalid("database has no credentials table: %v", err)
	}
	m.Credentials = nCred
	var keyData []byte
	keyData, res.KeySource, err = chooseKey(ctx, cfg, st, sdb, opts)
	if err != nil && !(opts.DiscardCredentials && errors.As(err, new(*KeyError))) {
		return nil, err
	}
	if err != nil {
		// Recovery: keep everything but the undecryptable credentials.
		tx, err := sdb.Writer.BeginTx(ctx, nil)
		if err != nil {
			return nil, err
		}
		if res.DiscardedFor, err = secrets.DiscardAllTx(ctx, tx); err != nil {
			tx.Rollback()
			return nil, fmt.Errorf("discard credentials: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		res.Discarded, res.KeySource, keyData = true, "", nil
	} else {
		res.CredentialsOK = int(m.Credentials)
	}

	// Old sessions and setup codes must not survive into the restored
	// controller: everyone signs in again.
	if r, err := sdb.Writer.ExecContext(ctx, `DELETE FROM sessions`); err == nil {
		res.SessionsCleared, _ = r.RowsAffected()
	} else {
		return nil, err
	}
	if _, err := sdb.Writer.ExecContext(ctx, `DELETE FROM setup_codes`); err != nil {
		return nil, err
	}
	details := map[string]any{
		"backup_created_at": m.CreatedAt, "backup_version": m.DroolVersion, "backup_schema": m.SchemaVersion,
		"backup_host": m.Hostname, "migrations": res.Migrations, "key_source": res.KeySource,
		"credentials_discarded": res.Discarded,
	}
	if res.Discarded {
		details["hosts_without_credentials"] = res.DiscardedFor
	}
	actor := cliActor()
	if err := audit.NewWriter(sdb, slog.New(slog.DiscardHandler)).Record(ctx, audit.Event{
		Action: "backup.restore", Actor: &actor, TargetType: "controller", Details: details,
	}); err != nil {
		return nil, fmt.Errorf("record restore in audit log: %w", err)
	}
	var counts map[string]int64
	if _, counts, err = describe(ctx, sdb.Reader); err != nil {
		return nil, err
	}
	res.Counts = counts
	closeDB()
	for _, suffix := range []string{"-wal", "-shm"} {
		if fileExists(dbPath + suffix) {
			return nil, fmt.Errorf("restored database was not closed cleanly (%s remains)", filepath.Base(dbPath+suffix))
		}
	}

	if err := swap(cfg, st, keyData, res); err != nil {
		return nil, err
	}
	return res, nil
}

// extract reads the archive into a new staging directory inside dataDir and
// verifies it against its manifest.
func extract(r io.Reader, dataDir string) (*staged, error) {
	dir, err := os.MkdirTemp(dataDir, ".restore-")
	if err != nil {
		return nil, fmt.Errorf("create staging directory: %w", err)
	}
	st := &staged{dir: dir, has: map[string]bool{}}
	gz, err := gzip.NewReader(r)
	if err != nil {
		return st, invalid("not a gzip-compressed DROOL backup (%v)", err)
	}
	tr := tar.NewReader(gz)
	sums := map[string]FileEntry{}
	var manifestData []byte
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return st, invalid("archive is truncated or corrupt (%v)", err)
		}
		name := h.Name
		if !knownNames[name] {
			return st, invalid("unexpected file %q in archive", name)
		}
		if h.Typeflag != tar.TypeReg {
			return st, invalid("%s is not a regular file", name)
		}
		if st.has[name] {
			return st, invalid("%s appears twice", name)
		}
		if name != nameDB && h.Size > maxSmallFile {
			return st, invalid("%s is unexpectedly large (%d bytes)", name, h.Size)
		}
		st.has[name] = true
		if name == nameManifest {
			if manifestData, err = io.ReadAll(io.LimitReader(tr, maxSmallFile)); err != nil {
				return st, invalid("read manifest: %v", err)
			}
			continue
		}
		dst := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
			return st, err
		}
		f, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return st, err
		}
		hsh := sha256.New()
		n, err := io.Copy(io.MultiWriter(f, hsh), tr)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return st, invalid("archive is truncated or corrupt while reading %s (%v)", name, err)
		}
		sums[name] = FileEntry{Name: name, Size: n, SHA256: hex.EncodeToString(hsh.Sum(nil))}
	}
	if manifestData == nil {
		return st, invalid("no manifest.json; this is not a DROOL backup")
	}
	var m Manifest
	if err := json.Unmarshal(manifestData, &m); err != nil {
		return st, invalid("manifest is not valid JSON (%v)", err)
	}
	if m.Format != Format {
		return st, invalid("manifest format is %q, not %q", m.Format, Format)
	}
	if m.FormatVersion != FormatVersion {
		return st, invalid("backup format version %d is not supported by this DROOL (supports %d)", m.FormatVersion, FormatVersion)
	}
	listed := map[string]bool{}
	for _, f := range m.Files {
		listed[f.Name] = true
		got, ok := sums[f.Name]
		if !ok {
			return st, invalid("%s is listed in the manifest but missing from the archive (incomplete backup?)", f.Name)
		}
		if got.Size != f.Size || got.SHA256 != f.SHA256 {
			return st, invalid("%s does not match its checksum; the backup is corrupt", f.Name)
		}
	}
	for name := range sums {
		if !listed[name] {
			return st, invalid("%s is in the archive but not in the manifest", name)
		}
	}
	if !st.has[nameDB] {
		return st, invalid("no database in the archive")
	}
	if st.has[nameKey] != m.IncludesMasterKey {
		return st, invalid("manifest and archive disagree about whether the master key is included")
	}
	if st.has[nameTLSCert] != st.has[nameTLSKey] {
		return st, invalid("TLS certificate and key must both be present")
	}
	st.manifest = &m
	return st, nil
}

func checkIntegrity(ctx context.Context, path string) error {
	d, err := openReadOnly(path)
	if err != nil {
		return invalid("database cannot be opened: %v", err)
	}
	defer d.Close()
	var res string
	if err := d.QueryRowContext(ctx, `PRAGMA integrity_check`).Scan(&res); err != nil {
		return invalid("database is not a valid SQLite file (%v)", err)
	}
	if res != "ok" {
		return invalid("database failed its integrity check: %s", res)
	}
	return nil
}

// chooseKey finds the master key for the restored credentials: the one in
// the backup, then --master-key, then the key already installed here.
func chooseKey(ctx context.Context, cfg *config.Loaded, st *staged, sdb *store.Store, opts RestoreOptions) ([]byte, string, error) {
	m := st.manifest
	type cand struct {
		source, label string
		data          []byte
	}
	var cands []cand
	if st.has[nameKey] {
		data, err := os.ReadFile(filepath.Join(st.dir, nameKey))
		if err != nil {
			return nil, "", err
		}
		cands = append(cands, cand{"backup", "the master key in the backup", data})
	}
	if opts.MasterKeyFile != "" {
		data, err := os.ReadFile(opts.MasterKeyFile)
		if errors.Is(err, os.ErrPermission) {
			return nil, "", fmt.Errorf("--master-key: %w (the file must be readable by the user running drool restore; "+
				"in Docker that is the container's unprivileged user, so make it readable for the restore, e.g. chmod 644)", err)
		}
		if err != nil {
			return nil, "", fmt.Errorf("--master-key: %w", err)
		}
		cands = append(cands, cand{"file", opts.MasterKeyFile, data})
	}
	existing, existErr := os.ReadFile(cfg.MasterKeyPath())
	if existErr == nil {
		cands = append(cands, cand{"existing", "the existing key at " + cfg.MasterKeyPath(), existing})
	}

	if m.Credentials == 0 {
		// Nothing to decrypt; install the backup's key if it has one, else keep what's here.
		if len(cands) > 0 && cands[0].source != "existing" {
			return cands[0].data, cands[0].source, nil
		}
		return nil, "existing", nil
	}
	var tried []string
	for _, c := range cands {
		box, err := secrets.ParseKeyFile(c.data)
		if err != nil {
			if c.source == "file" {
				return nil, "", fmt.Errorf("--master-key %s: %w", c.label, err)
			}
			tried = append(tried, c.label+" (unreadable)")
			continue
		}
		if _, err := secrets.VerifyBox(ctx, sdb.R, box); err != nil {
			if c.source == "backup" {
				return nil, "", invalid("the master key in the backup does not open its credentials (%v)", err)
			}
			tried = append(tried, fmt.Sprintf("%s (key id %s, does not match)", c.label, box.KeyID()))
			continue
		}
		if c.source == "existing" {
			return nil, c.source, nil // already in place
		}
		return c.data, c.source, nil
	}
	detail := fmt.Sprintf("this backup has %d stored credentials (SSH keys, passwords) encrypted with master key id %s, "+
		"and the backup does not include that key", m.Credentials, orUnknown(m.MasterKeyID))
	if len(tried) > 0 {
		detail += "; tried " + strings.Join(tried, ", ")
	} else {
		detail += "; no master key is installed here"
	}
	return nil, "", &KeyError{Manifest: m, Detail: detail}
}

func orUnknown(s string) string {
	if s == "" {
		return "(unknown)"
	}
	return s
}

// swap moves the current files aside and the restored ones into place,
// undoing everything on failure.
func swap(cfg *config.Loaded, st *staged, keyData []byte, res *RestoreResult) (err error) {
	ts := time.Now().UTC().Format("20060102-150405")
	prev := filepath.Join(cfg.DataDir, "pre-restore-"+ts)
	if err := os.Mkdir(prev, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", prev, err)
	}
	type move struct{ from, to string }
	var done []move
	mv := func(from, to string) error {
		if err := os.Rename(from, to); err != nil {
			return err
		}
		done = append(done, move{from, to})
		return nil
	}
	var keyBackup string
	keyInstalled := false
	defer func() {
		if err == nil {
			return
		}
		for i := len(done) - 1; i >= 0; i-- {
			if rerr := os.Rename(done[i].to, done[i].from); rerr != nil {
				err = fmt.Errorf("%w; ALSO FAILED to put %s back from %s: %v", err, done[i].from, done[i].to, rerr)
			}
		}
		if keyInstalled && keyBackup != "" {
			if data, rerr := os.ReadFile(keyBackup); rerr == nil {
				if werr := writeFileAtomic(cfg.MasterKeyPath(), data); werr != nil {
					err = fmt.Errorf("%w; ALSO FAILED to put the master key back from %s: %v", err, keyBackup, werr)
				}
			}
		} else if keyInstalled {
			os.Remove(cfg.MasterKeyPath())
		}
		err = fmt.Errorf("restore failed and was rolled back; the previous installation is unchanged: %w", err)
	}()

	live := filepath.Join(cfg.DataDir, store.FileName)
	for _, name := range []string{store.FileName, store.FileName + "-wal", store.FileName + "-shm"} {
		p := filepath.Join(cfg.DataDir, name)
		if fileExists(p) {
			if err := mv(p, filepath.Join(prev, name)); err != nil {
				return err
			}
		}
	}
	if st.has[nameTLSCert] {
		tlsDir := filepath.Join(cfg.DataDir, "tls")
		if _, err := os.Stat(tlsDir); err == nil {
			if err := mv(tlsDir, filepath.Join(prev, "tls")); err != nil {
				return err
			}
		}
		if err := mv(filepath.Join(st.dir, "tls"), tlsDir); err != nil {
			return err
		}
		res.TLSRestored = true
	}
	if keyData != nil {
		kp := cfg.MasterKeyPath()
		if old, rerr := os.ReadFile(kp); rerr == nil {
			keyBackup = filepath.Join(prev, "master.key")
			if err := os.WriteFile(keyBackup, old, 0o600); err != nil {
				return err
			}
		}
		if err := writeFileAtomic(kp, keyData); err != nil {
			return fmt.Errorf("install master key: %w", err)
		}
		keyInstalled = true
	}
	if err := mv(filepath.Join(st.dir, nameDB), live); err != nil {
		return err
	}
	if st.has[nameConfig] {
		dst := filepath.Join(cfg.DataDir, "drool.yaml.from-backup")
		data, rerr := os.ReadFile(filepath.Join(st.dir, filepath.FromSlash(nameConfig)))
		if rerr == nil && os.WriteFile(dst, data, 0o600) == nil {
			res.ConfigRestoredTo = dst
		}
	}
	keptOld := keyBackup != ""
	for _, m := range done {
		keptOld = keptOld || strings.HasPrefix(m.to, prev+string(filepath.Separator))
	}
	if keptOld {
		res.PreviousDataDir = prev
	} else {
		os.Remove(prev) // nothing was replaced (fresh data directory)
	}
	return nil
}

func writeFileAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".tmp-"+filepath.Base(path)+"-")
	if err != nil {
		return err
	}
	tmp := f.Name()
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}
