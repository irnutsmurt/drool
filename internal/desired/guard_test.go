package desired

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"drool/internal/model"
	"drool/internal/monitor/services"
)

// Restores refuse hosts DROOL must not change, saying why: an agent from
// another controller epoch, a blocked management path, managed-file state
// ahead of this controller, an agent ceiling that doesn't allow the
// correction. Nothing runs on them; the other hosts are still corrected.
func TestRestoreRefusesBlockedHosts(t *testing.T) {
	hosts := []string{"ahead", "ceiling", "epoch", "fine", "quarantined"}
	e := newEnv(t, hosts...)
	for _, h := range hosts {
		e.setUnit(h, "app.service", "inactive", "enabled")
	}
	e.reportAll()
	sha := e.upload("app.conf", "workers = 4\n")
	e.baseline("App", []Resource{svc("app", StateRunning, nil), {Kind: KindFile, Path: "/etc/app.conf", Artifact: sha}}, everywhere)
	e.setPath("epoch", PathInfo{AgentState: "epoch_mismatch"})
	e.setPath("quarantined", PathInfo{Blocked: true, Reason: "its agent identity is quarantined (a possible clone)"})
	e.sql(`INSERT INTO state_versions (host_id, domain, epoch, revision, updated_at, status) VALUES ('h_ahead', 'files', 'ep1', 3, 0, 'host_ahead')`)
	// An agent host whose ceiling allows monitoring only.
	e.sql(`UPDATE hosts SET management_method = 'agent' WHERE id = 'h_ceiling'`)
	e.sql(`INSERT INTO agent_identities (id, host_id, public_key, fingerprint, status, enrolled_at, effective_json)
		VALUES ('agt_1', 'h_ceiling', x'01', 'fp', 'active', 0, '{"monitoring":true}')`)
	e.step()
	e.checkAll()

	p, err := e.s.PreviewRestore(e.ctx, Selection{}, RestoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var items []string
	for _, it := range p.Items {
		items = append(items, it.HostName+" "+it.Key)
	}
	if got := strings.Join(items, ", "); got != "ahead service:app.service, fine file:/etc/app.conf, fine service:app.service" {
		t.Fatalf("items: %s", got)
	}
	reasons := map[string]string{}
	for _, x := range p.Excluded {
		reasons[x.HostName+" "+x.Key] = x.Reason
	}
	for k, want := range map[string]string{
		"epoch service:app.service":       "belongs to another controller epoch",
		"epoch file:/etc/app.conf":        "belongs to another controller epoch",
		"quarantined service:app.service": "DROOL can't change this host: its agent identity is quarantined",
		"ahead file:/etc/app.conf":        "managed-file state is ahead of this controller (host_ahead)",
		"ceiling service:app.service":     "capability ceiling doesn't allow this: the agent's capability ceiling does not allow service management",
		"ceiling file:/etc/app.conf":      "capability ceiling doesn't allow this",
	} {
		if !strings.Contains(reasons[k], want) {
			t.Errorf("%s: %q, want %q", k, reasons[k], want)
		}
	}
	res, err := e.s.Restore(e.ctx, Selection{}, RestoreOptions{}, p.Hash, operator)
	if err != nil || len(res.Jobs) != 1 {
		t.Fatalf("%v %v", res, err)
	}
	e.waitJobs()
	for _, h := range []string{"epoch", "quarantined", "ceiling"} {
		if ops := e.exec.ops("h_" + h); len(ops) != 0 {
			t.Fatalf("%s was changed: %v", h, ops)
		}
	}
	if ops := e.exec.ops("h_ahead"); len(ops) != 1 || !strings.Contains(ops[0], "app.service") {
		t.Fatalf("ahead: %v", ops)
	}
	if st := e.status("ahead", "file:/etc/app.conf"); st != StatusDrift {
		t.Fatalf("the file on the host that is ahead: %s", st)
	}
	if st := e.status("fine", "file:/etc/app.conf"); st != StatusCompliant {
		t.Fatalf("fine: %s", st)
	}
}

// Executing re-plans from what DROOL knows then: if anything changed
// since the preview, nothing is submitted and the operator reviews again.
func TestRestoreRefusesAChangedPlan(t *testing.T) {
	e := newEnv(t, "web-1", "web-2")
	e.setUnit("web-1", "app.service", "inactive", "enabled")
	e.setUnit("web-2", "app.service", "inactive", "enabled")
	e.reportAll()
	e.baseline("App", []Resource{svc("app", StateRunning, nil)}, everywhere)
	e.step()
	p, err := e.s.PreviewRestore(e.ctx, Selection{}, RestoreOptions{})
	if err != nil || len(p.Items) != 2 {
		t.Fatalf("%+v %v", p, err)
	}
	// Someone starts it on web-2 by hand in the meantime.
	e.setUnit("web-2", "app.service", "active", "enabled")
	e.reportServices("h_web-2")
	e.step()
	if _, err := e.s.Restore(e.ctx, Selection{}, RestoreOptions{}, p.Hash, operator); !errors.Is(err, ErrPlanChanged) {
		t.Fatalf("got %v", err)
	}
	// Other options are another plan too (the operator saw a different one).
	p2, err := e.s.PreviewRestore(e.ctx, Selection{}, RestoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.Restore(e.ctx, Selection{}, RestoreOptions{AllowRemovals: true}, p2.Hash, operator); !errors.Is(err, ErrPlanChanged) {
		t.Fatalf("other options: %v", err)
	}
	if n := e.jobCount(); n != 0 {
		t.Fatalf("%d jobs", n)
	}
	if _, err := e.s.Restore(e.ctx, Selection{}, RestoreOptions{}, p2.Hash, operator); err != nil {
		t.Fatal(err)
	}
	e.waitJobs()
	if ops := e.exec.ops("h_web-2"); len(ops) != 0 {
		t.Fatalf("web-2 was touched: %v", ops)
	}
}

// A correction is one job until it would exceed the step limit; then
// hosts go to further jobs whole (a host's steps are never split). A
// single host needing more than the limit is refused with advice.
func TestCorrectionsSplitPastTheStepLimit(t *testing.T) {
	e := newEnv(t, "a-host", "b-host", "c-host", "big")
	e.reportAll()
	for _, h := range []string{"a-host", "b-host", "c-host"} {
		var rs []Resource
		for i := range 20 {
			rs = append(rs, pkgR(fmt.Sprintf("tool-%s-%02d", h[:1], i), StateInstalled))
		}
		e.baseline("Tools "+h, rs, onHost(h))
	}
	var big []Resource
	for i := range model.MaxCorrectiveSteps + 1 {
		big = append(big, pkgR(fmt.Sprintf("lib-%02d", i), StateInstalled))
	}
	e.baseline("Big", big, onHost("big"))
	e.step()
	e.checkAll()

	sel := Selection{HostIDs: []string{"h_a-host", "h_b-host", "h_c-host"}}
	p, err := e.s.PreviewRestore(e.ctx, sel, RestoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Items) != 60 || len(p.Jobs) != 2 {
		t.Fatalf("%d items, %d jobs", len(p.Items), len(p.Jobs))
	}
	j0, j1 := p.Jobs[0], p.Jobs[1]
	if len(j0.Steps) != 40 || len(j0.Hosts) != 2 || len(j1.Steps) != 20 || len(j1.Hosts) != 1 || j1.Hosts[0].HostName != "c-host" {
		t.Fatalf("jobs: %d steps on %d hosts, %d steps on %d hosts", len(j0.Steps), len(j0.Hosts), len(j1.Steps), len(j1.Hosts))
	}
	if !strings.HasSuffix(j0.Name, "(part 1 of 2)") || !strings.HasSuffix(j1.Name, "(part 2 of 2)") {
		t.Fatalf("names %q %q", j0.Name, j1.Name)
	}
	for _, h := range j0.Hosts {
		if len(h.Steps) != 20 {
			t.Fatalf("%s runs %d steps", h.HostName, len(h.Steps))
		}
	}
	res, err := e.s.Restore(e.ctx, sel, RestoreOptions{}, p.Hash, operator)
	if err != nil || len(res.Jobs) != 2 {
		t.Fatalf("%v %v", res, err)
	}
	e.waitJobs()
	if v, _ := e.s.Host(e.ctx, "h_c-host"); v.Status != StatusCompliant {
		t.Fatalf("c-host: %s", v.Status)
	}

	_, err = e.s.PreviewRestore(e.ctx, Selection{HostIDs: []string{"h_big"}}, RestoreOptions{})
	var ve *ValidationError
	if !errors.As(err, &ve) || !strings.Contains(ve.Msg, fmt.Sprintf("big needs %d corrections; at most %d fit", model.MaxCorrectiveSteps+1, model.MaxCorrectiveSteps)) {
		t.Fatalf("got %v", err)
	}
}

// History keeps 180 days and at most 100 entries per host and resource;
// finished corrections go with it.
func TestHistoryIsBounded(t *testing.T) {
	e := newEnv(t, "web-1")
	now := e.clk.now().UnixMilli()
	old := e.clk.now().Add(-181 * 24 * time.Hour).UnixMilli()
	for i := range 130 {
		e.sql(`INSERT INTO ds_history (ts, host_id, host_name, key, kind) VALUES (?, 'h_web-1', 'web-1', 'package:curl', 'drift_detected')`, now-int64(130-i))
	}
	for range 5 {
		e.sql(`INSERT INTO ds_history (ts, host_id, host_name, key, kind) VALUES (?, 'h_web-1', 'web-1', 'package:htop', 'drift_detected')`, old)
	}
	e.sql(`INSERT INTO ds_reconciles (id, kind, job_id, status, items_json, created_at, finished_at) VALUES ('rec_old', 'manual', 'job_x', 'done', '[]', ?, ?)`, old, old)
	e.sql(`INSERT INTO ds_reconciles (id, kind, job_id, status, items_json, created_at, finished_at) VALUES ('rec_new', 'manual', 'job_y', 'done', '[]', ?, ?)`, now, now)
	e.s.prune(e.ctx)
	count := func(q string) (n int) {
		e.st.Reader.QueryRow(q).Scan(&n)
		return n
	}
	if n := count(`SELECT count(*) FROM ds_history WHERE key = 'package:curl'`); n != 100 {
		t.Fatalf("curl: %d", n)
	}
	if n := count(`SELECT min(ts) FROM ds_history WHERE key = 'package:curl'`); int64(n) != now-100 {
		t.Fatal("the newest entries are kept")
	}
	if n := count(`SELECT count(*) FROM ds_history WHERE key = 'package:htop'`); n != 0 {
		t.Fatalf("htop: %d", n)
	}
	if n := count(`SELECT count(*) FROM ds_reconciles`); n != 1 {
		t.Fatalf("corrections: %d", n)
	}
}

// Accept current state for a package and for a missing file: the package
// becomes this host's own declaration; the missing file becomes "Not
// managed on this host". The baseline is untouched; resetting the host's
// declaration brings the baseline's value (and the drift) back.
func TestAcceptPackageAndMissingFile(t *testing.T) {
	e := newEnv(t, "web-1", "web-2")
	e.reportAll()
	e.f.do(func() {
		e.f.hosts["h_web-1"].packages["htop"] = "3.2.2-2"
		e.f.hosts["h_web-2"].packages["htop"] = "3.2.2-2"
	})
	sha := e.upload("motd", "Managed by DROOL\n")
	b := e.baseline("Base", []Resource{pkgR("htop", StateAbsent), {Kind: KindFile, Path: "/etc/motd", Artifact: sha}}, everywhere)
	e.step()
	e.checkAll()
	if st := e.status("web-1", "package:htop"); st != StatusDrift {
		t.Fatal(st)
	}
	if r := e.resource("web-1", "file:/etc/motd"); r.Status != StatusDrift || r.Current != "missing" {
		t.Fatalf("%s %q", r.Status, r.Current)
	}
	acc, err := e.s.Accept(e.ctx, "h_web-1", []string{"package:htop", "file:/etc/motd"}, "web-1 is a workstation", admin)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, a := range acc.Accepted {
		got[a.Key] = a.Now
	}
	if got["package:htop"] != "installed" || got["file:/etc/motd"] != "not managed on this host" {
		t.Fatalf("%v", got)
	}
	e.step()
	r := e.resource("web-1", "package:htop")
	if r.Status != StatusCompliant || r.Source.Level != LevelHost || r.Source.Label != "Custom for this host" {
		t.Fatalf("htop after accept: %s %+v", r.Status, r.Source)
	}
	if h := e.history("web-1", "package:htop"); h[0].Kind != "accepted" || h[0].Resolution != "configuration" {
		t.Fatalf("history %s", kinds(h))
	}
	v, err := e.s.Host(e.ctx, "h_web-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Excluded) != 1 || v.Excluded[0].Key != "file:/etc/motd" || v.Excluded[0].Source.Label != "Not managed on this host" {
		t.Fatalf("excluded %+v", v.Excluded)
	}
	for _, x := range v.Resources {
		if x.Key == "file:/etc/motd" {
			t.Fatal("an excluded file is still managed")
		}
	}
	// Other hosts and the baseline are unchanged.
	if st := e.status("web-2", "package:htop"); st != StatusDrift {
		t.Fatalf("web-2: %s", st)
	}
	if bv, _ := e.s.Baseline(e.ctx, b.ID); bv.Revision != 1 {
		t.Fatal("accepting changed the baseline")
	}
	// Reset: inherit again.
	if err := e.s.ResetOverride(e.ctx, "h_web-1", "package:htop"); err != nil {
		t.Fatal(err)
	}
	e.step()
	e.checkAll()
	if r := e.resource("web-1", "package:htop"); r.Status != StatusDrift || r.Source.Level != LevelGlobal {
		t.Fatalf("after reset: %s %+v", r.Status, r.Source)
	}
}

// Events are aggregated: a few hosts are named (with up to three
// resources each), many hosts become one event; unchanged rechecks and
// drift that merely changes add nothing.
func TestEventsAreAggregated(t *testing.T) {
	hosts := []string{"n1", "n2", "n3", "n4", "n5", "n6"}
	e := newEnv(t, hosts...)
	for _, h := range hosts {
		e.setUnit(h, "a.service", "active", "enabled")
		e.setUnit(h, "b.service", "active", "enabled")
	}
	e.reportAll()
	b := e.baseline("Few", []Resource{svc("a", StateRunning, nil), svc("b", StateRunning, nil)}, everywhere)
	e.step()
	e.s.flushEvents(e.ctx)
	if ev := e.events("desired.drift"); len(ev) != 0 {
		t.Fatalf("compliant: %v", ev)
	}
	// Two hosts drift on both services: one event per host, naming them.
	for _, h := range []string{"n1", "n2"} {
		e.setUnit(h, "a.service", "inactive", "enabled")
		e.setUnit(h, "b.service", "failed", "enabled")
	}
	e.reportAll()
	e.step()
	e.s.flushEvents(e.ctx)
	ev := e.events("desired.drift")
	if len(ev) != 2 || ev[0] != "Desired state: drift detected on n1 (a.service, b.service)" {
		t.Fatalf("%q", ev)
	}
	// Rechecks and a drift that changes (failed → inactive) add nothing.
	e.setUnit("n1", "b.service", "inactive", "enabled")
	e.reportAll()
	e.step()
	e.s.flushEvents(e.ctx)
	if ev := e.events("desired.drift"); len(ev) != 2 {
		t.Fatalf("%q", ev)
	}
	// A change that makes every host drift: one event.
	if _, _, err := e.s.UpdateBaseline(e.ctx, b.ID, BaselineInput{Name: "Few", Assignments: []Assignment{everywhere},
		Resources: []Resource{svc("a", StateRunning, nil), svc("b", StateRunning, nil), svc("c", StateRunning, nil)}}, admin); err != nil {
		t.Fatal(err)
	}
	e.reportAll()
	e.step()
	e.s.flushEvents(e.ctx)
	ev = e.events("desired.drift")
	if len(ev) != 3 || ev[2] != "Desired state: drift detected on 6 hosts (n1, n2, n3, …)" {
		t.Fatalf("%q", ev)
	}
	// Conflicts likewise.
	e.group("g1", "lab", hosts...)
	e.group("g2", "media", hosts...)
	e.baseline("Lab", []Resource{svc("d", StateRunning, nil)}, inGroup("g1"))
	e.baseline("Media", []Resource{svc("d", StateStopped, nil)}, inGroup("g2"))
	e.step()
	e.s.flushEvents(e.ctx)
	if ev := e.events("desired.conflict"); len(ev) != 1 || ev[0] != "Desired state: conflict on 6 hosts (n1, n2, n3, …)" {
		t.Fatalf("%q", ev)
	}
}

// An agent sends its service list when something changes and at least
// every 30 minutes; while it is connected, an unchanged list is current.
// Once it disconnects, what it last said counts only for a short grace.
func TestAgentServiceDataFreshness(t *testing.T) {
	e := newEnv(t, "nas")
	e.setUnit("nas", "smbd.service", "active", "enabled")
	e.sql(`UPDATE hosts SET management_method = 'agent' WHERE id = 'h_nas'`)
	e.sql(`INSERT INTO agent_identities (id, host_id, public_key, fingerprint, status, enrolled_at, connected, last_seen_at)
		VALUES ('agt_nas', 'h_nas', x'02', 'fp', 'active', 0, 1, ?)`, e.clk.now().UnixMilli())
	report := func() {
		h := e.f.host("h_nas")
		var rep services.Report
		e.f.do(func() {
			rep = services.Report{Supported: true}
			for n, u := range h.units {
				rep.Units = append(rep.Units, services.Unit{Name: n, Load: "loaded", Active: u.active, Sub: u.sub, FileState: u.fileState})
			}
		})
		if err := e.mon.IngestServices(e.ctx, "h_nas", &rep, false); err != nil {
			t.Fatal(err)
		}
	}
	report()
	e.baseline("NAS", []Resource{svc("smbd", StateRunning, on())}, everywhere)
	e.step()
	const key = "service:smbd.service"
	sweep := func(d time.Duration) ResourceView {
		e.clk.add(d)
		e.s.staleSweep(e.ctx)
		e.s.evaluateDirty(e.ctx)
		return e.resource("nas", key)
	}
	if r := sweep(0); r.Status != StatusCompliant {
		t.Fatalf("%s %q", r.Status, r.Note)
	}
	// Twenty quiet minutes, the agent connected: still current.
	if r := sweep(20 * time.Minute); r.Status != StatusCompliant {
		t.Fatalf("connected, unchanged list: %s %q", r.Status, r.Note)
	}
	// The agent disconnects: a short grace, then unknown with the reason.
	e.sql(`UPDATE agent_identities SET connected = 0, last_seen_at = ? WHERE id = 'agt_nas'`, e.clk.now().UnixMilli())
	if r := sweep(2 * time.Minute); r.Status != StatusCompliant {
		t.Fatalf("just disconnected: %s %q", r.Status, r.Note)
	}
	if r := sweep(4 * time.Minute); r.Status != StatusUnknown || !strings.Contains(r.Note, "the agent is not connected (last seen") {
		t.Fatalf("disconnected: %s %q", r.Status, r.Note)
	}
	// Back, with a fresh list.
	e.sql(`UPDATE agent_identities SET connected = 1, last_seen_at = ? WHERE id = 'agt_nas'`, e.clk.now().UnixMilli())
	report()
	if r := sweep(0); r.Status != StatusCompliant {
		t.Fatalf("reconnected: %s %q", r.Status, r.Note)
	}
	// Connected but silent past the agent's 30-minute resend: unknown.
	if r := sweep(36 * time.Minute); r.Status != StatusUnknown || !strings.Contains(r.Note, "no fresh service data since") {
		t.Fatalf("silent agent: %s %q", r.Status, r.Note)
	}
}

// Listed for one baseline, a host shows that baseline's state only: drift
// another baseline decides isn't this baseline's.
func TestHostsListedForABaseline(t *testing.T) {
	e := newEnv(t, "web-1")
	e.reportAll()
	e.f.do(func() { e.f.hosts["h_web-1"].packages["sudo"] = "1.9" })
	a := e.baseline("Tools", []Resource{pkgR("curl", StateInstalled)}, everywhere)
	b := e.baseline("Base", []Resource{pkgR("sudo", StateInstalled)}, everywhere)
	e.step()
	e.checkAll()
	rows, err := e.s.Hosts(e.ctx, HostFilter{BaselineID: b.ID})
	if err != nil || len(rows) != 1 {
		t.Fatalf("%v %v", rows, err)
	}
	if r := rows[0]; r.Status != StatusCompliant || len(r.Problems) != 0 || r.Counts.Total != 1 || r.Counts.Compliant != 1 {
		t.Fatalf("for Base: %+v", r)
	}
	rows, _ = e.s.Hosts(e.ctx, HostFilter{BaselineID: a.ID})
	if r := rows[0]; r.Status != StatusDrift || len(r.Problems) != 1 || r.Problems[0] != "package curl (drift)" || r.Counts.Total != 1 {
		t.Fatalf("for Tools: %+v", r)
	}
	// Unfiltered: the host as a whole.
	rows, _ = e.s.Hosts(e.ctx, HostFilter{})
	if r := rows[0]; r.Status != StatusDrift || r.Counts.Total != 2 {
		t.Fatalf("all: %+v", r)
	}
}

// A priority change that resolves a conflict is shown as resolved (and as
// drift when the winning value differs), even on a host whose status is
// dominated by something else (here a failed restore).
func TestPriorityImpactSeesConflictsBehindOtherProblems(t *testing.T) {
	e := newEnv(t, "app")
	e.setUnit("app", "worker.service", "inactive", "enabled")
	e.reportAll()
	e.group("g_web", "web", "app")
	e.group("g_lab", "lab", "app")
	e.baseline("Worker", []Resource{svc("worker", StateRunning, nil)}, onHost("app"))
	e.baseline("Web tools", []Resource{pkgR("nano", StateInstalled)}, inGroup("g_web"))
	e.baseline("Lab minimal", []Resource{pkgR("nano", StateAbsent)}, inGroup("g_lab"))
	e.step()
	e.checkAll()
	e.f.do(func() { e.f.hosts["h_app"].failService["worker.service"] = "Job for worker.service failed" })
	if _, err := e.s.Restore(e.ctx, Selection{Keys: []string{"service:worker.service"}}, RestoreOptions{}, "", operator); err != nil {
		t.Fatal(err)
	}
	e.waitJobs()
	if v, _ := e.s.Host(e.ctx, "h_app"); v.Status != StatusFailed {
		t.Fatalf("host %s", v.Status)
	}
	imp, err := e.s.PreviewPriority(e.ctx, "g_web", 10)
	if err != nil {
		t.Fatal(err)
	}
	if imp.HostsAffected != 1 || imp.Counts.ResolvedConflicts != 1 || imp.Counts.BecomeDrifted != 1 || imp.Counts.Unchanged != 0 {
		t.Fatalf("%+v", imp.Counts)
	}
}
