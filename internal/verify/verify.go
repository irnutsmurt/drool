// Package verify runs the controller-side workflow steps (M13): wait,
// verify the host is reachable, verify services are running, verify the
// host's health. They run inside a host's ordinary job, after its other
// steps, under the job's host lock; nothing runs on the host for them
// beyond the monitoring collection DROOL already does (so no shell, no
// extra privileges, and never a change to an agent's capability ceiling).
//
// Services and health come from M11 monitoring data, collected fresh for
// the check: an agent is asked to report now; an SSH host is polled now
// (the job already holds the host, so the poller can't, and the check
// can't wait for its next pass). Data from before the check began never
// passes it.
package verify

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"drool/internal/driver"
	"drool/internal/model"
	"drool/internal/monitor/monsvc"
	"drool/internal/store"
	"drool/internal/store/db"
)

// Reachability is how a host answers right now.
type Reachability struct {
	OK     bool
	Detail string
}

// Deps are the executor's collaborators.
type Deps struct {
	Store   *store.Store
	Monitor *monsvc.Service
	// AgentConnected: the host is managed by its agent and the agent is
	// connected now (mgmt.Router.Decide).
	AgentConnected func(ctx context.Context, h db.Host) bool
	// Probe runs an SSH connection test (inventory.Probe); nil when the
	// controller has no SSH path to the host.
	Probe func(ctx context.Context, hostID string) Reachability
	// PollSSH collects one monitoring poll over SSH now.
	PollSSH func(ctx context.Context, h db.Host) error
	// Interval between checks (default 10 s); Fresh how long to wait for
	// an agent's report (default 15 s).
	Interval time.Duration
	Fresh    time.Duration
	Log      *slog.Logger
}

// Executor implements engine.LocalExecutor.
type Executor struct{ d Deps }

// New creates the executor.
func New(d Deps) *Executor {
	if d.Interval <= 0 {
		d.Interval = 10 * time.Second
	}
	if d.Fresh <= 0 {
		d.Fresh = 15 * time.Second
	}
	if d.Log == nil {
		d.Log = slog.Default()
	}
	return &Executor{d: d}
}

func note(out driver.OutputSink, format string, args ...any) {
	fmt.Fprintf(out.Stdout(), format+"\n", args...)
}

func zero() *int { z := 0; return &z }

func fail(msg, hint string) error {
	return &model.OpError{Class: model.ClassVerification, Message: msg, Hint: hint}
}

// Execute runs one controller-side step for a host.
func (x *Executor) Execute(ctx context.Context, hostID string, op model.Operation, out driver.OutputSink) (*driver.OpResult, error) {
	switch op.Type {
	case model.OpWait:
		note(out, "waiting %s", (time.Duration(op.Wait.Seconds) * time.Second).String())
		select {
		case <-time.After(time.Duration(op.Wait.Seconds) * time.Second):
			return &driver.OpResult{ExitCode: zero()}, nil
		case <-ctx.Done():
			return nil, &model.OpError{Class: model.ClassCancelled, Message: "cancelled while waiting"}
		}
	case model.OpVerifyHost:
		return x.verifyHost(ctx, hostID, op.VerifyHost, out)
	case model.OpVerifyServices:
		return x.verifyServices(ctx, hostID, op.VerifyServices, out)
	case model.OpVerifyHealth:
		return x.verifyHealth(ctx, hostID, op.VerifyHealth, out)
	}
	return nil, &model.OpError{Class: model.ClassValidation, Message: "not a controller-side step: " + op.Type}
}

// sleep waits for the next check; false when the deadline passed or the
// job was cancelled.
func (x *Executor) sleep(ctx context.Context, deadline time.Time) bool {
	d := min(x.d.Interval, time.Until(deadline))
	if d <= 0 {
		return false
	}
	select {
	case <-time.After(d):
		return true
	case <-ctx.Done():
		return false
	}
}

func (x *Executor) host(ctx context.Context, id string) (db.Host, error) {
	h, err := x.d.Store.R.GetHost(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return h, &model.OpError{Class: model.ClassConfiguration, Message: "the host no longer exists"}
	}
	return h, err
}

func cancelled(ctx context.Context) error {
	if ctx.Err() != nil {
		return &model.OpError{Class: model.ClassCancelled, Message: "cancelled"}
	}
	return nil
}

func (x *Executor) verifyHost(ctx context.Context, hostID string, op *model.VerifyHostOp, out driver.OutputSink) (*driver.OpResult, error) {
	deadline := time.Now().Add(time.Duration(op.TimeoutS) * time.Second)
	last := ""
	for {
		h, err := x.host(ctx, hostID)
		if err != nil {
			return nil, err
		}
		r := x.reach(ctx, h)
		if r.OK {
			note(out, "the host is reachable: %s", r.Detail)
			return &driver.OpResult{ExitCode: zero(), Data: map[string]any{"reachable": true, "detail": r.Detail}}, nil
		}
		if r.Detail != last {
			note(out, "not reachable yet: %s", r.Detail)
			last = r.Detail
		}
		if !x.sleep(ctx, deadline) {
			if err := cancelled(ctx); err != nil {
				return nil, err
			}
			return nil, fail(fmt.Sprintf("the host did not answer within %s: %s", time.Duration(op.TimeoutS)*time.Second, r.Detail),
				"Check that the host is up and on the network (and, for agent hosts, that drool-agent is running).")
		}
	}
}

// reach checks the host's management path: its agent when it is managed
// by one (an SSH fallback counts when configured), SSH otherwise.
func (x *Executor) reach(ctx context.Context, h db.Host) Reachability {
	if h.ManagementMethod == "agent" {
		if x.d.AgentConnected != nil && x.d.AgentConnected(ctx, h) {
			return Reachability{OK: true, Detail: "the agent is connected"}
		}
		if h.ManagementFallback == "ssh" && h.SshConfigured != 0 && x.d.Probe != nil {
			if r := x.d.Probe(ctx, h.ID); r.OK {
				return Reachability{OK: true, Detail: "over SSH (the agent is not connected yet)"}
			}
		}
		return Reachability{Detail: "the agent is not connected"}
	}
	if x.d.Probe == nil {
		return Reachability{Detail: "no SSH path to the host"}
	}
	return x.d.Probe(ctx, h.ID)
}

// stamp is when the host's monitoring data of a kind was last stored. It
// is only compared for change (an agent's state carries the agent's own
// clock, so it is never compared with the controller's).
func (x *Executor) stamp(ctx context.Context, hostID, kind string) int64 {
	col := "updated_at"
	if kind == "services" {
		col = "services_at"
	}
	var at int64
	x.d.Store.Reader.QueryRowContext(ctx, `SELECT `+col+` FROM mon_current WHERE host_id = ?`, hostID).Scan(&at)
	return at
}

// fresh asks for new monitoring data and reports whether any arrived since
// the step began (base: the stamp then). A check never passes on data
// collected before it began: right after a reboot that data describes the
// host as it was.
func (x *Executor) fresh(ctx context.Context, h db.Host, kind string, base int64, out driver.OutputSink) bool {
	if x.d.AgentConnected != nil && x.d.AgentConnected(ctx, h) {
		// Best effort: the agent's regular reports count as well.
		x.d.Monitor.RequestRefresh(ctx, h.ID, kind)
		end := time.Now().Add(x.d.Fresh)
		for {
			if x.stamp(ctx, h.ID, kind) != base {
				return true
			}
			if !time.Now().Before(end) {
				return false
			}
			select {
			case <-time.After(min(250*time.Millisecond, x.d.Fresh)):
			case <-ctx.Done():
				return false
			}
		}
	}
	if x.d.PollSSH == nil || h.SshConfigured == 0 {
		return x.stamp(ctx, h.ID, kind) != base
	}
	if err := x.d.PollSSH(ctx, h); err != nil {
		note(out, "collecting monitoring data over SSH failed: %v", err)
		return false
	}
	return true
}

const staleHint = "DROOL doesn't pass a check on data collected before the check began. Check that the host's agent is connected (or SSH works) and that monitoring collects from it."

func (x *Executor) monitoringOn(ctx context.Context) error {
	if x.d.Monitor == nil || !x.d.Monitor.Settings(ctx).Enabled {
		return &model.OpError{Class: model.ClassConfiguration, Message: "monitoring is turned off (Settings → Monitoring); service and health verification use monitoring data"}
	}
	return nil
}

func (x *Executor) verifyServices(ctx context.Context, hostID string, op *model.VerifyServicesOp, out driver.OutputSink) (*driver.OpResult, error) {
	if err := x.monitoringOn(ctx); err != nil {
		return nil, err
	}
	deadline := time.Now().Add(time.Duration(op.TimeoutS) * time.Second)
	base := x.stamp(ctx, hostID, "services")
	last := ""
	for {
		h, err := x.host(ctx, hostID)
		if err != nil {
			return nil, err
		}
		if !x.fresh(ctx, h, "services", base, out) {
			if msg := "no fresh monitoring data from the host yet"; msg != last {
				note(out, "waiting: %s", msg)
				last = msg
			}
			if !x.sleep(ctx, deadline) {
				if err := cancelled(ctx); err != nil {
					return nil, err
				}
				return nil, fail(fmt.Sprintf("no fresh monitoring data from the host within %s, so its services couldn't be checked", time.Duration(op.TimeoutS)*time.Second), staleHint)
			}
			continue
		}
		rows, err := x.d.Monitor.Services(ctx, hostID)
		if err != nil {
			return nil, err
		}
		byName := map[string]monsvc.ServiceRow{}
		for _, r := range rows {
			byName[r.Name] = r
		}
		var bad []string
		states := map[string]string{}
		for _, name := range op.Services {
			r, ok := byName[name]
			switch {
			case !ok:
				bad = append(bad, name+" is not installed (systemd doesn't know it)")
				states[name] = "not found"
			case !r.Known:
				bad = append(bad, name+": state unknown (no fresh data)")
				states[name] = "unknown"
			case r.Active != "active":
				bad = append(bad, fmt.Sprintf("%s is %s/%s", name, r.Active, r.Sub))
				states[name] = r.Active + "/" + r.Sub
			default:
				states[name] = r.Active + "/" + r.Sub
			}
		}
		if len(bad) == 0 {
			note(out, "all %d service(s) are running: %s", len(op.Services), strings.Join(op.Services, ", "))
			return &driver.OpResult{ExitCode: zero(), Data: map[string]any{"services": states}}, nil
		}
		if msg := strings.Join(bad, "; "); msg != last {
			note(out, "waiting: %s", msg)
			last = msg
		}
		if !x.sleep(ctx, deadline) {
			if err := cancelled(ctx); err != nil {
				return nil, err
			}
			return &driver.OpResult{Data: map[string]any{"services": states}}, fail(fmt.Sprintf("after %s: %s", time.Duration(op.TimeoutS)*time.Second, strings.Join(bad, "; ")),
				"Look at the service's state and logs on the host's Services tab.")
		}
	}
}

func (x *Executor) verifyHealth(ctx context.Context, hostID string, op *model.VerifyHealthOp, out driver.OutputSink) (*driver.OpResult, error) {
	if err := x.monitoringOn(ctx); err != nil {
		return nil, err
	}
	deadline := time.Now().Add(time.Duration(op.TimeoutS) * time.Second)
	what := "Critical"
	if op.Policy == model.HealthNoWarning {
		what = "Warning or Critical"
	}
	base := x.stamp(ctx, hostID, "monitor_state")
	last := ""
	for {
		h, err := x.host(ctx, hostID)
		if err != nil {
			return nil, err
		}
		if !x.fresh(ctx, h, "monitor_state", base, out) {
			if msg := "no fresh monitoring data from the host yet"; msg != last {
				note(out, "waiting: %s", msg)
				last = msg
			}
			if !x.sleep(ctx, deadline) {
				if err := cancelled(ctx); err != nil {
					return nil, err
				}
				return nil, fail(fmt.Sprintf("no fresh monitoring data from the host within %s, so its health couldn't be checked", time.Duration(op.TimeoutS)*time.Second), staleHint)
			}
			continue
		}
		issues, err := x.d.Monitor.Issues(ctx, monsvc.IssueFilter{HostID: hostID, State: "active", Limit: 200})
		if err != nil {
			return nil, err
		}
		var bad []string
		for _, i := range issues {
			if i.Severity == "critical" || i.Severity == "warning" && op.Policy == model.HealthNoWarning {
				bad = append(bad, fmt.Sprintf("%s (%s)", i.Title, i.Severity))
			}
		}
		if len(bad) == 0 {
			note(out, "no active %s health issues", what)
			return &driver.OpResult{ExitCode: zero(), Data: map[string]any{"issues": 0}}, nil
		}
		if msg := strings.Join(bad, "; "); msg != last {
			note(out, "waiting: %s", msg)
			last = msg
		}
		if !x.sleep(ctx, deadline) {
			if err := cancelled(ctx); err != nil {
				return nil, err
			}
			return &driver.OpResult{Data: map[string]any{"issues": len(bad)}}, fail(fmt.Sprintf("%d active %s health issue(s) after %s: %s", len(bad), what,
				time.Duration(op.TimeoutS)*time.Second, strings.Join(bad, "; ")), "See the host's monitoring page.")
		}
	}
}
