package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"gopkg.in/yaml.v3"

	"drool/internal/agentsvc"
	"drool/internal/artifacts"
	"drool/internal/audit"
	"drool/internal/auth"
	"drool/internal/config"
	"drool/internal/driver/sshdriver"
	"drool/internal/engine"
	"drool/internal/filedeploy"
	"drool/internal/firewall/fwsvc"
	"drool/internal/firewall/hostfw"
	"drool/internal/inventory"
	"drool/internal/logging"
	"drool/internal/mgmt"
	"drool/internal/netmon/netsvc"
	"drool/internal/secrets"
	"drool/internal/sshx"
	"drool/internal/store"
	"drool/internal/store/db"
)

const pw = "correct horse battery"

type harness struct {
	t    *testing.T
	eng  *engine.Engine
	srv  *Server
	ts   *httptest.Server
	auth *auth.Service
	st   *store.Store

	agents *agentsvc.Service
	pki    *agentsvc.PKI
	router *mgmt.Router
	fw     *fwsvc.Service
	// fwSSH is where the firewall tooling of SSH hosts lives in tests
	// (fake nft, systemd-run; see firewall_test.go), fwFake its fake kernel.
	fwSSH  hostfw.Paths
	fwFake string
	// net is network monitoring; geoDir its (initially empty) GeoIP
	// database directory.
	net    *netsvc.Service
	geoDir string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), store.FileName))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load("", false, []string{"DROOL_DATA_DIR=" + t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	logs := logging.Discard()
	authSvc := auth.NewService(st, auth.Options{IdleTimeout: time.Hour, AbsoluteTimeout: 24 * time.Hour})
	auditW := audit.NewWriter(st, logs.Audit)
	box := secrets.NewBoxForTest()
	secretSvc := secrets.NewService(st, box)
	if _, _, err := secretSvc.EnsureControllerKey(context.Background(), func(*db.Queries, db.Secret) error { return nil }); err != nil {
		t.Fatal(err)
	}
	dialer := &sshx.Dialer{DialTimeout: 3 * time.Second}
	pool := sshx.NewPool(dialer)
	t.Cleanup(pool.Close)
	inv := inventory.NewService(st, secretSvc, dialer, pool)
	arts, err := artifacts.Open(t.TempDir(), st)
	if err != nil {
		t.Fatal(err)
	}
	rec := &filedeploy.StoreRecorder{Store: st, Audit: auditW}
	executor := &sshdriver.Executor{Pool: pool, Files: &sshdriver.FileDeps{Source: arts, Recorder: rec}}
	pki, _, err := agentsvc.OpenPKI(context.Background(), st, box, "test")
	if err != nil {
		t.Fatal(err)
	}
	agents, err := agentsvc.New(context.Background(), agentsvc.Options{Config: cfg.Agent, Store: st, PKI: pki, Audit: auditW,
		Inventory: inv, Artifacts: arts, Recorder: rec, Log: logs.App})
	if err != nil {
		t.Fatal(err)
	}
	router := &mgmt.Router{St: st, Inv: inv, SSH: executor, Agents: agents}
	fwFake, fwSSH := fakeHostDir(t, "ssh")
	fw, err := fwsvc.New(context.Background(), fwsvc.Deps{Store: st, Inv: inv, Agents: agents, Audit: auditW, Log: logs.App,
		Decide: router.FirewallDecision, ToolPaths: &fwSSH})
	if err != nil {
		t.Fatal(err)
	}
	router.Firewall = fw
	agents.SetFirewallHooks(fw)
	fwCtx, fwCancel := context.WithCancel(context.Background())
	go fw.Run(fwCtx)
	t.Cleanup(fwCancel)
	geoDir := t.TempDir()
	network := netsvc.New(netsvc.Deps{Store: st, Firewall: fw, GeoIPDir: geoDir, Log: logs.App})
	eng := engine.New(st, inv, router, auditW, logs.Job, engine.Config{RetryDelay: 10 * time.Millisecond})
	if err := eng.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	srv, err := New(Deps{
		Config: cfg, Store: st, Auth: authSvc, Audit: auditW, Logs: logs,
		Secrets: secretSvc, Inventory: inv, Pool: pool, Engine: eng, Simulator: router, Files: router, Artifacts: arts,
		Agents: agents, Router: router, Firewall: fw, Network: network,
		UI: fstest.MapFS{
			"index.html":    {Data: []byte("<!doctype html><title>ui</title>")},
			"assets/app.js": {Data: []byte("console.log(1)")},
		},
		Registry: prometheus.NewRegistry(), TLSMode: "self_signed",
	})
	if err != nil {
		t.Fatal(err)
	}
	srv.SetReady(true)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return &harness{t: t, srv: srv, ts: ts, auth: authSvc, st: st, eng: eng, agents: agents, pki: pki, router: router,
		fw: fw, fwSSH: fwSSH, fwFake: fwFake, net: network, geoDir: geoDir}
}

func (h *harness) mkUser(name, role string) {
	h.t.Helper()
	if _, err := h.auth.CreateUser(context.Background(), name, pw, role); err != nil {
		h.t.Fatal(err)
	}
}

// client carries a session cookie and CSRF token, or a bearer token.
type client struct {
	h      *harness
	cookie string
	csrf   string
	bearer string
	extra  map[string]string
}

type resp struct {
	status int
	header http.Header
	body   []byte
}

func (r resp) json(t *testing.T, v any) {
	t.Helper()
	if err := json.Unmarshal(r.body, v); err != nil {
		t.Fatalf("decode %s: %v", r.body, err)
	}
}

func (r resp) problemCode(t *testing.T) string {
	t.Helper()
	var p Problem
	r.json(t, &p)
	return p.Code
}

func (c *client) do(method, path string, body any) resp {
	c.h.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, c.h.ts.URL+path, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.cookie != "" {
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: c.cookie})
	}
	if c.csrf != "" {
		req.Header.Set(csrfHeader, c.csrf)
	}
	if c.bearer != "" {
		req.Header.Set("Authorization", "Bearer "+c.bearer)
	}
	for k, v := range c.extra {
		req.Header.Set(k, v)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		c.h.t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return resp{status: res.StatusCode, header: res.Header, body: b}
}

// doHeader is do with one extra request header.
func (c *client) doHeader(method, path string, body any, key, value string) resp {
	c.h.t.Helper()
	saved := c.extra
	c.extra = map[string]string{key: value}
	defer func() { c.extra = saved }()
	return c.do(method, path, body)
}

func (h *harness) login(name string) *client {
	h.t.Helper()
	c := &client{h: h}
	r := c.do("POST", "/api/v1/auth/login", map[string]string{"username": name, "password": pw})
	if r.status != 200 {
		h.t.Fatalf("login %s: %d %s", name, r.status, r.body)
	}
	for _, sc := range (&http.Response{Header: r.header}).Cookies() {
		if sc.Name == sessionCookie {
			c.cookie = sc.Value
			if !sc.HttpOnly || !sc.Secure || sc.SameSite != http.SameSiteStrictMode {
				h.t.Fatalf("session cookie flags wrong: %+v", sc)
			}
		}
	}
	var me meResponse
	r.json(h.t, &me)
	c.csrf = me.CSRFToken
	if c.cookie == "" || c.csrf == "" {
		h.t.Fatal("login did not return cookie and csrf token")
	}
	return c
}

func TestLoginMeLogout(t *testing.T) {
	h := newHarness(t)
	h.mkUser("admin", auth.RoleAdmin)
	anon := &client{h: h}

	if r := anon.do("GET", "/api/v1/auth/me", nil); r.status != 401 || r.problemCode(t) != "unauthenticated" {
		t.Fatalf("anonymous me: %d %s", r.status, r.body)
	}
	if r := anon.do("POST", "/api/v1/auth/login", map[string]string{"username": "admin", "password": "wrong password!"}); r.status != 401 {
		t.Fatalf("bad login: %d", r.status)
	}
	if r := anon.do("POST", "/api/v1/auth/login", map[string]any{"username": "admin", "password": pw, "extra": 1}); r.status != 400 {
		t.Fatalf("unknown field accepted: %d", r.status)
	}

	c := h.login("admin")
	r := c.do("GET", "/api/v1/auth/me", nil)
	var me meResponse
	r.json(t, &me)
	if r.status != 200 || me.User.Username != "admin" || me.AuthKind != "session" || len(me.Permissions) == 0 {
		t.Fatalf("me: %d %s", r.status, r.body)
	}
	if r.header.Get("X-Request-Id") == "" || r.header.Get("Cache-Control") != "no-store" ||
		r.header.Get("Content-Security-Policy") == "" {
		t.Fatalf("missing headers: %v", r.header)
	}

	// Logout without CSRF is refused; with CSRF it succeeds and kills the session.
	noCSRF := &client{h: h, cookie: c.cookie}
	if r := noCSRF.do("POST", "/api/v1/auth/logout", nil); r.status != 403 || r.problemCode(t) != "csrf" {
		t.Fatalf("logout without csrf: %d %s", r.status, r.body)
	}
	if r := c.do("POST", "/api/v1/auth/logout", nil); r.status != 204 {
		t.Fatalf("logout: %d", r.status)
	}
	if r := c.do("GET", "/api/v1/auth/me", nil); r.status != 401 {
		t.Fatalf("session survived logout: %d", r.status)
	}
}

func TestPermissions(t *testing.T) {
	h := newHarness(t)
	h.mkUser("admin", auth.RoleAdmin)
	h.mkUser("viewer", auth.RoleViewer)
	h.mkUser("auditor", auth.RoleAuditor)
	v := h.login("viewer")

	if r := v.do("GET", "/api/v1/users", nil); r.status != 403 {
		t.Fatalf("viewer listed users: %d", r.status)
	}
	if r := v.do("GET", "/api/v1/audit", nil); r.status != 403 {
		t.Fatalf("viewer read audit: %d", r.status)
	}
	var info systemInfo
	r := v.do("GET", "/api/v1/system/info", nil)
	r.json(t, &info)
	if r.status != 200 || info.Config != nil || info.SchemaVersion == 0 {
		t.Fatalf("viewer system info: %d %s", r.status, r.body)
	}

	a := h.login("auditor")
	r = a.do("GET", "/api/v1/system/info", nil)
	r.json(t, &info)
	if len(info.Config) == 0 {
		t.Fatal("auditor should see config sources")
	}
	r = a.do("GET", "/api/v1/audit?action=authz.", nil)
	var list listResponse[auditJSON]
	r.json(t, &list)
	if r.status != 200 || len(list.Items) == 0 || list.Items[0].Result != "denied" {
		t.Fatalf("authz denial not audited: %s", r.body)
	}
}

func TestUserManagement(t *testing.T) {
	h := newHarness(t)
	h.mkUser("admin", auth.RoleAdmin)
	c := h.login("admin")

	r := c.do("POST", "/api/v1/users", map[string]string{"username": "op", "password": pw, "role": "operator"})
	if r.status != 201 {
		t.Fatalf("create: %d %s", r.status, r.body)
	}
	var u userJSON
	r.json(t, &u)

	if r := c.do("POST", "/api/v1/users", map[string]string{"username": "op", "password": pw, "role": "operator"}); r.status != 409 {
		t.Fatalf("duplicate: %d", r.status)
	}
	if r := c.do("POST", "/api/v1/users", map[string]string{"username": "x", "password": "short", "role": "operator"}); r.status != 422 {
		t.Fatalf("weak password: %d", r.status)
	}
	if r := c.do("PATCH", "/api/v1/users/"+u.ID, map[string]any{"role": "viewer"}); r.status != 200 {
		t.Fatalf("update: %d %s", r.status, r.body)
	}
	var me meResponse
	c.do("GET", "/api/v1/auth/me", nil).json(t, &me)
	if r := c.do("PATCH", "/api/v1/users/"+me.User.ID, map[string]any{"disabled": true}); r.status != 409 {
		t.Fatalf("self-disable: %d", r.status)
	}
	if r := c.do("DELETE", "/api/v1/users/"+u.ID, nil); r.status != 204 {
		t.Fatalf("delete: %d", r.status)
	}
	if r := c.do("GET", "/api/v1/users/"+u.ID, nil); r.status != 404 {
		t.Fatalf("get deleted: %d", r.status)
	}

	var list listResponse[auditJSON]
	c.do("GET", "/api/v1/audit?action=user.", nil).json(t, &list)
	actions := map[string]bool{}
	for _, e := range list.Items {
		actions[e.Action+"/"+e.Result] = true
		if e.Actor.ID != me.User.ID && e.Result == "success" {
			t.Fatalf("audit actor wrong: %+v", e)
		}
	}
	for _, want := range []string{"user.create/success", "user.create/failure", "user.update/success", "user.delete/success"} {
		if !actions[want] {
			t.Fatalf("missing audit %s in %v", want, actions)
		}
	}
}

func TestMutationRolledBackWhenAuditFails(t *testing.T) {
	h := newHarness(t)
	h.mkUser("admin", auth.RoleAdmin)
	c := h.login("admin")
	if _, err := h.st.Writer.Exec(`CREATE TRIGGER fail_user_create_audit BEFORE INSERT ON audit_events
		WHEN NEW.action = 'user.create' AND NEW.result = 'success'
		BEGIN SELECT RAISE(ABORT, 'audit unavailable'); END`); err != nil {
		t.Fatal(err)
	}
	r := c.do("POST", "/api/v1/users", map[string]string{"username": "ghost", "password": pw, "role": "viewer"})
	if r.status != 500 {
		t.Fatalf("expected failure, got %d %s", r.status, r.body)
	}
	if _, err := h.st.R.GetUserByUsername(context.Background(), "ghost"); err == nil {
		t.Fatal("user was created even though its audit record failed")
	}
}

func TestAPITokenFlow(t *testing.T) {
	h := newHarness(t)
	h.mkUser("admin", auth.RoleAdmin)
	c := h.login("admin")

	r := c.do("POST", "/api/v1/tokens", map[string]any{"name": "ci", "expires_in_days": 30})
	if r.status != 201 {
		t.Fatalf("create token: %d %s", r.status, r.body)
	}
	var created createTokenResponse
	r.json(t, &created)

	tc := &client{h: h, bearer: created.Token}
	r = tc.do("GET", "/api/v1/auth/me", nil)
	var me meResponse
	r.json(t, &me)
	if r.status != 200 || me.AuthKind != "token" || me.CSRFToken != "" {
		t.Fatalf("token me: %d %s", r.status, r.body)
	}
	// Bearer requests don't need CSRF, but a token cannot mint more tokens.
	if r := tc.do("POST", "/api/v1/tokens", map[string]any{"name": "evil"}); r.status != 403 {
		t.Fatalf("token minted token: %d", r.status)
	}
	if r := tc.do("POST", "/api/v1/users", map[string]string{"username": "viatoken", "password": pw, "role": "viewer"}); r.status != 201 {
		t.Fatalf("token user create: %d %s", r.status, r.body)
	}

	var list listResponse[tokenJSON]
	c.do("GET", "/api/v1/tokens", nil).json(t, &list)
	if len(list.Items) != 1 || strings.Contains(string(c.do("GET", "/api/v1/tokens", nil).body), created.Token) {
		t.Fatal("token listing wrong or leaks secret")
	}
	if r := c.do("DELETE", "/api/v1/tokens/"+created.ID, nil); r.status != 204 {
		t.Fatalf("revoke: %d", r.status)
	}
	if r := tc.do("GET", "/api/v1/auth/me", nil); r.status != 401 {
		t.Fatalf("revoked token works: %d", r.status)
	}
	if r := (&client{h: h, bearer: "drool_garbage"}).do("GET", "/api/v1/auth/me", nil); r.status != 401 {
		t.Fatalf("garbage token: %d", r.status)
	}
}

func TestChangeOwnPassword(t *testing.T) {
	h := newHarness(t)
	h.mkUser("op", auth.RoleOperator)
	c := h.login("op")
	if r := c.do("POST", "/api/v1/auth/password", map[string]string{"current_password": "nope nope nope", "new_password": "a brand new passphrase"}); r.status != 403 {
		t.Fatalf("wrong current: %d", r.status)
	}
	if r := c.do("POST", "/api/v1/auth/password", map[string]string{"current_password": pw, "new_password": "a brand new passphrase"}); r.status != 204 {
		t.Fatalf("change: %d %s", r.status, r.body)
	}
	if r := c.do("GET", "/api/v1/auth/me", nil); r.status != 200 {
		t.Fatal("current session should survive own password change")
	}
}

func TestHealthReadyMetricsAndUI(t *testing.T) {
	h := newHarness(t)
	anon := &client{h: h}
	if r := anon.do("GET", "/health", nil); r.status != 200 || !strings.Contains(string(r.body), `"database":"ok"`) {
		t.Fatalf("health: %d %s", r.status, r.body)
	}
	if r := anon.do("GET", "/ready", nil); r.status != 200 {
		t.Fatalf("ready: %d", r.status)
	}
	h.srv.SetReady(false)
	if r := anon.do("GET", "/ready", nil); r.status != 503 {
		t.Fatalf("not-ready: %d", r.status)
	}
	if r := anon.do("GET", "/metrics", nil); r.status != 200 || !strings.Contains(string(r.body), "drool_http_requests_total") {
		t.Fatalf("metrics: %d", r.status)
	}
	for _, p := range []string{"/", "/hosts", "/settings/users"} {
		if r := anon.do("GET", p, nil); r.status != 200 || !strings.Contains(string(r.body), "<title>ui</title>") {
			t.Fatalf("SPA %s: %d", p, r.status)
		}
	}
	if r := anon.do("GET", "/assets/app.js", nil); r.status != 200 || !strings.Contains(r.header.Get("Cache-Control"), "immutable") {
		t.Fatalf("asset: %d %v", r.status, r.header)
	}
	if r := anon.do("GET", "/missing.js", nil); r.status != 404 {
		t.Fatalf("missing asset: %d", r.status)
	}
	if r := anon.do("GET", "/api/v1/nope", nil); r.status != 404 || r.problemCode(t) != "not_found" {
		t.Fatalf("unknown api: %d", r.status)
	}
}

func TestClientIPTrustedProxies(t *testing.T) {
	cfg, err := config.Load("", false, []string{"DROOL_SERVER_TRUSTED_PROXIES=10.0.0.0/8"})
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(Deps{Config: cfg, Logs: logging.Discard(), Registry: prometheus.NewRegistry(), UI: fstest.MapFS{}})
	if err != nil {
		t.Fatal(err)
	}
	mk := func(remote, xff string) *http.Request {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = remote
		if xff != "" {
			r.Header.Set("X-Forwarded-For", xff)
		}
		return r
	}
	cases := []struct{ remote, xff, want string }{
		{"203.0.113.5:1234", "1.2.3.4", "203.0.113.5"},                       // untrusted peer: ignore XFF
		{"10.1.1.1:1234", "198.51.100.7", "198.51.100.7"},                    // trusted proxy
		{"10.1.1.1:1234", "6.6.6.6, 198.51.100.7, 10.2.2.2", "198.51.100.7"}, // right-most untrusted
		{"10.1.1.1:1234", "garbage", "10.1.1.1"},
	}
	for _, c := range cases {
		if got := s.clientIP(mk(c.remote, c.xff)); got != c.want {
			t.Errorf("clientIP(%s, %q) = %s, want %s", c.remote, c.xff, got, c.want)
		}
	}
}

func TestSetupClaimFlow(t *testing.T) {
	h := newHarness(t)
	anon := &client{h: h}
	var st setupStatusResponse
	anon.do("GET", "/api/v1/setup/status", nil).json(t, &st)
	if !st.SetupRequired || st.CodeAvailable {
		t.Fatalf("fresh status %+v", st)
	}
	code, _, err := h.auth.IssueSetupCode(context.Background(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	r := anon.do("POST", "/api/v1/setup/claim", map[string]string{"code": "not-it", "username": "admin", "password": pw})
	if r.status != 403 || r.problemCode(t) != "invalid_setup_code" {
		t.Fatalf("wrong code: %d %s", r.status, r.body)
	}
	r = anon.do("POST", "/api/v1/setup/claim", map[string]string{"code": code, "username": "admin", "password": pw})
	if r.status != 201 {
		t.Fatalf("claim: %d %s", r.status, r.body)
	}
	var me meResponse
	r.json(t, &me)
	if me.User.Role != "admin" || me.CSRFToken == "" {
		t.Fatalf("claim response %+v", me)
	}
	var cookie string
	for _, sc := range (&http.Response{Header: r.header}).Cookies() {
		if sc.Name == sessionCookie {
			cookie = sc.Value
		}
	}
	c := &client{h: h, cookie: cookie, csrf: me.CSRFToken}
	if r := c.do("GET", "/api/v1/auth/me", nil); r.status != 200 {
		t.Fatalf("claimed session not usable: %d", r.status)
	}
	if r := anon.do("POST", "/api/v1/setup/claim", map[string]string{"code": code, "username": "evil", "password": pw}); r.status != 409 {
		t.Fatalf("re-claim: %d %s", r.status, r.body)
	}
	anon.do("GET", "/api/v1/setup/status", nil).json(t, &st)
	if st.SetupRequired {
		t.Fatal("setup still required after claim")
	}

	// Attempts are audited, and the code never appears in the audit trail.
	r = c.do("GET", "/api/v1/audit?action=setup.", nil)
	if strings.Contains(string(r.body), code) {
		t.Fatal("setup code leaked into audit log")
	}
	var list listResponse[auditJSON]
	r.json(t, &list)
	results := map[string]int{}
	for _, e := range list.Items {
		results[e.Result]++
	}
	if results["success"] != 1 || results["failure"] != 2 {
		t.Fatalf("setup audit results %v", results)
	}
}

// TestOpenAPIConformance checks that every route in the route table is
// documented in api/openapi.yaml and vice versa.
func TestOpenAPIConformance(t *testing.T) {
	data, err := os.ReadFile("../../api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var spec struct {
		Paths map[string]map[string]any `yaml:"paths"`
	}
	if err := yaml.Unmarshal(data, &spec); err != nil {
		t.Fatal(err)
	}
	methods := map[string]bool{"get": true, "post": true, "put": true, "patch": true, "delete": true}
	inSpec := map[string]bool{}
	for p, ops := range spec.Paths {
		for m := range ops {
			if methods[m] {
				inSpec[strings.ToUpper(m)+" "+APIPrefix+p] = true
			}
		}
	}
	inCode := map[string]bool{}
	for _, rt := range (&Server{}).routes() {
		inCode[rt.method+" "+rt.path] = true
	}
	var missing, extra []string
	for k := range inCode {
		if !inSpec[k] {
			missing = append(missing, k)
		}
	}
	for k := range inSpec {
		if !inCode[k] {
			extra = append(extra, k)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	if len(missing) > 0 {
		t.Errorf("routes not documented in openapi.yaml: %v", missing)
	}
	if len(extra) > 0 {
		t.Errorf("openapi.yaml documents routes that do not exist: %v", extra)
	}
}
