//go:build integration

package integration

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"drool/internal/store"
)

// TestBackupCLI checks the backup and restore commands against a running
// controller configured like the container image (logs on stdout).
func TestBackupCLI(t *testing.T) {
	freshController(t) // a controller of its own: nothing from other tests (M17)
	env := append(os.Environ(), "DROOL_DATA_DIR="+ctl.dataDir, "DROOL_LOGGING_OUTPUT=stdout")

	cmd := exec.Command(binPath, "backup", "-o", "-")
	cmd.Env = env
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		t.Fatalf("backup: %v: %s", err, errb.String())
	}
	// Strict: gzip's multistream reader fails on anything after the archive.
	gz, err := gzip.NewReader(bytes.NewReader(out.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	var names []string
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("archive: %v", err)
		}
		names = append(names, h.Name)
	}
	if _, err := io.Copy(io.Discard, gz); err != nil {
		t.Fatalf("data after the archive (log lines on stdout?): %v", err)
	}
	if len(names) < 2 || names[0] != "manifest.json" {
		t.Fatalf("unexpected members %v", names)
	}
	if !strings.Contains(errb.String(), "NOT a complete") {
		t.Errorf("a backup without the master key must say so: %s", errb.String())
	}

	restore := exec.Command(binPath, "restore", "--yes", "-")
	restore.Env = env
	restore.Stdin = bytes.NewReader(out.Bytes())
	msg, err := restore.CombinedOutput()
	if err == nil || !strings.Contains(string(msg), "in use by a running DROOL controller") {
		t.Fatalf("restore under a running controller: %v %s", err, msg)
	}
}

// TestUpgradeSafetyCLI runs the real binary against data directories in
// the states an upgrade, a downgrade or an interrupted restore leave
// (M16): a backup before every schema upgrade, a refused downgrade that
// says what to do, drool backup verify's verdicts, and drool restore
// --recover.
func TestUpgradeSafetyCLI(t *testing.T) {
	dir := t.TempDir()
	env := append(os.Environ(), "DROOL_DATA_DIR="+dir, "DROOL_LOGGING_OUTPUT=stdout")
	drool := func(args ...string) (string, int) {
		cmd := exec.Command(binPath, args...)
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		code := 0
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else if err != nil {
			t.Fatal(err)
		}
		return string(out), code
	}
	// A database as an older DROOL (schema 15, M14) left it.
	st, err := store.Open(filepath.Join(dir, store.FileName))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.MigrateTo(context.Background(), 15); err != nil {
		t.Fatal(err)
	}
	st.Writer.Exec(`INSERT INTO users (id, username, password_hash, role, created_at, updated_at, password_changed_at) VALUES ('usr_1','admin','x','admin',0,0,0)`)
	st.Close()

	out, code := drool("migrate")
	expect(t, code == 0 && strings.Contains(out, "Backed up the database before upgrading it from schema 15"), "upgrade: %d\n%s", code, out)
	pre, _ := filepath.Glob(filepath.Join(dir, "backups", "drool-pre-upgrade-*-schema15.tar.gz"))
	expect(t, len(pre) == 1, "pre-upgrade backups: %v", pre)
	out, code = drool("backup", "verify", pre[0])
	expect(t, code == 0 && strings.Contains(out, "Restorable and complete") && strings.Contains(out, "schema 15"), "verify the pre-upgrade backup: %d\n%s", code, out)
	out, _ = drool("migrate")
	expect(t, strings.Contains(out, "up to date") && !strings.Contains(out, "Backed up"), "no backup without an upgrade:\n%s", out)

	// A damaged file and a file that isn't a backup.
	b, _ := os.ReadFile(pre[0])
	cut := filepath.Join(dir, "cut.tar.gz")
	os.WriteFile(cut, b[:len(b)-5], 0o600)
	out, code = drool("backup", "verify", cut)
	expect(t, code == 1 && strings.Contains(out, "NOT RESTORABLE"), "a cut backup: %d\n%s", code, out)

	// A newer DROOL's database: refused, saying what to do.
	st, _ = store.Open(filepath.Join(dir, store.FileName))
	st.Writer.Exec(`INSERT INTO schema_migrations (version, name, applied_at, drool_version) VALUES (9999, 'future.sql', 0, 'v9.9.9')`)
	st.Close()
	out, code = drool("migrate")
	expect(t, code == 1 && strings.Contains(out, "upgraded to schema 9999 by DROOL v9.9.9") && strings.Contains(out, "doesn't downgrade") &&
		strings.Contains(out, "drool-pre-upgrade-"), "downgrade: %d\n%s", code, out)
	st, _ = store.Open(filepath.Join(dir, store.FileName))
	st.Writer.Exec(`DELETE FROM schema_migrations WHERE version = 9999`)
	st.Close()

	// An interrupted restore: the database moved aside, the journal left.
	prev := filepath.Join(dir, "pre-restore-20260101-000000")
	os.Mkdir(prev, 0o700)
	os.Rename(filepath.Join(dir, store.FileName), filepath.Join(prev, store.FileName))
	journal := fmt.Sprintf(`{"version":1,"started_at":"2026-01-01T00:00:00Z","previous_dir":%q,"moves":[{"from":%q,"to":%q}],"committed":false}`,
		prev, filepath.Join(dir, store.FileName), filepath.Join(prev, store.FileName))
	os.WriteFile(filepath.Join(dir, "restore.journal"), []byte(journal), 0o600)
	out, code = drool("migrate")
	expect(t, code == 1 && strings.Contains(out, "drool restore --recover"), "start on a half-restored directory: %d\n%s", code, out)
	_, err = os.Stat(filepath.Join(dir, store.FileName))
	expect(t, os.IsNotExist(err), "an empty database was created over the interrupted restore")
	out, code = drool("restore", "--recover")
	expect(t, code == 0 && strings.Contains(out, "previous installation back"), "recover: %d\n%s", code, out)
	out, code = drool("migrate")
	expect(t, code == 0 && strings.Contains(out, "up to date"), "after recovering: %d\n%s", code, out)
}
