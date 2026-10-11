package api

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"drool/internal/auth"
	"drool/internal/desired"
	"drool/internal/monitor/services"
	"drool/internal/ops/pkg"
	"drool/internal/sshx/sshtest"
)

// Desired state through the API, against a real SSH host (sshtest
// running commands in a temporary directory): files are hashed and
// deployed for real; services come from monitoring's ingestion; the host
// has neither apt nor dnf (whatever the machine running the test has), and
// that unsupported package manager is itself tested.

type desiredAPI struct {
	*schedAPI
	auditor *client
	dir     string
	file    string
	sha     string
}

func newDesiredAPI(t *testing.T) *desiredAPI {
	t.Helper()
	e := &desiredAPI{schedAPI: newSchedAPI(t)}
	e.srvA.SetExec(func(cmd string, stdin []byte) (string, string, int) {
		if strings.Contains(cmd, pkg.DetectCommand) {
			return "none\n", "", 0
		}
		return sshtest.ExecLocal(cmd, stdin)
	})
	e.h.mkUser("aud", auth.RoleAuditor)
	e.auditor = e.h.login("aud")
	e.dir = t.TempDir()
	e.file = filepath.Join(e.dir, "app.conf")
	if err := os.WriteFile(e.file, []byte("key = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var art struct {
		SHA256 string `json:"sha256"`
	}
	e.admin.raw("POST", "/api/v1/artifacts?name=app.conf", strings.NewReader("key = 2\n")).json(t, &art)
	e.sha = art.SHA256
	units := []services.Unit{{Name: "app.service", Load: "loaded", Active: "inactive", Sub: "dead", FileState: "enabled"}}
	if err := e.h.mon.IngestServices(context.Background(), e.hostA, &services.Report{Supported: true, Units: units}, true); err != nil {
		t.Fatal(err)
	}
	return e
}

func (e *desiredAPI) step() {
	e.h.desired.Step(context.Background())
}

func (e *desiredAPI) check() {
	e.h.t.Helper()
	e.h.desired.Refresh([]string{e.hostA})
	// The service's loop isn't running here: run the due check now.
	e.h.desired.Step(context.Background())
	e.h.desired.CheckNow(context.Background(), e.hostA)
}

func TestDesiredStateThroughTheAPI(t *testing.T) {
	e := newDesiredAPI(t)
	in := map[string]any{
		"name": "App", "description": "the app's config and service",
		"resources": []map[string]any{
			{"kind": "file", "path": e.file, "artifact": e.sha, "artifact_name": "app.conf", "mode": "0644"},
			{"kind": "service", "name": "app", "state": "running", "enabled": true},
			{"kind": "package", "name": "telnet", "state": "absent"},
		},
		"assignments": []map[string]any{{"scope": "host", "host_id": e.hostA}},
	}
	// Only administrators change policy; the impact comes first.
	if r := e.op.do("POST", "/api/v1/desired/baselines", in); r.status != 403 {
		t.Fatalf("operator created a baseline: %d", r.status)
	}
	var imp desired.Impact
	if r := e.admin.do("POST", "/api/v1/desired/baselines/preview", in); r.status != 200 {
		t.Fatalf("preview: %d %s", r.status, r.body)
	} else {
		r.json(t, &imp)
	}
	if imp.HostsAffected != 1 || len(imp.Added) != 3 || imp.Counts.BecomeDrifted != 1 {
		t.Fatalf("impact %+v", imp)
	}
	var b desired.BaselineView
	if r := e.admin.do("POST", "/api/v1/desired/baselines", in); r.status != 201 {
		t.Fatalf("create: %d %s", r.status, r.body)
	} else {
		r.json(t, &b)
	}
	if b.Revision != 1 || len(b.Resources) != 3 || b.Assignments[0].Name != "alpha" {
		t.Fatalf("%+v", b)
	}
	e.step()
	e.check()

	// Everyone who sees hosts sees compliance (and, linked, the state of
	// the host's firewall policy, which the Firewall manages).
	var host struct {
		desired.HostView
		Firewall *struct {
			Mode     string `json:"mode"`
			Enforced bool   `json:"enforced"`
		} `json:"firewall"`
	}
	if r := e.viewer.do("GET", "/api/v1/desired/hosts/"+e.hostA, nil); r.status != 200 {
		t.Fatalf("viewer: %d", r.status)
	} else {
		r.json(t, &host)
	}
	if host.Firewall == nil || host.Firewall.Mode != "off" || host.Firewall.Enforced {
		t.Fatalf("firewall line %+v", host.Firewall)
	}
	got := map[string]desired.ResourceView{}
	for _, r := range host.Resources {
		got[r.Key] = r
	}
	file := got["file:"+e.file]
	if file.Status != desired.StatusDrift || desired.DiffText(file.Diffs) == "" || !strings.HasPrefix(desired.DiffText(file.Diffs), "content: ") {
		t.Fatalf("file %+v", file)
	}
	if s := got["service:app.service"]; s.Status != desired.StatusDrift || s.Current != "stopped, enabled at boot" {
		t.Fatalf("service %+v", s)
	}
	// The host's package manager isn't apt or dnf: unknown, never compliant.
	if p := got["package:telnet"]; p.Status != desired.StatusUnknown || !strings.Contains(p.Note, "unsupported package manager") {
		t.Fatalf("package %+v", p)
	}
	if host.Baselines[0].Via != "this host" || host.Status != desired.StatusDrift {
		t.Fatalf("host %+v", host)
	}

	// Review difference: hashes for everyone, contents with files.read.
	var rv map[string]any
	e.viewer.do("GET", "/api/v1/desired/hosts/"+e.hostA+"/review?key=file:"+e.file, nil).json(t, &rv)
	if rv["content_allowed"] != false || rv["file"].(map[string]any)["diff"] != nil {
		t.Fatalf("viewer review %v", rv)
	}
	e.op.do("GET", "/api/v1/desired/hosts/"+e.hostA+"/review?key=file:"+e.file, nil).json(t, &rv)
	if diff, _ := rv["file"].(map[string]any)["diff"].(string); !strings.Contains(diff, "-key = 1") || !strings.Contains(diff, "+key = 2") {
		t.Fatalf("operator review %v", rv)
	}
	if b, _ := os.ReadFile(e.file); string(b) != "key = 1\n" {
		t.Fatal("review changed the file")
	}
	if actor, d := e.auditDetails("desired.review"); actor != "rob" || d["path"] != e.file {
		t.Fatalf("review audit %s %v", actor, d)
	}

	// Restore: viewers can't; operators preview, then restore exactly that.
	sel := map[string]any{"host_ids": []string{e.hostA}, "keys": []string{"file:" + e.file}}
	if r := e.viewer.do("POST", "/api/v1/desired/restore/preview", sel); r.status != 403 {
		t.Fatalf("viewer preview %d", r.status)
	}
	var prev struct {
		Plan    desired.Plan `json:"plan"`
		Missing []string     `json:"missing_permissions"`
	}
	e.op.do("POST", "/api/v1/desired/restore/preview", sel).json(t, &prev)
	if len(prev.Plan.Items) != 1 || len(prev.Plan.Jobs) != 1 || len(prev.Missing) != 0 || prev.Plan.Needs[0] != "files.deploy" {
		t.Fatalf("preview %+v", prev)
	}
	if r := e.op.do("POST", "/api/v1/desired/restore", sel); r.status != 422 {
		t.Fatalf("restore without a preview: %d", r.status)
	}
	sel["plan_hash"] = "stale"
	if r := e.op.do("POST", "/api/v1/desired/restore", sel); r.status != 409 || r.problemCode(t) != "plan_changed" {
		t.Fatalf("restore with another plan: %d %s", r.status, r.body)
	}
	sel["plan_hash"] = prev.Plan.Hash
	var res desired.RestoreResult
	if r := e.op.do("POST", "/api/v1/desired/restore", sel); r.status != 201 {
		t.Fatalf("restore: %d %s", r.status, r.body)
	} else {
		r.json(t, &res)
	}
	j := waitJob(t, e.op, res.Jobs[0])
	if j.Counts["succeeded"] != 1 || j.Origin == nil || j.Origin.ReconcileKind != "manual" {
		t.Fatalf("job %+v", j)
	}
	if err := e.h.desired.SettleNow(context.Background()); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(e.file); string(b) != "key = 2\n" {
		t.Fatalf("not deployed: %q", b)
	}
	var rd struct {
		Resource desired.ResourceView   `json:"resource"`
		History  []desired.HistoryEntry `json:"history"`
	}
	e.viewer.do("GET", "/api/v1/desired/hosts/"+e.hostA+"/resource?key=file:"+e.file, nil).json(t, &rd)
	if rd.Resource.Status != desired.StatusCompliant || rd.History[0].Kind != "restored" || rd.History[0].Resolution != "restored" || rd.History[0].JobID != j.ID {
		t.Fatalf("after restore %+v %+v", rd.Resource, rd.History)
	}
	// The job's hosts say which steps each ran; re-running it is refused.
	var hosts listResponse[jobHostJSON]
	e.op.do("GET", "/api/v1/jobs/"+j.ID+"/hosts", nil).json(t, &hosts)
	if len(hosts.Items) != 1 || len(hosts.Items[0].Steps) != 1 {
		t.Fatalf("job hosts %+v", hosts.Items)
	}
	if r := e.op.do("POST", "/api/v1/jobs/"+j.ID+"/rerun", map[string]any{"hosts": "all"}); r.status != 409 || r.problemCode(t) != "desired_state_job" {
		t.Fatalf("rerun: %d %s", r.status, r.body)
	}
	if actor, d := e.auditDetails("desired.restore"); actor != "rob" || d["job_id"] != j.ID || d["plan_hash"] != prev.Plan.Hash {
		t.Fatalf("restore audit %s %v", actor, d)
	}

	// Accept current state: administrators only; host-specific.
	if r := e.op.do("POST", "/api/v1/desired/hosts/"+e.hostA+"/accept", map[string]any{"keys": []string{"service:app.service"}}); r.status != 403 {
		t.Fatalf("operator accepted: %d", r.status)
	}
	if r := e.admin.do("POST", "/api/v1/desired/hosts/"+e.hostA+"/accept", map[string]any{"keys": []string{"service:app.service"}, "note": "app runs on demand"}); r.status != 200 {
		t.Fatalf("accept: %d %s", r.status, r.body)
	}
	if _, d := e.auditDetails("desired.accept"); d["was"] != "running, enabled at boot" || d["now"] != "stopped, enabled at boot" || d["note"] != "app runs on demand" {
		t.Fatalf("accept audit %v", d)
	}
	e.viewer.do("GET", "/api/v1/desired/hosts/"+e.hostA, nil).json(t, &host)
	for _, r := range host.Resources {
		if r.Key == "service:app.service" && (r.Status != desired.StatusCompliant || r.Source.Label != "Custom for this host") {
			t.Fatalf("after accept %+v", r)
		}
	}
	// Reset to inherited.
	if r := e.admin.do("DELETE", "/api/v1/desired/hosts/"+e.hostA+"/overrides?key=service:app.service", nil); r.status != 200 {
		t.Fatalf("reset: %d %s", r.status, r.body)
	}

	// The fleet summary, and history.
	var sum map[string]any
	e.auditor.do("GET", "/api/v1/desired/summary", nil).json(t, &sum)
	if sum["baselines"] != float64(1) || sum["firewall"] == nil || sum["agents"] == nil {
		t.Fatalf("summary %v", sum)
	}
	var hist listResponse[desired.HistoryEntry]
	e.auditor.do("GET", "/api/v1/desired/history?host="+e.hostA, nil).json(t, &hist)
	if len(hist.Items) < 3 {
		t.Fatalf("history %+v", hist.Items)
	}

	// Automatic restore is administrator-only and off by default.
	if r := e.op.do("PUT", "/api/v1/desired/baselines/"+b.ID+"/auto", map[string]any{"enabled": true}); r.status != 403 {
		t.Fatalf("operator enabled automatic restore: %d", r.status)
	}
	var bv desired.BaselineView
	e.admin.do("PUT", "/api/v1/desired/baselines/"+b.ID+"/auto", map[string]any{"enabled": true, "strategy": map[string]any{"concurrency": 2}}).json(t, &bv)
	if !bv.Auto.Enabled || bv.Auto.Owner.Name != "admin" || bv.Auto.Strategy.Concurrency != 2 {
		t.Fatalf("%+v", bv.Auto)
	}
	if _, d := e.auditDetails("desired.auto"); d["enabled"] != true || d["owner"] != "admin" {
		t.Fatalf("auto audit %v", d)
	}
}

func TestJobPreviewWarnsAboutDrift(t *testing.T) {
	e := newDesiredAPI(t)
	in := map[string]any{"name": "App", "assignments": []map[string]any{{"scope": "global"}},
		"resources": []map[string]any{{"kind": "service", "name": "app", "state": "running"}}}
	if r := e.admin.do("POST", "/api/v1/desired/baselines", in); r.status != 201 {
		t.Fatal(r.status)
	}
	var p previewResponse
	e.op.do("POST", "/api/v1/jobs/preview", map[string]any{"targets": map[string]any{"hosts": []string{e.hostA}},
		"steps": []map[string]any{{"type": "service", "name": "app", "state": "stopped"}}}).json(t, &p)
	if len(p.DesiredWarnings) != 1 || !strings.Contains(p.DesiredWarnings[0].Message, "app.service is managed to keep running") {
		t.Fatalf("%+v", p.DesiredWarnings)
	}
	b, _ := json.Marshal(p.DesiredWarnings)
	if strings.Contains(string(b), "Automatic reconciliation") {
		t.Fatal("the stronger warning without automatic restore")
	}
}
