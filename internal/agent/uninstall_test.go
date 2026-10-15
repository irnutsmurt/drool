package agent

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"drool/internal/agentcaps"
	"drool/internal/agentrm"
	"drool/internal/agentrpc"
	"drool/internal/agentrpc/pb"
)

// A Shepherd removes itself only when the request is meant for it (its
// identity, its controller epoch, not expired), starts its helper once for
// repeated requests, refuses work while it goes, and never starts the real
// helper unless it runs as root from the standard places (here it can't:
// these tests must never touch the machine's Shepherd paths or systemd).
func TestSelfUninstallRequest(t *testing.T) {
	full, _ := agentcaps.Preset(agentcaps.PresetFull)
	a := testAgent(t, full)
	var started []string
	a.opt.RemovalDir = t.TempDir()
	a.opt.StartRemoval = func(path string) (string, error) {
		started = append(started, path)
		return "test helper", nil
	}
	req := func(mod func(*pb.SelfUninstall)) *pb.SelfUninstall {
		m := &pb.SelfUninstall{RequestId: "rm_1", IdentityId: "aid_1", ControllerEpoch: "epoch-A",
			IssuedAtUnix: time.Now().Unix(), ExpiresAtUnix: time.Now().Add(time.Minute).Unix()}
		if mod != nil {
			mod(m)
		}
		return m
	}
	for name, c := range map[string]struct {
		mod  func(*pb.SelfUninstall)
		code string
	}{
		"no id":            {func(m *pb.SelfUninstall) { m.RequestId = "" }, "invalid"},
		"a strange id":     {func(m *pb.SelfUninstall) { m.RequestId = "rm 1; rm -rf /" }, "invalid"},
		"expired":          {func(m *pb.SelfUninstall) { m.ExpiresAtUnix = time.Now().Add(-time.Second).Unix() }, "expired"},
		"no expiry":        {func(m *pb.SelfUninstall) { m.ExpiresAtUnix = 0 }, "expired"},
		"from the future":  {func(m *pb.SelfUninstall) { m.IssuedAtUnix = time.Now().Add(time.Hour).Unix() }, "invalid"},
		"another identity": {func(m *pb.SelfUninstall) { m.IdentityId = "aid_2" }, "identity"},
		"another epoch":    {func(m *pb.SelfUninstall) { m.ControllerEpoch = "epoch-B" }, "epoch"},
	} {
		if r := a.selfUninstall(req(c.mod)); r.Accepted || r.Code != c.code || r.Error == "" {
			t.Errorf("%s: %+v", name, r)
		}
	}
	if len(started) != 0 || a.removing() {
		t.Fatal("a refused request started the helper")
	}
	// Accepted: the helper is written root-only and started once, with the
	// shared removal script.
	r := a.selfUninstall(req(nil))
	if !r.Accepted || r.Already || r.Helper != "test helper" || len(r.Removes) != len(agentrm.Items) || len(started) != 1 {
		t.Fatalf("accepted: %+v (%d started)", r, len(started))
	}
	b, err := os.ReadFile(started[0])
	if err != nil {
		t.Fatal(err)
	}
	fi, _ := os.Stat(started[0])
	dir := filepath.Dir(started[0])
	if fi.Mode().Perm() != 0o700 || !strings.HasPrefix(filepath.Base(dir), "drool-shepherd-removal.") ||
		!strings.HasPrefix(string(b), "MODE=self\nGO='"+dir+"/go'\nDIR='"+dir+"'\n") || !strings.HasSuffix(string(b), agentrm.Script) {
		t.Fatalf("the helper: %v %s\n%s", fi.Mode(), dir, b)
	}
	// The same request again (a lost answer), or another one: the removal
	// already started, nothing started twice.
	for _, id := range []string{"rm_1", "rm_2"} {
		if r := a.selfUninstall(req(func(m *pb.SelfUninstall) { m.RequestId = id })); !r.Accepted || !r.Already || len(started) != 1 {
			t.Fatalf("again (%s): %+v, %d started", id, r, len(started))
		}
	}
	// While it goes, no operation is admitted.
	if rej := a.admit(cmdEnv("op_during")); rej == nil || rej.Reason != agentrpc.RejectBusy || !strings.Contains(rej.Message, "removing itself") {
		t.Fatalf("an operation during the removal: %+v", rej)
	}

	// The helper can't start: refused, and nothing is left behind.
	b2 := testAgent(t, full)
	b2.opt.RemovalDir = t.TempDir()
	b2.opt.StartRemoval = func(string) (string, error) { return "", errors.New("systemd-run: not found") }
	if r := b2.selfUninstall(req(nil)); r.Accepted || r.Code != "start" || !strings.Contains(r.Error, "systemd-run: not found") {
		t.Fatalf("helper not started: %+v", r)
	}
	if left, _ := os.ReadDir(b2.opt.RemovalDir); len(left) != 0 || b2.removing() {
		t.Fatalf("a failed start left %d entries", len(left))
	}

	// Without a test starter, a Shepherd that isn't root in the standard
	// places refuses before it writes anything.
	c := testAgent(t, full)
	c.opt.RemovalDir = t.TempDir()
	if r := c.selfUninstall(req(nil)); r.Accepted || r.Code != "start" || !strings.Contains(r.Error, "can't remove itself") && !strings.Contains(r.Error, "standard places") {
		t.Fatalf("the real helper outside a root install: %+v", r)
	}
	if left, _ := os.ReadDir(c.opt.RemovalDir); len(left) != 0 {
		t.Fatal("the guard ran after writing the helper")
	}
	if _, err := c.startRemoval("/nonexistent"); err == nil {
		t.Fatal("startRemoval itself doesn't check")
	}
}

// The removal script is valid shell, removes exactly the Shepherd's items,
// keeps its parent alone only over SSH, and the self helper deletes only its
// own directory.
func TestRemovalScript(t *testing.T) {
	for _, mode := range []string{"check", "remove", "self"} {
		cmd := exec.Command("sh", "-n")
		cmd.Stdin = strings.NewReader("MODE=" + mode + "\nGO=/run/x/go\nDIR=/run/x\n" + agentrm.Script)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s: %v %s", mode, err, out)
		}
	}
	s := agentrm.Script
	for _, it := range agentrm.Items {
		if !strings.Contains(s, it.Path) && it.Path != "/etc/systemd/system/drool-agent.service" && it.Path != "/usr/libexec/drool-agent" {
			t.Errorf("the script doesn't handle %s", it.Path)
		}
	}
	for _, want := range []string{`[ "$MODE" != self ] && [ "$p" = "$PPID" ] && continue`,
		`/run/drool-shepherd-removal.*|/tmp/drool-shepherd-removal.*|/var/tmp/drool-shepherd-removal.*) rm -rf -- "$DIR"`,
		`while [ ! -e "$GO" ] && [ $n -lt 20 ]`, `trap '' HUP PIPE`} {
		if !strings.Contains(s, want) {
			t.Errorf("the script lacks %q", want)
		}
	}
}
