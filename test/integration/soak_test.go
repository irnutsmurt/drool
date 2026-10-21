//go:build integration

package integration

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"drool/internal/agent"
	"drool/internal/agentcaps"
	"drool/internal/hostexec"
	"drool/internal/netmon/collect"
)

// Soak tests (M16, ARCHITECTURE-ALPHA §33.10): a real controller process
// and a swarm of real Shepherds (the agent library, in this process) under
// steady load: a job on every host every 20 s, a schedule every minute,
// a tenth of the Shepherds reconnecting every 30 s. The controller's own
// goroutines, open files and heap are sampled from /metrics; afterwards
// every Shepherd stops and everything must drain back. The Shepherds run
// commands through a stub (nothing is executed on this machine), with a
// shell-only ceiling and no connection collection.
//
//	go test -tags integration ./test/integration -run TestSoak -soak 5,50,200 -soak-duration 3m
var (
	flagSoak         = flag.String("soak", "", "comma-separated Shepherd counts for TestSoak (empty: skip)")
	flagSoakDuration = flag.Duration("soak-duration", 2*time.Minute, "load phase per size")
)

// stubRunner answers every command with exit status 0 (and a run
// directory name for the step that creates one); nothing is executed.
type stubRunner struct{}

func (stubRunner) Run(ctx context.Context, cmd string, opt hostexec.RunOptions) (*hostexec.RunResult, error) {
	if strings.Contains(cmd, "mktemp") {
		return &hostexec.RunResult{Stdout: []byte("/tmp/drool.soakstub01\n")}, nil
	}
	return &hostexec.RunResult{}, nil
}

type stubProcess struct{ done chan struct{} }

func (p stubProcess) Done() <-chan struct{}  { return p.done }
func (p stubProcess) ExitCode() (int, error) { return 0, nil }
func (p stubProcess) Close()                 {}

func (stubRunner) Start(ctx context.Context, cmd string, stdin []byte, stdout, stderr io.Writer, pty bool) (hostexec.Process, error) {
	io.WriteString(stdout, "ok\n")
	d := make(chan struct{})
	close(d)
	return stubProcess{done: d}, nil
}

type sample struct {
	At                         time.Duration
	Goroutines, FDs, Connected int
	HeapMiB, RSSMiB            float64
}

func metrics(t *testing.T, c *controller) map[string]float64 {
	t.Helper()
	r, err := c.client.Get(c.base() + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	out := map[string]float64{}
	sc := bufio.NewScanner(r.Body)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) != 2 || strings.HasPrefix(f[0], "#") {
			continue
		}
		if v, err := strconv.ParseFloat(f[1], 64); err == nil {
			out[f[0]] = v
		}
	}
	return out
}

func TestSoak(t *testing.T) {
	if *flagSoak == "" {
		t.Skip("soak test: run with -soak 5,50,200")
	}
	var report []string
	for _, f := range strings.Split(*flagSoak, ",") {
		n, err := strconv.Atoi(strings.TrimSpace(f))
		if err != nil || n < 1 {
			t.Fatalf("-soak: %q", f)
		}
		t.Run(fmt.Sprint(n, " shepherds"), func(t *testing.T) { report = append(report, soak(t, n, *flagSoakDuration)) })
	}
	t.Log("\n" + strings.Join(report, "\n"))
	if p := os.Getenv("DROOL_SOAK_REPORT"); p != "" {
		os.WriteFile(p, []byte(strings.Join(report, "\n")+"\n"), 0o644)
	}
}

func soak(t *testing.T, n int, duration time.Duration) string {
	dir := t.TempDir()
	agentPortN := freePort()
	sc := &controller{dataDir: filepath.Join(dir, "data"), extraEnv: []string{
		"DROOL_AGENT_BIND=127.0.0.1", fmt.Sprintf("DROOL_AGENT_PORT=%d", agentPortN),
		fmt.Sprintf("DROOL_AGENT_ADVERTISE=127.0.0.1:%d", agentPortN), "DROOL_TEST_AUTH_TIMING=fast",
		"DROOL_TEST_SCHED_TIMING=fast", "DROOL_LOGGING_LEVEL=warn"}}
	if err := sc.start(); err != nil {
		t.Fatal(err)
	}
	defer sc.stop()
	if err := sc.claim(); err != nil {
		t.Fatal(err)
	}
	shared := ctl
	ctl = sc
	defer func() { ctl = shared }()
	// Network activity collection off: the Shepherds would otherwise
	// sample this machine's sockets.
	sc.must(t, "PUT", "/api/v1/network/settings", map[string]any{"collection": "off", "retention_days": 30, "max_flows_per_host": 5000, "enrichment": false}, nil)
	base := metrics(t, sc)

	caps := agentcaps.Caps{Shell: true}
	type swarmAgent struct {
		paths  agent.Paths
		cancel context.CancelFunc
		done   chan struct{}
		a      *agent.Agent
	}
	start := func(p agent.Paths) *swarmAgent {
		a, err := agent.New(agent.Options{Paths: p, Version: agentV1, StatusEvery: time.Minute, RenewCheck: time.Hour, Supervisor: "exit",
			Runner: stubRunner{}, NetCollect: &collect.Options{DisableConntrack: true, SS: func(context.Context) (string, error) { return "", nil }},
			// Never the real self-removal helper on the workstation.
			StartRemoval: func(string) (string, error) { return "", errors.New("not in this test") }, RemovalDir: t.TempDir()})
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		s := &swarmAgent{paths: p, cancel: cancel, done: make(chan struct{}), a: a}
		go func() { a.Run(ctx); close(s.done) }()
		return s
	}
	stop := func(s *swarmAgent) { s.cancel(); <-s.done; s.a.Close() }
	swarm := make([]*swarmAgent, n)
	hosts := make([]string, n)
	t0 := time.Now()
	var wg sync.WaitGroup
	sem := make(chan struct{}, 16)
	for i := 0; i < n; i++ {
		var tok struct {
			Token string `json:"token"`
		}
		sc.must(t, "POST", "/api/v1/agents/tokens", map[string]any{"description": fmt.Sprintf("soak-%03d", i)}, &tok)
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, token string) {
			defer func() { <-sem; wg.Done() }()
			p := agent.Under(filepath.Join(dir, fmt.Sprintf("a%03d", i)))
			os.MkdirAll(filepath.Dir(p.Config), 0o755)
			os.WriteFile(p.Config, agentcaps.Render(caps, ""), 0o600)
			id, err := agent.Enroll(context.Background(), p, token, agentV1)
			if err != nil {
				t.Errorf("enroll %d: %v", i, err)
				return
			}
			hosts[i] = id.HostID
			swarm[i] = start(p)
		}(i, tok.Token)
	}
	wg.Wait()
	if t.Failed() {
		t.FailNow()
	}
	connected := func() int {
		var list struct {
			Items []struct {
				Connected bool `json:"connected"`
			} `json:"items"`
		}
		sc.must(t, "GET", "/api/v1/agents", nil, &list)
		k := 0
		for _, a := range list.Items {
			if a.Connected {
				k++
			}
		}
		return k
	}
	waitFor(t, 3*time.Minute, "every Shepherd connected", func() bool { return connected() == n })
	enrolled := time.Since(t0)

	var samples []sample
	take := func(at time.Duration) sample {
		m := metrics(t, sc)
		s := sample{At: at, Goroutines: int(m["go_goroutines"]), FDs: int(m["process_open_fds"]), Connected: connected(),
			HeapMiB: m["go_memstats_heap_inuse_bytes"] / (1 << 20), RSSMiB: m["process_resident_memory_bytes"] / (1 << 20)}
		samples = append(samples, s)
		return s
	}
	warm := take(0)
	// A scheduled run on every host each minute (the scheduler and the
	// engine together), and jobs every 20 s.
	var schedules []string
	for i := 1; i <= int(duration/time.Minute); i++ {
		var sch struct {
			ID string `json:"id"`
		}
		at := time.Now().UTC().Truncate(time.Minute).Add(time.Duration(i) * time.Minute)
		sc.must(t, "POST", "/api/v1/schedules", map[string]any{"name": fmt.Sprintf("soak %d", i), "time_zone": "UTC",
			"recurrence": map[string]any{"kind": "once", "at": at.Format("2006-01-02T15:04")},
			"steps":      []any{map[string]any{"type": "command", "command": "true"}}, "targets": map[string]any{"hosts": hosts}}, &sch)
		schedules = append(schedules, sch.ID)
	}
	var jobs []string
	loadStart := time.Now()
	nextJob, nextChurn, nextSample := loadStart, loadStart.Add(30*time.Second), loadStart.Add(10*time.Second)
	churned := 0
	for time.Since(loadStart) < duration {
		now := time.Now()
		if !now.Before(nextJob) {
			var j struct {
				ID string `json:"id"`
			}
			sc.must(t, "POST", "/api/v1/jobs", map[string]any{"steps": []any{map[string]any{"type": "command", "command": "true"}},
				"targets": map[string]any{"hosts": hosts}, "strategy": map[string]any{"concurrency": 20}}, &j)
			jobs = append(jobs, j.ID)
			nextJob = now.Add(20 * time.Second)
		}
		if !now.Before(nextChurn) {
			k := max(1, n/10)
			for i := 0; i < k; i++ {
				idx := (churned + i) % n
				stop(swarm[idx])
				swarm[idx] = start(swarm[idx].paths)
			}
			churned += k
			nextChurn = now.Add(30 * time.Second)
		}
		if !now.Before(nextSample) {
			take(time.Since(loadStart))
			nextSample = now.Add(10 * time.Second)
		}
		time.Sleep(200 * time.Millisecond)
	}
	// The scheduled runs' jobs too.
	for _, id := range schedules {
		var occ struct {
			Items []struct {
				JobID string `json:"job_id"`
			} `json:"items"`
		}
		sc.must(t, "GET", "/api/v1/schedules/"+id+"/occurrences", nil, &occ)
		for _, o := range occ.Items {
			if o.JobID != "" {
				jobs = append(jobs, o.JobID)
			}
		}
		sc.must(t, "DELETE", "/api/v1/schedules/"+id, nil, nil)
	}
	// Every job finishes (no stuck host, no stale lock).
	for _, id := range jobs {
		if st := waitJob(t, id, 5*time.Minute); st != "completed" {
			t.Errorf("job %s ended %s", id, st)
		}
	}
	var failed int
	for _, id := range jobs {
		var j struct {
			Counts map[string]int `json:"counts"`
		}
		sc.must(t, "GET", "/api/v1/jobs/"+id, nil, &j)
		f := j.Counts["failed"] + j.Counts["timed_out"] + j.Counts["interrupted"]
		if f > 0 && failed == 0 {
			var hs struct {
				Items []struct {
					HostID       string `json:"host_id"`
					State        string `json:"state"`
					ErrorClass   string `json:"error_class"`
					ErrorMessage string `json:"error_message"`
				} `json:"items"`
			}
			sc.must(t, "GET", "/api/v1/jobs/"+id+"/hosts", nil, &hs)
			for _, h := range hs.Items {
				if h.State != "succeeded" {
					t.Errorf("job %s on %s: %s %s: %s", id, h.HostID, h.State, h.ErrorClass, h.ErrorMessage)
					break
				}
			}
		}
		failed += f
	}
	loaded := take(time.Since(loadStart))

	// Drain: every Shepherd stops; sessions, goroutines and files go back.
	for _, s := range swarm {
		stop(s)
	}
	waitFor(t, 3*time.Minute, "every session gone", func() bool { return connected() == 0 })
	var drained sample
	waitFor(t, 2*time.Minute, "the controller back near its baseline", func() bool {
		drained = take(time.Since(loadStart))
		return drained.Goroutines <= int(base["go_goroutines"])+40
	})
	// The load phase plateaus: the last third doesn't keep climbing past
	// the middle third.
	third := len(samples) / 3
	peak := func(from, to int) (g, f int) {
		for _, s := range samples[from:to] {
			g, f = max(g, s.Goroutines), max(f, s.FDs)
		}
		return
	}
	if third >= 2 {
		g2, f2 := peak(third, 2*third)
		g3, f3 := peak(2*third, len(samples)-2)
		if float64(g3) > float64(g2)*1.25+50 || float64(f3) > float64(f2)*1.25+20 {
			t.Errorf("still climbing: goroutines %d → %d, files %d → %d", g2, g3, f2, f3)
		}
	}
	if drained.FDs > int(base["process_open_fds"])+15 {
		t.Errorf("open files after draining: %d (baseline %d)", drained.FDs, int(base["process_open_fds"]))
	}
	maxG, maxHeap := 0, 0.0
	for _, s := range samples {
		maxG, maxHeap = max(maxG, s.Goroutines), max(maxHeap, s.HeapMiB)
	}
	js, _ := json.Marshal(samples)
	os.WriteFile(filepath.Join(filepath.Dir(sc.dataDir), "samples.json"), js, 0o644)
	return fmt.Sprintf("%4d Shepherds: enrolled and connected in %s; %s of load, %d jobs (%d host runs, %d failed), %d reconnects; "+
		"controller goroutines %d idle → %d connected → peak %d → %d drained; open files %d → %d → %d; heap peak %.0f MiB; RSS %.0f MiB loaded",
		n, enrolled.Round(time.Second), duration, len(jobs), len(jobs)*n, failed, churned,
		int(base["go_goroutines"]), warm.Goroutines, maxG, drained.Goroutines, int(base["process_open_fds"]), loaded.FDs, drained.FDs, maxHeap, loaded.RSSMiB)
}
