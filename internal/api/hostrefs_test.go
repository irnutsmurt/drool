package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"drool/internal/audit"
	"drool/internal/auth"
	"drool/internal/hostrefs"
	"drool/internal/sched"
)

// Removing a host takes it out of the schedules and firewall policies that
// name it (dogfooding correction batch, ARCHITECTURE-ALPHA §40.7). Before,
// a schedule kept the removed host's id: after adding the host back, saving
// the schedule failed with "host <old id> not found", and so did every run.
// Now the removal plan lists each change, the removal makes them in its
// transaction (each audited), a schedule left without targets is disabled
// and says why, a firewall policy left without a source or destination is
// deleted, and notification routes keep the host as they always did (so
// none widens to every host). Adding the host back and putting it in the
// schedule then works, and so does running it.
func TestRemovedHostLeavesSchedulesAndPolicies(t *testing.T) {
	h := newHarness(t)
	h.mkUser("admin", auth.RoleAdmin)
	c := h.login("admin")
	must := func(want int, method, path string, body any) resp {
		t.Helper()
		r := c.do(method, path, body)
		if r.status != want {
			t.Fatalf("%s %s: %d %s", method, path, r.status, r.body)
		}
		return r
	}
	idOf := func(r resp) string {
		t.Helper()
		var v struct {
			ID string `json:"id"`
		}
		r.json(t, &v)
		return v.ID
	}
	var nas, db3 hostJSON
	must(201, "POST", "/api/v1/hosts", map[string]any{"name": "old-nas", "address": "192.0.2.61", "port": 22, "user": "deploy"}).json(t, &nas)
	must(201, "POST", "/api/v1/hosts", map[string]any{"name": "db-03", "address": "192.0.2.63", "port": 22, "user": "deploy"}).json(t, &db3)
	web, _ := readyHostServer(t, h, c, "web-02")
	g := idOf(must(201, "POST", "/api/v1/groups", map[string]any{"name": "databases"}))
	must(200, "POST", "/api/v1/groups/"+g+"/members", map[string]any{"host_ids": []string{db3.ID}})

	schedule := func(name string, mod func(m map[string]any)) string {
		t.Helper()
		m := weekly(trueStep, nas.ID, web)
		m["name"] = name
		if mod != nil {
			mod(m)
		}
		return idOf(must(201, "POST", "/api/v1/schedules", m))
	}
	updates := schedule("Weekly updates", nil)
	only := schedule("Only old-nas", func(m map[string]any) { m["targets"] = map[string]any{"hosts": []string{nas.ID}} })
	pack := schedule("Databases but old-nas", func(m map[string]any) {
		m["targets"] = map[string]any{"groups": []string{g}, "exclude_hosts": []string{nas.ID}}
	})
	canary := schedule("Canary first", func(m map[string]any) { m["strategy"] = map[string]any{"canary_hosts": []string{nas.ID}} })
	window := schedule("NAS maintenance", func(m map[string]any) {
		m["kind"], m["duration_minutes"] = "maintenance", 60
		m["targets"] = map[string]any{"hosts": []string{nas.ID}}
		delete(m, "steps")
	})
	sel := func(kind, id string) map[string]any { return map[string]any{"kind": kind, "id": id} }
	policy := func(name string, from, to []any) string {
		t.Helper()
		return idOf(must(201, "POST", "/api/v1/firewall/policies", map[string]any{"name": name, "action": "allow", "from": from, "to": to, "services": []string{"ssh"}}))
	}
	p1 := policy("nas to web", []any{sel("host", nas.ID)}, []any{sel("host", web)})
	p2 := policy("web and nas to db", []any{sel("host", nas.ID), sel("host", web)}, []any{sel("host", db3.ID)})
	p3 := policy("lan to nas", []any{map[string]any{"kind": "cidr", "cidr": "10.0.0.0/8"}}, []any{sel("host", nas.ID)})
	p4 := policy("web to db", []any{sel("host", web)}, []any{sel("host", db3.ID)})
	route := idOf(must(201, "POST", "/api/v1/notifications/routes", map[string]any{"name": "nas pager", "host_ids": []string{nas.ID}}))

	// The plan says what changes, either way.
	var plan removalPlan
	must(200, "POST", "/api/v1/hosts/"+nas.ID+"/removal/plan", nil).json(t, &plan)
	got := map[string]hostrefs.Change{}
	for _, ch := range plan.Changes {
		got[ch.ID] = ch
		if ch.Name == "" || ch.Detail == "" || ch.Path == "" || !strings.Contains(ch.Detail, "old-nas") {
			t.Fatalf("a change without words or a page: %+v", ch)
		}
	}
	if len(plan.Changes) != 8 || !got[only].Disabled || !got[window].Disabled || got[updates].Disabled || got[pack].Disabled ||
		!got[p1].Deleted || !got[p3].Deleted || got[p2].Deleted {
		t.Fatalf("plan: %+v", plan.Changes)
	}
	if _, listed := got[p4]; listed {
		t.Fatal("a policy that doesn't name the host is in the plan")
	}
	if !strings.Contains(got[canary].Detail, "the first host by name is the canary instead") ||
		!strings.Contains(got[only].Detail, "disabled until its targets are chosen again") ||
		!strings.Contains(got[p1].Detail, "Its only source was old-nas") || !strings.Contains(got[p3].Detail, "Its only destination was old-nas") ||
		!strings.Contains(got[p2].Detail, "taken out of its sources") {
		t.Fatalf("plan wording: %+v", plan.Changes)
	}
	// Planning changes nothing.
	if sc := getSchedule(t, c, updates); sc.Revision != 1 || !slices.Contains(sc.Config.Targets.Hosts, nas.ID) {
		t.Fatalf("the plan changed a schedule: %+v", sc)
	}

	must(204, "DELETE", "/api/v1/hosts/"+nas.ID, nil)

	// Schedules: taken out, in a new revision by the remover.
	if sc := getSchedule(t, c, updates); !slices.Equal(sc.Config.Targets.Hosts, []string{web}) || !sc.Enabled || sc.Revision != 2 || sc.Attention != "" {
		t.Fatalf("Weekly updates: %+v", sc)
	}
	var by string
	h.st.Reader.QueryRow(`SELECT changed_by_name FROM schedule_revisions WHERE schedule_id = ? AND revision = 2`, updates).Scan(&by)
	if by != "admin" {
		t.Fatalf("revision 2 by %q", by)
	}
	for _, id := range []string{only, window} {
		if sc := getSchedule(t, c, id); sc.Enabled || !sc.Config.Targets.Empty() ||
			sc.Attention != "Disabled: its only target, old-nas, was removed from DROOL. Choose its targets, then enable it." {
			t.Fatalf("%s: %+v", sc.Name, sc)
		}
	}
	if sc := getSchedule(t, c, pack); len(sc.Config.Targets.ExcludeHosts) != 0 || !slices.Equal(sc.Config.Targets.Groups, []string{g}) || !sc.Enabled {
		t.Fatalf("Databases but old-nas: %+v", sc)
	}
	if sc := getSchedule(t, c, canary); len(sc.Config.Strategy.CanaryHosts) != 0 || sc.Config.Strategy.CanaryCount != 1 ||
		!slices.Equal(sc.Config.Targets.Hosts, []string{web}) {
		t.Fatalf("Canary first: %+v", sc)
	}
	// Nothing to run on: it can't be switched back on until it has targets.
	if r := c.do("POST", "/api/v1/schedules/"+only+"/enable", nil); r.status != 422 || !strings.Contains(string(r.body), "it has no targets") {
		t.Fatalf("enabling a schedule without targets: %d %s", r.status, r.body)
	}

	// Firewall policies: deleted when a side is left empty, else taken out.
	var pols struct {
		Items []struct {
			ID   string            `json:"id"`
			From []json.RawMessage `json:"from"`
			To   []json.RawMessage `json:"to"`
		} `json:"items"`
	}
	must(200, "GET", "/api/v1/firewall/policies", nil).json(t, &pols)
	left := map[string]string{}
	for _, p := range pols.Items {
		b, _ := json.Marshal(map[string]any{"from": p.From, "to": p.To})
		left[p.ID] = string(b)
	}
	if _, ok := left[p1]; ok {
		t.Fatal("a policy whose only source was the host is still there")
	}
	if _, ok := left[p3]; ok {
		t.Fatal("a policy whose only destination was the host is still there")
	}
	if strings.Contains(left[p2], nas.ID) || !strings.Contains(left[p2], web) || !strings.Contains(left[p4], web) {
		t.Fatalf("policies after: %v", left)
	}

	// The route keeps the host (dropping it would widen the route to every
	// host): unchanged.
	var hostIDs string
	var enabled bool
	h.st.Reader.QueryRow(`SELECT host_ids_json, enabled FROM notify_routes WHERE id = ?`, route).Scan(&hostIDs, &enabled)
	if hostIDs != `["`+nas.ID+`"]` || !enabled {
		t.Fatalf("route: %s %v", hostIDs, enabled)
	}

	// Audited: the removal lists them; each change is its own record.
	assertAuditLike(t, h, "host.delete", `"taken_out_of":[`)
	count := func(action string) int {
		var n int
		h.st.Reader.QueryRow(`SELECT count(*) FROM audit_events WHERE action = ? AND details_json LIKE '%the host old-nas was removed from DROOL%' AND actor_name = 'admin'`, action).Scan(&n)
		return n
	}
	if count("schedule.update") != 5 || count("firewall.policy.delete") != 2 || count("firewall.policy.update") != 1 {
		t.Fatalf("audit: %d schedule.update, %d firewall.policy.delete, %d firewall.policy.update",
			count("schedule.update"), count("firewall.policy.delete"), count("firewall.policy.update"))
	}

	// Add the host back and put it in the schedule again: saved, and a run
	// starts on both hosts (it used to say "host <old id> not found").
	again, _ := readyHostServer(t, h, c, "old-nas")
	m := weekly(trueStep, web, again)
	m["name"] = "Weekly updates"
	must(200, "PUT", "/api/v1/schedules/"+updates, m)
	var run struct {
		JobID  string `json:"job_id"`
		Status string `json:"status"`
	}
	must(201, "POST", "/api/v1/schedules/"+updates+"/run-now", nil).json(t, &run)
	if j := waitJob(t, c, run.JobID); j.Counts["succeeded"] != 2 {
		t.Fatalf("the run after adding the host back: %+v", j)
	}
}

// Schedules and firewall policies that still name a host removed before
// DROOL took removed hosts out of them (like the one that said "host
// <old id> not found") are repaired at start, as DROOL's own audited
// changes, naming the host from the audit log; a second start finds
// nothing.
func TestRemovedHostRepairAtStart(t *testing.T) {
	h := newHarness(t)
	h.mkUser("admin", auth.RoleAdmin)
	c := h.login("admin")
	var oldDB, app hostJSON
	c.do("POST", "/api/v1/hosts", map[string]any{"name": "old-db", "address": "192.0.2.71", "port": 22, "user": "deploy"}).json(t, &oldDB)
	c.do("POST", "/api/v1/hosts", map[string]any{"name": "app-01", "address": "192.0.2.72", "port": 22, "user": "deploy"}).json(t, &app)
	create := func(path string, body map[string]any) string {
		t.Helper()
		r := c.do("POST", path, body)
		var v struct {
			ID string `json:"id"`
		}
		r.json(t, &v)
		if r.status != 201 {
			t.Fatalf("POST %s: %d %s", path, r.status, r.body)
		}
		return v.ID
	}
	both := weekly(trueStep, oldDB.ID, app.ID)
	both["name"] = "Patch both"
	updates := create("/api/v1/schedules", both)
	alone := weekly(trueStep, oldDB.ID)
	alone["name"] = "Back up old-db"
	backup := create("/api/v1/schedules", alone)
	pol := create("/api/v1/firewall/policies", map[string]any{"name": "app to db", "action": "allow",
		"from": []any{map[string]any{"kind": "host", "id": app.ID}}, "to": []any{map[string]any{"kind": "host", "id": oldDB.ID}}, "services": []string{"ssh"}})

	// As an earlier DROOL removed it: the host and what cascades, recorded,
	// the schedules and the policy left naming it.
	h.auditW.Record(context.Background(), audit.Event{Action: "host.delete", Actor: &audit.System, TargetType: "host", TargetID: oldDB.ID,
		Details: map[string]any{"name": "old-db"}})
	if _, err := h.st.Writer.Exec(`DELETE FROM hosts WHERE id = ?`, oldDB.ID); err != nil {
		t.Fatal(err)
	}
	h.st.Writer.Exec(`UPDATE schedules SET attention = ? WHERE id = ?`, "The last run could not start: host "+oldDB.ID+" not found", updates)
	// The report: saving the schedule failed.
	if r := c.do("PUT", "/api/v1/schedules/"+updates, both); r.status != 422 || !strings.Contains(string(r.body), "host "+oldDB.ID+" not found") {
		t.Fatalf("before the repair: %d %s", r.status, r.body)
	}

	repair := func() ([]hostrefs.Removed, [][]hostrefs.Change) {
		t.Helper()
		var gone []hostrefs.Removed
		var changes [][]hostrefs.Change
		var emits []func()
		if err := h.st.InRawTx(context.Background(), func(tx *sql.Tx) error {
			var err error
			gone, changes, emits, err = hostrefs.RepairTx(context.Background(), tx, h.auditW, time.Now())
			return err
		}); err != nil {
			t.Fatal(err)
		}
		for _, e := range emits {
			e()
		}
		return gone, changes
	}
	gone, changes := repair()
	if len(gone) != 1 || gone[0].ID != oldDB.ID || gone[0].Name != "old-db" || len(changes[0]) != 3 {
		t.Fatalf("repair: %+v %+v", gone, changes)
	}
	if sc := getSchedule(t, c, updates); !slices.Equal(sc.Config.Targets.Hosts, []string{app.ID}) || !sc.Enabled || sc.Attention != "" {
		t.Fatalf("Patch both: %+v", sc)
	}
	if sc := getSchedule(t, c, backup); sc.Enabled || !strings.Contains(sc.Attention, "its only target, old-db, was removed") {
		t.Fatalf("Back up old-db: %+v", sc)
	}
	if r := c.do("GET", "/api/v1/firewall/policies/"+pol, nil); r.status != 404 {
		t.Fatalf("the policy whose only destination was old-db: %d %s", r.status, r.body)
	}
	var n int
	h.st.Reader.QueryRow(`SELECT count(*) FROM audit_events WHERE actor_type = 'system' AND details_json LIKE '%the host old-db was removed from DROOL%'`).Scan(&n)
	if n != 3 {
		t.Fatalf("%d audit records of the repair", n)
	}
	var by string
	h.st.Reader.QueryRow(`SELECT changed_by_name FROM schedule_revisions WHERE schedule_id = ? ORDER BY revision DESC LIMIT 1`, updates).Scan(&by)
	if by != "DROOL" {
		t.Fatalf("the repaired revision is by %q", by)
	}
	// Saving works again; a second start finds nothing.
	if r := c.do("PUT", "/api/v1/schedules/"+updates, weekly(trueStep, app.ID)); r.status != 200 {
		t.Fatalf("after the repair: %d %s", r.status, r.body)
	}
	if gone, _ := repair(); len(gone) != 0 {
		t.Fatalf("a second repair: %+v", gone)
	}
}

func getSchedule(t *testing.T, c *client, id string) sched.Schedule {
	t.Helper()
	var sc sched.Schedule
	r := c.do("GET", "/api/v1/schedules/"+id, nil)
	if r.status != 200 {
		t.Fatalf("GET schedule %s: %d %s", id, r.status, r.body)
	}
	r.json(t, &sc)
	return sc
}
