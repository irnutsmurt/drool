package api

import (
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"drool/internal/auth"
	"drool/internal/sshx/sshtest"
)

const (
	setupLoginPW = "api-setup-login-pw-5b1e"
	setupSudoPW  = "api-setup-sudo-pw-c07d"
)

// accountHost is an in-process SSH server playing a Linux host for DROOL's
// own SSH account: admin signs in with a password and sudo asks for one;
// the setup script makes DROOL's key work for the account it names (the
// real script runs against real OpenSSH, useradd and visudo in the
// container tests; never on this machine).
type accountHost struct {
	srv      *sshtest.Server
	key      ssh.Signer
	ctlPub   ssh.PublicKey
	mu       sync.Mutex
	accounts map[string]bool
}

func newAccountHost(t *testing.T, controllerKey string) *accountHost {
	ah := &accountHost{key: sshtest.NewEd25519Signer(t), accounts: map[string]bool{}}
	ah.ctlPub, _, _, _, _ = ssh.ParseAuthorizedKey([]byte(controllerKey))
	ah.srv = sshtest.Start(t, ah.key)
	ah.srv.SetPassword("admin", setupLoginPW)
	ah.srv.SetUserExec(func(user, cmd string, stdin []byte) (string, string, int) {
		ah.mu.Lock()
		account := ah.accounts[user]
		ah.mu.Unlock()
		if user == "admin" && strings.HasPrefix(cmd, "sudo -k -S -p '' -- ") {
			pw, rest, _ := strings.Cut(string(stdin), "\n")
			if pw != setupSudoPW {
				return "", "sudo: 1 incorrect password attempt\n", 1
			}
			cmd, stdin = strings.TrimPrefix(cmd, "sudo -k -S -p '' -- "), []byte(rest)
			if cmd == "sh -s" {
				return ah.script(string(stdin))
			}
			if cmd == "true" {
				return "", "", 0
			}
		}
		if account && cmd == "id -un" {
			return user + "\n", "", 0
		}
		if account && cmd == "sudo -n -- id -u" {
			return "0\n", "", 0
		}
		return linuxExec(cmd, stdin)
	})
	return ah
}

func (ah *accountHost) script(s string) (string, string, int) {
	_, rest, _ := strings.Cut(s, "\nU='")
	user, _, _ := strings.Cut(rest, "'")
	if strings.Contains(s, "kv ok 1") {
		return "@@account new\n@@shell /bin/bash\n@@ok 1\n", "", 0
	}
	ah.mu.Lock()
	ah.accounts[user] = true
	ah.mu.Unlock()
	ah.srv.AuthorizeUser(user, ah.ctlPub)
	return "@@account new\n@@shell /bin/bash\n@@created 1\n@@home /home/" + user + "\n@@uid 998\n@@done 1\n", "", 0
}

func TestSSHAccountSetupThroughTheAPI(t *testing.T) {
	h := newHarness(t)
	h.mkUser("admin", auth.RoleAdmin)
	h.mkUser("op", auth.RoleOperator)
	c := h.login("admin")
	op := h.login("op")
	var ck controllerKeyResponse
	c.do("GET", "/api/v1/secrets/controller-ssh-key", nil).json(t, &ck)
	web := newAccountHost(t, ck.PublicKey)
	addr, port := web.srv.Host()
	body := map[string]any{"name": "web", "address": addr, "port": port, "user": "admin",
		"setup_credentials": map[string]any{"ssh_password": setupLoginPW, "sudo_password": setupSudoPW}}

	// Only administrators store setup credentials, and the request is
	// checked before anything is stored.
	if r := op.do("POST", "/api/v1/hosts", body); r.status != 403 {
		t.Fatalf("operator adding a host with setup credentials: %d", r.status)
	}
	bad := map[string]any{"name": "bad", "address": addr, "port": port, "user": "admin",
		"setup_credentials": map[string]any{"ssh_password": "x", "ssh_private_key": "y"}}
	if r := c.do("POST", "/api/v1/hosts", bad); r.status != 422 {
		t.Fatalf("password and key: %d %s", r.status, r.body)
	}
	bad["setup_credentials"] = map[string]any{"ssh_private_key": "not a key"}
	if r := c.do("POST", "/api/v1/hosts", bad); r.status != 422 || !strings.Contains(string(r.body), "invalid SSH private key") {
		t.Fatalf("bad key: %d %s", r.status, r.body)
	}
	var host hostJSON
	if r := c.do("POST", "/api/v1/hosts", body); r.status != 201 {
		t.Fatalf("add host: %d %s", r.status, r.body)
	} else {
		r.json(t, &host)
	}
	var creds struct {
		Items []struct {
			ID        string    `json:"id"`
			Name      string    `json:"name"`
			Kind      string    `json:"kind"`
			Temporary bool      `json:"temporary"`
			UsedBy    []hostRef `json:"used_by"`
		} `json:"items"`
	}
	c.do("GET", "/api/v1/secrets", nil).json(t, &creds)
	temps := map[string]string{}
	for _, cr := range creds.Items {
		if cr.Temporary {
			temps[cr.Name] = cr.Kind
		}
	}
	if temps["setup-web-login"] != "password" || temps["setup-web-sudo"] != "sudo_password" || len(temps) != 2 {
		t.Fatalf("temporary credentials: %+v", creds.Items)
	}
	if r := c.do("PATCH", "/api/v1/hosts/"+host.ID, map[string]any{"setup_credentials": map[string]any{"ssh_password": "x"}}); r.status != 422 {
		t.Fatalf("setup credentials on an edit: %d", r.status)
	}
	// Trust the host key, test the connection as admin.
	c.do("POST", "/api/v1/hosts/"+host.ID+"/hostkeys/scan", nil)
	if r := c.do("POST", "/api/v1/hosts/"+host.ID+"/hostkeys/approve", map[string]string{"fingerprint": ssh.FingerprintSHA256(web.key.PublicKey())}); r.status != 200 {
		t.Fatalf("approve: %s", r.body)
	}
	var probe probeResponse
	c.do("POST", "/api/v1/hosts/"+host.ID+"/probe", nil).json(t, &probe)
	if !probe.OK {
		t.Fatalf("probe as the setup account: %+v", probe.Steps)
	}

	// The plan: administrators only; it changes nothing.
	if r := op.do("POST", "/api/v1/hosts/"+host.ID+"/ssh-account/plan", map[string]any{}); r.status != 403 {
		t.Fatalf("operator plan: %d", r.status)
	}
	var plan struct {
		User        string `json:"user"`
		SetupUser   string `json:"setup_user"`
		Warning     string `json:"warning"`
		Blockers    []string
		Credentials []struct {
			Name    string `json:"name"`
			Outcome string `json:"outcome"`
		} `json:"credentials"`
		Check *struct {
			OK      bool   `json:"ok"`
			Account string `json:"account"`
		} `json:"check"`
	}
	if r := c.do("POST", "/api/v1/hosts/"+host.ID+"/ssh-account/plan", map[string]any{}); r.status != 200 {
		t.Fatalf("plan: %d %s", r.status, r.body)
	} else {
		r.json(t, &plan)
		// Lists are lists, never null (the page reads their length).
		for _, k := range []string{`"blockers":[]`, `"changes":[`, `"notes":[`, `"credentials":[`, `"used_by":[]`} {
			if !strings.Contains(string(r.body), k) {
				t.Fatalf("plan JSON lacks %s: %s", k, r.body)
			}
		}
	}
	if plan.User != "drool" || plan.SetupUser != "admin" || len(plan.Blockers) != 0 || plan.Check == nil || !plan.Check.OK ||
		!strings.Contains(plan.Warning, "root-equivalent") || len(plan.Credentials) != 2 || plan.Credentials[0].Outcome != "delete" {
		t.Fatalf("plan: %+v", plan)
	}

	// The setup itself: operators can't; administrators confirm who they
	// are first; a name DROOL refuses is refused before any job exists.
	if r := op.do("POST", "/api/v1/hosts/"+host.ID+"/ssh-account", map[string]any{}); r.status != 403 {
		t.Fatalf("operator setup: %d", r.status)
	}
	if r := c.do("POST", "/api/v1/hosts/"+host.ID+"/ssh-account", map[string]any{"user": "root"}); r.status != 409 || r.problemCode(t) != "ssh_account_blocked" {
		t.Fatalf("root: %d %s", r.status, r.body)
	}
	h.clock.Store(int64(10 * time.Minute))
	if r := c.do("POST", "/api/v1/hosts/"+host.ID+"/ssh-account", map[string]any{}); r.status != 403 || r.problemCode(t) != "reauth_required" {
		t.Fatalf("setup without a recent sign-in: %d %s", r.status, r.body)
	}
	c.reauth(t, map[string]string{"password": pw})
	r := c.do("POST", "/api/v1/hosts/"+host.ID+"/ssh-account", map[string]any{})
	if r.status != 201 {
		t.Fatalf("setup: %d %s", r.status, r.body)
	}
	var j jobJSON
	r.json(t, &j)
	if j = waitJob(t, c, j.ID); j.Counts["succeeded"] != 1 {
		t.Fatalf("setup job: %+v", j)
	}
	var jh struct {
		Steps []struct {
			Result map[string]any `json:"result"`
		} `json:"steps"`
	}
	c.do("GET", "/api/v1/jobs/"+j.ID+"/hosts/"+host.ID, nil).json(t, &jh)
	if len(jh.Steps) != 1 || jh.Steps[0].Result["switched"] != true || jh.Steps[0].Result["user"] != "drool" {
		t.Fatalf("step result: %+v", jh.Steps)
	}
	c.do("GET", "/api/v1/hosts/"+host.ID, nil).json(t, &host)
	if host.User != "drool" || host.CredentialID != ck.CredentialID || host.BecomeCredentialID != nil || host.SSHAccountAt == nil {
		t.Fatalf("host after the switch: %+v", host)
	}
	c.do("GET", "/api/v1/secrets", nil).json(t, &creds)
	for _, cr := range creds.Items {
		if cr.Temporary {
			t.Fatalf("temporary credential left: %+v", cr)
		}
	}

	// Editing anything but how DROOL signs in keeps the mark; signing in as
	// someone else clears it.
	c.do("PATCH", "/api/v1/hosts/"+host.ID, map[string]any{"description": "web server"}).json(t, &host)
	if host.SSHAccountAt == nil {
		t.Fatal("a description edit cleared the account mark")
	}
	c.do("PATCH", "/api/v1/hosts/"+host.ID, map[string]any{"user": "admin"}).json(t, &host)
	if host.SSHAccountAt != nil {
		t.Fatal("still marked as DROOL's own account after a user change")
	}
	// Never a job step of its own: not submitted, not re-run.
	if r := c.do("POST", "/api/v1/jobs", map[string]any{"steps": []map[string]any{{"type": "ssh_account", "user": "drool"}},
		"targets": map[string]any{"hosts": []string{host.ID}}}); r.status != 422 || !strings.Contains(string(r.body), "host's page") {
		t.Fatalf("submitted as a job: %d %s", r.status, r.body)
	}
	if r := c.do("POST", "/api/v1/jobs/"+j.ID+"/rerun", map[string]any{}); r.status < 400 {
		t.Fatalf("re-run: %d %s", r.status, r.body)
	}
	// The audit records say what happened, never a secret.
	var all strings.Builder
	rows, err := h.st.Reader.Query(`SELECT action || ' ' || result || ' ' || details_json FROM audit_events ORDER BY seq`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var s string
		rows.Scan(&s)
		all.WriteString(s + "\n")
	}
	rows.Close()
	for _, want := range []string{`host.create success`, `"setup_credentials":[`, `host.ssh_account success`, `"switched":true`, `credential.delete success`} {
		if !strings.Contains(all.String(), want) {
			t.Fatalf("audit lacks %s:\n%s", want, all.String())
		}
	}
	if strings.Contains(all.String(), setupLoginPW) || strings.Contains(all.String(), setupSudoPW) {
		t.Fatal("a setup password is in the audit log")
	}
}
