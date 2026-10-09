package verify

import (
	"bytes"
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"drool/internal/logging"
	"drool/internal/model"
	"drool/internal/monitor/monsvc"
	"drool/internal/store"
	"drool/internal/store/db"
)

// The checks run against real monitoring tables (services, issues) with
// fake reachability and collection: nothing reaches a host.

type sink struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *sink) Write(p []byte) (int, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.buf.Write(p) }
func (s *sink) Stdout() io.Writer           { return s }
func (s *sink) Stderr() io.Writer           { return s }
func (s *sink) String() string              { s.mu.Lock(); defer s.mu.Unlock(); return s.buf.String() }

type env struct {
	t       *testing.T
	st      *store.Store
	mon     *monsvc.Service
	agentOn atomic.Bool
	reach   atomic.Bool
	probes  atomic.Int32
	upAfter int32 // > 0: the host answers after this many refused probes
	polls   atomic.Int32
	// onPoll runs inside each SSH poll (after the poll "collected").
	onPoll func()
	x      *Executor
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
	for _, q := range []string{
		`INSERT INTO hosts (id, name, connect_address, ssh_user, status, created_at, updated_at) VALUES ('h1', 'web1', '10.0.0.1', 'x', 'ready', 0, 0)`,
		`INSERT INTO hosts (id, name, connect_address, ssh_user, status, management_method, created_at, updated_at) VALUES ('h2', 'nas', '10.0.0.2', 'x', 'ready', 'agent', 0, 0)`,
		`INSERT INTO mon_current (host_id, source, updated_at, state_json, services_supported, services_at) VALUES ('h1', 'ssh', 1000, '{}', 1, 1000)`,
		`INSERT INTO mon_current (host_id, source, updated_at, state_json, services_supported, services_at) VALUES ('h2', 'agent', 1000, '{}', 1, 1000)`,
	} {
		if _, err := st.Writer.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	logs := logging.Discard()
	e := &env{t: t, st: st, mon: monsvc.New(monsvc.Deps{Store: st, Log: logs.App})}
	e.x = New(Deps{Store: st, Monitor: e.mon, Log: logs.App, Interval: 10 * time.Millisecond, Fresh: 50 * time.Millisecond,
		AgentConnected: func(_ context.Context, h db.Host) bool { return h.ManagementMethod == "agent" && e.agentOn.Load() },
		Probe: func(context.Context, string) Reachability {
			if n := e.probes.Add(1); e.upAfter > 0 && n > e.upAfter {
				e.reach.Store(true)
			}
			if e.reach.Load() {
				return Reachability{OK: true, Detail: "SSH connection test passed"}
			}
			return Reachability{Detail: "connection refused"}
		},
		PollSSH: func(_ context.Context, h db.Host) error {
			e.polls.Add(1)
			e.collected(h.ID)
			if e.onPoll != nil {
				e.onPoll()
			}
			return nil
		},
	})
	return e
}

// collected marks new monitoring data for a host (what an ingest does).
func (e *env) collected(host string) {
	e.st.Writer.Exec(`UPDATE mon_current SET updated_at = updated_at + 1, services_at = services_at + 1 WHERE host_id = ?`, host)
}

func (e *env) unit(host, name, active, sub string, known bool) {
	k := 0
	if known {
		k = 1
	}
	if _, err := e.st.Writer.Exec(`INSERT INTO mon_services (host_id, unit, active_state, sub_state, known, changed_at) VALUES (?, ?, ?, ?, ?, 0)
		ON CONFLICT(host_id, unit) DO UPDATE SET active_state = excluded.active_state, sub_state = excluded.sub_state, known = excluded.known`,
		host, name, active, sub, k); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) issue(host, id, sev, state string) {
	if _, err := e.st.Writer.Exec(`INSERT INTO mon_issues (id, host_id, check_id, subject, severity, state, title, started_at, updated_at) VALUES (?, ?, 'disk', ?, ?, ?, ?, 0, 0)`,
		id, host, id, sev, state, "Disk "+id+" almost full"); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) run(host string, op model.Operation) (*sink, error) {
	e.t.Helper()
	if err := op.Normalize(); err != nil {
		e.t.Fatal(err)
	}
	out := &sink{}
	_, err := e.x.Execute(context.Background(), host, op, out)
	return out, err
}

func class(err error) model.ErrorClass {
	var oe *model.OpError
	if errors.As(err, &oe) {
		return oe.Class
	}
	return ""
}

func TestWait(t *testing.T) {
	e := newEnv(t)
	start := time.Now()
	if _, err := e.run("h1", model.Operation{Type: model.OpWait, Wait: &model.WaitOp{Seconds: 1}}); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d < time.Second {
		t.Fatalf("waited %s", d)
	}
	// Cancelling the job ends the wait at once.
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(20*time.Millisecond, cancel)
	start = time.Now()
	_, err := e.x.Execute(ctx, "h1", model.Operation{Type: model.OpWait, Wait: &model.WaitOp{Seconds: 3600}}, &sink{})
	if class(err) != model.ClassCancelled || time.Since(start) > 5*time.Second {
		t.Fatalf("cancel: %v after %s", err, time.Since(start))
	}
}

func TestVerifyHost(t *testing.T) {
	op := model.Operation{Type: model.OpVerifyHost, VerifyHost: &model.VerifyHostOp{TimeoutS: 1}}
	t.Run("ssh host comes back", func(t *testing.T) {
		e := newEnv(t)
		e.upAfter = 2
		out, err := e.run("h1", op)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), "not reachable yet: connection refused") || !strings.Contains(out.String(), "the host is reachable") {
			t.Fatalf("output: %q", out)
		}
	})
	t.Run("ssh host never answers", func(t *testing.T) {
		e := newEnv(t)
		_, err := e.run("h1", op)
		if class(err) != model.ClassVerification || !strings.Contains(err.Error(), "did not answer within 1s") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("agent host", func(t *testing.T) {
		e := newEnv(t)
		e.reach.Store(true) // SSH would answer, but the host is managed by its agent
		if _, err := e.run("h2", op); class(err) != model.ClassVerification || !strings.Contains(err.Error(), "agent is not connected") {
			t.Fatalf("agent down: %v", err)
		}
		e.agentOn.Store(true)
		if _, err := e.run("h2", op); err != nil {
			t.Fatalf("agent up: %v", err)
		}
	})
	t.Run("agent host with SSH fallback", func(t *testing.T) {
		e := newEnv(t)
		e.st.Writer.Exec(`UPDATE hosts SET management_fallback = 'ssh', ssh_configured = 1 WHERE id = 'h2'`)
		e.reach.Store(true)
		out, err := e.run("h2", op)
		if err != nil || !strings.Contains(out.String(), "over SSH") {
			t.Fatalf("fallback: %v %q", err, out)
		}
	})
	t.Run("deleted host", func(t *testing.T) {
		e := newEnv(t)
		e.st.Writer.Exec(`DELETE FROM mon_current WHERE host_id = 'h1'`)
		e.st.Writer.Exec(`DELETE FROM hosts WHERE id = 'h1'`)
		if _, err := e.run("h1", op); class(err) != model.ClassConfiguration {
			t.Fatalf("got %v", err)
		}
	})
}

func TestVerifyServices(t *testing.T) {
	op := func(names ...string) model.Operation {
		return model.Operation{Type: model.OpVerifyServices, VerifyServices: &model.VerifyServicesOp{Services: names, TimeoutS: 1}}
	}
	t.Run("running, failed, missing, unknown", func(t *testing.T) {
		e := newEnv(t)
		e.st.Writer.Exec(`UPDATE hosts SET ssh_configured = 1 WHERE id = 'h1'`)
		e.unit("h1", "ssh.service", "active", "running", true)
		e.unit("h1", "jellyfin.service", "failed", "failed", true)
		e.unit("h1", "nfs.service", "active", "running", false)
		if _, err := e.run("h1", op("ssh")); err != nil {
			t.Fatalf("ssh: %v", err)
		}
		if e.polls.Load() == 0 {
			t.Fatal("services were checked without collecting fresh data")
		}
		_, err := e.run("h1", op("ssh", "jellyfin", "plex", "nfs"))
		if class(err) != model.ClassVerification {
			t.Fatalf("got %v", err)
		}
		for _, want := range []string{"jellyfin.service is failed/failed", "plex.service is not installed", "nfs.service: state unknown"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("missing %q in %v", want, err)
			}
		}
	})
	t.Run("a service that starts in time passes", func(t *testing.T) {
		e := newEnv(t)
		e.st.Writer.Exec(`UPDATE hosts SET ssh_configured = 1 WHERE id = 'h1'`)
		e.unit("h1", "jellyfin.service", "activating", "start", true)
		var n atomic.Int32
		e.onPoll = func() {
			if n.Add(1) == 3 {
				e.unit("h1", "jellyfin.service", "active", "running", true)
			}
		}
		out, err := e.run("h1", op("jellyfin"))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), "waiting: jellyfin.service is activating/start") {
			t.Fatalf("output: %q", out)
		}
	})
	t.Run("old data never passes", func(t *testing.T) {
		// An agent host whose agent doesn't report: the services DROOL
		// saw before the check (say, before a reboot) look fine, but the
		// check can't confirm them now.
		e := newEnv(t)
		e.agentOn.Store(true)
		e.unit("h2", "ssh.service", "active", "running", true)
		out, err := e.run("h2", op("ssh"))
		if class(err) != model.ClassVerification || !strings.Contains(err.Error(), "no fresh monitoring data") {
			t.Fatalf("got %v (%q)", err, out)
		}
		// Its next report arrives: now it passes.
		time.AfterFunc(30*time.Millisecond, func() { e.collected("h2") })
		if _, err := e.run("h2", op("ssh")); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("monitoring off", func(t *testing.T) {
		e := newEnv(t)
		s := monsvc.DefaultSettings()
		s.Enabled = false
		if err := monsvc.SaveSettingsTx(context.Background(), e.st.W, s, "", 0); err != nil {
			t.Fatal(err)
		}
		if _, err := e.run("h1", op("ssh")); class(err) != model.ClassConfiguration || !strings.Contains(err.Error(), "monitoring is turned off") {
			t.Fatalf("got %v", err)
		}
	})
}

func TestVerifyHealth(t *testing.T) {
	op := func(policy string) model.Operation {
		return model.Operation{Type: model.OpVerifyHealth, VerifyHealth: &model.VerifyHealthOp{Policy: policy, TimeoutS: 1}}
	}
	e := newEnv(t)
	e.st.Writer.Exec(`UPDATE hosts SET ssh_configured = 1 WHERE id = 'h1'`)
	e.issue("h1", "old", "critical", "resolved")
	e.issue("h1", "warn", "warning", "active")
	e.issue("h2", "other", "critical", "active") // another host's issue
	if _, err := e.run("h1", op(model.HealthNoCritical)); err != nil {
		t.Fatalf("no critical: %v", err)
	}
	_, err := e.run("h1", op(model.HealthNoWarning))
	if class(err) != model.ClassVerification || !strings.Contains(err.Error(), "1 active Warning or Critical health issue(s)") {
		t.Fatalf("no warning: %v", err)
	}
	// A critical issue that clears during the check.
	e.issue("h1", "crit", "critical", "active")
	var n atomic.Int32
	e.onPoll = func() {
		if n.Add(1) == 3 {
			e.st.Writer.Exec(`UPDATE mon_issues SET state = 'resolved' WHERE id = 'crit'`)
		}
	}
	out, err := e.run("h1", op(model.HealthNoCritical))
	if err != nil || !strings.Contains(out.String(), "waiting: Disk crit almost full (critical)") {
		t.Fatalf("clearing: %v %q", err, out)
	}
}
