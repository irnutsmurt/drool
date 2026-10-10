package backup

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"drool/internal/audit"
	"drool/internal/config"
	"drool/internal/secrets"
	"drool/internal/store"
)

// Backup and restore torture tests (M16, ARCHITECTURE-ALPHA §33.6).

// installation fingerprints a data directory: every regular file's
// content, ignoring what a restore is expected to add beside it.
func installation(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		if d.IsDir() {
			if strings.HasPrefix(rel, "pre-restore-") || strings.HasPrefix(rel, "backups") {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(rel, ".lock") || strings.HasSuffix(rel, "-shm") || strings.HasSuffix(rel, "-wal") {
			return nil
		}
		b, _ := os.ReadFile(p)
		sum := sha256.Sum256(b)
		out[rel] = hex.EncodeToString(sum[:8])
		return nil
	})
	return out
}

func sameInstallation(t *testing.T, before, after map[string]string) {
	t.Helper()
	for k, v := range before {
		if after[k] != v {
			t.Errorf("%s changed (%s → %s)", k, v, after[k])
		}
	}
	for k := range after {
		if _, ok := before[k]; !ok {
			t.Errorf("%s appeared", k)
		}
	}
}

// TestInterruptedRestoreAtEveryStep stops a restore after each file move
// as if the process had died there: DROOL then refuses the data directory,
// and drool restore --recover puts the previous installation back exactly.
func TestInterruptedRestoreAtEveryStep(t *testing.T) {
	src := controller(t)
	// A different installation with its own key: the restore replaces the
	// database, the TLS files and the master key.
	other := controller(t)
	archive := makeBackup(t, src, true)
	ctx := context.Background()
	for k := 1; ; k++ {
		dst := &config.Loaded{Config: config.Defaults()}
		dst.DataDir = t.TempDir()
		copyDir(t, other.DataDir, dst.DataDir)
		before := installation(t, dst.DataDir)

		testCrashAfterMove = k
		_, err := Restore(ctx, dst, bytes.NewReader(archive), RestoreOptions{})
		testCrashAfterMove = 0
		if err == nil {
			if k < 3 {
				t.Fatalf("only %d moves?", k-1)
			}
			break // k is past the last move: the restore completed
		}
		if !errors.Is(err, errSimulatedCrash) {
			t.Fatalf("move %d: %v", k, err)
		}
		if err := CheckJournal(dst.DataDir); !errors.Is(err, ErrInterruptedRestore) || !strings.Contains(err.Error(), "--recover") {
			t.Fatalf("move %d: a half-restored data directory isn't refused: %v", k, err)
		}
		if _, err := Restore(ctx, dst, bytes.NewReader(archive), RestoreOptions{}); !errors.Is(err, ErrInterruptedRestore) {
			t.Fatalf("move %d: a second restore over the interrupted one: %v", k, err)
		}
		res, err := Recover(dst)
		if err != nil {
			t.Fatalf("move %d: recover: %v", k, err)
		}
		if res.Completed || res.RolledBack != k {
			t.Fatalf("move %d: %+v", k, res)
		}
		sameInstallation(t, before, installation(t, dst.DataDir))
		if err := CheckJournal(dst.DataDir); err != nil {
			t.Fatalf("move %d: journal left: %v", k, err)
		}
		// And the recovered installation still opens with its own key.
		st, _ := store.Open(filepath.Join(dst.DataDir, store.FileName))
		box, _, _, _ := secrets.LoadOrCreateBox(dst.MasterKeyPath())
		if _, err := secrets.VerifyBox(ctx, st.Reader, box); err != nil {
			t.Fatalf("move %d: the recovered installation's credentials: %v", k, err)
		}
		st.Close()
	}
}

// TestRecoverFinishesACompletedRestore: a crash after the last move, while
// the journal said the restore was complete, keeps the restored data.
func TestRecoverFinishesACompletedRestore(t *testing.T) {
	src := controller(t)
	dst := fresh(t)
	if _, err := Restore(context.Background(), dst, bytes.NewReader(makeBackup(t, src, true)), RestoreOptions{}); err != nil {
		t.Fatal(err)
	}
	restored := installation(t, dst.DataDir)
	j := &journal{Version: 1, Started: time.Now(), Committed: true, PrevDir: filepath.Join(dst.DataDir, "pre-restore-x"),
		Moves: []journalMove{{From: filepath.Join(dst.DataDir, ".restore-1", "drool.db"), To: filepath.Join(dst.DataDir, store.FileName)}}}
	if err := j.save(dst.DataDir); err != nil {
		t.Fatal(err)
	}
	res, err := Recover(dst)
	if err != nil || !res.Completed {
		t.Fatalf("recover: %+v %v", res, err)
	}
	after := installation(t, dst.DataDir)
	delete(restored, JournalName)
	sameInstallation(t, restored, after)
}

func copyDir(t *testing.T, from, to string) {
	t.Helper()
	filepath.WalkDir(from, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(from, p)
		dst := filepath.Join(to, rel)
		if d.IsDir() {
			return os.MkdirAll(dst, 0o700)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(dst, b, 0o600)
	})
}

// TestTruncatedEverywhere: an archive cut anywhere is refused and changes
// nothing.
func TestTruncatedEverywhere(t *testing.T) {
	src := controller(t)
	good := makeBackup(t, src, true)
	dst := controller(t)
	before := installation(t, dst.DataDir)
	for _, cut := range []int{10, 100, len(good) / 4, len(good) / 2, len(good) * 3 / 4, len(good) - 30, len(good) - 1} {
		_, err := Restore(context.Background(), dst, bytes.NewReader(good[:cut]), RestoreOptions{})
		var inv *InvalidError
		if !errors.As(err, &inv) {
			t.Errorf("cut at %d of %d: %v", cut, len(good), err)
		}
	}
	sameInstallation(t, before, installation(t, dst.DataDir))
}

type failingWriter struct{ left int }

func (w *failingWriter) Write(p []byte) (int, error) {
	if len(p) > w.left {
		n := w.left
		w.left = 0
		return n, errors.New("no space left on device")
	}
	w.left -= len(p)
	return len(p), nil
}

// TestBackupWriteFailure: a backup that can't be written fails loudly and
// never leaves a file under the backup's name.
func TestBackupWriteFailure(t *testing.T) {
	src := controller(t)
	for _, after := range []int{0, 100, 4096} {
		if _, err := Create(context.Background(), src, &failingWriter{left: after}, Options{}); err == nil || !strings.Contains(err.Error(), "no space") {
			t.Errorf("write failing after %d bytes: %v", after, err)
		}
	}
	// A real full disk: the file size limit (RLIMIT_FSIZE) of a separate
	// process, so this test process isn't limited.
	if os.Getenv("DROOL_TEST_BACKUP_FSIZE") != "" {
		return
	}
	dest := filepath.Join(t.TempDir(), "out", "backup.tar.gz")
	cmd := exec.Command(os.Args[0], "-test.run=^TestBackupFileSizeLimitChild$")
	cmd.Env = append(os.Environ(), "DROOL_TEST_BACKUP_FSIZE="+src.DataDir, "DROOL_TEST_BACKUP_DEST="+dest)
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "BACKUP FAILED AS EXPECTED") {
		t.Fatalf("child: %v\n%s", err, out)
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Fatal("a failed backup left a file under the backup's name")
	}
	entries, _ := os.ReadDir(filepath.Dir(dest))
	for _, e := range entries {
		t.Errorf("left behind: %s", e.Name())
	}
}

func TestBackupFileSizeLimitChild(t *testing.T) {
	dataDir := os.Getenv("DROOL_TEST_BACKUP_FSIZE")
	if dataDir == "" {
		t.Skip("child half of TestBackupWriteFailure")
	}
	signal.Ignore(syscall.SIGXFSZ) // writes past the limit fail with EFBIG instead
	// Random (incompressible) data: the database snapshot stays under the
	// limit, the archive with the TLS file doesn't.
	noise := make([]byte, 1536<<10)
	rand.Read(noise)
	os.WriteFile(filepath.Join(dataDir, "tls", "self-signed.key"), noise, 0o600)
	lim := &syscall.Rlimit{Cur: 1 << 20, Max: 1 << 20}
	if err := syscall.Setrlimit(syscall.RLIMIT_FSIZE, lim); err != nil {
		t.Skip("can't limit the file size here:", err)
	}
	cfg := &config.Loaded{Config: config.Defaults()}
	cfg.DataDir = dataDir
	_, err := CreateFile(context.Background(), cfg, os.Getenv("DROOL_TEST_BACKUP_DEST"), Options{})
	if err == nil {
		t.Fatal("the backup succeeded past the file size limit")
	}
	fmt.Println("BACKUP FAILED AS EXPECTED:", err)
}

// TestOldSchemaBackupsRestore: backups made at older schema versions
// restore, upgrade on the staged copy and come out prepared for
// reconciliation.
func TestOldSchemaBackupsRestore(t *testing.T) {
	ctx := context.Background()
	for _, schema := range []int{5, 8, 12, 15, 18} {
		t.Run(fmt.Sprint("schema ", schema), func(t *testing.T) {
			cfg := &config.Loaded{Config: config.Defaults()}
			cfg.DataDir = t.TempDir()
			st, err := store.Open(filepath.Join(cfg.DataDir, store.FileName))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := st.MigrateTo(ctx, schema); err != nil {
				t.Fatal(err)
			}
			for _, q := range []string{
				`INSERT INTO users (id, username, password_hash, role, created_at, updated_at, password_changed_at) VALUES ('usr_1','admin','x','admin',0,0,0)`,
				`INSERT INTO hosts (id, name, connect_address, ssh_user, status, created_at, updated_at) VALUES ('h1','web1','10.0.0.5','deploy','ready',0,0)`,
				`INSERT INTO jobs (id, name, spec_json, spec_version, status, created_by, created_at) VALUES ('job_1','patch','{}',1,'running','usr_1',1)`,
				`INSERT INTO job_hosts (job_id, host_id, host_name, position, state) VALUES ('job_1','h1','web1',0,'running')`,
			} {
				if _, err := st.Writer.Exec(q); err != nil {
					t.Fatal(err)
				}
			}
			st.Close()
			var buf bytes.Buffer
			res, err := Create(ctx, cfg, &buf, Options{})
			if err != nil {
				t.Fatal(err)
			}
			if res.Manifest.SchemaVersion != schema || res.KeyWarning != "" {
				t.Fatalf("manifest %d, key warning %q", res.Manifest.SchemaVersion, res.KeyWarning)
			}
			dst := fresh(t)
			rr, err := Restore(ctx, dst, &buf, RestoreOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if len(rr.Migrations) != store.LatestVersion()-schema || rr.Reconcile == nil || rr.Reconcile.JobsInterrupted != 1 || rr.Reconcile.Hosts != 1 {
				t.Fatalf("restore: migrations %v, reconcile %+v", rr.Migrations, rr.Reconcile)
			}
			if count(t, dst, "users") != 1 || count(t, dst, "host_reconciliation") != 1 {
				t.Fatal("data lost in the upgrade")
			}
		})
	}
}

// TestPreUpgradeBackup: the automatic backup before a schema upgrade, and
// only the newest three are kept.
func TestPreUpgradeBackup(t *testing.T) {
	src := controller(t)
	ctx := context.Background()
	var paths []string
	for i := 0; i < 5; i++ {
		p, res, err := PreUpgrade(ctx, src, 18)
		if err != nil {
			t.Fatal(err)
		}
		if !res.Manifest.KeyVerified || res.Manifest.IncludesMasterKey {
			t.Fatalf("pre-upgrade manifest: %+v", res.Manifest)
		}
		paths = append(paths, p)
		time.Sleep(1100 * time.Millisecond) // the name carries the second
	}
	list := PreUpgradeBackups(src.DataDir)
	if len(list) != 3 || list[0] != paths[4] || list[2] != paths[2] {
		t.Fatalf("kept %v", list)
	}
	f, _ := os.Open(list[0])
	defer f.Close()
	dst := fresh(t)
	if _, err := Restore(ctx, dst, f, RestoreOptions{MasterKeyFile: src.MasterKeyPath()}); err != nil {
		t.Fatalf("restore the pre-upgrade backup: %v", err)
	}
}

// TestManifestRecordsEpochAndKeyCheck (M16).
func TestManifestRecordsEpochAndKeyCheck(t *testing.T) {
	src := controller(t)
	st, _ := store.Open(filepath.Join(src.DataDir, store.FileName))
	st.Writer.Exec(`INSERT INTO settings (key, value_json, updated_at) VALUES ('controller_epoch', '"e1"', 0)`)
	st.Close()
	var buf bytes.Buffer
	res, err := Create(context.Background(), src, &buf, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Manifest.StateEpoch == nil || *res.Manifest.StateEpoch != "e1" || !res.Manifest.KeyVerified {
		t.Fatalf("manifest: %+v", res.Manifest)
	}
	// A key that doesn't open the credentials is never recorded as checked.
	os.Remove(src.MasterKeyPath())
	secrets.LoadOrCreateBox(src.MasterKeyPath())
	res, err = Create(context.Background(), src, io.Discard, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Manifest.KeyVerified || res.KeyWarning == "" {
		t.Fatalf("a wrong key recorded as verified: %+v", res.Manifest)
	}
}

// TestVerify: drool backup verify's verdicts, and that it changes nothing.
func TestVerify(t *testing.T) {
	src := controller(t)
	ctx := context.Background()
	st, _ := store.Open(filepath.Join(src.DataDir, store.FileName))
	w := audit.NewWriter(st, slog.New(slog.DiscardHandler))
	for _, a := range []string{"host.create", "job.create", "credential.create"} {
		if err := w.Record(ctx, audit.Event{Action: a, Actor: &audit.System}); err != nil {
			t.Fatal(err)
		}
	}
	st.Close()
	before := installation(t, src.DataDir)
	full := makeBackup(t, src, true)
	noKey := makeBackup(t, src, false)

	rep, err := Verify(ctx, bytes.NewReader(full), VerifyOptions{})
	if err != nil || !rep.Complete() || rep.KeySource != "backup" || rep.Sealed == 0 || rep.Audit == nil || !rep.Audit.OK || rep.Audit.Records < 3 {
		t.Fatalf("complete backup: %+v %+v %v", rep, rep.Audit, err)
	}
	rep, err = Verify(ctx, bytes.NewReader(noKey), VerifyOptions{})
	if err != nil || !rep.Restorable() || rep.Complete() || !strings.Contains(strings.Join(rep.Warnings, " "), "--master-key") {
		t.Fatalf("backup without its key: %+v %v", rep, err)
	}
	rep, _ = Verify(ctx, bytes.NewReader(noKey), VerifyOptions{MasterKeyFile: src.MasterKeyPath()})
	if !rep.Complete() || rep.KeySource != "file" {
		t.Fatalf("with the key file: %+v", rep)
	}
	other := filepath.Join(t.TempDir(), "other.key")
	secrets.LoadOrCreateBox(other)
	rep, _ = Verify(ctx, bytes.NewReader(noKey), VerifyOptions{MasterKeyFile: other})
	if rep.Complete() || !strings.Contains(strings.Join(rep.Warnings, " "), "doesn't open") {
		t.Fatalf("with another key: %+v", rep)
	}
	rep, _ = Verify(ctx, bytes.NewReader(full[:len(full)-1]), VerifyOptions{})
	if rep.Restorable() {
		t.Fatal("a cut archive passed")
	}
	// A record changed in the backup's audit history: still restorable,
	// but said loudly.
	tampered := rewrite(t, full, func(n string, d []byte) []byte {
		if n != nameDB {
			return d
		}
		p := filepath.Join(t.TempDir(), "db")
		os.WriteFile(p, d, 0o600)
		st, _ := store.Open(p)
		st.Writer.Exec(`DROP TRIGGER audit_no_update`)
		st.Writer.Exec(`UPDATE audit_events SET action = 'nothing.happened' WHERE seq = (SELECT max(seq) FROM audit_events)`)
		st.Writer.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`)
		st.Close()
		b, _ := os.ReadFile(p)
		return b
	}, nil)
	tampered = fixManifest(t, tampered)
	rep, err = Verify(ctx, bytes.NewReader(tampered), VerifyOptions{})
	if err != nil || !rep.Restorable() || rep.Audit == nil || rep.Audit.OK || !strings.Contains(strings.Join(rep.Warnings, " "), "audit history fails") {
		t.Fatalf("tampered audit history: %+v %v", rep, err)
	}
	sameInstallation(t, before, installation(t, src.DataDir))
}

// fixManifest recomputes the manifest's checksums (a tampered backup
// that is internally consistent).
func fixManifest(t *testing.T, archive []byte) []byte {
	t.Helper()
	sums := map[string]FileEntry{}
	rewrite(t, archive, func(n string, d []byte) []byte {
		s := sha256.Sum256(d)
		sums[n] = FileEntry{Name: n, Size: int64(len(d)), SHA256: hex.EncodeToString(s[:])}
		return d
	}, nil)
	return rewrite(t, archive, func(n string, d []byte) []byte {
		if n != nameManifest {
			return d
		}
		var m Manifest
		json.Unmarshal(d, &m)
		for i, f := range m.Files {
			m.Files[i] = sums[f.Name]
		}
		b, _ := json.Marshal(m)
		return b
	}, nil)
}

// TestMasterKeyRotation (M16): every sealed value is re-sealed with a new
// key version, the old version leaves the key file, and a backup made
// before the rotation restores with the saved previous key file.
func TestMasterKeyRotation(t *testing.T) {
	src := controller(t)
	ctx := context.Background()
	before := makeBackup(t, src, false)
	st, err := store.Open(filepath.Join(src.DataDir, store.FileName))
	if err != nil {
		t.Fatal(err)
	}
	// A second kind of sealed value: a notification destination's secret.
	box, _, _, _ := secrets.LoadOrCreateBox(src.MasterKeyPath())
	ct, nonce, ver, _ := box.Seal("ndst_1", "notify-webhook", []byte("https://hooks.example.test/T000/B000/xyz"))
	if _, err := st.Writer.Exec(`INSERT INTO notify_destinations (id, name, kind, config_json, secret_ciphertext, secret_nonce, secret_key_version, created_at, updated_at)
		VALUES ('ndst_1','ops','webhook','{}',?,?,?,0,0)`, ct, nonce, ver); err != nil {
		t.Fatal(err)
	}
	res, err := secrets.RotateMasterKey(ctx, st.Writer, src.MasterKeyPath(), false, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if res.OldVersion != 1 || res.NewVersion != 2 || res.Total != 2 || res.Resealed["secrets"] != 1 || res.Resealed["notify_destinations"] != 1 {
		t.Fatalf("rotation: %+v", res)
	}
	key, _ := os.ReadFile(src.MasterKeyPath())
	if !strings.HasPrefix(strings.TrimSpace(string(key)), "2:") || strings.Count(strings.TrimSpace(string(key)), "\n") != 0 {
		t.Fatalf("key file after retiring the old version: %q", key)
	}
	if fi, _ := os.Stat(src.MasterKeyPath()); fi.Mode().Perm() != 0o600 {
		t.Fatalf("key file mode %v", fi.Mode().Perm())
	}
	newBox, _, _, _ := secrets.LoadOrCreateBox(src.MasterKeyPath())
	if _, err := secrets.VerifyBox(ctx, st.Reader, newBox); err != nil {
		t.Fatalf("the rotated key: %v", err)
	}
	oldBox, _, _, _ := secrets.LoadOrCreateBox(res.PreviousKeyFile)
	if _, err := secrets.VerifyBox(ctx, st.Reader, oldBox); err == nil {
		t.Fatal("the old key still opens the re-sealed values")
	}
	var v int
	st.Reader.QueryRow(`SELECT secret_key_version FROM notify_destinations`).Scan(&v)
	st.Close()
	if v != 2 {
		t.Fatalf("destination secret still at version %d", v)
	}
	// The backup from before the rotation: refused with the new key alone,
	// restored with the saved previous key file.
	dst := fresh(t)
	os.WriteFile(dst.MasterKeyPath(), key, 0o600)
	var ke *KeyError
	if _, err := Restore(ctx, dst, bytes.NewReader(before), RestoreOptions{}); !errors.As(err, &ke) {
		t.Fatalf("old backup with the new key: %v", err)
	}
	if _, err := Restore(ctx, dst, bytes.NewReader(before), RestoreOptions{MasterKeyFile: res.PreviousKeyFile}); err != nil {
		t.Fatalf("old backup with the previous key file: %v", err)
	}

	// --keep-previous keeps both versions, and a key that doesn't open
	// everything is never rotated.
	src2 := controller(t)
	st2, _ := store.Open(filepath.Join(src2.DataDir, store.FileName))
	defer st2.Close()
	res2, err := secrets.RotateMasterKey(ctx, st2.Writer, src2.MasterKeyPath(), true, time.Now())
	if err != nil || !res2.KeptPrevious {
		t.Fatalf("keep previous: %+v %v", res2, err)
	}
	key2, _ := os.ReadFile(src2.MasterKeyPath())
	if !strings.Contains(string(key2), "1:") || !strings.Contains(string(key2), "2:") {
		t.Fatalf("both versions expected: %q", key2)
	}
	wrong := filepath.Join(t.TempDir(), "wrong.key")
	secrets.LoadOrCreateBox(wrong)
	if _, err := secrets.RotateMasterKey(ctx, st2.Writer, wrong, false, time.Now()); err == nil || !strings.Contains(err.Error(), "nothing was rotated") {
		t.Fatalf("rotation with a key that doesn't open everything: %v", err)
	}
}
