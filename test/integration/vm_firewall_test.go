//go:build integration

package integration

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// Real nftables, systemd timers, reboots and the boot loader (DESIGN §17)
// in disposable cloud VMs under qemu TCG in unprivileged containers. A
// mistake here can only break a throwaway VM.

var (
	testKeyOnce   sync.Once
	testSigner    ssh.Signer
	fwControlFile string
	fwControlMu   sync.Mutex
)

// testAuthorizedKey is the test's own key for the deploy user: a side
// channel to inspect and disturb a VM independently of DROOL.
func testAuthorizedKey() string {
	testKeyOnce.Do(func() {
		_, priv, _ := ed25519.GenerateKey(rand.Reader)
		testSigner, _ = ssh.NewSignerFromKey(priv)
	})
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(testSigner.PublicKey())))
}

// vmSSH runs a command as root over a fresh test connection.
func vmSSH(v *vm, cmd string) (string, error) {
	testAuthorizedKey()
	cfg := &ssh.ClientConfig{User: "deploy", Auth: []ssh.AuthMethod{ssh.PublicKeys(testSigner)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), Timeout: 15 * time.Second}
	// qemu accepts TCP on the forwarded port while the guest is down: bound
	// the whole exchange, not just the TCP connect.
	nc, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", v.c.Port), 15*time.Second)
	if err != nil {
		return "", err
	}
	nc.SetDeadline(time.Now().Add(2 * time.Minute))
	cc, chans, reqs, err := ssh.NewClientConn(nc, nc.RemoteAddr().String(), cfg)
	if err != nil {
		nc.Close()
		return "", err
	}
	c := ssh.NewClient(cc, chans, reqs)
	defer c.Close()
	s, err := c.NewSession()
	if err != nil {
		return "", err
	}
	defer s.Close()
	out, err := s.CombinedOutput("sudo sh -c " + shq(cmd))
	return strings.TrimSpace(string(out)), err
}

func mustVMSSH(t *testing.T, v *vm, cmd string) string {
	t.Helper()
	var out string
	var err error
	for i := 0; i < 3; i++ {
		if out, err = vmSSH(v, cmd); err == nil {
			return out
		}
		time.Sleep(3 * time.Second)
	}
	t.Fatalf("side channel %q: %v\n%s", cmd, err, out)
	return ""
}

func shq(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// fwControl sets a host's test control line ("delay=N", "skip", "" clears).
func fwControl(host, val string) {
	fwControlMu.Lock()
	defer fwControlMu.Unlock()
	b, _ := os.ReadFile(fwControlFile)
	var keep []string
	for _, l := range strings.Split(string(b), "\n") {
		if l != "" && !strings.HasPrefix(l, host+" ") {
			keep = append(keep, l)
		}
	}
	if val != "" {
		keep = append(keep, host+" "+val)
	}
	os.WriteFile(fwControlFile, []byte(strings.Join(keep, "\n")+"\n"), 0o644)
}

type fwTxT struct {
	ID        string `json:"id"`
	State     string `json:"state"`
	Transport string `json:"transport"`
	Revision  int64  `json:"revision"`
	Detail    string `json:"detail"`
	Steps     []struct {
		Text string `json:"text"`
	} `json:"steps"`
}

func (tx fwTxT) timeline() string {
	var b strings.Builder
	for _, s := range tx.Steps {
		b.WriteString("  " + s.Text + "\n")
	}
	return b.String()
}

func lastFwTx(t *testing.T, hostID string) fwTxT {
	var list struct {
		Items []fwTxT `json:"items"`
	}
	ctl.must(t, "GET", "/api/v1/firewall/transactions?host_id="+hostID, nil, &list)
	if len(list.Items) == 0 {
		return fwTxT{}
	}
	var tx fwTxT
	ctl.must(t, "GET", "/api/v1/firewall/transactions/"+list.Items[0].ID, nil, &tx)
	return tx
}

func startFwApply(t *testing.T, hostID string, extra map[string]any) string {
	t.Helper()
	body := map[string]any{"host_ids": []string{hostID}}
	for k, v := range extra {
		body[k] = v
	}
	var j struct {
		ID string `json:"id"`
	}
	ctl.must(t, "POST", "/api/v1/firewall/apply", body, &j)
	return j.ID
}

func fwApply(t *testing.T, hostID string, extra map[string]any) (jobHost, fwTxT) {
	t.Helper()
	id := startFwApply(t, hostID, extra)
	waitJob(t, id, 20*time.Minute)
	return getJobHost(t, id, hostID), lastFwTx(t, hostID)
}

// waitCandidate waits until the host runs the candidate containing marker.
func waitCandidate(t *testing.T, v *vm, hostID, jobID, marker string) {
	t.Helper()
	var out string
	var err error
	waitApplying(t, hostID, jobID, fmt.Sprintf("%q to go live", marker), func() bool {
		out, err = vmSSH(v, "nft list table inet drool")
		return strings.Contains(out, marker)
	}, func() string { return fmt.Sprintf("side channel: %v %.300s", err, out) })
}

// waitApplying waits for ok while the apply job runs, failing early (with
// the job's outcome) if the job ends first, and with the desired ruleset and
// the transaction's timeline on a timeout.
func waitApplying(t *testing.T, hostID, jobID, what string, ok func() bool, diag func() string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Minute)
	for !ok() {
		var j struct {
			Status string `json:"status"`
		}
		ctl.must(t, "GET", "/api/v1/jobs/"+jobID, nil, &j)
		if j.Status == "completed" || j.Status == "cancelled" || j.Status == "interrupted" {
			jh := getJobHost(t, jobID, hostID)
			tx := lastFwTx(t, hostID)
			t.Fatalf("the apply job ended before %s: %+v\nlast tx %s %s: %s\n%s", what, jh, tx.ID, tx.State, tx.Detail, tx.timeline())
		}
		if time.Now().After(deadline) {
			var plan struct {
				Rendered struct {
					Full string `json:"full"`
				} `json:"rendered"`
			}
			ctl.must(t, "GET", "/api/v1/firewall/hosts/"+hostID, nil, &plan)
			t.Logf("desired ruleset:\n%s", plan.Rendered.Full)
			tx := lastFwTx(t, hostID)
			extra := ""
			if diag != nil {
				extra = diag()
			}
			t.Fatalf("timed out waiting for %s (job %s; %s); last tx %s %s: %s\n%s", what, j.Status, extra, tx.ID, tx.State, tx.Detail, tx.timeline())
		}
		time.Sleep(2 * time.Second)
	}
}

func fwPolicy(t *testing.T, p map[string]any) string {
	t.Helper()
	var out struct {
		ID string `json:"id"`
	}
	ctl.must(t, "POST", "/api/v1/firewall/policies", p, &out)
	return out.ID
}

type fwPlanT struct {
	Summary struct {
		Status    string `json:"status"`
		Supported *bool  `json:"supported"`
		Reason    string `json:"reason"`
	} `json:"summary"`
	Eligibility *struct {
		Supported bool     `json:"supported"`
		Reasons   []string `json:"reasons"`
		Detected  []string `json:"detected"`
	} `json:"eligibility"`
	Applied struct {
		Enforced       bool  `json:"enforced"`
		Revision       int64 `json:"revision"`
		SetsGenApplied int64 `json:"sets_generation_applied"`
	} `json:"applied"`
}

func fwPlan(t *testing.T, hostID string) fwPlanT {
	var p fwPlanT
	ctl.must(t, "GET", "/api/v1/firewall/hosts/"+hostID, nil, &p)
	return p
}

func fwDetect(t *testing.T, hostID string) (bool, string) {
	t.Helper()
	var r struct {
		Eligibility struct {
			Supported bool     `json:"supported"`
			Reasons   []string `json:"reasons"`
			Detected  []string `json:"detected"`
		} `json:"eligibility"`
	}
	// DROOL gives a detection a minute; a cold, emulated host with one vCPU
	// (Python's ufw, a dozen systemctl calls) can need longer the first
	// time. DROOL calls that timeout retryable, and an operator checks again.
	for attempt := 1; ; attempt++ {
		var raw json.RawMessage
		st, err := ctl.do("POST", "/api/v1/firewall/hosts/"+hostID+"/detect", nil, &raw)
		if err == nil && st == 200 {
			if err := json.Unmarshal(raw, &r); err != nil {
				t.Fatalf("detect: %v", err)
			}
			break
		}
		if attempt < 3 && st == 502 && strings.Contains(string(raw), `"error_class":"TimeoutError"`) && strings.Contains(string(raw), `"retryable":true`) {
			t.Logf("detect timed out on a slow host (attempt %d); checking again", attempt)
			continue
		}
		t.Fatalf("POST /api/v1/firewall/hosts/%s/detect: HTTP %d %v %s", hostID, st, err, raw)
	}
	return r.Eligibility.Supported, strings.Join(append(r.Eligibility.Reasons, r.Eligibility.Detected...), "; ")
}

func TestVMFirewall(t *testing.T) {
	if *flagVMs == "" {
		t.Skip("set -vm to run the VM tests")
	}
	freshController(t) // a controller of its own: nothing from other tests (M17)
	want := map[string]bool{}
	for _, n := range strings.Split(*flagVMs, ",") {
		want[strings.TrimSpace(n)] = true
	}
	for _, def := range vmDefs {
		if !want[def.Name] {
			continue
		}
		t.Run(def.Name, func(t *testing.T) {
			t.Parallel()
			v := startVM(t, def)
			h := addVMHost(t, "fw-"+def.Name, v, hostOpts{})
			testVMFirewallSSH(t, def, v, h)
			testVMFirewallAgent(t, def, v, h)
		})
	}
}

func waitSSHBack(t *testing.T, v *vm) {
	t.Helper()
	waitFor(t, 15*time.Minute, "VM back after reboot", func() bool {
		_, err := vmSSH(v, "true")
		return err == nil
	})
}

// ensureNft checks the host and installs nftables the way an operator
// would when a minimal cloud image lacks it.
func ensureNft(t *testing.T, name string, h host) {
	t.Helper()
	sup, why := fwDetect(t, h.ID)
	t.Logf("%s: %s", name, why)
	if !sup && strings.Contains(why, "nft) is not installed") {
		// A freshly booted cloud image may still be running dnf-makecache or
		// unattended-upgrades: DROOL reports the lock; try again, as an
		// operator would.
		for i := 0; ; i++ {
			jh := run(t, h.ID, map[string]any{"type": "package", "action": "install", "names": []string{"nftables"}})
			if jh.State == "succeeded" {
				break
			}
			if i == 4 || jh.ErrorClass != "PackageManagerError" || !strings.Contains(jh.ErrorMessage, "locked") {
				t.Fatalf("install nftables: %v", jh)
			}
			t.Logf("%s: %s; retrying", name, jh.ErrorMessage)
			time.Sleep(30 * time.Second)
		}
		sup, why = fwDetect(t, h.ID)
		t.Logf("%s after installing nftables: %s", name, why)
	}
	if !sup {
		t.Fatalf("refused: %s", why)
	}
}

func testVMFirewallSSH(t *testing.T, def vmDef, v *vm, h host) {
	ok := t.Run("detect: plain nftables is supported", func(t *testing.T) { ensureNft(t, def.Name, h) })
	if !ok {
		t.FailNow()
	}
	ctl.must(t, "PUT", "/api/v1/firewall/hosts/"+h.ID, map[string]any{"mode": "enforce", "rollback_timeout_s": 90}, nil)
	fwPolicy(t, map[string]any{"name": "web-" + def.Name, "from": []any{map[string]any{"kind": "internal"}},
		"to": []any{map[string]any{"kind": "host", "id": h.ID}}, "services": []string{"https"}})

	t.Run("SSH transaction commits over a fresh connection", func(t *testing.T) {
		jh, tx := fwApply(t, h.ID, nil)
		if jh.State != "succeeded" || tx.State != "committed" || tx.Transport != "ssh" {
			t.Fatalf("%v %+v\n%s", jh, tx, tx.timeline())
		}
		t.Logf("timeline:\n%s", tx.timeline())
		live := mustVMSSH(t, v, "nft list table inet drool")
		for _, s := range []string{"policy drop", "management: SSH management from the controller", "policy web-" + def.Name} {
			if !strings.Contains(live, s) {
				t.Errorf("live table lacks %q:\n%s", s, live)
			}
		}
		if out := mustVMSSH(t, v, "systemctl is-enabled drool-firewall.service; grep -c '\"state\": \"committed\"' /var/lib/drool/firewall/transaction.json"); out != "enabled\n1" {
			t.Errorf("boot unit / journal: %q", out)
		}
		if !probe(t, h.ID).OK {
			t.Error("DROOL can't reach the host after commit")
		}
	})

	t.Run("outbound restriction is enforced; the management path survives", func(t *testing.T) {
		ctl.must(t, "PUT", "/api/v1/firewall/hosts/"+h.ID, map[string]any{"mode": "enforce", "outbound": "restrict", "rollback_timeout_s": 90}, nil)
		_, tx := fwApply(t, h.ID, nil)
		if tx.State != "committed" {
			t.Fatalf("%+v\n%s", tx, tx.timeline())
		}
		probeOut := fmt.Sprintf("timeout 5 bash -c 'echo > /dev/tcp/%s/%d' && echo open || echo blocked", agentGateway, ctl.agentListenPort())
		if out := mustVMSSH(t, v, probeOut); out != "blocked" {
			t.Errorf("outbound to the controller's agent port: %s (expected blocked)", out)
		}
		var svc struct{}
		ctl.must(t, "POST", "/api/v1/firewall/services", map[string]any{"name": "agentport-" + strings.ReplaceAll(def.Name, ".", ""),
			"entries": []any{map[string]any{"protocol": "tcp", "ports": fmt.Sprint(ctl.agentListenPort())}}}, &svc)
		pid := fwPolicy(t, map[string]any{"name": "to-ctl-" + def.Name, "from": []any{map[string]any{"kind": "host", "id": h.ID}},
			"to": []any{map[string]any{"kind": "cidr", "cidr": agentGateway + "/32"}}, "services": []string{"agentport-" + strings.ReplaceAll(def.Name, ".", "")}})
		if _, tx = fwApply(t, h.ID, nil); tx.State != "committed" {
			t.Fatalf("%+v", tx)
		}
		if out := mustVMSSH(t, v, probeOut); out != "open" {
			t.Errorf("outbound allowed by policy: %s", out)
		}
		ctl.must(t, "DELETE", "/api/v1/firewall/policies/"+pid, nil, nil)
		ctl.must(t, "PUT", "/api/v1/firewall/hosts/"+h.ID, map[string]any{"mode": "enforce", "outbound": "allow", "rollback_timeout_s": 90}, nil)
		if _, tx = fwApply(t, h.ID, nil); tx.State != "committed" {
			t.Fatalf("%+v", tx)
		}
	})

	t.Run("reboot: the boot loader restores the committed policy", func(t *testing.T) {
		jh := run(t, h.ID, map[string]any{"type": "reboot"})
		if jh.State != "succeeded" {
			t.Fatalf("reboot: %v", jh)
		}
		live := mustVMSSH(t, v, "nft list table inet drool")
		if !strings.Contains(live, "policy web-"+def.Name) {
			t.Fatalf("not restored at boot:\n%s", live)
		}
		if out := mustVMSSH(t, v, "systemctl show -p ActiveState --value drool-firewall.service"); out != "active" {
			t.Errorf("boot unit state: %s", out)
		}
	})

	t.Run("management path deliberately blocked: rollback by the host's timer", func(t *testing.T) {
		pid := fwPolicy(t, map[string]any{"name": "lockout-" + def.Name, "action": "deny", "from": []any{map[string]any{"kind": "any"}},
			"to": []any{map[string]any{"kind": "host", "id": h.ID}}, "services": []string{"ssh"}})
		defer ctl.must(t, "DELETE", "/api/v1/firewall/policies/"+pid, nil, nil)
		jh, _ := fwApply(t, h.ID, nil)
		if jh.State != "failed" || !strings.Contains(jh.ErrorMessage, "would remove the management path") {
			t.Fatalf("not refused: %v", jh)
		}
		t0 := time.Now()
		jh, tx := fwApply(t, h.ID, map[string]any{"override_mgmt": true, "reason": "VM test: block the management path"})
		if jh.State != "failed" || tx.State != "rolled_back" {
			t.Fatalf("expected a rollback: %v %+v\n%s", jh, tx, tx.timeline())
		}
		t.Logf("rolled back after %s:\n%s", time.Since(t0).Round(time.Second), tx.timeline())
		if !strings.Contains(tx.Detail, "rollback timer fired") {
			t.Errorf("rollback reason: %s", tx.Detail)
		}
		live := mustVMSSH(t, v, "nft list table inet drool")
		// Other hosts' lockout denies legitimately appear in this host's output chain.
		if strings.Contains(live, "lockout-"+def.Name) || !strings.Contains(live, "management: SSH management") {
			t.Fatalf("previous policy not restored:\n%s", live)
		}
	})

	t.Run("pending transaction blocks reboots", func(t *testing.T) {
		fwPolicy(t, map[string]any{"name": "ping-" + def.Name, "from": []any{map[string]any{"kind": "internal"}},
			"to": []any{map[string]any{"kind": "host", "id": h.ID}}, "services": []string{"ping"}})
		fwControl(h.Name, "delay=40")
		defer fwControl(h.Name, "")
		job := startFwApply(t, h.ID, nil)
		waitApplying(t, h.ID, job, "a pending transaction", func() bool { return lastFwTx(t, h.ID).State == "pending" }, nil)
		st, err := ctl.do("POST", "/api/v1/jobs", map[string]any{"steps": []any{map[string]any{"type": "reboot"}},
			"targets": map[string]any{"hosts": []string{h.ID}}}, nil)
		if err != nil || st != 409 {
			t.Errorf("reboot during a transaction: %d %v", st, err)
		}
		waitJob(t, job, 10*time.Minute)
		if tx := lastFwTx(t, h.ID); tx.State != "committed" {
			t.Fatalf("%+v\n%s", tx, tx.timeline())
		}
	})

	t.Run("reboot while pending: the boot loader rolls back", func(t *testing.T) {
		before := mustVMSSH(t, v, "nft list table inet drool")
		fwPolicy(t, map[string]any{"name": "smtp-" + def.Name, "from": []any{map[string]any{"kind": "internal"}},
			"to": []any{map[string]any{"kind": "host", "id": h.ID}}, "services": []string{"smtp"}})
		fwControl(h.Name, "skip")
		defer fwControl(h.Name, "")
		job := startFwApply(t, h.ID, nil)
		waitCandidate(t, v, h.ID, job, "policy smtp-"+def.Name)
		// A hard reset, without syncing: the journal must already be durable.
		vmSSH(v, "echo 1 > /proc/sys/kernel/sysrq; echo b > /proc/sysrq-trigger")
		time.Sleep(10 * time.Second)
		waitSSHBack(t, v)
		j := mustVMSSH(t, v, "cat /var/lib/drool/firewall/transaction.json")
		if !strings.Contains(j, `"state": "rolled_back"`) || !strings.Contains(j, "rebooted during the transaction") {
			t.Fatalf("journal after reboot:\n%s", j)
		}
		after := mustVMSSH(t, v, "nft list table inet drool")
		if strings.Contains(after, "smtp-"+def.Name) || !strings.Contains(after, "policy ping-"+def.Name) {
			t.Fatalf("not the committed policy after reboot:\n%s\n(before:\n%s)", after, before)
		}
		waitJob(t, job, 15*time.Minute)
		// The controller confirms it, or resolves it once the host is back.
		waitFor(t, 5*time.Minute, "rollback recorded", func() bool { return lastFwTx(t, h.ID).State == "rolled_back" })
		if tx := lastFwTx(t, h.ID); !strings.Contains(tx.Detail, "rebooted during the transaction") {
			t.Errorf("reason: %s", tx.Detail)
		}
	})

	t.Run("external drift is detected and replaced only on request", func(t *testing.T) {
		if _, tx := fwApply(t, h.ID, nil); tx.State != "committed" { // the smtp policy is still wanted
			t.Fatalf("%+v", tx)
		}
		mustVMSSH(t, v, "nft add rule inet drool input tcp dport 9999 accept")
		fwDetect(t, h.ID)
		if p := fwPlan(t, h.ID); p.Summary.Status != "drift" {
			t.Fatalf("drift not shown: %+v", p.Summary)
		}
		if jh, _ := fwApply(t, h.ID, nil); jh.State != "failed" || !strings.Contains(jh.ErrorMessage, "drift") {
			t.Fatalf("applied over drift: %v", jh)
		}
		if _, tx := fwApply(t, h.ID, map[string]any{"allow_drift": true, "reason": "VM test: replace drift"}); tx.State != "committed" {
			t.Fatalf("%+v", tx)
		}
		if live := mustVMSSH(t, v, "nft list table inet drool"); strings.Contains(live, "9999") {
			t.Fatal("drift survived")
		}
	})

	t.Run("other firewall rules: refused, nothing touched", func(t *testing.T) {
		mustVMSSH(t, v, "nft add table ip custom; nft add chain ip custom input '{ type filter hook input priority 0; policy accept; }'; nft add rule ip custom input tcp dport 1234 accept")
		sup, why := fwDetect(t, h.ID)
		if sup || !strings.Contains(why, "other nftables rules are present in table ip custom") {
			t.Fatalf("custom rules: %v %s", sup, why)
		}
		jh, _ := fwApply(t, h.ID, nil)
		if jh.State != "failed" || jh.ErrorClass != "ConfigurationError" {
			t.Fatalf("applied next to other rules: %v", jh)
		}
		if out := mustVMSSH(t, v, "nft list table ip custom"); !strings.Contains(out, "1234") {
			t.Fatal("DROOL touched another table")
		}
		mustVMSSH(t, v, "nft delete table ip custom")
		if def.Name == "ubuntu-24.04" {
			mustVMSSH(t, v, "ufw allow 22/tcp >/dev/null; ufw --force enable >/dev/null")
			sup, why = fwDetect(t, h.ID)
			mustVMSSH(t, v, "ufw --force disable >/dev/null")
			if sup || !strings.Contains(why, "UFW is active") {
				t.Errorf("UFW: %v %s", sup, why)
			}
			// Disabled, its chains stay loaded until a reboot: still refused, with a hint.
			if sup, why = fwDetect(t, h.ID); sup || !strings.Contains(why, "UFW is disabled but its rules are still loaded") {
				t.Errorf("disabled UFW with its rules loaded: %v %s", sup, why)
			}
			mustVMSSH(t, v, "nft delete table ip filter; nft delete table ip6 filter")
		}
		if sup, why := fwDetect(t, h.ID); !sup {
			t.Fatalf("not supported again: %s", why)
		}
	})

	t.Run("address change: set update without a transaction", func(t *testing.T) {
		var peer struct {
			ID string `json:"id"`
		}
		ctl.must(t, "POST", "/api/v1/hosts", map[string]any{"name": "fw-peer-" + def.Name, "address": "192.0.2.50", "port": 22, "user": "deploy"}, &peer)
		var g struct {
			ID string `json:"id"`
		}
		ctl.must(t, "POST", "/api/v1/groups", map[string]any{"name": "fwpeers-" + def.Name, "host_ids": []string{peer.ID}}, &g)
		fwPolicy(t, map[string]any{"name": "peers-" + def.Name, "from": []any{map[string]any{"kind": "group", "id": g.ID}},
			"to": []any{map[string]any{"kind": "host", "id": h.ID}}, "services": []string{"redis"}})
		_, tx := fwApply(t, h.ID, nil)
		if tx.State != "committed" || !strings.Contains(mustVMSSH(t, v, "nft list table inet drool"), "192.0.2.50") {
			t.Fatalf("group policy: %+v", tx)
		}
		ctl.must(t, "PATCH", "/api/v1/hosts/"+peer.ID, map[string]any{"address": "192.0.2.51"}, nil)
		waitFor(t, 3*time.Minute, "set update on the host", func() bool {
			out, _ := vmSSH(v, "nft list table inet drool")
			return strings.Contains(out, "192.0.2.51") && !strings.Contains(out, "192.0.2.50")
		})
		if lastFwTx(t, h.ID).ID != tx.ID {
			t.Error("the address change ran a transaction")
		}
		ctl.must(t, "DELETE", "/api/v1/hosts/"+peer.ID, nil, nil)
	})
}

func testVMFirewallAgent(t *testing.T, def vmDef, v *vm, h host) {
	ok := t.Run("install the agent", func(t *testing.T) {
		installAgent(t, h.ID, "full", "")
		waitAgent(t, h.ID, 5*time.Minute, "agent connected", func(a hostAgent) bool { return a.Management.Active == "agent" })
	})
	if !ok {
		return
	}
	t.Run("agent transaction commits over a new mTLS connection", func(t *testing.T) {
		fwPolicy(t, map[string]any{"name": "imap-" + def.Name, "from": []any{map[string]any{"kind": "internal"}},
			"to": []any{map[string]any{"kind": "host", "id": h.ID}}, "services": []string{"imap"}})
		jh, tx := fwApply(t, h.ID, nil)
		if jh.State != "succeeded" || tx.State != "committed" || tx.Transport != "agent" || !strings.Contains(tx.timeline(), "new connection at") {
			t.Fatalf("%v %+v\n%s", jh, tx, tx.timeline())
		}
		t.Logf("timeline:\n%s", tx.timeline())
		if !strings.Contains(mustVMSSH(t, v, "nft list table inet drool"), "policy imap-"+def.Name) {
			t.Fatal("not live")
		}
	})

	// M10: with DROOL's firewall enforced, connection tracking is active on
	// the host: the agent switches from socket sampling to conntrack and
	// counts connections too short-lived for any sample.
	t.Run("network monitoring: conntrack sees short-lived connections", func(t *testing.T) {
		type hostNet struct {
			Collection struct{ Mode, Source, Label, Detail string } `json:"collection"`
		}
		var hn hostNet
		deadline := time.Now().Add(5 * time.Minute)
		for {
			ctl.must(t, "GET", "/api/v1/hosts/"+h.ID+"/network", nil, &hn)
			if hn.Collection.Source == "conntrack" {
				break
			}
			if time.Now().After(deadline) {
				diag, _ := vmSSH(v, "sysctl net.netfilter.nf_conntrack_events net.netfilter.nf_conntrack_count 2>&1; lsmod | grep -i conntrack; journalctl -u drool-agent --no-pager | grep -i 'collection' | tail -5")
				t.Fatalf("no conntrack collection: %+v\n%s", hn.Collection, diag)
			}
			time.Sleep(5 * time.Second)
		}
		if hn.Collection.Label != "Continuous: conntrack" {
			t.Fatalf("collection: %+v", hn.Collection)
		}
		type page struct {
			Items []struct {
				Direction string `json:"direction"`
				Port      int    `json:"port"`
				Process   string `json:"process"`
				Count     int64  `json:"count"`
				Source    string `json:"source"`
			} `json:"items"`
		}
		count := func() int64 {
			var p page
			ctl.must(t, "GET", fmt.Sprintf("/api/v1/network/flows?host=%s&direction=out&peer=%s&port=%d", h.ID, agentGateway, ctl.agentListenPort()), nil, &p)
			var n int64
			for _, f := range p.Items {
				n += f.Count
			}
			return n
		}
		before := count()
		// Six TCP connections that open and close within a second.
		mustVMSSH(t, v, fmt.Sprintf("for i in 1 2 3 4 5 6; do timeout 3 bash -c 'exec 3<>/dev/tcp/%s/%d' 2>/dev/null; sleep 0.3; done; true", agentGateway, ctl.agentListenPort()))
		waitFor(t, 3*time.Minute, "short-lived connections counted", func() bool { return count() >= before+6 })
		// Inbound SSH is attributed to the listening sshd.
		var p page
		ctl.must(t, "GET", "/api/v1/network/flows?host="+h.ID+"&direction=in&port=22", nil, &p)
		found := false
		for _, f := range p.Items {
			if f.Process == "sshd" {
				found = true
			}
		}
		if !found {
			t.Fatalf("inbound SSH without its process: %+v", p.Items)
		}
		t.Logf("conntrack: %d → %d connections to the agent endpoint", before, count())
	})

	t.Run("agent path blocked: rollback, then reconnect", func(t *testing.T) {
		svc := "agentport-" + strings.ReplaceAll(def.Name, ".", "")
		pid := fwPolicy(t, map[string]any{"name": "cut-agent-" + def.Name, "action": "deny", "from": []any{map[string]any{"kind": "host", "id": h.ID}},
			"to": []any{map[string]any{"kind": "cidr", "cidr": agentGateway + "/32"}}, "services": []string{svc}})
		deleted := false
		defer func() {
			if !deleted {
				ctl.must(t, "DELETE", "/api/v1/firewall/policies/"+pid, nil, nil)
			}
		}()
		jh, tx := fwApply(t, h.ID, map[string]any{"override_mgmt": true, "reason": "VM test: block the agent path"})
		if jh.State != "failed" || tx.State != "rolled_back" {
			host, _ := vmSSH(v, fmt.Sprintf("nft list table inet drool; ss -tnp | grep -F ':%d'; ip route get %s; cat /var/lib/drool/firewall/transaction.json", ctl.agentListenPort(), agentGateway))
			t.Fatalf("%v %+v\n%s\n--- the host ---\n%s", jh, tx, tx.timeline(), host)
		}
		ctl.must(t, "DELETE", "/api/v1/firewall/policies/"+pid, nil, nil)
		deleted = true
		waitAgent(t, h.ID, 5*time.Minute, "agent back", func(a hostAgent) bool { return a.Management.Active == "agent" })
	})

	t.Run("agent process death: the systemd timer rolls back", func(t *testing.T) {
		fwPolicy(t, map[string]any{"name": "mysql-" + def.Name, "from": []any{map[string]any{"kind": "internal"}},
			"to": []any{map[string]any{"kind": "host", "id": h.ID}}, "services": []string{"mysql"}})
		fwControl(h.Name, "delay=30")
		defer fwControl(h.Name, "")
		job := startFwApply(t, h.ID, nil)
		waitCandidate(t, v, h.ID, job, "policy mysql-"+def.Name)
		mustVMSSH(t, v, "systemctl kill -s KILL drool-agent; systemctl stop drool-agent")
		waitFor(t, 4*time.Minute, "timer rollback", func() bool {
			j, _ := vmSSH(v, "cat /var/lib/drool/firewall/transaction.json")
			return strings.Contains(j, `"state": "rolled_back"`)
		})
		if live := mustVMSSH(t, v, "nft list table inet drool"); strings.Contains(live, "mysql-"+def.Name) {
			t.Fatal("candidate survived the agent's death")
		}
		mustVMSSH(t, v, "systemctl start drool-agent")
		waitJob(t, job, 10*time.Minute)
		waitFor(t, 5*time.Minute, "rollback reported", func() bool { return lastFwTx(t, h.ID).State == "rolled_back" })
	})

	t.Run("remove DROOL's policy", func(t *testing.T) {
		_, tx := fwApply(t, h.ID, map[string]any{"remove": true})
		if tx.State != "committed" {
			t.Fatalf("%+v\n%s", tx, tx.timeline())
		}
		if out, _ := vmSSH(v, "nft list table inet drool >/dev/null 2>&1 && echo present || echo gone"); out != "gone" {
			t.Fatalf("table after removal: %s", out)
		}
	})
}

// TestVMFirewallControllerCrash: the controller dies between apply and
// commit; the host, reached through a jump host, restores its previous
// policy on its own, and the restarted controller learns the outcome.
func TestVMFirewallControllerCrash(t *testing.T) {
	if *flagVMs == "" || !strings.Contains(*flagVMs, "debian-12") {
		t.Skip("set -vm debian-12 to run")
	}
	freshController(t) // a controller of its own: nothing from other tests (M17)
	jump := startHost(t, image(t, allDistros[0]), "fwcrash-jump", true, netOuter)
	jh := addHost(t, "fwcrash-jump", jump, hostOpts{JumpOnly: true})
	var def vmDef
	for _, d := range vmDefs {
		if d.Name == "debian-12" {
			def = d
		}
	}
	v := startVM(t, def)
	h := addVMHost(t, "fwcrash-vm", v, hostOpts{Via: jh.ID})
	ensureNft(t, def.Name, h)
	mustVMSSH(t, v, "true") // the test's side channel works before any policy
	ctl.must(t, "PUT", "/api/v1/firewall/hosts/"+h.ID, map[string]any{"mode": "enforce", "rollback_timeout_s": 60}, nil)
	// The VM sees real source addresses: the jump host is the management
	// path, while the test's side channel arrives from the Docker gateway and
	// needs its own allowance.
	fwPolicy(t, map[string]any{"name": "crash-sidechannel", "from": []any{map[string]any{"kind": "internal"}},
		"to": []any{map[string]any{"kind": "host", "id": h.ID}}, "services": []string{"ssh"}})
	fwPolicy(t, map[string]any{"name": "crash-base", "from": []any{map[string]any{"kind": "internal"}},
		"to": []any{map[string]any{"kind": "host", "id": h.ID}}, "services": []string{"http"}})
	_, tx := fwApply(t, h.ID, nil)
	if tx.State != "committed" || !strings.Contains(tx.timeline(), "brand-new SSH connection") {
		t.Fatalf("through the jump host: %+v\n%s", tx, tx.timeline())
	}
	t.Logf("through the jump host:\n%s", tx.timeline())
	if _, err := vmSSH(v, "true"); err != nil {
		var plan struct {
			Rendered struct {
				Full string `json:"full"`
			} `json:"rendered"`
		}
		ctl.must(t, "GET", "/api/v1/firewall/hosts/"+h.ID, nil, &plan)
		t.Fatalf("side channel after the first commit: %v\n%s", err, plan.Rendered.Full)
	}

	fwPolicy(t, map[string]any{"name": "crash-new", "from": []any{map[string]any{"kind": "internal"}},
		"to": []any{map[string]any{"kind": "host", "id": h.ID}}, "services": []string{"postgres"}})
	fwControl(h.Name, "delay=300")
	job := startFwApply(t, h.ID, nil)
	waitCandidate(t, v, h.ID, job, "policy crash-new")
	ctl.crash()
	fwControl(h.Name, "")
	waitFor(t, 4*time.Minute, "host rolled back without the controller", func() bool {
		j, _ := vmSSH(v, "cat /var/lib/drool/firewall/transaction.json")
		return strings.Contains(j, `"state": "rolled_back"`)
	})
	if live := mustVMSSH(t, v, "nft list table inet drool"); strings.Contains(live, "crash-new") || !strings.Contains(live, "crash-base") {
		t.Fatalf("after the controller vanished:\n%s", live)
	}
	if err := ctl.start(); err != nil {
		t.Fatal(err)
	}
	if err := ctl.login(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 5*time.Minute, "restarted controller resolves the transaction", func() bool {
		tx := lastFwTx(t, h.ID)
		return tx.State == "rolled_back" || tx.State == "failed"
	})
	tx = lastFwTx(t, h.ID)
	t.Logf("resolved: %s (%s)", tx.State, tx.Detail)
	if tx.State != "rolled_back" {
		t.Errorf("resolved as %s", tx.State)
	}
}
