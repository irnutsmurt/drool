//go:build integration

package integration

import (
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// sudoRsImage is the Ubuntu 26.04 test image with sudo-rs installed: what
// Ubuntu makes sudo by default since 25.10 (the test image itself only has
// the classic sudo package).
func sudoRsImage(t *testing.T) string {
	t.Helper()
	var u2604 distro
	for _, d := range allDistros {
		if d.Name == "ubuntu-26.04" {
			u2604 = d
		}
	}
	base := image(t, u2604)
	tag := base + "-sudo-rs"
	imagesMu.Lock()
	err, done := built[tag]
	imagesMu.Unlock()
	if !done {
		cmd := exec.Command("docker", "build", "-q", "-t", tag, "-")
		cmd.Stdin = strings.NewReader("FROM " + base + "\n" +
			"RUN apt-get update && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends sudo-rs && rm -rf /var/lib/apt/lists/*\n")
		if out, berr := cmd.CombinedOutput(); berr != nil {
			err = berr
			t.Logf("docker build: %s", out)
		}
		imagesMu.Lock()
		built[tag] = err
		imagesMu.Unlock()
	}
	if err != nil {
		t.Fatalf("build the sudo-rs image: %v", err)
	}
	return tag
}

// rejectingVisudo puts a visudo in front of the host's own (the setup
// script's PATH starts with /usr/local/sbin) that rejects every proposed
// file containing pattern, the way a host's visudo rejects syntax its sudo
// doesn't have, and leaves everything else to the real one.
func rejectingVisudo(t *testing.T, c *container, pattern string) {
	t.Helper()
	c.exec(t, `cat > /usr/local/sbin/visudo <<'EOF'
#!/bin/sh
real=$(PATH=/usr/sbin:/usr/bin:/sbin:/bin command -v visudo)
if [ "$1" = -cf ] && grep -q -- '`+pattern+`' "$2"; then
	echo "$2:4:16: syntax error: unknown setting: '`+pattern+`'"
	echo "visudo: invalid sudoers file"
	exit 1
fi
exec "$real" "$@"
EOF
chmod 755 /usr/local/sbin/visudo`)
}

// TestSSHAccountSudoPolicy: the sudo rule of DROOL's own account follows the
// host's actual sudo (dogfooding 2026-10-14: a host's visudo rejected
// "Defaults:drool !requiretty", the line DROOL used to add everywhere).
// On real sudo-rs, which has no requiretty, the rule is the minimal one; on
// a host whose sudo requires a terminal for the account, DROOL adds the
// exception only if visudo accepts it, and otherwise refuses without
// installing anything. Failures switch nothing, keep the credentials and the
// account, and running the setup again continues. sshd runs in unprivileged
// containers; nothing here touches this machine.
func TestSSHAccountSudoPolicy(t *testing.T) {
	freshController(t)
	var ck struct {
		CredentialID string `json:"credential_id"`
	}
	ctl.must(t, "GET", "/api/v1/secrets/controller-ssh-key", nil, &ck)
	type hostResp struct {
		ID                 string  `json:"id"`
		User               string  `json:"user"`
		CredentialID       string  `json:"credential_id"`
		BecomeCredentialID *string `json:"become_credential_id"`
	}
	getH := func(id string) hostResp {
		var h hostResp
		ctl.must(t, "GET", "/api/v1/hosts/"+id, nil, &h)
		return h
	}
	// A host signing in as pwuser with its password, whose sudo asks for it.
	add := func(name string, c *container) hostResp {
		t.Helper()
		c.exec(t, ": > /home/pwuser/.ssh/authorized_keys")
		var h hostResp
		ctl.must(t, "POST", "/api/v1/hosts", map[string]any{"name": name, "address": "127.0.0.1", "port": c.Port, "user": "pwuser",
			"setup_credentials": map[string]any{"ssh_password": sudoPassword, "sudo_password": sudoPassword}}, &h)
		ctl.must(t, "POST", "/api/v1/hosts/"+h.ID+"/hostkeys/scan", nil, nil)
		ctl.must(t, "POST", "/api/v1/hosts/"+h.ID+"/hostkeys/approve", map[string]string{"fingerprint": c.fingerprint(t)}, nil)
		st, detail, _ := probe(t, h.ID).step("privilege escalation")
		expect(t, st == "ok", "%s: privilege escalation as pwuser: %s %s", name, st, detail)
		return h
	}
	setUp := func(id string) (jobHost, string) {
		t.Helper()
		var j struct{ ID string }
		ctl.must(t, "POST", "/api/v1/hosts/"+id+"/ssh-account", map[string]string{}, &j)
		waitJob(t, j.ID, 5*time.Minute)
		return getJobHost(t, j.ID, id), output(t, j.ID, id)
	}
	temporary := func(name string) bool {
		var creds struct {
			Items []struct {
				Name      string `json:"name"`
				Temporary bool   `json:"temporary"`
			} `json:"items"`
		}
		ctl.must(t, "GET", "/api/v1/secrets", nil, &creds)
		for _, c := range creds.Items {
			if c.Name == name && c.Temporary {
				return true
			}
		}
		return false
	}
	// failedSafely: nothing switched, the setup credentials kept, the account
	// left to continue with, and no DROOL sudo rule or proposal on the host.
	failedSafely := func(c *container, h hostResp, name string, jh jobHost, out string) {
		t.Helper()
		expect(t, jh.State == "failed" && strings.Contains(jh.ErrorMessage, "Nothing was switched") &&
			strings.Contains(jh.ErrorMessage, "running the setup again continues with it"), "%s: %v\n%s", name, jh, out)
		now := getH(h.ID)
		expect(t, now.User == "pwuser" && now.CredentialID == h.CredentialID && temporary("setup-"+name+"-login") && temporary("setup-"+name+"-sudo"),
			"%s after the failure: %+v", name, now)
		expect(t, strings.Contains(c.exec(t, "getent passwd drool"), "/home/drool"), "%s: the account isn't there to continue with", name)
		expect(t, strings.TrimSpace(c.exec(t, "ls -A /etc/sudoers.d | grep -c -e '^drool-account' -e '^[.]drool-' || true")) == "0", "%s: a DROOL sudo rule or proposal is on the host: %s", name, c.exec(t, "ls -la /etc/sudoers.d"))
	}
	noSecrets := func(texts ...string) {
		t.Helper()
		var audit json.RawMessage
		ctl.must(t, "GET", "/api/v1/audit?limit=500", nil, &audit)
		expect(t, !strings.Contains(strings.Join(texts, "\n")+string(audit), sudoPassword), "a setup password leaked into output, errors or the audit log")
	}

	// --- sudo-rs, the reported host ---
	rs := startHost(t, sudoRsImage(t), "sudo-rs", true, netOuter)
	expect(t, strings.Contains(rs.exec(t, "sudo --version"), "sudo-rs"), "the host's sudo isn't sudo-rs: %s", rs.exec(t, "sudo --version"))
	// Its visudo rejects DROOL's old line, as on the reported host.
	reject, _ := docker("exec", rs.Name, "sh", "-c", `printf '%s\n' 'Defaults:drool !requiretty' 'drool ALL=(ALL:ALL) NOPASSWD: ALL' > /tmp/old && chmod 440 /tmp/old && visudo -cf /tmp/old 2>&1`)
	expect(t, strings.Contains(reject, "unknown setting: 'requiretty'"), "sudo-rs accepted the old line: %s", reject)
	hrs := add("sudo-rs", rs)
	// The first attempt fails where the reported one did, after the account
	// was created: visudo rejects the rule.
	rejectingVisudo(t, rs, "NOPASSWD")
	jh, out := setUp(hrs.ID)
	failedSafely(rs, hrs, "sudo-rs", jh, out)
	expect(t, strings.Contains(jh.ErrorMessage, "visudo rejected DROOL's sudo rule, so it wasn't installed"), "the first attempt: %s", jh.ErrorMessage)
	// Running it again continues with that account.
	rs.exec(t, "rm /usr/local/sbin/visudo")
	jh, out = setUp(hrs.ID)
	expect(t, jh.State == "succeeded", "sudo-rs setup again: %v\n%s", jh, out)
	expect(t, strings.Contains(out, "exists from an earlier setup by DROOL") && strings.Contains(out, "Sudo rule for drool: drool ALL=(ALL:ALL) NOPASSWD: ALL") &&
		!strings.Contains(out, "requiretty"), "sudo-rs output: %s", out)
	sudoers := rs.exec(t, "cat /etc/sudoers.d/drool-account-drool")
	expect(t, !strings.Contains(sudoers, "requiretty") && strings.Contains(sudoers, "drool ALL=(ALL:ALL) NOPASSWD: ALL"), "sudo-rs rule: %s", sudoers)
	expect(t, strings.Contains(rs.exec(t, "visudo -c 2>&1; sudo -n -l -U drool"), "NOPASSWD: ALL"), "sudo-rs doesn't see the rule")
	now := getH(hrs.ID)
	expect(t, now.User == "drool" && now.CredentialID == ck.CredentialID && now.BecomeCredentialID == nil && !temporary("setup-sudo-rs-login"), "after the switch: %+v", now)
	r := run(t, hrs.ID, cmd("id -u && test -w /etc", true))
	expect(t, r.State == "succeeded", "root through sudo-rs as drool: %v", r)
	noSecrets(out, jh.ErrorMessage)

	// --- A host whose sudo requires a terminal for the account ---
	rhel := allDistros[0]
	for _, d := range selected() {
		if d.Family == "dnf" {
			rhel = d
		}
	}
	tty := startHost(t, image(t, rhel), "acct-tty", true, netOuter)
	tty.exec(t, `echo 'Defaults:drool requiretty' > /etc/sudoers.d/00-drool-tty && chmod 440 /etc/sudoers.d/00-drool-tty`)
	htty := add("acct-tty", tty)
	// Its visudo doesn't accept the exception: refused, nothing installed.
	rejectingVisudo(t, tty, "requiretty")
	jh, out = setUp(htty.ID)
	failedSafely(tty, htty, "acct-tty", jh, out)
	expect(t, strings.Contains(jh.ErrorMessage, "requires a terminal for drool (requiretty), and its visudo doesn't accept an exception"), "refusal: %s", jh.ErrorMessage)
	// Its visudo accepts it: the exception is added, and the account works
	// without a terminal, on the fresh connection too.
	tty.exec(t, "rm /usr/local/sbin/visudo")
	jh, out = setUp(htty.ID)
	expect(t, jh.State == "succeeded" && strings.Contains(out, "requires a terminal (requiretty): the rule includes an exception for drool"), "tty setup: %v\n%s", jh, out)
	sudoers = tty.exec(t, "cat /etc/sudoers.d/drool-account-drool")
	expect(t, strings.Contains(sudoers, "Defaults:drool !requiretty\ndrool ALL=(ALL:ALL) NOPASSWD: ALL"), "tty rule: %s", sudoers)
	expect(t, strings.TrimSpace(tty.exec(t, "setsid -w runuser -u drool -- sudo -n -- id -u </dev/null")) == "0", "drool's sudo without a terminal")
	expect(t, getH(htty.ID).User == "drool", "acct-tty didn't switch")
	r = run(t, htty.ID, cmd("id -u", true))
	expect(t, r.State == "succeeded", "root as drool on the requiretty host: %v", r)
	// The host's own files, and problems it already had, stay as they were.
	expect(t, strings.TrimSpace(tty.exec(t, "stat -c %a /etc/sudoers.d/deploy /etc/sudoers.d/pwuser /etc/sudoers.d/00-drool-tty")) == "644\n644\n440", "DROOL changed other sudoers files")
	noSecrets(out, jh.ErrorMessage)
}
