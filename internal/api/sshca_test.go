package api

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"drool/internal/auth"
	"drool/internal/sshx/sshtest"
	"drool/internal/store/db"
)

// certHost is an in-process SSH server that behaves like sshd for DROOL's
// certificate trust: running the trust script makes it trust the CA keys
// the script lists (the real script runs against real OpenSSH in the
// container and VM tests; never on this machine).
type certHost struct {
	srv     *sshtest.Server
	key     ssh.Signer
	mu      sync.Mutex
	scripts int
	refuse  bool // the trust script fails like sshd -t would
}

func newCertHost(t *testing.T) *certHost {
	ch := &certHost{key: sshtest.NewEd25519Signer(t)}
	ch.srv = sshtest.Start(t, ch.key)
	ch.srv.SetExec(func(cmd string, stdin []byte) (string, string, int) {
		if strings.Contains(string(stdin), "TrustedUserCAKeys") {
			ch.mu.Lock()
			ch.scripts++
			refuse := ch.refuse
			ch.mu.Unlock()
			if refuse {
				return "@@ERR invalid sshd rejected the configuration (the previous one was put back): bad\n", "", 1
			}
			for _, line := range strings.Split(string(stdin), "\n") {
				if strings.HasPrefix(line, "ssh-ed25519 ") {
					pub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(line))
					if err == nil {
						ch.srv.TrustUserCA(pub)
					}
				}
			}
			return "@@changed 1\n", "", 0
		}
		if cmd == "id -un" {
			return "deploy\n", "", 0
		}
		return linuxExec(cmd, stdin)
	})
	return ch
}

func (ch *certHost) add(t *testing.T, c *client, name string) string {
	t.Helper()
	var ck controllerKeyResponse
	c.do("GET", "/api/v1/secrets/controller-ssh-key", nil).json(t, &ck)
	authorizeCred(t, ch.srv, ck.PublicKey)
	addr, port := ch.srv.Host()
	var host hostJSON
	c.do("POST", "/api/v1/hosts", map[string]any{"name": name, "address": addr, "port": port, "user": "deploy"}).json(t, &host)
	c.do("POST", "/api/v1/hosts/"+host.ID+"/hostkeys/scan", nil)
	if r := c.do("POST", "/api/v1/hosts/"+host.ID+"/hostkeys/approve", map[string]string{"fingerprint": ssh.FingerprintSHA256(ch.key.PublicKey())}); r.status != 200 {
		t.Fatalf("approve: %s", r.body)
	}
	return host.ID
}

func trustJob(t *testing.T, c *client, hostIDs ...string) jobJSON {
	t.Helper()
	r := c.do("POST", "/api/v1/jobs", map[string]any{"steps": []map[string]any{{"type": "ssh_ca_trust"}},
		"targets": map[string]any{"hosts": hostIDs}})
	if r.status != 201 {
		t.Fatalf("trust job: %d %s", r.status, r.body)
	}
	var j jobJSON
	r.json(t, &j)
	return waitJob(t, c, j.ID)
}

func TestSSHCertificateMigration(t *testing.T) {
	h := newHarness(t)
	h.mkUser("admin", auth.RoleAdmin)
	h.mkUser("op", auth.RoleOperator)
	c := h.login("admin")
	web := newCertHost(t)
	id := web.add(t, c, "web")

	// No authority yet: a host can't switch, and a trust step says why.
	if r := c.do("PUT", "/api/v1/hosts/"+id+"/ssh-auth", map[string]string{"mode": "certificate"}); r.status != 409 {
		t.Fatalf("switch without a CA: %d %s", r.status, r.body)
	}
	var st sshcaResponse
	r := c.do("POST", "/api/v1/ssh-ca", map[string]string{"name": "homelab"})
	if r.status != 200 {
		t.Fatalf("create CA: %d %s", r.status, r.body)
	}
	r.json(t, &st)
	if len(st.Authorities) != 1 || st.Authorities[0].State != "active" || !st.Authorities[0].HasKey || st.TTLSeconds != 600 {
		t.Fatalf("CA: %+v", st)
	}
	if r := c.do("POST", "/api/v1/ssh-ca", map[string]string{"name": "second"}); r.status != 409 {
		t.Fatalf("second CA: %d", r.status)
	}
	caPub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(st.Authorities[0].PublicKey))
	if err != nil {
		t.Fatal(err)
	}
	// The CA's private key is sealed, never in the database in the clear.
	var ct []byte
	h.st.Reader.QueryRow(`SELECT key_ciphertext FROM ssh_user_cas`).Scan(&ct)
	if len(ct) == 0 {
		t.Fatal("no sealed key")
	}

	// The host doesn't trust the CA yet: switching is refused and the host
	// keeps its credential.
	if r := c.do("PUT", "/api/v1/hosts/"+id+"/ssh-auth", map[string]string{"mode": "certificate"}); r.status != 409 || r.problemCode(t) != "certificate_refused" {
		t.Fatalf("switch before trust: %d %s", r.status, r.body)
	}
	var auth hostSSHAuthResponse
	c.do("GET", "/api/v1/hosts/"+id+"/ssh-auth", nil).json(t, &auth)
	if auth.Mode != "credential" {
		t.Fatalf("mode after a refused switch: %+v", auth)
	}

	// Operators can't run trust steps: they change how DROOL signs in.
	op := h.login("op")
	if r := op.do("POST", "/api/v1/jobs", map[string]any{"steps": []map[string]any{{"type": "ssh_ca_trust"}}, "targets": map[string]any{"hosts": []string{id}}}); r.status != 403 {
		t.Fatalf("operator trust step: %d", r.status)
	}
	// Step 1: install the trust, which proves it with a fresh connection.
	if j := trustJob(t, c, id); j.Counts["succeeded"] != 1 {
		t.Fatalf("trust job: %+v", j)
	}
	c.do("GET", "/api/v1/hosts/"+id+"/ssh-auth", nil).json(t, &auth)
	if len(auth.Trust) != 1 || auth.Trust[0].VerifiedAt == nil || auth.Trust[0].InstalledAt == nil {
		t.Fatalf("trust: %+v", auth.Trust)
	}
	cert := web.srv.LastCertificate()
	if cert == nil || cert.ValidPrincipals[0] != "deploy" || cert.ValidBefore-cert.ValidAfter != uint64((15*time.Minute).Seconds()) ||
		!strings.HasPrefix(cert.KeyId, "drool:test:web:") || cert.CertType != ssh.UserCert {
		t.Fatalf("certificate: %+v", cert)
	}
	if _, ok := cert.Permissions.Extensions["permit-port-forwarding"]; ok {
		t.Fatal("port forwarding allowed for a host that isn't a jump host")
	}

	// Step 2: switch, verified again on a fresh connection.
	if r := c.do("PUT", "/api/v1/hosts/"+id+"/ssh-auth", map[string]string{"mode": "certificate"}); r.status != 200 {
		t.Fatalf("switch: %d %s", r.status, r.body)
	}
	// The host stops accepting DROOL's static key: it works with
	// certificates alone now.
	var ck controllerKeyResponse
	c.do("GET", "/api/v1/secrets/controller-ssh-key", nil).json(t, &ck)
	staticPub, _, _, _, _ := ssh.ParseAuthorizedKey([]byte(ck.PublicKey))
	web.srv.Unauthorize(staticPub)
	h.srv.d.Pool.Evict(id)
	var probe probeResponse
	before := countAudit(t, h, "ssh.certificate.issue")
	c.do("POST", "/api/v1/hosts/"+id+"/probe", nil).json(t, &probe)
	if !probe.OK {
		t.Fatalf("probe with certificates: %+v", probe.Steps)
	}
	if countAudit(t, h, "ssh.certificate.issue") <= before {
		t.Fatal("certificate issuance not audited")
	}
	var details string
	h.st.Reader.QueryRow(`SELECT details_json FROM audit_events WHERE action = 'ssh.certificate.issue' ORDER BY seq DESC LIMIT 1`).Scan(&details)
	for _, want := range []string{`"principal":"deploy"`, `"ca":"SHA256:`, `"valid_before":`, `"serial":`, `"purpose":`} {
		if !strings.Contains(details, want) {
			t.Fatalf("issuance audit lacks %s: %s", want, details)
		}
	}
	if strings.Contains(details, "PRIVATE") {
		t.Fatal("key material in the audit log")
	}
	// The host stops trusting the CA (someone edited sshd_config): DROOL
	// doesn't quietly fall back to the static key.
	web.srv.UntrustUserCA(caPub)
	h.srv.d.Pool.Evict(id)
	c.do("POST", "/api/v1/hosts/"+id+"/probe", nil).json(t, &probe)
	if probe.OK || !strings.Contains(probeText(probe), "refused DROOL's SSH certificate") {
		t.Fatalf("probe without trust: %+v", probe.Steps)
	}
	web.srv.TrustUserCA(caPub)
	h.srv.d.Pool.Evict(id)

	// Retiring the static credential needs a fresh confirmation.
	h.clock.Store(int64(10 * time.Minute))
	if r := c.do("PATCH", "/api/v1/hosts/"+id, map[string]any{"credential_id": ""}); r.status != 403 || r.problemCode(t) != "reauth_required" {
		t.Fatalf("remove credential without reauth: %d %s", r.status, r.body)
	}
	c.reauth(t, map[string]string{"password": pw})
	if r := c.do("PATCH", "/api/v1/hosts/"+id, map[string]any{"credential_id": ""}); r.status != 200 {
		t.Fatalf("remove credential: %d %s", r.status, r.body)
	}
	c.do("GET", "/api/v1/ssh-ca", nil).json(t, &st)
	if st.CertificateHosts != 1 || st.CertificateOnlyHosts != 1 {
		t.Fatalf("counts: %+v", st)
	}
	// Going back needs a credential.
	if r := c.do("PUT", "/api/v1/hosts/"+id+"/ssh-auth", map[string]string{"mode": "credential"}); r.status != 409 {
		t.Fatalf("back without a credential: %d", r.status)
	}
	// The active CA can never be retired; removing trust from a host that
	// depends on it is refused.
	if r := c.do("POST", "/api/v1/ssh-ca/"+st.Authorities[0].ID+"/retire", nil); r.status != 409 {
		t.Fatalf("retire active: %d %s", r.status, r.body)
	}
	r = c.do("POST", "/api/v1/jobs", map[string]any{"steps": []map[string]any{{"type": "ssh_ca_trust", "action": "remove"}},
		"targets": map[string]any{"hosts": []string{id}}})
	var j jobJSON
	r.json(t, &j)
	if j = waitJob(t, c, j.ID); j.Counts["failed"] != 1 {
		t.Fatalf("remove trust from a certificate host: %+v", j)
	}
}

func probeText(p probeResponse) string {
	var b strings.Builder
	for _, s := range p.Steps {
		b.WriteString(s.Detail + " " + s.Hint + "\n")
	}
	return b.String()
}

func countAudit(t *testing.T, h *harness, action string) int {
	t.Helper()
	var n int
	h.st.Reader.QueryRow(`SELECT count(*) FROM audit_events WHERE action = ?`, action).Scan(&n)
	return n
}

func TestSSHCertificateRotation(t *testing.T) {
	h := newHarness(t)
	h.mkUser("admin", auth.RoleAdmin)
	c := h.login("admin")
	a, b := newCertHost(t), newCertHost(t)
	ida, idb := a.add(t, c, "alpha"), b.add(t, c, "beta")
	var st sshcaResponse
	c.do("POST", "/api/v1/ssh-ca", map[string]string{"name": "one"}).json(t, &st)
	oldID := st.Authorities[0].ID
	trustJob(t, c, ida, idb)
	for _, id := range []string{ida, idb} {
		if r := c.do("PUT", "/api/v1/hosts/"+id+"/ssh-auth", map[string]string{"mode": "certificate"}); r.status != 200 {
			t.Fatalf("switch: %d %s", r.status, r.body)
		}
	}
	// Introduce the new authority: the old one keeps issuing.
	r := c.do("POST", "/api/v1/ssh-ca/rotate", map[string]string{"name": "two"})
	if r.status != 200 {
		t.Fatalf("rotate: %d %s", r.status, r.body)
	}
	r.json(t, &st)
	newID := ""
	for _, ca := range st.Authorities {
		if ca.State == "next" {
			newID = ca.ID
		}
	}
	if newID == "" || len(st.Unverified) != 2 {
		t.Fatalf("after introducing: %+v", st)
	}
	if r := c.do("POST", "/api/v1/ssh-ca/rotate", map[string]string{"name": "three"}); r.status != 409 {
		t.Fatalf("two rotations at once: %d", r.status)
	}
	// Activating before the hosts trust it would strand them: refused.
	r = c.do("POST", "/api/v1/ssh-ca/"+newID+"/activate", nil)
	if r.status != 409 || !strings.Contains(string(r.body), "alpha") || !strings.Contains(string(r.body), "beta") {
		t.Fatalf("activate early: %d %s", r.status, r.body)
	}
	// One host gets the new trust; the other's sshd refuses the change.
	b.refuse = true
	if j := trustJob(t, c, ida, idb); j.Counts["succeeded"] != 1 || j.Counts["failed"] != 1 {
		t.Fatalf("trust job: %+v", j)
	}
	c.do("GET", "/api/v1/ssh-ca", nil).json(t, &st)
	if len(st.Unverified) != 1 || st.Unverified[0] != "beta" {
		t.Fatalf("unverified: %v", st.Unverified)
	}
	if r := c.do("POST", "/api/v1/ssh-ca/"+newID+"/activate", nil); r.status != 409 {
		t.Fatalf("activate with beta unverified: %d", r.status)
	}
	// beta still works with the old authority meanwhile.
	var probe probeResponse
	c.do("POST", "/api/v1/hosts/"+idb+"/probe", nil).json(t, &probe)
	if !probe.OK {
		t.Fatalf("beta during the rotation: %+v", probe.Steps)
	}
	b.refuse = false
	trustJob(t, c, idb)
	if r := c.do("POST", "/api/v1/ssh-ca/"+newID+"/activate", nil); r.status != 200 {
		t.Fatalf("activate: %d %s", r.status, r.body)
	}
	// New connections use the new authority.
	h.srv.d.Pool.Evict(ida)
	c.do("POST", "/api/v1/hosts/"+ida+"/probe", nil).json(t, &probe)
	cert := a.srv.LastCertificate()
	newPub := authorityKey(t, h, newID)
	if !probe.OK || string(cert.SignatureKey.Marshal()) != string(newPub.Marshal()) {
		t.Fatalf("after activation: ok=%v", probe.OK)
	}
	// Retire the old one: its key is destroyed; the trust step then lists
	// only the new key.
	if r := c.do("POST", "/api/v1/ssh-ca/"+oldID+"/retire", nil); r.status != 200 {
		t.Fatalf("retire old: %d %s", r.status, r.body)
	}
	var ct []byte
	h.st.Reader.QueryRow(`SELECT key_ciphertext FROM ssh_user_cas WHERE id = ?`, oldID).Scan(&ct)
	if ct != nil {
		t.Fatal("retired key kept")
	}
	if _, _, err := h.sshca.Issue(context.Background(), hostRow(t, h, ida), oldID); err == nil {
		t.Fatal("issued with a retired authority")
	}
}

func hostRow(t *testing.T, h *harness, id string) db.Host {
	t.Helper()
	row, err := h.st.R.GetHost(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return row
}

func authorityKey(t *testing.T, h *harness, id string) ssh.PublicKey {
	t.Helper()
	var line string
	h.st.Reader.QueryRow(`SELECT public_key FROM ssh_user_cas WHERE id = ?`, id).Scan(&line)
	pub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(line))
	if err != nil {
		t.Fatal(err)
	}
	return pub
}
