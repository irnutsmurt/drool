package sched

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"drool/internal/driver"
	"drool/internal/model"
)

// TestSchedulerScale (DROOL_SCALE_TEST=1): thousands of schedules, a burst
// of them due in the same minute, and another writer (what ingestion,
// alerts and agents do) working throughout. The scheduler must create
// exactly one job per occurrence, find due work through its index, keep
// its own memory and goroutines bounded (the engine's jobs are ordinary
// jobs), and leave the database writable for everyone else.
func TestSchedulerScale(t *testing.T) {
	if os.Getenv("DROOL_SCALE_TEST") == "" {
		t.Skip("set DROOL_SCALE_TEST=1")
	}
	const burst, idle, disabled = 3000, 2000, 1000
	e := newEnv(t)

	// The burst: due at 12:01 UTC on the test's Friday. Read-only steps,
	// so jobs on the same hosts may overlap (host locks aren't what is
	// measured here).
	start := time.Now()
	in := ScheduleInput{Kind: ScheduleJob, Config: Config{
		Recurrence: Recurrence{Kind: KindDaily, Time: "12:01"}, TimeZone: "UTC",
		Steps: []model.Operation{{Type: model.OpCommand, Command: &model.CommandOp{Command: "true", ReadOnly: true}}}}}
	var first string
	for i := range burst {
		in.Name = fmt.Sprintf("burst-%04d", i)
		in.Config.Targets = model.TargetSpec{Hosts: []string{fmt.Sprintf("h%02d", i%12+1)}}
		sc := e.create(in)
		if first == "" {
			first = sc.ID
		}
	}
	createdIn := time.Since(start)
	// Schedules that aren't due, and disabled ones, cloned in bulk.
	for i := range idle + disabled {
		enabled, next := 1, t0.Add(48*time.Hour).UnixMilli()
		if i >= idle {
			enabled, next = 0, t0.Add(time.Minute).UnixMilli()
		}
		if _, err := e.st.Writer.Exec(`INSERT INTO schedules (id, kind, name, enabled, owner_id, revision, config_json, next_run_at, created_at, updated_at)
			SELECT ?, kind, ?, ?, owner_id, revision, config_json, ?, created_at, updated_at FROM schedules WHERE id = ?`,
			fmt.Sprintf("sch_clone%05d", i), fmt.Sprintf("clone-%05d", i), enabled, next, first); err != nil {
			t.Fatal(err)
		}
	}

	// Due work is found through the partial index, not a table scan.
	var plan []string
	rows, err := e.st.Reader.Query(`EXPLAIN QUERY PLAN SELECT `+rowCols+` FROM schedules
		WHERE enabled = 1 AND archived_at IS NULL AND next_run_at <= ? ORDER BY next_run_at LIMIT ?`, t0.UnixMilli(), 100)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id, parent, notused int
		var detail string
		rows.Scan(&id, &parent, &notused, &detail)
		plan = append(plan, detail)
	}
	rows.Close()
	if p := strings.Join(plan, "; "); !strings.Contains(p, "schedules_due") || strings.Contains(p, "TEMP B-TREE") {
		t.Fatalf("due query plan: %s", p)
	}
	// With nothing due, a tick is one cheap query.
	quiet := time.Now()
	e.tick()
	quietTick := time.Since(quiet)

	// Another writer, all the way through the burst.
	var lat []time.Duration
	var latMu sync.Mutex
	stop := make(chan struct{})
	var writerDone sync.WaitGroup
	writerDone.Add(1)
	go func() {
		defer writerDone.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			t1 := time.Now()
			if _, err := e.st.Writer.Exec(`INSERT INTO mon_events (host_id, ts, kind, subject, severity, message, data_json) VALUES ('h01', ?, 'scale', '', 'info', 'writer', '')`, i); err != nil {
				t.Error(err)
				return
			}
			latMu.Lock()
			lat = append(lat, time.Since(t1))
			latMu.Unlock()
			time.Sleep(2 * time.Millisecond)
		}
	}()
	// Goroutines and heap, sampled.
	var peakG, peakHeap atomic.Int64
	sampling := make(chan struct{})
	var sampled sync.WaitGroup
	sampled.Add(1)
	go func() {
		defer sampled.Done()
		var ms runtime.MemStats
		for {
			if g := int64(runtime.NumGoroutine()); g > peakG.Load() {
				peakG.Store(g)
			}
			runtime.ReadMemStats(&ms)
			if h := int64(ms.HeapInuse); h > peakHeap.Load() {
				peakHeap.Store(h)
			}
			select {
			case <-sampling:
				return
			case <-time.After(20 * time.Millisecond):
			}
		}
	}()
	runtime.GC()
	baseG := runtime.NumGoroutine()

	e.clk.set(t0.Add(time.Minute + 2*time.Second))
	tickStart := time.Now()
	e.tick()
	tickDur := time.Since(tickStart)
	e.eng.Wait() // every job finished
	allDone := time.Since(tickStart)
	close(stop)
	writerDone.Wait()
	close(sampling)
	sampled.Wait()
	runtime.GC()
	time.Sleep(100 * time.Millisecond)
	afterG := runtime.NumGoroutine()

	count := func(q string, args ...any) int {
		var n int
		if err := e.st.Reader.QueryRow(q, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := count(`SELECT count(*) FROM jobs WHERE schedule_id IS NOT NULL`); n != burst {
		t.Fatalf("%d jobs for %d due occurrences", n, burst)
	}
	if n := count(`SELECT count(*) FROM (SELECT schedule_id FROM schedule_occurrences GROUP BY schedule_id, scheduled_for HAVING count(*) > 1)`); n != 0 {
		t.Fatalf("%d duplicated occurrences", n)
	}
	if n := count(`SELECT count(*) FROM jobs WHERE schedule_id IS NOT NULL AND status != 'completed'`); n != 0 {
		t.Fatalf("%d jobs didn't complete", n)
	}
	if n := count(`SELECT count(*) FROM schedule_occurrences WHERE schedule_id LIKE 'sch_clone%'`); n != 0 {
		t.Fatalf("idle or disabled schedules ran: %d", n)
	}
	// The same tick again, and a restart: nothing new.
	e.tick()
	e.restart()
	e.tick()
	e.eng.Wait()
	if n := count(`SELECT count(*) FROM jobs WHERE schedule_id IS NOT NULL`); n != burst {
		t.Fatalf("after a second tick and a restart: %d jobs", n)
	}

	// The next day, with hosts that take a while: the engine runs 20
	// hosts at a time and the rest of the burst waits as ordinary queued
	// jobs, never as scheduler state.
	firstG, firstHeap := peakG.Load(), peakHeap.Load()
	e.exec.mu.Lock()
	e.exec.do = func(ctx context.Context, _ string, _ model.Operation) (*driver.OpResult, error) {
		select {
		case <-time.After(200 * time.Millisecond):
		case <-ctx.Done():
		}
		z := 0
		return &driver.OpResult{ExitCode: &z}, nil
	}
	e.exec.mu.Unlock()
	peakG.Store(0)
	peakHeap.Store(0)
	sampling = make(chan struct{})
	sampled.Add(1)
	go func() {
		defer sampled.Done()
		var ms runtime.MemStats
		for {
			if g := int64(runtime.NumGoroutine()); g > peakG.Load() {
				peakG.Store(g)
			}
			runtime.ReadMemStats(&ms)
			if h := int64(ms.HeapInuse); h > peakHeap.Load() {
				peakHeap.Store(h)
			}
			select {
			case <-sampling:
				return
			case <-time.After(20 * time.Millisecond):
			}
		}
	}()
	e.clk.set(t0.Add(24*time.Hour + time.Minute + 2*time.Second))
	slowStart := time.Now()
	e.tick()
	slowTick := time.Since(slowStart)
	e.eng.Wait()
	slowAll := time.Since(slowStart)
	close(sampling)
	sampled.Wait()
	if n := count(`SELECT count(*) FROM jobs WHERE schedule_id IS NOT NULL AND status = 'completed'`); n != 2*burst {
		t.Fatalf("second day: %d completed jobs", n)
	}
	t.Logf("slow hosts (200 ms each, 20 at a time): %d occurrences dispatched in %s; all jobs finished after %s; peak goroutines %d, peak heap %.1f MiB",
		burst, slowTick.Round(time.Millisecond), slowAll.Round(time.Millisecond), peakG.Load(), float64(peakHeap.Load())/(1<<20))

	sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
	p := func(q float64) time.Duration { return lat[int(float64(len(lat)-1)*q)] }
	t.Logf("created %d schedules in %s (%.1f ms each); %d idle and %d disabled alongside", burst, createdIn.Round(time.Millisecond),
		float64(createdIn.Milliseconds())/burst, idle, disabled)
	t.Logf("tick with nothing due: %s; due query plan: %s", quietTick.Round(time.Microsecond), strings.Join(plan, "; "))
	t.Logf("burst: %d occurrences dispatched in %s (%.0f/s); all jobs finished after %s", burst, tickDur.Round(time.Millisecond),
		float64(burst)/tickDur.Seconds(), allDone.Round(time.Millisecond))
	t.Logf("other writer during the burst: %d writes, p50 %s, p99 %s, max %s", len(lat), p(0.5).Round(time.Microsecond),
		p(0.99).Round(time.Microsecond), lat[len(lat)-1].Round(time.Microsecond))
	t.Logf("goroutines: %d before, peak %d (the engine's queued jobs), %d after; peak heap in use %.1f MiB",
		baseG, firstG, afterG, float64(firstHeap)/(1<<20))
	if p(0.99) > 250*time.Millisecond {
		t.Errorf("another writer waited %s (p99) during the burst", p(0.99))
	}
	if afterG > baseG+20 {
		t.Errorf("goroutines didn't return to baseline: %d → %d", baseG, afterG)
	}
}
