package api

import (
	"context"
	"slices"
	"strings"
	"testing"

	"drool/internal/auth"
	"drool/internal/store/db"
)

// Setup-only credentials over a host's life (final After-M17 batch): a
// password typed in Add host stays while the host still signs in with it
// (an abandoned or failed setup can be finished later), and goes when the
// host is removed or signs in another way, unless another host (a jump host
// included) uses it. Stored credentials are never deleted for having been
// used, and DROOL's start sweeps temporary ones no host uses. Every deletion
// is audited, never with a value.
func TestSetupCredentialLifecycle(t *testing.T) {
	h := newHarness(t)
	h.mkUser("admin", auth.RoleAdmin)
	c := h.login("admin")
	const loginPW, sudoPW = "the-setup-login-password", "the-setup-sudo-password"
	add := func(name string, extra map[string]any) hostJSON {
		t.Helper()
		body := map[string]any{"name": name, "address": "192.0.2.10", "port": 22, "user": "alex"}
		for k, v := range extra {
			body[k] = v
		}
		r := c.do("POST", "/api/v1/hosts", body)
		if r.status != 201 {
			t.Fatalf("add %s: %d %s", name, r.status, r.body)
		}
		var out hostJSON
		r.json(t, &out)
		return out
	}
	setup := map[string]any{"setup_credentials": map[string]any{"ssh_password": loginPW, "sudo_password": sudoPW}}
	creds := func() map[string]secretJSON {
		t.Helper()
		var list listResponse[secretJSON]
		c.do("GET", "/api/v1/secrets", nil).json(t, &list)
		out := map[string]secretJSON{}
		for _, s := range list.Items {
			out[s.Name] = s
		}
		return out
	}
	has := func(name string) bool { _, ok := creds()[name]; return ok }

	// Abandoned: added with setup details, never set up. They stay, marked
	// as setup-only and used by the host, and DROOL's start keeps them.
	web := add("web-01", setup)
	cs := creds()
	if !cs["setup-web-01-login"].Temporary || !cs["setup-web-01-sudo"].Temporary ||
		len(cs["setup-web-01-login"].UsedBy) != 1 || cs["setup-web-01-login"].UsedBy[0].Name != "web-01" {
		t.Fatalf("abandoned setup: %+v", cs)
	}
	sweep := func() []string {
		t.Helper()
		var names []string
		if err := h.st.InTx(context.Background(), func(q *db.Queries) error {
			forgot, err := h.srv.d.Secrets.ForgetUnusedTemporaryTx(context.Background(), q)
			for _, f := range forgot {
				names = append(names, f.Name)
			}
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return names
	}
	if got := sweep(); len(got) != 0 {
		t.Fatalf("the start swept a setup in progress: %v", got)
	}

	// Removing the half-added host takes its setup-only credentials along.
	if r := c.do("DELETE", "/api/v1/hosts/"+web.ID, nil); r.status != 204 {
		t.Fatalf("remove: %d %s", r.status, r.body)
	}
	if has("setup-web-01-login") || has("setup-web-01-sudo") {
		t.Fatalf("left behind after removing the host: %+v", creds())
	}
	assertAuditLike(t, h, "host.delete", `"setup_credentials_deleted":["setup-web-01-login","setup-web-01-sudo"]`)
	assertAuditLike(t, h, "credential.delete", `"name":"setup-web-01-login"`, `"temporary":true`, `it was removed from DROOL`)

	// Shared: another host signs in with the same setup-only credential (a
	// jump host, here): removing the first one keeps it.
	jump := add("jump-01", setup)
	app := add("app-01", map[string]any{"setup_credentials": map[string]any{"ssh_password": loginPW}})
	if r := c.do("PATCH", "/api/v1/hosts/"+app.ID, map[string]any{"via_host_id": jump.ID, "credential_id": jump.CredentialID}); r.status != 200 {
		t.Fatalf("share: %d %s", r.status, r.body)
	}
	// app-01's own setup login went when it switched to jump-01's.
	if has("setup-app-01-login") {
		t.Fatal("app-01's own setup credential stayed after it stopped using it")
	}
	if r := c.do("PATCH", "/api/v1/hosts/"+app.ID, map[string]any{"via_host_id": ""}); r.status != 200 {
		t.Fatalf("direct again: %d %s", r.status, r.body)
	}
	if r := c.do("DELETE", "/api/v1/hosts/"+app.ID, nil); r.status != 204 {
		t.Fatalf("remove app-01: %d %s", r.status, r.body)
	}
	if !has("setup-jump-01-login") || !has("setup-jump-01-sudo") {
		t.Fatalf("a credential the jump host still uses was deleted: %+v", creds())
	}

	// A stored credential is never deleted for having been used.
	var stored struct {
		ID string `json:"id"`
	}
	c.do("POST", "/api/v1/secrets", map[string]any{"name": "ops-password", "kind": "password", "value": "a stored password"}).json(t, &stored)
	nas := add("nas-01", map[string]any{"credential_id": stored.ID})
	if r := c.do("DELETE", "/api/v1/hosts/"+nas.ID, nil); r.status != 204 {
		t.Fatalf("remove nas-01: %d %s", r.status, r.body)
	}
	if !has("ops-password") {
		t.Fatal("a stored credential went with a host")
	}

	// Signing in another way: switching jump-01 to the stored credential
	// forgets its setup login; its setup sudo password goes when that is
	// replaced too.
	if r := c.do("PATCH", "/api/v1/hosts/"+jump.ID, map[string]any{"credential_id": stored.ID}); r.status != 200 {
		t.Fatalf("switch credential: %d %s", r.status, r.body)
	}
	if has("setup-jump-01-login") || !has("setup-jump-01-sudo") {
		t.Fatalf("after switching the login: %+v", creds())
	}
	if r := c.do("PATCH", "/api/v1/hosts/"+jump.ID, map[string]any{"become_credential_id": ""}); r.status != 200 {
		t.Fatalf("drop the sudo password: %d %s", r.status, r.body)
	}
	if has("setup-jump-01-sudo") {
		t.Fatal("the setup sudo password stayed after the host stopped using it")
	}
	assertAuditLike(t, h, "host.update", `"setup_credentials_deleted":["setup-jump-01-sudo"]`)

	// Left behind before DROOL forgot them by itself (a host removed by an
	// older version): the start sweeps them, audited.
	orphan := add("old-01", setup)
	if _, err := h.st.Writer.Exec(`DELETE FROM hosts WHERE id = ?`, orphan.ID); err != nil {
		t.Fatal(err)
	}
	if got := sweep(); !slices.Equal(got, []string{"setup-old-01-login", "setup-old-01-sudo"}) {
		t.Fatalf("swept %v", got)
	}

	// No value anywhere in the audit log.
	var all string
	h.st.Reader.QueryRow(`SELECT group_concat(details_json, ' ') FROM audit_events`).Scan(&all)
	if strings.Contains(all, loginPW) || strings.Contains(all, sudoPW) || strings.Contains(all, "a stored password") {
		t.Fatal("a credential value reached the audit log")
	}
}

// assertAuditLike requires an audit record of action (any result) whose
// details contain every one of wants.
func assertAuditLike(t *testing.T, h *harness, action string, wants ...string) {
	t.Helper()
	rows, err := h.st.Reader.Query(`SELECT details_json FROM audit_events WHERE action = ?`, action)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var seen []string
	for rows.Next() {
		var d string
		rows.Scan(&d)
		seen = append(seen, d)
		ok := true
		for _, w := range wants {
			if !strings.Contains(d, w) {
				ok = false
			}
		}
		if ok {
			return
		}
	}
	t.Fatalf("no %s record with %v among %v", action, wants, seen)
}
