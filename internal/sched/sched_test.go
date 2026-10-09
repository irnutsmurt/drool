package sched

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"drool/internal/alerts"
	"drool/internal/audit"
	"drool/internal/driver"
	"drool/internal/engine"
	"drool/internal/logging"
	"drool/internal/model"
	"drool/internal/store"
)

// The scheduler is tested against the real job engine (with a fake
// executor: nothing reaches a host), real M12 maintenance and an injected
// clock: no test sleeps for real scheduling time.

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) set(t time.Time) {
	c.mu.Lock()
	c.t = t
	c.mu.Unlock()
}
func (c *clock) add(d time.Duration) { c.set(c.now().Add(d)) }

type nopResolver struct{}

func (nopResolver) HostOS(context.Context, string) (model.HostOS, error) { return model.HostOS{}, nil }

// fakeExec runs every operation instantly (or as `do` says).
type fakeExec struct {
	mu    sync.Mutex
	calls map[string]int
	do    func(ctx context.Context, host string, op model.Operation) (*driver.OpResult, error)
}

type fakePath struct {
	f    *fakeExec
	host string
}

func (f *fakeExec) Prepare(_ context.Context, id string) (engine.Path, error) {
	return fakePath{f, id}, nil
}
func (p fakePath) Execute(ctx context.Context, op model.Operation, _ driver.OutputSink, started func()) (*driver.OpResult, error) {
	p.f.mu.Lock()
	p.f.calls[p.host]++
	do := p.f.do
	p.f.mu.Unlock()
	started()
	if do != nil {
		return do(ctx, p.host, op)
	}
	z := 0
	return &driver.OpResult{ExitCode: &z}, nil
}
func (fakePath) Transport() (string, string) { return "ssh", "" }
func (fakePath) JumpHostID() string          { return "" }
func (fakePath) Secrets() []string           { return nil }

type env struct {
	t    *testing.T
	st   *store.Store
	eng  *engine.Engine
	exec *fakeExec
	al   *alerts.Service
	s    *Service
	clk  *clock
	aw   *audit.Writer
}

var t0 = time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC) // a Friday

func newEnv(t *testing.T) *env {
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
	for _, u := range []struct{ id, name, role string }{{"usr_admin", "admin", "admin"}, {"usr_op", "rob", "operator"}, {"usr_view", "vera", "viewer"}} {
		if _, err := st.Writer.Exec(`INSERT INTO users (id, username, password_hash, role, created_at, updated_at, password_changed_at) VALUES (?, ?, 'x', ?, 0, 0, 0)`,
			u.id, u.name, u.role); err != nil {
			t.Fatal(err)
		}
	}
	for i := 1; i <= 12; i++ {
		id := fmt.Sprintf("h%02d", i)
		if _, err := st.Writer.Exec(`INSERT INTO hosts (id, name, connect_address, ssh_user, status, created_at, updated_at) VALUES (?, ?, '10.0.0.1', 'x', 'ready', 0, 0)`,
			id, "host-"+id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.Writer.Exec(`INSERT INTO groups (id, name, created_at, updated_at) VALUES ('grp_web', 'web', 0, 0)`); err != nil {
		t.Fatal(err)
	}
	for _, h := range []string{"h01", "h02", "h03"} {
		st.Writer.Exec(`INSERT INTO group_members (group_id, host_id) VALUES ('grp_web', ?)`, h)
	}
	logs := logging.Discard()
	aw := audit.NewWriter(st, logs.Audit)
	fe := &fakeExec{calls: map[string]int{}}
	eng := engine.New(st, nopResolver{}, fe, aw, logs.App, engine.Config{RetryDelay: time.Millisecond})
	if err := eng.Start(ctx); err != nil {
		t.Fatal(err)
	}
	clk := &clock{t: t0}
	al := alerts.New(alerts.Deps{Store: st, Audit: aw, Log: logs.App, Config: alerts.Config{DisableWorkers: true}})
	al.SetClock(clk.now)
	s := New(Deps{Store: st, Engine: eng, Alerts: al, Audit: aw, Log: logs.App})
	s.SetClock(clk.now)
	return &env{t: t, st: st, eng: eng, exec: fe, al: al, s: s, clk: clk, aw: aw}
}

// restart is a controller restart: a new engine (interrupting active
// jobs) and a new scheduler on the same database.
func (e *env) restart() {
	e.t.Helper()
	e.eng.Wait()
	logs := logging.Discard()
	e.eng = engine.New(e.st, nopResolver{}, e.exec, e.aw, logs.App, engine.Config{RetryDelay: time.Millisecond})
	if err := e.eng.Start(context.Background()); err != nil {
		e.t.Fatal(err)
	}
	e.s = New(Deps{Store: e.st, Engine: e.eng, Alerts: e.al, Audit: e.aw, Log: logs.App})
	e.s.SetClock(e.clk.now)
}

var rob = Actor{UserID: "usr_op", Name: "rob"}

func updateSteps() []model.Operation {
	return []model.Operation{{Type: model.OpSystemUpdate, SystemUpdate: &model.SystemUpdateOp{Reboot: "if_required"}}}
}

func (e *env) create(in ScheduleInput) *Schedule {
	e.t.Helper()
	sc, err := e.s.CreateSchedule(context.Background(), in, rob, func(*sql.Tx, *Schedule) error { return nil })
	if err != nil {
		e.t.Fatal(err)
	}
	return sc
}

func weeklyJob(name string, targets model.TargetSpec) ScheduleInput {
	return ScheduleInput{Kind: ScheduleJob, Name: name, Config: Config{
		Recurrence: Recurrence{Kind: KindWeekly, Time: "03:00", Weekdays: []string{"sun"}}, TimeZone: "UTC",
		Steps: updateSteps(), Targets: targets}}
}

func (e *env) tick() { e.t.Helper(); e.s.Tick(context.Background()) }

func (e *env) occ(id string) []*Occurrence {
	e.t.Helper()
	o, err := e.s.Occurrences(context.Background(), id, 0, "", 200)
	if err != nil {
		e.t.Fatal(err)
	}
	return o
}

func (e *env) jobs(scheduleID string) []string {
	rows, err := e.st.Reader.Query(`SELECT id FROM jobs WHERE schedule_id = ? ORDER BY created_at`, scheduleID)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		rows.Scan(&id)
		out = append(out, id)
	}
	return out
}

func (e *env) jobHosts(job string) map[string]string {
	rows, _ := e.st.Reader.Query(`SELECT host_id, state FROM job_hosts WHERE job_id = ?`, job)
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var h, s string
		rows.Scan(&h, &s)
		out[h] = s
	}
	return out
}

func (e *env) jobStatus(job string) string {
	var s string
	e.st.Reader.QueryRow(`SELECT status FROM jobs WHERE id = ?`, job).Scan(&s)
	return s
}

func (e *env) waitJob(job string) string {
	e.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if s := e.jobStatus(job); s == "completed" || s == "cancelled" || s == "interrupted" {
			e.eng.Wait()
			return s
		}
		time.Sleep(5 * time.Millisecond)
	}
	e.t.Fatalf("job %s did not finish (%s)", job, e.jobStatus(job))
	return ""
}

func (e *env) next(id string) time.Time {
	sc, err := e.s.GetSchedule(context.Background(), id)
	if err != nil {
		e.t.Fatal(err)
	}
	if sc.NextRunAt == nil {
		return time.Time{}
	}
	return time.UnixMilli(*sc.NextRunAt).UTC()
}

func TestScheduleRunsOrdinaryJobs(t *testing.T) {
	e := newEnv(t)
	sc := e.create(weeklyJob("Sunday updates", model.TargetSpec{Groups: []string{"grp_web"}}))
	sunday := time.Date(2026, 10, 11, 3, 0, 0, 0, time.UTC)
	if !e.next(sc.ID).Equal(sunday) || !strings.Contains(sc.When, "Every Sunday at 03:00 (UTC)") {
		t.Fatalf("next %s, when %q", e.next(sc.ID), sc.When)
	}
	e.tick()
	if len(e.jobs(sc.ID)) != 0 {
		t.Fatal("ran before its time")
	}
	e.clk.set(sunday.Add(5 * time.Second))
	e.tick()
	jobs := e.jobs(sc.ID)
	if len(jobs) != 1 {
		t.Fatalf("jobs %v", jobs)
	}
	e.waitJob(jobs[0])
	if hs := e.jobHosts(jobs[0]); len(hs) != 3 || hs["h01"] != "succeeded" {
		t.Fatalf("an ordinary job on the group's hosts: %v", hs)
	}
	o := e.occ(sc.ID)
	if len(o) != 1 || o[0].Status != OccDispatched || o[0].JobID != jobs[0] || o[0].ScheduledFor != sunday.UnixMilli() || o[0].DispatchedAt == nil ||
		!strings.HasPrefix(o[0].Outcome, "Completed: 3 succeeded") {
		t.Fatalf("occurrence %+v", o)
	}
	if !e.next(sc.ID).Equal(sunday.AddDate(0, 0, 7)) {
		t.Fatalf("next %s", e.next(sc.ID))
	}
	// The job remembers its origin and runs as the owner; the audit says
	// "system, via schedule X, owned by Y".
	var createdBy, schedID, occID string
	e.st.Reader.QueryRow(`SELECT created_by, schedule_id, occurrence_id FROM jobs WHERE id = ?`, jobs[0]).Scan(&createdBy, &schedID, &occID)
	if createdBy != "usr_op" || schedID != sc.ID || occID != o[0].ID {
		t.Fatalf("job origin %s %s %s", createdBy, schedID, occID)
	}
	var actorType, actorName, details string
	e.st.Reader.QueryRow(`SELECT actor_type, actor_name, details_json FROM audit_events WHERE action = 'job.create' AND target_id = ?`, jobs[0]).Scan(&actorType, &actorName, &details)
	if actorType != "system" || actorName != "schedule Sunday updates" || !strings.Contains(details, `"owner":"rob"`) || !strings.Contains(details, `"via":"schedule"`) ||
		!strings.Contains(details, `"summary":"system execution via schedule \"Sunday updates\", owned by rob"`) {
		t.Fatalf("audit %s %s %s", actorType, actorName, details)
	}
	// A tick at the same time again creates nothing.
	e.tick()
	if len(e.jobs(sc.ID)) != 1 {
		t.Fatal("duplicate")
	}
}

func TestTargetsResolvedAtEachRun(t *testing.T) {
	e := newEnv(t)
	sc := e.create(weeklyJob("web", model.TargetSpec{Groups: []string{"grp_web"}}))
	sunday := time.Date(2026, 10, 11, 3, 0, 0, 0, time.UTC)
	e.clk.set(sunday)
	e.tick()
	first := e.jobs(sc.ID)[0]
	e.waitJob(first)
	e.st.Writer.Exec(`INSERT INTO group_members (group_id, host_id) VALUES ('grp_web', 'h09')`)
	if hs := e.jobHosts(first); len(hs) != 3 || hs["h09"] != "" {
		t.Fatalf("the first run keeps the hosts it started with: %v", hs)
	}
	e.clk.set(sunday.AddDate(0, 0, 7))
	e.tick()
	second := e.jobs(sc.ID)[1]
	e.waitJob(second)
	if hs := e.jobHosts(second); len(hs) != 4 || hs["h09"] != "succeeded" {
		t.Fatalf("a host added to the group takes part next time: %v", hs)
	}
	var n int
	e.st.Reader.QueryRow(`SELECT count(*) FROM job_hosts WHERE host_id = 'h09'`).Scan(&n)
	if n != 1 {
		t.Fatal("no retroactive run for the new host")
	}
}

func TestDSTNeverDuplicates(t *testing.T) {
	e := newEnv(t)
	in := weeklyJob("la", model.TargetSpec{Hosts: []string{"h01"}})
	in.Recurrence = Recurrence{Kind: KindDaily, Time: "01:30"}
	in.TimeZone = "America/Los_Angeles"
	e.clk.set(time.Date(2027, 11, 6, 12, 0, 0, 0, time.UTC))
	sc := e.create(in)
	// Fall back: 01:30 happens at 08:30Z (PDT) and 09:30Z (PST).
	for _, at := range []string{"2027-11-07T08:30:01Z", "2027-11-07T09:00:00Z", "2027-11-07T09:30:01Z", "2027-11-07T10:00:00Z"} {
		e.clk.set(utc(at))
		e.tick()
	}
	jobs := e.jobs(sc.ID)
	if len(jobs) != 1 {
		t.Fatalf("one run on the fall-back day: %v", jobs)
	}
	e.waitJob(jobs[0])
	if n := e.next(sc.ID); n.In(zone(t, "America/Los_Angeles")).Format("2006-01-02 15:04 MST") != "2027-11-08 01:30 PST" {
		t.Fatal(n)
	}
	// Spring forward: 02:30 doesn't exist; it runs at 03:00 PDT, once.
	in2 := weeklyJob("la2", model.TargetSpec{Hosts: []string{"h02"}})
	in2.Recurrence = Recurrence{Kind: KindDaily, Time: "02:30"}
	in2.TimeZone = "America/Los_Angeles"
	e.clk.set(time.Date(2027, 3, 13, 20, 0, 0, 0, time.UTC))
	sc2 := e.create(in2)
	if n := e.next(sc2.ID); !n.Equal(utc("2027-03-14T10:00:00Z")) {
		t.Fatalf("the first valid time after the gap: %s", n)
	}
	for _, at := range []string{"2027-03-14T10:00:01Z", "2027-03-14T10:30:00Z", "2027-03-14T11:00:00Z"} {
		e.clk.set(utc(at))
		e.tick()
	}
	if j := e.jobs(sc2.ID); len(j) != 1 {
		t.Fatalf("%v", j)
	} else {
		e.waitJob(j[0])
	}
}

func TestDurability(t *testing.T) {
	sunday := time.Date(2026, 10, 11, 3, 0, 0, 0, time.UTC)
	t.Run("restart before the occurrence", func(t *testing.T) {
		e := newEnv(t)
		sc := e.create(weeklyJob("a", model.TargetSpec{Hosts: []string{"h01"}}))
		e.restart()
		e.clk.set(sunday.Add(time.Minute))
		e.tick()
		if j := e.jobs(sc.ID); len(j) != 1 {
			t.Fatal(j)
		} else {
			e.waitJob(j[0])
		}
	})
	t.Run("crash while the occurrence is being created: rolled back, then created once", func(t *testing.T) {
		e := newEnv(t)
		sc := e.create(weeklyJob("b", model.TargetSpec{Hosts: []string{"h01"}}))
		e.clk.set(sunday.Add(10 * time.Second))
		e.s.SetHooks(Hooks{BeforeCommit: func(string) error { return errors.New("simulated crash") }})
		e.tick()
		var n int
		e.st.Reader.QueryRow(`SELECT count(*) FROM schedule_occurrences WHERE schedule_id = ?`, sc.ID).Scan(&n)
		if n != 0 || len(e.jobs(sc.ID)) != 0 {
			t.Fatal("nothing commits when the transaction doesn't")
		}
		e.restart()
		e.clk.add(time.Minute)
		e.tick()
		if j := e.jobs(sc.ID); len(j) != 1 {
			t.Fatal(j)
		} else {
			e.waitJob(j[0])
		}
		if o := e.occ(sc.ID); len(o) != 1 || o[0].Status != OccDispatched {
			t.Fatalf("%+v", o)
		}
	})
	t.Run("crash right after the job was created: never a second job, never re-run silently", func(t *testing.T) {
		e := newEnv(t)
		sc := e.create(weeklyJob("c", model.TargetSpec{Hosts: []string{"h01"}}))
		e.clk.set(sunday.Add(10 * time.Second))
		e.s.SetHooks(Hooks{NoLaunch: true})
		e.tick()
		jobs := e.jobs(sc.ID)
		if len(jobs) != 1 || e.jobStatus(jobs[0]) != "pending" {
			t.Fatal(jobs)
		}
		e.restart() // the engine marks the never-launched job interrupted
		if e.jobStatus(jobs[0]) != "interrupted" {
			t.Fatal(e.jobStatus(jobs[0]))
		}
		e.s.st().Writer.Exec(`UPDATE schedules SET next_run_at = ? WHERE id = ?`, sunday.UnixMilli(), sc.ID) // as if not advanced
		e.clk.add(time.Minute)
		e.tick()
		if j := e.jobs(sc.ID); len(j) != 1 {
			t.Fatalf("the occurrence's uniqueness prevents a second job: %v", j)
		}
		if o := e.occ(sc.ID); len(o) != 1 || !strings.HasPrefix(o[0].Outcome, "Interrupted") {
			t.Fatalf("%+v", o)
		}
		if !e.next(sc.ID).Equal(sunday.AddDate(0, 0, 7)) {
			t.Fatal("the schedule continues with the next occurrence")
		}
	})
	t.Run("two schedulers at once", func(t *testing.T) {
		e := newEnv(t)
		sc := e.create(weeklyJob("d", model.TargetSpec{Hosts: []string{"h01"}}))
		other := New(Deps{Store: e.st, Engine: e.eng, Alerts: e.al, Audit: e.aw, Log: logging.Discard().App})
		other.SetClock(e.clk.now)
		e.clk.set(sunday.Add(10 * time.Second))
		var wg sync.WaitGroup
		for _, s := range []*Service{e.s, other, e.s, other} {
			wg.Add(1)
			go func() { defer wg.Done(); s.Tick(context.Background()) }()
		}
		wg.Wait()
		if j := e.jobs(sc.ID); len(j) != 1 {
			t.Fatalf("%v", j)
		} else {
			e.waitJob(j[0])
		}
	})
}

func TestMissedRuns(t *testing.T) {
	daily := func(name, policy string) ScheduleInput {
		in := weeklyJob(name, model.TargetSpec{Hosts: []string{"h01"}})
		in.Recurrence = Recurrence{Kind: KindDaily, Time: "03:00"}
		in.MissedPolicy = policy
		return in
	}
	t.Run("down for five days: skipped and recorded, never replayed", func(t *testing.T) {
		e := newEnv(t)
		sc := e.create(daily("d", ""))
		e.clk.set(time.Date(2026, 10, 15, 9, 0, 0, 0, time.UTC)) // runs due Oct 10–15 at 03:00
		e.restart()
		e.tick()
		if j := e.jobs(sc.ID); len(j) != 0 {
			t.Fatalf("never five updates in succession: %v", j)
		}
		o := e.occ(sc.ID)
		if len(o) != 1 || o[0].Status != OccMissed || o[0].MissedCount != 6 || !strings.Contains(o[0].Outcome, "Missed 6 runs") {
			t.Fatalf("%+v", o)
		}
		if !e.next(sc.ID).Equal(time.Date(2026, 10, 16, 3, 0, 0, 0, time.UTC)) {
			t.Fatal(e.next(sc.ID))
		}
		var title string
		e.st.Reader.QueryRow(`SELECT title FROM events WHERE kind = 'schedule.missed'`).Scan(&title)
		if !strings.Contains(title, "6 scheduled run(s) were missed") {
			t.Fatalf("event %q", title)
		}
	})
	t.Run("run one missed occurrence when DROOL returns", func(t *testing.T) {
		e := newEnv(t)
		sc := e.create(daily("r", MissedRunOnce))
		e.clk.set(time.Date(2026, 10, 15, 9, 0, 0, 0, time.UTC))
		e.restart()
		e.tick()
		j := e.jobs(sc.ID)
		if len(j) != 1 {
			t.Fatalf("exactly one catch-up run: %v", j)
		}
		e.waitJob(j[0])
		o := e.occ(sc.ID)
		if len(o) != 2 || o[0].Status != OccDispatched || o[0].ScheduledFor != time.Date(2026, 10, 15, 3, 0, 0, 0, time.UTC).UnixMilli() ||
			!strings.Contains(o[0].Reason, "started late") || o[1].Status != OccMissed || o[1].MissedCount != 5 {
			t.Fatalf("%+v %+v", o[0], o[1])
		}
	})
	t.Run("a quick restart still runs", func(t *testing.T) {
		e := newEnv(t)
		sc := e.create(daily("q", ""))
		e.clk.set(time.Date(2026, 10, 10, 3, 2, 0, 0, time.UTC))
		e.restart()
		e.tick()
		if j := e.jobs(sc.ID); len(j) != 1 {
			t.Fatal(j)
		} else {
			e.waitJob(j[0])
		}
	})
	t.Run("one-time: runs late by default, expires past its limit", func(t *testing.T) {
		e := newEnv(t)
		in := weeklyJob("once", model.TargetSpec{Hosts: []string{"h01"}})
		in.Recurrence = Recurrence{Kind: KindOnce, At: "2026-10-10T03:00"}
		late := e.create(in)
		in.Name, in.LateLimitMinutes = "once-limited", 60
		limited := e.create(in)
		e.clk.set(time.Date(2026, 10, 10, 6, 0, 0, 0, time.UTC))
		e.restart()
		e.tick()
		if j := e.jobs(late.ID); len(j) != 1 {
			t.Fatal(j)
		} else {
			e.waitJob(j[0])
		}
		if o := e.occ(late.ID); !strings.Contains(o[0].Reason, "started late") {
			t.Fatalf("%+v", o[0])
		}
		if j := e.jobs(limited.ID); len(j) != 0 {
			t.Fatal("expired")
		}
		if o := e.occ(limited.ID); len(o) != 1 || o[0].Status != OccExpired || !strings.Contains(o[0].Outcome, "more than 60 minutes late") {
			t.Fatalf("%+v", o)
		}
		if sc, _ := e.s.GetSchedule(context.Background(), late.ID); sc.NextRunAt != nil {
			t.Fatal("a one-time schedule has no next run")
		}
	})
}

func TestOverlap(t *testing.T) {
	block := func(e *env) chan struct{} {
		release := make(chan struct{})
		e.exec.do = func(ctx context.Context, host string, op model.Operation) (*driver.OpResult, error) {
			select {
			case <-release:
			case <-ctx.Done():
			}
			z := 0
			return &driver.OpResult{ExitCode: &z}, nil
		}
		return release
	}
	hourly := func(name, policy string) ScheduleInput {
		in := weeklyJob(name, model.TargetSpec{Hosts: []string{"h01"}})
		in.Recurrence = Recurrence{Kind: KindHourly, At: "2026-10-09T13:00", EveryHours: 1}
		in.OverlapPolicy = policy
		return in
	}
	t.Run("default: skip while the previous run is active", func(t *testing.T) {
		e := newEnv(t)
		release := block(e)
		sc := e.create(hourly("h", ""))
		e.clk.set(utc("2026-10-09T13:00:05Z"))
		e.tick()
		e.clk.set(utc("2026-10-09T14:00:05Z"))
		e.tick()
		e.clk.set(utc("2026-10-09T15:00:05Z"))
		e.tick()
		if j := e.jobs(sc.ID); len(j) != 1 {
			t.Fatalf("no overlapping runs: %v", j)
		}
		o := e.occ(sc.ID)
		if len(o) != 3 || o[0].Status != OccSkipped || !strings.Contains(o[0].Outcome, "previous run") || o[1].Status != OccSkipped {
			t.Fatalf("%+v", o)
		}
		close(release)
		e.waitJob(e.jobs(sc.ID)[0])
	})
	t.Run("queue one: one waits, the rest are skipped; the queued one runs after", func(t *testing.T) {
		e := newEnv(t)
		release := block(e)
		sc := e.create(hourly("q", OverlapQueue))
		for _, at := range []string{"2026-10-09T13:00:05Z", "2026-10-09T14:00:05Z", "2026-10-09T15:00:05Z"} {
			e.clk.set(utc(at))
			e.tick()
		}
		o := e.occ(sc.ID)
		if o[1].Status != OccQueued || o[0].Status != OccSkipped || !strings.Contains(o[0].Reason, "already queued") {
			t.Fatalf("%+v %+v", o[0], o[1])
		}
		close(release)
		e.waitJob(e.jobs(sc.ID)[0])
		e.exec.do = nil
		e.tick() // the queued run starts now
		j := e.jobs(sc.ID)
		if len(j) != 2 {
			t.Fatalf("%v", j)
		}
		e.waitJob(j[1])
		e.tick()
		if len(e.jobs(sc.ID)) != 2 {
			t.Fatal("never more than one queued run")
		}
	})
}

func TestEditEnableRunNow(t *testing.T) {
	e := newEnv(t)
	sc := e.create(weeklyJob("w", model.TargetSpec{Hosts: []string{"h01"}}))
	ctx := context.Background()
	release := make(chan struct{})
	e.exec.do = func(ctx context.Context, host string, op model.Operation) (*driver.OpResult, error) {
		select {
		case <-release:
		case <-ctx.Done():
		}
		z := 0
		return &driver.OpResult{ExitCode: &z}, nil
	}
	res, err := e.s.RunNow(ctx, sc.ID, Actor{UserID: "usr_admin", Name: "admin"})
	if err != nil || res.JobID == "" {
		t.Fatal(err)
	}
	if _, err := e.s.RunNow(ctx, sc.ID, rob); !errors.Is(err, ErrBusy) {
		t.Fatalf("a second manual run while one is active: %v", err)
	}
	var createdBy string
	e.st.Reader.QueryRow(`SELECT created_by FROM jobs WHERE id = ?`, res.JobID).Scan(&createdBy)
	if createdBy != "usr_admin" || !e.next(sc.ID).Equal(time.Date(2026, 10, 11, 3, 0, 0, 0, time.UTC)) {
		t.Fatal("a manual run is the caller's, and doesn't move the schedule")
	}
	// Editing changes future runs only.
	in := weeklyJob("w", model.TargetSpec{Hosts: []string{"h01", "h02"}})
	in.Steps = []model.Operation{{Type: model.OpService, Service: &model.ServiceOp{Name: "nginx", State: "restarted"}}}
	sc2, err := e.s.UpdateSchedule(ctx, sc.ID, in, rob, false, func(*sql.Tx, *Schedule, *Schedule) error { return nil })
	if err != nil || sc2.Revision != 2 {
		t.Fatal(err)
	}
	var spec string
	e.st.Reader.QueryRow(`SELECT spec_json FROM jobs WHERE id = ?`, res.JobID).Scan(&spec)
	if !strings.Contains(spec, "system_update") || strings.Contains(spec, "nginx") {
		t.Fatal("the running job keeps its specification")
	}
	// Disabling doesn't cancel the running job; cancelling doesn't disable.
	// (The job starts in the background: wait until it runs.)
	for deadline := time.Now().Add(10 * time.Second); e.jobStatus(res.JobID) == "pending" && time.Now().Before(deadline); {
		time.Sleep(5 * time.Millisecond)
	}
	if _, err := e.s.SetEnabled(ctx, sc.ID, false, func(*sql.Tx, *Schedule) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if e.jobStatus(res.JobID) != "running" {
		t.Fatal(e.jobStatus(res.JobID))
	}
	e.clk.set(time.Date(2026, 10, 11, 3, 0, 5, 0, time.UTC))
	e.tick()
	if len(e.jobs(sc.ID)) != 1 {
		t.Fatal("disabled: no runs")
	}
	if _, err := e.s.SetEnabled(ctx, sc.ID, true, func(*sql.Tx, *Schedule) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if !e.next(sc.ID).Equal(time.Date(2026, 10, 18, 3, 0, 0, 0, time.UTC)) {
		t.Fatalf("re-enabled: the next occurrence after now, no catch-up: %s", e.next(sc.ID))
	}
	e.eng.Cancel(ctx, res.JobID)
	e.waitJob(res.JobID)
	if s, _ := e.s.GetSchedule(ctx, sc.ID); !s.Enabled {
		t.Fatal("cancelling the job doesn't disable the schedule")
	}
	close(release)
	e.exec.do = nil
	e.clk.set(time.Date(2026, 10, 18, 3, 0, 5, 0, time.UTC))
	e.tick()
	j := e.jobs(sc.ID)
	e.waitJob(j[len(j)-1])
	e.st.Reader.QueryRow(`SELECT spec_json FROM jobs WHERE id = ?`, j[len(j)-1]).Scan(&spec)
	if !strings.Contains(spec, "nginx") {
		t.Fatal("the next run uses the new revision")
	}
	if o := e.occ(sc.ID); o[0].ScheduleRevision != 2 {
		t.Fatalf("history says which revision ran: %+v", o[0])
	}
	// Archive keeps history.
	if err := e.s.ArchiveSchedule(ctx, sc.ID, func(*sql.Tx, *Schedule) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if len(e.occ(sc.ID)) == 0 || len(e.jobs(sc.ID)) == 0 {
		t.Fatal("history kept")
	}
}

func TestWorkflowRevisionsArePinned(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	wf, err := e.s.CreateWorkflow(ctx, WorkflowInput{Name: "Production update", Steps: updateSteps()}, rob, func(*sql.Tx, *Workflow) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	in := weeklyJob("prod", model.TargetSpec{Hosts: []string{"h01"}})
	in.Steps, in.Workflow = nil, &WorkflowRef{ID: wf.ID}
	sc := e.create(in)
	if sc.Config.Workflow.Revision != 1 {
		t.Fatal(sc.Config.Workflow)
	}
	_, changed, err := e.s.UpdateWorkflow(ctx, wf.ID, WorkflowInput{Name: "Production update", Steps: []model.Operation{{Type: model.OpReboot, Reboot: &model.RebootOp{}}}},
		rob, func(*sql.Tx, *Revision, *Revision) error { return nil })
	if err != nil || !changed {
		t.Fatal(err)
	}
	if _, changed, _ := e.s.UpdateWorkflow(ctx, wf.ID, WorkflowInput{Name: "Production update", Steps: []model.Operation{{Type: model.OpReboot, Reboot: &model.RebootOp{}}}},
		rob, func(*sql.Tx, *Revision, *Revision) error { return nil }); changed {
		t.Fatal("no change, no revision")
	}
	s2, _ := e.s.GetSchedule(ctx, sc.ID)
	if !s2.WorkflowUpdate || s2.WorkflowLatest != 2 {
		t.Fatalf("a newer revision is available: %+v", s2)
	}
	e.clk.set(time.Date(2026, 10, 11, 3, 0, 5, 0, time.UTC))
	e.tick()
	j := e.jobs(sc.ID)[0]
	e.waitJob(j)
	var spec string
	var rev int
	e.st.Reader.QueryRow(`SELECT spec_json, workflow_revision FROM jobs WHERE id = ?`, j).Scan(&spec, &rev)
	if !strings.Contains(spec, "system_update") || strings.Contains(spec, `"type":"reboot"`) || rev != 1 {
		t.Fatalf("the schedule runs the revision it pinned: %s rev %d", spec, rev)
	}
}

func TestOwnerAuthorization(t *testing.T) {
	sunday := time.Date(2026, 10, 11, 3, 0, 5, 0, time.UTC)
	t.Run("owner disabled: blocked, schedule disabled, explained", func(t *testing.T) {
		e := newEnv(t)
		sc := e.create(weeklyJob("a", model.TargetSpec{Hosts: []string{"h01"}}))
		e.st.Writer.Exec(`UPDATE users SET disabled = 1 WHERE id = 'usr_op'`)
		e.clk.set(sunday)
		e.tick()
		if len(e.jobs(sc.ID)) != 0 {
			t.Fatal("never runs with a stale snapshot of the owner's rights")
		}
		o := e.occ(sc.ID)
		s, _ := e.s.GetSchedule(context.Background(), sc.ID)
		if len(o) != 1 || o[0].Status != OccBlocked || !strings.Contains(o[0].Reason, "(rob) is disabled") || s.Enabled || !strings.Contains(s.Attention, "is disabled") {
			t.Fatalf("%+v %+v", o, s)
		}
		var ev string
		e.st.Reader.QueryRow(`SELECT title FROM events WHERE kind = 'schedule.disabled'`).Scan(&ev)
		if !strings.Contains(ev, `"a" was disabled`) {
			t.Fatal(ev)
		}
	})
	t.Run("owner lost the permission; an admin reassigns", func(t *testing.T) {
		e := newEnv(t)
		sc := e.create(weeklyJob("b", model.TargetSpec{Hosts: []string{"h01"}}))
		e.st.Writer.Exec(`UPDATE users SET role = 'viewer' WHERE id = 'usr_op'`)
		e.clk.set(sunday)
		e.tick()
		if o := e.occ(sc.ID); o[0].Status != OccBlocked || !strings.Contains(o[0].Reason, "no longer has the jobs.execute permission") {
			t.Fatalf("%+v", o[0])
		}
		ctx := context.Background()
		if _, err := e.s.Reassign(ctx, sc.ID, "usr_view", func(*sql.Tx, *Schedule, *Schedule) error { return nil }); err == nil {
			t.Fatal("the new owner must be able to run it")
		}
		if _, err := e.s.Reassign(ctx, sc.ID, "usr_admin", func(*sql.Tx, *Schedule, *Schedule) error { return nil }); err != nil {
			t.Fatal(err)
		}
		if _, err := e.s.SetEnabled(ctx, sc.ID, true, func(*sql.Tx, *Schedule) error { return nil }); err != nil {
			t.Fatal(err)
		}
		e.clk.add(7 * 24 * time.Hour)
		e.tick()
		j := e.jobs(sc.ID)
		if len(j) != 1 {
			t.Fatal(j)
		}
		e.waitJob(j[0])
		var by string
		e.st.Reader.QueryRow(`SELECT created_by FROM jobs WHERE id = ?`, j[0]).Scan(&by)
		if by != "usr_admin" {
			t.Fatal(by)
		}
	})
	t.Run("owner deleted", func(t *testing.T) {
		e := newEnv(t)
		sc := e.create(weeklyJob("c", model.TargetSpec{Hosts: []string{"h01"}}))
		e.st.Writer.Exec(`UPDATE schedules SET owner_id = NULL WHERE id = ?`, sc.ID)
		e.clk.set(sunday)
		e.tick()
		if o := e.occ(sc.ID); o[0].Status != OccBlocked || !strings.Contains(o[0].Reason, "deleted") {
			t.Fatalf("%+v", o[0])
		}
	})
	t.Run("file and firewall steps need their own permissions", func(t *testing.T) {
		need := RequiredPermissions(ScheduleJob, []model.Operation{{Type: model.OpFileDeploy, FileDeploy: &model.FileDeployOp{Elevated: true}},
			{Type: model.OpFirewallApply, FirewallApply: &model.FirewallApplyOp{}}})
		got := fmt.Sprint(need)
		if got != "[jobs.execute files.deploy files.elevate firewall.write]" {
			t.Fatal(got)
		}
		if MissingPermission("operator", need) != "firewall.write" {
			t.Fatal("an operator can't schedule a firewall apply")
		}
	})
}

func TestMaintenanceWindows(t *testing.T) {
	window := func(name string, targets model.TargetSpec) ScheduleInput {
		return ScheduleInput{Kind: ScheduleMaintenance, Name: name, Config: Config{
			Recurrence: Recurrence{Kind: KindWeekly, Time: "02:00", Weekdays: []string{"sun"}}, TimeZone: "UTC",
			Targets: targets, DurationMinutes: 120, Reason: "patching"}}
	}
	start := time.Date(2026, 10, 11, 2, 0, 0, 0, time.UTC)
	t.Run("a recurring window is ordinary maintenance, ending on time", func(t *testing.T) {
		e := newEnv(t)
		w := e.create(window("Sunday", model.TargetSpec{Groups: []string{"grp_web"}}))
		e.clk.set(start.Add(10 * time.Second))
		e.tick()
		m, err := e.al.ActiveWindow(context.Background(), w.ID)
		if err != nil || m == nil || *m.EndsAt != start.Add(2*time.Hour).UnixMilli() || !strings.Contains(m.Reason, `Recurring window "Sunday": patching`) {
			t.Fatalf("%+v %v", m, err)
		}
		// Live group membership.
		e.st.Writer.Exec(`INSERT INTO group_members (group_id, host_id) VALUES ('grp_web', 'h10')`)
		cv, _ := e.al.Covers(context.Background(), []string{"h10"})
		if len(cv["h10"]) != 1 || cv["h10"][0].ScheduleID != w.ID {
			t.Fatalf("%+v", cv)
		}
		// M12 ends it at the window's end.
		e.clk.set(start.Add(2*time.Hour + time.Second))
		e.al.Tick(context.Background())
		if m, _ := e.al.ActiveWindow(context.Background(), w.ID); m != nil {
			t.Fatal("ended")
		}
		if o := e.occ(w.ID); len(o) != 1 || o[0].Status != OccDispatched || o[0].MaintenanceID == "" {
			t.Fatalf("%+v", o)
		}
	})
	t.Run("restart inside the window: started for the rest of it", func(t *testing.T) {
		e := newEnv(t)
		w := e.create(window("Sunday", model.TargetSpec{Hosts: []string{"h01"}}))
		e.clk.set(start.Add(45 * time.Minute))
		e.restart()
		e.tick()
		m, _ := e.al.ActiveWindow(context.Background(), w.ID)
		if m == nil || *m.EndsAt != start.Add(2*time.Hour).UnixMilli() {
			t.Fatalf("the window keeps its original end: %+v", m)
		}
		if o := e.occ(w.ID); !strings.Contains(o[0].Reason, "started late") {
			t.Fatalf("%+v", o[0])
		}
	})
	t.Run("a window missed entirely is recorded, never started late", func(t *testing.T) {
		e := newEnv(t)
		w := e.create(window("Sunday", model.TargetSpec{Hosts: []string{"h01"}}))
		e.clk.set(start.Add(3 * time.Hour))
		e.restart()
		e.tick()
		if m, _ := e.al.ActiveWindow(context.Background(), w.ID); m != nil {
			t.Fatal("no retroactive window")
		}
		var n int
		e.st.Reader.QueryRow(`SELECT count(*) FROM maintenance`).Scan(&n)
		o := e.occ(w.ID)
		if n != 0 || len(o) != 1 || o[0].Status != OccMissed || !strings.Contains(o[0].Reason, "no maintenance was started afterwards") {
			t.Fatalf("%d %+v", n, o)
		}
		var events int
		e.st.Reader.QueryRow(`SELECT count(*) FROM events WHERE kind LIKE 'maintenance.%'`).Scan(&events)
		if events != 0 {
			t.Fatal("no maintenance events for a window DROOL never kept")
		}
	})
	t.Run("scheduled automation skips hosts in unrelated maintenance; manual runs don't", func(t *testing.T) {
		e := newEnv(t)
		ctx := context.Background()
		if _, err := e.al.StartMaintenance(ctx, alerts.MaintenanceInput{HostIDs: []string{"h02"}, Reason: "disk swap", Indefinite: true},
			alerts.Actor{Name: "admin"}, func(*sql.Tx, *alerts.Maintenance) error { return nil }); err != nil {
			t.Fatal(err)
		}
		sc := e.create(weeklyJob("web", model.TargetSpec{Groups: []string{"grp_web"}}))
		e.clk.set(time.Date(2026, 10, 11, 3, 0, 5, 0, time.UTC))
		e.tick()
		j := e.jobs(sc.ID)[0]
		e.waitJob(j)
		hs := e.jobHosts(j)
		var msg string
		e.st.Reader.QueryRow(`SELECT error_message FROM job_hosts WHERE job_id = ? AND host_id = 'h02'`, j).Scan(&msg)
		if hs["h02"] != "skipped" || msg != "skipped: maintenance active (disk swap)" || hs["h01"] != "succeeded" || e.exec.calls["h02"] != 0 {
			t.Fatalf("%v %q", hs, msg)
		}
		if o := e.occ(sc.ID); o[0].SkippedHosts != 1 {
			t.Fatalf("%+v", o[0])
		}
		res, err := e.s.RunNow(ctx, sc.ID, rob)
		if err != nil {
			t.Fatal(err)
		}
		e.waitJob(res.JobID)
		if e.jobHosts(res.JobID)["h02"] != "succeeded" {
			t.Fatal("a deliberate manual run is not blocked by maintenance")
		}
		// Everything in maintenance: the occurrence is skipped, no job.
		only := e.create(weeklyJob("h02 only", model.TargetSpec{Hosts: []string{"h02"}}))
		e.clk.set(time.Date(2026, 10, 18, 3, 0, 5, 0, time.UTC))
		e.tick()
		if o := e.occ(only.ID); len(o) != 1 || o[0].Status != OccSkipped || !strings.Contains(o[0].Reason, "in maintenance") || len(e.jobs(only.ID)) != 0 {
			t.Fatalf("%+v", o)
		}
	})
	t.Run("a workflow associated with its window runs inside it", func(t *testing.T) {
		e := newEnv(t)
		ctx := context.Background()
		w := e.create(window("Sunday", model.TargetSpec{Groups: []string{"grp_web"}}))
		// Unrelated maintenance on h03 too.
		e.clk.set(start.Add(time.Minute))
		e.tick() // the window starts
		if _, err := e.al.StartMaintenance(ctx, alerts.MaintenanceInput{HostIDs: []string{"h03"}, Reason: "unrelated", Indefinite: true},
			alerts.Actor{Name: "admin"}, func(*sql.Tx, *alerts.Maintenance) error { return nil }); err != nil {
			t.Fatal(err)
		}
		in := weeklyJob("update inside the window", model.TargetSpec{Groups: []string{"grp_web"}})
		in.Recurrence = Recurrence{Kind: KindWeekly, Time: "02:10", Weekdays: []string{"sun"}}
		in.WindowID = w.ID
		sc := e.create(in)
		pv, err := e.s.PreviewSchedule(ctx, in)
		if err != nil || pv.Window == nil {
			t.Fatal(err)
		}
		e.clk.set(start.Add(10*time.Minute + 5*time.Second))
		e.tick()
		j := e.jobs(sc.ID)[0]
		e.waitJob(j)
		hs := e.jobHosts(j)
		if hs["h01"] != "succeeded" || hs["h02"] != "succeeded" || hs["h03"] != "skipped" {
			t.Fatalf("approved inside its window, not inside an unrelated one: %v", hs)
		}
		// Next week, the window isn't active (disabled): the run is skipped.
		e.s.SetEnabled(ctx, w.ID, false, func(*sql.Tx, *Schedule) error { return nil })
		e.clk.set(start.AddDate(0, 0, 7).Add(10*time.Minute + 5*time.Second))
		e.tick()
		if o := e.occ(sc.ID); o[0].Status != OccSkipped || !strings.Contains(o[0].Reason, `window "Sunday" is not active`) {
			t.Fatalf("%+v", o[0])
		}
	})
}

func TestCanaryFromSchedule(t *testing.T) {
	e := newEnv(t)
	e.exec.do = func(ctx context.Context, host string, op model.Operation) (*driver.OpResult, error) {
		if host == "h01" {
			return nil, &model.OpError{Class: model.ClassExecution, Message: "exited with status 100"}
		}
		z := 0
		return &driver.OpResult{ExitCode: &z}, nil
	}
	in := weeklyJob("fleet", model.TargetSpec{Hosts: []string{"h01", "h02", "h03", "h04", "h05", "h06"}})
	two := 2
	in.Strategy = model.Strategy{Concurrency: 2, CanaryCount: 1, BatchSize: 2, FailureThreshold: &two}
	sc := e.create(in)
	e.clk.set(time.Date(2026, 10, 11, 3, 0, 5, 0, time.UTC))
	e.tick()
	j := e.jobs(sc.ID)[0]
	deadline := time.Now().Add(5 * time.Second)
	for e.jobStatus(j) != "paused" && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	var reason string
	e.st.Reader.QueryRow(`SELECT pause_reason FROM jobs WHERE id = ?`, j).Scan(&reason)
	if !strings.HasPrefix(reason, "Canary host-h01 failed") || len(e.exec.calls) != 1 {
		t.Fatalf("%q %v", reason, e.exec.calls)
	}
	if o := e.occ(sc.ID); !strings.HasPrefix(o[0].Outcome, "Paused: Canary") {
		t.Fatalf("%+v", o[0])
	}
	e.eng.Cancel(context.Background(), j)
	e.waitJob(j)
}

func TestPreviewAndNextRuns(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	in := weeklyJob("p", model.TargetSpec{Groups: []string{"grp_web"}, Hosts: []string{"h05", "h06", "h07"}})
	two := 2
	in.Steps = append(updateSteps(), model.Operation{Type: model.OpVerifyHost, VerifyHost: &model.VerifyHostOp{}},
		model.Operation{Type: model.OpVerifyServices, VerifyServices: &model.VerifyServicesOp{Services: []string{"jellyfin"}}})
	in.Strategy = model.Strategy{CanaryCount: 1, BatchSize: 2, FailureThreshold: &two}
	in.TimeZone = "America/Los_Angeles"
	pv, err := e.s.PreviewSchedule(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if len(pv.Targets) != 6 || len(pv.Waves) != 4 || !pv.Waves[0].Canary || pv.Waves[0].Hosts[0] != "host-h01" || len(pv.NextRuns) != 5 ||
		!strings.Contains(pv.Reboot, "only if the update requires it") || len(pv.Verify) != 2 || pv.Concurrency != 10 || *pv.Threshold != 2 {
		t.Fatalf("%+v", pv)
	}
	b, _ := json.Marshal(pv)
	for _, want := range []string{"Package changes depend on the repositories at run time", "resolved again at every run", "the new run is skipped"} {
		if !strings.Contains(string(b), want) {
			t.Errorf("preview lacks %q", want)
		}
	}
	runs, err := NextRuns(Recurrence{Kind: KindWeekly, Time: "02:30", Weekdays: []string{"sun"}}, "America/Los_Angeles", utc("2027-03-01T00:00:00Z"), 3)
	if err != nil || len(runs) != 3 || !strings.Contains(runs[1].Note, "the clocks skip 02:30") {
		t.Fatalf("%+v %v", runs, err)
	}
}

func (s *Service) st() *store.Store { return s.d.Store }
