package sshaccount

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"drool/internal/audit"
	"drool/internal/driver"
	"drool/internal/inventory"
	"drool/internal/model"
	"drool/internal/secrets"
	"drool/internal/sshx"
	"drool/internal/sshx/sshtest"
	"drool/internal/store"
	"drool/internal/store/db"
)

// Values that must never show up in output, results, errors or audit.
const (
	loginPW = "bootstrap-login-pw-7f3a"
	sudoPW  = "bootstrap-sudo-pw-91c2"
)

type sink struct{ out, err bytes.Buffer }

func (s *sink) Stdout() io.Writer { return &s.out }
func (s *sink) Stderr() io.Writer { return &s.err }

type env struct {
	t      *testing.T
	ctx    context.Context
	st     *store.Store
	sec    *secrets.Service
	inv    *inventory.Service
	pool   *sshx.Pool
	svc    *Service
	ctlID  string
	ctlPub ssh.PublicKey
}

func newEnv(t *testing.T) *env {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), store.FileName))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	sec := secrets.NewService(st, secrets.NewBoxForTest())
	row, _, err := sec.EnsureControllerKey(ctx, func(*db.Queries, db.Secret) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	pub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(row.PublicPart))
	if err != nil {
		t.Fatal(err)
	}
	dialer := &sshx.Dialer{}
	pool := sshx.NewPool(dialer)
	t.Cleanup(pool.Close)
	inv := inventory.NewService(st, sec, dialer, pool)
	svc := New(Deps{Store: st, Secrets: sec, Hosts: inv, Dialer: dialer, Pool: pool,
		Audit: audit.NewWriter(st, slog.New(slog.DiscardHandler)), Controller: "ctl"})
	return &env{t: t, ctx: ctx, st: st, sec: sec, inv: inv, pool: pool, svc: svc, ctlID: row.ID, ctlPub: pub}
}

func (e *env) secret(name, kind, value string, temporary bool) string {
	e.t.Helper()
	p, err := e.sec.Prepare(secrets.Input{Name: name, Kind: kind, Value: value, Temporary: temporary})
	if err != nil {
		e.t.Fatal(err)
	}
	if err := e.st.InTx(e.ctx, func(q *db.Queries) error { return e.sec.InsertTx(e.ctx, q, p) }); err != nil {
		e.t.Fatal(err)
	}
	return p.Row.ID
}

// addHost adds a host for srv and pins its key (unless pin is false).
func (e *env) addHost(name string, srv *sshtest.Server, in inventory.HostInput, pin bool) string {
	e.t.Helper()
	addr, port := srv.Host()
	in.Name, in.Address, in.Port = name, addr, port
	var id string
	err := e.st.InTx(e.ctx, func(q *db.Queries) error {
		h, err := e.inv.CreateHostTx(e.ctx, q, in)
		if err != nil {
			return err
		}
		id = h.ID
		if !pin {
			return nil
		}
		keys, err := e.inv.RecordOfferedKeysTx(e.ctx, q, h.ID, []ssh.PublicKey{srv.HostKeys[0].PublicKey()})
		if err != nil {
			return err
		}
		_, err = e.inv.ApproveHostKeyTx(e.ctx, q, h.ID, keys[0].ID, "")
		return err
	})
	if err != nil {
		e.t.Fatal(err)
	}
	return id
}

func (e *env) host(id string) db.Host {
	e.t.Helper()
	h, err := e.st.R.GetHost(e.ctx, id)
	if err != nil {
		e.t.Fatal(err)
	}
	return h
}

func (e *env) exists(secretID string) bool {
	_, err := e.st.R.GetSecret(e.ctx, secretID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		e.t.Fatal(err)
	}
	return err == nil
}

// run executes the setup step as the engine would.
func (e *env) run(hostID, user string) (*driver.OpResult, error, *sink) {
	e.t.Helper()
	out := &sink{}
	ctx := driver.WithStepInfo(e.ctx, driver.StepInfo{JobID: "job_test", HostID: hostID, Step: 0, Attempt: 1})
	res, err := e.svc.Execute(ctx, hostID, model.Operation{Type: model.OpSSHAccount, SSHAccount: &model.SSHAccountOp{User: user}}, out)
	return res, err, out
}

func (e *env) auditRows() []string {
	e.t.Helper()
	rows, err := e.st.Reader.QueryContext(e.ctx, `SELECT action || ' ' || result || ' ' || details_json FROM audit_events ORDER BY ts, id`)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		rows.Scan(&s)
		out = append(out, s)
	}
	return out
}

// noSecrets fails if a secret value shows up anywhere the setup leaves
// words: output, result data, error, audit records.
func (e *env) noSecrets(res *driver.OpResult, err error, out *sink) {
	e.t.Helper()
	var all strings.Builder
	all.WriteString(out.out.String() + out.err.String())
	if res != nil {
		b, _ := json.Marshal(res.Data)
		all.Write(b)
	}
	if err != nil {
		all.WriteString(err.Error() + model.AsOpError(err).Hint)
	}
	all.WriteString(strings.Join(e.auditRows(), "\n"))
	for _, v := range []string{loginPW, sudoPW} {
		if strings.Contains(all.String(), v) {
			e.t.Fatalf("a secret value leaked:\n%s", all.String())
		}
	}
}

var userLine = regexp.MustCompile(`(?m)^U='([^']+)'$`)

// fakeHost plays a Linux host: the setup account admin (password sign-in,
// sudo asks for a password) and what the setup script does to it. The
// script itself runs against real sshd, useradd and visudo in the
// integration tests.
type fakeHost struct {
	t      *testing.T
	srv    *sshtest.Server
	ctlPub ssh.PublicKey

	mu        sync.Mutex
	accounts  map[string]bool // accounts the setup created
	ran       []string        // "user: command"
	authorize bool            // the setup authorizes DROOL's key for the account
	sudoOK    bool            // the account's passwordless sudo works
	// apply overrides the setup script's outcome.
	apply func(user string) (stdout, stderr string, exit int)
	// effect runs when the setup script runs (before its outcome).
	effect func()
	// connections when the setup ran, and when the account first signed in.
	connsAtApply, connsAtVerify int64
}

func newFakeHost(t *testing.T, ctlPub ssh.PublicKey) *fakeHost {
	f := &fakeHost{t: t, srv: sshtest.Start(t), ctlPub: ctlPub, accounts: map[string]bool{}, authorize: true, sudoOK: true}
	f.srv.SetPassword("admin", loginPW)
	f.srv.SetUserExec(f.exec)
	return f
}

func (f *fakeHost) exec(user, cmd string, stdin []byte) (string, string, int) {
	f.mu.Lock()
	f.ran = append(f.ran, user+": "+cmd)
	isAccount := f.accounts[user]
	f.mu.Unlock()
	switch {
	case user == "admin" && strings.HasPrefix(cmd, "sudo -k -S -p '' -- "):
		pw, rest, _ := strings.Cut(string(stdin), "\n")
		if pw != sudoPW {
			return "", "Sorry, try again.\nsudo: 1 incorrect password attempt\n", 1
		}
		return f.root(strings.TrimPrefix(cmd, "sudo -k -S -p '' -- "), rest)
	case user == "admin" && cmd == "sudo -k -S -p '' -- true", user == "admin" && cmd == "true":
		return "", "", 0
	case isAccount && cmd == "id -un":
		f.mu.Lock()
		if f.connsAtVerify == 0 {
			f.connsAtVerify = f.srv.Connections()
		}
		f.mu.Unlock()
		return user + "\n", "", 0
	case isAccount && (cmd == "sudo -n -- id -u" || cmd == "sudo -n -- true"):
		if !f.sudoOK {
			return "", "sudo: a password is required\n", 1
		}
		if cmd == "sudo -n -- true" {
			return "", "", 0
		}
		return "0\n", "", 0
	}
	return "", "unknown command: " + cmd, 127
}

func (f *fakeHost) root(cmd, script string) (string, string, int) {
	if cmd != "sh -s" {
		return "", "unexpected root command: " + cmd, 127
	}
	m := userLine.FindStringSubmatch(script)
	if m == nil {
		f.t.Errorf("no account name in the script")
		return "", "", 2
	}
	user := m[1]
	if strings.Contains(script, "kv ok 1") { // the plan's read-only check
		return "@@account new\n@@shell /bin/bash\n@@ok 1\n", "", 0
	}
	f.mu.Lock()
	f.connsAtApply = f.srv.Connections()
	f.mu.Unlock()
	if f.effect != nil {
		f.effect()
	}
	if f.apply != nil {
		return f.apply(user)
	}
	f.mu.Lock()
	f.accounts[user] = true
	f.mu.Unlock()
	if f.authorize {
		f.srv.AuthorizeUser(user, f.ctlPub)
	}
	return "@@account new\n@@shell /bin/bash\n@@created 1\n@@key_added 1\n@@sudoers_changed 1\n@@home /home/" + user + "\n@@uid 998\n@@done 1\n", "", 0
}

func (f *fakeHost) commandsOf(user string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, r := range f.ran {
		if c, ok := strings.CutPrefix(r, user+": "); ok {
			out = append(out, c)
		}
	}
	return out
}

// bootstrap adds a host signing in as admin with temporary credentials
// entered for the setup.
func (e *env) bootstrap(name string, f *fakeHost) (hostID, loginID, sudoID string) {
	loginID = e.secret("setup-"+name+"-login", secrets.KindPassword, loginPW, true)
	sudoID = e.secret("setup-"+name+"-sudo", secrets.KindSudoPassword, sudoPW, true)
	hostID = e.addHost(name, f.srv, inventory.HostInput{User: "admin", CredentialID: loginID, BecomeMethod: "sudo", BecomeCredentialID: sudoID}, true)
	return
}

func data(t *testing.T, res *driver.OpResult) Result {
	t.Helper()
	if res == nil {
		t.Fatal("no result")
	}
	b, _ := json.Marshal(res.Data)
	var r Result
	if err := json.Unmarshal(b, &r); err != nil {
		t.Fatal(err)
	}
	return r
}

// unchanged checks that a host still signs in exactly as before.
func (e *env) unchanged(hostID, loginID, sudoID string) {
	e.t.Helper()
	h := e.host(hostID)
	if h.SshUser != "admin" || h.SshCredentialID.String != loginID || h.BecomeCredentialID.String != sudoID {
		e.t.Fatalf("the host's sign-in changed: %s %s %s", h.SshUser, h.SshCredentialID.String, h.BecomeCredentialID.String)
	}
	if !e.exists(loginID) || !e.exists(sudoID) {
		e.t.Fatal("a setup credential was deleted although nothing was switched")
	}
}

func TestSetUpSwitchesOnlyAfterAFreshSignIn(t *testing.T) {
	e := newEnv(t)
	f := newFakeHost(t, e.ctlPub)
	id, loginID, sudoID := e.bootstrap("web", f)

	res, err, out := e.run(id, "drool")
	if err != nil {
		t.Fatalf("setup failed: %v\n%s", err, out.out.String())
	}
	r := data(t, res)
	if !r.Verified || !r.Switched || r.Account != "created" || r.Phase != "done" || r.SetupUser != "admin" {
		t.Fatalf("result: %+v", r)
	}
	h := e.host(id)
	if h.SshUser != "drool" || h.SshCredentialID.String != e.ctlID || h.BecomeMethod != "sudo" || h.BecomeCredentialID.Valid || !h.SshAccountAt.Valid {
		t.Fatalf("host after the switch: user %s cred %s become %s/%v", h.SshUser, h.SshCredentialID.String, h.BecomeMethod, h.BecomeCredentialID)
	}
	// The proof was a new connection as the account, with sudo checked
	// over it: never the pooled setup connection.
	f.mu.Lock()
	apply, verify := f.connsAtApply, f.connsAtVerify
	f.mu.Unlock()
	if verify <= apply {
		t.Fatalf("the account's first sign-in rode an existing connection (%d at setup, %d at sign-in)", apply, verify)
	}
	cmds := f.commandsOf("drool")
	if len(cmds) < 2 || cmds[0] != "id -un" || cmds[1] != "sudo -n -- id -u" {
		t.Fatalf("commands as drool: %q", cmds)
	}
	// Both setup credentials were entered for the setup and nothing else
	// uses them: forgotten.
	if e.exists(loginID) || e.exists(sudoID) {
		t.Fatal("temporary setup credentials were kept")
	}
	if len(r.Credentials) != 2 || r.Credentials[0].Outcome != "deleted" || r.Credentials[1].Outcome != "deleted" {
		t.Fatalf("credential outcomes: %+v", r.Credentials)
	}
	rows := strings.Join(e.auditRows(), "\n")
	for _, want := range []string{`host.ssh_account success`, `"switched":true`, `"user":"drool"`, `"setup_user":"admin"`, `credential.delete success`, `"temporary":true`} {
		if !strings.Contains(rows, want) {
			t.Fatalf("audit lacks %s:\n%s", want, rows)
		}
	}
	for _, want := range []string{"Switched: web now signs in as drool", "Forgot the setup credential setup-web-login", "on a brand-new connection"} {
		if !strings.Contains(out.out.String(), want) {
			t.Fatalf("output lacks %q:\n%s", want, out.out.String())
		}
	}
	e.noSecrets(res, err, out)
	// The setup account was never asked to change itself: every root
	// command was the setup script.
	for _, c := range f.commandsOf("admin") {
		if c != "sudo -k -S -p '' -- sh -s" {
			t.Fatalf("unexpected command as admin: %q", c)
		}
	}
}

func TestSharedCredentialsAreRetained(t *testing.T) {
	e := newEnv(t)
	f := newFakeHost(t, e.ctlPub)
	id, loginID, sudoID := e.bootstrap("web", f)
	// Another host uses the setup's sudo password too.
	other := newFakeHost(t, e.ctlPub)
	e.addHost("db", other.srv, inventory.HostInput{User: "admin", CredentialID: e.ctlID, BecomeMethod: "sudo", BecomeCredentialID: sudoID}, true)
	// A stored (not temporary) credential is never deleted by the setup.
	stored := e.secret("ops-password", secrets.KindPassword, loginPW, false)
	if _, err := e.st.Writer.ExecContext(e.ctx, `UPDATE hosts SET ssh_credential_id = ? WHERE id = ?`, stored, id); err != nil {
		t.Fatal(err)
	}
	res, err, out := e.run(id, "drool")
	if err != nil {
		t.Fatalf("setup failed: %v\n%s", err, out.out.String())
	}
	r := data(t, res)
	got := map[string]CredentialResult{}
	for _, c := range r.Credentials {
		got[c.Name] = c
	}
	if c := got["setup-web-sudo"]; c.Outcome != "retained" || len(c.UsedBy) != 1 || c.UsedBy[0] != "db" {
		t.Fatalf("shared setup credential: %+v", c)
	}
	if c := got["ops-password"]; c.Outcome != "kept" {
		t.Fatalf("stored credential: %+v", c)
	}
	if !e.exists(sudoID) || !e.exists(stored) {
		t.Fatal("a credential still in use, or a stored one, was deleted")
	}
	if !e.exists(loginID) {
		// Not used by web any more (the stored one replaced it), but by
		// no one else either: it would have been forgotten had web used it.
		t.Log("setup-web-login stays: web didn't use it when the setup ran")
	}
	if !strings.Contains(out.out.String(), "Kept the setup credential setup-web-sudo: db still uses it") {
		t.Fatalf("output: %s", out.out.String())
	}
	e.noSecrets(res, err, out)
}

func TestJumpHostIsTraversedAndItsCredentialKept(t *testing.T) {
	e := newEnv(t)
	f := newFakeHost(t, e.ctlPub)
	jump := sshtest.Start(t)
	jump.SetPassword("admin", loginPW)
	jump.AllowForwarding(true)
	loginID := e.secret("setup-shared-login", secrets.KindPassword, loginPW, true)
	sudoID := e.secret("setup-web-sudo", secrets.KindSudoPassword, sudoPW, true)
	// The jump host signs in with the same (temporary) password.
	jumpID := e.addHost("bastion", jump, inventory.HostInput{User: "admin", CredentialID: loginID, BecomeMethod: "none", JumpOnly: true}, true)
	id := e.addHost("inner", f.srv, inventory.HostInput{User: "admin", CredentialID: loginID, BecomeMethod: "sudo", BecomeCredentialID: sudoID, ViaHostID: jumpID}, true)
	before := jump.Connections()
	res, err, out := e.run(id, "drool")
	if err != nil {
		t.Fatalf("setup failed: %v\n%s", err, out.out.String())
	}
	r := data(t, res)
	if !r.Switched || !strings.Contains(out.out.String(), "on a brand-new connection through bastion") {
		t.Fatalf("result %+v\n%s", r, out.out.String())
	}
	// Setup connection, the account's fresh one, and the check after the
	// switch: each dialled the jump host anew.
	if n := jump.Connections() - before; n < 3 {
		t.Fatalf("jump host connections during the setup: %d", n)
	}
	f.mu.Lock()
	apply, verify := f.connsAtApply, f.connsAtVerify
	f.mu.Unlock()
	if verify <= apply {
		t.Fatal("the fresh sign-in didn't open a new connection through the jump host")
	}
	if !e.exists(loginID) || e.exists(sudoID) {
		t.Fatalf("the jump host's credential must stay (it uses it) and the unused sudo password go: login %v sudo %v", e.exists(loginID), e.exists(sudoID))
	}
	jh := e.host(jumpID)
	if jh.SshUser != "admin" || jh.SshCredentialID.String != loginID {
		t.Fatalf("the jump host's own sign-in changed: %s %s", jh.SshUser, jh.SshCredentialID.String)
	}
	got := map[string]string{}
	for _, c := range r.Credentials {
		got[c.Name] = c.Outcome
	}
	if got["setup-shared-login"] != "retained" || got["setup-web-sudo"] != "deleted" {
		t.Fatalf("outcomes: %v", got)
	}
	e.noSecrets(res, err, out)
}

func TestFailuresLeaveTheHostAsItWas(t *testing.T) {
	cases := []struct {
		name  string
		setup func(e *env, f *fakeHost, hostID string)
		phase string
		want  string
		class model.ErrorClass
	}{
		{"an existing account DROOL didn't create", func(e *env, f *fakeHost, _ string) {
			f.apply = func(user string) (string, string, int) {
				return "@@ERR exists an account named " + user + " already exists on this host, and DROOL didn't create it\n", "", 1
			}
		}, "check", "already exists", model.ClassConfiguration},
		{"the setup account's sudo refuses", func(e *env, f *fakeHost, hostID string) {
			wrong := e.secret("wrong-sudo", secrets.KindSudoPassword, "not-the-password", false)
			if _, err := e.st.Writer.ExecContext(e.ctx, `UPDATE hosts SET become_credential_id = ? WHERE id = ?`, wrong, hostID); err != nil {
				e.t.Fatal(err)
			}
		}, "check", "sudo rejected the password", model.ClassAuthorization},
		{"the new account can't sign in", func(e *env, f *fakeHost, _ string) { f.authorize = false }, "verify", "authentication as \"drool\" failed", model.ClassAuthentication},
		{"the new account's sudo asks for a password", func(e *env, f *fakeHost, _ string) { f.sudoOK = false }, "verify", "sudo without a password didn't work", model.ClassAuthorization},
		{"the host key changes before the fresh sign-in", func(e *env, f *fakeHost, _ string) {
			f.effect = func() { f.srv.SetHostKeys(sshtest.NewEd25519Signer(e.t)) }
		}, "verify", "host key", model.ClassHostKey},
		{"the host's settings change meanwhile", func(e *env, f *fakeHost, hostID string) {
			f.effect = func() {
				if _, err := e.st.Writer.ExecContext(e.ctx, `UPDATE hosts SET ssh_needs_pty = 1 WHERE id = ?`, hostID); err != nil {
					e.t.Error(err)
				}
			}
		}, "switch", "your change wins", model.ClassConfiguration},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			f := newFakeHost(t, e.ctlPub)
			id, loginID, sudoID := e.bootstrap("web", f)
			c.setup(e, f, id)
			h0 := e.host(id)
			res, err, out := e.run(id, "drool")
			if err == nil {
				t.Fatalf("setup succeeded:\n%s", out.out.String())
			}
			oe := model.AsOpError(err)
			if !strings.Contains(err.Error(), c.want) || oe.Class != c.class {
				t.Fatalf("error %v (class %s), want %q (%s)", err, oe.Class, c.want, c.class)
			}
			if !strings.Contains(err.Error(), "Nothing was switched") && !strings.Contains(err.Error(), "didn't switch") {
				t.Fatalf("the error doesn't say nothing was switched: %v", err)
			}
			r := data(t, res)
			if r.Switched || r.Phase != c.phase {
				t.Fatalf("result: %+v (want phase %s)", r, c.phase)
			}
			h := e.host(id)
			if h.SshUser != h0.SshUser || h.SshCredentialID != h0.SshCredentialID || h.BecomeCredentialID != h0.BecomeCredentialID || h.SshAccountAt.Valid {
				t.Fatalf("the host's sign-in changed: %+v", h)
			}
			if !e.exists(loginID) || !e.exists(sudoID) {
				t.Fatal("a setup credential was deleted although nothing was switched")
			}
			rows := strings.Join(e.auditRows(), "\n")
			if !strings.Contains(rows, "host.ssh_account failure") || !strings.Contains(rows, `"switched":false`) || strings.Contains(rows, "credential.delete") {
				t.Fatalf("audit:\n%s", rows)
			}
			e.noSecrets(res, err, out)
		})
	}
}

func TestLostConnectionIsReportedAsUnknown(t *testing.T) {
	e := newEnv(t)
	f := newFakeHost(t, e.ctlPub)
	id, loginID, sudoID := e.bootstrap("web", f)
	f.effect = func() { f.srv.Restart(100*time.Millisecond, nil) }
	f.apply = func(string) (string, string, int) { return "", "", 0 }
	res, err, out := e.run(id, "drool")
	if err == nil {
		t.Fatal("setup succeeded over a dropped connection")
	}
	r := data(t, res)
	if !r.OutcomeUnknown || r.Switched || !strings.Contains(err.Error(), "doesn't know how far it got") {
		t.Fatalf("result %+v, error %v", r, err)
	}
	if !strings.Contains(model.AsOpError(err).Hint, "Run the setup again") {
		t.Fatalf("hint: %q", model.AsOpError(err).Hint)
	}
	e.unchanged(id, loginID, sudoID)
	e.noSecrets(res, err, out)
}

func TestRunningAgainContinuesWithTheAccount(t *testing.T) {
	e := newEnv(t)
	f := newFakeHost(t, e.ctlPub)
	id, loginID, sudoID := e.bootstrap("web", f)
	f.sudoOK = false
	if _, err, _ := e.run(id, "drool"); err == nil {
		t.Fatal("first attempt succeeded")
	}
	e.unchanged(id, loginID, sudoID)
	// The cause is fixed; the script finds DROOL's account and continues.
	f.sudoOK = true
	f.apply = func(user string) (string, string, int) {
		return "@@account existing\n@@shell /bin/bash\n@@created 0\n@@home /home/drool\n@@uid 998\n@@done 1\n", "", 0
	}
	res, err, out := e.run(id, "drool")
	if err != nil {
		t.Fatalf("second attempt: %v\n%s", err, out.out.String())
	}
	if r := data(t, res); r.Account != "existing" || !r.Switched {
		t.Fatalf("result: %+v", r)
	}
	if !strings.Contains(out.out.String(), "exists from an earlier setup by DROOL") {
		t.Fatalf("output: %s", out.out.String())
	}
}

func TestPlan(t *testing.T) {
	e := newEnv(t)
	f := newFakeHost(t, e.ctlPub)
	id, _, _ := e.bootstrap("web", f)
	p, err := e.svc.Plan(e.ctx, id, "drool")
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Blockers) != 0 || p.Check == nil || !p.Check.OK || p.Check.Account != "new" || len(p.Changes) != 5 {
		t.Fatalf("plan: %+v", p)
	}
	if p.Warning != RootEquivalent || !strings.Contains(p.Warning, "root-equivalent") {
		t.Fatalf("warning: %q", p.Warning)
	}
	if !strings.Contains(strings.Join(p.Notes, " "), "won't delete or change the account it uses for the setup (admin)") {
		t.Fatalf("notes: %q", p.Notes)
	}
	for _, c := range p.Credentials {
		if c.Outcome != OutcomeDelete || !c.Temporary {
			t.Fatalf("credentials: %+v", p.Credentials)
		}
	}
	if !strings.Contains(strings.Join(p.Changes, " "), "/etc/sudoers.d/drool-account-drool") {
		t.Fatalf("changes: %q", p.Changes)
	}
	// The plan only read: nothing ran as the account, nothing changed.
	if len(f.commandsOf("drool")) != 0 || e.host(id).SshUser != "admin" {
		t.Fatal("planning changed something")
	}
	// What the host says when the name is taken.
	f.mu.Lock()
	f.srv.SetUserExec(func(user, cmd string, stdin []byte) (string, string, int) {
		return "@@ERR exists an account named drool already exists on this host, and DROOL didn't create it\n", "", 1
	})
	f.mu.Unlock()
	p, err = e.svc.Plan(e.ctx, id, "drool")
	if err != nil {
		t.Fatal(err)
	}
	if p.Check == nil || p.Check.OK || p.Check.Code != "exists" || len(p.Blockers) != 1 || len(p.Changes) != 0 {
		t.Fatalf("plan for a taken name: %+v", p)
	}
}

func TestBlockers(t *testing.T) {
	e := newEnv(t)
	f := newFakeHost(t, e.ctlPub)
	id, _, _ := e.bootstrap("web", f)
	unpinned := e.addHost("new", f.srv, inventory.HostInput{User: "admin", CredentialID: e.ctlID}, false)
	jumpOnly := e.addHost("bastion", f.srv, inventory.HostInput{User: "admin", CredentialID: e.ctlID, JumpOnly: true}, true)
	cases := []struct{ host, user, want string }{
		{id, "root", "can't be root"},
		{id, "Drool", "lower-case"},
		{id, "drool.admin", "lower-case"},
		{id, "admin", "already signs in as admin"},
		{unpinned, "drool", "Approve new's host key first"},
		{jumpOnly, "drool", "jump host only"},
	}
	for _, c := range cases {
		b, err := e.svc.Blockers(e.ctx, c.host, c.user)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(strings.Join(b, " "), c.want) {
			t.Errorf("%s as %s: %q, want %q", c.host, c.user, b, c.want)
		}
	}
	if _, err := e.st.Writer.ExecContext(e.ctx, `UPDATE hosts SET ssh_auth = 'certificate' WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	if b, _ := e.svc.Blockers(e.ctx, id, "drool"); !strings.Contains(strings.Join(b, " "), "SSH certificates") {
		t.Errorf("certificate mode: %q", b)
	}
	// Execute refuses the same way, before contacting the host.
	res, err, _ := e.run(id, "drool")
	if err == nil || !strings.Contains(err.Error(), "SSH certificates") || data(t, res).Phase != "check" {
		t.Fatalf("execute in certificate mode: %v", err)
	}
	if b, _ := e.svc.Blockers(e.ctx, "host_missing", "drool"); b != nil {
		t.Fatal("blockers for a missing host")
	}
}

// The script is valid sh in both modes, quotes what it's given, and checks
// ownership before it changes anything.
func TestScript(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh")
	}
	for _, mode := range []string{"check", "apply"} {
		s, err := renderScript(scriptInput{Mode: mode, User: "drool", Key: "ssh-ed25519 AAAAC3Nza drool:controller-default",
			Fingerprint: "SHA256:abc", Token: "0123456789ab", Controller: "it's-ctl"})
		if err != nil {
			t.Fatal(err)
		}
		f := filepath.Join(t.TempDir(), mode+".sh")
		os.WriteFile(f, []byte(s), 0o600)
		if out, err := exec.Command(sh, "-n", f).CombinedOutput(); err != nil {
			t.Fatalf("%s script: %v: %s", mode, err, out)
		}
		if !strings.Contains(s, `CONTROLLER='it'\''s-ctl'`) {
			t.Fatalf("%s script doesn't quote: %s", mode, s[:400])
		}
		// Ownership is settled before the first change.
		owner := strings.Index(s, `err exists "an account named $U already exists`)
		first := strings.Index(s, "useradd -r")
		if mode == "check" && first != -1 && strings.Index(s, "kv ok 1") > first {
			t.Fatal("the check script reaches useradd")
		}
		if mode == "apply" && (owner == -1 || first == -1 || owner > first) {
			t.Fatal("the apply script may create the account before checking the name")
		}
	}
	if markerPath("drool") != "/var/lib/drool/ssh-accounts/drool" || sudoersPath("drool") != "/etc/sudoers.d/drool-account-drool" {
		t.Fatal("paths")
	}
}

// The cleanup scripts (dogfooding correction batch) are valid shell, quote
// what they're given, prove ownership before anything is written, carry the
// helper only when starting it, and the helper checks again before its
// first removal and removes the marker last. They're only parsed here (sh
// -n): they remove accounts, and run against real hosts in the container
// tests.
func TestCleanupScripts(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh")
	}
	parse := func(name, s string) {
		t.Helper()
		f := filepath.Join(t.TempDir(), name+".sh")
		os.WriteFile(f, []byte(s), 0o600)
		if out, err := exec.Command(sh, "-n", f).CombinedOutput(); err != nil {
			t.Fatalf("%s: %v: %s", name, err, out)
		}
	}
	for _, mode := range []string{"check", "start"} {
		s, err := renderCleanup(mode, "drool", "SHA256:a'b")
		if err != nil {
			t.Fatal(err)
		}
		parse(mode, s)
		if !strings.Contains(s, `KEYFP='SHA256:a'\''b'`) {
			t.Fatalf("%s doesn't quote", mode)
		}
		proof := strings.Index(s, `err replaced "the account $U isn't the one DROOL created`)
		if proof == -1 || strings.Index(s, "err notours") > proof || strings.Index(s, "err foreign") > proof {
			t.Fatalf("%s: ownership checks out of order", mode)
		}
		helper := strings.Index(s, "DROOL_ACCOUNT_CLEANUP")
		switch mode {
		case "check":
			if helper != -1 || strings.Contains(s, "systemd-run") || strings.Contains(s, "mktemp") {
				t.Fatal("the check writes or starts something")
			}
		case "start":
			if helper == -1 || helper < proof || !strings.Contains(s, "systemd-run --quiet --collect") || !strings.Contains(s, `setsid /bin/sh "$D/cleanup.sh" </dev/null >/dev/null 2>&1 &`) {
				t.Fatal("the start doesn't stage and start the helper after the proof")
			}
		}
	}
	h, err := CleanupHelper("drool", "SHA256:abc")
	if err != nil {
		t.Fatal(err)
	}
	parse("helper", h)
	recheck := strings.Index(h, `[ "$(mval key)" != "$KEYFP" ] || [ -z "$muid" ] || [ "$muid" != "$uid" ]`)
	first := strings.Index(h, "rm -f -- \"$S\"")
	if recheck == -1 || first == -1 || recheck > first {
		t.Fatal("the helper removes something before checking ownership again")
	}
	if strings.Index(h, `rm -f -- "$M"`) < strings.Index(h, `userdel "$U"`) {
		t.Fatal("the marker goes before the account")
	}
	for _, want := range []string{`[ "$(head -n 1 "$S")" = "$HEADER" ]`, `[ "$(stat -c %u "$H" 2>/dev/null)" = "$uid" ]`, `/|/root|/home|/var|/var/lib|/usr|/etc`,
		`[ -n "$mgid" ]`, `/run/drool-account-cleanup.*|/var/tmp/drool-account-cleanup.*) rm -rf -- "$me"`} {
		if !strings.Contains(h, want) {
			t.Errorf("the helper lacks %q", want)
		}
	}
	// The setup now records the group created with the account.
	s, _ := renderScript(scriptInput{Mode: "apply", User: "drool", Key: "ssh-ed25519 AAAAC3Nza drool", Fingerprint: "SHA256:abc", Token: "0123456789ab"})
	if !strings.Contains(s, `printf 'gid=%s\n' "$gid" >>"$M"`) {
		t.Fatal("the setup doesn't record the account's group")
	}
}
