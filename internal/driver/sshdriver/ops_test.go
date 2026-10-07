package sshdriver

import (
	"bytes"
	"context"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"drool/internal/hostops"
	"drool/internal/model"
	"drool/internal/sshx"
	"drool/internal/sshx/sshtest"
)

type sink struct {
	mu     sync.Mutex
	out    bytes.Buffer
	phases []string
}

func (s *sink) Stdout() io.Writer { return s }
func (s *sink) Stderr() io.Writer { return s }
func (s *sink) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.out.Write(p)
}
func (s *sink) Phase(st string) { s.mu.Lock(); s.phases = append(s.phases, st); s.mu.Unlock() }

// fakeSystemd emulates systemctl for one host.
type fakeSystemd struct {
	mu        sync.Mutex
	units     map[string]map[string]string // unit -> property -> value
	failStart map[string]bool
	calls     []string
}

func (f *fakeSystemd) exec(cmd string, _ []byte) (string, string, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cmd = strings.TrimPrefix(cmd, "sudo -n -- ")
	f.calls = append(f.calls, cmd)
	unitOf := func() string {
		i := strings.LastIndex(cmd, "'")
		j := strings.LastIndex(cmd[:i], "'")
		return cmd[j+1 : i]
	}
	switch {
	case strings.HasPrefix(cmd, "systemctl show"):
		u, ok := f.units[unitOf()]
		if !ok {
			return "LoadState=not-found\nActiveState=inactive\nSubState=dead\nUnitFileState=\nCanReload=no\n", "", 0
		}
		var b strings.Builder
		for _, k := range []string{"LoadState", "ActiveState", "SubState", "UnitFileState", "CanReload"} {
			b.WriteString(k + "=" + u[k] + "\n")
		}
		return b.String(), "", 0
	case strings.HasPrefix(cmd, "systemctl --no-ask-password "):
		verb := strings.Fields(cmd)[2]
		name := unitOf()
		u := f.units[name]
		switch verb {
		case "start", "restart":
			if f.failStart[name] {
				u["ActiveState"], u["SubState"] = "failed", "failed"
				return "", "Job for " + name + " failed.\n", 1
			}
			u["ActiveState"], u["SubState"] = "active", "running"
		case "stop":
			u["ActiveState"], u["SubState"] = "inactive", "dead"
		case "reload":
		case "enable":
			u["UnitFileState"] = "enabled"
		case "disable":
			u["UnitFileState"] = "disabled"
		}
		return "", "", 0
	case strings.HasPrefix(cmd, "systemctl status"):
		return "× " + unitOf() + "\n  Active: failed\nOct 06 web: bind() to 0.0.0.0:80 failed (98: Address already in use)\n", "", 3
	}
	return "", "unexpected: " + cmd, 127
}

func unit(active, file string, reload bool) map[string]string {
	r := "no"
	if reload {
		r = "yes"
	}
	sub := map[string]string{"active": "running", "inactive": "dead"}[active]
	return map[string]string{"LoadState": "loaded", "ActiveState": active, "SubState": sub, "UnitFileState": file, "CanReload": r}
}

func setup(t *testing.T, exec sshtest.ExecFunc) (*Executor, *sshx.Target, *sshtest.Server) {
	t.Helper()
	hostKey := sshtest.NewEd25519Signer(t)
	srv := sshtest.Start(t, hostKey)
	client := sshtest.NewEd25519Signer(t)
	srv.Authorize(client.PublicKey())
	srv.SetExec(exec)
	h, p := srv.Host()
	pool := sshx.NewPool(&sshx.Dialer{DialTimeout: time.Second, HandshakeTimeout: 2 * time.Second})
	t.Cleanup(pool.Close)
	return &Executor{Pool: pool}, &sshx.Target{HostID: "h", Name: "h", Address: h, Port: p, User: "deploy",
		Signer: client, PinnedKey: hostKey.PublicKey()}, srv
}

func svcOp(name, state string, enabled *bool) model.Operation {
	op := model.Operation{Type: model.OpService, Service: &model.ServiceOp{Name: name, State: state, Enabled: enabled}}
	if err := op.Normalize(); err != nil {
		panic(err)
	}
	return op
}

func TestServiceOperations(t *testing.T) {
	sd := &fakeSystemd{units: map[string]map[string]string{
		"nginx.service":  unit("inactive", "disabled", true),
		"cron.service":   unit("active", "enabled", false),
		"masked.service": {"LoadState": "masked", "ActiveState": "inactive", "SubState": "dead", "UnitFileState": "masked", "CanReload": "no"},
		"web.service":    unit("inactive", "enabled", false),
	}, failStart: map[string]bool{"web.service": true}}
	x, tg, _ := setup(t, sd.exec)
	b := Become{Method: "sudo"}
	ctx := context.Background()
	yes := true

	// Idempotent start + enable: acts once, then reports no change.
	for i, wantChanged := range []bool{true, false} {
		s := &sink{}
		res, err := x.Execute(ctx, tg, b, svcOp("nginx", "running", &yes), s, func() {})
		if err != nil || *res.Changed != wantChanged {
			t.Fatalf("run %d: changed=%v err=%v out=%s", i, res != nil && *res.Changed, err, s.out.String())
		}
	}
	if sd.units["nginx.service"]["UnitFileState"] != "enabled" || sd.units["nginx.service"]["ActiveState"] != "active" {
		t.Fatalf("nginx state %v", sd.units["nginx.service"])
	}
	// Restart always acts.
	res, err := x.Execute(ctx, tg, b, svcOp("cron", "restarted", nil), &sink{}, func() {})
	if err != nil || !*res.Changed {
		t.Fatalf("restart: %v", err)
	}
	// Useful errors instead of shell output.
	cases := []struct {
		op   model.Operation
		want string
	}{
		{svcOp("nosuch", "running", nil), "not found"},
		{svcOp("masked", "running", nil), "masked"},
		{svcOp("cron", "reloaded", nil), "does not support reload"},
		{svcOp("web", "running", nil), "failed to start"},
	}
	for _, c := range cases {
		s := &sink{}
		_, err := x.Execute(ctx, tg, b, c.op, s, func() {})
		if model.ClassOf(err) != model.ClassService || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v", c.op.Service.Name, err)
		}
		if c.want == "failed to start" && !strings.Contains(s.out.String(), "Address already in use") {
			t.Errorf("journal lines not attached: %s", s.out.String())
		}
	}
}

func TestServiceNeedsSystemdAndRoot(t *testing.T) {
	x, tg, _ := setup(t, func(string, []byte) (string, string, int) {
		return "", "System has not been booted with systemd as init system (PID 1). Can't operate.\nFailed to connect to bus: Host is down\n", 1
	})
	_, err := x.Execute(context.Background(), tg, Become{Method: "sudo"}, svcOp("ssh", "running", nil), &sink{}, func() {})
	if model.ClassOf(err) != model.ClassConfiguration || !strings.Contains(err.Error(), "systemd is not running") {
		t.Fatalf("no systemd: %v", err)
	}
	_, err = x.Execute(context.Background(), tg, Become{Method: "none"}, svcOp("ssh", "running", nil), &sink{}, func() {})
	if model.ClassOf(err) != model.ClassConfiguration || !strings.Contains(err.Error(), "needs root") {
		t.Fatalf("no root: %v", err)
	}
}

// rebootHost emulates boot_id and a reboot trigger on an sshtest server.
type rebootHost struct {
	mu      sync.Mutex
	bootID  string
	trigger func()
}

func (r *rebootHost) exec(cmd string, _ []byte) (string, string, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	switch {
	case cmd == "cat /proc/sys/kernel/random/boot_id":
		return r.bootID + "\n", "", 0
	case strings.Contains(cmd, "systemd-run"):
		if r.trigger != nil {
			go r.trigger()
		}
		return "", "", 0
	}
	return "", "unexpected: " + cmd, 127
}

func rebootOp(timeout int) model.Operation {
	op := model.Operation{Type: model.OpReboot, Reboot: &model.RebootOp{TimeoutS: timeout}}
	_ = op.Normalize()
	return op
}

func TestRebootWaitsForNewBootID(t *testing.T) {
	rebootPollStart, rebootPollMax = 100*time.Millisecond, 200*time.Millisecond
	rh := &rebootHost{bootID: "11111111-1111-1111-1111-111111111111"}
	x, tg, srv := setup(t, rh.exec)
	rh.trigger = func() {
		time.Sleep(300 * time.Millisecond)
		srv.Restart(700*time.Millisecond, func() {
			rh.mu.Lock()
			rh.bootID = "22222222-2222-2222-2222-222222222222"
			rh.mu.Unlock()
		})
	}
	s := &sink{}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	res, err := x.Execute(ctx, tg, Become{Method: "sudo"}, rebootOp(10), s, func() {})
	if err != nil {
		t.Fatalf("reboot: %v\n%s", err, s.out.String())
	}
	rb := res.Data["reboot"].(*hostops.RebootResult)
	if rb.BootIDAfter != "22222222-2222-2222-2222-222222222222" || rb.DownSeconds <= 0 {
		t.Fatalf("result %+v", rb)
	}
	if strings.Join(s.phases, ",") != "rebooting,waiting_host,verifying" {
		t.Fatalf("phases %v", s.phases)
	}
}

func TestRebootNeverHappens(t *testing.T) {
	rebootPollStart, rebootPollMax = 100*time.Millisecond, 200*time.Millisecond
	rh := &rebootHost{bootID: "11111111-1111-1111-1111-111111111111"} // trigger does nothing
	x, tg, _ := setup(t, rh.exec)
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	res, err := x.Execute(ctx, tg, Become{Method: "sudo"}, rebootOp(10), &sink{}, func() {})
	if model.ClassOf(err) != model.ClassTimeout || !strings.Contains(err.Error(), "never went away") {
		t.Fatalf("expected timeout with old boot id, got %v", err)
	}
	if res == nil || res.Changed == nil || *res.Changed {
		t.Fatalf("a reboot that never happened must not be reported as a change: %+v", res)
	}
}

func TestRebootComesBackWithDifferentKey(t *testing.T) {
	rebootPollStart, rebootPollMax = 100*time.Millisecond, 200*time.Millisecond
	rh := &rebootHost{bootID: "11111111-1111-1111-1111-111111111111"}
	x, tg, srv := setup(t, rh.exec)
	rh.trigger = func() {
		srv.Restart(300*time.Millisecond, func() { srv.SetHostKeys(sshtest.NewEd25519Signer(t)) })
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := x.Execute(ctx, tg, Become{Method: "sudo"}, rebootOp(10), &sink{}, func() {})
	if model.ClassOf(err) != model.ClassHostKey {
		t.Fatalf("expected HostKeyError after reboot with a new key, got %v", err)
	}
}
