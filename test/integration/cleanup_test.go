//go:build integration

package integration

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"drool/internal/sshaccount"
)

// Dogfooding correction batch (ARCHITECTURE-ALPHA §40): DROOL cleans up
// after itself when a host is removed, and a Shepherd removes itself
// without SSH. Everything runs in unprivileged containers (sshd, no
// systemd: helpers start in their own session); every check of the host
// is made independently of DROOL, as root through docker exec.

// shepherdLeft lists what is left of a Shepherd on a container: its paths,
// its processes and its removal helper.
func shepherdLeft(t *testing.T, c *container) string {
	t.Helper()
	return strings.TrimSpace(c.exec(t, `for p in /usr/libexec/drool-agent /var/lib/drool-agent /var/log/drool-agent /etc/drool/agent.yaml /etc/drool/release-keys.d /etc/drool /run/drool-shepherd-removal.* /var/tmp/drool-shepherd-removal.*; do [ -e "$p" ] && echo "$p"; done
for d in /proc/[0-9]*; do e=$(readlink "$d/exe" 2>/dev/null); case "$e" in /usr/libexec/drool-agent/*) echo "process ${d#/proc/}";; esac; done; true`))
}

type uninstallRes struct {
	Method          string   `json:"method"`
	Outcome         string   `json:"outcome"`
	Removed         []string `json:"removed"`
	Remaining       []string `json:"remaining"`
	IdentityRevoked bool     `json:"identity_revoked"`
	Management      string   `json:"management"`
	Message         string   `json:"message"`
	Disconnected    bool     `json:"disconnected"`
	Verified        bool     `json:"verified"`
}

type uninstallPlanT struct {
	Method   string `json:"method"`
	Refusal  string `json:"refusal"`
	Shepherd struct {
		Available bool   `json:"available"`
		Code      string `json:"code"`
		Reason    string `json:"reason"`
	} `json:"shepherd"`
	SSH struct {
		Available bool   `json:"available"`
		Code      string `json:"code"`
	} `json:"ssh"`
}

// TestShepherdSelfUninstallWithoutSSH: a Shepherd on a host DROOL has no
// SSH path to (enrolled by hand) removes itself when asked: it accepts,
// its detached helper stops it and its supervisor and removes its files,
// it disconnects, DROOL revokes its identity, and the host stays in DROOL
// with no management path, said so. DROOL reports the removal as
// initiated, never verified; the container shows it's gone. Asking again:
// nothing left that DROOL knows of.
func TestShepherdSelfUninstallWithoutSSH(t *testing.T) {
	freshController(t)
	d := selected()[0]
	c := startHost(t, image(t, d), "selfrm-nossh", false, netOuter)
	var tok struct {
		Token string `json:"token"`
	}
	ctl.must(t, "POST", "/api/v1/agents/tokens", map[string]any{"preset": "monitor", "description": "self-removal"}, &tok)
	mustDocker(t, "cp", filepath.Join(agentDir, "drool-agent-linux-amd64"), c.Name+":/tmp/drool-agent")
	if out, err := dockerStdin(c.Name, tok.Token+"\n", "/tmp/drool-agent", "install", "--preset", "monitor", "--token-file", "-"); err != nil {
		t.Fatalf("install: %v\n%s", err, out)
	}
	c.exec(t, "rm -f /tmp/drool-agent")
	var hosts struct {
		Items []struct {
			ID, Name string
		} `json:"items"`
	}
	ctl.must(t, "GET", "/api/v1/hosts", nil, &hosts)
	var id string
	for _, h := range hosts.Items {
		if strings.HasPrefix(h.Name, "selfrm-nossh") {
			id = h.ID
		}
	}
	expect(t, id != "", "no agent-only host: %+v", hosts)
	waitAgent(t, id, time.Minute, "Shepherd connected", func(a hostAgent) bool { return a.Identity != nil && a.Identity.Connected })
	expect(t, strings.Contains(shepherdLeft(t, c), "/var/lib/drool-agent"), "no Shepherd on the host before: %s", shepherdLeft(t, c))

	var p uninstallPlanT
	ctl.must(t, "POST", "/api/v1/hosts/"+id+"/agent/uninstall/plan", nil, &p)
	expect(t, p.Method == "shepherd" && p.Shepherd.Available && p.SSH.Code == "no_ssh", "plan without SSH: %+v", p)

	var res uninstallRes
	ctl.must(t, "POST", "/api/v1/hosts/"+id+"/agent/uninstall", map[string]any{}, &res)
	expect(t, res.Method == "shepherd" && res.Outcome == "initiated" && res.Disconnected && !res.Verified && res.IdentityRevoked && res.Management == "none" &&
		strings.Contains(res.Message, "Self-removal initiated") && strings.Contains(res.Message, "can't check that every file is gone"), "self-removal: %+v", res)
	// On the host, looked at independently: the helper ran and went.
	waitFor(t, time.Minute, "the Shepherd gone from the host", func() bool { return shepherdLeft(t, c) == "" })
	// In DROOL: revoked, no management path, honestly.
	a := getHostAgent(t, id)
	expect(t, a.Management.Active == "none" && strings.Contains(a.Management.Reason, "no way to manage this host") && (a.Identity == nil || !a.Identity.Connected),
		"after: %+v", a)
	st, _ := ctl.do("POST", "/api/v1/hosts/"+id+"/agent/uninstall", map[string]any{}, nil)
	expect(t, st == 409, "asking again: %d", st)
	ctl.must(t, "POST", "/api/v1/hosts/"+id+"/agent/uninstall/plan", nil, &p)
	expect(t, strings.Contains(p.Refusal, "trusts no Shepherd identity"), "plan afterwards: %+v", p)
}

// TestShepherdSelfUninstallPartialAndForget: what the helper can't remove
// (a read-only mount on one of the Shepherd's paths) is found by DROOL's
// check over SSH afterwards and reported as partial, again on a retry; a
// Shepherd neither way reaches (offline, no SSH) can only be forgotten:
// DROOL revokes it and leaves the host exactly as it was.
func TestShepherdSelfUninstallPartialAndForget(t *testing.T) {
	freshController(t)
	d := selected()[0]
	c := startHostWith(t, image(t, d), "selfrm-partial", true, []string{"--tmpfs", "/etc/drool/release-keys.d:ro,mode=755"}, netOuter)
	h := addHost(t, "selfrm-partial", c, hostOpts{})
	installAgent(t, h.ID, "managed", "")
	waitAgent(t, h.ID, time.Minute, "Shepherd connected", func(a hostAgent) bool { return a.Identity != nil && a.Identity.Connected })
	var res uninstallRes
	ctl.must(t, "POST", "/api/v1/hosts/"+h.ID+"/agent/uninstall", map[string]any{}, &res)
	expect(t, res.Method == "shepherd" && res.Outcome == "partial" && res.Verified && res.IdentityRevoked &&
		strings.Join(res.Remaining, ",") == "/etc/drool/release-keys.d" && strings.Contains(res.Message, "still finds"), "partial: %+v", res)
	left := shepherdLeft(t, c)
	expect(t, strings.Contains(left, "/etc/drool/release-keys.d") && !strings.Contains(left, "/var/lib/drool-agent") && !strings.Contains(left, "process"),
		"on the host: %s", left)
	// Again: over SSH now (the identity is revoked), the same leftover, said so.
	ctl.must(t, "POST", "/api/v1/hosts/"+h.ID+"/agent/uninstall", map[string]any{}, &res)
	expect(t, res.Method == "ssh" && res.Outcome == "partial" && strings.Join(res.Remaining, ",") == "/etc/drool/release-keys.d", "retry: %+v", res)

	// Offline, no SSH: forgotten, the host untouched.
	c2 := startHost(t, image(t, d), "selfrm-forget", false, netOuter)
	var tok struct {
		Token string `json:"token"`
	}
	ctl.must(t, "POST", "/api/v1/agents/tokens", map[string]any{"preset": "monitor", "description": "forget"}, &tok)
	mustDocker(t, "cp", filepath.Join(agentDir, "drool-agent-linux-amd64"), c2.Name+":/tmp/drool-agent")
	if out, err := dockerStdin(c2.Name, tok.Token+"\n", "/tmp/drool-agent", "install", "--preset", "monitor", "--token-file", "-"); err != nil {
		t.Fatalf("install: %v\n%s", err, out)
	}
	var hosts struct {
		Items []struct{ ID, Name string } `json:"items"`
	}
	ctl.must(t, "GET", "/api/v1/hosts", nil, &hosts)
	var id string
	for _, x := range hosts.Items {
		if strings.HasPrefix(x.Name, "selfrm-forget") {
			id = x.ID
		}
	}
	expect(t, id != "", "no agent-only host")
	waitAgent(t, id, time.Minute, "Shepherd connected", func(a hostAgent) bool { return a.Identity != nil && a.Identity.Connected })
	stopAgent(t, c2)
	waitAgent(t, id, time.Minute, "Shepherd offline", func(a hostAgent) bool { return a.Identity != nil && !a.Identity.Connected })
	before := shepherdLeft(t, c2)
	var prob problem
	st, _ := ctl.do("POST", "/api/v1/hosts/"+id+"/agent/uninstall", map[string]any{}, &prob)
	expect(t, st == 409 && prob.Code == "no_path" && strings.Contains(prob.Detail, "by hand"), "neither way: %d %+v", st, prob)
	ctl.must(t, "POST", "/api/v1/hosts/"+id+"/agent/uninstall", map[string]any{"method": "forget"}, &res)
	expect(t, res.Outcome == "forgotten" && res.IdentityRevoked && res.Management == "none", "forget: %+v", res)
	expect(t, shepherdLeft(t, c2) == before && strings.Contains(before, "/var/lib/drool-agent"), "forgetting changed the host: %s / %s", before, shepherdLeft(t, c2))
}

// TestShepherdUninstallOlderShepherd: a Shepherd from before Shepherds
// could remove themselves (the previous build, without uninstall-1) is
// told apart: asking it is refused with "update it or use SSH", and DROOL
// takes SSH instead, which works.
func TestShepherdUninstallOlderShepherd(t *testing.T) {
	freshController(t)
	d := selected()[0]
	c := startHost(t, image(t, d), "selfrm-old", true, netOuter)
	h := addHost(t, "selfrm-old", c, hostOpts{})
	prev := historic{"the previous 1.0 dogfood build", "9655106"}
	bin := prev.build(t, "drool-agent", "amd64")
	var tok struct {
		Token string `json:"token"`
	}
	ctl.must(t, "POST", "/api/v1/agents/tokens", map[string]any{"preset": "full", "description": "older", "host_id": h.ID}, &tok)
	mustDocker(t, "cp", bin, c.Name+":/tmp/drool-agent")
	if out, err := dockerStdin(c.Name, tok.Token+"\n", "/tmp/drool-agent", "install", "--preset", "full", "--token-file", "-"); err != nil {
		t.Fatalf("install the older Shepherd: %v\n%s", err, out)
	}
	waitAgent(t, h.ID, time.Minute, "older Shepherd connected", func(a hostAgent) bool { return a.Identity != nil && a.Identity.Connected })
	var p uninstallPlanT
	ctl.must(t, "POST", "/api/v1/hosts/"+h.ID+"/agent/uninstall/plan", nil, &p)
	expect(t, p.Shepherd.Code == "too_old" && p.Method == "ssh" && p.SSH.Available && strings.Contains(p.Shepherd.Reason, "Update it"), "plan: %+v", p)
	var prob problem
	st, _ := ctl.do("POST", "/api/v1/hosts/"+h.ID+"/agent/uninstall", map[string]any{"method": "shepherd"}, &prob)
	expect(t, st == 409 && prob.Code == "too_old" && strings.Contains(prob.Detail, "over SSH instead"), "asking an older Shepherd: %d %+v", st, prob)
	var res uninstallRes
	ctl.must(t, "POST", "/api/v1/hosts/"+h.ID+"/agent/uninstall", map[string]any{}, &res)
	expect(t, res.Method == "ssh" && res.Outcome == "uninstalled" && res.IdentityRevoked && len(res.Remaining) == 0, "over SSH: %+v", res)
	expect(t, shepherdLeft(t, c) == "", "left on the host: %s", shepherdLeft(t, c))
}

// accountHostSetup adds a host signing in as pwuser (password only, sudo
// asks for it) and lets DROOL create its account there.
func accountHostSetup(t *testing.T, name string, c *container) string {
	t.Helper()
	c.exec(t, ": > /home/pwuser/.ssh/authorized_keys")
	var h struct{ ID string }
	ctl.must(t, "POST", "/api/v1/hosts", map[string]any{"name": name, "address": "127.0.0.1", "port": c.Port, "user": "pwuser",
		"setup_credentials": map[string]any{"ssh_password": sudoPassword, "sudo_password": sudoPassword}}, &h)
	ctl.must(t, "POST", "/api/v1/hosts/"+h.ID+"/hostkeys/scan", nil, nil)
	ctl.must(t, "POST", "/api/v1/hosts/"+h.ID+"/hostkeys/approve", map[string]string{"fingerprint": c.fingerprint(t)}, nil)
	var j struct{ ID string }
	ctl.must(t, "POST", "/api/v1/hosts/"+h.ID+"/ssh-account", map[string]string{}, &j)
	waitJob(t, j.ID, 5*time.Minute)
	jh := getJobHost(t, j.ID, h.ID)
	expect(t, jh.State == "succeeded", "account setup on %s: %+v\n%s", name, jh, output(t, j.ID, h.ID))
	return h.ID
}

// accountLeft lists what is left of DROOL's account drool on a container.
func accountLeft(t *testing.T, c *container) string {
	t.Helper()
	return strings.TrimSpace(c.exec(t, `getent passwd drool; getent group drool; for p in /etc/sudoers.d/drool-account-drool /var/lib/drool/ssh-accounts/drool /home/drool /run/drool-account-cleanup.* /var/tmp/drool-account-cleanup.*; do [ -e "$p" ] && echo "$p"; done; true`))
}

type removalPlanT struct {
	CanCleanUp bool `json:"can_clean_up"`
	Parts      []struct {
		What   string `json:"what"`
		Ready  bool   `json:"ready"`
		Method string `json:"method"`
		Reason string `json:"reason"`
	} `json:"parts"`
}

type removalResT struct {
	Outcome     string `json:"outcome"`
	HostDeleted bool   `json:"host_deleted"`
	Steps       []struct {
		What, Outcome, Message string
	} `json:"steps"`
	Message string `json:"message"`
}

// TestSSHAccountCleanupOnRemoval: DROOL's own account, created for real
// (useradd, visudo), is removed with the host: proven DROOL's by its
// marker, a helper started over the account's last session removes the
// sudo rule, the account with its home, its group (recorded in the
// marker), the marker and itself, after DROOL's session ended. DROOL says
// it can't verify the end; the container shows it. Unrelated users,
// groups, sudo rules and files survive. A forged marker (another key,
// another user ID, none) is refused before anything changes. The helper
// itself finishes what an interrupted run left, runs again harmlessly, and
// leaves an account it can't prove alone.
func TestSSHAccountCleanupOnRemoval(t *testing.T) {
	freshController(t)
	d := selected()[0]
	c := startHost(t, image(t, d), "acct-clean", true, netOuter)
	id := accountHostSetup(t, "acct-clean", c)
	marker := c.exec(t, "cat /var/lib/drool/ssh-accounts/drool")
	gid := strings.TrimSpace(c.exec(t, "id -g drool"))
	expect(t, strings.Contains(marker, "gid="+gid), "the marker records the account's group: %s", marker)
	// Things that aren't DROOL's.
	c.exec(t, `useradd -m alice && groupadd droolers && usermod -aG droolers alice &&
printf 'alice ALL=(ALL) NOPASSWD: /usr/bin/true\n' > /etc/sudoers.d/alice && chmod 440 /etc/sudoers.d/alice &&
echo keep > /var/lib/drool/keep-me && useradd -M drool-other`)
	unrelated := func() string {
		return c.exec(t, "getent passwd alice drool-other | cut -d: -f1; getent group droolers | cut -d: -f1; cat /etc/sudoers.d/alice /var/lib/drool/keep-me; ls -d /home/alice")
	}
	before := unrelated()

	var p removalPlanT
	ctl.must(t, "POST", "/api/v1/hosts/"+id+"/removal/plan", nil, &p)
	expect(t, p.CanCleanUp && len(p.Parts) == 1 && p.Parts[0].What == "account" && p.Parts[0].Ready, "plan: %+v", p)

	// Forged markers: refused, nothing changed, the host stays.
	orig := marker
	for name, forged := range map[string]string{
		"another key":     strings.Replace(orig, "key=SHA256:", "key=SHA256:forged", 1),
		"another user ID": strings.Replace(orig, "uid=", "uid=9", 1),
	} {
		c.exec(t, "cat > /var/lib/drool/ssh-accounts/drool <<'EOF'\n"+forged+"EOF")
		var prob problem
		st, _ := ctl.do("POST", "/api/v1/hosts/"+id+"/removal", nil, &prob)
		expect(t, st == 409 && prob.Code == "cleanup_unavailable", "%s: %d %+v", name, st, prob)
		expect(t, strings.Contains(accountLeft(t, c), "/etc/sudoers.d/drool-account-drool"), "%s: something was removed: %s", name, accountLeft(t, c))
		st, _ = ctl.do("GET", "/api/v1/hosts/"+id, nil, nil)
		expect(t, st == 200, "%s: the host was forgotten", name)
	}
	c.exec(t, "cat > /var/lib/drool/ssh-accounts/drool <<'EOF'\n"+orig+"EOF")
	c.exec(t, "mv /var/lib/drool/ssh-accounts/drool /root/marker")
	st, _ := ctl.do("POST", "/api/v1/hosts/"+id+"/removal", nil, nil)
	expect(t, st == 409, "no marker: %d", st)
	c.exec(t, "mv /root/marker /var/lib/drool/ssh-accounts/drool")

	// Cleaned up.
	var res removalResT
	ctl.must(t, "POST", "/api/v1/hosts/"+id+"/removal", nil, &res)
	expect(t, res.Outcome == "removed" && res.HostDeleted && len(res.Steps) == 1 && res.Steps[0].Outcome == "initiated" &&
		strings.Contains(res.Steps[0].Message, "can't verify the final state through that account"), "removal: %+v", res)
	waitFor(t, 90*time.Second, "DROOL's account gone from the host", func() bool { return accountLeft(t, c) == "" })
	expect(t, unrelated() == before, "something unrelated changed:\n%s\n---\n%s", before, unrelated())
	expect(t, strings.TrimSpace(c.exec(t, "ls -d /var/lib/drool/ssh-accounts 2>/dev/null; true")) == "", "the empty marker directory is left")
	st, _ = ctl.do("GET", "/api/v1/hosts/"+id, nil, nil)
	expect(t, st == 404, "the host is still in DROOL: %d", st)

	// The helper by hand: what an interrupted run leaves, finished.
	var ck struct {
		PublicKey string `json:"public_key"`
	}
	ctl.must(t, "GET", "/api/v1/secrets/controller-ssh-key", nil, &ck)
	pub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(ck.PublicKey))
	if err != nil {
		t.Fatal(err)
	}
	helper, err := sshaccount.CleanupHelper("drool", ssh.FingerprintSHA256(pub))
	if err != nil {
		t.Fatal(err)
	}
	c2 := startHost(t, image(t, d), "acct-clean2", true, netOuter)
	id2 := accountHostSetup(t, "acct-clean2", c2)
	ctl.must(t, "DELETE", "/api/v1/hosts/"+id2, nil, nil) // from DROOL only: the account stays
	expect(t, strings.Contains(accountLeft(t, c2), "drool:x:"), "removing from DROOL only touched the account: %s", accountLeft(t, c2))
	runHelper := func() string {
		t.Helper()
		c2.exec(t, "mkdir -p /run/drool-account-cleanup.it && cat > /run/drool-account-cleanup.it/cleanup.sh <<'DROOL_IT_EOF'\n"+helper+"DROOL_IT_EOF")
		return c2.exec(t, "sh /run/drool-account-cleanup.it/cleanup.sh; echo exit=$?")
	}
	// A marker another DROOL wrote: the helper leaves the account alone.
	orig2 := c2.exec(t, "cat /var/lib/drool/ssh-accounts/drool")
	c2.exec(t, "cat > /var/lib/drool/ssh-accounts/drool <<'EOF'\n"+strings.Replace(orig2, "key=SHA256:", "key=SHA256:other", 1)+"EOF")
	out := runHelper()
	expect(t, strings.Contains(out, "exit=1") && strings.Contains(accountLeft(t, c2), "drool:x:"), "a foreign marker: %s / %s", out, accountLeft(t, c2))
	c2.exec(t, "cat > /var/lib/drool/ssh-accounts/drool <<'EOF'\n"+orig2+"EOF")
	// Interrupted after the sudo rule went: it finishes.
	c2.exec(t, "rm /etc/sudoers.d/drool-account-drool")
	out = runHelper()
	expect(t, strings.Contains(out, "exit=0") && accountLeft(t, c2) == "", "after an interruption: %s / %s", out, accountLeft(t, c2))
	// Again, with nothing left: harmless.
	out = runHelper()
	expect(t, strings.Contains(out, "exit=0") && accountLeft(t, c2) == "", "again: %s / %s", out, accountLeft(t, c2))
}

// TestHostRemovalCleansUpBoth: a host with a Shepherd and DROOL's own
// account, removed with the cleanup: the Shepherd removes itself first
// (verified over the account's SSH), then the account goes; the host is
// forgotten; nothing of either is left on the container.
func TestHostRemovalCleansUpBoth(t *testing.T) {
	freshController(t)
	d := selected()[0]
	c := startHost(t, image(t, d), "both-clean", true, netOuter)
	id := accountHostSetup(t, "both-clean", c)
	installAgent(t, id, "managed", "")
	waitAgent(t, id, time.Minute, "Shepherd connected", func(a hostAgent) bool { return a.Identity != nil && a.Identity.Connected })
	var p removalPlanT
	ctl.must(t, "POST", "/api/v1/hosts/"+id+"/removal/plan", nil, &p)
	expect(t, p.CanCleanUp && len(p.Parts) == 2 && p.Parts[0].What == "shepherd" && p.Parts[0].Method == "shepherd" && p.Parts[1].What == "account", "plan: %+v", p)
	var res removalResT
	ctl.must(t, "POST", "/api/v1/hosts/"+id+"/removal", nil, &res)
	expect(t, res.Outcome == "removed" && res.HostDeleted && len(res.Steps) == 2 && res.Steps[0].What == "shepherd" &&
		(res.Steps[0].Outcome == "uninstalled" || res.Steps[0].Outcome == "initiated") && res.Steps[1].Outcome == "initiated", "removal: %+v", res)
	waitFor(t, 90*time.Second, "both gone from the host", func() bool { return shepherdLeft(t, c) == "" && accountLeft(t, c) == "" })
}
