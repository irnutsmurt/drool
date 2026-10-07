package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"gopkg.in/natefinch/lumberjack.v2"
)

// OpLog is the agent's own record of what it was asked to do (DESIGN §4.2):
// operation ids, types, the controller that asked, timestamps and outcomes.
// It never contains secrets, command text or file contents (only hashes,
// paths and names), and there is no RPC to read back, rewrite or delete
// it; the agent's file capability can't reach its directory either.
type OpLog struct {
	mu sync.Mutex
	w  *lumberjack.Logger
}

// OpEvent is one log line.
type OpEvent struct {
	Time       time.Time `json:"time"`
	Event      string    `json:"event"` // received | rejected | accepted | completed | interrupted | enrolled | renewed | recovered | update | manifest | config
	OpID       string    `json:"op_id,omitempty"`
	Type       string    `json:"type,omitempty"`
	Controller string    `json:"controller,omitempty"` // controller certificate fingerprint
	JobID      string    `json:"job_id,omitempty"`
	Summary    string    `json:"summary,omitempty"`     // non-secret: path, package or unit names
	OpHash     string    `json:"op_sha256,omitempty"`   // of the operation as received
	Outcome    string    `json:"outcome,omitempty"`     // ok | failed | rejected
	ErrorClass string    `json:"error_class,omitempty"` // no messages: they can echo input
	Reason     string    `json:"reason,omitempty"`      // rejection reason code
}

// OpenOpLog opens <dir>/operations.log (0600), rotated at 10 MiB, keeping
// ten compressed generations.
func OpenOpLog(dir string) (*OpLog, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	name := filepath.Join(dir, "operations.log")
	f, err := os.OpenFile(name, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	f.Close()
	return &OpLog{w: &lumberjack.Logger{Filename: name, MaxSize: 10, MaxBackups: 10, Compress: true}}, nil
}

// Write appends an event.
func (l *OpLog) Write(e OpEvent) {
	if l == nil {
		return
	}
	if e.Time.IsZero() {
		e.Time = time.Now().UTC()
	}
	b, _ := json.Marshal(e)
	l.mu.Lock()
	defer l.mu.Unlock()
	l.w.Write(append(b, '\n'))
}

// Close flushes the log.
func (l *OpLog) Close() error {
	if l == nil {
		return nil
	}
	return l.w.Close()
}
