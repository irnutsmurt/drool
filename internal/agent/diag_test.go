package agent

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestDiagHandler(t *testing.T) {
	var out bytes.Buffer
	h := newDiagHandler(slog.NewTextHandler(&out, &slog.HandlerOptions{Level: slog.LevelInfo}))
	ctx := context.Background()
	t0 := time.Now()
	rec := func(at time.Duration, l slog.Level, msg string) {
		r := slog.NewRecord(t0.Add(at), l, msg, 0)
		r.AddAttrs(slog.String("err", "x"))
		if err := h.Handle(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	rec(0, slog.LevelDebug, "routine sample")
	for i := 0; i < 3; i++ {
		rec(time.Duration(i)*time.Second, slog.LevelInfo, "connected to controller")
		rec(time.Duration(i)*time.Second, slog.LevelWarn, "collector failed")
	}
	rec(11*time.Minute, slog.LevelWarn, "collector failed")
	got := out.String()
	if strings.Contains(got, "routine sample") {
		t.Error("debug records must stay in memory")
	}
	if n := strings.Count(got, "connected to controller"); n != 3 {
		t.Errorf("info lifecycle records are all written: %d\n%s", n, got)
	}
	if n := strings.Count(got, "collector failed"); n != 2 {
		t.Errorf("repeated warnings are deduplicated: %d\n%s", n, got)
	}
	if !strings.Contains(got, "repeated_since_last=2") {
		t.Errorf("the next write counts what was suppressed:\n%s", got)
	}
	d := h.Diagnostics()
	if len(d) != 8 || d[0].Level != "DEBUG" {
		t.Errorf("the ring keeps everything: %d %+v", len(d), d[0])
	}
	for i := 0; i < diagRingSize+10; i++ {
		rec(time.Hour, slog.LevelDebug, "fill")
	}
	if d := h.Diagnostics(); len(d) != diagRingSize || d[len(d)-1].Message != "fill err=x" {
		t.Errorf("ring bounded: %d", len(d))
	}
}

func TestHeapGuard(t *testing.T) {
	run := func(seq []uint64) (tripped bool, at int) {
		i := 0
		read := func() (uint64, uint64) {
			v := seq[min(i, len(seq)-1)]
			i++
			return v, 100
		}
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() {
			heapGuard(ctx, time.Millisecond, 3, read, func(_, _ uint64) { tripped, at = true, i })
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(200 * time.Millisecond):
			cancel()
			<-done
		}
		cancel()
		return
	}
	if tripped, _ := run([]uint64{150, 150, 50, 150, 150, 50, 10}); tripped {
		t.Error("growth the garbage collector recovers from must not restart the agent")
	}
	if tripped, at := run([]uint64{50, 150, 150, 150}); !tripped || at != 4 {
		t.Errorf("sustained growth beyond the limit restarts it: %v at %d", tripped, at)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	heapGuard(ctx, time.Millisecond, 1, func() (uint64, uint64) { return 200, 100 }, func(_, _ uint64) { t.Error("stopped guard tripped") })
}
