package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"drool/internal/auth"
)

// TestSearch: the navigation search finds hosts (name, address, host name,
// tag, interface address), Packs, services with their host, open alerts by
// words of their title/check/category ("disk full") and runs; every word
// must match; each kind is bounded and searched only with its permission.
func TestSearch(t *testing.T) {
	h := newHarness(t)
	h.mkUser("admin", auth.RoleAdmin)
	h.mkUser("viewer", auth.RoleViewer)
	c := h.login("admin")
	var media, web hostJSON
	c.do("POST", "/api/v1/hosts", map[string]any{"name": "media-01", "address": "192.0.2.21", "port": 22, "user": "deploy",
		"tags": []string{"media", "debian"}, "description": "Jellyfin box"}).json(t, &media)
	c.do("POST", "/api/v1/hosts", map[string]any{"name": "web-01", "address": "web.example.lan", "port": 22, "user": "deploy", "tags": []string{"web"}}).json(t, &web)
	if media.ID == "" || web.ID == "" {
		t.Fatal("hosts not created")
	}
	var g groupJSON
	c.do("POST", "/api/v1/groups", map[string]any{"name": "media-servers", "description": "Jellyfin and the NAS"}).json(t, &g)
	ctx := context.Background()
	now := time.Now().UnixMilli()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := h.st.Writer.ExecContext(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	// web-01's own host name and a second interface address, as discovery stores them.
	facts, _ := json.Marshal(map[string]any{"interfaces": []any{map[string]any{"name": "eth1", "addresses": []any{
		map[string]any{"address": "198.51.100.20", "prefix": 24, "family": 4}}}}, "ports": []any{map[string]any{"proto": "tcp", "address": "127.0.0.1", "port": 5432}}})
	exec(`INSERT INTO host_facts (host_id, collected_at, hostname, distro_name, facts_json) VALUES (?, ?, 'www-prod', 'Ubuntu 24.04 LTS', ?)`, web.ID, now, string(facts))
	exec(`INSERT INTO mon_services (host_id, unit, description, active_state, sub_state, changed_at) VALUES
		(?, 'jellyfin.service', 'Jellyfin Media Server', 'failed', 'failed', ?),
		(?, 'ssh.service', 'OpenBSD Secure Shell server', 'active', 'running', ?),
		(?, 'ssh.service', 'OpenBSD Secure Shell server', 'active', 'running', ?)`, media.ID, now, media.ID, now, web.ID, now)
	alert := func(id, host, hostName, check, category, subject, severity, state, title string) {
		exec(`INSERT INTO alerts (id, host_id, host_name, issue_id, check_id, subject, category, severity, peak_severity, state, title,
			issue_started_at, opened_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, host, hostName, "iss_"+id, check, subject, category, severity, severity, state, title, now, now, now)
	}
	alert("alr_disk", web.ID, "web-01", "filesystem", "disk", "/", "warning", "active", "/ is over 85% full")
	alert("alr_jelly", media.ID, "media-01", "service_failed", "services", "jellyfin.service", "critical", "acknowledged", "jellyfin.service failed")
	alert("alr_old", media.ID, "media-01", "filesystem", "disk", "/srv", "critical", "resolved", "/srv is over 95% full")
	var adminID string
	if err := h.st.Reader.QueryRowContext(ctx, `SELECT id FROM users WHERE username = 'admin'`).Scan(&adminID); err != nil {
		t.Fatal(err)
	}
	for i := range 3 {
		exec(`INSERT INTO jobs (id, name, spec_json, spec_version, status, created_by, created_at) VALUES (?, ?, '{}', 1, 'completed', ?, ?)`,
			fmt.Sprintf("job_upd%d", i), fmt.Sprintf("Weekly update %d", i), adminID, now+int64(i))
	}

	type result = searchResult
	search := func(cl *client, q string, extra ...string) searchResponse {
		t.Helper()
		path := "/api/v1/search?q=" + url.QueryEscape(q) + strings.Join(extra, "")
		r := cl.do("GET", path, nil)
		if r.status != 200 {
			t.Fatalf("%s: %d %s", q, r.status, r.body)
		}
		var out searchResponse
		r.json(t, &out)
		return out
	}
	find := func(res searchResponse, kind string) []result {
		var out []result
		for _, x := range res.Results {
			if x.Kind == kind {
				out = append(out, x)
			}
		}
		return out
	}
	titles := func(rs []result) string {
		var s []string
		for _, r := range rs {
			s = append(s, r.Title)
		}
		return strings.Join(s, ",")
	}

	// Hosts: by name, address, tag, the machine's own host name, an interface address.
	for q, want := range map[string]string{
		"media-01": "media-01", "192.0.2.21": "media-01", "debian": "media-01", "jellyfin box": "media-01",
		"www-prod": "web-01", "198.51.100.20": "web-01", "web.example": "web-01",
	} {
		if got := titles(find(search(c, q), "host")); got != want {
			t.Errorf("hosts for %q: %q, want %q", q, got, want)
		}
	}
	if m := find(search(c, "198.51.100.20"), "host")[0].Match; m != "interface address 198.51.100.20" {
		t.Errorf("why web-01 matched: %q", m)
	}
	if m := find(search(c, "debian"), "host")[0].Match; m != "tag debian" {
		t.Errorf("why media-01 matched: %q", m)
	}
	// Loopback isn't an address that tells hosts apart.
	if got := find(search(c, "127.0.0.1"), "host"); len(got) != 0 {
		t.Errorf("loopback found hosts: %+v", got)
	}
	// Packs.
	if got := titles(find(search(c, "media"), "pack")); got != "media-servers" {
		t.Errorf("packs: %q", got)
	}
	// Services, with their host; a host's name alone isn't a service search.
	svc := find(search(c, "jellyfin"), "service")
	if len(svc) != 1 || svc[0].HostID != media.ID || svc[0].HostName != "media-01" || svc[0].State != "failed" {
		t.Errorf("jellyfin service: %+v", svc)
	}
	if got := find(search(c, "ssh web-01"), "service"); len(got) != 1 || got[0].HostID != web.ID {
		t.Errorf("ssh on web-01: %+v", got)
	}
	if got := find(search(c, "media-01"), "service"); len(got) != 0 {
		t.Errorf("a host name matched its services: %+v", got)
	}
	// Open alerts by words of their title, check or category; resolved ones aren't searched.
	if got := find(search(c, "disk full"), "alert"); len(got) != 1 || got[0].ID != "alr_disk" || got[0].HostName != "web-01" {
		t.Errorf("disk full: %+v", got)
	}
	if got := find(search(c, "jellyfin"), "alert"); len(got) != 1 || !strings.Contains(got[0].Detail, "acknowledged") {
		t.Errorf("acknowledged alert: %+v", got)
	}
	if got := find(search(c, "/srv"), "alert"); len(got) != 0 {
		t.Errorf("a resolved alert was found: %+v", got)
	}
	// Runs, newest first, bounded: one more than the limit marks the kind as having more.
	res := search(c, "weekly update", "&limit=2")
	if got := titles(find(res, "run")); got != "Weekly update 2,Weekly update 1" || !strings.Contains(strings.Join(res.More, ","), "run") {
		t.Errorf("runs: %q more %v", got, res.More)
	}
	// Every word must match; LIKE wildcards in the query are literal.
	if got := search(c, "media nonsense"); len(got.Results) != 0 {
		t.Errorf("a partial match: %+v", got.Results)
	}
	// ("%%" as a wildcard matches everything; "a_d" would match "and" in
	// the pack's description.)
	if got := search(c, "%%"); len(got.Results) != 0 {
		t.Errorf("%%%% matched as a wildcard: %+v", got.Results)
	}
	if got := search(c, "a_d"); len(got.Results) != 0 {
		t.Errorf("_ matched as a wildcard: %+v", got.Results)
	}
	if got := find(search(c, "85%"), "alert"); len(got) != 1 {
		t.Errorf("a literal %% in a title: %+v", got)
	}
	// Validation.
	for _, bad := range []string{"", "?q=" + strings.Repeat("x", 101), "?q=x&limit=0", "?q=x&limit=11"} {
		path := "/api/v1/search" + bad
		if bad == "" {
			path += "?q="
		}
		if r := c.do("GET", path, nil); r.status != 422 {
			t.Errorf("%s: %d", path, r.status)
		}
	}
	// A viewer reads all four kinds; unauthenticated callers nothing.
	if got := search(h.login("viewer"), "jellyfin"); len(find(got, "service")) != 1 || len(find(got, "alert")) != 1 {
		t.Errorf("viewer: %+v", got.Results)
	}
	if r := (&client{h: h}).do("GET", "/api/v1/search?q=media", nil); r.status != 401 {
		t.Errorf("unauthenticated: %d", r.status)
	}
	// Each kind needs its own permission: a principal without any finds nothing.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/search?q=media", nil)
	h.srv.handleSearch(rec, req.WithContext(withPrincipal(req.Context(), &auth.Principal{Kind: auth.KindSession, Role: "none"})))
	var none searchResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &none); err != nil || rec.Code != 200 || len(none.Results) != 0 {
		t.Errorf("no permissions: %d %s", rec.Code, rec.Body)
	}
}
