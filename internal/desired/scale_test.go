package desired

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"drool/internal/model"
	"drool/internal/store"
)

// TestDesiredScale measures desired state on 5, 50 and 200 hosts (opt-in:
// DROOL_SCALE_TEST=1): checking and evaluating every host, the database
// it writes (and that unchanged checks write nothing), list and detail
// queries, a broad baseline change becoming one controlled job, another
// writer's latency meanwhile, goroutines and heap, and a pathological
// fleet where every resource is in conflict.
func TestDesiredScale(t *testing.T) {
	if os.Getenv("DROOL_SCALE_TEST") != "1" {
		t.Skip("set DROOL_SCALE_TEST=1")
	}
	for _, n := range []int{5, 50, 200} {
		t.Run(fmt.Sprintf("%d hosts", n), func(t *testing.T) { scaleRun(t, n) })
	}
	t.Run("200 hosts, everything in conflict", scaleConflicts)
}

func dbSize(e *env) int64 {
	var total int64
	for _, f := range []string{store.FileName, store.FileName + "-wal"} {
		if fi, err := os.Stat(filepath.Join(e.data, f)); err == nil {
			total += fi.Size()
		}
	}
	return total
}

func rowCount(e *env, table string) int {
	var n int
	e.st.Reader.QueryRow(`SELECT count(*) FROM ` + table).Scan(&n)
	return n
}

func timeIt(f func()) time.Duration {
	s := time.Now()
	f()
	return time.Since(s)
}

func heapMiB() float64 {
	runtime.GC()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return float64(m.HeapAlloc) / (1 << 20)
}

func scaleRun(t *testing.T, n int) {
	var names []string
	for i := 1; i <= n; i++ {
		names = append(names, fmt.Sprintf("s%03d", i))
	}
	e := newEnv(t, names...)
	g0 := runtime.NumGoroutine()
	h0 := heapMiB()
	// A realistic fleet: 6 services, 10 packages, 4 files everywhere; a
	// group baseline for half the hosts.
	var rs []Resource
	for i := 0; i < 6; i++ {
		rs = append(rs, svc(fmt.Sprintf("svc%d", i), StateRunning, on()))
	}
	for i := 0; i < 10; i++ {
		st := StateInstalled
		if i%3 == 0 {
			st = StateAbsent
		}
		rs = append(rs, pkgR(fmt.Sprintf("pkg%d", i), st))
	}
	for i := 0; i < 4; i++ {
		sha := e.upload(fmt.Sprintf("f%d", i), fmt.Sprintf("content %d\n", i))
		rs = append(rs, Resource{Kind: KindFile, Path: fmt.Sprintf("/etc/app/f%d.conf", i), Artifact: sha, Mode: "0644"})
	}
	e.group("g_half", "half", names[:n/2]...)
	for _, h := range names {
		fh := e.f.host("h_" + h)
		e.f.do(func() {
			for i := 0; i < 6; i++ {
				fh.units[fmt.Sprintf("svc%d.service", i)] = &unit{active: "active", sub: "running", fileState: "enabled"}
			}
			for i := 0; i < 10; i++ {
				if i%3 != 0 {
					fh.packages[fmt.Sprintf("pkg%d", i)] = "1"
				}
			}
			for i := 0; i < 4; i++ {
				fh.files[fmt.Sprintf("/etc/app/f%d.conf", i)] = &fakeFile{kind: "file", content: []byte(fmt.Sprintf("content %d\n", i)), owner: "root", group: "root", mode: 0o644}
			}
		})
	}
	e.reportAll()
	b := e.baseline("Fleet", rs, everywhere)
	e.baseline("Half", []Resource{pkgR("extra", StateAbsent)}, inGroup("g_half"))
	size0 := dbSize(e)
	first := timeIt(func() { e.step(); e.checkAll() })
	sum, _ := e.s.Summary(e.ctx)
	if sum.Hosts.Compliant != n {
		t.Fatalf("summary %+v", sum.Hosts)
	}
	rows := rowCount(e, "ds_resource_state")
	size1 := dbSize(e)
	// Unchanged checks write nothing but each host's check time.
	var changes1, changes2 int
	e.clk.add(time.Minute)
	e.st.Writer.QueryRow(`SELECT total_changes()`).Scan(&changes1)
	again := timeIt(func() { e.checkAll() })
	e.st.Writer.QueryRow(`SELECT total_changes()`).Scan(&changes2)
	hist := rowCount(e, "ds_history")

	// Queries.
	var list, detail, summary time.Duration
	list = timeIt(func() { e.s.Hosts(e.ctx, HostFilter{}) })
	detail = timeIt(func() { e.s.Host(e.ctx, "h_"+names[n/2]) })
	summary = timeIt(func() { e.s.Summary(e.ctx) })

	// Another writer during a full re-evaluation of every host after a
	// broad change: all hosts drift at once.
	for _, h := range names {
		fh := e.f.host("h_" + h)
		e.f.do(func() { fh.packages["pkg0"] = "9" })
	}
	var lat []time.Duration
	var stop atomic.Bool
	var wg sync.WaitGroup
	e.st.Writer.Exec(`CREATE TABLE IF NOT EXISTS bench_other (id INTEGER PRIMARY KEY, v TEXT)`)
	wg.Add(1)
	go func() {
		defer wg.Done()
		for !stop.Load() {
			s := time.Now()
			e.st.Writer.Exec(`INSERT INTO bench_other (v) VALUES ('x')`)
			lat = append(lat, time.Since(s))
			time.Sleep(2 * time.Millisecond)
		}
	}()
	drift := timeIt(func() { e.checkAll() })
	stop.Store(true)
	wg.Wait()
	sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
	p := func(q float64) time.Duration {
		if len(lat) == 0 {
			return 0
		}
		return lat[min(len(lat)-1, int(q*float64(len(lat))))]
	}
	sum, _ = e.s.Summary(e.ctx)
	if sum.Hosts.Drift != n {
		t.Fatalf("after the broad drift %+v", sum.Hosts)
	}
	histDrift := rowCount(e, "ds_history")
	// Automatic restore of the broad drift: one job, canary and batches.
	if _, err := e.s.SetAuto(e.ctx, b.ID, true, nil, admin); err != nil {
		t.Fatal(err)
	}
	gPeak := 0
	var mu sync.Mutex
	stop.Store(false)
	wg.Add(1)
	go func() {
		defer wg.Done()
		for !stop.Load() {
			mu.Lock()
			gPeak = max(gPeak, runtime.NumGoroutine())
			mu.Unlock()
			time.Sleep(time.Millisecond)
		}
	}()
	dispatch := timeIt(func() { e.s.AutoRestoreNow(e.ctx) })
	jobs := e.jobCount()
	settle := timeIt(func() { e.waitJobs() })
	stop.Store(true)
	wg.Wait()
	sum, _ = e.s.Summary(e.ctx)
	if jobs != 1 || sum.Hosts.Compliant != n {
		t.Fatalf("%d jobs; after %+v", jobs, sum.Hosts)
	}
	var waves int
	e.st.Reader.QueryRow(`SELECT max(wave) + 1 FROM job_hosts`).Scan(&waves)
	h1 := heapMiB()
	t.Logf("%d hosts × %d resources: first check+evaluation %s (%.2f ms/host); unchanged re-check %s, %d row changes (one check time per host); "+
		"%d state rows, %d history rows; database %s → %s",
		n, len(rs)+1, first.Round(time.Millisecond), float64(first.Milliseconds())/float64(n), again.Round(time.Millisecond), changes2-changes1,
		rows, hist, humanBytes(size0), humanBytes(size1))
	t.Logf("queries: host list %s, host detail %s, summary %s", list.Round(time.Microsecond), detail.Round(time.Microsecond), summary.Round(time.Microsecond))
	t.Logf("broad drift: re-check and evaluate all %s (%d history rows: one per drifted resource); other writer p50 %s p99 %s max %s (%d writes)",
		drift.Round(time.Millisecond), histDrift-hist, p(0.5).Round(time.Microsecond), p(0.99).Round(time.Microsecond), p(1).Round(time.Microsecond), len(lat))
	t.Logf("automatic restore: planned and submitted %d job (%d waves) in %s; ran and settled in %s; goroutines %d before, peak %d, after %d; heap %.1f → %.1f MiB",
		jobs, waves, dispatch.Round(time.Millisecond), settle.Round(time.Millisecond), g0, gPeak, runtime.NumGoroutine(), h0, h1)
}

func humanBytes(n int64) string {
	switch {
	case n > 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
	case n > 1<<10:
		return fmt.Sprintf("%.0f KiB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}

// scaleConflicts: 200 hosts in 10 groups of equal priority whose
// baselines disagree on 20 resources: every resource on every host is a
// conflict, none is restored, and resolution stays fast.
func scaleConflicts(t *testing.T) {
	var names []string
	for i := 1; i <= 200; i++ {
		names = append(names, fmt.Sprintf("c%03d", i))
	}
	e := newEnv(t, names...)
	for g := 0; g < 10; g++ {
		e.group(fmt.Sprintf("g%d", g), fmt.Sprintf("group-%d", g), names...)
		var rs []Resource
		for i := 0; i < 20; i++ {
			st := StateInstalled
			if (g+i)%2 == 0 {
				st = StateAbsent
			}
			rs = append(rs, pkgR(fmt.Sprintf("p%d", i), st))
		}
		e.baseline(fmt.Sprintf("B%d", g), rs, inGroup(fmt.Sprintf("g%d", g)))
	}
	snap := e.s.Snapshot()
	resolve := timeIt(func() {
		for id := range snap.Hosts {
			snap.Resolve(id)
		}
	})
	eval := timeIt(func() { e.step(); e.checkAll() })
	sum, _ := e.s.Summary(e.ctx)
	if sum.Resources.Conflict != 4000 || sum.Hosts.Conflict != 200 {
		t.Fatalf("%+v", sum)
	}
	p, err := e.s.PreviewRestore(context.Background(), Selection{}, RestoreOptions{Strategy: &model.Strategy{}})
	if err != nil || len(p.Items) != 0 {
		t.Fatalf("restore planned something: %v %d", err, len(p.Items))
	}
	if !strings.Contains(p.Excluded[0].Reason, "Conflict between groups") {
		t.Fatal(p.Excluded[0].Reason)
	}
	t.Logf("pathological: 200 hosts × 20 resources × 10 equal-priority groups: resolving every host %s; check and evaluate %s; 4000 conflicts, nothing restorable (%d excluded with the reason)",
		resolve.Round(time.Millisecond), eval.Round(time.Millisecond), len(p.Excluded))
}
