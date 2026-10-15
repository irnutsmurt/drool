package engine

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"drool/internal/audit"
	"drool/internal/driver"
	"drool/internal/logging"
	"drool/internal/model"
	"drool/internal/store"
	"drool/internal/store/db"
)

// fakeResolver resolves any host to a target with a known sudo password.
type fakeResolver struct{}

func (fakeResolver) HostOS(_ context.Context, id string) (model.HostOS, error) {
	switch {
	case strings.HasPrefix(id, "deb"):
		return model.HostOS{Family: "debian", Distro: "ubuntu", Known: true}, nil
	case strings.HasPrefix(id, "rh"):
		return model.HostOS{Family: "redhat", Distro: "rocky", Known: true}, nil
	}
	return model.HostOS{}, nil
}

// execFunc is the shape of a fake executor's per-operation behavior.
type execFunc func(ctx context.Context, host string, op model.Operation, out driver.OutputSink, started func()) (*driver.OpResult, error)

// fakePath adapts an execFunc to the engine's Path (an SSH-like path with
// a known sudo password to redact).
type fakePath struct {
	host string
	run  execFunc
}

func (p fakePath) Execute(ctx context.Context, op model.Operation, out driver.OutputSink, started func()) (*driver.OpResult, error) {
	return p.run(ctx, p.host, op, out, started)
}
func (p fakePath) Transport() (string, string) { return "ssh", "" }
func (p fakePath) JumpHostID() string          { return "" }
func (p fakePath) Secrets() []string           { return []string{"s3cret-sudo-pw"} }

// fakeExec runs a per-host behavior.
type fakeExec struct {
	mu      sync.Mutex
	calls   map[string]int
	running atomic.Int32
	peak    atomic.Int32
	do      func(ctx context.Context, host string, call int, out driver.OutputSink) (*driver.OpResult, error)
}

func (f *fakeExec) Prepare(_ context.Context, id string) (Path, error) {
	return fakePath{host: id, run: f.execute}, nil
}

func (f *fakeExec) execute(ctx context.Context, host string, _ model.Operation, out driver.OutputSink, started func()) (*driver.OpResult, error) {
	f.mu.Lock()
	f.calls[host]++
	n := f.calls[host]
	f.mu.Unlock()
	cur := f.running.Add(1)
	defer f.running.Add(-1)
	for {
		p := f.peak.Load()
		if cur <= p || f.peak.CompareAndSwap(p, cur) {
			break
		}
	}
	started()
	return f.do(ctx, host, n, out)
}

func ok() (*driver.OpResult, error) { c := 0; return &driver.OpResult{ExitCode: &c}, nil }

type env struct {
	t    *testing.T
	st   *store.Store
	e    *Engine
	exec *fakeExec
	user string
}

func newEnv(t *testing.T, cfg Config, hosts ...string) *env {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), store.FileName))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ctx := context.Background()
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Writer.Exec(`INSERT INTO users (id, username, password_hash, role, created_at, updated_at, password_changed_at)
		VALUES ('usr_1', 'admin', 'x', 'admin', 0, 0, 0)`); err != nil {
		t.Fatal(err)
	}
	for _, h := range hosts {
		if _, err := st.Writer.Exec(`INSERT INTO hosts (id, name, connect_address, ssh_user, status, created_at, updated_at)
			VALUES (?, ?, '10.0.0.1', 'deploy', 'ready', 0, 0)`, h, h); err != nil {
			t.Fatal(err)
		}
	}
	if cfg.RetryDelay == 0 {
		cfg.RetryDelay = time.Millisecond
	}
	logs := logging.Discard()
	fe := &fakeExec{calls: map[string]int{}, do: func(context.Context, string, int, driver.OutputSink) (*driver.OpResult, error) { return ok() }}
	e := New(st, fakeResolver{}, fe, audit.NewWriter(st, logs.Audit), logs.App, cfg)
	if err := e.Start(ctx); err != nil {
		t.Fatal(err)
	}
	return &env{t: t, st: st, e: e, exec: fe, user: "usr_1"}
}

func cmdSpec(hosts ...string) model.JobSpec {
	return model.JobSpec{
		Steps:   []model.Operation{{Type: model.OpCommand, Command: &model.CommandOp{Command: "echo hi"}}},
		Targets: model.TargetSpec{Hosts: hosts},
	}
}

func (v *env) run(spec model.JobSpec) string {
	v.t.Helper()
	var sub *Submission
	err := v.st.InTx(context.Background(), func(q *db.Queries) error {
		var err error
		sub, err = v.e.SubmitTx(context.Background(), q, spec, v.user, "", "")
		return err
	})
	if err != nil {
		v.t.Fatal(err)
	}
	v.e.Launch(sub.Job.ID)
	return sub.Job.ID
}

func (v *env) wait(job string) db.Job {
	v.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		j, err := v.st.R.GetJob(context.Background(), job)
		if err == nil && (j.Status == JobCompleted || j.Status == JobCancelled) {
			v.e.Wait()
			return j
		}
		time.Sleep(10 * time.Millisecond)
	}
	v.t.Fatalf("job %s did not finish", job)
	return db.Job{}
}

func (v *env) hosts(job string) map[string]db.JobHost {
	hs, err := v.st.R.ListJobHosts(context.Background(), job)
	if err != nil {
		v.t.Fatal(err)
	}
	out := map[string]db.JobHost{}
	for _, h := range hs {
		out[h.HostID] = h
	}
	return out
}

func (v *env) output(job, host string) string {
	rows, err := v.st.R.ListJobOutput(context.Background(), db.ListJobOutputParams{JobID: job, HostID: host, StepIndex: 0, Seq: 0, Limit: 1000})
	if err != nil {
		v.t.Fatal(err)
	}
	var b strings.Builder
	for _, r := range rows {
		b.Write(r.Chunk)
	}
	return b.String()
}

func TestJobSuccessWithOutputAndConcurrency(t *testing.T) {
	var hosts []string
	for i := 0; i < 12; i++ {
		hosts = append(hosts, fmt.Sprintf("h%02d", i))
	}
	v := newEnv(t, Config{}, hosts...)
	v.exec.do = func(_ context.Context, host string, _ int, out driver.OutputSink) (*driver.OpResult, error) {
		fmt.Fprintf(out.Stdout(), "hello from %s\n", host)
		fmt.Fprint(out.Stderr(), "warn\n")
		time.Sleep(20 * time.Millisecond)
		return ok()
	}
	spec := cmdSpec(hosts...)
	spec.Strategy.Concurrency = 3
	job := v.run(spec)
	if j := v.wait(job); j.Status != JobCompleted {
		t.Fatalf("status %s", j.Status)
	}
	for _, h := range v.hosts(job) {
		if h.State != HostSucceeded || !h.ExitCode.Valid || h.ExitCode.Int64 != 0 {
			t.Fatalf("host %s: %+v", h.HostID, h)
		}
	}
	if p := v.exec.peak.Load(); p > 3 {
		t.Fatalf("concurrency limit exceeded: peak %d", p)
	}
	if out := v.output(job, "h03"); !strings.Contains(out, "hello from h03") || !strings.Contains(out, "warn") {
		t.Fatalf("output %q", out)
	}
}

func TestExecutionFailureNotRetried(t *testing.T) {
	v := newEnv(t, Config{}, "a")
	v.exec.do = func(context.Context, string, int, driver.OutputSink) (*driver.OpResult, error) {
		c := 2
		return &driver.OpResult{ExitCode: &c}, model.Errorf(model.ClassExecution, false, "exited with status 2")
	}
	job := v.run(cmdSpec("a"))
	v.wait(job)
	h := v.hosts(job)["a"]
	if h.State != HostFailed || h.ExitCode.Int64 != 2 || h.ErrorClass != string(model.ClassExecution) || v.exec.calls["a"] != 1 {
		t.Fatalf("host %+v calls %d", h, v.exec.calls["a"])
	}
}

func TestNotStartedConnectionErrorRetried(t *testing.T) {
	v := newEnv(t, Config{}, "a", "b")
	v.exec.do = func(_ context.Context, host string, call int, _ driver.OutputSink) (*driver.OpResult, error) {
		if host == "a" && call < 3 {
			return nil, &driver.NotStartedError{Err: model.Errorf(model.ClassConnection, true, "connection refused")}
		}
		if host == "b" {
			// Connection lost after the command started: not safe to retry.
			return nil, model.Errorf(model.ClassConnection, true, "connection lost")
		}
		return ok()
	}
	job := v.run(cmdSpec("a", "b"))
	v.wait(job)
	hs := v.hosts(job)
	if hs["a"].State != HostSucceeded || v.exec.calls["a"] != 3 {
		t.Fatalf("a: %s after %d calls", hs["a"].State, v.exec.calls["a"])
	}
	if hs["b"].State != HostFailed || v.exec.calls["b"] != 1 {
		t.Fatalf("b: %s after %d calls", hs["b"].State, v.exec.calls["b"])
	}
	if out := v.output(job, "a"); !strings.Contains(out, "attempt 3 of 3") {
		t.Fatalf("retry note missing: %q", out)
	}
}

func (v *env) waitPaused(job string) db.Job {
	v.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if j, err := v.st.R.GetJob(context.Background(), job); err == nil && j.Status == JobPaused {
			return j
		}
		time.Sleep(5 * time.Millisecond)
	}
	j, _ := v.st.R.GetJob(context.Background(), job)
	v.t.Fatalf("job %s never paused (status %s)", job, j.Status)
	return db.Job{}
}

func TestFailureThresholdPausesThenResumesOrCancels(t *testing.T) {
	hosts := []string{"a", "b", "c", "d", "e", "f", "g"}
	two := 2
	failing := func(v *env) {
		v.exec.do = func(_ context.Context, host string, _ int, _ driver.OutputSink) (*driver.OpResult, error) {
			if host == "a" || host == "b" || host == "e" {
				return nil, model.Errorf(model.ClassExecution, false, "boom")
			}
			return ok()
		}
	}
	t.Run("pause, explain, resume: failed hosts stay failed", func(t *testing.T) {
		v := newEnv(t, Config{}, hosts...)
		failing(v)
		spec := cmdSpec(hosts...)
		spec.Strategy.Concurrency = 1
		spec.Strategy.FailureThreshold = &two
		job := v.run(spec)
		j := v.waitPaused(job)
		if j.PauseReason != "Paused after 2 failures (the failure threshold is 2). 5 hosts have not started; resume to continue, or cancel" {
			t.Fatalf("reason %q", j.PauseReason)
		}
		counts, _ := Counts(context.Background(), v.st.R, job)
		if counts[HostFailed] != 2 || counts[HostQueued] != 5 {
			t.Fatalf("no host starts while paused: %v", counts)
		}
		if err := v.e.Resume(context.Background(), job); err != nil {
			t.Fatal(err)
		}
		v.wait(job)
		counts, _ = Counts(context.Background(), v.st.R, job)
		if counts[HostFailed] != 3 || counts[HostSucceeded] != 4 {
			t.Fatalf("after resume (one more failure is under the renewed allowance): %v", counts)
		}
	})
	t.Run("cancel instead", func(t *testing.T) {
		v := newEnv(t, Config{}, hosts...)
		failing(v)
		spec := cmdSpec(hosts...)
		spec.Strategy.Concurrency = 1
		spec.Strategy.FailureThreshold = &two
		job := v.run(spec)
		v.waitPaused(job)
		if err := v.e.Cancel(context.Background(), job); err != nil {
			t.Fatal(err)
		}
		if j := v.wait(job); j.Status != JobCancelled {
			t.Fatal(j.Status)
		}
		counts, _ := Counts(context.Background(), v.st.R, job)
		if counts[HostFailed] != 2 || counts[HostCancelled] != 5 {
			t.Fatalf("%v", counts)
		}
	})
	t.Run("running hosts finish before the pause", func(t *testing.T) {
		v := newEnv(t, Config{}, hosts...)
		release := make(chan struct{})
		v.exec.do = func(_ context.Context, host string, _ int, _ driver.OutputSink) (*driver.OpResult, error) {
			if host == "a" || host == "b" {
				return nil, model.Errorf(model.ClassExecution, false, "boom")
			}
			<-release
			return ok()
		}
		spec := cmdSpec(hosts...)
		spec.Strategy.Concurrency = 3
		spec.Strategy.FailureThreshold = &two
		job := v.run(spec)
		// Wait for the two failures (not a fixed time: under the race
		// detector they can take a while), then check nothing pauses
		// while the hosts already running are still running.
		deadline := time.Now().Add(10 * time.Second)
		for hs := v.hosts(job); hs["a"].State != HostFailed || hs["b"].State != HostFailed; hs = v.hosts(job) {
			if time.Now().After(deadline) {
				t.Fatalf("a and b didn't fail: %v %v", hs["a"].State, hs["b"].State)
			}
			time.Sleep(5 * time.Millisecond)
		}
		time.Sleep(50 * time.Millisecond)
		if j, _ := v.st.R.GetJob(context.Background(), job); j.Status == JobPaused {
			t.Fatal("paused while a host was still running")
		}
		close(release)
		v.waitPaused(job)
		counts, _ := Counts(context.Background(), v.st.R, job)
		if counts[HostFailed] != 2 || counts[HostSucceeded]+counts[HostQueued] != 5 || counts[HostQueued] < 3 {
			t.Fatalf("running hosts finished, nothing new started: %v", counts)
		}
		v.e.Cancel(context.Background(), job)
		v.wait(job)
	})
	t.Run("threshold on the last hosts: nothing left to pause for", func(t *testing.T) {
		v := newEnv(t, Config{}, "a", "b")
		v.exec.do = func(context.Context, string, int, driver.OutputSink) (*driver.OpResult, error) {
			return nil, model.Errorf(model.ClassExecution, false, "boom")
		}
		spec := cmdSpec("a", "b")
		spec.Strategy.FailureThreshold = &two
		job := v.run(spec)
		if j := v.wait(job); j.Status != JobCompleted {
			t.Fatal(j.Status)
		}
	})
}

func TestCanaryAndBatches(t *testing.T) {
	var hosts []string
	for i := range 12 {
		hosts = append(hosts, fmt.Sprintf("h%02d", i))
	}
	t.Run("canary first, then batches; next batch only after the current one", func(t *testing.T) {
		v := newEnv(t, Config{}, hosts...)
		var mu sync.Mutex
		var order []string
		running := map[string]bool{}
		v.exec.do = func(_ context.Context, host string, _ int, _ driver.OutputSink) (*driver.OpResult, error) {
			mu.Lock()
			order = append(order, host)
			running[host] = true
			mu.Unlock()
			time.Sleep(15 * time.Millisecond)
			mu.Lock()
			delete(running, host)
			mu.Unlock()
			return ok()
		}
		spec := cmdSpec(hosts...)
		spec.Strategy = model.Strategy{Concurrency: 2, CanaryCount: 1, BatchSize: 5}
		job := v.run(spec)
		v.wait(job)
		hs, _ := v.st.R.ListJobHosts(context.Background(), job)
		waves := map[int64][]string{}
		for _, h := range hs {
			waves[h.Wave] = append(waves[h.Wave], h.HostID)
		}
		if len(waves[0]) != 1 || waves[0][0] != "h00" || len(waves[1]) != 5 || len(waves[2]) != 5 || len(waves[3]) != 1 {
			t.Fatalf("waves %v", waves)
		}
		pos := map[string]int{}
		for i, h := range order {
			pos[h] = i
		}
		for w := int64(1); w <= 3; w++ {
			for _, a := range waves[w-1] {
				for _, b := range waves[w] {
					if pos[a] > pos[b] {
						t.Fatalf("%s (wave %d) started after %s (wave %d): %v", a, w-1, b, w, order)
					}
				}
			}
		}
		if p := v.exec.peak.Load(); p > 2 {
			t.Fatalf("concurrency inside a batch exceeded: %d", p)
		}
	})
	t.Run("a failed canary pauses before the rest of the fleet starts", func(t *testing.T) {
		v := newEnv(t, Config{}, hosts...)
		v.exec.do = func(_ context.Context, host string, _ int, _ driver.OutputSink) (*driver.OpResult, error) {
			if host == "h03" {
				c := 1
				return &driver.OpResult{ExitCode: &c}, model.Errorf(model.ClassExecution, false, "exited with status 1")
			}
			return ok()
		}
		spec := cmdSpec(hosts...)
		spec.Strategy = model.Strategy{Concurrency: 5, CanaryHosts: []string{"h03"}, BatchSize: 4}
		job := v.run(spec)
		j := v.waitPaused(job)
		if j.PauseReason != "Canary h03 failed: exited with status 1. 11 hosts have not started; resume to continue the rollout, or cancel" {
			t.Fatalf("%q", j.PauseReason)
		}
		if calls := len(v.exec.calls); calls != 1 {
			t.Fatalf("only the canary ran: %v", v.exec.calls)
		}
		if err := v.e.Resume(context.Background(), job); err != nil {
			t.Fatal(err)
		}
		v.wait(job)
		counts, _ := Counts(context.Background(), v.st.R, job)
		if counts[HostFailed] != 1 || counts[HostSucceeded] != 11 {
			t.Fatalf("the failed canary stays failed: %v", counts)
		}
	})
	t.Run("a successful canary continues on its own", func(t *testing.T) {
		v := newEnv(t, Config{}, hosts...)
		spec := cmdSpec(hosts...)
		spec.Strategy = model.Strategy{Concurrency: 10, CanaryCount: 2, BatchSize: 3}
		job := v.run(spec)
		if j := v.wait(job); j.Status != JobCompleted {
			t.Fatal(j.Status)
		}
		counts, _ := Counts(context.Background(), v.st.R, job)
		if counts[HostSucceeded] != 12 {
			t.Fatalf("%v", counts)
		}
	})
	t.Run("hosts left out up front", func(t *testing.T) {
		v := newEnv(t, Config{}, hosts...)
		var sub *Submission
		err := v.st.InTx(context.Background(), func(q *db.Queries) error {
			var err error
			sub, err = v.e.Submit(context.Background(), q, cmdSpec("h00", "h01", "h02"), SubmitOptions{UserID: v.user,
				Skip: map[string]string{"h01": "maintenance active (kernel upgrade)"}, Origin: Origin{ScheduleID: "sch_1", OccurrenceID: "occ_1"}})
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		v.e.Launch(sub.Job.ID)
		v.wait(sub.Job.ID)
		hs := v.hosts(sub.Job.ID)
		if hs["h01"].State != HostSkipped || hs["h01"].Wave != -1 || hs["h01"].ErrorMessage != "skipped: maintenance active (kernel upgrade)" ||
			hs["h00"].State != HostSucceeded || v.exec.calls["h01"] != 0 {
			t.Fatalf("%+v", hs)
		}
		j, _ := v.st.R.GetJob(context.Background(), sub.Job.ID)
		if j.ScheduleID.String != "sch_1" || j.OccurrenceID.String != "occ_1" {
			t.Fatalf("origin %+v", j)
		}
		err = v.st.InTx(context.Background(), func(q *db.Queries) error {
			_, err := v.e.Submit(context.Background(), q, cmdSpec("h00"), SubmitOptions{UserID: v.user, Skip: map[string]string{"h00": "x"}})
			return err
		})
		if !errors.Is(err, ErrNothingToRun) {
			t.Fatal(err)
		}
	})
}

func TestCircuitBreakerPausesAndResumes(t *testing.T) {
	hosts := []string{"a", "b", "c", "d", "e", "f", "g"}
	v := newEnv(t, Config{BreakerThreshold: 3}, hosts...)
	var fail atomic.Bool
	fail.Store(true)
	v.exec.do = func(context.Context, string, int, driver.OutputSink) (*driver.OpResult, error) {
		if fail.Load() {
			return nil, model.Errorf(model.ClassAuthentication, false, "auth failed")
		}
		return ok()
	}
	spec := cmdSpec(hosts...)
	spec.Strategy.Concurrency = 1
	job := v.run(spec)
	deadline := time.Now().Add(5 * time.Second)
	for {
		j, _ := v.st.R.GetJob(context.Background(), job)
		if j.Status == JobPaused {
			if !strings.Contains(j.PauseReason, "AuthenticationError") {
				t.Fatalf("pause reason %q", j.PauseReason)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("job never paused (status %s)", j.Status)
		}
		time.Sleep(10 * time.Millisecond)
	}
	counts, _ := Counts(context.Background(), v.st.R, job)
	if counts[HostFailed] != 3 || counts[HostQueued] != 4 {
		t.Fatalf("while paused: %v", counts)
	}
	fail.Store(false) // the admin fixed the credential
	if err := v.e.Resume(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	v.wait(job)
	counts, _ = Counts(context.Background(), v.st.R, job)
	if counts[HostFailed] != 3 || counts[HostSucceeded] != 4 {
		t.Fatalf("after resume: %v", counts)
	}
}

func TestCancelTerminatesRunningAndQueued(t *testing.T) {
	hosts := []string{"a", "b", "c", "d"}
	v := newEnv(t, Config{}, hosts...)
	started := make(chan struct{}, 4)
	v.exec.do = func(ctx context.Context, _ string, _ int, _ driver.OutputSink) (*driver.OpResult, error) {
		started <- struct{}{}
		<-ctx.Done()
		return nil, &model.OpError{Class: model.ClassCancelled, Message: "cancelled"}
	}
	spec := cmdSpec(hosts...)
	spec.Strategy.Concurrency = 2
	job := v.run(spec)
	<-started
	<-started
	if err := v.e.Cancel(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	if j := v.wait(job); j.Status != JobCancelled {
		t.Fatalf("status %s", j.Status)
	}
	counts, _ := Counts(context.Background(), v.st.R, job)
	if counts[HostCancelled] != 4 {
		t.Fatalf("counts %v", counts)
	}
}

func TestTimeout(t *testing.T) {
	v := newEnv(t, Config{}, "a")
	v.exec.do = func(ctx context.Context, _ string, _ int, _ driver.OutputSink) (*driver.OpResult, error) {
		<-ctx.Done()
		return nil, &model.OpError{Class: model.ClassTimeout, Message: "timed out"}
	}
	spec := cmdSpec("a")
	spec.Steps[0].Command.TimeoutS = 1
	job := v.run(spec)
	v.wait(job)
	if h := v.hosts(job)["a"]; h.State != HostTimedOut {
		t.Fatalf("state %s", h.State)
	}
}

func TestExclusiveLockSerializesJobs(t *testing.T) {
	v := newEnv(t, Config{}, "a")
	release := make(chan struct{})
	first := true
	var mu sync.Mutex
	v.exec.do = func(context.Context, string, int, driver.OutputSink) (*driver.OpResult, error) {
		mu.Lock()
		isFirst := first
		first = false
		mu.Unlock()
		if isFirst {
			<-release
		}
		return ok()
	}
	j1 := v.run(cmdSpec("a"))
	// The first job holds the host before the second is submitted.
	deadline := time.Now().Add(5 * time.Second)
	for v.hosts(j1)["a"].State != HostRunning {
		if time.Now().After(deadline) {
			t.Fatalf("first job not running: %s", v.hosts(j1)["a"].State)
		}
		time.Sleep(5 * time.Millisecond)
	}
	j2 := v.run(cmdSpec("a"))
	deadline = time.Now().Add(5 * time.Second)
	for v.hosts(j2)["a"].State != HostWaitingLock {
		if time.Now().After(deadline) {
			t.Fatalf("second job not waiting for lock: %s", v.hosts(j2)["a"].State)
		}
		time.Sleep(10 * time.Millisecond)
	}
	close(release)
	v.wait(j1)
	v.wait(j2)
	if v.hosts(j2)["a"].State != HostSucceeded {
		t.Fatal("second job did not run after the lock was released")
	}
}

func TestOutputRedactionAndCap(t *testing.T) {
	v := newEnv(t, Config{OutputCap: 200}, "a", "b")
	v.exec.do = func(_ context.Context, host string, _ int, out driver.OutputSink) (*driver.OpResult, error) {
		if host == "a" {
			// The secret arrives split across two writes.
			out.Stdout().Write([]byte("password is s3cret-"))
			out.Stdout().Write([]byte("sudo-pw ok\n"))
			return ok()
		}
		for i := 0; i < 100; i++ {
			fmt.Fprintf(out.Stdout(), "line %03d of flood output\n", i)
		}
		return ok()
	}
	job := v.run(cmdSpec("a", "b"))
	v.wait(job)
	if out := v.output(job, "a"); strings.Contains(out, "s3cret") || !strings.Contains(out, "«redacted»") {
		t.Fatalf("redaction failed: %q", out)
	}
	out := v.output(job, "b")
	if !strings.Contains(out, "output limit reached") || len(out) > 300 {
		t.Fatalf("cap not enforced (%d bytes): %q", len(out), out)
	}
	steps, _ := v.st.R.ListJobSteps(context.Background(), db.ListJobStepsParams{JobID: job, HostID: "b"})
	if len(steps) != 1 || steps[0].Truncated != 1 {
		t.Fatalf("truncated flag not set: %+v", steps)
	}
}

func TestRestartMarksInterrupted(t *testing.T) {
	v := newEnv(t, Config{}, "a", "b")
	block := make(chan struct{})
	defer close(block)
	v.exec.do = func(context.Context, string, int, driver.OutputSink) (*driver.OpResult, error) {
		<-block
		return ok()
	}
	job := v.run(cmdSpec("a", "b"))
	// Both hosts run at once (the default concurrency) and block in their
	// operation: from then on the first engine writes nothing until the
	// test ends, so it can't overwrite what the second engine records.
	deadline := time.Now().Add(5 * time.Second)
	for hs := v.hosts(job); hs["a"].State != HostRunning || hs["b"].State != HostRunning; hs = v.hosts(job) {
		if time.Now().After(deadline) {
			t.Fatalf("hosts never started: %+v", hs)
		}
		time.Sleep(10 * time.Millisecond)
	}
	// Simulate a controller restart: a new engine starts on the same database.
	logs := logging.Discard()
	e2 := New(v.st, fakeResolver{}, v.exec, audit.NewWriter(v.st, logs.Audit), logs.App, Config{})
	if err := e2.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	j, _ := v.st.R.GetJob(context.Background(), job)
	if j.Status != JobInterrupted {
		t.Fatalf("job status %s", j.Status)
	}
	for _, h := range v.hosts(job) {
		if h.State != "interrupted" {
			t.Fatalf("host %s state %s", h.HostID, h.State)
		}
	}
}

func TestHubReplay(t *testing.T) {
	h := NewHub()
	h.Publish("j", "host", map[string]int{"n": 1})
	h.Publish("j", "host", map[string]int{"n": 2})
	sub := h.Subscribe("j", 1)
	if len(sub.Replay) != 1 || sub.Replay[0].ID != 2 || !sub.Complete {
		t.Fatalf("replay %+v", sub)
	}
	h.Publish("j", "output", map[string]int{"n": 3})
	if ev := <-sub.Events; ev.ID != 3 {
		t.Fatalf("live event %+v", ev)
	}
	h.Publish("j", "end", nil)
	if _, open := <-sub.Events; open {
		// drain the end event
		if _, open := <-sub.Events; open {
			t.Fatal("channel not closed after end")
		}
	}
	if s := h.Subscribe("unknown", 5); !s.Ended || s.Complete {
		t.Fatalf("unknown job subscription %+v", s)
	}
}

func TestTargetResolution(t *testing.T) {
	v := newEnv(t, Config{}, "web1", "web2", "db1")
	ctx := context.Background()
	v.st.Writer.Exec(`UPDATE hosts SET jump_only = 1 WHERE id = 'db1'`)
	v.st.Writer.Exec(`INSERT INTO host_tags VALUES ('web1','role:web'),('web2','role:web'),('db1','role:web')`)
	hosts, warns, err := ResolveTargets(ctx, v.st.R, model.TargetSpec{Tags: []string{"role:web"}, ExcludeHosts: []string{"web2"}})
	if err != nil || len(hosts) != 1 || hosts[0].ID != "web1" || len(warns) != 1 {
		t.Fatalf("hosts %+v warns %v err %v", hosts, warns, err)
	}
	if _, _, err := ResolveTargets(ctx, v.st.R, model.TargetSpec{Hosts: []string{"db1"}}); err == nil {
		t.Fatal("explicit jump-only host accepted")
	}
	if _, _, err := ResolveTargets(ctx, v.st.R, model.TargetSpec{Hosts: []string{"nope"}}); err == nil {
		t.Fatal("unknown host accepted")
	}
}

func TestWhenAndPerOSScripts(t *testing.T) {
	v := newEnv(t, Config{}, "deb1", "rh1", "unk1")
	var mu sync.Mutex
	ran := map[string][]string{}
	v.exec.do = nil
	exec := &recordingExec{fakeExec: v.exec, record: func(host, what string) {
		mu.Lock()
		ran[host] = append(ran[host], what)
		mu.Unlock()
	}}
	v.e.exec = exec
	spec := model.JobSpec{
		Steps: []model.Operation{
			{Type: model.OpScript, Script: &model.ScriptOp{Body: model.ScriptBody{ByOS: map[string]string{
				"debian": "echo debian", "redhat": "echo redhat"}}}},
			{Type: model.OpCommand, Command: &model.CommandOp{Command: "apt-get -v", When: &model.When{OSFamily: []string{"debian"}}}},
		},
		Targets: model.TargetSpec{Hosts: []string{"deb1", "rh1", "unk1"}},
	}
	job := v.run(spec)
	v.wait(job)
	hs := v.hosts(job)
	if got := strings.Join(ran["deb1"], "|"); got != "echo debian|apt-get -v" {
		t.Fatalf("deb1 ran %q", got)
	}
	if got := strings.Join(ran["rh1"], "|"); got != "echo redhat" || hs["rh1"].State != HostSucceeded ||
		!strings.Contains(hs["rh1"].ErrorMessage, "step 2") {
		t.Fatalf("rh1 ran %q state %s msg %q", got, hs["rh1"].State, hs["rh1"].ErrorMessage)
	}
	if len(ran["unk1"]) != 0 || hs["unk1"].State != HostSkipped || !strings.Contains(hs["unk1"].ErrorMessage, "facts unknown") {
		t.Fatalf("unk1 ran %v state %s msg %q", ran["unk1"], hs["unk1"].State, hs["unk1"].ErrorMessage)
	}
}

// recordingExec records which body or command each host executed.
type recordingExec struct {
	*fakeExec
	record func(host, what string)
}

func (r *recordingExec) Prepare(_ context.Context, id string) (Path, error) {
	return fakePath{host: id, run: func(ctx context.Context, host string, op model.Operation, _ driver.OutputSink, started func()) (*driver.OpResult, error) {
		started()
		if op.Type == model.OpScript {
			r.record(host, op.Script.Body.Text)
		} else {
			r.record(host, op.Command.Command)
		}
		return ok()
	}}, nil
}

func TestHostChangedAfterTypedOps(t *testing.T) {
	v := newEnv(t, Config{}, "h1")
	type call struct {
		host     string
		lockFree bool
	}
	calls := make(chan call, 4)
	v.e.OnHostChanged(func(id string) {
		release, free := v.e.Locks().TryAcquire(id, "probe", model.LockExclusive)
		if free {
			release()
		}
		calls <- call{id, free}
	})
	v.wait(v.run(cmdSpec("h1")))
	pkgSpec := model.JobSpec{
		Steps:   []model.Operation{{Type: model.OpPackage, Package: &model.PackageOp{Action: "install", Names: []string{"curl"}}}},
		Targets: model.TargetSpec{Hosts: []string{"h1"}},
	}
	v.wait(v.run(pkgSpec))
	select {
	case c := <-calls:
		if c.host != "h1" || !c.lockFree {
			t.Fatalf("hook called with %+v; want h1 after the host lock is released", c)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("hook not called after a package job")
	}
	select {
	case c := <-calls:
		t.Fatalf("unexpected extra hook call %+v (command jobs must not trigger it)", c)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestRedactHoldsBackOnlyPossibleSecretStart(t *testing.T) {
	w := &outputWriter{secrets: [][]byte{[]byte("s3cret-sudo-pw")}, maxSec: 14}
	for _, c := range []struct{ in, out, rest string }{
		{"[drool] dnf -y install 'tree'\n", "[drool] dnf -y install 'tree'\n", ""}, // nothing held back
		{"password is s3cr", "password is ", "s3cr"},                               // possible split secret
		{"ends with s", "ends with ", "s"},
		{"has s3cret-sudo-pw inside", "has «redacted» inside", ""},
	} {
		out, rest := w.redactCut([]byte(c.in), false)
		if string(out) != c.out || string(rest) != c.rest {
			t.Errorf("redactCut(%q) = %q, %q; want %q, %q", c.in, out, rest, c.out, c.rest)
		}
	}
}
