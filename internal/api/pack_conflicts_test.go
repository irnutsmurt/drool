package api

import (
	"testing"
)

type packConflictsResp struct {
	Checked []string `json:"checked"`
	Items   []struct {
		Area, Kind, What, Value, Applies, Key string
		HostName                              string `json:"host_name"`
		Others                                []struct {
			Name     string
			Priority int
			Value    string
		}
	} `json:"items"`
}

// Where a Pack's settings don't decide what a member host gets because of
// another Pack (ARCHITECTURE-ALPHA §47): the services' verdicts, gathered
// for one Pack, in words.
func TestPackConflicts(t *testing.T) {
	e := newDesiredAPI(t)
	pack := func(name string) string {
		t.Helper()
		var g struct{ ID string }
		e.admin.do("POST", "/api/v1/groups", map[string]any{"name": name}).json(t, &g)
		if r := e.admin.do("POST", "/api/v1/groups/"+g.ID+"/members", map[string]any{"host_ids": []string{e.hostA}}); r.status != 200 {
			t.Fatalf("members: %d %s", r.status, r.body)
		}
		return g.ID
	}
	get := func(id string) packConflictsResp {
		t.Helper()
		var c packConflictsResp
		r := e.viewer.do("GET", "/api/v1/groups/"+id+"/conflicts", nil)
		if r.status != 200 {
			t.Fatalf("conflicts: %d %s", r.status, r.body)
		}
		r.json(t, &c)
		return c
	}
	override := func(body map[string]any) {
		t.Helper()
		if r := e.admin.do("PUT", "/api/v1/monitoring/health/overrides", body); r.status != 200 {
			t.Fatalf("override: %d %s", r.status, r.body)
		}
	}
	media, backups := pack("media"), pack("backups")

	// Equal monitoring priority, different values: a conflict, for both.
	override(map[string]any{"scope": "group", "group_id": media, "check": "memory", "fields": map[string]any{"warning": 70}})
	override(map[string]any{"scope": "group", "group_id": backups, "check": "memory", "fields": map[string]any{"warning": 75}})
	c := get(media)
	if len(c.Checked) != 2 || len(c.Items) != 1 {
		t.Fatalf("one conflict: %+v", c)
	}
	it := c.Items[0]
	if it.Area != "monitoring" || it.Kind != "conflict" || it.HostName != "alpha" || it.What != "Memory pressure, warning at" || it.Value != "70%" ||
		len(it.Others) != 1 || it.Others[0].Name != "backups" || it.Others[0].Value != "75%" || it.Applies != "90% from Recommended" {
		t.Fatalf("the conflict in words: %+v", it)
	}
	if b := get(backups); len(b.Items) != 1 || b.Items[0].Value != "75%" || b.Items[0].Others[0].Name != "media" {
		t.Fatalf("the other Pack sees it too: %+v", b)
	}

	// A higher priority settles it: the other Pack is outranked there.
	if r := e.admin.do("PUT", "/api/v1/monitoring/health/groups/"+backups+"/priority", map[string]any{"priority": 5}); r.status != 200 {
		t.Fatalf("priority: %d %s", r.status, r.body)
	}
	c = get(media)
	if len(c.Items) != 1 || c.Items[0].Kind != "outranked" || c.Items[0].Value != "70%" || c.Items[0].Others[0].Name != "backups" ||
		c.Items[0].Others[0].Priority != 5 || c.Items[0].Applies != "75% from the Pack backups" {
		t.Fatalf("outranked: %+v", c)
	}
	if b := get(backups); len(b.Items) != 0 {
		t.Fatalf("the winning Pack has nothing to report: %+v", b)
	}

	// The host's own setting decides for it: nothing to report.
	override(map[string]any{"scope": "host", "host_id": e.hostA, "check": "memory", "fields": map[string]any{"warning": 80}})
	if c := get(media); len(c.Items) != 0 {
		t.Fatalf("a host's own setting isn't a Pack conflict: %+v", c)
	}

	// Desired State: two Packs' baselines disagree about one package at
	// equal priority.
	for _, b := range []struct{ pack, state string }{{media, "installed"}, {backups, "absent"}} {
		in := map[string]any{"name": "telnet " + b.state, "resources": []map[string]any{{"kind": "package", "name": "telnet", "state": b.state}},
			"assignments": []map[string]any{{"scope": "group", "group_id": b.pack}}}
		if r := e.admin.do("POST", "/api/v1/desired/baselines", in); r.status != 201 {
			t.Fatalf("baseline: %d %s", r.status, r.body)
		}
	}
	e.step()
	c = get(media)
	var found bool
	for _, x := range c.Items {
		if x.Area == "desired" {
			found = x.Kind == "conflict" && x.HostName == "alpha" && x.Key == "package:telnet" && len(x.Others) == 1 && x.Others[0].Name == "backups"
		}
	}
	if !found {
		t.Fatalf("the Desired State conflict: %+v", c)
	}

	if r := e.viewer.do("GET", "/api/v1/groups/grp_nope/conflicts", nil); r.status != 404 {
		t.Fatalf("an unknown Pack: %d", r.status)
	}
}

// A new Pack, still empty, has its health checks and monitoring priority at
// once (migration 0028): the monitoring configuration knew a Pack only once
// something else changed it, so its page said it couldn't load them.
func TestNewEmptyPackHasHealthChecks(t *testing.T) {
	e := newAlertEnv(t)
	var g struct{ ID string }
	e.c.do("POST", "/api/v1/groups", map[string]any{"name": "fresh"}).json(t, &g)
	if r := e.c.do("GET", "/api/v1/monitoring/health/groups/"+g.ID, nil); r.status != 200 {
		t.Fatalf("a new Pack's health checks: %d %s", r.status, r.body)
	}
	var list struct {
		Groups []struct {
			GroupID string `json:"group_id"`
			Name    string `json:"name"`
		} `json:"groups"`
	}
	e.c.do("GET", "/api/v1/monitoring/health/groups", nil).json(t, &list)
	found := false
	for _, x := range list.Groups {
		found = found || x.GroupID == g.ID
	}
	if !found {
		t.Fatalf("not among the Packs' monitoring priorities: %+v", list)
	}
	// Renamed: the new name at once.
	if r := e.c.do("PATCH", "/api/v1/groups/"+g.ID, map[string]any{"name": "renamed"}); r.status != 200 {
		t.Fatalf("rename: %d %s", r.status, r.body)
	}
	e.c.do("GET", "/api/v1/monitoring/health/groups", nil).json(t, &list)
	for _, x := range list.Groups {
		if x.GroupID == g.ID && x.Name != "renamed" {
			t.Fatalf("the old name: %+v", x)
		}
	}
}
