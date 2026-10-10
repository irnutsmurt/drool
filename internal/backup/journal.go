package backup

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"drool/internal/config"
	"drool/internal/store"
)

// The restore journal (M16, ARCHITECTURE-ALPHA §33.6). Restore swaps files
// with renames inside the data directory; before each rename the journal
// records it. A restore that completes removes the journal; one that fails
// puts every file back and removes it. Only a crash part-way leaves it,
// and then DROOL refuses to start (an empty database would otherwise be
// created and run as a new installation) until drool restore --recover
// puts the previous installation back.

// JournalName is the restore journal in the data directory.
const JournalName = "restore.journal"

type journalMove struct {
	From string `json:"from"`
	To   string `json:"to"`
}

type journal struct {
	Version       int           `json:"version"`
	Started       time.Time     `json:"started_at"`
	BackupCreated time.Time     `json:"backup_created_at"`
	PrevDir       string        `json:"previous_dir"`
	Moves         []journalMove `json:"moves"` // in order; the last may not have happened
	KeyPath       string        `json:"key_path,omitempty"`
	KeyBackup     string        `json:"key_backup,omitempty"` // the replaced master key ("" when there was none)
	KeyInstalled  bool          `json:"key_installed"`        // the backup's key may have been written
	Committed     bool          `json:"committed"`            // every file was in place
}

// errSimulatedCrash and testCrashAfterMove let tests stop a restore after
// a given move as if the process had died there.
var (
	errSimulatedCrash  = errors.New("simulated crash")
	testCrashAfterMove = 0
)

func (j *journal) save(dataDir string) error {
	b, err := json.MarshalIndent(j, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(dataDir, JournalName), b)
}

// finish marks the restore complete, then removes the journal.
func (j *journal) finish(dataDir string) error {
	j.Committed = true
	if err := j.save(dataDir); err != nil {
		return err
	}
	if err := os.Remove(filepath.Join(dataDir, JournalName)); err != nil {
		return err
	}
	return SyncDir(dataDir)
}

// rollBack undoes the recorded moves, newest first, and the master key.
func (j *journal) rollBack() error {
	var problems []string
	for i := len(j.Moves) - 1; i >= 0; i-- {
		m := j.Moves[i]
		_, toErr := os.Lstat(m.To)
		_, fromErr := os.Lstat(m.From)
		switch {
		case toErr == nil && os.IsNotExist(fromErr):
			if err := os.Rename(m.To, m.From); err != nil {
				// Restored data moved in from a staging directory that is
				// gone: it is a copy (the archive has it), so it can go.
				if strings.Contains(m.From, string(filepath.Separator)+".restore-") && os.RemoveAll(m.To) == nil {
					continue
				}
				problems = append(problems, fmt.Sprintf("move %s back to %s: %v", m.To, m.From, err))
			}
		case os.IsNotExist(toErr) && fromErr == nil:
			// Never moved (the crash came between the journal and the rename).
		case toErr == nil && fromErr == nil:
			problems = append(problems, fmt.Sprintf("both %s and %s exist; left as they are", m.From, m.To))
		default:
			problems = append(problems, fmt.Sprintf("neither %s nor %s exists", m.From, m.To))
		}
	}
	if j.KeyInstalled {
		if j.KeyBackup != "" {
			if data, err := os.ReadFile(j.KeyBackup); err == nil {
				if err := writeFileAtomic(j.KeyPath, data); err != nil {
					problems = append(problems, fmt.Sprintf("put the master key back from %s: %v", j.KeyBackup, err))
				}
			} else {
				problems = append(problems, fmt.Sprintf("read the saved master key %s: %v", j.KeyBackup, err))
			}
		} else if err := os.Remove(j.KeyPath); err != nil && !os.IsNotExist(err) {
			problems = append(problems, fmt.Sprintf("remove the restored master key: %v", err))
		}
	}
	for _, d := range []string{filepath.Dir(j.PrevDir), j.PrevDir} {
		SyncDir(d)
	}
	if len(problems) > 0 {
		return errors.New(strings.Join(problems, "; "))
	}
	return nil
}

func readJournal(dataDir string) (*journal, error) {
	b, err := os.ReadFile(filepath.Join(dataDir, JournalName))
	if err != nil {
		return nil, err
	}
	var j journal
	if err := json.Unmarshal(b, &j); err != nil {
		return nil, fmt.Errorf("the restore journal %s is unreadable (%v): repair the data directory by hand from %s",
			filepath.Join(dataDir, JournalName), err, "the pre-restore-* directory")
	}
	return &j, nil
}

// ErrInterruptedRestore: a restore was interrupted in the data directory.
var ErrInterruptedRestore = errors.New("a restore was interrupted")

// CheckJournal refuses a data directory with an interrupted restore.
func CheckJournal(dataDir string) error {
	j, err := readJournal(dataDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInterruptedRestore, err)
	}
	return fmt.Errorf("%w on %s in %s (journal %s): DROOL doesn't start on a half-restored data directory. "+
		"Run drool restore --recover to put the previous installation back (or finish the restore, if it had completed)",
		ErrInterruptedRestore, j.Started.Format(time.RFC3339), dataDir, filepath.Join(dataDir, JournalName))
}

// RecoverResult says what Recover did.
type RecoverResult struct {
	Completed  bool // the restore had completed: it was only finished
	RolledBack int  // moves undone
	PrevDir    string
}

// Recover deals with an interrupted restore: one that had put every file
// in place is finished; otherwise every recorded move is undone and the
// previous master key put back, which restores the installation as it was
// before the restore began.
func Recover(cfg *config.Loaded) (*RecoverResult, error) {
	release, err := store.LockDataDir(cfg.DataDir)
	if err != nil {
		return nil, err
	}
	defer release()
	j, err := readJournal(cfg.DataDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, errors.New("there is no interrupted restore to recover")
	}
	if err != nil {
		return nil, err
	}
	res := &RecoverResult{PrevDir: j.PrevDir}
	if j.Committed {
		res.Completed = true
	} else {
		if err := j.rollBack(); err != nil {
			return nil, fmt.Errorf("could not put everything back: %w. The journal %s is kept", err, filepath.Join(cfg.DataDir, JournalName))
		}
		res.RolledBack = len(j.Moves)
	}
	removeStaging(cfg.DataDir)
	if err := os.Remove(filepath.Join(cfg.DataDir, JournalName)); err != nil {
		return nil, err
	}
	return res, SyncDir(cfg.DataDir)
}

// removeStaging deletes staging directories a crash left behind.
func removeStaging(dataDir string) {
	entries, _ := os.ReadDir(dataDir)
	for _, e := range entries {
		if e.IsDir() && (strings.HasPrefix(e.Name(), ".restore-") || strings.HasPrefix(e.Name(), ".backup-")) {
			os.RemoveAll(filepath.Join(dataDir, e.Name()))
		}
	}
}
