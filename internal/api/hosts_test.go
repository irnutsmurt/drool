package api

import (
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"

	"drool/internal/auth"
	"drool/internal/sshx/sshtest"
)

// linuxExec answers the probe's commands like a Linux host with passwordless sudo.
func linuxExec(cmd string, _ []byte) (string, string, int) {
	switch {
	case cmd == "sudo -n -- true":
		return "", "", 0
	case cmd == "uname -sr":
		return "Linux 6.8.0-test\n", "", 0
	case strings.Contains(cmd, "sdstate()") && strings.HasSuffix(strings.TrimSpace(cmd), `"$(cat /proc/1/comm 2>/dev/null)"`):
		return "running systemd\n", "", 0 // the init system step: PID 1 is systemd
	}
	return "", "unexpected: " + cmd, 127
}

type credResp struct {
	ID        string `json:"id"`
	PublicKey string `json:"public_key"`
}

func authorizeCred(t *testing.T, srv *sshtest.Server, publicKey string) {
	t.Helper()
	pub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(publicKey))
	if err != nil {
		t.Fatal(err)
	}
	srv.Authorize(pub)
}

func TestHostLifecycleOverSSH(t *testing.T) {
	h := newHarness(t)
	h.mkUser("admin", auth.RoleAdmin)
	c := h.login("admin")

	// The controller key exists from startup and is shown for installation.
	var ck controllerKeyResponse
	c.do("GET", "/api/v1/secrets/controller-ssh-key", nil).json(t, &ck)
	if !strings.HasPrefix(ck.PublicKey, "ssh-ed25519 ") || !strings.Contains(ck.InstallCmd, "authorized_keys") {
		t.Fatalf("controller key: %+v", ck)
	}

	hostKey := sshtest.NewEd25519Signer(t)
	srv := sshtest.Start(t, hostKey)
	srv.SetExec(linuxExec)
	authorizeCred(t, srv, ck.PublicKey)
	addr, port := srv.Host()

	// Create with the default credential (the controller key).
	r := c.do("POST", "/api/v1/hosts", map[string]any{
		"name": "web01", "address": addr, "port": port, "user": "deploy", "tags": []string{"role:web", "env:prod"},
	})
	if r.status != 201 {
		t.Fatalf("create host: %d %s", r.status, r.body)
	}
	var host hostJSON
	r.json(t, &host)
	if host.Status != "pending_hostkey" || host.CredentialID != ck.CredentialID || host.BecomeMethod != "sudo" {
		t.Fatalf("new host: %+v", host)
	}

	// Probing before approving a key explains what to do.
	var pr probeResponse
	c.do("POST", "/api/v1/hosts/"+host.ID+"/probe", nil).json(t, &pr)
	if pr.OK || pr.Host.Status != "pending_hostkey" {
		t.Fatalf("probe before approval: %+v", pr)
	}

	// Scan, then approve by fingerprint (the "paste expected fingerprint" path).
	var keys listResponse[hostKeyJSON]
	r = c.do("POST", "/api/v1/hosts/"+host.ID+"/hostkeys/scan", nil)
	r.json(t, &keys)
	want := ssh.FingerprintSHA256(hostKey.PublicKey())
	if r.status != 200 || len(keys.Items) != 1 || keys.Items[0].Fingerprint != want || keys.Items[0].Status != "offered" {
		t.Fatalf("scan: %d %s", r.status, r.body)
	}
	if r := c.do("POST", "/api/v1/hosts/"+host.ID+"/hostkeys/approve", map[string]string{"fingerprint": "SHA256:nope"}); r.status != 422 {
		t.Fatalf("wrong fingerprint: %d", r.status)
	}
	if r := c.do("POST", "/api/v1/hosts/"+host.ID+"/hostkeys/approve", map[string]string{"fingerprint": want}); r.status != 200 {
		t.Fatalf("approve: %d %s", r.status, r.body)
	}

	r = c.do("POST", "/api/v1/hosts/"+host.ID+"/probe", nil)
	r.json(t, &pr)
	if r.status != 200 || !pr.OK || pr.Host.Status != "ready" || len(pr.Steps) != 6 {
		t.Fatalf("probe: %d %s", r.status, r.body)
	}

	// Tags, description and groups don't touch the connection: still ready.
	r = c.do("PATCH", "/api/v1/hosts/"+host.ID, map[string]any{"tags": []string{"role:web"}, "description": "front"})
	r.json(t, &host)
	if r.status != 200 || host.Status != "ready" {
		t.Fatalf("tagging reset the connection status: %d %s", r.status, r.body)
	}
	// A different SSH user does: the last test no longer applies.
	r = c.do("PATCH", "/api/v1/hosts/"+host.ID, map[string]any{"user": "someoneelse"})
	r.json(t, &host)
	if host.Status != "untested" {
		t.Fatalf("changing the user should require a new test, status %q", host.Status)
	}
	r = c.do("PATCH", "/api/v1/hosts/"+host.ID, map[string]any{"user": "deploy"})

	// Changing the address means the pinned key no longer proves identity, so
	// the key must be approved again.
	srv2 := sshtest.Start(t, sshtest.NewEd25519Signer(t))
	addr2, port2 := srv2.Host()
	r = c.do("PATCH", "/api/v1/hosts/"+host.ID, map[string]any{"port": port2, "address": addr2})
	r.json(t, &host)
	if host.Status != "pending_hostkey" || host.PinnedKey != nil {
		t.Fatalf("address change must require re-approval: %+v", host)
	}

	// List with filters.
	var list listResponse[hostJSON]
	c.do("GET", "/api/v1/hosts?tag=role:web", nil).json(t, &list)
	if len(list.Items) != 1 {
		t.Fatalf("tag filter: %d", len(list.Items))
	}
	c.do("GET", "/api/v1/hosts?tag=role:db", nil).json(t, &list)
	if len(list.Items) != 0 {
		t.Fatalf("tag filter (none): %d", len(list.Items))
	}
}

func TestHostKeyChangeDetected(t *testing.T) {
	h := newHarness(t)
	h.mkUser("admin", auth.RoleAdmin)
	c := h.login("admin")
	var ck controllerKeyResponse
	c.do("GET", "/api/v1/secrets/controller-ssh-key", nil).json(t, &ck)

	oldKey, newKey := sshtest.NewEd25519Signer(t), sshtest.NewEd25519Signer(t)
	// Two servers stand in for the same machine before and after a reinstall.
	srvOld := sshtest.Start(t, oldKey)
	srvNew := sshtest.Start(t, newKey)
	for _, s := range []*sshtest.Server{srvOld, srvNew} {
		s.SetExec(linuxExec)
		authorizeCred(t, s, ck.PublicKey)
	}
	addr, port := srvOld.Host()
	var host hostJSON
	c.do("POST", "/api/v1/hosts", map[string]any{"name": "db01", "address": addr, "port": port, "user": "root"}).json(t, &host)
	if host.BecomeMethod != "none" {
		t.Fatalf("root should default to become none: %s", host.BecomeMethod)
	}
	c.do("POST", "/api/v1/hosts/"+host.ID+"/hostkeys/scan", nil)
	c.do("POST", "/api/v1/hosts/"+host.ID+"/hostkeys/approve", map[string]string{"fingerprint": ssh.FingerprintSHA256(oldKey.PublicKey())})

	// Point the stored address at the server with the different key, directly in
	// the database, simulating a reinstall at the same address.
	addrNew, portNew := srvNew.Host()
	if _, err := h.st.Writer.Exec(`UPDATE hosts SET connect_address = ?, ssh_port = ? WHERE id = ?`, addrNew, portNew, host.ID); err != nil {
		t.Fatal(err)
	}
	var pr probeResponse
	c.do("POST", "/api/v1/hosts/"+host.ID+"/probe", nil).json(t, &pr)
	if pr.OK || pr.Host.Status != "hostkey_mismatch" {
		t.Fatalf("key change not detected: %+v", pr)
	}
	var keys listResponse[hostKeyJSON]
	c.do("GET", "/api/v1/hosts/"+host.ID+"/hostkeys", nil).json(t, &keys)
	var offered, pinned string
	for _, k := range keys.Items {
		switch k.Status {
		case "offered":
			offered = k.Fingerprint
		case "pinned":
			pinned = k.Fingerprint
		}
	}
	if offered != ssh.FingerprintSHA256(newKey.PublicKey()) || pinned != ssh.FingerprintSHA256(oldKey.PublicKey()) {
		t.Fatalf("keys after mismatch: offered=%s pinned=%s", offered, pinned)
	}
	// Approving the new key (an explicit admin decision) restores access.
	c.do("POST", "/api/v1/hosts/"+host.ID+"/hostkeys/approve", map[string]string{"fingerprint": offered})
	c.do("POST", "/api/v1/hosts/"+host.ID+"/probe", nil).json(t, &pr)
	if !pr.OK || pr.Host.Status != "ready" {
		t.Fatalf("after re-approval: %+v", pr)
	}
}

func TestJumpHostAndCredentials(t *testing.T) {
	h := newHarness(t)
	h.mkUser("admin", auth.RoleAdmin)
	c := h.login("admin")

	// A generated key and a sudo password credential.
	var key, sudo credResp
	if r := c.do("POST", "/api/v1/secrets", map[string]any{"name": "ops-key", "kind": "ssh_private_key", "generate": true}); r.status != 201 {
		t.Fatalf("generate key: %d %s", r.status, r.body)
	} else {
		r.json(t, &key)
	}
	c.do("POST", "/api/v1/secrets", map[string]any{"name": "sudo-pw", "kind": "sudo_password", "value": "hunter2-sudo"}).json(t, &sudo)
	if r := c.do("POST", "/api/v1/secrets", map[string]any{"name": "bad", "kind": "ssh_private_key", "value": "not a key"}); r.status != 422 {
		t.Fatalf("bad key: %d", r.status)
	}
	// Secret values are never returned.
	if r := c.do("GET", "/api/v1/secrets", nil); strings.Contains(string(r.body), "hunter2") || strings.Contains(string(r.body), "PRIVATE KEY") {
		t.Fatal("secret value leaked in listing")
	}

	jumpKey, targetKey := sshtest.NewEd25519Signer(t), sshtest.NewEd25519Signer(t)
	jumpSrv, targetSrv := sshtest.Start(t, jumpKey), sshtest.Start(t, targetKey)
	jumpSrv.AllowForwarding(true)
	authorizeCred(t, jumpSrv, key.PublicKey)
	authorizeCred(t, targetSrv, key.PublicKey)
	var sawPassword bool
	targetSrv.SetExec(func(cmd string, stdin []byte) (string, string, int) {
		if cmd == "sudo -k -S -p '' -- true" {
			sawPassword = string(stdin) == "hunter2-sudo\n"
			return "", "", 0
		}
		return linuxExec(cmd, stdin)
	})

	ja, jp := jumpSrv.Host()
	var jump, web hostJSON
	c.do("POST", "/api/v1/hosts", map[string]any{"name": "jump01", "address": ja, "port": jp, "user": "deploy",
		"credential_id": key.ID, "jump_only": true}).json(t, &jump)
	ta, tp := targetSrv.Host()
	r := c.do("POST", "/api/v1/hosts", map[string]any{"name": "web02", "address": ta, "port": tp, "user": "deploy",
		"credential_id": key.ID, "become_credential_id": sudo.ID, "via_host_id": jump.ID})
	r.json(t, &web)
	if r.status != 201 || web.ViaHostID == nil || *web.ViaHostID != jump.ID {
		t.Fatalf("create via jump: %d %s", r.status, r.body)
	}

	// Scanning the target before the jump host is pinned fails clearly.
	if r := c.do("POST", "/api/v1/hosts/"+web.ID+"/hostkeys/scan", nil); r.status != 502 || !strings.Contains(string(r.body), "jump01") {
		t.Fatalf("scan with unpinned jump: %d %s", r.status, r.body)
	}
	for _, hk := range []struct {
		id  string
		key ssh.Signer
	}{{jump.ID, jumpKey}, {web.ID, targetKey}} {
		c.do("POST", "/api/v1/hosts/"+hk.id+"/hostkeys/scan", nil)
		if r := c.do("POST", "/api/v1/hosts/"+hk.id+"/hostkeys/approve", map[string]string{"fingerprint": ssh.FingerprintSHA256(hk.key.PublicKey())}); r.status != 200 {
			t.Fatalf("approve %s: %d %s", hk.id, r.status, r.body)
		}
	}
	var pr probeResponse
	c.do("POST", "/api/v1/hosts/"+jump.ID+"/probe", nil).json(t, &pr)
	if !pr.OK || len(pr.Steps) != 3 {
		t.Fatalf("jump-only probe: %+v", pr)
	}
	c.do("POST", "/api/v1/hosts/"+web.ID+"/probe", nil).json(t, &pr)
	if !pr.OK || pr.Host.Status != "ready" || !sawPassword {
		t.Fatalf("probe via jump with sudo password: ok=%v sawPassword=%v %+v", pr.OK, sawPassword, pr.Steps)
	}

	// Guard rails: two hops, deleting a used jump host, deleting a used credential.
	if r := c.do("PATCH", "/api/v1/hosts/"+jump.ID, map[string]any{"via_host_id": web.ID}); r.status != 422 {
		t.Fatalf("jump host using a jump host: %d %s", r.status, r.body)
	}
	if r := c.do("DELETE", "/api/v1/hosts/"+jump.ID, nil); r.status != 409 {
		t.Fatalf("delete used jump host: %d", r.status)
	}
	if r := c.do("DELETE", "/api/v1/secrets/"+key.ID, nil); r.status != 409 || !strings.Contains(string(r.body), "web02") {
		t.Fatalf("delete used credential: %d %s", r.status, r.body)
	}

	// Groups.
	var g groupJSON
	r = c.do("POST", "/api/v1/groups", map[string]any{"name": "web", "host_ids": []string{web.ID}})
	r.json(t, &g)
	if r.status != 201 || len(g.HostIDs) != 1 {
		t.Fatalf("create group: %d %s", r.status, r.body)
	}
	var list listResponse[hostJSON]
	c.do("GET", "/api/v1/hosts?group="+g.ID, nil).json(t, &list)
	if len(list.Items) != 1 || list.Items[0].ID != web.ID {
		t.Fatalf("group filter: %+v", list.Items)
	}
	c.do("PATCH", "/api/v1/groups/"+g.ID, map[string]any{"host_ids": []string{web.ID, jump.ID}}).json(t, &g)
	if len(g.HostIDs) != 2 {
		t.Fatalf("set members: %+v", g)
	}

	// Everything above was audited.
	var audit listResponse[auditJSON]
	c.do("GET", "/api/v1/audit?limit=200", nil).json(t, &audit)
	seen := map[string]bool{}
	for _, e := range audit.Items {
		seen[e.Action] = true
	}
	for _, a := range []string{"credential.create", "host.create", "hostkey.scan", "hostkey.approve", "host.probe", "group.create", "group.update"} {
		if !seen[a] {
			t.Errorf("no audit event %s", a)
		}
	}
}
