package api

import (
	"regexp"
	"sort"
	"strings"
	"testing"

	"drool/internal/auth"
)

// TestAuthzMatrix checks every route against every role (ARCHITECTURE-ALPHA
// §18): anonymous requests are rejected, roles without the permission get
// 403, roles with it get past authorization, state-changing session
// requests need the CSRF header, and session-only routes reject tokens.
func TestAuthzMatrix(t *testing.T) {
	h := newHarness(t)
	roles := []string{auth.RoleAdmin, auth.RoleOperator, auth.RoleAuditor, auth.RoleViewer}
	clients := map[string]*client{}
	for _, r := range roles {
		h.mkUser("u-"+r, r)
		clients[r] = h.login("u-" + r)
	}
	var tok createTokenResponse
	clients[auth.RoleAdmin].do("POST", "/api/v1/tokens", map[string]any{"name": "matrix"}).json(t, &tok)
	tokenClient := &client{h: h, bearer: tok.Token}
	anon := &client{h: h}
	param := regexp.MustCompile(`\{[^}]+\}`)

	routes := h.srv.routes()
	// Logging out ends the sessions the rest of the matrix uses: do it last.
	sort.SliceStable(routes, func(i, j int) bool {
		return !strings.HasSuffix(routes[i].path, "/auth/logout") && strings.HasSuffix(routes[j].path, "/auth/logout")
	})
	for _, rt := range routes {
		if rt.auth == public {
			continue
		}
		path := param.ReplaceAllString(rt.path, "x_nonexistent")
		if strings.HasSuffix(rt.path, "/events") {
			continue // SSE: authorization is covered by the jobs routes; the stream would block
		}
		name := rt.method + " " + rt.path
		var body any
		if isStateChanging(rt.method) {
			body = map[string]any{}
		}

		if isStateChanging(rt.method) {
			noCSRF := &client{h: h, cookie: clients[auth.RoleAdmin].cookie}
			if r := noCSRF.do(rt.method, path, body); r.status != 403 || !strings.Contains(string(r.body), "csrf") {
				t.Errorf("%s: session request without CSRF header got %d %s", name, r.status, r.body)
			}
		}
		if r := anon.do(rt.method, path, body); r.status != 401 {
			t.Errorf("%s: anonymous got %d, want 401", name, r.status)
		}
		for _, role := range roles {
			r := clients[role].do(rt.method, path, body)
			allowed := rt.perm == "" || auth.RoleHas(role, rt.perm)
			switch {
			case !allowed && (r.status != 403 || !strings.Contains(string(r.body), `"code":"forbidden"`)):
				t.Errorf("%s: %s got %d, want 403 (lacks %s)", name, role, r.status, rt.perm)
			case allowed && (r.status == 401 || (r.status == 403 && strings.Contains(string(r.body), `"code":"forbidden"`))):
				t.Errorf("%s: %s got %d, but has the permission: %s", name, role, r.status, r.body)
			}
		}
		if rt.sessionOnly {
			if r := tokenClient.do(rt.method, path, body); r.status != 403 {
				t.Errorf("%s: session-only route accepted an API token (%d)", name, r.status)
			}
		}
	}
}

func TestTokensAreScopedToTheirOwner(t *testing.T) {
	h := newHarness(t)
	h.mkUser("admin", auth.RoleAdmin)
	h.mkUser("op", auth.RoleOperator)
	a, op := h.login("admin"), h.login("op")
	var tok createTokenResponse
	a.do("POST", "/api/v1/tokens", map[string]any{"name": "admins"}).json(t, &tok)
	if r := op.do("DELETE", "/api/v1/tokens/"+tok.ID, nil); r.status == 204 {
		t.Fatal("an operator revoked the admin's token")
	}
	var list listResponse[tokenJSON]
	op.do("GET", "/api/v1/tokens", nil).json(t, &list)
	if len(list.Items) != 0 {
		t.Fatalf("operator sees other users' tokens: %+v", list.Items)
	}
	// A token acts with its owner's current role: demotion applies at once.
	tc := &client{h: h, bearer: tok.Token}
	if r := tc.do("GET", "/api/v1/audit", nil); r.status != 200 {
		t.Fatalf("admin token: %d", r.status)
	}
	var users listResponse[userJSON]
	a.do("GET", "/api/v1/users", nil).json(t, &users)
	for _, u := range users.Items {
		if u.Username == "admin" {
			h.mkUser("admin2", auth.RoleAdmin) // keep an admin
			a2 := h.login("admin2")
			if r := a2.do("PATCH", "/api/v1/users/"+u.ID, map[string]any{"role": "viewer"}); r.status != 200 {
				t.Fatalf("demote: %d %s", r.status, r.body)
			}
		}
	}
	if r := tc.do("GET", "/api/v1/audit", nil); r.status != 403 {
		t.Fatalf("token kept admin rights after its owner was demoted: %d", r.status)
	}
}

func TestControllerKeyIsProtected(t *testing.T) {
	h := newHarness(t)
	h.mkUser("admin", auth.RoleAdmin)
	a := h.login("admin")
	var k controllerKeyResponse
	a.do("GET", "/api/v1/secrets/controller-ssh-key", nil).json(t, &k)
	if r := a.do("DELETE", "/api/v1/secrets/"+k.CredentialID, nil); r.status != 409 || r.problemCode(t) != "controller_key" {
		t.Fatalf("deleting the controller key: %d %s", r.status, r.body)
	}
	if r := a.do("PATCH", "/api/v1/secrets/"+k.CredentialID, map[string]any{"name": "renamed"}); r.status != 409 {
		t.Fatalf("renaming the controller key: %d %s", r.status, r.body)
	}
	if r := a.do("PATCH", "/api/v1/secrets/"+k.CredentialID, map[string]any{"description": "ok"}); r.status != 200 {
		t.Fatalf("editing the description should work: %d %s", r.status, r.body)
	}
}
