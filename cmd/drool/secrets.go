package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"sort"
	"strings"
	"time"

	"drool/internal/audit"
	"drool/internal/logging"
	"drool/internal/secrets"
	"drool/internal/store"
)

func cmdSecrets(path string, explicit bool, args []string) error {
	if len(args) == 0 || args[0] != "rotate-key" {
		return errors.New("usage: drool secrets rotate-key [--keep-previous]")
	}
	fs := flag.NewFlagSet("secrets rotate-key", flag.ContinueOnError)
	keep := fs.Bool("keep-previous", false, "keep the old key version in the key file (to restore older backups here without the saved copy)")
	fs.Usage = func() {
		fmt.Print(`Usage: drool secrets rotate-key [--keep-previous]

Replaces the master key that seals stored credentials, CA keys and other
secrets: adds a new key version, re-seals every stored value with it in one
transaction, checks that the new version alone opens everything, then
removes the old version from the key file. The key file as it was is saved
next to it first: backups made before the rotation need it. Stop DROOL
first (systemctl stop drool, or docker compose stop drool).

Options:
`)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	cfg, err := loadConfig(path, explicit)
	if err != nil {
		return err
	}
	unlock, err := store.LockDataDir(cfg.DataDir)
	if err != nil {
		if errors.Is(err, store.ErrLocked) {
			return fmt.Errorf("%w; stop it first: the running controller seals new values with the key it loaded", err)
		}
		return err
	}
	defer unlock()
	ctx := context.Background()
	st, _, err := openStore(ctx, cfg)
	if err != nil {
		return err
	}
	defer st.Close()
	res, err := secrets.RotateMasterKey(ctx, st.Writer, cfg.MasterKeyPath(), *keep, time.Now())
	if err != nil {
		return err
	}
	actor := audit.Actor{Type: audit.ActorSystem, Name: "cli:" + currentOSUser()}
	if err := audit.NewWriter(st, logging.Discard().Audit).Record(ctx, audit.Event{Action: "master_key.rotate", Actor: &actor,
		TargetType: "controller", Details: map[string]any{"old_version": res.OldVersion, "new_version": res.NewVersion,
			"resealed": res.Total, "old_key_id": res.OldKeyID, "new_key_id": res.NewKeyID, "kept_previous": res.KeptPrevious}}); err != nil {
		return fmt.Errorf("the key was rotated, but recording it in the audit log failed: %w", err)
	}
	tables := make([]string, 0, len(res.Resealed))
	for t, n := range res.Resealed {
		tables = append(tables, fmt.Sprintf("%s %d", t, n))
	}
	sort.Strings(tables)
	fmt.Printf("Rotated the master key: version %d → %d (key id %s → %s).\n", res.OldVersion, res.NewVersion, res.OldKeyID, res.NewKeyID)
	fmt.Printf("  Re-sealed %d stored value(s)%s; the new version alone opens all of them.\n", res.Total, map[bool]string{true: " (" + strings.Join(tables, ", ") + ")", false: ""}[len(tables) > 0])
	if res.KeptPrevious {
		fmt.Println("  The old version stays in the key file (--keep-previous): older backups restore here as before.")
	} else {
		fmt.Println("  The old version was removed from the key file.")
	}
	fmt.Printf(`
The key file as it was is saved at:
    %s
Backups made before now need it to restore their credentials (drool restore
--master-key). Keep it with those backups, offline, then delete it here.
Backups made with --include-master-key before now contain the old key.

Make a new backup once DROOL runs again. Audit checkpoints made with the old
version can only be checked structurally from now on; DROOL seals the whole
history with the new version when it starts.
`, res.PreviousKeyFile)
	return nil
}
