//go:build integration

package integration

import (
	"strings"
	"testing"
	"time"
)

// TestSSHCertificates moves real OpenSSH hosts from DROOL's static key to
// short-lived certificates from its user CA (JIT SSH, M16): the trust step
// against real sshd (sshd -t, sshd -T, reload, refusal of a host that
// trusts another CA), the verified switch, work with the static key gone,
// principals, a jump host in certificate mode, and a full rotation. sshd
// runs in unprivileged containers; nothing here touches this machine's SSH.
func TestSSHCertificates(t *testing.T) {
	ds := selected()
	d := ds[0]
	other := ds[len(ds)-1] // a RHEL-family sshd when the whole matrix runs
	web := startHost(t, image(t, d), "cert-web", true, netOuter)
	rh := startHost(t, image(t, other), "cert-rh", true, netOuter)
	corp := startHost(t, image(t, d), "cert-corp", true, netOuter)
	jump := startHost(t, image(t, d), "cert-jump", true, netOuter, netInner)
	inner := startHost(t, image(t, d), "cert-inner", false, netInner)

	hw := addHost(t, "cert-web", web, hostOpts{})
	hr := addHost(t, "cert-rh", rh, hostOpts{})
	hc := addHost(t, "cert-corp", corp, hostOpts{})
	hj := addHost(t, "cert-jump", jump, hostOpts{JumpOnly: true})
	hi := addHost(t, "cert-inner", inner, hostOpts{Via: hj.ID})
	for _, h := range []host{hw, hr, hc, hj, hi} {
		// Containers have no systemd: "unsupported" is as good as ready here.
		expect(t, h.Status == "ready" || h.Status == "unsupported", "%s: %s %s", h.Name, h.Status, h.Detail)
	}

	var ca struct {
		Authorities []struct {
			ID          string `json:"id"`
			Name        string `json:"name"`
			State       string `json:"state"`
			PublicKey   string `json:"public_key"`
			Fingerprint string `json:"fingerprint"`
		} `json:"authorities"`
		Unverified []string `json:"unverified"`
	}
	ctl.must(t, "POST", "/api/v1/ssh-ca", map[string]string{"name": "it-one"}, &ca)
	expect(t, len(ca.Authorities) == 1 && ca.Authorities[0].State == "active", "CA: %+v", ca)
	first := ca.Authorities[0]

	// A host that already trusts its own CA file is left alone.
	corp.exec(t, `echo "TrustedUserCAKeys /etc/ssh/corp_ca.pub" > /etc/ssh/sshd_config.d/10-corp.conf && touch /etc/ssh/corp_ca.pub && kill -HUP 1`)

	trust := func(ids ...string) string {
		t.Helper()
		var j struct{ ID string }
		ctl.must(t, "POST", "/api/v1/jobs", map[string]any{"steps": []any{map[string]any{"type": "ssh_ca_trust"}},
			"targets": map[string]any{"hosts": ids}}, &j)
		waitJob(t, j.ID, 5*time.Minute)
		return j.ID
	}
	jid := trust(hw.ID, hr.ID, hc.ID, hj.ID, hi.ID)
	for _, h := range []host{hw, hr, hj, hi} {
		jh := getJobHost(t, jid, h.ID)
		expect(t, jh.State == "succeeded", "trust on %s: %v\n%s", h.Name, jh, output(t, jid, h.ID))
		out := output(t, jid, h.ID)
		expect(t, strings.Contains(out, "accepted on a fresh connection"), "%s output: %s", h.Name, out)
	}
	jh := getJobHost(t, jid, hc.ID)
	expect(t, jh.State == "failed" && strings.Contains(jh.ErrorMessage, "corp_ca.pub"), "foreign CA host: %v", jh)
	_, err := docker("exec", corp.Name, "test", "-e", "/etc/ssh/sshd_config.d/40-drool-user-ca.conf")
	expect(t, err != nil, "DROOL's file written on a host that trusts another CA")
	// What sshd now has, on a real host.
	keys := web.exec(t, "cat /etc/ssh/drool_user_ca.pub")
	expect(t, strings.TrimSpace(keys) == first.PublicKey, "keys file: %q", keys)
	expect(t, strings.Contains(web.exec(t, "/usr/sbin/sshd -T | grep -i ^trustedusercakeys"), "/etc/ssh/drool_user_ca.pub"), "sshd -T")
	// Running it again changes nothing (and doesn't reload sshd).
	jid = trust(hw.ID)
	expect(t, strings.Contains(output(t, jid, hw.ID), "Already installed"), "second run: %s", output(t, jid, hw.ID))

	// Switch, verified on a fresh connection; then take DROOL's static key
	// away from the hosts entirely.
	for _, h := range []host{hw, hr, hj, hi} {
		ctl.must(t, "PUT", "/api/v1/hosts/"+h.ID+"/ssh-auth", map[string]string{"mode": "certificate"}, nil)
	}
	for _, c := range []*container{web, rh, jump, inner} {
		c.exec(t, "for u in deploy pwuser; do : > /home/$u/.ssh/authorized_keys; done")
	}
	for _, h := range []host{hw, hr, hi} {
		jh := run(t, h.ID, cmd("id -un; sudo -n true && echo root-ok", false))
		expect(t, jh.State == "succeeded", "%s with certificates only: %v", h.Name, jh)
	}
	// The host's own log names DROOL's certificate (its key id).
	logOut, _ := execCommand("docker", "logs", web.Name).CombinedOutput() // sshd -e logs to stderr
	logs := string(logOut)
	expect(t, strings.Contains(logs, "ED25519-CERT") && strings.Contains(logs, "ID drool:"), "sshd log has no certificate login:\n%s", tail(logs, 20))
	// The connection test works too, and over the jump host in certificate mode.
	p := probe(t, hi.ID)
	st0, detail, _ := p.step("authentication")
	expect(t, st0 == "ok", "probe through a certificate jump host: %s %s %+v", st0, detail, p.Steps)

	// A principal mapping (AuthorizedPrincipalsFile): the user name alone
	// is refused; the configured principal works.
	web.exec(t, `mkdir -p /etc/ssh/principals && echo drool-ops > /etc/ssh/principals/deploy && `+
		`echo "AuthorizedPrincipalsFile /etc/ssh/principals/%u" > /etc/ssh/sshd_config.d/30-principals.conf && kill -HUP 1`)
	time.Sleep(time.Second)
	var prob problem
	st, _ := ctl.do("PUT", "/api/v1/hosts/"+hw.ID+"/ssh-auth", map[string]string{"mode": "certificate", "principal": ""}, &prob)
	expect(t, st == 409 && prob.Code == "certificate_refused", "principal mismatch accepted: %d %+v", st, prob)
	ctl.must(t, "PUT", "/api/v1/hosts/"+hw.ID+"/ssh-auth", map[string]string{"mode": "certificate", "principal": "drool-ops"}, nil)
	jh = run(t, hw.ID, cmd("true", false))
	expect(t, jh.State == "succeeded", "with the mapped principal: %v", jh)

	// Rotation: introduce, trust both, activate, retire the old one.
	ctl.must(t, "POST", "/api/v1/ssh-ca/rotate", map[string]string{"name": "it-two"}, &ca)
	var second string
	for _, a := range ca.Authorities {
		if a.State == "next" {
			second = a.ID
		}
	}
	expect(t, second != "" && len(ca.Unverified) == 4, "after rotate: %+v", ca)
	st, _ = ctl.do("POST", "/api/v1/ssh-ca/"+second+"/activate", nil, &prob)
	expect(t, st == 409, "activated before the hosts trust it: %d", st)
	trust(hw.ID, hr.ID, hj.ID, hi.ID)
	expect(t, len(strings.Fields(web.exec(t, "cat /etc/ssh/drool_user_ca.pub"))) >= 4, "both keys listed")
	ctl.must(t, "POST", "/api/v1/ssh-ca/"+second+"/activate", nil, &ca)
	ctl.must(t, "POST", "/api/v1/ssh-ca/"+first.ID+"/retire", nil, &ca)
	trust(hw.ID, hr.ID, hj.ID, hi.ID) // hosts drop the retired authority
	keys = web.exec(t, "cat /etc/ssh/drool_user_ca.pub")
	expect(t, !strings.Contains(keys, first.PublicKey[:60]), "retired key still trusted: %s", keys)
	for _, h := range []host{hw, hr, hi} {
		jh := run(t, h.ID, cmd("true", false))
		expect(t, jh.State == "succeeded", "%s after the rotation: %v", h.Name, jh)
	}

	// Removing trust from a host that depends on it is refused.
	jid = submit(t, []string{hr.ID}, map[string]any{"type": "ssh_ca_trust", "action": "remove"})
	waitJob(t, jid, time.Minute)
	expect(t, getJobHost(t, jid, hr.ID).State == "failed", "remove on a certificate host: %v", getJobHost(t, jid, hr.ID))
}

func tail(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
