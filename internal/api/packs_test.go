package api

import (
	"maps"
	"slices"
	"strings"
	"testing"

	"drool/internal/auth"
)

// A Pack is an object of its own: it is created with a name alone, can stay
// empty, and its members are added and removed afterwards, one change at a
// time, from the Pack or from a host, without replacing anyone else's.
func TestPackMembership(t *testing.T) {
	h := newHarness(t)
	h.mkUser("admin", auth.RoleAdmin)
	h.mkUser("viewer", auth.RoleViewer)
	c := h.login("admin")
	var a, b hostJSON
	c.do("POST", "/api/v1/hosts", map[string]any{"name": "media-01", "address": "192.0.2.21", "port": 22, "user": "deploy"}).json(t, &a)
	c.do("POST", "/api/v1/hosts", map[string]any{"name": "media-02", "address": "192.0.2.22", "port": 22, "user": "deploy"}).json(t, &b)

	// Created empty, with a description.
	var g groupJSON
	r := c.do("POST", "/api/v1/groups", map[string]any{"name": "media-servers", "description": "Jellyfin and the NAS"})
	r.json(t, &g)
	if r.status != 201 || g.HostIDs == nil || len(g.HostIDs) != 0 || g.Description != "Jellyfin and the NAS" {
		t.Fatalf("create an empty pack: %d %s", r.status, r.body)
	}
	if !strings.Contains(string(c.do("GET", "/api/v1/groups/"+g.ID, nil).body), `"host_ids":[]`) {
		t.Fatal("an empty pack lists no hosts as []")
	}

	members := func(r resp) []string {
		t.Helper()
		var out groupJSON
		if r.status != 200 {
			t.Fatalf("%d %s", r.status, r.body)
		}
		r.json(t, &out)
		slices.Sort(out.HostIDs)
		return out.HostIDs
	}
	both := []string{a.ID, b.ID}
	slices.Sort(both)
	if got := members(c.do("POST", "/api/v1/groups/"+g.ID+"/members", map[string]any{"host_ids": []string{a.ID, b.ID}})); !slices.Equal(got, both) {
		t.Fatalf("added: %v", got)
	}
	// Adding a member again changes nothing.
	if got := members(c.do("POST", "/api/v1/groups/"+g.ID+"/members", map[string]any{"host_ids": []string{a.ID}})); !slices.Equal(got, both) {
		t.Fatalf("added again: %v", got)
	}
	// The host sees it too.
	var ha hostJSON
	c.do("GET", "/api/v1/hosts/"+a.ID, nil).json(t, &ha)
	if !slices.Contains(ha.GroupIDs, g.ID) {
		t.Fatalf("media-01's packs: %v", ha.GroupIDs)
	}

	// Removing a host keeps the pack and the other members; the last one
	// leaves an empty pack, not a deleted one.
	if got := members(c.do("DELETE", "/api/v1/groups/"+g.ID+"/members/"+a.ID, nil)); !slices.Equal(got, []string{b.ID}) {
		t.Fatalf("after removing media-01: %v", got)
	}
	if got := members(c.do("DELETE", "/api/v1/groups/"+g.ID+"/members/"+a.ID, nil)); !slices.Equal(got, []string{b.ID}) {
		t.Fatalf("removing it again: %v", got)
	}
	if got := members(c.do("DELETE", "/api/v1/groups/"+g.ID+"/members/"+b.ID, nil)); len(got) != 0 {
		t.Fatalf("after removing the last host: %v", got)
	}
	if r := c.do("GET", "/api/v1/groups/"+g.ID, nil); r.status != 200 {
		t.Fatalf("the empty pack is gone: %d", r.status)
	}
	if r := c.do("GET", "/api/v1/hosts/"+a.ID, nil); r.status != 200 {
		t.Fatalf("the host is gone: %d", r.status)
	}

	// Refusals.
	if r := c.do("POST", "/api/v1/groups/"+g.ID+"/members", map[string]any{"host_ids": []string{"hst_unknown"}}); r.status != 422 {
		t.Fatalf("an unknown host: %d %s", r.status, r.body)
	}
	if r := c.do("POST", "/api/v1/groups/"+g.ID+"/members", map[string]any{"host_ids": []string{}}); r.status != 422 {
		t.Fatalf("no hosts: %d %s", r.status, r.body)
	}
	if r := c.do("POST", "/api/v1/groups/grp_unknown/members", map[string]any{"host_ids": []string{a.ID}}); r.status != 404 {
		t.Fatalf("an unknown pack: %d %s", r.status, r.body)
	}
	if r := c.do("DELETE", "/api/v1/groups/grp_unknown/members/"+a.ID, nil); r.status != 404 {
		t.Fatalf("removing from an unknown pack: %d %s", r.status, r.body)
	}
	v := h.login("viewer")
	if r := v.do("POST", "/api/v1/groups/"+g.ID+"/members", map[string]any{"host_ids": []string{a.ID}}); r.status != 403 {
		t.Fatalf("a viewer adding: %d", r.status)
	}
	if r := v.do("DELETE", "/api/v1/groups/"+g.ID+"/members/"+a.ID, nil); r.status != 403 {
		t.Fatalf("a viewer removing: %d", r.status)
	}

	if n := countAudit(t, h, "group.members.add"); n != 2 {
		t.Errorf("%d group.members.add audit events", n)
	}
	if n := countAudit(t, h, "group.members.remove"); n != 3 {
		t.Errorf("%d group.members.remove audit events", n)
	}
}

// Deleting a Pack (final After-M17 batch): every subsystem that can name a
// Pack is asked first, in the deleting transaction. While a firewall policy,
// a schedule, a notification route, an active maintenance window, a Desired
// State assignment, a monitoring setting or a live enrollment token names it,
// the deletion is refused with the list, and nothing is deleted or broken;
// once they are changed, it goes, with what is harmless to drop (its
// memberships, priorities, firewall rollback timeout, helper approvals), and
// enrollment token history stays. Hosts are never deleted.
func TestPackDeletionDependencies(t *testing.T) {
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
	var host hostJSON
	must(201, "POST", "/api/v1/hosts", map[string]any{"name": "media-01", "address": "192.0.2.21", "port": 22, "user": "deploy"}).json(t, &host)
	g := idOf(must(201, "POST", "/api/v1/groups", map[string]any{"name": "media-servers"}))
	must(200, "POST", "/api/v1/groups/"+g+"/members", map[string]any{"host_ids": []string{host.ID}})

	// An unused Pack lists nothing and could go at once.
	var deps packDeps
	must(200, "GET", "/api/v1/groups/"+g+"/dependencies", nil).json(t, &deps)
	if len(deps.Blocking) != 0 || deps.Members != 1 || len(deps.Removed) != 1 || deps.Removed[0].Kind != "membership" {
		t.Fatalf("unused: %+v", deps)
	}

	// Everything that can name it.
	policy := idOf(must(201, "POST", "/api/v1/firewall/policies", map[string]any{"name": "media in", "action": "allow",
		"from": []any{map[string]any{"kind": "any"}}, "to": []any{map[string]any{"kind": "group", "id": g}}, "services": []string{"ssh"}}))
	schedule := idOf(must(201, "POST", "/api/v1/schedules", map[string]any{"name": "Nightly", "time_zone": "UTC",
		"recurrence": map[string]any{"kind": "daily", "time": "03:00"}, "targets": map[string]any{"groups": []string{g}},
		"steps": []map[string]any{{"type": "command", "command": "true"}}}))
	route := idOf(must(201, "POST", "/api/v1/notifications/routes", map[string]any{"name": "media pager", "group_ids": []string{g}}))
	maint := idOf(must(201, "POST", "/api/v1/maintenance", map[string]any{"group_ids": []string{g}, "reason": "disk swap", "duration_minutes": 60}))
	baseline := idOf(must(201, "POST", "/api/v1/desired/baselines", map[string]any{"name": "media",
		"resources":   []map[string]any{{"kind": "package", "name": "telnet", "state": "absent"}},
		"assignments": []map[string]any{{"scope": "group", "group_id": g}}}))
	var ov struct {
		Override struct {
			ID string `json:"id"`
		} `json:"override"`
	}
	must(200, "PUT", "/api/v1/monitoring/health/overrides", map[string]any{"scope": "group", "group_id": g, "check": "cpu",
		"fields": map[string]any{"warning": 80}}).json(t, &ov)
	token := idOf(must(201, "POST", "/api/v1/agents/tokens", map[string]any{"group_id": g, "preset": "monitor", "description": "lab install"}))
	used := idOf(must(201, "POST", "/api/v1/agents/tokens", map[string]any{"group_id": g, "preset": "monitor", "description": "old install"}))
	must(204, "POST", "/api/v1/agents/tokens/"+used+"/revoke", nil)
	// ...and what goes with it.
	must(200, "PUT", "/api/v1/monitoring/health/groups/"+g+"/priority", map[string]any{"priority": 5})
	must(204, "PUT", "/api/v1/firewall/groups/"+g, map[string]any{"rollback_timeout_s": 300})
	must(201, "POST", "/api/v1/monitoring/approvals", map[string]any{"scope": "group", "group_id": g, "package": "smartmontools"})

	must(200, "GET", "/api/v1/groups/"+g+"/dependencies", nil).json(t, &deps)
	kinds := map[string]string{}
	for _, b := range deps.Blocking {
		kinds[b.Kind] = b.ID
		if b.Detail == "" || b.Path == "" || b.Name == "" {
			t.Fatalf("a reference without words or a page: %+v", b)
		}
	}
	want := map[string]string{"firewall_policy": policy, "schedule": schedule, "notification_route": route, "maintenance": maint,
		"desired_assignment": baseline, "health_override": ov.Override.ID, "enrollment_token": token}
	if !maps.Equal(kinds, want) {
		t.Fatalf("blocking: got %v, want %v", kinds, want)
	}
	removed := map[string]bool{}
	for _, x := range deps.Removed {
		removed[x.Kind] = true
	}
	for _, k := range []string{"membership", "monitoring_priority", "firewall_timeout", "helper_approvals", "token_history"} {
		if !removed[k] {
			t.Fatalf("%s isn't listed as going with the pack: %+v", k, deps.Removed)
		}
	}

	// Refused while in use: nothing deleted, nothing changed.
	r := c.do("DELETE", "/api/v1/groups/"+g, nil)
	var p Problem
	r.json(t, &p)
	if r.status != 409 || p.Code != "pack_in_use" || p.Dependencies == nil || len(p.Dependencies.Blocking) != 7 ||
		!strings.Contains(p.Detail, "media-servers is still in use (7 references") {
		t.Fatalf("delete in use: %d %s", r.status, r.body)
	}
	must(200, "GET", "/api/v1/groups/"+g, nil)
	var deletes int
	h.st.Reader.QueryRow(`SELECT count(*) FROM audit_events WHERE action = 'group.delete'`).Scan(&deletes)
	if deletes != 0 {
		t.Fatal("a refused deletion was audited as a deletion")
	}

	// Changing them one by one shrinks the list.
	fix := []struct{ method, path string }{
		{"DELETE", "/api/v1/firewall/policies/" + policy},
		{"DELETE", "/api/v1/schedules/" + schedule},
		{"DELETE", "/api/v1/notifications/routes/" + route},
		{"POST", "/api/v1/maintenance/" + maint + "/end"},
		{"DELETE", "/api/v1/desired/baselines/" + baseline},
		{"DELETE", "/api/v1/monitoring/health/overrides/" + ov.Override.ID},
		{"POST", "/api/v1/agents/tokens/" + token + "/revoke"},
	}
	for i, f := range fix {
		if r := c.do(f.method, f.path, nil); r.status >= 300 {
			t.Fatalf("%s %s: %d %s", f.method, f.path, r.status, r.body)
		}
		must(200, "GET", "/api/v1/groups/"+g+"/dependencies", nil).json(t, &deps)
		if len(deps.Blocking) != len(fix)-i-1 {
			t.Fatalf("after %s %s: %d left: %+v", f.method, f.path, len(deps.Blocking), deps.Blocking)
		}
	}

	// Now it goes; the host stays; nothing names it any more.
	must(204, "DELETE", "/api/v1/groups/"+g, nil)
	must(404, "GET", "/api/v1/groups/"+g, nil)
	must(200, "GET", "/api/v1/hosts/"+host.ID, nil)
	for _, q := range []string{
		`SELECT count(*) FROM group_members WHERE group_id = ?`, `SELECT count(*) FROM mon_group_health WHERE group_id = ?`,
		`SELECT count(*) FROM fw_group_config WHERE group_id = ?`, `SELECT count(*) FROM mon_helper_approvals WHERE group_id = ?`,
		`SELECT count(*) FROM agent_enrollment_tokens WHERE group_id = ?`, `SELECT count(*) FROM mon_health_overrides WHERE group_id = ?`,
		`SELECT count(*) FROM ds_assignments WHERE group_id = ?`,
	} {
		var n int
		h.st.Reader.QueryRow(q, g).Scan(&n)
		if n != 0 {
			t.Fatalf("%s: %d left", q, n)
		}
	}
	var tokens int
	h.st.Reader.QueryRow(`SELECT count(*) FROM agent_enrollment_tokens WHERE id IN (?, ?)`, token, used).Scan(&tokens)
	if tokens != 2 {
		t.Fatalf("token history: %d of 2 kept", tokens)
	}
	var details string
	h.st.Reader.QueryRow(`SELECT details_json FROM audit_events WHERE action = 'group.delete' ORDER BY rowid DESC LIMIT 1`).Scan(&details)
	if !strings.Contains(details, `"name":"media-servers"`) || !strings.Contains(details, "leaves the Pack") || !strings.Contains(details, "smartmontools") {
		t.Fatalf("audit: %s", details)
	}
}

type packDeps struct {
	Members  int `json:"members"`
	Blocking []struct {
		Kind, ID, Name, Detail, Path string
	} `json:"blocking"`
	Removed []struct {
		Kind   string `json:"kind"`
		Count  int    `json:"count"`
		Detail string `json:"detail"`
	} `json:"removed"`
}
