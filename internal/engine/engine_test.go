package engine

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"drool/internal/audit"
	"drool/internal/driver"
	"drool/internal/driver/sshdriver"
	"drool/internal/inventory"
	"drool/internal/logging"
	"drool/internal/model"
	"drool/internal/sshx"
	"drool/internal/store"
	"drool/internal/store/db"
)

// fakeResolver resolves any host to a target with a known sudo password.
type fakeResolver struct{}

func (fakeResolver) Resolve(_ context.Context, id string, _ bool) (*inventory.Conn, error) {
	return &inventory.Conn{Target: &sshx.Target{HostID: id, Name: id},
		Become: sshdriver.Become{Method: "sudo", Password: "s3cret-sudo-pw"}}, nil
}

// fakeExec runs a per-host behavior.
type fakeExec struct {
	mu      sync.Mutex
	calls   map[string]int
	running atomic.Int32
	peak    atomic.Int32
	do      func(ctx context.Context, host string, call int, out driver.OutputSink) (*driver.OpResult, error)
}

func (f *fakeExec) Execute(ctx context.Context, t *sshx.Target, _ sshdriver.Become, _ model.Operation,
	out driver.OutputSink, started func()) (*driver.OpResult, error) {
	f.mu.Lock()
	f.calls[t.HostID]++
	n := f.calls[t.HostID]
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
	return f.do(ctx, t.HostID, n, out)
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

func TestFailureThresholdSkipsRemaining(t *testing.T) {
	hosts := []string{"a", "b", "c", "d", "e"}
	v := newEnv(t, Config{}, hosts...)
	v.exec.do = func(context.Context, string, int, driver.OutputSink) (*driver.OpResult, error) {
		return nil, model.Errorf(model.ClassExecution, false, "boom")
	}
	spec := cmdSpec(hosts...)
	spec.Strategy.Concurrency = 1
	two := 2
	spec.Strategy.FailureThreshold = &two
	job := v.run(spec)
	v.wait(job)
	counts, _ := Counts(context.Background(), v.st.R, job)
	if counts[HostFailed] != 2 || counts[HostSkipped] != 3 {
		t.Fatalf("counts %v", counts)
	}
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
	time.Sleep(50 * time.Millisecond)
	j2 := v.run(cmdSpec("a"))
	deadline := time.Now().Add(5 * time.Second)
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
	deadline := time.Now().Add(5 * time.Second)
	for v.hosts(job)["a"].State != HostRunning {
		if time.Now().After(deadline) {
			t.Fatal("host never started")
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
