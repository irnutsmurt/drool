package api

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"drool/internal/auth"
	"drool/internal/firewall/document"
	"drool/internal/store/db"
)

// Firewall Designer (M9): one canonical document behind Topology, Rules and
// YAML; previews, management paths, simulation and Explain on drafts.

type checkResp struct {
	Document document.Document `json:"document"`
	YAML     string            `json:"yaml"`
	Valid    bool              `json:"valid"`
	Problems []document.Problem
	Notices  []string
	Changed  bool
	Revision string
	Blockers []string
	Impact   []struct {
		HostName  string   `json:"host_name"`
		Added     []string `json:"added"`
		NewDenies []string `json:"new_denies"`
		Removed   []string `json:"removed"`
		Conflicts []string `json:"conflicts"`
	}
	Management []struct {
		Host       string `json:"host"`
		Kind       string `json:"kind"`
		Blocked    bool   `json:"blocked"`
		Unverified bool   `json:"unverified"`
		Hops       []struct {
			FromName  string   `json:"from_name"`
			ToName    string   `json:"to_name"`
			Port      int      `json:"port"`
			Status    string   `json:"status"`
			BlockedBy []string `json:"blocked_by"`
			Detail    string   `json:"detail"`
		} `json:"hops"`
	}
}

func (c checkResp) path(host, kind string) string {
	for _, m := range c.Management {
		if m.Host == host && m.Kind == kind {
			var parts []string
			for _, h := range m.Hops {
				parts = append(parts, fmt.Sprintf("%s→%s=%s%v", h.FromName, h.ToName, h.Status, h.BlockedBy))
			}
			return strings.Join(parts, " ")
		}
	}
	return "(none)"
}

func (e *fwEnv) check(t *testing.T, body map[string]any) checkResp {
	t.Helper()
	r := e.c.do("POST", "/api/v1/firewall/document/check", body)
	if r.status != 200 {
		t.Fatalf("check: %d %s", r.status, r.body)
	}
	var c checkResp
	r.json(t, &c)
	return c
}

func (e *fwEnv) checkYAML(t *testing.T, y string) checkResp {
	t.Helper()
	return e.check(t, map[string]any{"yaml": y})
}

func (e *fwEnv) saveYAML(t *testing.T, y, rev string) map[string]any {
	t.Helper()
	r := e.c.do("PUT", "/api/v1/firewall/document", map[string]any{"yaml": y, "revision": rev})
	if r.status != 200 {
		t.Fatalf("save: %d %s", r.status, r.body)
	}
	var out map[string]any
	r.json(t, &out)
	return out
}

func observe(t *testing.T, e *fwEnv, hostID, kind, addr string, port int) {
	t.Helper()
	if err := e.h.st.W.UpsertFwMgmtObservation(context.Background(), db.UpsertFwMgmtObservationParams{HostID: hostID, Kind: kind,
		Address: addr, Port: int64(port), ObservedAt: time.Now().UnixMilli()}); err != nil {
		t.Fatal(err)
	}
}

const designerBase = `networks:
  admin-lan:
    cidr: 192.168.1.0/24
  partners:
    cidrs: [203.0.113.0/24, "2001:db8:77::/48"]
groups:
  ssh-managed:
    members: [server-a, server-b]
  bastions:
    members: []
services:
  app-8812:
    protocol: tcp
    port: 8812
  admin-alt:
    protocol: tcp
    port: 2222
policy:
  - name: admin-to-jump
    from: admin-lan
    to: jump-a
    allow: [ssh]
  - name: jump-to-servers
    from: jump-a
    to: ssh-managed
    allow: [ssh]
  - name: server-a-to-server-b
    from: server-a
    to: server-b
    allow: [app-8812, https]
  - name: partners-web
    from: partners
    to: server-b
    allow: [https]
  - name: no-bastion-ssh
    from: bastions
    to: server-a
    deny: [ssh]
  - name: no-alt-admin
    from: any
    to: server-b
    deny: [admin-alt]
`

func TestFirewallDesigner(t *testing.T) {
	e := newFwEnv(t)
	ctx := context.Background()
	jump, sj := e.sshFwHost(t, "jump-a")
	serverA, sa := e.sshFwHost(t, "server-a")
	serverB, sb := e.sshFwHost(t, "server-b")
	_, pj := sj.Host()
	_, pa := sa.Host()
	_, pb := sb.Host()
	if r := e.c.do("PATCH", "/api/v1/hosts/"+serverA, map[string]any{"via_host_id": jump}); r.status != 200 {
		t.Fatalf("via: %d %s", r.status, r.body)
	}
	for id, a := range map[string]string{jump: "10.0.0.5", serverA: "10.0.0.11", serverB: "10.0.0.12"} {
		e.h.st.W.UpsertHostAddress(ctx, upsertAddr(id, a))
	}
	// How the hosts saw DROOL's connections: the controller reaches jump-a
	// and server-b directly (from 192.0.2.10); server-a through jump-a.
	observe(t, e, jump, "ssh_in", "192.0.2.10", pj)
	observe(t, e, serverB, "ssh_in", "192.0.2.10", pb)

	// An empty installation: an empty, valid document.
	var doc struct {
		Document document.Document `json:"document"`
		YAML     string            `json:"yaml"`
		Revision string            `json:"revision"`
	}
	e.c.do("GET", "/api/v1/firewall/document", nil).json(t, &doc)
	if len(doc.Document.Policy) != 0 || !strings.Contains(doc.YAML, "policy: []") {
		t.Fatalf("empty document: %+v %q", doc.Document, doc.YAML)
	}

	// YAML → canonical model: valid, previewed, management paths laid out.
	c := e.checkYAML(t, designerBase)
	if !c.Valid || len(c.Problems) > 0 {
		t.Fatalf("base: %+v", c.Problems)
	}
	if !c.Changed || len(c.Blockers) != 0 {
		t.Fatalf("changed=%v blockers=%v", c.Changed, c.Blockers)
	}
	if p := c.path("server-a", "ssh"); p != "DROOL Controller→jump-a=protected[] jump-a→server-a=unverified[]" {
		t.Fatalf("jump path before measuring server-a: %s", p)
	}
	observe(t, e, serverA, "ssh_in", "10.0.0.5", pa)
	// The test sshds don't listen on 22: SSH here means their real ports.
	designerBase := strings.NewReplacer("allow: [ssh]", "allow: [sshd]", "deny: [ssh]", "deny: [sshd]").Replace(designerBase) +
		""
	designerBase = strings.Replace(designerBase, "services:\n", fmt.Sprintf("services:\n  sshd:\n    entries:\n      - {protocol: tcp, port: \"%d,%d,%d\"}\n", pj, pa, pb), 1)
	c = e.checkYAML(t, designerBase)
	for _, m := range c.Management {
		for _, h := range m.Hops {
			want := map[string]int{"jump-a": pj, "server-a": pa, "server-b": pb}[h.ToName]
			if want != 0 && h.Port != want {
				t.Fatalf("%s hop to %s: port %d, want %d", m.Host, h.ToName, h.Port, want)
			}
		}
	}
	if p := c.path("server-a", "ssh"); p != "DROOL Controller→jump-a=protected[] jump-a→server-a=protected[]" {
		t.Fatalf("jump path: %s", p)
	}
	if p := c.path("server-b", "ssh"); p != "DROOL Controller→server-b=protected[]" {
		t.Fatalf("direct path: %s", p)
	}
	impactOf := func(c checkResp, host string) string {
		for _, im := range c.Impact {
			if im.HostName == host {
				return strings.Join(append(append(im.Added, im.NewDenies...), im.Removed...), "\n")
			}
		}
		return ""
	}
	if im := impactOf(c, "server-b"); !strings.Contains(im, "allow inbound TCP 443, 8812 from host server-a (policy server-a-to-server-b)") ||
		!strings.Contains(im, "allow inbound TCP 443 from network partners (policy partners-web)") ||
		!strings.Contains(im, "deny inbound TCP 2222 from anywhere (policy no-alt-admin)") {
		t.Fatalf("server-b impact:\n%s", im)
	}

	// Save it; the stored document renders back to the same canonical
	// YAML, and the Rules view (document JSON) round-trips too.
	saved := e.saveYAML(t, designerBase, doc.Revision)
	rev := saved["revision"].(string)
	e.c.do("GET", "/api/v1/firewall/document", nil).json(t, &doc)
	if doc.YAML != c.YAML || doc.Revision != rev {
		t.Fatalf("stored YAML differs from the checked one:\n%s\n---\n%s", doc.YAML, c.YAML)
	}
	byJSON := e.check(t, map[string]any{"document": doc.Document})
	if byJSON.YAML != doc.YAML || byJSON.Changed {
		t.Fatalf("document → YAML: changed=%v\n%s", byJSON.Changed, byJSON.YAML)
	}
	// The stored policies are the ones M8 compiles and applies.
	var pols struct {
		Items []struct {
			Name string `json:"name"`
			From []struct {
				Kind string `json:"kind"`
			} `json:"from"`
		} `json:"items"`
	}
	e.c.do("GET", "/api/v1/firewall/policies", nil).json(t, &pols)
	if len(pols.Items) != 6 || pols.Items[0].Name != "admin-to-jump" || pols.Items[0].From[0].Kind != "network" {
		t.Fatalf("stored policies: %+v", pols.Items)
	}

	// A stale editor can't overwrite.
	r := e.c.do("PUT", "/api/v1/firewall/document", map[string]any{"yaml": designerBase, "revision": "0000"})
	if r.status != 409 || !strings.Contains(string(r.body), "revision_conflict") {
		t.Fatalf("stale save: %d %s", r.status, r.body)
	}

	// Management path: an explicit deny of SSH to server-a blocks the
	// jump-host segment.
	deny := designerBase + "  - name: lock-a\n    from: any\n    to: server-a\n    deny: [sshd]\n"
	c = e.checkYAML(t, deny)
	if !c.Valid || len(c.Blockers) == 0 || c.path("server-a", "ssh") != "DROOL Controller→jump-a=protected[] jump-a→server-a=blocked[lock-a]" {
		t.Fatalf("explicit deny: %v %s", c.Blockers, c.path("server-a", "ssh"))
	}
	// Indirect: jump-a joins the bastions group that may not SSH to server-a.
	c = e.checkYAML(t, strings.Replace(designerBase, "  bastions:\n    members: []", "  bastions:\n    members: [jump-a]", 1))
	if c.path("server-a", "ssh") != "DROOL Controller→jump-a=protected[] jump-a→server-a=blocked[no-bastion-ssh]" {
		t.Fatalf("membership change: %s", c.path("server-a", "ssh"))
	}
	// Indirect: a network object widened over the controller's address.
	c = e.checkYAML(t, strings.Replace(designerBase, "to: server-b\n    allow: [https]", "to: server-b\n    deny: [sshd]", 1))
	if c.path("server-b", "ssh") != "DROOL Controller→server-b=protected[]" {
		t.Fatalf("partners deny without the controller: %s", c.path("server-b", "ssh"))
	}
	widened := strings.Replace(strings.Replace(designerBase, "to: server-b\n    allow: [https]", "to: server-b\n    deny: [sshd]", 1),
		"203.0.113.0/24", "192.0.2.0/24", 1)
	c = e.checkYAML(t, widened)
	if c.path("server-b", "ssh") != "DROOL Controller→server-b=blocked[partners-web]" {
		t.Fatalf("CIDR change: %s", c.path("server-b", "ssh"))
	}
	// Indirect: a service redefined onto port 22 under an existing deny.
	c = e.checkYAML(t, strings.Replace(designerBase, "port: 2222", fmt.Sprintf("port: %d", pb), 1))
	if c.path("server-b", "ssh") != "DROOL Controller→server-b=blocked[no-alt-admin]" {
		t.Fatalf("service change: %s", c.path("server-b", "ssh"))
	}
	// Removing the allow that looks like management does not cut it:
	// DROOL's management path is allowed ahead of every rule.
	c = e.checkYAML(t, strings.Replace(designerBase, "  - name: jump-to-servers\n    from: jump-a\n    to: ssh-managed\n    allow: [sshd]\n", "", 1))
	if !c.Valid || len(c.Blockers) != 0 || c.path("server-a", "ssh") != "DROOL Controller→jump-a=protected[] jump-a→server-a=protected[]" {
		t.Fatalf("allow removed: %v %s", c.Blockers, c.path("server-a", "ssh"))
	}
	if im := impactOf(c, "server-a"); !strings.Contains(im, fmt.Sprintf("from host jump-a (policy jump-to-servers)")) {
		t.Fatalf("removed allowance not previewed:\n%s", im)
	}

	// Invalid drafts are explained with lines and never saved.
	c = e.checkYAML(t, designerBase+"  - name: broken\n    from: nowhere\n    to: server-a\n    allow: [ssh]\n")
	if c.Valid || len(c.Problems) != 1 || c.Problems[0].Line == 0 || !strings.Contains(c.Problems[0].Message, `unknown name "nowhere"`) {
		t.Fatalf("invalid: %+v", c.Problems)
	}
	c = e.checkYAML(t, strings.Replace(designerBase, "port: 8812", "port: 99999", 1))
	if c.Valid || len(c.Problems) != 1 || !strings.Contains(c.Problems[0].Message, "invalid port") || c.Problems[0].Line != strings.Count(designerBase[:strings.Index(designerBase, "  app-8812:")], "\n")+1 {
		t.Fatalf("bad port: %+v", c.Problems)
	}
	// Deleting a referenced object is a problem, not a silent change.
	c = e.checkYAML(t, strings.Replace(designerBase, "  app-8812:\n    protocol: tcp\n    port: 8812\n", "", 1))
	if c.Valid || !strings.Contains(c.Problems[0].Message, `unknown service "app-8812"`) {
		t.Fatalf("deleted service: %+v", c.Problems)
	}
	r = e.c.do("PUT", "/api/v1/firewall/document", map[string]any{"yaml": "policy: [", "revision": rev})
	if r.status != 422 || !strings.Contains(string(r.body), `"problems"`) {
		t.Fatalf("invalid save: %d %s", r.status, r.body)
	}
	// Omitting a group keeps it (groups are shared).
	c = e.checkYAML(t, strings.Replace(designerBase, "  bastions:\n    members: []\n", "", 1)+"")
	if !c.Valid && len(c.Notices) == 0 {
		t.Fatalf("omitted group: %+v %v", c.Problems, c.Notices)
	}

	// Simulation and Explain run on the draft with the same engine (on
	// hosts DROOL filters: mode Off means "not filtered", hence YES).
	e.setHost(t, serverB, map[string]any{"mode": "enforce", "rollback_timeout_s": 30})
	draft := strings.Replace(designerBase, "allow: [app-8812, https]", "allow: [https]", 1)
	var sim struct {
		Answer  string `json:"answer"`
		Summary string `json:"summary"`
	}
	e.c.do("POST", "/api/v1/firewall/simulate", map[string]any{"from_host": serverA, "to_host": serverB, "protocol": "tcp", "port": 8812}).json(t, &sim)
	if sim.Answer != "YES" || !strings.Contains(sim.Summary, "server-a-to-server-b") {
		t.Fatalf("stored: %+v", sim)
	}
	e.c.do("POST", "/api/v1/firewall/simulate", map[string]any{"from_host": serverA, "to_host": serverB, "protocol": "tcp", "port": 8812, "yaml": draft}).json(t, &sim)
	if sim.Answer != "NO" {
		t.Fatalf("draft: %+v", sim)
	}
	var ex struct {
		Explain []struct {
			Direction string `json:"direction"`
			Verdict   string `json:"verdict"`
			Service   string `json:"service"`
			Peer      string `json:"peer"`
			Reason    struct {
				Kind string `json:"kind"`
				Text string `json:"text"`
			} `json:"reason"`
		} `json:"explain"`
		Management []struct {
			Kind string `json:"kind"`
		} `json:"management"`
	}
	e.c.do("POST", "/api/v1/firewall/document/explain", map[string]any{"host_id": serverB, "yaml": draft}).json(t, &ex)
	var lines []string
	for _, a := range ex.Explain {
		lines = append(lines, fmt.Sprintf("%s %s %s %s (%s)", a.Direction, a.Verdict, a.Service, a.Peer, a.Reason.Text))
	}
	all := strings.Join(lines, "\n")
	if strings.Contains(all, "8812") || !strings.Contains(all, "in drop TCP 2222 anywhere (policy no-alt-admin)") ||
		!strings.Contains(all, "SSH management") || len(ex.Management) != 1 {
		t.Fatalf("explain:\n%s", all)
	}

	// The agent path: a host may not be denied its way to the controller.
	agentID, _, _, _ := e.agentFwHost(t, "agent1", fullCaps(""))
	eventually(t, 10*time.Second, "agent endpoints", func() bool {
		obs, _ := e.h.st.R.ListFwMgmtObservations(ctx, agentID)
		return len(obs) > 0
	})
	obs, _ := e.h.st.R.ListFwMgmtObservations(ctx, agentID)
	cport := obs[0].Port
	var ah hostJSON
	e.c.do("GET", "/api/v1/hosts/"+agentID, nil).json(t, &ah)
	agentDeny := strings.Replace(designerBase, "  admin-alt:\n", fmt.Sprintf("  ctl:\n    protocol: tcp\n    port: %d\n  admin-alt:\n", cport), 1) +
		"  - name: agent-lockdown\n    from: host:" + ah.Name + "\n    to: 127.0.0.0/8\n    deny: [ctl]\n"
	c = e.checkYAML(t, agentDeny)
	if p := c.path(ah.Name, "agent"); p != ah.Name+"→DROOL Controller=blocked[agent-lockdown]" {
		t.Fatalf("agent path: %s (%v)", p, c.Problems)
	}

	// Applying is refused while a management path is blocked; the audited
	// emergency override still goes through the dead-man transaction.
	e.setHost(t, serverB, map[string]any{"mode": "enforce", "rollback_timeout_s": 30})
	rev = e.saveYAML(t, strings.Replace(designerBase, "port: 2222", fmt.Sprintf("port: %d", pb), 1), rev)["revision"].(string)
	jh, _ := e.apply(t, serverB, nil)
	if jh.State != "failed" || !strings.Contains(jh.ErrorMessage, "would remove the management path") {
		t.Fatalf("apply with a blocked path: %+v", jh)
	}
	if r := e.c.do("POST", "/api/v1/firewall/apply", map[string]any{"host_ids": []string{serverB}, "override_mgmt": true}); r.status != 422 {
		t.Fatalf("override without a reason: %d %s", r.status, r.body)
	}
	e.h.mkUser("ops", auth.RoleOperator)
	op := e.h.login("ops")
	if r := op.do("POST", "/api/v1/firewall/apply", map[string]any{"host_ids": []string{serverB}, "override_mgmt": true, "reason": "x"}); r.status != 403 {
		t.Fatalf("operator override: %d %s", r.status, r.body)
	}
	if r := op.do("PUT", "/api/v1/firewall/document", map[string]any{"yaml": designerBase, "revision": rev}); r.status != 403 {
		t.Fatalf("operator save: %d", r.status)
	}
	if r := op.do("POST", "/api/v1/firewall/document/check", map[string]any{"yaml": designerBase}); r.status != 200 {
		t.Fatalf("operator check (read-only): %d", r.status)
	}
	_, tx := e.apply(t, serverB, map[string]any{"override_mgmt": true, "reason": "designer test: emergency override"})
	if !tx.OverrideMgmt || tx.Transport != "ssh" || !strings.Contains(tx.timeline(), "rollback timeout") {
		t.Fatalf("override transaction: %+v\n%s", tx, tx.timeline())
	}

	// Layout is stored separately from policy.
	if r := e.c.do("PUT", "/api/v1/firewall/layout", map[string]any{"nodes": map[string]any{"host:server-a": map[string]int{"x": 10, "y": 20}}, "expanded": []string{"group:ssh-managed"}}); r.status != 204 {
		t.Fatalf("layout: %d %s", r.status, r.body)
	}
	var lay map[string]any
	e.c.do("GET", "/api/v1/firewall/layout", nil).json(t, &lay)
	b, _ := json.Marshal(lay)
	if !strings.Contains(string(b), `"host:server-a":{"x":10,"y":20}`) {
		t.Fatalf("layout: %s", b)
	}
	if r := e.c.do("PUT", "/api/v1/firewall/layout", []int{1}); r.status != 422 {
		t.Fatalf("layout array: %d", r.status)
	}
	e.c.do("GET", "/api/v1/firewall/document", nil).json(t, &doc)
	if doc.Revision != rev {
		t.Fatal("layout changed the policy revision")
	}
}

// TestFirewallDesignerForwardedPort: the controller dials a forwarded port,
// the host sees its sshd on 22; the path check uses what the host sees.
func TestFirewallDesignerForwardedPort(t *testing.T) {
	e := newFwEnv(t)
	id, srv := e.sshFwHost(t, "nat1")
	_, dial := srv.Host()
	observe(t, e, id, "ssh_in", "192.0.2.10", 22)
	c := e.checkYAML(t, "policy:\n  - name: no-ssh\n    from: any\n    to: nat1\n    deny: [ssh]\n")
	if p := c.path("nat1", "ssh"); p != "DROOL Controller→nat1=blocked[no-ssh]" {
		t.Fatalf("deny of the port the host sees (22, dialled as %d): %s", dial, p)
	}
	for _, m := range c.Management {
		if m.Host == "nat1" && m.Hops[0].Port != 22 {
			t.Fatalf("hop port %d, want the observed 22", m.Hops[0].Port)
		}
	}
	c = e.checkYAML(t, fmt.Sprintf("services:\n  dialled:\n    protocol: tcp\n    port: %d\npolicy:\n  - name: no-dialled\n    from: any\n    to: nat1\n    deny: [dialled]\n", dial))
	if p := c.path("nat1", "ssh"); p != "DROOL Controller→nat1=protected[]" {
		t.Fatalf("deny of the dialled port (not seen on the host): %s", p)
	}
}

// TestFirewallDesignerUnmeasuredAtRisk: before a host's management path is
// measured, a deny of its SSH port is flagged as a risk (not silently OK).
func TestFirewallDesignerUnmeasuredAtRisk(t *testing.T) {
	e := newFwEnv(t)
	_, srv := e.sshFwHost(t, "fresh1")
	_, port := srv.Host()
	c := e.checkYAML(t, fmt.Sprintf("services:\n  sshd:\n    protocol: tcp\n    port: %d\npolicy:\n  - name: lockdown\n    from: any\n    to: fresh1\n    deny: [sshd]\n", port))
	var found bool
	for _, m := range c.Management {
		if m.Host == "fresh1" {
			found = m.Unverified && strings.Contains(m.Hops[0].Detail, "lockdown denies TCP") && m.Hops[0].Status == "unverified"
		}
	}
	if !found || len(c.Blockers) != 0 {
		t.Fatalf("unmeasured host with a deny of its port: %+v %v", c.Management, c.Blockers)
	}
}

// TestFirewallSetUpdateHeldNearManagement: a membership change that makes
// a deny overlap the management path is not pushed unattended.
func TestFirewallSetUpdateHeldNearManagement(t *testing.T) {
	e := newFwEnv(t)
	ctx := context.Background()
	hostID, srv := e.sshFwHost(t, "web1")
	_, port := srv.Host()
	srv.SetClientIP("10.9.9.9") // web1 sees DROOL's connections from 10.9.9.9 (NAT)
	svc := fmt.Sprintf("services:\n  sshd:\n    protocol: tcp\n    port: %d\n", port)
	alias, _ := e.sshFwHost(t, "ctl-alias") // a managed host that has the controller's address
	other, _ := e.sshFwHost(t, "other")
	e.h.st.W.UpsertHostAddress(ctx, upsertAddr(other, "10.8.8.8"))
	e.h.st.W.UpsertHostAddress(ctx, upsertAddr(hostID, "10.0.0.20"))
	e.h.st.W.UpsertHostAddress(ctx, upsertAddr(alias, "10.9.9.9"))
	e.setHost(t, hostID, map[string]any{"mode": "enforce", "rollback_timeout_s": 30})
	var doc struct {
		Revision string `json:"revision"`
	}
	e.c.do("GET", "/api/v1/firewall/document", nil).json(t, &doc)
	rev := e.saveYAML(t, "groups:\n  blocked:\n    members: [other]\n"+svc+"policy:\n  - name: no-ssh-from-blocked\n    from: blocked\n    to: web1\n    deny: [sshd]\n", doc.Revision)["revision"].(string)
	if _, tx := e.apply(t, hostID, nil); tx.State != "committed" {
		t.Fatalf("baseline: %+v", tx)
	}
	// The designer warns before saving...
	c := e.checkYAML(t, "groups:\n  blocked:\n    members: [ctl-alias, other]\n"+svc+"policy:\n  - name: no-ssh-from-blocked\n    from: blocked\n    to: web1\n    deny: [sshd]\n")
	if len(c.Blockers) == 0 || !strings.Contains(c.path("web1", "ssh"), "blocked[no-ssh-from-blocked]") {
		t.Fatalf("designer: %v %s", c.Blockers, c.path("web1", "ssh"))
	}
	// ...and if it is saved anyway, the converger holds the set update.
	e.saveYAML(t, "groups:\n  blocked:\n    members: [ctl-alias, other]\n"+svc+"policy:\n  - name: no-ssh-from-blocked\n    from: blocked\n    to: web1\n    deny: [sshd]\n", rev)
	e.h.fw.Kick()
	var plan struct {
		Applied struct {
			SetsError string `json:"sets_error"`
		} `json:"applied"`
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		r := e.c.do("GET", "/api/v1/firewall/hosts/"+hostID, nil)
		r.json(t, &plan)
		if strings.Contains(plan.Applied.SetsError, "held") {
			break
		}
		if time.Now().After(deadline) {
			var dbg struct {
				Applied any `json:"applied"`
				Policy  struct {
					Conflicts any `json:"conflicts"`
				} `json:"policy"`
			}
			r.json(t, &dbg)
			t.Fatalf("not held: %+v", dbg)
		}
		time.Sleep(100 * time.Millisecond)
	}
	if live := fileText(e.h.fwFake + "/live"); !strings.Contains(live, "elements = { 10.8.8.8 }") {
		t.Fatalf("the set update was pushed:\n%s", live)
	}
}
