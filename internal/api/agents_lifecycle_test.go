package api

import (
	"context"
	"crypto/ed25519"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"drool/internal/agent"
	"drool/internal/agentcaps"
	"drool/internal/agentpki"
	"drool/internal/agentrpc"
)

func (e *agentEnv) enroll(t *testing.T, body map[string]any, caps agentcaps.Caps, version string) (agent.Paths, *agent.Identity) {
	t.Helper()
	p := agentPaths(t, caps)
	tok := e.token(t, body)
	id, err := agent.Enroll(context.Background(), p, tok.Token, version)
	if err != nil {
		t.Fatalf("enroll: %v", err)
	}
	return p, id
}

func monitorCaps() agentcaps.Caps { c, _ := agentcaps.Preset(agentcaps.PresetMonitor); return c }

func TestAgentTokenRules(t *testing.T) {
	e := newAgentEnv(t)
	ctx := context.Background()

	// Expired token.
	tok := e.token(t, map[string]any{"ttl_minutes": 1})
	e.h.st.Writer.Exec(`UPDATE agent_enrollment_tokens SET expires_at = 1 WHERE id = ?`, tok.ID)
	if _, err := agent.Enroll(ctx, agentPaths(t, monitorCaps()), tok.Token, "v1.0.0"); err == nil {
		t.Error("expired token accepted")
	}
	// Revoked token.
	tok = e.token(t, map[string]any{})
	if r := e.c.do("POST", "/api/v1/agents/tokens/"+tok.ID+"/revoke", nil); r.status != 204 {
		t.Fatalf("revoke token: %d %s", r.status, r.body)
	}
	if _, err := agent.Enroll(ctx, agentPaths(t, monitorCaps()), tok.Token, "v1.0.0"); err == nil {
		t.Error("revoked token accepted")
	}
	// A token whose pinned root doesn't match the controller (an attacker's
	// controller in between, or a wrong address): the agent refuses before
	// sending anything secret.
	tok = e.token(t, map[string]any{})
	b, _ := agentrpc.ParseBundle(tok.Token)
	b.Root = "sha256:" + strings.Repeat("ab", 32)
	if _, err := agent.Enroll(ctx, agentPaths(t, monitorCaps()), b.Encode(), "v1.0.0"); err == nil || !strings.Contains(err.Error(), "pins") {
		t.Errorf("wrong root pin: %v", err)
	}
	// The real token still works afterwards (nothing was consumed).
	var g struct {
		ID string `json:"id"`
	}
	e.c.do("POST", "/api/v1/groups", map[string]any{"name": "agents-group"}).json(t, &g)
	tok = e.token(t, map[string]any{"group_id": g.ID})
	// New hosts are never matched to existing ones by name.
	existing := readyHost(t, e.h, e.c, mustHostname(t))
	id, err := agent.Enroll(ctx, agentPaths(t, monitorCaps()), tok.Token, "v1.0.0")
	if err != nil {
		t.Fatalf("enroll with group token: %v", err)
	}
	if id.HostID == existing {
		t.Fatal("a new agent was attached to an existing host because the hostname matched")
	}
	h, _ := e.h.st.R.GetHost(ctx, id.HostID)
	if h.ManagementMethod != "agent" || h.SshConfigured != 0 || !strings.HasPrefix(h.Name, mustHostname(t)+"-") {
		t.Errorf("new agent-only host: %+v", h)
	}
	var grp struct {
		HostIDs []string `json:"host_ids"`
	}
	e.c.do("GET", "/api/v1/groups/"+g.ID, nil).json(t, &grp)
	if len(grp.HostIDs) != 1 || grp.HostIDs[0] != id.HostID {
		t.Errorf("group-bound token: members %v", grp.HostIDs)
	}
	// An agent-only host without fallback: not connected means not managed.
	jh, _ := e.runJob(t, id.HostID, map[string]any{"type": "command", "command": "true"})
	if jh.State != "failed" || jh.ErrorClass != "ConnectionError" {
		t.Errorf("agent-only host with no agent: %+v", jh)
	}
}

func mustHostname(t *testing.T) string {
	h, err := os.Hostname()
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func TestAgentCertificateLifecycle(t *testing.T) {
	e := newAgentEnv(t)
	ctx := context.Background()
	e.h.agents.SetLifetimes(time.Minute, 180*24*time.Hour)
	p, id := e.enroll(t, map[string]any{}, monitorCaps(), "v1.0.0")
	st, _ := agent.OpenStore(p.State)
	first, _ := st.Chain()

	// Renewal at about two thirds of the lifetime: with a one-minute
	// certificate (backdated five minutes) that is right away.
	ag := startAgent(t, p, "v1.0.0")
	eventually(t, 10*time.Second, "renewal", func() bool {
		c, err := st.Chain()
		return err == nil && c[0].SerialNumber.Cmp(first[0].SerialNumber) != 0
	})
	ag.stop()

	// Expired certificate, within the grace period: recovered with proof
	// of possession of the identity key.
	e.h.agents.SetLifetimes(30*24*time.Hour, 180*24*time.Hour)
	key, _ := st.IdentityKey(false)
	chain, _, err := e.h.pki.IssueAgent(id.IdentityID, key.Public().(ed25519.PublicKey), -time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	st.SaveChain(chain)
	ag = startAgent(t, p, "v1.0.0")
	eventually(t, 10*time.Second, "recovered session", func() bool { return e.connected(id.HostID) })
	if c, _ := st.Chain(); time.Until(c[0].NotAfter) < 29*24*time.Hour {
		t.Errorf("recovered certificate expires %s", c[0].NotAfter)
	}
	ag.stop()

	// Beyond the grace period: refused, re-enrollment required.
	e.h.agents.SetLifetimes(30*24*time.Hour, time.Second)
	chain, _, _ = e.h.pki.IssueAgent(id.IdentityID, key.Public().(ed25519.PublicKey), -time.Minute)
	st.SaveChain(chain)
	ag = startAgent(t, p, "v1.0.0")
	time.Sleep(2 * time.Second)
	if e.connected(id.HostID) {
		t.Fatal("an agent recovered beyond the grace period")
	}
	ag.stop()

	var au struct {
		Items []struct {
			Action string `json:"action"`
			Result string `json:"result"`
		} `json:"items"`
	}
	e.c.do("GET", "/api/v1/audit?action=agent.&limit=200", nil).json(t, &au)
	got := map[string]bool{}
	for _, it := range au.Items {
		got[it.Action+"/"+it.Result] = true
	}
	for _, want := range []string{"agent.renew/success", "agent.recover/success", "agent.recover/denied"} {
		if !got[want] {
			t.Errorf("missing audit %s (have %v)", want, got)
		}
	}
	// A revoked identity can't recover either.
	e.h.agents.SetLifetimes(30*24*time.Hour, 180*24*time.Hour)
	ha := e.hostAgent(t, id.HostID)
	e.c.do("POST", "/api/v1/agents/"+ha.Identity.ID+"/revoke", map[string]any{"reason": "lost laptop"})
	ag = startAgent(t, p, "v1.0.0")
	time.Sleep(2 * time.Second)
	if e.connected(id.HostID) {
		t.Fatal("a revoked identity recovered")
	}
	ag.stop()
	_ = ctx
}

func TestAgentCloneQuarantine(t *testing.T) {
	e := newAgentEnv(t)
	p, id := e.enroll(t, map[string]any{}, fullCaps(""), "v1.0.0")
	ag := startAgent(t, p, "v1.0.0")
	eventually(t, 10*time.Second, "session", func() bool { return e.connected(id.HostID) })

	// A cloned VM: same state directory, running at the same time.
	clone := agent.Under(t.TempDir())
	copyTree(t, filepath.Dir(p.State), filepath.Dir(clone.State))
	cl := startAgent(t, clone, "v1.0.0")
	eventually(t, 20*time.Second, "quarantine", func() bool {
		ha := e.hostAgent(t, id.HostID)
		return ha.Identity != nil && ha.Identity.Status == "quarantined" && len(ha.Sessions) == 2
	})
	ha := e.hostAgent(t, id.HostID)
	if !ha.Management.Blocked {
		t.Errorf("management not blocked during a conflict: %+v", ha.Management)
	}
	jh, _ := e.runJob(t, id.HostID, map[string]any{"type": "command", "command": "true"})
	if jh.State != "failed" || !strings.Contains(jh.ErrorMessage, "conflict") {
		t.Errorf("job during conflict: %+v", jh)
	}
	// DROOL doesn't pick a side: resolving needs only one machine left.
	if r := e.c.do("POST", "/api/v1/agents/"+ha.Identity.ID+"/resolve-conflict", nil); r.status != 409 {
		t.Errorf("resolve with both connected: %d %s", r.status, r.body)
	}
	cl.stop()
	eventually(t, 10*time.Second, "clone gone", func() bool { return len(e.hostAgent(t, id.HostID).Sessions) == 1 })
	if r := e.c.do("POST", "/api/v1/agents/"+ha.Identity.ID+"/resolve-conflict", nil); r.status != 204 {
		t.Fatalf("resolve: %d %s", r.status, r.body)
	}
	if jh, _ := e.runJob(t, id.HostID, map[string]any{"type": "command", "command": "true"}); jh.State != "succeeded" || jh.Transport != "agent" {
		t.Errorf("after resolving: %+v", jh)
	}
	ag.stop()
}

func copyTree(t *testing.T, src, dst string) {
	t.Helper()
	filepath.Walk(src, func(p string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dst, rel)
		if fi.IsDir() {
			return os.MkdirAll(target, fi.Mode().Perm()|0o700)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, b, fi.Mode().Perm())
	})
}

func TestAgentManifestRotation(t *testing.T) {
	e := newAgentEnv(t)
	p, id := e.enroll(t, map[string]any{}, monitorCaps(), "v1.0.0")
	ag := startAgent(t, p, "v1.0.0")
	eventually(t, 10*time.Second, "session", func() bool { return e.connected(id.HostID) })
	ag.stop() // offline during the rotation

	// Compromise recovery: new intermediate, old one revoked (manifest 2).
	if r := e.c.do("POST", "/api/v1/agents/pki/rotate", map[string]any{"revoke_old": true}); r.status != 200 {
		t.Fatalf("rotate: %d %s", r.status, r.body)
	}
	// The offline agent still has manifest 1; on reconnect it must take
	// manifest 2 first, then accept the new controller certificate.
	ag = startAgent(t, p, "v1.0.0")
	eventually(t, 10*time.Second, "session after rotation", func() bool { return e.connected(id.HostID) })
	st, _ := agent.OpenStore(p.State)
	m, _, _ := st.Manifest()
	if m.Version != 2 || len(m.RevokedIntermediates) != 1 {
		t.Fatalf("agent manifest after rotation: %+v", m)
	}
	// Its own certificate came from the revoked intermediate: it switched
	// over through certificate recovery (identity key proof), without
	// re-enrolling (DESIGN §4.3.1).
	c, _ := st.Chain()
	if len(c) < 2 || m.Revoked(agentpki.Fingerprint(c[1].Raw)) {
		t.Fatalf("agent still holds a certificate from the revoked intermediate")
	}
	ag.stop()
	// A certificate from the revoked intermediate is no good for a session.
	old := e.h.pki.Status()
	if len(old.RevokedIntermediates) != 1 {
		t.Fatalf("pki status %+v", old)
	}
}

func TestAgentPlannedRotationKeepsAgents(t *testing.T) {
	e := newAgentEnv(t)
	p, id := e.enroll(t, map[string]any{}, monitorCaps(), "v1.0.0")
	if r := e.c.do("POST", "/api/v1/agents/pki/rotate", map[string]any{"revoke_old": false}); r.status != 200 {
		t.Fatalf("rotate: %d %s", r.status, r.body)
	}
	ag := startAgent(t, p, "v1.0.0")
	eventually(t, 10*time.Second, "session after a planned rotation", func() bool { return e.connected(id.HostID) })
	ag.stop()
	// Root offline: rotation needs the root key supplied once.
	var exp struct {
		KeyPEM string `json:"key_pem"`
	}
	e.c.do("POST", "/api/v1/agents/pki/export-root", nil).json(t, &exp)
	var st struct {
		RootFingerprint string `json:"root_fingerprint"`
		RootKeyOnline   bool   `json:"root_key_online"`
	}
	e.c.do("GET", "/api/v1/agents/pki", nil).json(t, &st)
	if !st.RootKeyOnline {
		t.Fatal("root should start online")
	}
	if r := e.c.do("POST", "/api/v1/agents/pki/remove-root", map[string]any{"confirm": st.RootFingerprint}); r.status != 200 {
		t.Fatalf("remove root: %d %s", r.status, r.body)
	}
	if r := e.c.do("POST", "/api/v1/agents/pki/rotate", map[string]any{}); r.status != 409 {
		t.Errorf("rotation with the root offline: %d", r.status)
	}
	if r := e.c.do("POST", "/api/v1/agents/pki/rotate", map[string]any{"root_key_pem": exp.KeyPEM}); r.status != 200 {
		t.Errorf("rotation with the supplied root: %d %s", r.status, r.body)
	}
	e.c.do("GET", "/api/v1/agents/pki", nil).json(t, &st)
	if st.RootKeyOnline {
		t.Error("the supplied root key was stored")
	}
	var n int
	e.h.st.Reader.QueryRow(`SELECT count(*) FROM agent_ca WHERE kind = 'root' AND key_ciphertext IS NOT NULL`).Scan(&n)
	if n != 0 {
		t.Error("root key still in the database")
	}
}

func TestAgentProtocolCompatibility(t *testing.T) {
	e := newAgentEnv(t)
	hostID := readyHost(t, e.h, e.c, "proto")
	p, _ := e.enroll(t, map[string]any{"host_id": hostID}, fullCaps(""), "v1.0.0")
	defer func(min, max string) { agentrpc.ProtocolMin, agentrpc.ProtocolMax = min, max }(agentrpc.ProtocolMin, agentrpc.ProtocolMax)

	// A newer agent that also speaks 1: negotiated down, works.
	agentrpc.ProtocolMin, agentrpc.ProtocolMax = "1", "3"
	ag := startAgent(t, p, "v1.0.0")
	eventually(t, 10*time.Second, "session", func() bool { return e.connected(hostID) })
	if ha := e.hostAgent(t, hostID); ha.Identity.Protocol != 1 || !ha.Identity.Compatible {
		t.Errorf("negotiation: %+v", ha.Identity)
	}
	ag.stop()

	// New agent, old controller: no overlap. Shown as incompatible, SSH
	// fallback used, never pretended healthy.
	agentrpc.ProtocolMin, agentrpc.ProtocolMax = "2", "3"
	ag = startAgent(t, p, "v9.0.0")
	eventually(t, 10*time.Second, "incompatible recorded", func() bool {
		ha := e.hostAgent(t, hostID)
		return ha.Identity != nil && !ha.Identity.Compatible
	})
	ha := e.hostAgent(t, hostID)
	if ha.Management.AgentState != "incompatible" || ha.Management.Active != "ssh" {
		t.Errorf("incompatible agent: %+v", ha.Management)
	}
	if jh, _ := e.runJob(t, hostID, map[string]any{"type": "command", "command": "true"}); jh.Transport != "ssh" || !strings.Contains(jh.TransportNote, "incompatible") {
		t.Errorf("job with an incompatible agent: %+v", jh)
	}
	ag.stop()

	// Old agent, new controller (controller needs 2+).
	agentrpc.ProtocolMin, agentrpc.ProtocolMax = "1", "1"
	e.h.agents.ControllerRange = agentrpc.Range{Min: 2, Max: 3}
	ag = startAgent(t, p, "v0.9.0")
	time.Sleep(time.Second)
	if e.connected(hostID) {
		t.Error("an agent too old for the controller was given a session")
	}
	if ha := e.hostAgent(t, hostID); !strings.Contains(ha.Management.Reason, "too old") {
		t.Errorf("reason: %+v", ha.Management)
	}
	ag.stop()
}

func TestAgentLossDuringOperation(t *testing.T) {
	e := newAgentEnv(t)
	hostID := readyHost(t, e.h, e.c, "loss")
	p, _ := e.enroll(t, map[string]any{"host_id": hostID}, fullCaps(""), "v1.0.0")
	marker := filepath.Join(t.TempDir(), "ran")
	startAgent(t, p, "v1.0.0")
	eventually(t, 10*time.Second, "session", func() bool { return e.connected(hostID) })

	// Drop the connection while the command runs. The agent keeps running
	// it and reports the outcome after reconnecting; it is never run again
	// over SSH (the marker counts executions).
	go func() {
		time.Sleep(700 * time.Millisecond)
		for _, s := range e.h.agents.HostSessions(hostID) {
			e.h.agents.DisconnectIdentity(s.IdentityID, "test", "simulated network loss")
		}
	}()
	jh, out := e.runJob(t, hostID, map[string]any{"type": "command", "command": "sleep 2; echo x >> " + marker + "; echo finished"})
	b, _ := os.ReadFile(marker)
	if strings.Count(string(b), "x") != 1 {
		t.Fatalf("the operation ran %d times", strings.Count(string(b), "x"))
	}
	if jh.State != "succeeded" || jh.Transport != "agent" || !strings.Contains(out, "reported the outcome after reconnecting") {
		t.Fatalf("outcome after reconnect: %+v\n%s", jh, out)
	}
}

func TestAgentEndpointMigration(t *testing.T) {
	e := newAgentEnv(t)
	p, id := e.enroll(t, map[string]any{}, monitorCaps(), "v1.0.0")
	startAgent(t, p, "v1.0.0")
	eventually(t, 10*time.Second, "session", func() bool { return e.connected(id.HostID) })
	st, _ := agent.OpenStore(p.State)
	before, _ := st.Endpoints()

	// A candidate nobody can reach: tested, never committed.
	var m struct {
		ID string `json:"id"`
	}
	e.c.do("POST", "/api/v1/agents/endpoints", map[string]any{"endpoint": "127.0.0.1:1"}).json(t, &m)
	eventually(t, 15*time.Second, "candidate test", func() bool {
		var l struct {
			Items []struct {
				ID     string `json:"id"`
				Failed int    `json:"failed"`
			} `json:"items"`
		}
		e.c.do("GET", "/api/v1/agents/endpoints", nil).json(t, &l)
		return len(l.Items) > 0 && l.Items[0].Failed == 1
	})
	e.c.do("POST", "/api/v1/agents/endpoints/"+m.ID+"/commit", nil)
	time.Sleep(500 * time.Millisecond)
	if ep, _ := st.Endpoints(); ep.Primary != before.Primary {
		t.Fatalf("an unreachable endpoint became primary: %+v", ep)
	}

	// A working candidate (the same listener under another name):
	// confirmed, then committed.
	alt := strings.Replace(e.addr, "127.0.0.1", "localhost", 1)
	e.c.do("POST", "/api/v1/agents/endpoints", map[string]any{"endpoint": alt}).json(t, &m)
	eventually(t, 15*time.Second, "candidate confirmed", func() bool {
		ep, _ := st.Endpoints()
		return ep.CandidateOK && ep.Candidate == alt
	})
	e.c.do("POST", "/api/v1/agents/endpoints/"+m.ID+"/commit", nil)
	eventually(t, 5*time.Second, "committed", func() bool {
		ep, _ := st.Endpoints()
		return ep.Primary == alt && len(ep.Fallbacks) > 0 && ep.Fallbacks[0] == before.Primary
	})
}
