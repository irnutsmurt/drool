package agent

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// diagHandler keeps the agent's logging cheap on flash: every record,
// including debug-level routine diagnostics, goes to a bounded in-memory
// ring (sent to the controller on request); only Info and above reach
// the journal, and a warning or error repeating within dedupWindow is
// written once, then summarised with a count when it next appears.
// Info records (connections, applied changes, updates) are lifecycle
// events: infrequent and each one worth keeping.
type diagHandler struct {
	inner slog.Handler
	attrs []slog.Attr
	group string
	st    *diagState
}

type diagState struct {
	mu   sync.Mutex
	ring []DiagEntry
	next int
	full bool
	seen map[string]*dedup
}

type dedup struct {
	last       time.Time
	suppressed int
}

// DiagEntry is one ring record.
type DiagEntry struct {
	Time    time.Time `json:"time"`
	Level   string    `json:"level"`
	Message string    `json:"message"`
}

const (
	diagRingSize = 256
	dedupWindow  = 10 * time.Minute
	maxDedupKeys = 512
)

func newDiagHandler(inner slog.Handler) *diagHandler {
	return &diagHandler{inner: inner, st: &diagState{ring: make([]DiagEntry, diagRingSize), seen: map[string]*dedup{}}}
}

func (h *diagHandler) Enabled(_ context.Context, l slog.Level) bool { return true }

func (h *diagHandler) Handle(ctx context.Context, r slog.Record) error {
	var b strings.Builder
	b.WriteString(r.Message)
	for _, a := range h.attrs {
		fmt.Fprintf(&b, " %s=%v", a.Key, a.Value)
	}
	r.Attrs(func(a slog.Attr) bool {
		fmt.Fprintf(&b, " %s=%v", a.Key, a.Value)
		return true
	})
	line := b.String()
	st := h.st
	st.mu.Lock()
	st.ring[st.next] = DiagEntry{Time: r.Time, Level: r.Level.String(), Message: line}
	st.next = (st.next + 1) % diagRingSize
	st.full = st.full || st.next == 0
	write := r.Level >= slog.LevelInfo
	suppressed := 0
	if r.Level >= slog.LevelWarn {
		key := r.Level.String() + "|" + line
		d := st.seen[key]
		switch {
		case d != nil && r.Time.Sub(d.last) < dedupWindow:
			d.suppressed++
			write = false
		case d != nil:
			suppressed = d.suppressed
			d.last, d.suppressed = r.Time, 0
		default:
			if len(st.seen) >= maxDedupKeys {
				for k, v := range st.seen { // forget stale entries
					if r.Time.Sub(v.last) >= dedupWindow {
						delete(st.seen, k)
					}
				}
			}
			if len(st.seen) < maxDedupKeys {
				st.seen[key] = &dedup{last: r.Time}
			}
		}
	}
	st.mu.Unlock()
	if !write || !h.inner.Enabled(ctx, r.Level) {
		return nil
	}
	if suppressed > 0 {
		r.AddAttrs(slog.Int("repeated_since_last", suppressed))
	}
	return h.inner.Handle(ctx, r)
}

func (h *diagHandler) WithAttrs(as []slog.Attr) slog.Handler {
	return &diagHandler{inner: h.inner.WithAttrs(as), attrs: append(append([]slog.Attr{}, h.attrs...), as...), group: h.group, st: h.st}
}

func (h *diagHandler) WithGroup(name string) slog.Handler {
	return &diagHandler{inner: h.inner.WithGroup(name), attrs: h.attrs, group: name, st: h.st}
}

// Diagnostics returns the ring, oldest first.
func (h *diagHandler) Diagnostics() []DiagEntry {
	st := h.st
	st.mu.Lock()
	defer st.mu.Unlock()
	var out []DiagEntry
	if st.full {
		out = append(out, st.ring[st.next:]...)
	}
	out = append(out, st.ring[:st.next]...)
	return out
}
