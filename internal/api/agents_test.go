package api

import (
	"context"
	"encoding/json"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"drool/internal/agent"
	"drool/internal/agentcaps"
	"drool/internal/auth"
	"drool/internal/store/db"
)

// End-to-end tests of the agent against the controller in one process:
// the real gRPC endpoint with mTLS, the real agent, the REST API and the
// management router (ARCHITECTURE-ALPHA §23).

type agentEnv struct {
	h    *harness
	c    *client
	addr string
}

func newAgentEnv(t *testing.T) *agentEnv {
	t.Helper()
	h := newHarness(t)
	h.mkUser("admin", auth.RoleAdmin)
	c := h.login("admin")
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	h.agents.Serve(ln)
	t.Cleanup(h.agents.Stop)
	h.agents.SetAdvertise([]string{ln.Addr().String()})
	return &agentEnv{h: h, c: c, addr: ln.Addr().String()}
}

type tokenResp struct {
	ID             string `json:"id"`
	Token          string `json:"token"`
	InstallCommand string `json:"install_command"`
}

func (e *agentEnv) token(t *testing.T, body map[string]any) tokenResp {
	t.Helper()
	r := e.c.do("POST", "/api/v1/agents/tokens", body)
	if r.status != 201 {
		t.Fatalf("create token: %d %s", r.status, r.body)
	}
	var tr tokenResp
	r.json(t, &tr)
	return tr
}

// agentPaths prepares an agent's directories with a ceiling.
func agentPaths(t *testing.T, caps agentcaps.Caps) agent.Paths {
	t.Helper()
	p := agent.Under(t.TempDir())
	os.MkdirAll(filepath.Dir(p.Config), 0o755)
	if err := os.WriteFile(p.Config, agentcaps.Render(caps, ""), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func fullCaps(allowed string) agentcaps.Caps {
	c, _ := agentcaps.Preset(agentcaps.PresetFull)
	if allowed != "" {
		c.Files.AllowedPaths = []string{allowed}
	}
	return c
}

type runningAgent struct {
	a      *agent.Agent
	cancel context.CancelFunc
	done   chan struct{}
}

func (r *runningAgent) stop() {
	r.cancel()
	<-r.done
	r.a.Close()
}

func startAgent(t *testing.T, p agent.Paths, version string) *runningAgent {
	t.Helper()
	a, err := agent.New(agent.Options{Paths: p, Version: version, StatusEvery: time.Second, RenewCheck: 200 * time.Millisecond, Supervisor: "exit"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	r := &runningAgent{a: a, cancel: cancel, done: make(chan struct{})}
	go func() { a.Run(ctx); close(r.done) }()
	t.Cleanup(func() {
		select {
		case <-r.done:
		default:
			r.stop()
		}
	})
	return r
}

func eventually(t *testing.T, d time.Duration, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func (e *agentEnv) connected(hostID string) bool {
	_, ok := e.h.agents.SessionFor(hostID)
	return ok
}

type hostAgentResp struct {
	Management struct {
		Preferred  string `json:"preferred"`
		Fallback   string `json:"fallback"`
		Active     string `json:"active"`
		AgentState string `json:"agent_state"`
		Reason     string `json:"reason"`
		Blocked    bool   `json:"blocked"`
	} `json:"management"`
	Identity *struct {
		ID              string          `json:"id"`
		Status          string          `json:"status"`
		Effective       json.RawMessage `json:"effective"`
		ManifestVersion int64           `json:"manifest_version"`
		Compatible      bool            `json:"compatible"`
		Protocol        int64           `json:"protocol"`
	} `json:"identity"`
	Sessions      []map[string]any `json:"sessions"`
	StateVersions []struct {
		Domain   string `json:"domain"`
		Revision int64  `json:"revision"`
		Status   string `json:"status"`
	} `json:"state_versions"`
}

func (e *agentEnv) hostAgent(t *testing.T, id string) hostAgentResp {
	t.Helper()
	var r hostAgentResp
	e.c.do("GET", "/api/v1/hosts/"+id+"/agent", nil).json(t, &r)
	return r
}

type jobHostResp struct {
	State         string `json:"state"`
	ErrorClass    string `json:"error_class"`
	ErrorMessage  string `json:"error_message"`
	Transport     string `json:"transport"`
	TransportNote string `json:"transport_note"`
}

// runJob runs one step on one host and returns the host's result and output.
func (e *agentEnv) runJob(t *testing.T, hostID string, step map[string]any) (jobHostResp, string) {
	t.Helper()
	var j jobJSON
	r := e.c.do("POST", "/api/v1/jobs", map[string]any{"steps": []any{step}, "targets": map[string]any{"hosts": []string{hostID}}})
	if r.status != 201 {
		t.Fatalf("create job: %d %s", r.status, r.body)
	}
	r.json(t, &j)
	waitJob(t, e.c, j.ID)
	var jh jobHostResp
	e.c.do("GET", "/api/v1/jobs/"+j.ID+"/hosts/"+hostID, nil).json(t, &jh)
	var out struct {
		Segments []struct {
			Text string `json:"text"`
		} `json:"segments"`
	}
	e.c.do("GET", "/api/v1/jobs/"+j.ID+"/hosts/"+hostID+"/output", nil).json(t, &out)
	var b strings.Builder
	for _, s := range out.Segments {
		b.WriteString(s.Text)
	}
	return jh, b.String()
}

func TestAgentEnrollOperateFallbackRevoke(t *testing.T) {
	e := newAgentEnv(t)
	hostID := readyHost(t, e.h, e.c, "hybrid")
	allowed := t.TempDir()
	os.Chmod(allowed, 0o755)
	p := agentPaths(t, fullCaps(allowed))

	tok := e.token(t, map[string]any{"host_id": hostID, "preset": "full", "description": "test"})
	if !strings.Contains(tok.InstallCommand, "--token '"+tok.Token+"'") {
		t.Errorf("install command: %s", tok.InstallCommand)
	}
	ctx := context.Background()
	id, err := agent.Enroll(ctx, p, tok.Token, "v1.0.0")
	if err != nil {
		t.Fatalf("enroll: %v", err)
	}
	if id.HostID != hostID {
		t.Fatalf("enrolled into %s, want the token's host %s", id.HostID, hostID)
	}
	// Single use.
	p2 := agentPaths(t, fullCaps(allowed))
	if _, err := agent.Enroll(ctx, p2, tok.Token, "v1.0.0"); err == nil || !strings.Contains(err.Error(), "already used") {
		t.Fatalf("token reuse: %v", err)
	}
	// The private identity key never left the agent's directory; the
	// controller has only the public key.
	key, _ := os.ReadFile(filepath.Join(p.State, "identity.key"))
	ident, _ := e.h.st.R.GetLiveIdentityForHost(ctx, hostID)
	if len(key) == 0 || len(ident.PublicKey) != 32 {
		t.Fatalf("identity key material: %d bytes on the host, %d on the controller", len(key), len(ident.PublicKey))
	}

	ag := startAgent(t, p, "v1.0.0")
	eventually(t, 10*time.Second, "agent session", func() bool { return e.connected(hostID) })
	ha := e.hostAgent(t, hostID)
	if ha.Management.Preferred != "agent" || ha.Management.Fallback != "ssh" || ha.Management.Active != "agent" {
		t.Fatalf("management after enrollment: %+v", ha.Management)
	}

	// Operations go through the agent.
	jh, out := e.runJob(t, hostID, map[string]any{"type": "command", "command": "echo via-agent-$((40+2))"})
	if jh.State != "succeeded" || jh.Transport != "agent" || !strings.Contains(out, "via-agent-42") {
		t.Fatalf("command via agent: %+v %q", jh, out)
	}

	// Telemetry arrives without SSH polling; agent addresses are recorded
	// with their own source.
	eventually(t, 10*time.Second, "agent facts", func() bool {
		_, err := e.h.st.R.GetHostFacts(ctx, hostID)
		return err == nil
	})

	// Narrowing: no shell. The agent refuses; DROOL must not use SSH instead.
	noShell := fullCaps(allowed)
	noShell.Shell = false
	if r := e.c.do("PUT", "/api/v1/hosts/"+hostID+"/agent/caps", map[string]any{"caps": noShell}); r.status != 204 {
		t.Fatalf("narrow: %d %s", r.status, r.body)
	}
	eventually(t, 5*time.Second, "narrowing applied", func() bool { return !ag.a.Effective().Shell })
	jh, _ = e.runJob(t, hostID, map[string]any{"type": "command", "command": "echo should-not-run"})
	if jh.State != "failed" || jh.ErrorClass != "CapabilityError" || jh.Transport != "agent" {
		t.Fatalf("capability denial must not fall back to SSH: %+v", jh)
	}
	// The controller can't widen beyond the ceiling either.
	wider := agentcaps.All()
	e.c.do("PUT", "/api/v1/hosts/"+hostID+"/agent/caps", map[string]any{"caps": wider})
	eventually(t, 5*time.Second, "narrowing lifted", func() bool { return ag.a.Effective().Shell })
	if eff := ag.a.Effective(); len(eff.Files.AllowedPaths) != 1 || eff.Files.AllowedPaths[0] != filepath.Clean(allowed) {
		t.Fatalf("ceiling widened by the controller: %+v", eff.Files)
	}

	// Files through the agent, limited to the allowed path.
	base := "/api/v1/hosts/" + hostID + "/files"
	if r := e.c.raw("PUT", base+"/content?path="+url.QueryEscape(filepath.Join(allowed, "a.txt")), strings.NewReader("hello agent\n")); r.status != 200 {
		t.Fatalf("upload via agent: %d %s", r.status, r.body)
	}
	if b, _ := os.ReadFile(filepath.Join(allowed, "a.txt")); string(b) != "hello agent\n" {
		t.Fatalf("uploaded content %q", b)
	}
	if r := e.c.do("GET", base+"?path="+url.QueryEscape(filepath.Dir(allowed)), nil); r.status != 403 || r.problemCode(t) != "capability" {
		t.Fatalf("listing outside the allowed path: %d %s", r.status, r.body)
	}
	// A file deployment through the agent carries a files state version.
	art := e.c.raw("POST", "/api/v1/artifacts?name=app.conf", strings.NewReader("key = 1\n"))
	var a struct {
		SHA256 string `json:"sha256"`
	}
	art.json(t, &a)
	jh, out = e.runJob(t, hostID, map[string]any{"type": "file_deploy", "artifact": a.SHA256, "path": filepath.Join(allowed, "app.conf")})
	if jh.State != "succeeded" || jh.Transport != "agent" {
		t.Fatalf("file deploy via agent: %+v %s", jh, out)
	}
	ha = e.hostAgent(t, hostID)
	files := false
	for _, sv := range ha.StateVersions {
		files = files || (sv.Domain == "files" && sv.Revision == 1 && sv.Status == "ok")
	}
	if !files {
		t.Fatalf("files state version: %+v", ha.StateVersions)
	}
	st, _ := agent.OpenStore(p.State)
	if d, _ := st.Domains(); d["files"].Revision != 1 || d["files"].Epoch != e.h.agents.Epoch() {
		t.Fatalf("agent's applied files state: %+v", d)
	}

	// Agent stops before dispatch: SSH fallback, and the job says so.
	ag.stop()
	eventually(t, 10*time.Second, "agent gone", func() bool { return !e.connected(hostID) })
	jh, out = e.runJob(t, hostID, map[string]any{"type": "command", "command": "echo via-ssh"})
	if jh.State != "succeeded" || jh.Transport != "ssh" || !strings.Contains(jh.TransportNote, "SSH fallback") || !strings.Contains(out, "via-ssh") {
		t.Fatalf("fallback: %+v %q", jh, out)
	}

	// Back on the agent once it reconnects.
	ag = startAgent(t, p, "v1.0.0")
	eventually(t, 10*time.Second, "agent back", func() bool { return e.connected(hostID) })
	if jh, _ = e.runJob(t, hostID, map[string]any{"type": "command", "command": "true"}); jh.Transport != "agent" {
		t.Fatalf("after reconnect: %+v", jh)
	}

	// Revocation: immediate, and management is blocked (no silent SSH).
	ha = e.hostAgent(t, hostID)
	if r := e.c.do("POST", "/api/v1/agents/"+ha.Identity.ID+"/revoke", map[string]any{"reason": "test"}); r.status != 204 {
		t.Fatalf("revoke: %d %s", r.status, r.body)
	}
	eventually(t, 5*time.Second, "session closed", func() bool { return !e.connected(hostID) })
	jh, _ = e.runJob(t, hostID, map[string]any{"type": "command", "command": "echo nope"})
	if jh.State != "failed" || !strings.Contains(jh.ErrorMessage, "revoked") {
		t.Fatalf("job after revocation: %+v", jh)
	}
	time.Sleep(500 * time.Millisecond)
	if e.connected(hostID) {
		t.Fatal("a revoked agent reconnected")
	}
	// Switching to SSH is an explicit, audited administrator decision.
	if r := e.c.do("PUT", "/api/v1/hosts/"+hostID+"/management", map[string]any{"preferred": "ssh", "fallback": "none"}); r.status != 200 {
		t.Fatalf("switch to ssh: %d %s", r.status, r.body)
	}
	if jh, _ = e.runJob(t, hostID, map[string]any{"type": "command", "command": "true"}); jh.State != "succeeded" || jh.Transport != "ssh" {
		t.Fatalf("after switching to SSH: %+v", jh)
	}
	var au struct {
		Items []struct {
			Action string `json:"action"`
		} `json:"items"`
	}
	e.c.do("GET", "/api/v1/audit?action=agent.&limit=100", nil).json(t, &au)
	seen := map[string]bool{}
	for _, it := range au.Items {
		seen[it.Action] = true
	}
	for _, want := range []string{"agent.token.create", "agent.enroll", "agent.caps", "agent.revoke"} {
		if !seen[want] {
			t.Errorf("no %s audit event", want)
		}
	}
	_ = db.Host{}
}
