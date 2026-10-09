package desired

import (
	"database/sql"
	"sync"
)

// emitRegistry holds audit log-stream mirrors until their transaction
// commits (audit.Writer.RecordTx).
type emitRegistry struct {
	mu sync.Mutex
	m  map[*sql.Tx][]func()
}

var pendingEmits = &emitRegistry{m: map[*sql.Tx][]func(){}}

func (r *emitRegistry) add(tx *sql.Tx, f func()) {
	r.mu.Lock()
	r.m[tx] = append(r.m[tx], f)
	r.mu.Unlock()
}

func (r *emitRegistry) take(tx *sql.Tx) []func() {
	r.mu.Lock()
	defer r.mu.Unlock()
	fs := r.m[tx]
	delete(r.m, tx)
	return fs
}
