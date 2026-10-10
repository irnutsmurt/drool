package secrets

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"drool/internal/store"
)

func TestBoxRoundTripAndBinding(t *testing.T) {
	b := NewBoxForTest()
	ct, nonce, ver, err := b.Seal("sec_1", KindPassword, []byte("hunter2"))
	if err != nil {
		t.Fatal(err)
	}
	pt, err := b.Open("sec_1", KindPassword, ct, nonce, ver)
	if err != nil || string(pt) != "hunter2" {
		t.Fatalf("round trip: %q %v", pt, err)
	}
	// Ciphertext is bound to its row id and kind.
	if _, err := b.Open("sec_2", KindPassword, ct, nonce, ver); err == nil {
		t.Fatal("ciphertext opened under a different id")
	}
	if _, err := b.Open("sec_1", KindSudoPassword, ct, nonce, ver); err == nil {
		t.Fatal("ciphertext opened under a different kind")
	}
	ct[0] ^= 1
	if _, err := b.Open("sec_1", KindPassword, ct, nonce, ver); err == nil {
		t.Fatal("tampered ciphertext opened")
	}
	if _, err := NewBoxForTest().Open("sec_1", KindPassword, ct, nonce, ver); err == nil {
		t.Fatal("opened with a different master key")
	}
}

func TestKeyFileCreatedPrivate(t *testing.T) {
	p := filepath.Join(t.TempDir(), "master.key")
	b1, created, insecure, err := LoadOrCreateBox(p)
	if err != nil || !created || insecure {
		t.Fatalf("create: created=%v insecure=%v err=%v", created, insecure, err)
	}
	fi, _ := os.Stat(p)
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", fi.Mode().Perm())
	}
	ct, nonce, ver, _ := b1.Seal("id", KindPassword, []byte("x"))
	b2, created, _, err := LoadOrCreateBox(p)
	if err != nil || created {
		t.Fatalf("reload: %v %v", created, err)
	}
	if pt, err := b2.Open("id", KindPassword, ct, nonce, ver); err != nil || string(pt) != "x" {
		t.Fatal("reloaded key cannot decrypt")
	}
	os.Chmod(p, 0o644)
	if _, _, insecure, _ := LoadOrCreateBox(p); !insecure {
		t.Fatal("world-readable key file not flagged")
	}
}

// TestSealedLocationsCoverSchema: every column holding master-key ciphertext
// is listed in sealedLocations, so key checks, rotation and recovery can't
// miss it.
func TestSealedLocationsCoverSchema(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), store.FileName))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	listed := map[string]bool{}
	for _, l := range sealedLocations {
		listed[l.Table] = true
		if _, err := st.Reader.ExecContext(ctx, `EXPLAIN `+l.Select); err != nil {
			t.Fatalf("%s: %v", l.Table, err)
		}
	}
	rows, err := st.Reader.QueryContext(ctx, `SELECT m.name FROM sqlite_master m, pragma_table_info(m.name) c
		WHERE m.type = 'table' AND c.name LIKE '%ciphertext%'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var table string
		rows.Scan(&table)
		if !listed[table] {
			t.Errorf("table %s has a ciphertext column but is not in sealedLocations", table)
		}
	}
}
