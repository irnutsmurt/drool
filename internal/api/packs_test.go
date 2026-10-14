package api

import (
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
