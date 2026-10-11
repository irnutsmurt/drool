package api

import (
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"drool/internal/backup"
	"drool/internal/config"
)

// securityFacts are what only the controller can tell about how safely it
// is set up (M17, "Secure your installation" on Settings → System). The
// rest of the checklist comes from existing endpoints (two-factor, PKI,
// audit). Copies kept elsewhere — of backups or of the master key — are
// invisible to DROOL, and it says so rather than guess.
type securityFacts struct {
	// Deployment: "container" or "native" (how the commands it shows run).
	Deployment string `json:"deployment"`
	// TestMode lists DROOL_TEST_* variables set: test switches that weaken
	// or fake behavior, never for production.
	TestMode []string `json:"test_mode"`
	// AdminsWithoutMFA are administrator accounts without two-factor sign-in.
	AdminsWithoutMFA []string `json:"admins_without_mfa"`
	MasterKey        struct {
		Path string `json:"path"`
		// InDataDir: the key lives with the database (the default): a copy
		// of the data directory or volume holds both.
		InDataDir bool `json:"in_data_dir"`
		// PreviousFiles are older key files a rotation left next to it;
		// they belong offline with the backups made before the rotation.
		PreviousFiles []string `json:"previous_files"`
	} `json:"master_key"`
	// Backups are those in the data directory: what `drool backup` writes
	// by default, and the automatic ones before upgrades.
	Backups struct {
		Dir string `json:"dir"`
		// NewestManual is the newest `drool backup` there (null: none).
		NewestManual *string `json:"newest_manual"`
		Manual       int     `json:"manual"`
		PreUpgrade   int     `json:"pre_upgrade"`
		// Bytes is what they take on the controller's disk.
		Bytes int64 `json:"bytes"`
	} `json:"backups"`
	// Hosts signing in with short-lived SSH certificates, and with stored
	// keys or passwords.
	CertificateHosts int `json:"certificate_hosts"`
	CredentialHosts  int `json:"credential_hosts"`
}

func (s *Server) handleSecurityFacts(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	f := securityFacts{Deployment: "native", TestMode: []string{}, AdminsWithoutMFA: []string{}}
	if config.InContainer() {
		f.Deployment = "container"
	}
	for _, kv := range os.Environ() {
		if name, _, _ := strings.Cut(kv, "="); strings.HasPrefix(name, "DROOL_TEST_") {
			f.TestMode = append(f.TestMode, name)
		}
	}
	sort.Strings(f.TestMode)

	rows, err := s.d.Store.Reader.QueryContext(ctx, `SELECT u.username FROM users u
		LEFT JOIN user_mfa m ON m.user_id = u.id AND m.enabled_at IS NOT NULL
		WHERE u.role = 'admin' AND m.user_id IS NULL ORDER BY u.username`)
	if err != nil {
		writeProblem(w, r, s.internalError(r, err))
		return
	}
	for rows.Next() {
		var name string
		if rows.Scan(&name) == nil {
			f.AdminsWithoutMFA = append(f.AdminsWithoutMFA, name)
		}
	}
	rows.Close()

	cfg := s.d.Config
	key := cfg.MasterKeyPath()
	f.MasterKey.Path = key
	f.MasterKey.InDataDir = filepath.Dir(filepath.Clean(key)) == filepath.Clean(cfg.DataDir)
	f.MasterKey.PreviousFiles = []string{}
	if prev, _ := filepath.Glob(key + ".previous-*"); len(prev) > 0 {
		for _, p := range prev {
			f.MasterKey.PreviousFiles = append(f.MasterKey.PreviousFiles, filepath.Base(p))
		}
		sort.Strings(f.MasterKey.PreviousFiles)
	}

	f.Backups.Dir = filepath.Join(cfg.DataDir, "backups")
	var newest time.Time
	if ents, err := os.ReadDir(f.Backups.Dir); err == nil {
		for _, e := range ents {
			name := e.Name()
			if !e.Type().IsRegular() || !strings.HasSuffix(name, ".tar.gz") {
				continue
			}
			fi, err := e.Info()
			if err != nil {
				continue
			}
			switch {
			case strings.HasPrefix(name, backup.PreUpgradeName):
				f.Backups.PreUpgrade++
			case strings.HasPrefix(name, "drool-backup-"):
				f.Backups.Manual++
				if fi.ModTime().After(newest) {
					newest = fi.ModTime()
				}
			default:
				continue
			}
			f.Backups.Bytes += fi.Size()
		}
	}
	if !newest.IsZero() {
		v := newest.UTC().Format(time.RFC3339)
		f.Backups.NewestManual = &v
	}

	if err := s.d.Store.Reader.QueryRowContext(ctx, `SELECT
		coalesce(sum(CASE WHEN ssh_configured != 0 AND ssh_auth = 'certificate' THEN 1 ELSE 0 END), 0),
		coalesce(sum(CASE WHEN ssh_configured != 0 AND ssh_auth != 'certificate' THEN 1 ELSE 0 END), 0) FROM hosts`).
		Scan(&f.CertificateHosts, &f.CredentialHosts); err != nil {
		writeProblem(w, r, s.internalError(r, err))
		return
	}
	writeJSON(w, http.StatusOK, f)
}
