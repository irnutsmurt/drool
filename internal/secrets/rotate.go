package secrets

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Master key rotation (M16, ARCHITECTURE-ALPHA §33.7): introduce → migrate →
// verify → retire, each step leaving a state the controller starts from.
//
//  1. The current key file is copied aside (master.key.previous-<time>).
//  2. Introduce: a new key version is added to the key file; values sealed
//     with the old one still open, new ones would use the new one.
//  3. Migrate: every sealed value (sealedLocations) is re-sealed with the new
//     version in one transaction.
//  4. Verify: the new version alone opens every value.
//  5. Retire: the old version is removed from the key file (unless kept).
//
// A crash between any two steps leaves a key file that opens every stored
// value. The controller must be stopped: it seals with the key it loaded.

// testRotateStopAfter stops a rotation after a step (tests: as if the
// process had died there); 0: never.
var testRotateStopAfter = 0

var errStopped = fmt.Errorf("stopped (test)")

// RotateResult describes a rotation.
type RotateResult struct {
	OldVersion, NewVersion int
	Resealed               map[string]int // per table
	Total                  int
	PreviousKeyFile        string // the key file before the rotation (all old versions)
	KeptPrevious           bool
	OldKeyID, NewKeyID     string
}

// RotateMasterKey rotates the master key at keyPath over the database q.
func RotateMasterKey(ctx context.Context, db *sql.DB, keyPath string, keepPrevious bool, now time.Time) (*RotateResult, error) {
	old, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, fmt.Errorf("read master key: %w", err)
	}
	box, err := ParseKeyFile(old)
	if err != nil {
		return nil, fmt.Errorf("master key %s: %w", keyPath, err)
	}
	if _, err := VerifyBox(ctx, db, box); err != nil {
		return nil, fmt.Errorf("the current master key doesn't open every stored value, so nothing was rotated: %w", err)
	}
	res := &RotateResult{OldVersion: box.CurrentVersion(), NewVersion: box.CurrentVersion() + 1, Resealed: map[string]int{},
		KeptPrevious: keepPrevious, OldKeyID: box.KeyID()}

	// 1. The previous key file, for backups made before the rotation.
	res.PreviousKeyFile = filepath.Join(filepath.Dir(keyPath), filepath.Base(keyPath)+".previous-"+now.UTC().Format("20060102-150405"))
	if err := writeKeyFile(res.PreviousKeyFile, old); err != nil {
		return nil, err
	}
	// 2. Introduce.
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	line := fmt.Sprintf("%d:%s\n", res.NewVersion, base64.StdEncoding.EncodeToString(key))
	both := append(append([]byte{}, old...), []byte("\n"+line)...)
	if err := writeKeyFile(keyPath, both); err != nil {
		return nil, fmt.Errorf("add the new key version: %w", err)
	}
	if testRotateStopAfter == 2 {
		return nil, errStopped
	}
	box, err = ParseKeyFile(both)
	if err != nil {
		return nil, err
	}
	// 3. Migrate.
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	err = eachSealed(ctx, tx, func(loc *sealedLocation, v sealedValue) error {
		pt, err := box.Open(v.ID, v.Kind, v.Ciphertext, v.Nonce, v.Version)
		if err != nil {
			return fmt.Errorf("%s: %w", v.Label, err)
		}
		ct, nonce, ver, err := box.Seal(v.ID, v.Kind, pt)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, loc.Update, ct, nonce, ver, v.ID); err != nil {
			return fmt.Errorf("re-seal %s: %w", v.Label, err)
		}
		res.Resealed[loc.Table]++
		res.Total++
		return nil
	})
	if err != nil {
		tx.Rollback()
		return nil, fmt.Errorf("re-seal the stored values (nothing was changed in the database; the key file has both versions): %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	if testRotateStopAfter == 3 {
		return nil, errStopped
	}
	// 4. Verify with the new version alone.
	only, err := ParseKeyFile([]byte(line))
	if err != nil {
		return nil, err
	}
	if _, err := VerifyBox(ctx, db, only); err != nil {
		return nil, fmt.Errorf("after re-sealing, the new key version doesn't open everything (the key file keeps both versions): %w", err)
	}
	res.NewKeyID = box.KeyID()
	// 5. Retire.
	if !keepPrevious {
		if err := writeKeyFile(keyPath, []byte(line)); err != nil {
			return nil, fmt.Errorf("remove the old key version (the key file keeps both, which still works): %w", err)
		}
		res.NewKeyID = only.KeyID()
	}
	return res, nil
}

// writeKeyFile replaces a key file atomically (0600, synced, directory
// synced).
func writeKeyFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, ".tmp-"+filepath.Base(path)+"-")
	if err != nil {
		return err
	}
	tmp := f.Name()
	ok := false
	defer func() {
		if !ok {
			f.Close()
			os.Remove(tmp)
		}
	}()
	if err := f.Chmod(0o600); err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	ok = true
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
