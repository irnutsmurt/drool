package api

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"drool/internal/auth"
	"drool/internal/sched"
	"drool/internal/sshx/sshtest"
	"drool/internal/store/db"
	"drool/internal/verify"
)

// Scheduling and workflows through the API (M13): who may create, change,
// run and reassign schedules, what is audited, and that every run is an
// ordinary job. Route-level permissions for every role are in
// TestAuthzMatrix; this covers ownership and the checks behind it.

type schedAPI struct {
	h                      *harness
	admin, op, op2, viewer *client
	hostA                  string
	srvA                   *sshtest.Server // hostA's SSH server
}

func newSchedAPI(t *testing.T) *schedAPI {
	t.Helper()
	h := newHarness(t)
	h.mkUser("admin", auth.RoleAdmin)
	h.mkUser("rob", auth.RoleOperator)
	h.mkUser("sam", auth.RoleOperator)
	h.mkUser("vera", auth.RoleViewer)
	e := &schedAPI{h: h, admin: h.login("admin"), op: h.login("rob"), op2: h.login("sam"), viewer: h.login("vera")}
	e.hostA, e.srvA = readyHostServer(t, h, e.admin, "alpha")
	return e
}

// auditDetails returns the details of the latest audit event of an action.
func (e *schedAPI) auditDetails(action string) (actor string, d map[string]any) {
	e.h.t.Helper()
	var js string
	if err := e.h.st.Reader.QueryRow(`SELECT actor_name, details_json FROM audit_events WHERE action = ? ORDER BY id DESC LIMIT 1`, action).Scan(&actor, &js); err != nil {
		e.h.t.Fatalf("no %s audit event: %v", action, err)
	}
	json.Unmarshal([]byte(js), &d)
	return actor, d
}

func (e *schedAPI) userID(name string) string {
	var id string
	e.h.st.Reader.QueryRow(`SELECT id FROM users WHERE username = ?`, name).Scan(&id)
	return id
}

func weekly(steps []map[string]any, hosts ...string) map[string]any {
	return map[string]any{
		"name": "Weekly", "time_zone": "Europe/Berlin",
		"recurrence": map[string]any{"kind": "weekly", "time": "03:00", "weekdays": []string{"sun"}},
		"targets":    map[string]any{"hosts": hosts},
		"steps":      steps,
	}
}

var trueStep = []map[string]any{{"type": "command", "command": "true"}}

func TestScheduleOwnershipAndAudit(t *testing.T) {
	e := newSchedAPI(t)
	r := e.op.do("POST", "/api/v1/schedules", weekly(trueStep, e.hostA))
	if r.status != 201 {
		t.Fatalf("create: %d %s", r.status, r.body)
	}
	var sc sched.Schedule
	r.json(t, &sc)
	if sc.Owner == nil || sc.Owner.Name != "rob" || !sc.Enabled || sc.NextRunAt == nil || sc.When != "Every Sunday at 03:00 (Europe/Berlin)" {
		t.Fatalf("created %+v", sc)
	}
	if actor, d := e.auditDetails("schedule.create"); actor != "rob" || d["name"] != "Weekly" || d["owner"] != "rob" {
		t.Fatalf("create audit: %s %v", actor, d)
	}
	path := "/api/v1/schedules/" + sc.ID

	// Everyone who may read schedules sees it.
	var list struct{ Items []sched.Schedule }
	e.viewer.do("GET", "/api/v1/schedules", nil).json(t, &list)
	if len(list.Items) != 1 {
		t.Fatalf("viewer list: %+v", list)
	}
	// Another operator may not change someone else's schedule...
	for _, c := range []struct{ method, path string }{{"PUT", path}, {"DELETE", path}, {"POST", path + "/enable"}} {
		var body any
		if c.method == "PUT" {
			body = weekly(trueStep, e.hostA)
		}
		if r := e.op2.do(c.method, c.path, body); r.status != 403 {
			t.Errorf("%s %s by another operator: %d %s", c.method, c.path, r.status, r.body)
		}
	}
	// ...but anyone who manages schedules may stop one.
	if r := e.op2.do("POST", path+"/disable", nil); r.status != 200 {
		t.Fatalf("disable: %d %s", r.status, r.body)
	}
	if actor, _ := e.auditDetails("schedule.disable"); actor != "sam" {
		t.Fatalf("disable audited as %s", actor)
	}
	if r := e.op.do("POST", path+"/enable", nil); r.status != 200 {
		t.Fatalf("enable: %d %s", r.status, r.body)
	}

	// An administrator's edit makes them the owner: a schedule never runs
	// someone else's changes with its previous owner's permissions.
	in := weekly(trueStep, e.hostA)
	in["name"] = "Weekly (admin)"
	if r := e.admin.do("PUT", path, in); r.status != 200 {
		t.Fatalf("admin edit: %d %s", r.status, r.body)
	}
	e.admin.do("GET", path, nil).json(t, &sc)
	if sc.Owner.Name != "admin" || sc.Revision != 2 {
		t.Fatalf("after admin edit: owner %v revision %d", sc.Owner, sc.Revision)
	}
	if _, d := e.auditDetails("schedule.update"); d["ownership"] != "transferred to the editor" || d["previous_owner"] != "rob" {
		t.Fatalf("update audit: %v", d)
	}

	// Reassigning needs schedules.admin, and an owner who may run it.
	if r := e.op.do("POST", path+"/owner", map[string]string{"user_id": e.userID("rob")}); r.status != 403 {
		t.Fatalf("operator reassign: %d", r.status)
	}
	if r := e.admin.do("POST", path+"/owner", map[string]string{"user_id": e.userID("vera")}); r.status != 422 || !strings.Contains(string(r.body), "jobs.execute") {
		t.Fatalf("reassign to a viewer: %d %s", r.status, r.body)
	}
	if r := e.admin.do("POST", path+"/owner", map[string]string{"user_id": e.userID("rob")}); r.status != 200 {
		t.Fatalf("reassign: %d %s", r.status, r.body)
	}
	if _, d := e.auditDetails("schedule.reassign"); d["owner"] != "rob" || d["previous_owner"] != "admin" {
		t.Fatalf("reassign audit: %v", d)
	}

	// Enabling checks the owner can still run it.
	e.op.do("POST", path+"/disable", nil)
	if r := e.admin.do("PATCH", "/api/v1/users/"+e.userID("rob"), map[string]any{"disabled": true}); r.status != 200 {
		t.Fatalf("disable user: %d %s", r.status, r.body)
	}
	if r := e.admin.do("POST", path+"/enable", nil); r.status != 422 || !strings.Contains(string(r.body), "account is disabled") {
		t.Fatalf("enable with a disabled owner: %d %s", r.status, r.body)
	}
	e.admin.do("PATCH", "/api/v1/users/"+e.userID("rob"), map[string]any{"disabled": false})
	e.op = e.h.login("rob")
	if r := e.admin.do("POST", path+"/enable", nil); r.status != 200 {
		t.Fatalf("enable: %d %s", r.status, r.body)
	}

	// Run now is the caller's own action: the job runs as the caller.
	r = e.op2.do("POST", path+"/run-now", nil)
	if r.status != 201 {
		t.Fatalf("run now: %d %s", r.status, r.body)
	}
	var run struct {
		OccurrenceID string `json:"occurrence_id"`
		JobID        string `json:"job_id"`
		Status       string `json:"status"`
	}
	r.json(t, &run)
	j := waitJob(t, e.op2, run.JobID)
	if j.CreatedBy != "sam" || j.Counts["succeeded"] != 1 || j.Origin == nil || j.Origin.ScheduleID != sc.ID || !j.Origin.Manual {
		t.Fatalf("run-now job: %+v %+v", j, j.Origin)
	}
	if _, d := e.auditDetails("schedule.run_now"); d["occurrence_id"] != run.OccurrenceID {
		t.Fatalf("run-now audit: %v", d)
	}
	var occ struct{ Items []sched.Occurrence }
	e.viewer.do("GET", path+"/occurrences", nil).json(t, &occ)
	if len(occ.Items) != 1 || !occ.Items[0].Manual || occ.Items[0].TriggeredBy != "sam" || occ.Items[0].JobID != run.JobID ||
		!strings.HasPrefix(occ.Items[0].Outcome, "Completed: 1 succeeded") {
		t.Fatalf("occurrences: %+v", occ.Items)
	}
	// Run now doesn't move the next scheduled run.
	var after sched.Schedule
	e.op.do("GET", path, nil).json(t, &after)
	if after.NextRunAt == nil || *after.NextRunAt != *sc.NextRunAt {
		t.Fatalf("next run moved: %v → %v", sc.NextRunAt, after.NextRunAt)
	}

	// Archiving keeps history; only the owner (or an admin) may.
	if r := e.op2.do("DELETE", path, nil); r.status != 403 {
		t.Fatalf("archive by another operator: %d", r.status)
	}
	if r := e.op.do("DELETE", path, nil); r.status != 204 {
		t.Fatalf("archive: %d %s", r.status, r.body)
	}
	e.op.do("GET", "/api/v1/schedules", nil).json(t, &list)
	if len(list.Items) != 0 {
		t.Fatalf("archived schedule listed: %+v", list.Items)
	}
	e.op.do("GET", "/api/v1/schedules?archived=true", nil).json(t, &list)
	if len(list.Items) != 1 || !list.Items[0].Archived {
		t.Fatalf("archived list: %+v", list.Items)
	}
	e.viewer.do("GET", path+"/occurrences", nil).json(t, &occ)
	if len(occ.Items) != 1 {
		t.Fatalf("history lost on archive: %+v", occ.Items)
	}
	for _, a := range []string{"schedule.create", "schedule.update", "schedule.enable", "schedule.disable", "schedule.reassign", "schedule.run_now", "schedule.archive"} {
		e.auditDetails(a)
	}
}

func TestRunNowWhileBusy(t *testing.T) {
	e := newSchedAPI(t)
	var sc sched.Schedule
	e.op.do("POST", "/api/v1/schedules", weekly([]map[string]any{{"type": "command", "command": "sleep 2"}}, e.hostA)).json(t, &sc)
	r := e.op.do("POST", "/api/v1/schedules/"+sc.ID+"/run-now", nil)
	if r.status != 201 {
		t.Fatalf("run now: %d %s", r.status, r.body)
	}
	var run struct {
		JobID string `json:"job_id"`
	}
	r.json(t, &run)
	if r := e.op.do("POST", "/api/v1/schedules/"+sc.ID+"/run-now", nil); r.status != 409 || r.problemCode(t) != "busy" {
		t.Fatalf("second run now: %d %s", r.status, r.body)
	}
	// Disabling doesn't cancel the run in progress.
	e.op.do("POST", "/api/v1/schedules/"+sc.ID+"/disable", nil)
	if _, d := e.auditDetails("schedule.disable"); d["running_job_continues"] != run.JobID {
		t.Fatalf("disable audit: %v", d)
	}
	if j := waitJob(t, e.op, run.JobID); j.Status != "completed" || j.Counts["succeeded"] != 1 {
		t.Fatalf("job after disable: %+v", j)
	}
}

func TestScheduleValidation(t *testing.T) {
	e := newSchedAPI(t)
	bad := func(name string, mutate func(m map[string]any), want string) {
		t.Helper()
		in := weekly(trueStep, e.hostA)
		mutate(in)
		r := e.op.do("POST", "/api/v1/schedules", in)
		if r.status != 422 || !strings.Contains(string(r.body), want) {
			t.Errorf("%s: %d %s (want %q)", name, r.status, r.body, want)
		}
	}
	bad("controller zone", func(m map[string]any) { m["time_zone"] = "Local" }, "choose a time zone")
	bad("unknown zone", func(m map[string]any) { m["time_zone"] = "Mars/Olympus" }, "unknown time zone")
	bad("no targets", func(m map[string]any) { m["targets"] = map[string]any{} }, "at least one host")
	bad("no steps", func(m map[string]any) { delete(m, "steps") }, "choose a workflow, or the steps")
	bad("bad weekday", func(m map[string]any) {
		m["recurrence"] = map[string]any{"kind": "weekly", "time": "03:00", "weekdays": []string{"someday"}}
	}, "unknown weekday")
	bad("cron", func(m map[string]any) { m["recurrence"] = map[string]any{"kind": "cron", "time": "0 3 * * 0"} }, "kind must be")
	// Firewall policy isn't a schedulable step: an operator lacks the
	// permission anyway; an administrator is told where it belongs.
	fw := weekly([]map[string]any{{"type": "firewall_apply", "remove": true}}, e.hostA)
	if r := e.op.do("POST", "/api/v1/schedules", fw); r.status != 403 || !strings.Contains(string(r.body), "firewall.write") {
		t.Errorf("operator firewall schedule: %d %s", r.status, r.body)
	}
	if r := e.admin.do("POST", "/api/v1/schedules", fw); r.status != 422 || !strings.Contains(string(r.body), "Firewall page") {
		t.Errorf("admin firewall schedule: %d %s", r.status, r.body)
	}
	if r := e.admin.do("POST", "/api/v1/workflows", map[string]any{"name": "fw", "steps": fw["steps"]}); r.status != 422 || !strings.Contains(string(r.body), "Firewall page") {
		t.Errorf("admin firewall workflow: %d %s", r.status, r.body)
	}
	bad("window over itself", func(m map[string]any) {
		m["kind"], m["duration_minutes"] = "maintenance", 3*24*60
		m["recurrence"] = map[string]any{"kind": "daily", "time": "03:00"}
		delete(m, "steps")
	}, "overlap itself")
	bad("tags on a window", func(m map[string]any) {
		m["kind"], m["duration_minutes"] = "maintenance", 60
		m["targets"] = map[string]any{"tags": []string{"web"}}
		delete(m, "steps")
	}, "hosts and groups")
}

func TestWorkflowsThroughTheAPI(t *testing.T) {
	e := newSchedAPI(t)
	r := e.op.do("POST", "/api/v1/workflows", map[string]any{"name": "Check", "steps": trueStep})
	if r.status != 201 {
		t.Fatalf("create: %d %s", r.status, r.body)
	}
	var wf sched.Workflow
	r.json(t, &wf)
	if wf.LatestRevision != 1 {
		t.Fatalf("created %+v", wf)
	}
	if r := e.op2.do("POST", "/api/v1/workflows", map[string]any{"name": "check", "steps": trueStep}); r.status != 409 {
		t.Fatalf("duplicate name: %d %s", r.status, r.body)
	}
	if r := e.viewer.do("POST", "/api/v1/workflows", map[string]any{"name": "x", "steps": trueStep}); r.status != 403 {
		t.Fatalf("viewer create: %d", r.status)
	}
	path := "/api/v1/workflows/" + wf.ID
	var upd struct {
		Workflow sched.Workflow `json:"workflow"`
		Changed  bool           `json:"changed"`
	}
	e.op.do("PUT", path, map[string]any{"name": "Check", "steps": trueStep}).json(t, &upd)
	if upd.Changed || upd.Workflow.LatestRevision != 1 {
		t.Fatalf("no-op edit made a revision: %+v", upd)
	}

	// A schedule pins revision 1.
	in := weekly(nil, e.hostA)
	delete(in, "steps")
	in["workflow"] = map[string]any{"id": wf.ID}
	var sc sched.Schedule
	if r := e.op.do("POST", "/api/v1/schedules", in); r.status != 201 {
		t.Fatalf("schedule: %d %s", r.status, r.body)
	} else {
		r.json(t, &sc)
	}
	if sc.Config.Workflow.Revision != 1 || sc.WorkflowUpdate {
		t.Fatalf("pinned %+v", sc.Config.Workflow)
	}
	two := []map[string]any{{"type": "command", "command": "true"}, {"type": "command", "command": "echo second"}}
	e.op2.do("PUT", path, map[string]any{"name": "Check", "steps": two}).json(t, &upd)
	if !upd.Changed || upd.Workflow.LatestRevision != 2 || upd.Workflow.OutdatedPinning != 1 {
		t.Fatalf("edit: %+v", upd)
	}
	if _, d := e.auditDetails("workflow.update"); d["revision"] != float64(2) || d["previous_revision"] != float64(1) {
		t.Fatalf("workflow.update audit: %v", d)
	}
	// The schedule still runs revision 1, and says a newer one exists.
	e.op.do("GET", "/api/v1/schedules/"+sc.ID, nil).json(t, &sc)
	if sc.Config.Workflow.Revision != 1 || !sc.WorkflowUpdate || sc.WorkflowLatest != 2 {
		t.Fatalf("after workflow edit: %+v update=%v", sc.Config.Workflow, sc.WorkflowUpdate)
	}
	var rev sched.Revision
	e.viewer.do("GET", path+"/revisions/1", nil).json(t, &rev)
	if len(rev.Steps) != 1 {
		t.Fatalf("revision 1: %+v", rev)
	}
	// Updating is explicit, and audited.
	in["workflow"] = map[string]any{"id": wf.ID, "revision": 2}
	if r := e.op.do("PUT", "/api/v1/schedules/"+sc.ID, in); r.status != 200 {
		t.Fatalf("adopt revision 2: %d %s", r.status, r.body)
	}
	if _, d := e.auditDetails("schedule.update"); d["workflow_revision"] != float64(2) || d["workflow_revision_before"] != float64(1) {
		t.Fatalf("schedule.update audit: %v", d)
	}

	// Templates are starting points.
	var tpl struct{ Items []sched.Template }
	e.viewer.do("GET", "/api/v1/workflows/templates", nil).json(t, &tpl)
	if len(tpl.Items) != 3 {
		t.Fatalf("templates: %+v", tpl)
	}

	// Running a workflow creates an ordinary job that remembers its origin.
	if r := e.viewer.do("POST", path+"/run", map[string]any{"targets": map[string]any{"hosts": []string{e.hostA}}}); r.status != 403 {
		t.Fatalf("viewer run: %d", r.status)
	}
	r = e.op.do("POST", path+"/run", map[string]any{"targets": map[string]any{"hosts": []string{e.hostA}}, "revision": 1})
	if r.status != 201 {
		t.Fatalf("run: %d %s", r.status, r.body)
	}
	var j jobJSON
	r.json(t, &j)
	j = waitJob(t, e.op, j.ID)
	if j.Counts["succeeded"] != 1 || j.Origin == nil || j.Origin.WorkflowID != wf.ID || j.Origin.WorkflowRevision != 1 || j.Origin.WorkflowName != "Check" {
		t.Fatalf("workflow job: %+v %+v", j, j.Origin)
	}
	var spec struct{ Steps []json.RawMessage }
	json.Unmarshal(j.Spec, &spec)
	if len(spec.Steps) != 1 {
		t.Fatalf("revision 1 ran %d steps", len(spec.Steps))
	}

	// Archived: existing schedules keep their revision; new ones can't use it.
	if r := e.op.do("DELETE", path, nil); r.status != 204 {
		t.Fatalf("archive: %d %s", r.status, r.body)
	}
	e.op.do("GET", "/api/v1/schedules/"+sc.ID, nil).json(t, &sc)
	if !sc.WorkflowArchive || !sc.Enabled {
		t.Fatalf("schedule after archive: %+v", sc)
	}
	in["workflow"] = map[string]any{"id": wf.ID}
	if r := e.op.do("POST", "/api/v1/schedules", in); r.status != 422 || !strings.Contains(string(r.body), "archived") {
		t.Fatalf("schedule an archived workflow: %d %s", r.status, r.body)
	}
}

// TestWorkflowWithChecks runs update-style steps and the controller-side
// checks end to end: a real job over SSH, then wait and verify host.
func TestWorkflowWithChecks(t *testing.T) {
	e := newSchedAPI(t)
	e.h.eng.SetLocal(verify.New(verify.Deps{Store: e.h.st, Monitor: e.h.mon,
		AgentConnected: func(context.Context, db.Host) bool { return false },
		Probe: func(context.Context, string) verify.Reachability {
			return verify.Reachability{OK: true, Detail: "SSH connection test passed"}
		},
	}))
	steps := []map[string]any{{"type": "command", "command": "echo updated"}, {"type": "wait", "seconds": 1}, {"type": "verify_host", "timeout_s": 30}}
	var wf sched.Workflow
	if r := e.op.do("POST", "/api/v1/workflows", map[string]any{"name": "Update and check", "steps": steps}); r.status != 201 {
		t.Fatalf("create: %d %s", r.status, r.body)
	} else {
		r.json(t, &wf)
	}
	var j jobJSON
	e.op.do("POST", "/api/v1/workflows/"+wf.ID+"/run", map[string]any{"targets": map[string]any{"hosts": []string{e.hostA}}}).json(t, &j)
	j = waitJob(t, e.op, j.ID)
	if j.Counts["succeeded"] != 1 {
		t.Fatalf("job: %+v", j)
	}
	var out outputResponse
	e.op.do("GET", "/api/v1/jobs/"+j.ID+"/hosts/"+e.hostA+"/output?step=2", nil).json(t, &out)
	var text string
	for _, s := range out.Segments {
		text += s.Text
	}
	if !strings.Contains(text, "the host is reachable") {
		t.Fatalf("verify output: %q", text)
	}
}

func TestSchedulePreviewAndNextRuns(t *testing.T) {
	e := newSchedAPI(t)
	var hosts []string
	for i := 1; i <= 5; i++ {
		id := fmt.Sprintf("hp%d", i)
		if _, err := e.h.st.Writer.Exec(`INSERT INTO hosts (id, name, connect_address, ssh_user, status, created_at, updated_at) VALUES (?, ?, '10.0.0.1', 'x', 'ready', 0, 0)`,
			id, fmt.Sprintf("node-%d", i)); err != nil {
			t.Fatal(err)
		}
		hosts = append(hosts, id)
	}
	// node-5 is in maintenance: a scheduled run skips it.
	if r := e.op.do("POST", "/api/v1/maintenance", map[string]any{"host_ids": []string{"hp5"}, "reason": "disk swap", "duration_minutes": 60}); r.status != 201 {
		t.Fatalf("maintenance: %d %s", r.status, r.body)
	}
	in := weekly([]map[string]any{{"type": "system_update", "reboot": "if_required"}}, hosts...)
	in["strategy"] = map[string]any{"concurrency": 5, "canary_count": 1, "batch_size": 2, "failure_threshold": 2}
	var p sched.Preview
	if r := e.viewer.do("POST", "/api/v1/schedules/preview", in); r.status != 200 {
		t.Fatalf("preview: %d %s", r.status, r.body)
	} else {
		r.json(t, &p)
	}
	waves := [][]string{}
	for _, w := range p.Waves {
		waves = append(waves, w.Hosts)
	}
	if fmt.Sprint(waves) != "[[node-1] [node-2 node-3] [node-4]]" || !p.Waves[0].Canary {
		t.Fatalf("waves %v", p.Waves)
	}
	var skipped []string
	for _, h := range p.Targets {
		if h.Wave < 0 {
			skipped = append(skipped, h.Name+": "+h.Maintenance)
		}
	}
	if fmt.Sprint(skipped) != "[node-5: disk swap]" {
		t.Fatalf("skipped %v", skipped)
	}
	if !strings.Contains(p.Reboot, "only if the update requires it") || len(p.NextRuns) != 5 || p.Threshold == nil || *p.Threshold != 2 {
		t.Fatalf("preview %+v", p)
	}
	joined := strings.Join(append(p.Notes, p.Policies...), "\n")
	for _, want := range []string{"resolved again at every run", "can't promise exactly what next week's run installs", "recorded as missed", "skipped"} {
		if !strings.Contains(joined, want) {
			t.Errorf("preview text lacks %q:\n%s", want, joined)
		}
	}
	// Nothing was saved.
	var list struct{ Items []sched.Schedule }
	e.op.do("GET", "/api/v1/schedules", nil).json(t, &list)
	if len(list.Items) != 0 {
		t.Fatal("preview saved a schedule")
	}

	var nr struct {
		When string          `json:"when"`
		Runs []sched.NextRun `json:"runs"`
	}
	r := e.viewer.do("POST", "/api/v1/schedules/next-runs", map[string]any{"recurrence": map[string]any{"kind": "daily", "time": "03:00"}, "time_zone": "Europe/Berlin", "count": 3})
	r.json(t, &nr)
	if r.status != 200 || nr.When != "Every day at 03:00 (Europe/Berlin)" || len(nr.Runs) != 3 {
		t.Fatalf("next runs: %d %s", r.status, r.body)
	}
	for i, run := range nr.Runs {
		at := time.UnixMilli(run.At).In(mustZone(t, "Europe/Berlin"))
		if at.Hour() != 3 || at.Minute() != 0 || (i > 0 && run.At <= nr.Runs[i-1].At) {
			t.Fatalf("run %d at %s", i, at)
		}
	}
	if r := e.viewer.do("POST", "/api/v1/schedules/next-runs", map[string]any{"recurrence": map[string]any{"kind": "daily", "time": "03:00"}, "time_zone": "Local"}); r.status != 422 {
		t.Fatalf("Local zone: %d", r.status)
	}
	if r := e.viewer.do("POST", "/api/v1/schedules/next-runs", map[string]any{"recurrence": map[string]any{"kind": "daily", "time": "25:00"}, "time_zone": "UTC"}); r.status != 422 {
		t.Fatalf("bad time: %d", r.status)
	}
}

func mustZone(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatal(err)
	}
	return loc
}
