package desired

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	"drool/internal/alerts"
	"drool/internal/engine"
	"drool/internal/logging"
	"drool/internal/model"
)

func (e *env) jobCount() int {
	var n int
	e.st.Reader.QueryRow(`SELECT count(*) FROM jobs`).Scan(&n)
	return n
}

func (e *env) events(kind string) []string {
	rows, _ := e.st.Reader.Query(`SELECT title FROM events WHERE kind = ? ORDER BY id`, kind)
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		rows.Scan(&s)
		out = append(out, s)
	}
	return out
}

func (e *env) auto() {
	e.t.Helper()
	e.s.AutoRestoreNow(e.ctx)
	e.waitJobs()
	e.s.flushEvents(e.ctx)
}

func TestAutoRestoreOffByDefaultThenBounded(t *testing.T) {
	e := newEnv(t, "a1", "a2", "a3")
	for _, h := range []string{"a1", "a2", "a3"} {
		e.setUnit(h, "nginx.service", "active", "enabled")
	}
	e.reportAll()
	b := e.baseline("Server Defaults", []Resource{svc("nginx", StateRunning, nil)}, everywhere)
	e.step()
	e.setUnit("a1", "nginx.service", "inactive", "enabled")
	e.reportAll()
	e.step()
	if st := e.status("a1", "service:nginx.service"); st != StatusDrift {
		t.Fatal(st)
	}
	// Off by default: drift is reported, nothing is corrected.
	e.auto()
	if n := e.jobCount(); n != 0 {
		t.Fatalf("%d jobs with automatic restore off", n)
	}
	if v, _ := e.s.Baseline(e.ctx, b.ID); v.Auto.Enabled {
		t.Fatal("automatic restore is on by default")
	}
	// An administrator turns it on and becomes the accountable owner.
	v, err := e.s.SetAuto(e.ctx, b.ID, true, nil, admin)
	if err != nil || !v.Auto.Enabled || v.Auto.Owner.Name != "admin" {
		t.Fatalf("%+v %v", v, err)
	}
	e.auto()
	if n := e.jobCount(); n != 1 {
		t.Fatalf("%d jobs", n)
	}
	if st := e.status("a1", "service:nginx.service"); st != StatusCompliant {
		t.Fatal("not restored:", st)
	}
	h := e.history("a1", "service:nginx.service")
	if kinds(h) != "drift_detected,restore_requested,restored(restored)" || !strings.Contains(h[1].Detail, "automatic restore started") || h[1].Actor != "admin" {
		t.Fatalf("%s %+v", kinds(h), h[1])
	}
	var who, det string
	e.st.Reader.QueryRow(`SELECT actor_type, details_json FROM audit_events WHERE action = 'desired.restore'`).Scan(&who, &det)
	if who != "system" || !strings.Contains(det, `system execution via automatic restore of baseline \"Server Defaults\", owned by admin`) {
		t.Fatalf("audit %s %s", who, det)
	}
	var origin, kind string
	e.st.Reader.QueryRow(`SELECT j.created_by, c.kind FROM jobs j JOIN ds_reconciles c ON c.id = j.reconcile_id`).Scan(&origin, &kind)
	if origin != "usr_admin" || kind != "auto" {
		t.Fatalf("job as %s (%s)", origin, kind)
	}

	// Maintenance holds automatic restore back (observation continues);
	// after it ends, a short grace first.
	noTx := func(*sql.Tx, *alerts.Maintenance) error { return nil }
	m, err := e.al.StartMaintenance(e.ctx, alerts.MaintenanceInput{HostIDs: []string{"h_a2"}, Reason: "disk swap", Indefinite: true}, alerts.Actor{UserID: "usr_op", Name: "olga"}, noTx)
	if err != nil {
		t.Fatal(err)
	}
	e.setUnit("a2", "nginx.service", "inactive", "enabled")
	e.reportAll()
	e.step()
	if st := e.status("a2", "service:nginx.service"); st != StatusDrift {
		t.Fatal("drift hidden by maintenance:", st)
	}
	e.auto()
	if n := e.jobCount(); n != 1 {
		t.Fatal("corrected during maintenance")
	}
	// A manual restore is still possible, with a warning.
	p, err := e.s.PreviewRestore(e.ctx, Selection{HostIDs: []string{"h_a2"}}, RestoreOptions{})
	if err != nil || len(p.Items) != 1 || len(p.Warnings) != 1 || !strings.Contains(p.Warnings[0].Message, "in maintenance (disk swap)") {
		t.Fatalf("%+v %v", p, err)
	}
	if _, err := e.al.EndMaintenance(e.ctx, m.ID, alerts.Actor{UserID: "usr_op", Name: "olga"}, noTx); err != nil {
		t.Fatal(err)
	}
	e.clk.add(time.Minute)
	e.auto()
	if n := e.jobCount(); n != 1 {
		t.Fatal("corrected within the grace after maintenance")
	}
	e.clk.add(5 * time.Minute)
	e.auto()
	if n := e.jobCount(); n != 2 || e.status("a2", "service:nginx.service") != StatusCompliant {
		t.Fatalf("after the grace: %d jobs, %s", n, e.status("a2", "service:nginx.service"))
	}

	// Repeated failure backs off, then pauses for that resource.
	e.f.do(func() { e.f.hosts["h_a3"].failService["nginx.service"] = "Job for nginx.service failed" })
	e.setUnit("a3", "nginx.service", "inactive", "enabled")
	e.reportAll()
	e.step()
	e.auto()
	r := e.resource("a3", "service:nginx.service")
	if r.Status != StatusFailed || r.Auto.Attempts != 1 || r.Auto.NextAt == nil {
		t.Fatalf("after one failure %+v", r)
	}
	jobs := e.jobCount()
	e.auto()
	if e.jobCount() != jobs {
		t.Fatal("retried before the backoff")
	}
	e.clk.add(16 * time.Minute)
	e.auto()
	e.clk.add(61 * time.Minute)
	e.auto()
	r = e.resource("a3", "service:nginx.service")
	if r.Auto.Attempts != 3 || !strings.Contains(r.Auto.Paused, "failed 3 times; paused") {
		t.Fatalf("after three failures %+v", r.Auto)
	}
	jobs = e.jobCount()
	e.clk.add(24 * time.Hour)
	e.auto()
	if e.jobCount() != jobs {
		t.Fatal("kept trying after the pause")
	}
	if ev := e.events("desired.auto_paused"); len(ev) != 1 || !strings.Contains(ev[0], "nginx.service on a3: it failed 3 times") {
		t.Fatalf("events %v", ev)
	}
	// The drift is still visible.
	if st := e.status("a3", "service:nginx.service"); st != StatusFailed {
		t.Fatal(st)
	}
}

func TestAutoRestoreStopsFighting(t *testing.T) {
	e := newEnv(t, "a1")
	e.setUnit("a1", "nginx.service", "active", "enabled")
	e.reportAll()
	b := e.baseline("Server Defaults", []Resource{svc("nginx", StateRunning, nil)}, everywhere)
	if _, err := e.s.SetAuto(e.ctx, b.ID, true, nil, admin); err != nil {
		t.Fatal(err)
	}
	e.step()
	for i := 0; i < 4; i++ {
		// Someone (or something) on the host keeps stopping it: restored
		// three times, then the third drift after a restore pauses it.
		e.setUnit("a1", "nginx.service", "inactive", "enabled")
		e.reportAll()
		e.step()
		e.auto()
		e.clk.add(time.Hour)
	}
	r := e.resource("a1", "service:nginx.service")
	if !strings.Contains(r.Auto.Paused, "drifted again 3 times within a day") || r.Status != StatusDrift || e.jobCount() != 3 {
		t.Fatalf("%s %+v, %d jobs", r.Status, r.Auto, e.jobCount())
	}
	jobs := e.jobCount()
	e.auto()
	if e.jobCount() != jobs {
		t.Fatal("still fighting")
	}
	// Turning automatic restore on again (a deliberate act) starts afresh.
	if _, err := e.s.SetAuto(e.ctx, b.ID, true, nil, admin); err != nil {
		t.Fatal(err)
	}
	if r := e.resource("a1", "service:nginx.service"); r.Auto.Paused != "" {
		t.Fatal("not reset")
	}
}

func TestAutoRestoreOwnerLosesPermission(t *testing.T) {
	e := newEnv(t, "a1")
	e.setUnit("a1", "nginx.service", "active", "enabled")
	e.reportAll()
	b := e.baseline("Server Defaults", []Resource{svc("nginx", StateRunning, nil)}, everywhere)
	if _, err := e.s.SetAuto(e.ctx, b.ID, true, nil, Actor{UserID: "usr_admin2", Name: "ada"}); err != nil {
		t.Fatal(err)
	}
	// The owner is demoted.
	e.st.Writer.Exec(`UPDATE users SET role = 'operator' WHERE id = 'usr_admin2'`)
	e.setUnit("a1", "nginx.service", "inactive", "enabled")
	e.reportAll()
	e.step()
	e.auto()
	if n := e.jobCount(); n != 0 {
		t.Fatal("ran without the owner's permission")
	}
	v, _ := e.s.Baseline(e.ctx, b.ID)
	if !strings.Contains(v.Auto.Attention, "its owner (ada) no longer has the desired.auto permission") {
		t.Fatalf("%+v", v.Auto)
	}
	if ev := e.events("desired.auto_paused"); len(ev) != 1 {
		t.Fatal(ev)
	}
	var n int
	e.st.Reader.QueryRow(`SELECT count(*) FROM audit_events WHERE action = 'desired.auto_paused'`).Scan(&n)
	if n != 1 {
		t.Fatal("audit", n)
	}
	// Paused once; later passes add nothing.
	e.auto()
	if ev := e.events("desired.auto_paused"); len(ev) != 1 {
		t.Fatal(ev)
	}
	// A disabled owner pauses it too (after it was resumed by another admin).
	if _, err := e.s.SetAuto(e.ctx, b.ID, true, nil, Actor{UserID: "usr_admin2", Name: "ada"}); err != nil {
		t.Fatal(err)
	}
	e.st.Writer.Exec(`UPDATE users SET role = 'admin', disabled = 1 WHERE id = 'usr_admin2'`)
	e.auto()
	v, _ = e.s.Baseline(e.ctx, b.ID)
	if !strings.Contains(v.Auto.Attention, "its owner's account (ada) is disabled") || e.jobCount() != 0 {
		t.Fatalf("%+v", v.Auto)
	}
}

func TestAutoRestoreRolloutPausesOnCanary(t *testing.T) {
	hosts := []string{"c1", "c2", "c3", "c4", "c5", "c6"}
	e := newEnv(t, hosts...)
	e.eng.OnPause(e.s.OnJobPaused)
	for _, h := range hosts {
		e.setUnit(h, "nginx.service", "inactive", "enabled")
	}
	e.reportAll()
	b := e.baseline("Server Defaults", []Resource{svc("nginx", StateRunning, nil)}, everywhere)
	// The canary host will fail to start it (set before any pass runs).
	e.f.do(func() { e.f.hosts["h_c1"].failService["nginx.service"] = "boom" })
	if _, err := e.s.SetAuto(e.ctx, b.ID, true, nil, admin); err != nil {
		t.Fatal(err)
	}
	e.step()
	e.s.AutoRestoreNow(e.ctx)
	var jobID, status string
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		e.st.Reader.QueryRow(`SELECT id, status FROM jobs`).Scan(&jobID, &status)
		if status == engine.JobPaused {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if status != engine.JobPaused {
		t.Fatalf("job %s is %s", jobID, status)
	}
	// One job for the whole fleet; the canary failed before anyone else started.
	if n := e.jobCount(); n != 1 {
		t.Fatal(n)
	}
	for _, h := range hosts[1:] {
		if len(e.exec.ops("h_"+h)) != 0 {
			t.Fatalf("%s ran before the canary passed", h)
		}
	}
	// The engine tells the pause hook after it records the pause.
	var v *BaselineView
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if v, _ = e.s.Baseline(e.ctx, b.ID); v.Auto.Attention != "" {
			break
		}
	}
	if !strings.Contains(v.Auto.Attention, "the automatic restore paused its rollout: Canary c1 failed") {
		t.Fatalf("%+v", v.Auto)
	}
	// While paused (and needing attention) nothing else is started.
	e.s.AutoRestoreNow(e.ctx)
	if n := e.jobCount(); n != 1 {
		t.Fatal(n)
	}
	// The operator cancels; the drift stays visible, never claimed restored.
	if err := e.eng.Cancel(e.ctx, jobID); err != nil {
		t.Fatal(err)
	}
	e.waitJobs()
	for _, h := range hosts {
		if st := e.status(h, "service:nginx.service"); st != StatusFailed {
			t.Fatalf("%s: %s", h, st)
		}
	}
	if r := e.resource("c4", "service:nginx.service"); !strings.Contains(r.Note, "cancelled before this correction ran") {
		t.Fatal(r.Note)
	}
}

func TestRestartSettlesInterruptedCorrection(t *testing.T) {
	e := newEnv(t, "a1", "a2")
	for _, h := range []string{"a1", "a2"} {
		e.setUnit(h, "nginx.service", "inactive", "enabled")
	}
	e.reportAll()
	e.baseline("Server Defaults", []Resource{svc("nginx", StateRunning, nil)}, everywhere)
	e.step()
	hold := make(chan struct{})
	e.exec.mu.Lock()
	e.exec.hold = hold
	e.exec.mu.Unlock()
	if _, err := e.s.Restore(e.ctx, Selection{}, RestoreOptions{Strategy: &model.Strategy{Concurrency: 5}}, "", operator); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	// The controller restarts: a new engine marks the job interrupted; a
	// new desired-state service settles it without running anything again.
	logs := logging.Discard()
	eng2 := engine.New(e.st, nopResolver{}, e.exec, e.aw, logs.App, engine.Config{RetryDelay: time.Millisecond, RetryAttempts: 1})
	if err := eng2.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	old := e.eng
	e.eng = eng2
	s2 := e.newService()
	if err := s2.SettleNow(e.ctx); err != nil {
		t.Fatal(err)
	}
	for _, h := range []string{"a1", "a2"} {
		r := e.resource(h, "service:nginx.service")
		_ = r
		v, _, _ := s2.Resource(e.ctx, "h_"+h, "service:nginx.service")
		if v.Status != StatusFailed || !strings.Contains(v.Note, "not re-run automatically") {
			t.Fatalf("%s: %s %q", h, v.Status, v.Note)
		}
	}
	if n := e.jobCount(); n != 1 {
		t.Fatalf("%d jobs: an interrupted correction was re-run", n)
	}
	close(hold)
	old.Wait()
	// Restoring again is an explicit new request.
	e.exec.mu.Lock()
	e.exec.hold = nil
	e.exec.mu.Unlock()
	e.s = s2
	if _, err := s2.Restore(e.ctx, Selection{}, RestoreOptions{}, "", operator); err != nil {
		t.Fatal(err)
	}
	e.waitJobs()
	if st := e.status("a1", "service:nginx.service"); st != StatusCompliant {
		t.Fatal(st)
	}
}

func TestBroadChangeIsOneControlledJob(t *testing.T) {
	var hosts []string
	for i := 1; i <= 200; i++ {
		hosts = append(hosts, fmt.Sprintf("n%03d", i))
	}
	e := newEnv(t, hosts...)
	b := e.baseline("Server Defaults", []Resource{pkgR("curl", StateInstalled)}, everywhere)
	e.step()
	start := time.Now()
	e.checkAll()
	checkTook := time.Since(start)
	// A baseline change makes all 200 hosts drift at once.
	if _, _, err := e.s.UpdateBaseline(e.ctx, b.ID, BaselineInput{Name: "Server Defaults", Assignments: []Assignment{everywhere},
		Resources: []Resource{pkgR("curl", StateInstalled), pkgR("htop", StateInstalled), pkgR("telnet", StateAbsent)}}, admin); err != nil {
		t.Fatal(err)
	}
	for _, h := range hosts[:50] {
		fh := e.f.host("h_" + h)
		e.f.do(func() { fh.packages["telnet"] = "1" })
	}
	e.step()
	e.checkAll()
	sum, _ := e.s.Summary(e.ctx)
	if sum.Hosts.Drift != 200 || sum.Resources.Drift != 450 {
		t.Fatalf("summary %+v", sum)
	}
	if _, err := e.s.SetAuto(e.ctx, b.ID, true, nil, admin); err != nil {
		t.Fatal(err)
	}
	start = time.Now()
	e.s.AutoRestoreNow(e.ctx)
	dispatch := time.Since(start)
	if n := e.jobCount(); n != 1 {
		t.Fatalf("%d jobs for one broad change", n)
	}
	var hostsInJob, waves int
	e.st.Reader.QueryRow(`SELECT count(*), max(wave) + 1 FROM job_hosts`).Scan(&hostsInJob, &waves)
	if hostsInJob != 200 || waves != 41 { // one canary, then batches of five
		t.Fatalf("%d hosts in %d waves", hostsInJob, waves)
	}
	e.waitJobs()
	sum, _ = e.s.Summary(e.ctx)
	if sum.Hosts.Compliant != 200 {
		t.Fatalf("after: %+v", sum.Hosts)
	}
	t.Logf("200 hosts: checking all %s; planning and creating the one job %s", checkTook, dispatch)
}
