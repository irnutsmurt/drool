package backup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"drool/internal/agentsvc"
	"drool/internal/artifacts"
	"drool/internal/config"
	"drool/internal/secrets"
	"drool/internal/store"
	"drool/internal/store/db"
)

// controller creates a data directory with a migrated database, a master
// key, one credential, a host using it, a pinned key, a session, an audit
// event, and self-signed TLS files.
func controller(t *testing.T) *config.Loaded {
	t.Helper()
	cfg := &config.Loaded{Config: config.Defaults()}
	cfg.DataDir = t.TempDir()
	ctx := context.Background()
	st, err := store.Open(filepath.Join(cfg.DataDir, store.FileName))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	box, _, _, err := secrets.LoadOrCreateBox(cfg.MasterKeyPath())
	if err != nil {
		t.Fatal(err)
	}
	svc := secrets.NewService(st, box)
	p, err := svc.Prepare(secrets.Input{Name: "deploy-key", Kind: secrets.KindSSHKey, Generate: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.InTx(ctx, func(q *db.Queries) error { return svc.InsertTx(ctx, q, p) }); err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`INSERT INTO users (id, username, password_hash, role, created_at, updated_at, password_changed_at) VALUES ('usr_1','admin','x','admin',0,0,0)`,
		`INSERT INTO sessions (token_hash, user_id, csrf_token, created_at, last_seen_at, expires_at) VALUES (x'01','usr_1','c',0,0,9999999999999)`,
		`INSERT INTO hosts (id, name, connect_address, ssh_user, ssh_credential_id, status, created_at, updated_at) VALUES ('h1','web1','10.0.0.5','deploy','` + p.Row.ID + `','ready',0,0)`,
		`INSERT INTO groups (id, name, created_at, updated_at) VALUES ('g1','web',0,0)`,
		`INSERT INTO group_members (group_id, host_id) VALUES ('g1','h1')`,
		`INSERT INTO host_tags (host_id, tag) VALUES ('h1','role:web')`,
	} {
		if _, err := st.Writer.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	os.MkdirAll(filepath.Join(cfg.DataDir, "tls"), 0o700)
	os.WriteFile(filepath.Join(cfg.DataDir, "tls", "self-signed.crt"), []byte("CERT"), 0o600)
	os.WriteFile(filepath.Join(cfg.DataDir, "tls", "self-signed.key"), []byte("KEY"), 0o600)
	return cfg
}

func makeBackup(t *testing.T, cfg *config.Loaded, withKey bool) []byte {
	t.Helper()
	var buf bytes.Buffer
	res, err := Create(context.Background(), cfg, &buf, Options{IncludeMasterKey: withKey})
	if err != nil {
		t.Fatal(err)
	}
	if res.KeyWarning != "" {
		t.Fatalf("unexpected key warning: %s", res.KeyWarning)
	}
	return buf.Bytes()
}

// fresh returns an empty data directory for a "new machine".
func fresh(t *testing.T) *config.Loaded {
	cfg := &config.Loaded{Config: config.Defaults()}
	cfg.DataDir = t.TempDir()
	return cfg
}

func count(t *testing.T, cfg *config.Loaded, table string) int {
	t.Helper()
	st, err := store.Open(filepath.Join(cfg.DataDir, store.FileName))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	var n int
	if err := st.Reader.QueryRow(`SELECT count(*) FROM ` + table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestRoundTripWithMasterKey(t *testing.T) {
	src := controller(t)
	archive := makeBackup(t, src, true)

	dst := fresh(t)
	res, err := Restore(context.Background(), dst, bytes.NewReader(archive), RestoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res.PreviousDataDir != "" {
		t.Errorf("a fresh data directory has nothing to keep, got %s", res.PreviousDataDir)
	}
	if res.KeySource != "backup" || res.CredentialsOK != 1 || !res.TLSRestored || res.SessionsCleared != 1 {
		t.Fatalf("unexpected result: %+v", res)
	}
	for table, want := range map[string]int{"users": 1, "hosts": 1, "secrets": 1, "groups": 1, "group_members": 1, "host_tags": 1, "sessions": 0} {
		if got := count(t, dst, table); got != want {
			t.Errorf("%s: got %d rows, want %d", table, got, want)
		}
	}
	// The installed key opens the credentials.
	key, _ := os.ReadFile(dst.MasterKeyPath())
	srcKey, _ := os.ReadFile(src.MasterKeyPath())
	if !bytes.Equal(key, srcKey) {
		t.Fatal("master key not installed")
	}
	if fi, _ := os.Stat(dst.MasterKeyPath()); fi.Mode().Perm() != 0o600 {
		t.Fatalf("master key mode %v", fi.Mode().Perm())
	}
	if b, _ := os.ReadFile(filepath.Join(dst.DataDir, "tls", "self-signed.key")); string(b) != "KEY" {
		t.Fatal("TLS key not restored")
	}
	// The restore is audited in the restored database.
	if n := count(t, dst, `audit_events WHERE action = 'backup.restore'`); n != 1 {
		t.Fatalf("restore audit events: %d", n)
	}
}

func TestBackupWithoutKeyNeedsMatchingKey(t *testing.T) {
	src := controller(t)
	archive := makeBackup(t, src, false)
	ctx := context.Background()

	// A different machine with its own (different) key: refused, untouched.
	dst := fresh(t)
	if _, _, _, err := secrets.LoadOrCreateBox(dst.MasterKeyPath()); err != nil {
		t.Fatal(err)
	}
	_, err := Restore(ctx, dst, bytes.NewReader(archive), RestoreOptions{})
	var ke *KeyError
	if !errors.As(err, &ke) || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("expected KeyError, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(dst.DataDir, store.FileName)); !os.IsNotExist(err) {
		t.Fatal("database was written despite the refusal")
	}

	// Supplying the original key works and installs it.
	res, err := Restore(ctx, dst, bytes.NewReader(archive), RestoreOptions{MasterKeyFile: src.MasterKeyPath()})
	if err != nil {
		t.Fatal(err)
	}
	if res.KeySource != "file" || res.CredentialsOK != 1 {
		t.Fatalf("unexpected result: %+v", res)
	}
	if _, err := os.Stat(filepath.Join(res.PreviousDataDir, "master.key")); err != nil {
		t.Fatal("the replaced master key was not kept")
	}

	// Restoring on the original machine finds the existing key.
	res, err = Restore(ctx, src, bytes.NewReader(archive), RestoreOptions{})
	if err != nil || res.KeySource != "existing" {
		t.Fatalf("restore in place: %+v, %v", res, err)
	}
}

func TestDiscardCredentials(t *testing.T) {
	src := controller(t)
	archive := makeBackup(t, src, false)
	dst := fresh(t)
	res, err := Restore(context.Background(), dst, bytes.NewReader(archive), RestoreOptions{DiscardCredentials: true})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Discarded || len(res.DiscardedFor) != 1 || res.DiscardedFor[0] != "web1" {
		t.Fatalf("unexpected result: %+v", res)
	}
	if count(t, dst, "secrets") != 0 || count(t, dst, "hosts WHERE ssh_credential_id IS NULL AND status = 'untested'") != 1 {
		t.Fatal("credentials not discarded cleanly")
	}
	if count(t, dst, "users") != 1 || count(t, dst, "group_members") != 1 {
		t.Fatal("non-credential data lost")
	}
}

// rewrite rebuilds an archive, letting edit change, drop (return nil) or
// keep each member.
func rewrite(t *testing.T, archive []byte, edit func(name string, data []byte) []byte, extra map[string][]byte) []byte {
	t.Helper()
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	var out bytes.Buffer
	gw := gzip.NewWriter(&out)
	tw := tar.NewWriter(gw)
	put := func(name string, data []byte) {
		tw.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(data)), Typeflag: tar.TypeReg})
		tw.Write(data)
	}
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		data, _ := io.ReadAll(tr)
		if d := edit(h.Name, data); d != nil {
			put(h.Name, d)
		}
	}
	for n, d := range extra {
		put(n, d)
	}
	tw.Close()
	gw.Close()
	return out.Bytes()
}

func TestRejectsBadArchivesWithoutTouchingInstallation(t *testing.T) {
	src := controller(t)
	good := makeBackup(t, src, true)
	keep := func(_ string, d []byte) []byte { return d }
	cases := map[string]struct {
		archive []byte
		want    string
	}{
		"not gzip":    {[]byte("hello"), "not a gzip"},
		"truncated":   {good[:len(good)/2], "truncated or corrupt"},
		"no manifest": {rewrite(t, good, func(n string, d []byte) []byte { return map[bool][]byte{true: nil, false: d}[n == nameManifest] }, nil), "no manifest"},
		"missing db":  {rewrite(t, good, func(n string, d []byte) []byte { return map[bool][]byte{true: nil, false: d}[n == nameDB] }, nil), "missing from the archive"},
		"corrupt db": {rewrite(t, good, func(n string, d []byte) []byte {
			if n == nameDB {
				d = append([]byte{}, d...)
				d[200] ^= 0xff
			}
			return d
		}, nil), "checksum"},
		"unexpected": {rewrite(t, good, keep, map[string][]byte{"../../etc/passwd": []byte("x")}), "unexpected file"},
		"wrong format": {rewrite(t, good, func(n string, d []byte) []byte {
			if n == nameManifest {
				return bytes.Replace(d, []byte(`"drool-backup"`), []byte(`"other"`), 1)
			}
			return d
		}, nil), "format"},
		"newer format": {rewrite(t, good, func(n string, d []byte) []byte {
			if n == nameManifest {
				return bytes.Replace(d, []byte(`"format_version": 1`), []byte(`"format_version": 9`), 1)
			}
			return d
		}, nil), "not supported"},
		"bad json": {rewrite(t, good, func(n string, d []byte) []byte {
			if n == nameManifest {
				return []byte("{")
			}
			return d
		}, nil), "not valid JSON"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			dst := controller(t) // an existing installation that must survive
			before, _ := os.ReadFile(filepath.Join(dst.DataDir, store.FileName))
			_, err := Restore(context.Background(), dst, bytes.NewReader(c.archive), RestoreOptions{})
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("got %v, want error containing %q", err, c.want)
			}
			after, _ := os.ReadFile(filepath.Join(dst.DataDir, store.FileName))
			if !bytes.Equal(before, after) {
				t.Fatal("installation changed by a rejected restore")
			}
			entries, _ := os.ReadDir(dst.DataDir)
			for _, e := range entries {
				if strings.HasPrefix(e.Name(), ".restore-") || strings.HasPrefix(e.Name(), "pre-restore-") {
					t.Fatalf("left behind %s", e.Name())
				}
			}
		})
	}
}

func TestRefusesNewerSchema(t *testing.T) {
	src := controller(t)
	st, _ := store.Open(filepath.Join(src.DataDir, store.FileName))
	st.Writer.Exec(`INSERT INTO schema_migrations(version, name, applied_at) VALUES (999, '0999_future.sql', 0)`)
	st.Close()
	archive := makeBackup(t, src, true)
	_, err := Restore(context.Background(), fresh(t), bytes.NewReader(archive), RestoreOptions{})
	if err == nil || !strings.Contains(err.Error(), "newer DROOL") {
		t.Fatalf("got %v", err)
	}
}

func TestRefusesWhileControllerRuns(t *testing.T) {
	src := controller(t)
	archive := makeBackup(t, src, true)
	release, err := store.LockDataDir(src.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	_, err = Restore(context.Background(), src, bytes.NewReader(archive), RestoreOptions{})
	if !errors.Is(err, store.ErrLocked) {
		t.Fatalf("got %v", err)
	}
}

func TestConfigSetupSecretRedacted(t *testing.T) {
	src := controller(t)
	p := filepath.Join(t.TempDir(), "drool.yaml")
	os.WriteFile(p, []byte("server:\n  web_port: 9000\nsetup:\n  secret: hunter2-setup\n"), 0o600)
	src.FilePath = p
	archive := makeBackup(t, src, false)
	if bytes.Contains(gunzip(t, archive), []byte("hunter2-setup")) {
		t.Fatal("setup secret in backup")
	}
	dst := fresh(t)
	res, err := Restore(context.Background(), dst, bytes.NewReader(archive), RestoreOptions{MasterKeyFile: src.MasterKeyPath()})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(res.ConfigRestoredTo)
	if !strings.Contains(string(b), "web_port: 9000") {
		t.Fatalf("config not restored: %q", b)
	}
}

func TestBackupWithoutKeyHasNoKeyMaterial(t *testing.T) {
	src := controller(t)
	key, _ := os.ReadFile(src.MasterKeyPath())
	raw := gunzip(t, makeBackup(t, src, false))
	b64 := strings.TrimSpace(strings.SplitN(string(key), ":", 2)[1])
	if bytes.Contains(raw, []byte(b64)) {
		t.Fatal("master key material in a backup made without --include-master-key")
	}
}

func gunzip(t *testing.T, b []byte) []byte {
	gz, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	out, _ := io.ReadAll(gz)
	return out
}

func TestArtifactsRoundTripAndTamperDetection(t *testing.T) {
	src := controller(t)
	ctx := context.Background()
	st, _ := store.Open(filepath.Join(src.DataDir, store.FileName))
	arts, err := artifacts.Open(src.DataDir, st)
	if err != nil {
		t.Fatal(err)
	}
	a, err := arts.Put(ctx, strings.NewReader("listen 80;\n"), "nginx.conf", "")
	if err != nil {
		t.Fatal(err)
	}
	st.Writer.Exec(`INSERT INTO file_deployments (id, host_id, path, kind, outcome, before_exists, after_sha256, after_artifact, created_at)
		VALUES ('fd1','h1','/etc/nginx/nginx.conf','deploy','installed',0,?,?,0)`, a.SHA256, a.SHA256)
	st.Close()

	archive := makeBackup(t, src, true)
	if !bytes.Contains(gunzip(t, archive), []byte("artifacts/sha256/"+a.SHA256[:2]+"/"+a.SHA256)) {
		t.Fatal("artifact not in the backup")
	}
	dst := fresh(t)
	res, err := Restore(ctx, dst, bytes.NewReader(archive), RestoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Counts["artifacts"] != 1 || res.Counts["file_deployments"] != 1 {
		t.Fatalf("counts %v", res.Counts)
	}
	if b, _ := os.ReadFile(filepath.Join(dst.DataDir, artifacts.RelPath(a.SHA256))); string(b) != "listen 80;\n" {
		t.Fatalf("artifact not restored: %q", b)
	}

	// An artifact whose content doesn't match its name (and the manifest
	// updated to match) is still refused.
	evil := rewrite(t, archive, func(n string, d []byte) []byte {
		if strings.HasPrefix(n, "artifacts/") {
			return []byte("listen 6666;\n")
		}
		if n == nameManifest {
			var m Manifest
			json.Unmarshal(d, &m)
			for i := range m.Files {
				if strings.HasPrefix(m.Files[i].Name, "artifacts/") {
					sum := sha256.Sum256([]byte("listen 6666;\n"))
					m.Files[i].SHA256, m.Files[i].Size = hex.EncodeToString(sum[:]), 13
				}
			}
			out, _ := json.Marshal(m)
			return out
		}
		return d
	}, nil)
	if _, err := Restore(ctx, fresh(t), bytes.NewReader(evil), RestoreOptions{}); err == nil || !strings.Contains(err.Error(), "artifact") {
		t.Fatalf("tampered artifact accepted: %v", err)
	}
}

// TestAgentPKIBackup: Agent PKI state travels with backups (identities,
// manifests and the intermediate key, sealed with the master key), but a
// root key that was exported and removed never reappears. After a restore
// without the master key the PKI is recovered with the offline root key.
func TestAgentPKIBackup(t *testing.T) {
	src := controller(t)
	ctx := context.Background()
	st, err := store.Open(filepath.Join(src.DataDir, store.FileName))
	if err != nil {
		t.Fatal(err)
	}
	box, _, _, _ := secrets.LoadOrCreateBox(src.MasterKeyPath())
	pki, _, err := agentsvc.OpenPKI(ctx, st, box, "t")
	if err != nil {
		t.Fatal(err)
	}
	_, rootKey, _ := pki.ExportRoot()
	if err := pki.RemoveRootKey(ctx); err != nil {
		t.Fatal(err)
	}
	fp := pki.RootFingerprint()
	st.Close()

	// With the master key: everything works after the restore, and the
	// removed root key is not in it.
	dst := fresh(t)
	if _, err := Restore(ctx, dst, bytes.NewReader(makeBackup(t, src, true)), RestoreOptions{}); err != nil {
		t.Fatal(err)
	}
	if count(t, dst, "agent_ca WHERE kind = 'root' AND key_ciphertext IS NOT NULL") != 0 {
		t.Fatal("a removed root key came back through the backup")
	}
	st2, _ := store.Open(filepath.Join(dst.DataDir, store.FileName))
	box2, _, _, _ := secrets.LoadOrCreateBox(dst.MasterKeyPath())
	p2, _, err := agentsvc.OpenPKI(ctx, st2, box2, "t")
	if err != nil || p2.RootFingerprint() != fp {
		t.Fatalf("PKI after restore: %v", err)
	}
	st2.Close()

	// Without the master key (credentials discarded): the PKI is unusable
	// until a new intermediate is issued with the offline root key.
	dst3 := fresh(t)
	if _, err := Restore(ctx, dst3, bytes.NewReader(makeBackup(t, src, false)), RestoreOptions{DiscardCredentials: true}); err != nil {
		t.Fatal(err)
	}
	st3, _ := store.Open(filepath.Join(dst3.DataDir, store.FileName))
	defer st3.Close()
	box3, _, _, _ := secrets.LoadOrCreateBox(dst3.MasterKeyPath())
	if _, _, err := agentsvc.OpenPKI(ctx, st3, box3, "t"); !errors.Is(err, agentsvc.ErrPKIUnusable) {
		t.Fatalf("PKI after discarding: %v", err)
	}
	v, err := agentsvc.RecoverIntermediate(ctx, st3, box3, rootKey)
	if err != nil || v != 2 {
		t.Fatalf("recover intermediate: %d %v", v, err)
	}
	p3, _, err := agentsvc.OpenPKI(ctx, st3, box3, "t")
	if err != nil || p3.RootFingerprint() != fp || len(p3.Status().RevokedIntermediates) != 1 {
		t.Fatalf("PKI after recovery: %v %+v", err, p3)
	}
}
