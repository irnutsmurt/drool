package backup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"drool/internal/audit"
	"drool/internal/secrets"
	"drool/internal/store"
)

// VerifyOptions controls Verify.
type VerifyOptions struct {
	// MasterKeyFile is tried when the backup doesn't include its key.
	MasterKeyFile string
	// MasterKey is such a key file's content, read by the caller (from
	// standard input); it is used instead of MasterKeyFile.
	MasterKey []byte
}

// VerifyReport says what a backup can restore (drool backup verify, M16).
type VerifyReport struct {
	Manifest *Manifest
	// Problems make the backup unrestorable; Warnings don't.
	Problems []string
	Warnings []string
	Files    int
	// Migrations restoring it would apply on this DROOL.
	Migrations []string
	// Sealed values (credentials, CA keys, ...) and which key opened them
	// ("backup", "file"; "" when none did).
	Sealed    int
	KeySource string
	// Audit is the audit chain's verification (nil before schema 16).
	Audit *audit.Report
}

// Restorable: nothing prevents restoring it.
func (r *VerifyReport) Restorable() bool { return len(r.Problems) == 0 }

// Complete: restorable with everything, credentials included.
func (r *VerifyReport) Complete() bool {
	return r.Restorable() && (r.Sealed == 0 || r.KeySource != "")
}

// Verify checks a backup without restoring it: archive and checksums,
// the database's integrity, that it upgrades to this DROOL's schema (on a
// temporary copy), which master key opens its sealed values, and its
// audit history. Nothing outside a temporary directory is touched.
func Verify(ctx context.Context, r io.Reader, opts VerifyOptions) (*VerifyReport, error) {
	tmp, err := os.MkdirTemp("", "drool-verify-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	rep := &VerifyReport{}
	problem := func(f string, a ...any) { rep.Problems = append(rep.Problems, fmt.Sprintf(f, a...)) }

	st, err := extract(r, tmp)
	if err != nil {
		var inv *InvalidError
		if errors.As(err, &inv) {
			problem("%s", inv.Reason)
			return rep, nil
		}
		return nil, err
	}
	rep.Manifest, rep.Files = st.manifest, len(st.has)
	m := st.manifest
	dbPath := filepath.Join(st.dir, nameDB)
	if err := checkIntegrity(ctx, dbPath); err != nil {
		problem("%s", strings.TrimPrefix(err.Error(), "invalid backup: "))
		return rep, nil
	}
	sdb, err := store.Open(dbPath)
	if err != nil {
		problem("the database can't be opened: %v", err)
		return rep, nil
	}
	defer sdb.Close()
	schema, err := sdb.SchemaVersion(ctx)
	switch {
	case err != nil:
		problem("not a DROOL database: %v", err)
		return rep, nil
	case schema != m.SchemaVersion:
		problem("the database's schema (%d) doesn't match the manifest (%d)", schema, m.SchemaVersion)
		return rep, nil
	case schema > store.LatestVersion():
		problem("made by a newer DROOL (%s, schema %d); this one supports schema %d: restore it with that version or newer",
			m.DroolVersion, schema, store.LatestVersion())
		return rep, nil
	}
	if rep.Migrations, err = sdb.Migrate(ctx); err != nil {
		problem("upgrading it to this DROOL's schema fails: %v", err)
		return rep, nil
	}

	if rep.Sealed, err = secrets.CountSealed(ctx, sdb.Reader); err != nil {
		problem("its stored credentials can't be read: %v", err)
		return rep, nil
	}
	var box *secrets.Box
	try := func(source, label string, data []byte) {
		if rep.KeySource != "" {
			return
		}
		b, err := secrets.ParseKeyFile(data)
		if err != nil {
			rep.Warnings = append(rep.Warnings, label+" is not a DROOL master key file: "+err.Error())
			return
		}
		if _, err := secrets.VerifyBox(ctx, sdb.Reader, b); err != nil {
			if source == "backup" {
				problem("the master key in the backup doesn't open its stored credentials (%v)", err)
			} else if rep.Sealed > 0 {
				rep.Warnings = append(rep.Warnings, fmt.Sprintf("%s (key id %s) doesn't open its stored credentials", label, b.KeyID()))
			}
			return
		}
		rep.KeySource, box = source, b
	}
	if st.has[nameKey] {
		data, err := os.ReadFile(filepath.Join(st.dir, nameKey))
		if err != nil {
			return nil, err
		}
		try("backup", "the master key in the backup", data)
	}
	switch {
	case opts.MasterKey != nil:
		try("file", "the master key from standard input", opts.MasterKey)
	case opts.MasterKeyFile != "":
		data, err := os.ReadFile(opts.MasterKeyFile)
		if err != nil {
			return nil, fmt.Errorf("--master-key: %w", err)
		}
		try("file", opts.MasterKeyFile, data)
	}
	if rep.Sealed == 0 && rep.KeySource == "" {
		rep.KeySource = "none needed"
	}
	if rep.Sealed > 0 && rep.KeySource == "" && rep.Restorable() {
		rep.Warnings = append(rep.Warnings, fmt.Sprintf("its %d stored credential(s) and keys open only with master key id %s, which the backup doesn't include: "+
			"restore it with that key file (--master-key), or without credentials (--discard-credentials)", rep.Sealed, orUnknown(m.MasterKeyID)))
	}

	// The audit history (M16 chains it): verified as the controller would,
	// checkpoints too when a key opened.
	var hasChain int
	sdb.Reader.QueryRowContext(ctx, `SELECT count(*) FROM pragma_table_info('audit_events') WHERE name = 'hash'`).Scan(&hasChain)
	if hasChain > 0 {
		vo := audit.VerifyOptions{}
		if box != nil {
			vo.CheckpointKey = func(v int) ([]byte, bool) {
				k, _, err := box.DeriveKey(audit.CheckpointPurpose, v)
				return k, err == nil
			}
		}
		ar, err := audit.Verify(ctx, sdb.Reader, vo)
		if err != nil {
			rep.Warnings = append(rep.Warnings, "its audit history couldn't be checked: "+err.Error())
		} else {
			rep.Audit = ar
			if !ar.OK {
				rep.Warnings = append(rep.Warnings, "its audit history fails verification: "+ar.Problem+
					" (the restored controller will report it; investigate before trusting that history)")
			}
		}
	}
	for _, w := range []struct {
		cond bool
		msg  string
	}{
		{m.TLSNote != "", m.TLSNote},
		{m.AgentRootFingerprint != "" && !m.AgentRootKeyIncluded, "the Agent Root CA private key isn't in it (it was taken offline): keep that offline copy"},
	} {
		if w.cond {
			rep.Warnings = append(rep.Warnings, w.msg)
		}
	}
	return rep, nil
}
