package engine

import (
	"bytes"
	"context"
	"io"
	"sync"
	"time"

	"drool/internal/store/db"
)

// Output buffering (decision D1).
const (
	flushBytes    = 64 << 10
	flushInterval = 250 * time.Millisecond
)

const (
	streamStdout = 1
	streamStderr = 2
)

var truncationMarker = []byte("\n[drool: output limit reached; further output discarded]\n")

// outputWriter buffers one host step's output, redacts known secret values,
// enforces the size cap, and writes rows to job_output in batches.
type outputWriter struct {
	e       *Engine
	job     string
	host    string
	step    int
	cap     int64
	secrets [][]byte
	maxSec  int

	mu        sync.Mutex
	raw       [3][]byte // per stream, not yet flushed (index 1 and 2)
	order     []int     // streams in order of first write since last flush
	seq       int64
	total     int64
	counts    [3]int64
	truncated bool
	timer     *time.Timer
	closed    bool
	phase     func(state string) // reports progress phases on the host row
}

// Phase implements driver.PhaseSink.
func (w *outputWriter) Phase(state string) {
	if w.phase != nil {
		w.phase(state)
	}
}

func (e *Engine) newOutput(job, host string, step int, secrets []string) *outputWriter {
	w := &outputWriter{e: e, job: job, host: host, step: step, cap: e.cfg.OutputCap}
	for _, s := range secrets {
		if len(s) >= 4 {
			w.secrets = append(w.secrets, []byte(s))
			if len(s) > w.maxSec {
				w.maxSec = len(s)
			}
		}
	}
	// Continue numbering after earlier attempts of this step.
	if n, err := e.st.R.MaxJobOutputSeq(context.Background(), db.MaxJobOutputSeqParams{JobID: job, HostID: host, StepIndex: int64(step)}); err == nil {
		w.seq = n
	}
	return w
}

type streamWriter struct {
	w      *outputWriter
	stream int
}

func (s streamWriter) Write(p []byte) (int, error) { s.w.write(s.stream, p); return len(p), nil }

func (w *outputWriter) Stdout() io.Writer { return streamWriter{w, streamStdout} }
func (w *outputWriter) Stderr() io.Writer { return streamWriter{w, streamStderr} }

// Note writes a DROOL-generated line (e.g. retry markers) to stderr.
func (w *outputWriter) Note(line string) { w.write(streamStderr, []byte("[drool] "+line+"\n")) }

func (w *outputWriter) write(stream int, p []byte) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed || w.truncated || len(p) == 0 {
		return // past the cap, output is discarded without buffering
	}
	if len(w.raw[stream]) == 0 {
		w.order = append(w.order, stream)
	}
	w.raw[stream] = append(w.raw[stream], p...)
	if len(w.raw[streamStdout])+len(w.raw[streamStderr]) >= flushBytes {
		w.flushLocked(false)
		return
	}
	if w.timer == nil {
		w.timer = time.AfterFunc(flushInterval, func() {
			w.mu.Lock()
			defer w.mu.Unlock()
			w.timer = nil
			w.flushLocked(false)
		})
	}
}

// Close flushes everything and returns byte counts and truncation.
func (w *outputWriter) Close() (stdout, stderr int64, truncated bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.timer != nil {
		w.timer.Stop()
		w.timer = nil
	}
	w.flushLocked(true)
	w.closed = true
	return w.counts[streamStdout], w.counts[streamStderr], w.truncated
}

// redactCut returns the redacted prefix of buf that is safe to emit and the
// raw remainder to keep (a possible partial secret at the end).
func (w *outputWriter) redactCut(buf []byte, final bool) (out, rest []byte) {
	if len(w.secrets) == 0 {
		return buf, nil
	}
	cut := len(buf)
	if !final && w.maxSec > 1 {
		// Hold back only a tail that could be the start of a secret; holding
		// back a fixed length delays unrelated text until the stream's next
		// write, which can be minutes later and out of order.
		for _, s := range w.secrets {
			for n := min(len(s)-1, len(buf)); n > 0; n-- {
				if bytes.HasSuffix(buf, s[:n]) {
					cut = min(cut, len(buf)-n)
					break
				}
			}
		}
		// Never cut through a complete match.
		for _, s := range w.secrets {
			for i := 0; ; {
				j := bytes.Index(buf[i:], s)
				if j < 0 {
					break
				}
				start := i + j
				if start < cut && start+len(s) > cut {
					cut = start
				}
				i = start + 1
			}
		}
	}
	out = buf[:cut]
	for _, s := range w.secrets {
		out = bytes.ReplaceAll(out, s, []byte("«redacted»"))
	}
	return out, append([]byte(nil), buf[cut:]...)
}

func (w *outputWriter) flushLocked(final bool) {
	type seg struct {
		stream int
		data   []byte
	}
	var segs []seg
	order := w.order
	w.order = nil
	for _, st := range order {
		out, rest := w.redactCut(w.raw[st], final)
		w.raw[st] = rest
		if len(rest) > 0 {
			w.order = append(w.order, st)
		}
		if len(out) == 0 || w.truncated {
			continue
		}
		if room := w.cap - w.total; int64(len(out)) > room {
			out = out[:max(0, room)]
			w.truncated = true
		}
		if len(out) > 0 {
			segs = append(segs, seg{st, out})
			w.total += int64(len(out))
			w.counts[st] += int64(len(out))
		}
		if w.truncated {
			segs = append(segs, seg{streamStderr, truncationMarker})
		}
	}
	if len(segs) == 0 {
		return
	}
	ctx := context.Background()
	now := w.e.now().UnixMilli()
	err := w.e.st.InTx(ctx, func(q *db.Queries) error {
		for _, s := range segs {
			w.seq++
			if err := q.InsertJobOutput(ctx, db.InsertJobOutputParams{
				JobID: w.job, HostID: w.host, StepIndex: int64(w.step), Seq: w.seq,
				Stream: int64(s.stream), Ts: now, Chunk: s.data,
			}); err != nil {
				return err
			}
			w.e.hub.Publish(w.job, "output", OutputEvent{
				HostID: w.host, Step: w.step, Seq: w.seq, Stream: streamName(s.stream), Text: string(s.data),
			})
		}
		return nil
	})
	if err != nil {
		w.e.log.Error("store job output", "job_id", w.job, "host_id", w.host, "err", err)
	}
}

func streamName(s int) string {
	if s == streamStderr {
		return "stderr"
	}
	return "stdout"
}

// OutputEvent is the payload of an "output" event.
type OutputEvent struct {
	HostID string `json:"host_id"`
	Step   int    `json:"step"`
	Seq    int64  `json:"seq"`
	Stream string `json:"stream"`
	Text   string `json:"text"`
}
