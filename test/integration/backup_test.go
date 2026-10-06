//go:build integration

package integration

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// TestBackupCLI checks the backup and restore commands against a running
// controller configured like the container image (logs on stdout).
func TestBackupCLI(t *testing.T) {
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
