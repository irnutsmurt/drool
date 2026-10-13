package main

import (
	"bufio"
	"bytes"
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
	if len(args) > 0 && args[0] == "verify" {
		return cmdBackupVerify(args[1:])
	}
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

	if err := checkRestoreJournal(cfg); err != nil {
		return err
	}
	dest := *out
	if dest == "" {
		dest = filepath.Join(cfg.DataDir, "backups", "drool-backup-"+time.Now().UTC().Format("20060102-150405")+".tar.gz")
	}
	opts := backup.Options{IncludeMasterKey: *withKey}
	var res *backup.Result
	if toStdout {
		res, err = backup.Create(ctx, cfg, os.Stdout, opts)
	} else {
		// Written to a temporary file, synced and renamed: never a partial
		// file under the backup's name.
		res, err = backup.CreateFile(ctx, cfg, dest, opts)
	}
	if err != nil {
		return err
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
	keyFile := fs.String("master-key", "", "master key file for a backup made without --include-master-key (\"-\": read it from standard input, ahead of the backup when that is \"-\" too)")
	discard := fs.Bool("discard-credentials", false, "if no matching master key is available, restore everything except stored credentials (they are deleted and must be added again)")
	yes := fs.Bool("yes", false, "don't ask for confirmation")
	recoverFlag := fs.Bool("recover", false, "deal with a restore that was interrupted (crash, power loss): put the previous installation back, or finish the restore if it had completed")
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, `Usage: drool restore [options] FILE   ("-" reads from standard input)
       drool restore --recover          (after a restore was interrupted)

Replaces this controller's database, TLS material and (if included) master
key with the backup. Stop the controller first. The current files are moved
to <data_dir>/pre-restore-<time>/, and nothing is changed unless the whole
backup checks out.

With a master key file kept apart from the backup, pipe both in, the key
first, so that only you need to be able to read the key file (in a container,
nothing has to be mounted):

  cat master.key drool-backup.tar.gz | drool restore --master-key - -

Options:
`)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *recoverFlag {
		cfg, err := loadConfig(path, explicit)
		if err != nil {
			return err
		}
		res, err := backup.Recover(cfg)
		if err != nil {
			return err
		}
		if res.Completed {
			fmt.Println("The interrupted restore had already put every file in place; it is finished now.")
		} else {
			fmt.Printf("Put the previous installation back (%d file move(s) undone); the restore didn't happen.\n", res.RolledBack)
		}
		fmt.Println("Start DROOL again (systemctl start drool, or docker compose start drool).")
		return nil
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
	key, r, err := masterKeyAndBackup("restore", *keyFile, src)
	if err != nil {
		return err
	}
	if f, ok := r.(*os.File); ok && f != os.Stdin {
		defer f.Close()
	}
	if !*yes && src != "-" && isTerminal(os.Stdin) {
		fmt.Fprintf(os.Stderr, "Replace the DROOL data in %s with %s? [y/N] ", cfg.DataDir, src)
		var answer string
		fmt.Scanln(&answer)
		if a := strings.ToLower(strings.TrimSpace(answer)); a != "y" && a != "yes" {
			return errors.New("cancelled")
		}
	}

	opts := backup.RestoreOptions{MasterKeyFile: *keyFile, DiscardCredentials: *discard}
	if key != nil {
		opts.MasterKeyFile, opts.MasterKey = "", key
	}
	res, err := backup.Restore(context.Background(), cfg, r, opts)
	var ke *backup.KeyError
	if errors.As(err, &ke) {
		return fmt.Errorf(`%v.

Nothing was changed. To restore, do one of:
  - supply the master key file it was made with:  drool restore --master-key /path/to/master.key %s
    or pipe it in ahead of the backup:            cat master.key BACKUP | drool restore --master-key - -
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
	if rc := res.Reconcile; rc != nil {
		fmt.Printf(`
The restored controller has a new controller epoch (%s, was %s). Its hosts
still answer to the backup's: once DROOL is started, it compares each host
with the restored data and moves it to the new epoch when nothing would be
lost or rolled back; hosts changed after the backup wait for you
(Settings → System → Restore). Set aside, never repeated automatically:
`, shortEpoch(rc.Epoch), shortEpoch(rc.PreviousEpoch))
		fmt.Printf("  %d unfinished job(s) marked interrupted (outcome unknown)\n", rc.JobsInterrupted)
		fmt.Printf("  %d open firewall transaction(s) closed as unknown\n", rc.FirewallClosed)
		fmt.Printf("  %d unsent notification(s) dropped, %d queued scheduled run(s) skipped\n", rc.DeliveriesDropped, rc.OccurrencesDrop)
		fmt.Printf("  %d job schedule(s) held, %d automatic Desired State restore(s) paused, %d Shepherd rollout(s) paused\n",
			len(rc.SchedulesHeld), len(rc.AutoPaused), rc.RolloutsPaused)
	}
	fmt.Println("\nStart DROOL again (systemctl start drool, or docker compose start drool).")
	return nil
}

func shortEpoch(e string) string {
	if len(e) > 12 {
		return e[:12]
	}
	if e == "" {
		return "none"
	}
	return e
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

// maxKeyFile bounds what --master-key - reads: a master key file is a few
// short lines.
const maxKeyFile = 64 << 10

// masterKeyAndBackup opens the backup to read (src, "-" for standard input)
// and, for --master-key -, reads the master key file from standard input
// instead of a path. With "-" for both, standard input carries the key file
// first and the backup after it (cat master.key backup.tar.gz | drool restore
// --master-key - -); a backup is gzip, so it begins where gzip's magic bytes
// do, which never occur in a key file. This is how a container's unprivileged
// user gets the key without the operator's copy being made readable to it.
func masterKeyAndBackup(cmd, keyFlag, src string) (key []byte, backupR io.Reader, err error) {
	if keyFlag == "-" {
		example := "cat master.key drool-backup.tar.gz | drool " + cmd + " --master-key - -"
		if isTerminal(os.Stdin) {
			return nil, nil, errors.New("--master-key -: pipe the master key file into standard input, e.g. " + example)
		}
		var rest io.Reader
		if key, rest, err = splitKeyAndBackup(os.Stdin, src == "-", example); err != nil {
			return nil, nil, err
		}
		if src == "-" {
			return key, rest, nil
		}
	}
	if src == "-" {
		return key, os.Stdin, nil
	}
	f, err := os.Open(src)
	if err != nil {
		return nil, nil, err
	}
	return key, f, nil
}

// splitKeyAndBackup reads a master key file from in; with backupFollows, it
// stops where the backup's gzip stream begins and returns the rest of in.
// example is the command line shown in errors.
func splitKeyAndBackup(in io.Reader, backupFollows bool, example string) ([]byte, io.Reader, error) {
	hint := " (send the key file first, then the backup: " + example + ")"
	br := bufio.NewReader(in)
	var key bytes.Buffer
	for {
		if p, _ := br.Peek(2); backupFollows && len(p) == 2 && p[0] == 0x1f && p[1] == 0x8b {
			break
		}
		b, err := br.ReadByte()
		if err == io.EOF {
			if backupFollows {
				return nil, nil, errors.New("--master-key -: standard input ended without a backup after the master key" + hint)
			}
			break
		}
		if err != nil {
			return nil, nil, fmt.Errorf("--master-key -: %w", err)
		}
		if key.WriteByte(b); key.Len() > maxKeyFile {
			return nil, nil, errors.New("--master-key -: standard input doesn't start with a master key file" + hint)
		}
	}
	if len(bytes.TrimSpace(key.Bytes())) == 0 {
		if backupFollows {
			return nil, nil, errors.New("--master-key -: the backup on standard input has no master key file in front of it" + hint)
		}
		return nil, nil, errors.New("--master-key -: standard input is empty; pipe the master key file in")
	}
	return key.Bytes(), br, nil
}

func isTerminal(f *os.File) bool {
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

// errIncomplete: drool backup verify's exit status 2 (restorable, but its
// credentials need a key it doesn't include).
var errIncomplete = errors.New("restorable, but not complete on its own")

func cmdBackupVerify(args []string) error {
	fs := flag.NewFlagSet("backup verify", flag.ContinueOnError)
	keyFile := fs.String("master-key", "", "master key file to check the credentials with, for a backup made without --include-master-key (\"-\": read it from standard input, ahead of the backup when that is \"-\" too)")
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, `Usage: drool backup verify [--master-key FILE] FILE   ("-" reads from standard input)

Checks a backup without restoring it, and without touching any installation:
the archive and its checksums, the database's integrity, that it upgrades to
this DROOL's schema (on a temporary copy), which master key opens its stored
credentials, and its audit history.

Exit status: 0 restorable and complete; 2 restorable, but its credentials
need a master key it doesn't include; 1 not restorable.

With a master key file kept apart from the backup, pipe both in, the key
first: cat master.key drool-backup.tar.gz | drool backup verify --master-key - -

Options:
`)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return errors.New("backup verify needs exactly one backup file")
	}
	key, r, err := masterKeyAndBackup("backup verify", *keyFile, fs.Arg(0))
	if err != nil {
		return err
	}
	if f, ok := r.(*os.File); ok && f != os.Stdin {
		defer f.Close()
	}
	vopts := backup.VerifyOptions{MasterKeyFile: *keyFile}
	if key != nil {
		vopts.MasterKeyFile, vopts.MasterKey = "", key
	}
	rep, err := backup.Verify(context.Background(), r, vopts)
	if err != nil {
		return err
	}
	if m := rep.Manifest; m != nil {
		fmt.Printf("Backup made %s on %s by DROOL %s (schema %d).\n", m.CreatedAt.Format(time.RFC3339), orDash(m.Hostname), m.DroolVersion, m.SchemaVersion)
		fmt.Printf("  Archive: intact (%d files, checksums match); %s\n", rep.Files, summarizeCounts(m.Counts))
		if len(rep.Migrations) > 0 {
			fmt.Printf("  Database: integrity check passed; restoring it here applies %d migration(s) up to schema %d\n", len(rep.Migrations), store.LatestVersion())
		} else if rep.Restorable() {
			fmt.Println("  Database: integrity check passed; same schema as this DROOL")
		}
		switch rep.KeySource {
		case "backup":
			fmt.Printf("  Credentials: %d sealed value(s) open with the master key in the backup\n", rep.Sealed)
		case "file":
			fmt.Printf("  Credentials: %d sealed value(s) open with %s\n", rep.Sealed, *keyFile)
		case "none needed":
			fmt.Println("  Credentials: none stored")
		}
		if a := rep.Audit; a != nil && a.OK {
			sealed := "not sealed by a checkpoint yet"
			if a.SealedThrough > 0 {
				sealed = fmt.Sprintf("sealed through #%d", a.SealedThrough)
			}
			fmt.Printf("  Audit history: %d records verified (%s)\n", a.Records+a.LegacyRecords, sealed)
		}
	}
	for _, w := range rep.Warnings {
		fmt.Printf("  Note: %s.\n", w)
	}
	switch {
	case !rep.Restorable():
		fmt.Println("NOT RESTORABLE:")
		for _, p := range rep.Problems {
			fmt.Printf("  - %s\n", p)
		}
		return errors.New("the backup can't be restored")
	case !rep.Complete():
		fmt.Println("Restorable, but not complete on its own (see the note above).")
		return errIncomplete
	}
	fmt.Println("Restorable and complete.")
	return nil
}
