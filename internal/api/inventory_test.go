package api

import (
	"strings"
	"testing"

	"drool/internal/auth"
)

// The test SSH server executes commands locally, so this runs the real
// discovery and status scripts end to end (results stay in the test DB).
func TestRefreshCollectsFactsAndStatus(t *testing.T) {
	h := newHarness(t)
	h.mkUser("admin", auth.RoleAdmin)
	c := h.login("admin")
	id := readyHost(t, h, c, "local")

	var ref refreshResponse
	r := c.do("POST", "/api/v1/hosts/"+id+"/refresh", nil)
	if r.status != 200 {
		t.Fatalf("refresh: %d %s", r.status, r.body)
	}
	r.json(t, &ref)
	host := ref.Host
	if host.Facts == nil || host.Facts.Kernel == "" || host.Facts.Arch == "" || host.Facts.OSFamily == "" {
		t.Fatalf("facts %+v", host.Facts)
	}
	if host.Health == nil || !host.Health.Reachable || host.Health.Stale || host.Health.UptimeS == nil {
		t.Fatalf("health %+v", host.Health)
	}
	var facts factsResponse
	c.do("GET", "/api/v1/hosts/"+id+"/facts", nil).json(t, &facts)
	if facts.CollectedAt == nil || !strings.Contains(string(facts.Facts), `"interfaces"`) {
		t.Fatalf("facts endpoint %s", facts.Facts)
	}
	discovered := 0
	for _, a := range facts.Addresses {
		if a.Source == "discovered" {
			discovered++
		}
	}
	if discovered == 0 {
		t.Fatalf("no discovered addresses: %+v", facts.Addresses)
	}

	// A host that a job holds exclusively is not refreshed (no waiting).
	release, _ := h.eng.Locks().TryAcquire(id, "job_test", 2)
	if r := c.do("POST", "/api/v1/hosts/"+id+"/refresh", nil); r.status != 409 {
		t.Fatalf("refresh while locked: %d", r.status)
	}
	release()

	// Preview annotates hosts a conditional step would skip.
	family := host.Facts.OSFamily
	other := "redhat"
	if family == "redhat" {
		other = "debian"
	}
	var preview previewResponse
	c.do("POST", "/api/v1/jobs/preview", map[string]any{
		"targets": map[string]any{"hosts": []string{id}},
		"steps":   []map[string]any{{"type": "command", "command": "true", "when": map[string]any{"os_family": []string{other}}}},
	}).json(t, &preview)
	if family != "unknown" && (len(preview.Hosts) != 1 || !preview.Hosts[0].Skip) {
		t.Fatalf("preview %+v", preview)
	}
}
