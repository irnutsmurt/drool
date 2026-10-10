package sshca

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"drool/internal/audit"
	"drool/internal/secrets"
	"drool/internal/sshx"
	"drool/internal/sshx/sshtest"
	"drool/internal/store"
	"drool/internal/store/db"
)

type env struct {
	st  *store.Store
	svc *Service
	box *secrets.Box
}

func newEnv(t *testing.T) *env {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), store.FileName))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	box := secrets.NewBoxForTest()
	svc := New(Deps{Store: st, Box: box, Audit: audit.NewWriter(st, slog.New(slog.DiscardHandler)), Controller: "ctl"})
	return &env{st: st, svc: svc, box: box}
}

func (e *env) create(t *testing.T, name string) *CA {
	t.Helper()
	var ca *CA
	err := e.st.InRawTx(context.Background(), func(tx *sql.Tx) error {
		var err error
		ca, err = e.svc.CreateTx(context.Background(), tx, name, "")
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return ca
}

func host(name string) db.Host {
	return db.Host{ID: "host_" + name, Name: name, SshUser: "deploy", SshAuth: "certificate"}
}

// dial authenticates to srv with a certificate the service issues now.
func dial(t *testing.T, e *env, srv *sshtest.Server, h db.Host, caID string) error {
	t.Helper()
	addr, port := srv.Host()
	tg := &sshx.Target{HostID: h.ID, Name: h.Name, Address: addr, Port: port, User: h.SshUser, PinnedKey: srv.HostKeys[0].PublicKey()}
	if err := e.svc.prepare(tg, h, caID, "fp"); err != nil {
		return err
	}
	cl, err := (&sshx.Dialer{}).Dial(context.Background(), tg)
	if err != nil {
		return err
	}
	cl.Close()
	return nil
}

func caKey(t *testing.T, ca *CA) ssh.PublicKey {
	pub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(ca.PublicKey))
	if err != nil {
		t.Fatal(err)
	}
	return pub
}

func TestCertificateIsShortLivedAndNamesThePrincipal(t *testing.T) {
	e := newEnv(t)
	ca := e.create(t, "home")
	srv := sshtest.Start(t)
	srv.TrustUserCA(caKey(t, ca))
	h := host("web")
	if err := dial(t, e, srv, h, ""); err != nil {
		t.Fatalf("certificate refused: %v", err)
	}
	c := srv.LastCertificate()
	now := uint64(time.Now().Unix())
	if c.ValidPrincipals[0] != "deploy" || c.ValidAfter > now-4*60 || c.ValidBefore > now+10*60+5 || c.ValidBefore < now+9*60 {
		t.Fatalf("validity/principal: %+v (now %d)", c, now)
	}
	if string(c.SignatureKey.Marshal()) != string(caKey(t, ca).Marshal()) {
		t.Fatal("signed by another key")
	}
	// A principal override (AuthorizedPrincipalsFile setups).
	srv.SetPrincipals("deploy", "drool-admin")
	if err := dial(t, e, srv, h, ""); err == nil {
		t.Fatal("certificate for the user accepted although only drool-admin is allowed")
	}
	h.SshPrincipal = "drool-admin"
	if err := dial(t, e, srv, h, ""); err != nil {
		t.Fatalf("principal override refused: %v", err)
	}
}

func TestCertificateClockSkew(t *testing.T) {
	e := newEnv(t)
	ca := e.create(t, "home")
	srv := sshtest.Start(t)
	srv.TrustUserCA(caKey(t, ca))
	h := host("web")
	for _, c := range []struct {
		skew time.Duration
		ok   bool
	}{
		{-4 * time.Minute, true},  // host clock 4 min behind: within the backdating
		{9 * time.Minute, true},   // 9 min ahead: before expiry
		{-7 * time.Minute, false}, // 7 min behind: not yet valid for it
		{11 * time.Minute, false}, // 11 min ahead: already expired for it
	} {
		srv.SetClockSkew(c.skew)
		err := dial(t, e, srv, h, "")
		if (err == nil) != c.ok {
			t.Fatalf("skew %s: err %v", c.skew, err)
		}
	}
}

func TestCertificateFromUntrustedOrRetiredAuthority(t *testing.T) {
	e := newEnv(t)
	ca := e.create(t, "home")
	srv := sshtest.Start(t)
	other := sshtest.NewEd25519Signer(t)
	srv.TrustUserCA(other.PublicKey()) // trusts some other CA only
	if err := dial(t, e, srv, host("web"), ""); err == nil || !strings.Contains(err.Error(), "refused DROOL's SSH certificate") {
		t.Fatalf("untrusted CA: %v", err)
	}
	// Retiring needs a rotation first: the active one can't go.
	err := e.st.InRawTx(context.Background(), func(tx *sql.Tx) error {
		_, err := e.svc.RetireTx(context.Background(), tx, ca.ID)
		return err
	})
	if err == nil {
		t.Fatal("retired the active CA")
	}
}

func TestSerialsAreUniqueUnderConcurrency(t *testing.T) {
	e := newEnv(t)
	e.create(t, "home")
	var wg sync.WaitGroup
	serials := make(chan uint64, 64)
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, iss, err := e.svc.Issue(context.Background(), host(fmt.Sprint("h", i%4)), "")
			if err != nil {
				t.Error(err)
				return
			}
			serials <- iss.Serial
		}(i)
	}
	wg.Wait()
	close(serials)
	seen := map[uint64]bool{}
	for s := range serials {
		if seen[s] {
			t.Fatalf("serial %d issued twice", s)
		}
		seen[s] = true
	}
	var n int
	e.st.Reader.QueryRow(`SELECT count(*) FROM audit_events WHERE action = 'ssh.certificate.issue'`).Scan(&n)
	if len(seen) != 64 || n != 64 {
		t.Fatalf("%d serials, %d audit records", len(seen), n)
	}
	// Nothing but the sealed CA key is stored: no ephemeral keys.
	var dump strings.Builder
	rows, _ := e.st.Reader.Query(`SELECT details_json FROM audit_events`)
	for rows.Next() {
		var d string
		rows.Scan(&d)
		dump.WriteString(d)
	}
	rows.Close()
	if strings.Contains(dump.String(), "PRIVATE KEY") || strings.Contains(dump.String(), "ssh-ed25519 ") {
		t.Fatal("key material in the audit log")
	}
}

func TestJumpHostCertificateAllowsForwarding(t *testing.T) {
	e := newEnv(t)
	e.create(t, "home")
	h := host("jump")
	h.JumpOnly = 1
	signer, _, err := e.svc.Issue(context.Background(), h, "")
	if err != nil {
		t.Fatal(err)
	}
	cert := signer.PublicKey().(*ssh.Certificate)
	if _, ok := cert.Permissions.Extensions["permit-port-forwarding"]; !ok {
		t.Fatalf("jump host certificate: %+v", cert.Permissions)
	}
	plain, _, _ := e.svc.Issue(context.Background(), host("web"), "")
	if len(plain.PublicKey().(*ssh.Certificate).Permissions.Extensions) != 0 {
		t.Fatal("extensions on an ordinary host's certificate")
	}
}

// --- the trust script, against fake sshd/pgrep in a temporary root ---

// scriptEnv runs TrustScript with every path under dir and fake binaries
// first on PATH. The fake sshd's -T prints trustedusercakeys from the
// files (first value wins, like sshd); -t fails when sshd_config.d holds a
// file named "broken.conf".
type scriptEnv struct {
	dir, keys, conf, main string
	path                  string
}

func newScriptEnv(t *testing.T, mainConfig string) *scriptEnv {
	t.Helper()
	dir := t.TempDir()
	se := &scriptEnv{dir: dir, keys: filepath.Join(dir, "etc/ssh/drool_user_ca.pub"), conf: filepath.Join(dir, "etc/ssh/sshd_config.d/40-drool-user-ca.conf"),
		main: filepath.Join(dir, "etc/ssh/sshd_config")}
	os.MkdirAll(filepath.Join(dir, "etc/ssh/sshd_config.d"), 0o755)
	os.WriteFile(se.main, []byte(strings.ReplaceAll(mainConfig, "/etc/ssh", filepath.Join(dir, "etc/ssh"))), 0o644)
	bin := filepath.Join(dir, "bin")
	os.MkdirAll(bin, 0o755)
	sshd := `#!/bin/sh
D=` + filepath.Join(dir, "etc/ssh") + `
files() { for f in "$D"/sshd_config.d/*.conf; do [ -f "$f" ] && echo "$f"; done; echo "$D/sshd_config"; }
case "$1" in
-t) [ -f "$D/sshd_config.d/broken.conf" ] && { echo "line 1: Bad configuration option: bogus" >&2; exit 255; }; exit 0 ;;
-T) v=$(cat $(files) 2>/dev/null | awk 'tolower($1)=="trustedusercakeys"{print $2; exit}')
    echo "port 22"; echo "trustedusercakeys ${v:-none}"; echo "authorizedprincipalsfile none"; exit 0 ;;
esac
exit 1
`
	os.WriteFile(filepath.Join(bin, "sshd"), []byte(sshd), 0o755)
	// Never the workstation's systemd: "no unit is active", so the script
	// falls back to signalling the process below.
	os.WriteFile(filepath.Join(bin, "systemctl"), []byte("#!/bin/sh\nexit 1\n"), 0o755)
	// pgrep finds "the listener": a process the test owns.
	sleeper := exec.Command("sleep", "60")
	sleeper.Start()
	t.Cleanup(func() { sleeper.Process.Kill(); sleeper.Wait() })
	os.WriteFile(filepath.Join(bin, "pgrep"), []byte(fmt.Sprintf("#!/bin/sh\necho %d\n", sleeper.Process.Pid)), 0o755)
	se.path = bin + ":" + os.Getenv("PATH")
	return se
}

func (se *scriptEnv) run(t *testing.T, action string, keys ...string) (map[string]string, string, string) {
	t.Helper()
	script, err := TrustScript(action, keys)
	if err != nil {
		t.Fatal(err)
	}
	// Point every path at the temporary root.
	script = strings.ReplaceAll(script, "/etc/ssh", filepath.Join(se.dir, "etc/ssh"))
	script = strings.ReplaceAll(script, "/var/run/sshd.pid", filepath.Join(se.dir, "sshd.pid"))
	script = strings.ReplaceAll(script, "/run/sshd.pid", filepath.Join(se.dir, "sshd.pid"))
	if strings.Contains(script, " /etc/") || strings.Contains(script, "systemctl reload") && !strings.Contains(se.path, se.dir) {
		t.Fatal("the script would touch this machine")
	}
	cmd := exec.Command("sh", "-s")
	cmd.Stdin = strings.NewReader(script)
	cmd.Env = []string{"PATH=" + se.path}
	out, _ := cmd.CombinedOutput()
	vals, code, msg := parseOutput(out)
	return vals, code, msg
}

const debianConfig = "Include /etc/ssh/sshd_config.d/*.conf\nPasswordAuthentication no\n"

func TestTrustScript(t *testing.T) {
	const k1 = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOm1 drool-user-ca:one"
	const k2 = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOm2 drool-user-ca:two"
	t.Run("installs, is idempotent, lists every key", func(t *testing.T) {
		se := newScriptEnv(t, debianConfig)
		vals, code, msg := se.run(t, "install", k1)
		if code != "" || vals["changed"] != "1" {
			t.Fatalf("install: %s %s %v", code, msg, vals)
		}
		if b, _ := os.ReadFile(se.keys); string(b) != k1+"\n" {
			t.Fatalf("keys file: %q", b)
		}
		if b, _ := os.ReadFile(se.conf); !strings.Contains(string(b), "TrustedUserCAKeys "+se.keys) {
			t.Fatalf("conf: %q", b)
		}
		if vals, code, _ := se.run(t, "install", k1); code != "" || vals["changed"] != "0" {
			t.Fatalf("second install: %s %v", code, vals)
		}
		if vals, _, _ := se.run(t, "install", k1, k2); vals["changed"] != "1" {
			t.Fatal("adding a key didn't change anything")
		}
		if b, _ := os.ReadFile(se.keys); string(b) != k1+"\n"+k2+"\n" {
			t.Fatalf("keys file with two: %q", b)
		}
	})
	t.Run("refuses a host that trusts someone else's CA", func(t *testing.T) {
		se := newScriptEnv(t, debianConfig+"TrustedUserCAKeys /etc/ssh/corp_ca.pub\n")
		_, code, msg := se.run(t, "install", k1)
		if code != "foreign" || !strings.Contains(msg, "corp_ca.pub") {
			t.Fatalf("foreign: %s %s", code, msg)
		}
		if _, err := os.Stat(se.keys); err == nil {
			t.Fatal("files written anyway")
		}
	})
	t.Run("refuses without an Include of sshd_config.d", func(t *testing.T) {
		se := newScriptEnv(t, "PasswordAuthentication no\n")
		if _, code, _ := se.run(t, "install", k1); code != "noinclude" {
			t.Fatalf("code %s", code)
		}
	})
	t.Run("puts the previous files back when sshd rejects the result", func(t *testing.T) {
		se := newScriptEnv(t, debianConfig)
		se.run(t, "install", k1)
		os.WriteFile(filepath.Join(se.dir, "etc/ssh/sshd_config.d/broken.conf"), []byte("bogus\n"), 0o644)
		_, code, _ := se.run(t, "install", k1, k2)
		if code != "invalid" {
			t.Fatalf("code %s", code)
		}
		if b, _ := os.ReadFile(se.keys); string(b) != k1+"\n" {
			t.Fatalf("keys not restored: %q", b)
		}
		if _, err := os.Stat(se.keys + ".drool-prev"); err == nil {
			t.Fatal("backup left behind")
		}
	})
	t.Run("another file taking precedence is detected and undone", func(t *testing.T) {
		se := newScriptEnv(t, debianConfig)
		// A file sorted before DROOL's sets the option first (but to none
		// at the start, so the foreign check passes): simulate by writing it
		// after the check through the fake sshd reading 10-*.conf first.
		os.WriteFile(filepath.Join(se.dir, "etc/ssh/sshd_config.d/10-other.conf"), []byte("TrustedUserCAKeys none\n"), 0o644)
		_, code, _ := se.run(t, "install", k1)
		if code != "precedence" {
			t.Fatalf("code %s", code)
		}
		if _, err := os.Stat(se.conf); err == nil {
			t.Fatal("DROOL's file left in place")
		}
	})
	t.Run("removes", func(t *testing.T) {
		se := newScriptEnv(t, debianConfig)
		se.run(t, "install", k1)
		if vals, code, _ := se.run(t, "remove"); code != "" || vals["changed"] != "1" {
			t.Fatalf("remove: %s %v", code, vals)
		}
		for _, f := range []string{se.keys, se.conf} {
			if _, err := os.Stat(f); err == nil {
				t.Fatalf("%s still there", f)
			}
		}
		if vals, _, _ := se.run(t, "remove"); vals["changed"] != "0" {
			t.Fatal("second remove changed something")
		}
	})
	t.Run("rejects key lines that could break out of the here-document", func(t *testing.T) {
		if _, err := TrustScript("install", []string{"ssh-ed25519 AAAA x\nDROOL_CA_KEYS\nrm -rf /"}); err == nil {
			t.Fatal("accepted")
		}
	})
}
