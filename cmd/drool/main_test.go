package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRootRefusedOnTheServiceUsersDataDir(t *testing.T) {
	dir := t.TempDir() // owned by whoever runs the test
	if os.Geteuid() == 0 {
		t.Skip("needs a data directory owned by a user other than root")
	}
	if err := checkDataDirOwner(dir, os.Geteuid()); err != nil {
		t.Errorf("the owner refused: %v", err)
	}
	err := checkDataDirOwner(dir, 0)
	if err == nil || !strings.Contains(err.Error(), "sudo -u ") {
		t.Errorf("root on another user's data directory: %v", err)
	}
	// Root-owned or missing directories are root's to use.
	if err := checkDataDirOwner("/", 0); err != nil {
		t.Errorf("a root-owned directory: %v", err)
	}
	if err := checkDataDirOwner(filepath.Join(dir, "missing"), 0); err != nil {
		t.Errorf("a missing directory: %v", err)
	}
}
