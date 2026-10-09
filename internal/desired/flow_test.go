package desired

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestServiceDriftRestoreAndProof(t *testing.T) {
	e := newEnv(t, "web-1", "web-2")
	e.setUnit("web-1", "jellyfin.service", "active", "enabled")
	e.setUnit("web-2", "jellyfin.service", "failed", "disabled")
	e.reportAll()
	b := e.baseline("Media Server", []Resource{svc("jellyfin", StateRunning, on())}, everywhere)
	e.step()
	const key = "service:jellyfin.service"
	if st := e.status("web-1", key); st != StatusCompliant {
		t.Fatalf("web-1: %s", st)
	}
	r := e.resource("web-2", key)
	if r.Status != StatusDrift || DiffText(r.Diffs) != "state: running → failed; enabled: enabled at boot → disabled at boot" {
		t.Fatalf("web-2: %s %q", r.Status, DiffText(r.Diffs))
	}
	if r.Current != "failed, disabled at boot" || r.Want != "running, enabled at boot" || !r.Restorable {
		t.Fatalf("view: %+v", r)
	}
	if r.Source.Label != "Required by Global baseline Media Server" || r.Source.Baselines[0].Revision != 1 {
		t.Fatalf("source: %+v", r.Source)
	}
	if k := kinds(e.history("web-2", key)); k != "drift_detected" {
		t.Fatalf("history %s", k)
	}
	// An unchanged report writes nothing new.
	e.reportServices("h_web-2")
	e.step()
	if k := kinds(e.history("web-2", key)); k != "drift_detected" {
		t.Fatalf("history after an unchanged report: %s", k)
	}

	// Restore: preview, then submit exactly what was previewed.
	p, err := e.s.PreviewRestore(e.ctx, Selection{BaselineID: b.ID}, RestoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Items) != 1 || p.Items[0].HostName != "web-2" || len(p.Jobs) != 1 || len(p.Jobs[0].Steps) != 1 {
		t.Fatalf("plan %+v", p)
	}
	if got := p.Jobs[0].StepText[0]; got != "start + enable jellyfin.service" {
		t.Fatalf("step %q", got)
	}
	res, err := e.s.Restore(e.ctx, Selection{BaselineID: b.ID}, RestoreOptions{}, p.Hash, operator)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Jobs) != 1 {
		t.Fatalf("jobs %v", res.Jobs)
	}
	if st := e.status("web-2", key); st != StatusReconciling {
		t.Fatalf("while the job runs: %s", st)
	}
	// A second request for the same drift creates nothing.
	if _, err := e.s.Restore(e.ctx, Selection{BaselineID: b.ID}, RestoreOptions{}, "", operator); !errors.Is(err, ErrNothingToRestore) {
		t.Fatalf("second restore: %v", err)
	}
	e.waitJobs()
	r = e.resource("web-2", key)
	if r.Status != StatusCompliant || r.LastJobID != res.Jobs[0] {
		t.Fatalf("after: %+v", r)
	}
	h := e.history("web-2", key)
	if k := kinds(h); k != "drift_detected,restore_requested,restored(restored)" {
		t.Fatalf("history %s", k)
	}
	if h[0].JobID != res.Jobs[0] || h[0].Actor != "olga" || h[0].Baseline == nil || h[0].Baseline.Revision != 1 {
		t.Fatalf("restored entry %+v", h[0])
	}
	if ops := e.exec.ops("h_web-1"); len(ops) != 0 {
		t.Fatalf("a compliant host was touched: %v", ops)
	}
}

func TestRestoreNeedsProofNotExitCode(t *testing.T) {
	e := newEnv(t, "web-1")
	e.setUnit("web-1", "app.service", "inactive", "enabled")
	e.reportAll()
	e.baseline("App", []Resource{svc("app", StateRunning, nil)}, everywhere)
	e.step()
	// The start "succeeds" but the unit doesn't stay up.
	e.f.host("h_web-1").ignoreStart["app.service"] = true
	if _, err := e.s.Restore(e.ctx, Selection{}, RestoreOptions{}, "", operator); err != nil {
		t.Fatal(err)
	}
	e.waitJobs()
	r := e.resource("web-1", "service:app.service")
	if r.Status != StatusFailed || !strings.Contains(r.Note, "the correction ran, but the host still differs: state: running → stopped") {
		t.Fatalf("got %s: %q", r.Status, r.Note)
	}
	if k := kinds(e.history("web-1", "service:app.service")); k != "drift_detected,restore_requested,restore_failed" {
		t.Fatalf("history %s", k)
	}
	// The failed correction stays visible as such until something changes.
	e.reportAll()
	e.step()
	if st := e.status("web-1", "service:app.service"); st != StatusFailed {
		t.Fatalf("later: %s", st)
	}
	// Someone fixes it by hand: compliant, and history says the host changed.
	e.setUnit("web-1", "app.service", "active", "enabled")
	e.reportAll()
	e.step()
	h := e.history("web-1", "service:app.service")
	if h[0].Kind != "compliant" || h[0].Resolution != "observed" {
		t.Fatalf("history %s", kinds(h))
	}
}

func TestUnreachableAndStaleNeverCompliant(t *testing.T) {
	e := newEnv(t, "web-1")
	e.setUnit("web-1", "ssh.service", "active", "enabled")
	e.reportAll()
	e.f.do(func() { e.f.hosts["h_web-1"].packages["curl"] = "8.0" })
	e.baseline("Base", []Resource{svc("ssh", StateRunning, nil), pkgR("curl", StateInstalled)}, everywhere)
	e.step()
	e.checkAll()
	if st := e.status("web-1", "package:curl"); st != StatusCompliant {
		t.Fatalf("curl %s", st)
	}
	// The host goes away. Checks fail; the last observation is kept until
	// it is too old to trust, then the state is unknown — never compliant.
	e.f.host("h_web-1").unreachable = true
	e.checkAll()
	if st := e.status("web-1", "package:curl"); st != StatusCompliant {
		t.Fatalf("right after a failed check: %s", st)
	}
	e.clk.add(40 * time.Minute) // past the 35-minute window (15 min checks)
	e.s.staleSweep(e.ctx)
	e.s.evaluateDirty(e.ctx)
	r := e.resource("web-1", "package:curl")
	if r.Status != StatusUnknown || !strings.Contains(r.Note, "is unreachable") {
		t.Fatalf("curl after 40 min: %s %q", r.Status, r.Note)
	}
	if st := e.status("web-1", "service:ssh.service"); st != StatusUnknown {
		t.Fatalf("ssh after 40 min without service data: %s", st)
	}
	h := e.history("web-1", "")
	if len(h) == 0 || h[0].Kind != "unknown" {
		t.Fatalf("host history: %s", kinds(h))
	}
	// A brand-new resource on an unreachable host is unknown too.
	e.baseline("More", []Resource{pkgR("htop", StateAbsent)}, everywhere)
	e.step()
	if st := e.status("web-1", "package:htop"); st != StatusUnknown {
		t.Fatalf("htop: %s", st)
	}
	// Back: everything is observed again; the host's history says so once.
	e.f.host("h_web-1").unreachable = false
	e.reportAll()
	e.checkAll()
	for _, k := range []string{"package:curl", "package:htop", "service:ssh.service"} {
		if st := e.status("web-1", k); st != StatusCompliant {
			t.Fatalf("%s back: %s", k, st)
		}
	}
	if h := e.history("web-1", ""); h[0].Kind != "observed" {
		t.Fatalf("host history: %s", kinds(h))
	}
	// Resource-level: unknown never turned into a resource transition.
	if k := kinds(e.history("web-1", "package:curl")); k != "" {
		t.Fatalf("curl history: %s", k)
	}
}

func TestMonitoringOffMakesServicesUnknown(t *testing.T) {
	e := newEnv(t, "web-1")
	e.setUnit("web-1", "ssh.service", "active", "enabled")
	e.reportAll()
	e.baseline("Base", []Resource{svc("ssh", StateRunning, nil)}, everywhere)
	e.step()
	if st := e.status("web-1", "service:ssh.service"); st != StatusCompliant {
		t.Fatal(st)
	}
	if _, err := e.st.Writer.Exec(`INSERT INTO settings (key, value_json, updated_at) VALUES ('monitoring', '{"enabled":false}', 0)
		ON CONFLICT(key) DO UPDATE SET value_json = excluded.value_json`); err != nil {
		t.Fatal(err)
	}
	e.s.mons.mu.Lock()
	e.s.mons.at = time.Time{}
	e.s.mons.mu.Unlock()
	e.s.markDirty("h_web-1")
	e.s.evaluateDirty(e.ctx)
	r := e.resource("web-1", "service:ssh.service")
	if r.Status != StatusUnknown || !strings.Contains(r.Note, "monitoring is turned off") {
		t.Fatalf("%s %q", r.Status, r.Note)
	}
}
