package api

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"drool/internal/auth"
)

// TestSecurityFacts: what the "Secure your installation" checklist learns
// from the controller (M17), and who may ask.
func TestSecurityFacts(t *testing.T) {
	t.Setenv("DROOL_TEST_EXAMPLE_SWITCH", "1")
	h := newHarness(t)
	h.mkUser("admin", auth.RoleAdmin)
	h.mkUser("second", auth.RoleAdmin)
	h.mkUser("viewer", auth.RoleViewer)
	if r := h.login("viewer").do("GET", "/api/v1/system/security", nil); r.status != 403 {
		t.Fatalf("a viewer read the security facts: %d", r.status)
	}
	// One administrator has two-factor sign-in on.
	if _, err := h.st.Writer.Exec(`INSERT INTO user_mfa (user_id, secret_ciphertext, secret_nonce, secret_key_version, enabled_at, created_at, updated_at)
		SELECT id, x'00', x'00', 1, 1, 0, 0 FROM users WHERE username = 'second'`); err != nil {
		t.Fatal(err)
	}
	cfg := h.srv.d.Config
	dir := filepath.Join(cfg.DataDir, "backups")
	os.MkdirAll(dir, 0o700)
	for name, size := range map[string]int{"drool-backup-20261001-000000.tar.gz": 10, "drool-pre-upgrade-20261002-000000-schema5.tar.gz": 20,
		".drool-backup-unfinished.tar.gz": 99, "notes.txt": 7} {
		os.WriteFile(filepath.Join(dir, name), make([]byte, size), 0o600)
	}
	os.WriteFile(cfg.MasterKeyPath()+".previous-20261003-000000", []byte("1:x"), 0o600)

	var f securityFacts
	r := h.login("admin").do("GET", "/api/v1/system/security", nil)
	r.json(t, &f)
	if r.status != 200 {
		t.Fatalf("security facts: %d %s", r.status, r.body)
	}
	found := false
	for _, v := range f.TestMode {
		found = found || v == "DROOL_TEST_EXAMPLE_SWITCH"
	}
	if !found {
		t.Errorf("test mode not reported: %v", f.TestMode)
	}
	if !reflect.DeepEqual(f.AdminsWithoutMFA, []string{"admin"}) {
		t.Errorf("administrators without two-factor: %v", f.AdminsWithoutMFA)
	}
	if !f.MasterKey.InDataDir || !reflect.DeepEqual(f.MasterKey.PreviousFiles, []string{"master.key.previous-20261003-000000"}) {
		t.Errorf("master key: %+v", f.MasterKey)
	}
	if f.Backups.Manual != 1 || f.Backups.PreUpgrade != 1 || f.Backups.Bytes != 30 || f.Backups.NewestManual == nil {
		t.Errorf("backups: %+v", f.Backups)
	}
	if f.CertificateHosts != 0 || f.CredentialHosts != 0 || f.Deployment == "" {
		t.Errorf("facts: %+v", f)
	}
}
