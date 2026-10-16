package api

import (
	"testing"

	"drool/internal/auth"
	"drool/internal/model"
)

// TestHostBusy: what holds a host's lock, in words — a run by its name,
// the other users by what they do — and nothing once they let go.
func TestHostBusy(t *testing.T) {
	h := newHarness(t)
	h.mkUser("viewer", auth.RoleViewer)
	h.mkUser("admin", auth.RoleAdmin)
	c := h.login("admin")
	var host hostJSON
	c.do("POST", "/api/v1/hosts", map[string]any{"name": "busy-01", "address": "192.0.2.40", "port": 22, "user": "deploy"}).json(t, &host)
	get := func(cl *client) hostBusyJSON {
		t.Helper()
		r := cl.do("GET", "/api/v1/hosts/"+host.ID+"/busy", nil)
		if r.status != 200 {
			t.Fatalf("busy: %d %s", r.status, r.body)
		}
		var out hostBusyJSON
		r.json(t, &out)
		return out
	}
	if b := get(c); b.Busy || len(b.Holders) != 0 {
		t.Fatalf("idle host: %+v", b)
	}
	// A Shepherd install holds it.
	release, ok := h.eng.Locks().TryAcquire(host.ID, "agent-install", model.LockExclusive)
	if !ok {
		t.Fatal("lock")
	}
	b := get(h.login("viewer"))
	if !b.Busy || len(b.Holders) != 1 || b.Holders[0].Kind != "shepherd_install" || b.Holders[0].Label != "Installing a Shepherd" {
		t.Fatalf("installing: %+v", b)
	}
	release()
	// A run, named.
	var adminID string
	if err := h.st.Reader.QueryRow(`SELECT id FROM users WHERE username = 'admin'`).Scan(&adminID); err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.Writer.Exec(`INSERT INTO jobs (id, name, spec_json, spec_version, status, created_by, created_at) VALUES ('job_busy1', 'Weekly updates', '{}', 1, 'running', ?, 0)`, adminID); err != nil {
		t.Fatal(err)
	}
	release, _ = h.eng.Locks().TryAcquire(host.ID, "job_busy1", model.LockExclusive)
	if b := get(c); !b.Busy || b.Holders[0].Kind != "run" || b.Holders[0].JobID != "job_busy1" || b.Holders[0].Label != "The run “Weekly updates”" {
		t.Fatalf("a run: %+v", b)
	}
	release()
	if b := get(c); b.Busy {
		t.Fatalf("released: %+v", b)
	}
	if r := c.do("GET", "/api/v1/hosts/host_missing/busy", nil); r.status != 404 {
		t.Fatalf("a missing host: %d", r.status)
	}
}
