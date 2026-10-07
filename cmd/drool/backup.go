package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"drool/internal/audit"
	"drool/internal/backup"
	"drool/internal/logging"
	"drool/internal/store"
)

func cmdBackup(path string, explicit bool, args []string) error {
	fs := flag.NewFlagSet("backup", flag.ContinueOnError)
	out := fs.String("o", "", "output file (default <data_dir>/backups/drool-backup-<time>.tar.gz; \"-\" writes to stdout)")
	withKey := fs.Bool("include-master-key", false, "include the master key (makes the backup a complete disaster-recovery backup; keep it as safe as the controller itself)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := loadConfig(path, explicit)
	if err != nil {
		return err
	}
	ctx := context.Background()
	toStdout := *out == "-"
	msg := os.Stderr

	dest := *out
	if dest == "" {
		dir := filepath.Join(cfg.DataDir, "backups")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
		dest = filepath.Join(dir, "drool-backup-"+time.Now().UTC().Format("20060102-150405")+".tar.gz")
	}
	var w io.Writer = os.Stdout
	var f *os.File
	if !toStdout {
		if f, err = os.CreateTemp(filepath.Dir(dest), ".drool-backup-*"); err != nil {
			return fmt.Errorf("create backup file: %w", err)
		}
		defer os.Remove(f.Name()) // no-op after the rename
		if err := f.Chmod(0o600); err != nil {
			f.Close()
			return err
		}
		w = f
	}
	res, err := backup.Create(ctx, cfg, w, backup.Options{IncludeMasterKey: *withKey})
	if err != nil {
		if f != nil {
			f.Close()
		}
		return err
	}
	if f != nil {
		if err := f.Sync(); err != nil {
			f.Close()
			return err
		}
		if err := f.Close(); err != nil {
			return err
		}
		if err := os.Rename(f.Name(), dest); err != nil {
			return err
		}
	}
	m := res.Manifest

	// Record without migrating (a running controller owns the schema), and to
	// the database only, like the other CLI commands: a log line on stdout
	// would end up inside "backup -o -" archives in container deployments.
	if st, err := store.Open(filepath.Join(cfg.DataDir, store.FileName)); err == nil {
		actor := audit.Actor{Type: audit.ActorSystem, Name: "cli:" + currentOSUser()}
		_ = audit.NewWriter(st, logging.Discard().Audit).Record(ctx, audit.Event{Action: "backup.create", Actor: &actor,
			TargetType: "controller", Details: map[string]any{
				"file": map[bool]string{true: "(stdout)", false: dest}[toStdout], "includes_master_key": m.IncludesMasterKey,
				"schema": m.SchemaVersion}})
		st.Close()
	}

	if !toStdout {
		fmt.Fprintf(msg, "Backup written to %s\n", dest)
	} else {
		fmt.Fprintln(msg, "Backup written to standard output.")
	}
	fmt.Fprintf(msg, "  DROOL %s, schema %d, %s\n", m.DroolVersion, m.SchemaVersion, summarizeCounts(m.Counts))
	if res.KeyWarning != "" {
		fmt.Fprintf(msg, "\nWARNING: %s.\n", res.KeyWarning)
	}
	for _, w := range res.Warnings {
		fmt.Fprintf(msg, "WARNING: %s.\n", w)
	}
	switch {
	case m.IncludesMasterKey:
		fmt.Fprint(msg, `
This backup INCLUDES the master key: anyone with this file can decrypt every
stored credential (SSH private keys, passwords). Store it encrypted and as
carefully as the controller itself.
`)
	case m.Credentials > 0:
		fmt.Fprintf(msg, `
This backup does NOT include the master key, so it is NOT a complete
disaster-recovery backup on its own. Its %d stored credential(s) can only be
restored together with the master key file (key id %s):
    %s
Keep a copy of that file somewhere safe and separate from this backup, or run
"drool backup --include-master-key" for a self-contained backup.
`, m.Credentials, m.MasterKeyID, cfg.MasterKeyPath())
	}
	if m.AgentRootFingerprint != "" {
		if m.AgentRootKeyIncluded {
			fmt.Fprintf(msg, "\nThe Agent Root CA private key is still on this controller, so it is in this backup (sealed with the\nmaster key). Export it to offline storage and remove it from the controller (Agents → Trust in the web UI).\n")
		} else {
			fmt.Fprintf(msg, "\nThe Agent Root CA private key is offline and NOT in this backup (root %s).\nKeep your offline copy: it is needed to recover from a lost master key or a compromised intermediate.\n", m.AgentRootFingerprint[:23])
		}
	}
	if m.TLSNote != "" {
		fmt.Fprintf(msg, "\nNote: %s.\n", m.TLSNote)
	}
	if !toStdout && strings.HasPrefix(dest, cfg.DataDir+string(filepath.Separator)) {
		fmt.Fprintln(msg, "\nThe backup is inside the data directory; copy it to another machine for it to be useful after a disk failure.")
	}
	return nil
}

func cmdRestore(path string, explicit bool, args []string) error {
	fs := flag.NewFlagSet("restore", flag.ContinueOnError)
	keyFile := fs.String("master-key", "", "master key file for a backup made without --include-master-key")
	discard := fs.Bool("discard-credentials", false, "if no matching master key is available, restore everything except stored credentials (they are deleted and must be added again)")
	yes := fs.Bool("yes", false, "don't ask for confirmation")
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, `Usage: drool restore [options] FILE   ("-" reads from standard input)

Replaces this controller's database, TLS material and (if included) master
key with the backup. Stop the controller first. The current files are moved
to <data_dir>/pre-restore-<time>/, and nothing is changed unless the whole
backup checks out.

Options:
`)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return errors.New("restore needs exactly one backup file")
	}
	cfg, err := loadConfig(path, explicit)
	if err != nil {
		return err
	}
	src := fs.Arg(0)
	var r io.Reader = os.Stdin
	if src != "-" {
		f, err := os.Open(src)
		if err != nil {
			return err
		}
		defer f.Close()
		r = f
	}
	if !*yes && src != "-" && isTerminal(os.Stdin) {
		fmt.Fprintf(os.Stderr, "Replace the DROOL data in %s with %s? [y/N] ", cfg.DataDir, src)
		var answer string
		fmt.Scanln(&answer)
		if a := strings.ToLower(strings.TrimSpace(answer)); a != "y" && a != "yes" {
			return errors.New("cancelled")
		}
	}

	res, err := backup.Restore(context.Background(), cfg, r, backup.RestoreOptions{MasterKeyFile: *keyFile, DiscardCredentials: *discard})
	var ke *backup.KeyError
	if errors.As(err, &ke) {
		return fmt.Errorf(`%v.

Nothing was changed. To restore, do one of:
  - supply the master key file it was made with:  drool restore --master-key /path/to/master.key %s
  - restore without stored credentials:            drool restore --discard-credentials %s
    (users, hosts, pinned host keys, groups and history are kept; SSH keys and
    passwords are deleted, the controller generates a new SSH key, and each
    host needs a credential assigned again)`, err, src, src)
	}
	if err != nil {
		return err
	}
	m := res.Manifest
	fmt.Printf("Restored backup from %s (DROOL %s, made on %s).\n", m.CreatedAt.Format(time.RFC3339), m.DroolVersion, orDash(m.Hostname))
	fmt.Printf("  %s\n", summarizeCounts(res.Counts))
	if len(res.Migrations) > 0 {
		fmt.Printf("  Upgraded the database: %s\n", strings.Join(res.Migrations, ", "))
	}
	switch {
	case res.Discarded:
		fmt.Printf("  Stored credentials were DISCARDED. Hosts that need a credential assigned: %s\n", orDash(strings.Join(res.DiscardedFor, ", ")))
		if m.AgentKeysSealed > 0 {
			fmt.Printf("  The Agent CA keys were discarded too. Until you issue a new intermediate with the offline root key\n" +
				"  (drool agent-ca rotate --root-key FILE), the agent endpoint stays off and agent hosts use SSH fallback if configured.\n" +
				"  Agents then move to the new intermediate by themselves; no re-enrollment.\n")
		}
	case m.Credentials == 0:
		fmt.Println("  No stored credentials in this backup.")
	default:
		fmt.Printf("  %d credential(s) verified with %s.\n", res.CredentialsOK, map[string]string{
			"backup": "the master key from the backup (installed)", "file": "the --master-key file (installed)",
			"existing": "the master key already installed here"}[res.KeySource])
	}
	if res.TLSRestored {
		fmt.Println("  TLS certificate restored.")
	}
	if res.ConfigRestoredTo != "" {
		fmt.Printf("  The backup's config file was saved to %s (not applied; compare it with your current config).\n", res.ConfigRestoredTo)
	}
	fmt.Printf("  All users must sign in again (%d session(s) cleared).\n", res.SessionsCleared)
	if res.PreviousDataDir != "" {
		fmt.Printf("  The previous files were moved to %s; delete it once you're satisfied.\n", res.PreviousDataDir)
	}
	fmt.Println("\nStart DROOL again (systemctl start drool, or docker compose start drool).")
	return nil
}

func summarizeCounts(c map[string]int64) string {
	labels := map[string]string{"users": "users", "hosts": "hosts", "secrets": "credentials", "jobs": "jobs", "audit_events": "audit events"}
	keys := []string{"users", "hosts", "secrets", "jobs", "audit_events"}
	var parts []string
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%d %s", c[k], labels[k]))
	}
	return strings.Join(parts, ", ")
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

func isTerminal(f *os.File) bool {
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}
