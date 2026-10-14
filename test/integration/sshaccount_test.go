//go:build integration

package integration

import (
	"strings"
	"testing"
	"time"
)

// TestDedicatedSSHAccount sets up DROOL's own SSH account on real OpenSSH
// hosts (After M17): over an existing account that signs in with a password
// and whose sudo asks for one, DROOL creates its account with real useradd
// and visudo, proves it on a fresh connection and switches; the setup
// credentials are forgotten and the setup account is left exactly as it
// was. Also: an existing account named drool is refused and left alone,
// a refusing sudo and a refused fresh sign-in change nothing that worked,
// running the setup again continues, and a host behind a jump host. sshd
// runs in unprivileged containers; nothing here touches this machine.
func TestDedicatedSSHAccount(t *testing.T) {
	freshController(t)
	ds := selected()
	d, other := ds[0], ds[len(ds)-1] // a RHEL-family host when the matrix runs
	web := startHost(t, image(t, d), "acct-web", true, netOuter)
	rh := startHost(t, image(t, other), "acct-rh", true, netOuter)
	taken := startHost(t, image(t, d), "acct-taken", true, netOuter)
	jump := startHost(t, image(t, d), "acct-jump", true, netOuter, netInner)
	inner := startHost(t, image(t, d), "acct-inner", false, netInner)
	// The setup account signs in with its password only.
	for _, c := range []*container{web, rh, taken, inner} {
		c.exec(t, ": > /home/pwuser/.ssh/authorized_keys")
	}
	pwuserState := func(c *container) string {
		return c.exec(t, "getent passwd pwuser; getent shadow pwuser; cat /etc/sudoers.d/pwuser; ls -la /home/pwuser/.ssh; cat /home/pwuser/.ssh/authorized_keys; id pwuser")
	}

	type planResp struct {
		User        string   `json:"user"`
		Blockers    []string `json:"blockers"`
		Changes     []string `json:"changes"`
		Credentials []struct {
			Name, Outcome string
		} `json:"credentials"`
		Check *struct {
			OK      bool   `json:"ok"`
			Account string `json:"account"`
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"check"`
	}
	type hostResp struct {
		ID                 string  `json:"id"`
		Name               string  `json:"name"`
		User               string  `json:"user"`
		CredentialID       string  `json:"credential_id"`
		BecomeCredentialID *string `json:"become_credential_id"`
		Status             string  `json:"status"`
	}
	var ck struct {
		CredentialID string `json:"credential_id"`
		PublicKey    string `json:"public_key"`
	}
	ctl.must(t, "GET", "/api/v1/secrets/controller-ssh-key", nil, &ck)
	keyBody := strings.Join(strings.Fields(ck.PublicKey)[:2], " ")

	// add creates a host signing in as pwuser with what the wizard sends.
	add := func(name string, c *container, body map[string]any) hostResp {
		t.Helper()
		addr, port := "127.0.0.1", c.Port
		if body["via_host_id"] != nil {
			addr, port = c.Name, 22
		}
		body["name"], body["address"], body["port"], body["user"] = name, addr, port, "pwuser"
		var h hostResp
		ctl.must(t, "POST", "/api/v1/hosts", body, &h)
		ctl.must(t, "POST", "/api/v1/hosts/"+h.ID+"/hostkeys/scan", nil, nil)
		ctl.must(t, "POST", "/api/v1/hosts/"+h.ID+"/hostkeys/approve", map[string]string{"fingerprint": c.fingerprint(t)}, nil)
		p := probe(t, h.ID)
		st, detail, _ := p.step("privilege escalation")
		expect(t, st == "ok", "%s: privilege escalation as pwuser: %s %s", name, st, detail)
		return h
	}
	plan := func(id, user string) planResp {
		t.Helper()
		var p planResp
		ctl.must(t, "POST", "/api/v1/hosts/"+id+"/ssh-account/plan", map[string]string{"user": user}, &p)
		return p
	}
	setUp := func(id, user string) (string, jobHost) {
		t.Helper()
		var j struct{ ID string }
		ctl.must(t, "POST", "/api/v1/hosts/"+id+"/ssh-account", map[string]string{"user": user}, &j)
		waitJob(t, j.ID, 5*time.Minute)
		return j.ID, getJobHost(t, j.ID, id)
	}
	getH := func(id string) hostResp {
		var h hostResp
		ctl.must(t, "GET", "/api/v1/hosts/"+id, nil, &h)
		return h
	}
	temporary := func() map[string]bool {
		var creds struct {
			Items []struct {
				Name      string `json:"name"`
				Temporary bool   `json:"temporary"`
			} `json:"items"`
		}
		ctl.must(t, "GET", "/api/v1/secrets", nil, &creds)
		out := map[string]bool{}
		for _, c := range creds.Items {
			if c.Temporary {
				out[c.Name] = true
			}
		}
		return out
	}
	setup := map[string]any{"ssh_password": sudoPassword, "sudo_password": sudoPassword}

	// --- Debian: a refused fresh sign-in first, then the setup again ---
	before := pwuserState(web)
	hw := add("acct-web", web, map[string]any{"setup_credentials": setup})
	p := plan(hw.ID, "")
	expect(t, p.User == "drool" && len(p.Blockers) == 0 && p.Check != nil && p.Check.OK && p.Check.Account == "new" && len(p.Changes) == 5, "plan: %+v", p)
	// sshd lets only the existing users in: the account can't sign in, so
	// nothing switches.
	web.exec(t, `echo "AllowUsers pwuser deploy" > /etc/ssh/sshd_config.d/10-only.conf && kill -HUP 1`)
	jid, jh := setUp(hw.ID, "")
	expect(t, jh.State == "failed" && strings.Contains(jh.ErrorMessage, "Nothing was switched") && strings.Contains(jh.ErrorMessage, "authentication"), "refused sign-in: %v\n%s", jh, output(t, jid, hw.ID))
	h := getH(hw.ID)
	expect(t, h.User == "pwuser" && temporary()["setup-acct-web-login"] && temporary()["setup-acct-web-sudo"], "after a failed setup: %+v %v", h, temporary())
	expect(t, strings.Contains(web.exec(t, "getent passwd drool"), "/home/drool"), "the account was not created")
	web.exec(t, `rm /etc/ssh/sshd_config.d/10-only.conf && kill -HUP 1`)
	time.Sleep(time.Second) // sshd re-reads its configuration
	jid, jh = setUp(hw.ID, "")
	out := output(t, jid, hw.ID)
	expect(t, jh.State == "succeeded", "second setup: %v\n%s", jh, out)
	expect(t, strings.Contains(out, "exists from an earlier setup by DROOL") && strings.Contains(out, "Switched: acct-web now signs in as drool"), "output: %s", out)
	h = getH(hw.ID)
	expect(t, h.User == "drool" && h.CredentialID == ck.CredentialID && h.BecomeCredentialID == nil, "host after the switch: %+v", h)
	expect(t, !temporary()["setup-acct-web-login"] && !temporary()["setup-acct-web-sudo"], "setup credentials kept: %v", temporary())
	// What is on the host now.
	expect(t, strings.HasSuffix(strings.TrimSpace(web.exec(t, "getent passwd drool")), ":/home/drool:/bin/bash"), "passwd: %s", web.exec(t, "getent passwd drool"))
	expect(t, strings.HasPrefix(web.exec(t, "getent shadow drool | cut -d: -f2"), "*"), "password field: %s", web.exec(t, "getent shadow drool"))
	expect(t, strings.TrimSpace(web.exec(t, "stat -c '%U %a' /home/drool/.ssh /home/drool/.ssh/authorized_keys")) == "drool 700\ndrool 600", "modes: %s", web.exec(t, "stat -c '%U %a' /home/drool/.ssh /home/drool/.ssh/authorized_keys"))
	expect(t, strings.Count(web.exec(t, "cat /home/drool/.ssh/authorized_keys"), keyBody) == 1, "authorized_keys: %s", web.exec(t, "cat /home/drool/.ssh/authorized_keys"))
	sudoers := web.exec(t, "cat /etc/sudoers.d/drool-account-drool")
	// The minimal rule: this host's sudo needs no terminal exception.
	expect(t, strings.Contains(sudoers, "drool ALL=(ALL:ALL) NOPASSWD: ALL") && strings.Contains(sudoers, "root-equivalent") && !strings.Contains(sudoers, "requiretty"), "sudoers: %s", sudoers)
	expect(t, strings.Contains(out, "Sudo rule for drool: drool ALL=(ALL:ALL) NOPASSWD: ALL") && !strings.Contains(out, "requiretty"), "the reported rule: %s", out)
	expect(t, strings.TrimSpace(web.exec(t, "stat -c '%U %a' /etc/sudoers.d/drool-account-drool /var/lib/drool/ssh-accounts /var/lib/drool/ssh-accounts/drool")) == "root 440\nroot 700\nroot 600", "file modes")
	marker := web.exec(t, "cat /var/lib/drool/ssh-accounts/drool")
	expect(t, strings.Contains(marker, "uid="+strings.TrimSpace(web.exec(t, "id -u drool"))) && strings.Contains(marker, "key=SHA256:"), "marker: %s", marker)
	// The images' own sudoers files are mode 0644, which visudo -c reports:
	// a problem DROOL didn't make, reported as a warning and left alone.
	expect(t, strings.Contains(out, "warning: visudo -c reports problems that are already on this host"), "no warning about the host's sudoers files: %s", out)
	expect(t, strings.Contains(web.exec(t, "visudo -c 2>&1 | grep drool-account-drool || true"), "drool-account-drool: parsed OK"), "visudo on DROOL's file")
	expect(t, strings.Contains(web.exec(t, "sudo -n -l -U drool"), "NOPASSWD: ALL"), "sudo -l -U drool")
	expect(t, strings.TrimSpace(web.exec(t, "stat -c %a /etc/sudoers.d/deploy /etc/sudoers.d/pwuser")) == "644\n644", "DROOL changed other sudoers files")
	expect(t, pwuserState(web) == before, "the setup account changed:\n%s\n---\n%s", before, pwuserState(web))
	// DROOL works as the account: commands, and root through its sudo.
	r := run(t, hw.ID, cmd("id -un", false))
	expect(t, r.State == "succeeded", "command as drool: %v", r)
	r = run(t, hw.ID, cmd("id -u && test -w /etc", true))
	expect(t, r.State == "succeeded", "root as drool: %v", r)
	// Nothing more to do: it already signs in as drool.
	st, err := ctl.do("POST", "/api/v1/hosts/"+hw.ID+"/ssh-account", map[string]string{}, nil)
	expect(t, err == nil && st == 409, "setup again: %d %v", st, err)

	// --- RHEL family: a stored login credential is kept ---
	before = pwuserState(rh)
	login := createSecret(t, "rh-login", "password", sudoPassword)
	hr := add("acct-rh", rh, map[string]any{"credential_id": login, "setup_credentials": map[string]any{"sudo_password": sudoPassword}})
	p = plan(hr.ID, "drool")
	expect(t, p.Check != nil && p.Check.OK && len(p.Credentials) == 2, "rh plan: %+v", p)
	jid, jh = setUp(hr.ID, "drool")
	out = output(t, jid, hr.ID)
	expect(t, jh.State == "succeeded" && strings.Contains(out, "Created the account drool"), "rh setup: %v\n%s", jh, out)
	expect(t, strings.Contains(out, "Kept the stored credential rh-login") && strings.Contains(out, "Forgot the setup credential setup-acct-rh-sudo"), "rh output: %s", out)
	var cred struct {
		ID string `json:"id"`
	}
	ctl.must(t, "GET", "/api/v1/secrets/"+login, nil, &cred)
	rhRule := rh.exec(t, "cat /etc/sudoers.d/drool-account-drool")
	expect(t, rh.exec(t, "getent shadow drool | cut -d: -f2") != "" && strings.Contains(rhRule, "NOPASSWD: ALL") && !strings.Contains(rhRule, "requiretty"), "rh account: %s", rhRule)
	expect(t, pwuserState(rh) == before, "the setup account changed on %s", other.Name)
	r = run(t, hr.ID, cmd("id -u", true))
	expect(t, r.State == "succeeded", "root as drool on %s: %v", other.Name, r)

	// --- An account named drool that DROOL didn't create ---
	taken.exec(t, "useradd -m -s /bin/bash drool && echo 'drool:somebody-elses' | chpasswd && install -d -m 700 -o drool -g drool /home/drool/.ssh && echo 'ssh-ed25519 AAAAothers someone' > /home/drool/.ssh/authorized_keys")
	theirs := taken.exec(t, "getent passwd drool; getent shadow drool; cat /home/drool/.ssh/authorized_keys; ls /etc/sudoers.d")
	ht := add("acct-taken", taken, map[string]any{"setup_credentials": setup})
	p = plan(ht.ID, "drool")
	expect(t, p.Check != nil && !p.Check.OK && p.Check.Code == "exists" && len(p.Blockers) == 1, "plan for a taken name: %+v", p)
	jid, jh = setUp(ht.ID, "drool")
	expect(t, jh.State == "failed" && strings.Contains(jh.ErrorMessage, "DROOL never takes over an existing account"), "taken name: %v\n%s", jh, output(t, jid, ht.ID))
	expect(t, taken.exec(t, "getent passwd drool; getent shadow drool; cat /home/drool/.ssh/authorized_keys; ls /etc/sudoers.d") == theirs, "the existing drool account was changed")
	_, err = docker("exec", taken.Name, "test", "-e", "/var/lib/drool/ssh-accounts/drool")
	expect(t, err != nil, "a marker was written for someone else's account")
	// sudo refuses the setup account: nothing is created.
	wrong := createSecret(t, "wrong-sudo", "sudo_password", "not-the-password")
	var cur struct {
		BecomeCredentialID string `json:"become_credential_id"`
	}
	ctl.must(t, "GET", "/api/v1/hosts/"+ht.ID, nil, &cur)
	ctl.must(t, "PATCH", "/api/v1/hosts/"+ht.ID, map[string]any{"become_credential_id": wrong}, nil)
	jid, jh = setUp(ht.ID, "drool-mgmt")
	expect(t, jh.State == "failed" && strings.Contains(jh.ErrorMessage, "Nothing was switched"), "wrong sudo password: %v\n%s", jh, output(t, jid, ht.ID))
	_, err = docker("exec", taken.Name, "getent", "passwd", "drool-mgmt")
	expect(t, err != nil, "an account was created although sudo refused")
	ctl.must(t, "PATCH", "/api/v1/hosts/"+ht.ID, map[string]any{"become_credential_id": cur.BecomeCredentialID}, nil)
	jid, jh = setUp(ht.ID, "drool-mgmt")
	expect(t, jh.State == "succeeded", "another name: %v\n%s", jh, output(t, jid, ht.ID))
	expect(t, getH(ht.ID).User == "drool-mgmt", "host user after another name")

	// --- Behind a jump host ---
	hj := addHost(t, "acct-jump", jump, hostOpts{JumpOnly: true})
	hi := add("acct-inner", inner, map[string]any{"via_host_id": hj.ID, "setup_credentials": setup})
	jid, jh = setUp(hi.ID, "")
	out = output(t, jid, hi.ID)
	expect(t, jh.State == "succeeded" && strings.Contains(out, "on a brand-new connection through acct-jump"), "behind the jump host: %v\n%s", jh, out)
	jumpNow := getH(hj.ID)
	expect(t, jumpNow.User == "deploy" && jumpNow.CredentialID == ck.CredentialID, "the jump host's own sign-in changed: %+v", jumpNow)
	r = run(t, hi.ID, cmd("id -u", true))
	expect(t, r.State == "succeeded", "root as drool behind the jump host: %v", r)
	// A jump host only isn't given an account.
	st, err = ctl.do("POST", "/api/v1/hosts/"+hj.ID+"/ssh-account", map[string]string{}, nil)
	expect(t, err == nil && st == 409, "jump-only host: %d %v", st, err)
}
