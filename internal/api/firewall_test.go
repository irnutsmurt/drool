package api

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"drool/internal/agent"
	"drool/internal/agentcaps"
	"drool/internal/auth"
	"drool/internal/firewall/hostfw"
	"drool/internal/netmon/collect"
	"drool/internal/sshx/sshtest"
	"drool/internal/store/db"
)

// End-to-end firewall transactions in one process (ARCHITECTURE-ALPHA
// §24): the real controller, the real agent over mTLS, an in-process SSH
// server, and the real drool-fw tool — with fake nft, systemd-run and
// systemctl, so nothing touches this machine's firewall. The fake
// systemd-run really fires its timer in the background, so a dead agent or
// an unreachable host is rolled back exactly as on a real host.

const e2eFakeNft = `#!/bin/sh
case "$*" in
"--version") echo "nftables v1.0.6 (fake)"; exit 0 ;;
"-j list ruleset") echo '{"nftables": [{"metainfo": {"version": "1.0.6"}}]}'; exit 0 ;;
"-c -f "*) grep -q INVALID "$3" && { echo "Error: syntax error" >&2; exit 1; }; exit 0 ;;
"-f -") cat > "$FAKE/in"; f="$FAKE/in" ;;
"-f "*) f=$2 ;;
"list table inet drool") [ -s "$FAKE/live" ] && exit 0; echo "Error: No such file or directory" >&2; exit 1 ;;
"-s list table inet drool") cat "$FAKE/live"; exit 0 ;;
*) echo "fake nft: unsupported: $*" >&2; exit 2 ;;
esac
if grep -q '^delete table inet drool' "$f"; then
	sed '1,/^delete table inet drool/d' "$f" > "$FAKE/live"
else
	cat "$f" >> "$FAKE/live"
fi
`

const e2eFakeSystemdRun = `#!/bin/sh
unit= delay=
while [ $# -gt 0 ]; do
	case $1 in
	--unit=*) unit=${1#--unit=} ;;
	--on-active=*) delay=${1#--on-active=}; delay=${delay%s} ;;
	--*) ;;
	*) break ;;
	esac
	shift
done
echo "$unit $delay $*" >> "$FAKE/timers"
( sleep "$delay"; "$@" ) >/dev/null 2>&1 9>&- &
echo $! > "$FAKE/$unit.pid"
`

const e2eFakeSystemctl = `#!/bin/sh
echo "$*" >> "$FAKE/systemctl.log"
case $1 in
stop) u=${2%.timer}; [ -f "$FAKE/$u.pid" ] && kill "$(cat "$FAKE/$u.pid")" 2>/dev/null; rm -f "$FAKE/$u.pid" ;;
is-active) echo inactive; exit 3 ;;
cat) exit 1 ;;
esac
exit 0
`

// fakeHostDir is one host's firewall tooling directory and fake kernel.
func fakeHostDir(t *testing.T, name string) (string, hostfw.Paths) {
	dir := filepath.Join(t.TempDir(), name)
	fake := filepath.Join(dir, "fake")
	os.MkdirAll(fake, 0o755)
	t.Cleanup(func() {
		pids, _ := filepath.Glob(filepath.Join(fake, "*.pid"))
		for _, p := range pids {
			b, _ := os.ReadFile(p)
			var pid int
			json.Unmarshal(b, &pid)
			if pid > 0 {
				syscall.Kill(pid, syscall.SIGKILL)
			}
		}
	})
	return fake, hostfw.Paths{ToolDir: filepath.Join(dir, "tool"), UnitFile: filepath.Join(dir, "drool-firewall.service"),
		Env: "FAKE=" + fake + " DROOL_FW_STATE=" + filepath.Join(dir, "state") + " DROOL_FW_SELF=" + filepath.Join(dir, "tool", "drool-fw") +
			" DROOL_FW_LOCK=" + filepath.Join(dir, "lock")}
}

// installFakes puts the fake firewall binaries first on PATH. Detection
// runs as "root" (fake id) and sees no Docker on this machine.
func installFakes(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "bin")
	os.MkdirAll(bin, 0o755)
	for n, s := range map[string]string{"nft": e2eFakeNft, "systemd-run": e2eFakeSystemdRun, "systemctl": e2eFakeSystemctl,
		"id": "#!/bin/sh\necho 0\n", "pidof": "#!/bin/sh\nexit 1\n"} {
		os.WriteFile(filepath.Join(bin, n), []byte(s), 0o755)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	t.Setenv("FAKE", t.TempDir())
	t.Setenv("DROOL_FW_DOCKER_SOCK", filepath.Join(t.TempDir(), "no-docker.sock"))
}

func fileText(path string) string { b, _ := os.ReadFile(path); return string(b) }

type fwTx struct {
	ID        string `json:"id"`
	State     string `json:"state"`
	Transport string `json:"transport"`
	Revision  int64  `json:"revision"`
	Detail    string `json:"detail"`
	// OverrideMgmt: applied under the audited emergency override.
	OverrideMgmt bool `json:"override_mgmt"`
	Steps        []struct {
		Text string `json:"text"`
	} `json:"steps"`
}

func (tx fwTx) timeline() string {
	var b strings.Builder
	for _, s := range tx.Steps {
		b.WriteString(s.Text + "\n")
	}
	return b.String()
}

type fwEnv struct {
	*agentEnv
}

func newFwEnv(t *testing.T) *fwEnv {
	installFakes(t)
	return &fwEnv{newAgentEnv(t)}
}

// apply runs a firewall transaction job and returns the host result and
// the newest transaction.
func (e *fwEnv) apply(t *testing.T, hostID string, extra map[string]any) (jobHostResp, fwTx) {
	t.Helper()
	body := map[string]any{"host_ids": []string{hostID}}
	for k, v := range extra {
		body[k] = v
	}
	r := e.c.do("POST", "/api/v1/firewall/apply", body)
	if r.status != 201 {
		t.Fatalf("apply: %d %s", r.status, r.body)
	}
	var j jobJSON
	r.json(t, &j)
	deadline := time.Now().Add(3 * time.Minute)
	for {
		var cur jobJSON
		e.c.do("GET", "/api/v1/jobs/"+j.ID, nil).json(t, &cur)
		if cur.Status == "completed" || cur.Status == "cancelled" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("firewall job stuck: %s", cur.Status)
		}
		time.Sleep(100 * time.Millisecond)
	}
	var jh jobHostResp
	e.c.do("GET", "/api/v1/jobs/"+j.ID+"/hosts/"+hostID, nil).json(t, &jh)
	return jh, e.lastTx(t, hostID)
}

func (e *fwEnv) lastTx(t *testing.T, hostID string) fwTx {
	var list struct {
		Items []fwTx `json:"items"`
	}
	e.c.do("GET", "/api/v1/firewall/transactions?host_id="+hostID, nil).json(t, &list)
	if len(list.Items) == 0 {
		return fwTx{}
	}
	var full fwTx
	e.c.do("GET", "/api/v1/firewall/transactions/"+list.Items[0].ID, nil).json(t, &full)
	return full
}

func (e *fwEnv) setHost(t *testing.T, hostID string, cfg map[string]any) {
	t.Helper()
	if r := e.c.do("PUT", "/api/v1/firewall/hosts/"+hostID, cfg); r.status != 200 {
		t.Fatalf("host config: %d %s", r.status, r.body)
	}
}

func (e *fwEnv) policy(t *testing.T, p map[string]any) string {
	t.Helper()
	r := e.c.do("POST", "/api/v1/firewall/policies", p)
	if r.status != 201 {
		t.Fatalf("policy: %d %s", r.status, r.body)
	}
	var out struct {
		ID string `json:"id"`
	}
	r.json(t, &out)
	return out.ID
}

// sshFwHost adds an SSH host (in-process server, root login) whose
// tooling lives in the harness's fake host directory.
func (e *fwEnv) sshFwHost(t *testing.T, name string) (string, *sshtest.Server) {
	t.Helper()
	var ck controllerKeyResponse
	e.c.do("GET", "/api/v1/secrets/controller-ssh-key", nil).json(t, &ck)
	key := sshtest.NewEd25519Signer(t)
	srv := sshtest.Start(t, key)
	srv.SetExec(sshtest.ExecLocal)
	srv.SetConnectionEnv(true)
	authorizeCred(t, srv, ck.PublicKey)
	addr, port := srv.Host()
	var host hostJSON
	e.c.do("POST", "/api/v1/hosts", map[string]any{"name": name, "address": addr, "port": port, "user": "root", "become_method": "none"}).json(t, &host)
	e.c.do("POST", "/api/v1/hosts/"+host.ID+"/hostkeys/scan", nil)
	if r := e.c.do("POST", "/api/v1/hosts/"+host.ID+"/hostkeys/approve", map[string]string{"fingerprint": ssh.FingerprintSHA256(key.PublicKey())}); r.status != 200 {
		t.Fatalf("approve: %s", r.body)
	}
	return host.ID, srv
}

// agentFwHost enrolls an agent-only host whose tooling lives in its own
// fake host directory.
func (e *fwEnv) agentFwHost(t *testing.T, name string, caps agentcaps.Caps, mods ...func(*agent.Options)) (string, *runningAgent, string, hostfw.Paths) {
	t.Helper()
	p := agentPaths(t, caps)
	tok := e.token(t, map[string]any{"preset": "full", "description": name})
	id, err := agent.Enroll(context.Background(), p, tok.Token, "v1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	fake, fp := fakeHostDir(t, name)
	opts := agent.Options{Paths: p, Version: "v1.0.0", StatusEvery: time.Second, RenewCheck: time.Hour, Supervisor: "exit", FirewallPaths: &fp,
		// The in-process agent runs on the developer's machine: it must not
		// read that machine's sockets or conntrack.
		NetCollect: &collect.Options{DisableConntrack: true, SS: func(context.Context) (string, error) { return "", nil }},
		// Nor its /proc, /sys, disks or kernel log (system monitoring).
		Monitor: fixtureMonitor(t)}
	for _, m := range mods {
		m(&opts)
	}
	a, err := agent.New(opts)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	ra := &runningAgent{a: a, cancel: cancel, done: make(chan struct{})}
	go func() { a.Run(ctx); close(ra.done) }()
	t.Cleanup(func() {
		select {
		case <-ra.done:
		default:
			ra.stop()
		}
	})
	eventually(t, 10*time.Second, "agent session", func() bool { return e.connected(id.HostID) })
	// The address the policy uses for this host.
	e.h.st.W.UpsertHostAddress(context.Background(), upsertAddr(id.HostID, "10.20.0."+strings.TrimPrefix(name, "agent")))
	return id.HostID, ra, fake, fp
}

func TestFirewallAgentTransaction(t *testing.T) {
	e := newFwEnv(t)
	hostID, ag, fake, fp := e.agentFwHost(t, "agent1", fullCaps(""))
	ctx := context.Background()

	// Inspection works before anything is enforced.
	var det struct {
		Eligibility struct {
			Supported bool     `json:"supported"`
			Reasons   []string `json:"reasons"`
		} `json:"eligibility"`
	}
	if r := e.c.do("POST", "/api/v1/firewall/hosts/"+hostID+"/detect", nil); r.status != 200 {
		t.Fatalf("detect: %d %s", r.status, r.body)
	} else {
		r.json(t, &det)
	}
	if !det.Eligibility.Supported {
		t.Fatalf("plain (fake) nftables host refused: %v", det.Eligibility.Reasons)
	}
	// Monitor mode: applying is refused. The agent learns (the setting is
	// pushed after the change commits, not from inside its transaction).
	// Since M10 collection follows the network settings: every agent host
	// by default, or (collection "firewall") only hosts whose firewall mode
	// isn't off — the M8 behaviour.
	e.setHost(t, hostID, map[string]any{"mode": "monitor"})
	eventually(t, 5*time.Second, "learning on", func() bool { return ag.a.Learning() })
	e.setHost(t, hostID, map[string]any{"mode": "off"})
	time.Sleep(300 * time.Millisecond)
	if !ag.a.Learning() {
		t.Fatal("collection stopped with the firewall mode although network collection is on for all hosts")
	}
	if r := e.c.do("PUT", "/api/v1/network/settings", map[string]any{"collection": "firewall", "retention_days": 30, "max_flows_per_host": 5000, "enrichment": true}); r.status != 200 {
		t.Fatalf("network settings: %d %s", r.status, r.body)
	}
	eventually(t, 5*time.Second, "learning off", func() bool { return !ag.a.Learning() })
	e.setHost(t, hostID, map[string]any{"mode": "monitor"})
	eventually(t, 5*time.Second, "learning on again", func() bool { return ag.a.Learning() })
	if jh, _ := e.apply(t, hostID, nil); jh.State != "failed" || !strings.Contains(jh.ErrorMessage, "Enforce") {
		t.Fatalf("apply in monitor mode: %+v", jh)
	}
	e.setHost(t, hostID, map[string]any{"mode": "enforce", "rollback_timeout_s": 30})
	e.policy(t, map[string]any{"name": "web-in", "action": "allow", "from": []any{map[string]any{"kind": "internet"}},
		"to": []any{map[string]any{"kind": "host", "id": hostID}}, "services": []string{"https"}})

	before, _ := e.h.agents.HostFirewallSession(hostID)
	jh, tx := e.apply(t, hostID, nil)
	if jh.State != "succeeded" || tx.State != "committed" || tx.Transport != "agent" || tx.Revision != 1 {
		t.Fatalf("agent transaction: %+v %+v\n%s", jh, tx, tx.timeline())
	}
	// Verified and committed over a brand-new connection.
	after, _ := e.h.agents.HostFirewallSession(hostID)
	if !after.ConnectedAt.After(before.ConnectedAt) || !strings.Contains(tx.timeline(), "new connection at") {
		t.Fatalf("not committed over a new connection:\n%s", tx.timeline())
	}
	live := fileText(filepath.Join(fake, "live"))
	if !strings.Contains(live, "tcp dport { 443 } accept comment \"policy web-in\"") || !strings.Contains(live, "policy drop;") {
		t.Fatalf("live policy:\n%s", live)
	}
	state := filepath.Join(filepath.Dir(fp.ToolDir), "state")
	if j := fileText(filepath.Join(state, "transaction.json")); !strings.Contains(j, `"state": "committed"`) {
		t.Fatalf("journal: %s", j)
	}
	if u := fileText(fp.UnitFile); !strings.Contains(u, "ExecStart=/usr/libexec/drool-firewall/drool-fw boot") {
		t.Fatalf("boot unit: %q", u)
	}
	// The timer was armed and cancelled.
	if !strings.Contains(fileText(filepath.Join(fake, "timers")), "drool-fw-rollback-"+tx.ID+" 30") ||
		!strings.Contains(fileText(filepath.Join(fake, "systemctl.log")), "stop drool-fw-rollback-"+tx.ID+".timer") {
		t.Fatal("timer not armed/cancelled")
	}

	// Explain Host and simulation.
	var plan struct {
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
		Summary struct {
			Status string `json:"status"`
		} `json:"summary"`
	}
	e.c.do("GET", "/api/v1/firewall/hosts/"+hostID, nil).json(t, &plan)
	var ex []string
	for _, a := range plan.Explain {
		ex = append(ex, a.Direction+" "+a.Verdict+" "+a.Service+" "+a.Peer+" ("+a.Reason.Text+")")
	}
	all := strings.Join(ex, "\n")
	if !strings.Contains(all, "in accept TCP 443 the internet (policy web-in)") || plan.Summary.Status != "in_sync" {
		t.Fatalf("explain (%s):\n%s", plan.Summary.Status, all)
	}
	var sim struct {
		Answer  string `json:"answer"`
		Summary string `json:"summary"`
	}
	e.c.do("POST", "/api/v1/firewall/simulate", map[string]any{"from_address": "8.8.8.8", "to_host": hostID, "protocol": "tcp", "port": 443}).json(t, &sim)
	if sim.Answer != "YES" || !strings.Contains(sim.Summary, "web-in") {
		t.Fatalf("simulate allowed: %+v", sim)
	}
	e.c.do("POST", "/api/v1/firewall/simulate", map[string]any{"from_address": "8.8.8.8", "to_host": hostID, "protocol": "tcp", "port": 5432}).json(t, &sim)
	if sim.Answer != "NO" {
		t.Fatalf("simulate blocked: %+v", sim)
	}

	// Agent process death after the candidate was applied: the rollback
	// timer survives it and restores the previous policy.
	e.h.fw.SetTestCommitDelay(2 * time.Second)
	e.policy(t, map[string]any{"name": "db-in", "action": "allow", "from": []any{map[string]any{"kind": "internal"}},
		"to": []any{map[string]any{"kind": "host", "id": hostID}}, "services": []string{"postgres"}})
	done := make(chan struct{})
	go func() {
		defer close(done)
		eventually(t, 20*time.Second, "candidate applied", func() bool {
			return strings.Contains(fileText(filepath.Join(fake, "live")), "policy db-in")
		})
		ag.stop() // the agent dies and does not come back
	}()
	jh, tx = e.apply(t, hostID, nil)
	<-done
	if jh.State != "failed" || (tx.State != "rolled_back" && tx.State != "unknown") {
		t.Fatalf("after agent death: %+v %+v\n%s", jh, tx, tx.timeline())
	}
	eventually(t, 40*time.Second, "timer rollback on the host", func() bool {
		return strings.Contains(fileText(filepath.Join(state, "transaction.json")), `"state": "rolled_back"`)
	})
	live = fileText(filepath.Join(fake, "live"))
	if strings.Contains(live, "policy db-in") || !strings.Contains(live, "policy web-in") {
		t.Fatalf("previous policy not restored:\n%s", live)
	}
	// The agent comes back: the rollback is reported on reconnect.
	e.h.fw.SetTestCommitDelay(0)
	a2, err := agent.New(agent.Options{Paths: agentPathsOf(ag), Version: "v1.0.0", StatusEvery: time.Second, RenewCheck: time.Hour, Supervisor: "exit", FirewallPaths: &fp, Monitor: fixtureMonitor(t)})
	if err != nil {
		t.Fatal(err)
	}
	actx, acancel := context.WithCancel(ctx)
	defer acancel()
	go a2.Run(actx)
	eventually(t, 30*time.Second, "rollback reported", func() bool { return e.lastTx(t, hostID).State == "rolled_back" })
	if tx := e.lastTx(t, hostID); !strings.Contains(tx.Detail, "rollback timer fired") {
		t.Fatalf("reported reason: %+v", tx)
	}
}

func agentPathsOf(r *runningAgent) agent.Paths { return r.a.Paths() }

func TestFirewallAgentCapabilityDenied(t *testing.T) {
	e := newFwEnv(t)
	noFw := fullCaps("")
	noFw.Firewall = false
	hostID, _, fake, _ := e.agentFwHost(t, "agent2", noFw)
	e.setHost(t, hostID, map[string]any{"mode": "enforce", "rollback_timeout_s": 30})
	jh, _ := e.apply(t, hostID, nil)
	if jh.State != "failed" || jh.ErrorClass != "CapabilityError" || jh.Transport == "ssh" {
		t.Fatalf("capability denial: %+v", jh)
	}
	if fileText(filepath.Join(fake, "live")) != "" {
		t.Fatal("something was applied")
	}
}

func TestFirewallSSHTransaction(t *testing.T) {
	e := newFwEnv(t)
	hostID, srv := e.sshFwHost(t, "ssh1")
	e.h.st.W.UpsertHostAddress(context.Background(), upsertAddr(hostID, "10.30.0.5"))
	e.setHost(t, hostID, map[string]any{"mode": "enforce", "rollback_timeout_s": 30})
	conns := srv.Connections()
	jh, tx := e.apply(t, hostID, nil)
	if jh.State != "succeeded" || tx.State != "committed" || tx.Transport != "ssh" {
		t.Fatalf("SSH transaction: %+v %+v\n%s", jh, tx, tx.timeline())
	}
	if !strings.Contains(tx.timeline(), "brand-new SSH connection") || srv.Connections() < conns+2 {
		t.Fatalf("no fresh connections (%d → %d):\n%s", conns, srv.Connections(), tx.timeline())
	}
	// The management path was measured and protected: SSH from where the
	// host saw the controller, before any deny.
	live := fileText(filepath.Join(e.h.fwFake, "live"))
	if !strings.Contains(live, "ip saddr { 127.0.0.1 } tcp dport { ") || !strings.Contains(live, "management: SSH management from the controller") {
		t.Fatalf("management path not in the policy:\n%s", live)
	}

	// A deny that removes the management path is refused...
	pid := e.policy(t, map[string]any{"name": "lockout", "action": "deny", "from": []any{map[string]any{"kind": "any"}},
		"to": []any{map[string]any{"kind": "host", "id": hostID}}, "services": []string{"ssh"}})
	_, port := srv.Host()
	if r := e.c.do("POST", "/api/v1/firewall/services", map[string]any{"name": "testssh", "entries": []any{map[string]any{"protocol": "tcp", "ports": itoa(port)}}}); r.status != 201 {
		t.Fatalf("service: %s", r.body)
	}
	e.c.do("PUT", "/api/v1/firewall/policies/"+pid, map[string]any{"name": "lockout", "action": "deny", "from": []any{map[string]any{"kind": "any"}},
		"to": []any{map[string]any{"kind": "host", "id": hostID}}, "services": []string{"ssh", "testssh"}})
	jh, _ = e.apply(t, hostID, nil)
	if jh.State != "failed" || !strings.Contains(jh.ErrorMessage, "would remove the management path") {
		t.Fatalf("management protection: %+v", jh)
	}
	// ...the override needs a reason and firewall.override...
	if r := e.c.do("POST", "/api/v1/firewall/apply", map[string]any{"host_ids": []string{hostID}, "override_mgmt": true}); r.status != 422 {
		t.Fatalf("override without a reason: %d", r.status)
	}
	// ...and even with it, the dead-man transaction decides: the path is
	// "blocked" (the server refuses new connections), so nothing commits
	// and the host's timer restores the previous policy.
	go func() {
		eventually(t, 30*time.Second, "candidate applied", func() bool {
			return strings.Contains(fileText(filepath.Join(e.h.fwFake, "live")), "lockout")
		})
		srv.Restart(35*time.Second, nil)
	}()
	jh, tx = e.apply(t, hostID, map[string]any{"override_mgmt": true, "reason": "testing the dead-man rollback"})
	if jh.State != "failed" || tx.State != "rolled_back" {
		t.Fatalf("blocked path: %+v %+v\n%s", jh, tx, tx.timeline())
	}
	live = fileText(filepath.Join(e.h.fwFake, "live"))
	if strings.Contains(live, "lockout") || !strings.Contains(live, "management: SSH management") {
		t.Fatalf("not restored:\n%s", live)
	}
	e.c.do("DELETE", "/api/v1/firewall/policies/"+pid, nil)

	// While a transaction is pending, reboots and network restarts are refused.
	e.h.fw.SetTestCommitDelay(4 * time.Second)
	defer e.h.fw.SetTestCommitDelay(0)
	e.policy(t, map[string]any{"name": "ping-in", "action": "allow", "from": []any{map[string]any{"kind": "internal"}},
		"to": []any{map[string]any{"kind": "host", "id": hostID}}, "services": []string{"ping"}})
	go func() {
		eventually(t, 30*time.Second, "pending", func() bool { return e.lastTx(t, hostID).State == "pending" })
		reason := e.h.fw.PendingReason(context.Background(), hostID)
		if !strings.Contains(reason, "firewall transaction") {
			t.Errorf("pending reason: %q", reason)
		}
		r := e.c.do("POST", "/api/v1/jobs", map[string]any{"steps": []any{map[string]any{"type": "reboot"}},
			"targets": map[string]any{"hosts": []string{hostID}}})
		if r.status != 409 || !strings.Contains(string(r.body), "firewall") {
			t.Errorf("reboot while pending: %d %s", r.status, r.body)
		}
	}()
	jh, tx = e.apply(t, hostID, nil)
	if tx.State != "committed" {
		t.Fatalf("delayed commit: %+v\n%s", tx, tx.timeline())
	}
	if e.h.fw.PendingReason(context.Background(), hostID) != "" {
		t.Fatal("still pending after commit")
	}

	// Drift: DROOL's table was changed by hand. Refused unless replaced.
	os.WriteFile(filepath.Join(e.h.fwFake, "live"), []byte("tampered\n"), 0o644)
	e.c.do("POST", "/api/v1/firewall/hosts/"+hostID+"/detect", nil)
	var plan struct {
		Summary struct{ Status, Drift string } `json:"summary"`
	}
	e.c.do("GET", "/api/v1/firewall/hosts/"+hostID, nil).json(t, &plan)
	if plan.Summary.Status != "drift" {
		t.Fatalf("drift not shown: %+v", plan.Summary)
	}
	jh, _ = e.apply(t, hostID, nil)
	if jh.State != "failed" || !strings.Contains(jh.ErrorMessage, "drift") {
		t.Fatalf("drift: %+v", jh)
	}
	jh, tx = e.apply(t, hostID, map[string]any{"allow_drift": true, "reason": "replace hand edits"})
	if tx.State != "committed" || strings.Contains(fileText(filepath.Join(e.h.fwFake, "live")), "tampered") {
		t.Fatalf("replace drift: %+v\n%s", tx, tx.timeline())
	}

	// Address churn: a new member's address reaches the host as a set
	// update, without a dead-man transaction.
	other, _ := e.sshFwHost(t, "peer1")
	e.h.st.W.UpsertHostAddress(context.Background(), upsertAddr(other, "10.30.0.77"))
	var g struct {
		ID string `json:"id"`
	}
	e.c.do("POST", "/api/v1/groups", map[string]any{"name": "peers", "host_ids": []string{other}}).json(t, &g)
	e.policy(t, map[string]any{"name": "peers-in", "action": "allow", "from": []any{map[string]any{"kind": "group", "id": g.ID}},
		"to": []any{map[string]any{"kind": "host", "id": hostID}}, "services": []string{"redis"}})
	_, tx = e.apply(t, hostID, nil)
	if tx.State != "committed" || !strings.Contains(fileText(filepath.Join(e.h.fwFake, "live")), "10.30.0.77") {
		t.Fatalf("group policy: %+v", tx)
	}
	txBefore := tx.ID
	e.h.st.W.UpsertHostAddress(context.Background(), upsertAddr(other, "10.30.0.78"))
	e.h.fw.Kick()
	eventually(t, 30*time.Second, "set update", func() bool {
		return strings.Contains(fileText(filepath.Join(e.h.fwFake, "live")), "add element inet drool g_")
	})
	if !strings.Contains(fileText(filepath.Join(e.h.fwFake, "live")), "10.30.0.78") || e.lastTx(t, hostID).ID != txBefore {
		t.Fatal("set update missing or done as a transaction")
	}
	var prop struct {
		Enforced, Current int
	}
	eventually(t, 10*time.Second, "propagation current", func() bool {
		e.c.do("GET", "/api/v1/firewall/propagation", nil).json(t, &prop)
		return prop.Enforced >= 1 && prop.Current == prop.Enforced
	})
}

func TestFirewallRefusalsAndPermissions(t *testing.T) {
	e := newFwEnv(t)
	hostID, srv := e.sshFwHost(t, "ssh2")
	e.setHost(t, hostID, map[string]any{"mode": "enforce", "rollback_timeout_s": 30})
	// Docker on the host: enforcement refused, inspection still works.
	sock := filepath.Join(t.TempDir(), "docker.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	t.Setenv("DROOL_FW_DOCKER_SOCK", sock)
	r := e.c.do("POST", "/api/v1/firewall/hosts/"+hostID+"/detect", nil)
	if r.status != 200 || !strings.Contains(string(r.body), "Docker is running") {
		t.Fatalf("detect: %d %s", r.status, r.body)
	}
	jh, _ := e.apply(t, hostID, nil)
	if jh.State != "failed" || jh.ErrorClass != "ConfigurationError" || !strings.Contains(jh.ErrorMessage, "Docker is running") {
		t.Fatalf("Docker host: %+v", jh)
	}
	if fileText(filepath.Join(e.h.fwFake, "live")) != "" {
		t.Fatal("applied on a refused host")
	}
	t.Setenv("DROOL_FW_DOCKER_SOCK", filepath.Join(t.TempDir(), "none"))

	// A host whose committed revision is ahead (controller restored from
	// an older backup) is refused until adopted.
	jh, tx := e.apply(t, hostID, nil)
	if tx.State != "committed" {
		t.Fatalf("baseline: %+v %+v", jh, tx)
	}
	e.h.st.Writer.Exec(`UPDATE state_versions SET revision = 0 WHERE host_id = ? AND domain = 'firewall'`, hostID)
	e.c.do("POST", "/api/v1/firewall/hosts/"+hostID+"/detect", nil)
	jh, _ = e.apply(t, hostID, nil)
	if jh.State != "failed" || !strings.Contains(jh.ErrorMessage, "reconciliation") {
		t.Fatalf("host ahead: %+v", jh)
	}
	if r := e.c.do("POST", "/api/v1/firewall/hosts/"+hostID+"/adopt", nil); r.status != 204 {
		t.Fatalf("adopt: %d %s", r.status, r.body)
	}
	if _, tx = e.apply(t, hostID, nil); tx.State != "committed" || tx.Revision != 2 {
		t.Fatalf("after adopt: %+v", tx)
	}

	// Docker installed after DROOL enforced: applying is refused, but
	// DROOL's own policy can still be removed (it touches only inet drool).
	t.Setenv("DROOL_FW_DOCKER_SOCK", sock)
	if jh, _ = e.apply(t, hostID, nil); jh.State != "failed" || !strings.Contains(jh.ErrorMessage, "Docker is running") {
		t.Fatalf("apply next to Docker: %+v", jh)
	}
	if jh, tx = e.apply(t, hostID, map[string]any{"remove": true}); tx.State != "committed" {
		t.Fatalf("remove next to Docker: %+v %+v", jh, tx)
	}
	if live := fileText(filepath.Join(e.h.fwFake, "live")); strings.Contains(live, "policy") {
		t.Fatalf("after removal:\n%s", live)
	}
	t.Setenv("DROOL_FW_DOCKER_SOCK", filepath.Join(t.TempDir(), "none"))

	// A rollback timeout shorter than 3x the measured path is refused (a
	// slow path: the fresh connection takes about 4s here).
	srv.SetExec(func(cmd string, stdin []byte) (string, string, int) {
		if strings.Contains(cmd, "printf 'conn=") {
			time.Sleep(4 * time.Second)
		}
		return sshtest.ExecLocal(cmd, stdin)
	})
	e.h.st.Writer.Exec(`UPDATE fw_host_config SET rollback_timeout_s = 10 WHERE host_id = ?`, hostID)
	jh, _ = e.apply(t, hostID, nil)
	if jh.State != "failed" || !strings.Contains(jh.ErrorMessage, "shorter than 3x") {
		t.Fatalf("short timeout: %+v", jh)
	}

	// Permissions: operators read, only admins write or override; the
	// generic jobs API can't be used to bypass that.
	e.h.mkUser("op", auth.RoleOperator)
	op := e.h.login("op")
	if r := op.do("GET", "/api/v1/firewall/hosts", nil); r.status != 200 {
		t.Fatalf("operator read: %d", r.status)
	}
	if r := op.do("POST", "/api/v1/firewall/apply", map[string]any{"host_ids": []string{hostID}}); r.status != 403 {
		t.Fatalf("operator apply: %d", r.status)
	}
	if r := op.do("POST", "/api/v1/jobs", map[string]any{"steps": []any{map[string]any{"type": "firewall_apply"}},
		"targets": map[string]any{"hosts": []string{hostID}}}); r.status != 403 {
		t.Fatalf("operator via jobs API: %d %s", r.status, r.body)
	}
	if r := e.c.do("POST", "/api/v1/jobs", map[string]any{"steps": []any{map[string]any{"type": "firewall", "kind": "commit"}},
		"targets": map[string]any{"hosts": []string{hostID}}}); r.status != 422 {
		t.Fatalf("internal op via jobs API: %d %s", r.status, r.body)
	}
}

func itoa(n int) string { return strconv.Itoa(n) }

func upsertAddr(hostID, addr string) db.UpsertHostAddressParams {
	now := time.Now().UnixMilli()
	fam := int64(4)
	if strings.Contains(addr, ":") {
		fam = 6
	}
	return db.UpsertHostAddressParams{HostID: hostID, Address: addr, Family: fam, Source: "discovered", FirstSeenAt: now, LastSeenAt: now}
}

func TestFirewallPolicyPreview(t *testing.T) {
	e := newFwEnv(t)
	hostID, _ := e.sshFwHost(t, "prev1")
	other, _ := e.sshFwHost(t, "prev2")
	e.h.st.W.UpsertHostAddress(context.Background(), upsertAddr(hostID, "10.40.0.1"))
	var res struct {
		Items []struct {
			HostID string   `json:"host_id"`
			Added  []string `json:"added"`
		} `json:"items"`
	}
	draft := map[string]any{"name": "pub", "action": "allow", "from": []any{map[string]any{"kind": "internet"}},
		"to": []any{map[string]any{"kind": "host", "id": hostID}}, "services": []string{"https"}}
	if r := e.c.do("POST", "/api/v1/firewall/policies/preview", map[string]any{"policy": draft}); r.status != 200 {
		t.Fatalf("preview: %d %s", r.status, r.body)
	} else {
		r.json(t, &res)
		// Arrays are always arrays (the UI maps over them; OpenAPI requires them).
		if strings.Contains(string(r.body), `"removed":null`) || strings.Contains(string(r.body), `"added":null`) {
			t.Fatalf("null array in %s", r.body)
		}
	}
	if len(res.Items) != 1 || res.Items[0].HostID != hostID || len(res.Items[0].Added) != 1 ||
		!strings.Contains(res.Items[0].Added[0], "allow inbound TCP 443 from the internet (policy pub)") {
		t.Fatalf("impact: %+v", res.Items)
	}
	// Nothing was saved.
	var pols struct {
		Items []any `json:"items"`
	}
	e.c.do("GET", "/api/v1/firewall/policies", nil).json(t, &pols)
	if len(pols.Items) != 0 {
		t.Fatal("preview saved the policy")
	}
	// Deleting an existing policy shows what goes away.
	id := e.policy(t, draft)
	e.c.do("POST", "/api/v1/firewall/policies/preview", map[string]any{"id": id, "delete": true}).json(t, &res)
	if len(res.Items) != 1 || res.Items[0].HostID != hostID {
		t.Fatalf("delete impact: %+v (other host %s)", res.Items, other)
	}
}

// TestFirewallJSONArrays: list-valued fields are always arrays, never null,
// on a fresh installation too (the UI maps over them; found in the browser
// pass: null "stale" crashed the Firewall page).
func TestFirewallJSONArrays(t *testing.T) {
	e := newFwEnv(t)
	hostID, _ := e.sshFwHost(t, "arr1")
	gets := []string{"/api/v1/firewall/hosts", "/api/v1/firewall/propagation", "/api/v1/firewall/hosts/" + hostID,
		"/api/v1/firewall/transactions", "/api/v1/firewall/policies", "/api/v1/firewall/services", "/api/v1/firewall/groups",
		"/api/v1/firewall/hosts/" + hostID + "/observations", "/api/v1/firewall/hosts/" + hostID + "/suggestions", "/api/v1/firewall/settings"}
	bodies := map[string]string{}
	for _, p := range gets {
		r := e.c.do("GET", p, nil)
		if r.status != 200 {
			t.Fatalf("%s: %d %s", p, r.status, r.body)
		}
		bodies[p] = string(r.body)
	}
	r := e.c.do("POST", "/api/v1/firewall/simulate", map[string]any{"from_address": "8.8.8.8", "to_host": hostID, "protocol": "tcp", "port": 443})
	bodies["simulate"] = string(r.body)
	// Check records the SSH management path: Explain shows it before any
	// transaction, and the "not observed yet" warning goes away.
	if r := e.c.do("POST", "/api/v1/firewall/hosts/"+hostID+"/detect", nil); r.status != 200 {
		t.Fatalf("detect: %d %s", r.status, r.body)
	}
	plan := string(e.c.do("GET", "/api/v1/firewall/hosts/"+hostID, nil).body)
	if !strings.Contains(plan, "SSH management from the controller") || strings.Contains(plan, "has not been observed yet") {
		t.Errorf("management path after Check: %s", plan)
	}
	arrays := []string{"items", "stale", "rules_changed", "explain", "changes_added", "changes_removed", "hops", "pairs", "steps", "added", "removed", "internal_ranges"}
	for p, b := range bodies {
		for _, k := range arrays {
			if strings.Contains(b, `"`+k+`":null`) {
				t.Errorf("%s: %q is null: %s", p, k, b)
			}
		}
	}
}
