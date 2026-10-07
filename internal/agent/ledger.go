package agent

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"

	"drool/internal/agentrpc"
)

// Ledger durably records every operation id the agent accepted, so a
// duplicate or replayed command is rejected even across restarts, and the
// outcome of an operation whose connection dropped can be reported later
// (DESIGN §9). Entries are kept until a day after their expiry: by then
// the expiry check alone rejects a replay.
type Ledger struct {
	path string
	mu   sync.Mutex
	ops  map[string]*LedgerEntry
	now  func() time.Time
}

// Ledger entry states.
const (
	OpAccepted    = "accepted"
	OpRunning     = "running"
	OpCompleted   = "completed"
	OpInterrupted = "interrupted" // the agent stopped while it ran: outcome unknown
	OpAbandoned   = "abandoned"   // never received; recorded so a late copy can't start
)

// LedgerEntry is one operation.
type LedgerEntry struct {
	State      string             `json:"state"`
	Type       string             `json:"type"`
	AcceptedAt time.Time          `json:"accepted_at"`
	ExpiresAt  time.Time          `json:"expires_at"`
	FinishedAt time.Time          `json:"finished_at,omitempty"`
	Result     *agentrpc.OpResult `json:"result,omitempty"`
}

const maxStoredResult = 64 << 10

// OpenLedger loads the ledger. Operations that were accepted or running
// when the agent last stopped are marked interrupted.
func OpenLedger(dir string) (*Ledger, error) {
	l := &Ledger{path: filepath.Join(dir, "ledger.json"), ops: map[string]*LedgerEntry{}, now: time.Now}
	b, err := os.ReadFile(l.path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if len(b) > 0 {
		if err := json.Unmarshal(b, &l.ops); err != nil {
			return nil, err
		}
	}
	changed := false
	for _, e := range l.ops {
		if e.State == OpAccepted || e.State == OpRunning {
			e.State, e.FinishedAt = OpInterrupted, l.now()
			changed = true
		}
	}
	if changed {
		if err := l.saveLocked(); err != nil {
			return nil, err
		}
	}
	return l, nil
}

func (l *Ledger) saveLocked() error {
	cutoff := l.now().Add(-24 * time.Hour)
	for id, e := range l.ops {
		if e.ExpiresAt.Before(cutoff) && e.State != OpAccepted && e.State != OpRunning {
			delete(l.ops, id)
		}
	}
	b, err := json.Marshal(l.ops)
	if err != nil {
		return err
	}
	return writeFileAtomic(l.path, b, 0o600)
}

// ErrDuplicate means the operation id was seen before.
var ErrDuplicate = errors.New("duplicate operation id")

// Accept records a new operation id durably before anything runs. It
// fails for an id that was already recorded.
func (l *Ledger) Accept(opID, typ string, expires time.Time) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, ok := l.ops[opID]; ok {
		return ErrDuplicate
	}
	l.ops[opID] = &LedgerEntry{State: OpAccepted, Type: typ, AcceptedAt: l.now(), ExpiresAt: expires}
	if err := l.saveLocked(); err != nil {
		delete(l.ops, opID)
		return err
	}
	return nil
}

// Running marks an operation as started.
func (l *Ledger) Running(opID string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if e := l.ops[opID]; e != nil {
		e.State = OpRunning
		l.saveLocked()
	}
}

// Complete stores an operation's outcome.
func (l *Ledger) Complete(opID string, r *agentrpc.OpResult) {
	l.mu.Lock()
	defer l.mu.Unlock()
	e := l.ops[opID]
	if e == nil {
		return
	}
	stored := r
	if b, _ := json.Marshal(r); len(b) > maxStoredResult {
		stored = &agentrpc.OpResult{ExitCode: r.ExitCode, Changed: r.Changed, Error: r.Error,
			Data: map[string]any{"note": "the full result was too large to keep on the agent"}}
	}
	e.State, e.FinishedAt, e.Result = OpCompleted, l.now(), stored
	l.saveLocked()
}

// Abandon returns an operation's entry, or, when the id was never
// recorded, records it as abandoned (atomically with Accept), so that a
// copy of the operation still in flight can never start afterwards.
func (l *Ledger) Abandon(opID string, expires time.Time) (LedgerEntry, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if e, ok := l.ops[opID]; ok {
		return *e, true
	}
	l.ops[opID] = &LedgerEntry{State: OpAbandoned, AcceptedAt: l.now(), ExpiresAt: expires}
	l.saveLocked()
	return LedgerEntry{}, false
}

// Get returns an operation's entry.
func (l *Ledger) Get(opID string) (LedgerEntry, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	e, ok := l.ops[opID]
	if !ok {
		return LedgerEntry{}, false
	}
	return *e, true
}
