// Package backup creates and restores controller backups (ARCHITECTURE-ALPHA §17).
//
// A backup is a gzip-compressed tar archive:
//
//	manifest.json          what this is, who made it, file checksums
//	drool.db               consistent SQLite snapshot (VACUUM INTO)
//	tls/self-signed.crt    self-signed TLS material, when that mode is used
//	tls/self-signed.key
//	config/drool.yaml      the config file in use, if any (setup secret removed)
//	master.key             only with --include-master-key
//	artifacts/sha256/..    stored deployment artifacts (content-addressed;
//	                       history and rollback need them)
//
// Credentials in the database are encrypted with the master key. A backup
// without the key restores everything else, but credentials only work if the
// same key is available at restore time.
package backup

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"drool/internal/artifacts"
	"drool/internal/config"
	"drool/internal/secrets"
	"drool/internal/store"
	"drool/internal/version"
)

// Archive format identifiers.
const (
	Format        = "drool-backup"
	FormatVersion = 1
)

// Fixed archive member names.
const (
	nameManifest = "manifest.json"
	nameDB       = "drool.db"
	nameTLSCert  = "tls/self-signed.crt"
	nameTLSKey   = "tls/self-signed.key"
	nameConfig   = "config/drool.yaml"
	nameKey      = "master.key"
)

var knownNames = map[string]bool{nameManifest: true, nameDB: true, nameTLSCert: true, nameTLSKey: true, nameConfig: true, nameKey: true}

// maxSmallFile caps every member except the database.
const maxSmallFile = 1 << 20

// FileEntry describes one archive member.
type FileEntry struct {
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// Manifest describes a backup.
type Manifest struct {
	Format        string    `json:"format"`
	FormatVersion int       `json:"format_version"`
	DroolVersion  string    `json:"drool_version"`
	DroolCommit   string    `json:"drool_commit"`
	SchemaVersion int       `json:"schema_version"`
	CreatedAt     time.Time `json:"created_at"`
	Hostname      string    `json:"hostname"`
	// IncludesMasterKey is true only for --include-master-key backups, the
	// only kind that is a complete disaster-recovery backup on its own.
	IncludesMasterKey bool `json:"includes_master_key"`
	// MasterKeyID fingerprints the key the credentials were encrypted with
	// (empty if there was no key file). It is not secret.
	MasterKeyID string `json:"master_key_id"`
	Credentials int64  `json:"credentials"`
	TLSMode     string `json:"tls_mode"`
	// TLSNote explains TLS material that is not in the archive.
	TLSNote string           `json:"tls_note,omitempty"`
	Counts  map[string]int64 `json:"counts"`
	Files   []FileEntry      `json:"files"`
	// Agent PKI (M7): the intermediate (and the root, while it is still on
	// the controller) is sealed with the master key. A root key that was
	// exported and removed is never in a backup: keep that offline copy.
	AgentRootFingerprint string `json:"agent_root_fingerprint,omitempty"`
	AgentRootKeyIncluded bool   `json:"agent_root_key_included"`
	AgentKeysSealed      int64  `json:"agent_keys_sealed"`
	// Reserved for Beta restore reconciliation (DESIGN §10); null in the alpha.
	StateEpoch    *string `json:"state_epoch"`
	StateRevision *int64  `json:"state_revision"`
}

// Complete reports whether the backup alone can restore working credentials.
func (m *Manifest) Complete() bool {
	return m.IncludesMasterKey || (m.Credentials == 0 && m.AgentKeysSealed == 0)
}

// countedTables are summarized in the manifest and after a restore.
var countedTables = []string{"users", "api_tokens", "hosts", "ssh_host_keys", "secrets", "groups", "host_tags",
	"host_facts", "jobs", "job_output", "audit_events", "artifacts", "file_deployments",
	"agent_identities", "agent_ca", "agent_trust_manifests", "agent_enrollment_tokens", "agent_releases", "agent_rollouts", "state_versions",
	"fw_policies", "fw_services", "fw_host_config", "fw_group_config", "fw_host_state", "fw_transactions", "fw_observations", "fw_networks",
	"alerts", "maintenance", "notify_destinations", "notify_routes", "mon_health_overrides"}

// Options controls Create.
type Options struct {
	IncludeMasterKey bool
}

// Result describes a created backup.
type Result struct {
	Manifest *Manifest
	// KeyWarning is set when the master key could not be checked against
	// the stored credentials.
	KeyWarning string
	Warnings   []string
}

// artifactMember is an artifact's name inside the archive.
func artifactMember(sha string) string { return "artifacts/sha256/" + sha[:2] + "/" + sha }

// isArtifactMember reports whether name is a well-formed artifact member.
func isArtifactMember(name string) bool {
	rest, ok := strings.CutPrefix(name, "artifacts/sha256/")
	if !ok {
		return false
	}
	dir, sha, ok := strings.Cut(rest, "/")
	return ok && artifacts.ValidHash(sha) && dir == sha[:2]
}

// Create writes a backup of the controller described by cfg to w. It is safe
// to run while the controller is running.
func Create(ctx context.Context, cfg *config.Loaded, w io.Writer, opts Options) (*Result, error) {
	dbPath := filepath.Join(cfg.DataDir, store.FileName)
	if _, err := os.Stat(dbPath); err != nil {
		return nil, fmt.Errorf("no database at %s (is the data directory right?): %w", dbPath, err)
	}
	tmp, err := os.MkdirTemp(cfg.DataDir, ".backup-")
	if err != nil {
		return nil, fmt.Errorf("create temporary directory: %w", err)
	}
	defer os.RemoveAll(tmp)

	snap := filepath.Join(tmp, nameDB)
	if err := snapshot(ctx, dbPath, snap); err != nil {
		return nil, err
	}
	m := &Manifest{
		Format: Format, FormatVersion: FormatVersion, DroolVersion: version.Version, DroolCommit: version.Commit,
		CreatedAt: time.Now().UTC().Truncate(time.Second), TLSMode: cfg.Server.TLS.Mode,
		IncludesMasterKey: opts.IncludeMasterKey,
	}
	m.Hostname, _ = os.Hostname()
	res := &Result{Manifest: m}

	sdb, err := openReadOnly(snap)
	if err != nil {
		return nil, err
	}
	m.SchemaVersion, m.Counts, err = describe(ctx, sdb)
	var artHashes []string
	if err == nil {
		// Stored deployment artifacts (M6): deployment history and rollbacks
		// need them. Absent before schema 6.
		if rows, qerr := sdb.QueryContext(ctx, `SELECT sha256 FROM artifacts ORDER BY sha256`); qerr == nil {
			for rows.Next() {
				var h string
				if rows.Scan(&h) == nil && artifacts.ValidHash(h) {
					artHashes = append(artHashes, h)
				}
			}
			rows.Close()
		}
	}
	if err != nil {
		sdb.Close()
		return nil, err
	}
	m.Credentials = m.Counts["secrets"]
	var notifySecrets int64
	if sdb.QueryRowContext(ctx, `SELECT count(*) FROM notify_destinations WHERE secret_ciphertext IS NOT NULL`).Scan(&notifySecrets) == nil {
		m.Credentials += notifySecrets // absent before schema 12
	}
	if rows, qerr := sdb.QueryContext(ctx, `SELECT kind, fingerprint, key_ciphertext IS NOT NULL FROM agent_ca`); qerr == nil {
		for rows.Next() {
			var kind, fp string
			var sealed bool
			if rows.Scan(&kind, &fp, &sealed) != nil {
				continue
			}
			if sealed {
				m.AgentKeysSealed++
			}
			if kind == "root" {
				m.AgentRootFingerprint, m.AgentRootKeyIncluded = fp, sealed
			}
		}
		rows.Close()
	}

	// Fingerprint the key, and make sure it actually opens the credentials:
	// a backup that records the wrong key is worse than none.
	keyData, keyErr := os.ReadFile(cfg.MasterKeyPath())
	switch {
	case keyErr == nil:
		box, err := secrets.ParseKeyFile(keyData)
		if err != nil {
			sdb.Close()
			return nil, fmt.Errorf("master key %s: %w", cfg.MasterKeyPath(), err)
		}
		m.MasterKeyID = box.KeyID()
		if _, err := secrets.VerifyBox(ctx, newQueries(sdb), box); err != nil {
			res.KeyWarning = "the master key file does not match the stored credentials: " + err.Error()
		}
	case errors.Is(keyErr, os.ErrNotExist):
		if opts.IncludeMasterKey {
			sdb.Close()
			return nil, fmt.Errorf("--include-master-key: no master key at %s", cfg.MasterKeyPath())
		}
		if m.Credentials > 0 {
			res.KeyWarning = "no master key file at " + cfg.MasterKeyPath() + "; the stored credentials cannot be checked"
		}
	default:
		sdb.Close()
		return nil, fmt.Errorf("read master key: %w", keyErr)
	}
	sdb.Close()

	// Gather members: (archive name, local path or in-memory data).
	type member struct {
		name string
		path string
		data []byte
	}
	members := []member{{name: nameDB, path: snap}}
	switch cfg.Server.TLS.Mode {
	case "self_signed":
		crt, key := filepath.Join(cfg.DataDir, "tls", "self-signed.crt"), filepath.Join(cfg.DataDir, "tls", "self-signed.key")
		if fileExists(crt) && fileExists(key) {
			members = append(members, member{name: nameTLSCert, path: crt}, member{name: nameTLSKey, path: key})
		}
	case "provided":
		m.TLSNote = fmt.Sprintf("TLS certificate files are managed outside DROOL and are not included (%s, %s)",
			cfg.Server.TLS.CertFile, cfg.Server.TLS.KeyFile)
	}
	if cfg.FilePath != "" && fileExists(cfg.FilePath) {
		data, err := redactedConfig(cfg.FilePath)
		if err != nil {
			return nil, err
		}
		members = append(members, member{name: nameConfig, data: data})
	}
	if opts.IncludeMasterKey {
		members = append(members, member{name: nameKey, data: keyData})
	}
	for _, h := range artHashes {
		local := filepath.Join(cfg.DataDir, artifacts.RelPath(h))
		if fileExists(local) {
			members = append(members, member{name: artifactMember(h), path: local})
		} else {
			res.Warnings = append(res.Warnings, "stored artifact "+h+" is missing from "+filepath.Join(cfg.DataDir, artifacts.DirName))
		}
	}

	for _, mb := range members {
		var e FileEntry
		var err error
		if mb.path != "" {
			e, err = hashFile(mb.name, mb.path)
		} else {
			sum := sha256.Sum256(mb.data)
			e = FileEntry{Name: mb.name, Size: int64(len(mb.data)), SHA256: hex.EncodeToString(sum[:])}
		}
		if err != nil {
			return nil, err
		}
		m.Files = append(m.Files, e)
	}
	manifest, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, err
	}

	gz := gzip.NewWriter(w)
	tw := tar.NewWriter(gz)
	add := func(name string, size int64, r io.Reader) error {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: size, ModTime: m.CreatedAt, Typeflag: tar.TypeReg}); err != nil {
			return err
		}
		_, err := io.Copy(tw, r)
		return err
	}
	if err := add(nameManifest, int64(len(manifest)), bytesReader(manifest)); err != nil {
		return nil, fmt.Errorf("write backup: %w", err)
	}
	for i, mb := range members {
		var r io.Reader
		if mb.path != "" {
			f, err := os.Open(mb.path)
			if err != nil {
				return nil, err
			}
			defer f.Close()
			r = f
		} else {
			r = bytesReader(mb.data)
		}
		if err := add(mb.name, m.Files[i].Size, r); err != nil {
			return nil, fmt.Errorf("write backup: %w", err)
		}
	}
	if err := tw.Close(); err != nil {
		return nil, fmt.Errorf("write backup: %w", err)
	}
	if err := gz.Close(); err != nil {
		return nil, fmt.Errorf("write backup: %w", err)
	}
	return res, nil
}

// snapshot copies the live database consistently, even while the
// controller is writing to it.
func snapshot(ctx context.Context, src, dst string) error {
	d, err := sql.Open("sqlite", "file:"+filepath.ToSlash(src)+"?_pragma=busy_timeout(10000)")
	if err != nil {
		return err
	}
	defer d.Close()
	if _, err := d.ExecContext(ctx, `VACUUM INTO ?`, dst); err != nil {
		return fmt.Errorf("snapshot database: %w", err)
	}
	return nil
}

func openReadOnly(path string) (*sql.DB, error) {
	d, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?mode=ro&_pragma=query_only(1)")
	if err != nil {
		return nil, err
	}
	d.SetMaxOpenConns(1)
	return d, nil
}

// describe returns the schema version and row counts of a database.
func describe(ctx context.Context, d *sql.DB) (int, map[string]int64, error) {
	var schema sql.NullInt64
	if err := d.QueryRowContext(ctx, `SELECT max(version) FROM schema_migrations`).Scan(&schema); err != nil {
		return 0, nil, fmt.Errorf("not a DROOL database (no schema version): %w", err)
	}
	counts := map[string]int64{}
	for _, t := range countedTables {
		var n int64
		if err := d.QueryRowContext(ctx, `SELECT count(*) FROM `+t).Scan(&n); err != nil {
			// A table added by a later migration is simply absent in older backups.
			continue
		}
		counts[t] = n
	}
	return int(schema.Int64), counts, nil
}

// redactedConfig returns the config file with setup.secret blanked; it is
// useless after first-admin setup and should not travel in backups.
func redactedConfig(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config file: %w", err)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parse config file: %w", err)
	}
	if !redactKey(&doc, "setup", "secret") {
		return data, nil
	}
	return yaml.Marshal(&doc)
}

func redactKey(n *yaml.Node, section, key string) bool {
	if n.Kind == yaml.DocumentNode && len(n.Content) == 1 {
		return redactKey(n.Content[0], section, key)
	}
	if n.Kind != yaml.MappingNode {
		return false
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value != section || n.Content[i+1].Kind != yaml.MappingNode {
			continue
		}
		sec := n.Content[i+1]
		for j := 0; j+1 < len(sec.Content); j += 2 {
			if sec.Content[j].Value == key && sec.Content[j+1].Value != "" {
				sec.Content[j+1].Value = ""
				sec.Content[j+1].Tag = "!!str"
				return true
			}
		}
	}
	return false
}

func hashFile(name, path string) (FileEntry, error) {
	f, err := os.Open(path)
	if err != nil {
		return FileEntry{}, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return FileEntry{}, err
	}
	return FileEntry{Name: name, Size: n, SHA256: hex.EncodeToString(h.Sum(nil))}, nil
}

func fileExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.Mode().IsRegular()
}
