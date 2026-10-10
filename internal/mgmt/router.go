// Package mgmt chooses a host's management path (DESIGN §8,
// ARCHITECTURE-ALPHA §23.5): the agent when it is preferred and healthy,
// SSH otherwise, and SSH as the fallback only when that is safe:
//
//   - before an operation was dispatched to the agent (it can't have run);
//   - never after the agent accepted it (the outcome is then "unknown" and
//     resolved by asking the agent, never by running it again over SSH);
//   - never when the agent refused it because of its capability ceiling
//     (that would make the host owner's ceiling meaningless);
//   - never for a quarantined (cloned) or revoked agent identity: an
//     administrator decides first.
package mgmt

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"drool/internal/agentsvc"
	"drool/internal/driver"
	"drool/internal/driver/sshdriver"
	"drool/internal/engine"
	"drool/internal/files"
	"drool/internal/firewall/fwsvc"
	"drool/internal/hostops"
	"drool/internal/inventory"
	"drool/internal/model"
	"drool/internal/store"
	"drool/internal/store/db"
)

// Router implements engine.Executor, files.Opener and the API's simulator
// over both management paths.
type Router struct {
	St     *store.Store
	Inv    *inventory.Service
	SSH    *sshdriver.Executor
	Agents *agentsvc.Service // nil: agent endpoint disabled
	// Firewall runs firewall_apply steps and blocks reboots and network
	// restarts while a transaction is pending (DESIGN §17.3).
	Firewall *fwsvc.Service
}

// networkUnits are services whose restart can drop or rebuild networking
// or firewall state: refused while a firewall transaction is pending.
var networkUnits = map[string]bool{"networking": true, "network": true, "networkmanager": true, "systemd-networkd": true,
	"systemd-resolved": true, "nftables": true, "firewalld": true, "ufw": true, "iptables": true, "ip6tables": true,
	"netfilter-persistent": true, "wpa_supplicant": true, "drool-firewall": true, "docker": true}

// DisruptsFirewall reports whether op is refused while a firewall
// transaction is pending: reboots, system updates, and network service actions.
func DisruptsFirewall(op model.Operation) bool {
	switch op.Type {
	case model.OpReboot, model.OpSystemUpdate:
		return true
	case model.OpService:
		return networkUnits[strings.ToLower(strings.TrimSuffix(op.Service.Name, ".service"))]
	}
	return false
}

// FirewallDecision adapts Decide for the firewall service.
func (r *Router) FirewallDecision(ctx context.Context, h db.Host) fwsvc.Decision {
	d := r.Decide(ctx, h)
	return fwsvc.Decision{UseAgent: d.UseAgent, SSHAllowed: d.SSHAllowed, Blocked: d.Blocked, Reason: d.Reason}
}

// Decision is the management path for a host right now.
type Decision struct {
	Preferred string // ssh | agent
	Fallback  string // none | ssh
	// UseAgent: the agent is preferred and usable.
	UseAgent bool
	// SSHAllowed: SSH may be used (preferred, or as fallback right now).
	SSHAllowed bool
	// AgentState explains the agent side: connected, not_connected,
	// not_enrolled, incompatible, epoch_mismatch, quarantined, revoked.
	AgentState string
	Reason     string
	// Blocked: no path may be used (revoked/quarantined identity); Reason
	// says what the administrator has to decide.
	Blocked bool
}

// unavailableError: an agent that answers to another controller epoch
// (restore reconciliation) would refuse the operation; that is not a
// connection problem, and nothing is sent.
func (d Decision) unavailableError() error {
	if d.AgentState != "epoch_mismatch" {
		return nil
	}
	return &model.OpError{Class: model.ClassConfiguration, Message: d.Reason,
		Hint: "Nothing was sent to the host. Settings → System → Restore shows what is left to reconcile."}
}

// Decide returns the management decision for a host.
func (r *Router) Decide(ctx context.Context, h db.Host) Decision {
	d := Decision{Preferred: h.ManagementMethod, Fallback: h.ManagementFallback}
	if h.ManagementMethod != "agent" {
		d.SSHAllowed = true
		return d
	}
	sshOK := h.ManagementFallback == "ssh" && h.SshConfigured != 0
	block := func(state, reason string) Decision {
		d.AgentState, d.Reason, d.Blocked = state, reason, true
		return d
	}
	unavailable := func(state, reason string) Decision {
		d.AgentState, d.Reason, d.SSHAllowed = state, reason, sshOK
		return d
	}
	if r.Agents == nil {
		return unavailable("disabled", "the agent endpoint is disabled on this controller")
	}
	ident, err := r.St.R.GetLiveIdentityForHost(ctx, h.ID)
	if errors.Is(err, sql.ErrNoRows) {
		if all, _ := r.St.R.ListIdentitiesForHost(ctx, h.ID); len(all) > 0 && all[0].Status == "revoked" {
			return block("revoked", "this host's agent identity was revoked; re-enroll the agent, or switch the host to SSH management")
		}
		return unavailable("not_enrolled", "no agent is enrolled for this host")
	} else if err != nil {
		return unavailable("unknown", err.Error())
	}
	switch {
	case ident.Status == "quarantined":
		return block("quarantined", "identity conflict: two machines present this host's agent identity; resolve it on the host page")
	case ident.Compatible == 0:
		return unavailable("incompatible", "the agent's protocol version is incompatible: "+ident.CompatReason)
	case ident.EpochReported != "" && ident.EpochReported != r.Agents.Epoch():
		return unavailable("epoch_mismatch", "this host's Shepherd still answers to the controller epoch of the backup this controller was restored from; "+
			"it is moved to this controller's epoch once DROOL has compared its state")
	}
	if _, ok := r.Agents.SessionFor(h.ID); !ok {
		return unavailable("not_connected", "the agent is not connected")
	}
	d.AgentState, d.UseAgent = "connected", true
	return d
}

// --- jobs ---

// Prepare implements engine.Executor.
func (r *Router) Prepare(ctx context.Context, hostID string) (engine.Path, error) {
	h, err := r.St.R.GetHost(ctx, hostID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, inventory.ErrHostNotFound
	} else if err != nil {
		return nil, err
	}
	p := &path{r: r, host: h, pins: map[int]string{}}
	d := r.Decide(ctx, h)
	if d.Blocked {
		return nil, &model.OpError{Class: model.ClassAuthorization, Message: d.Reason}
	}
	if h.SshConfigured != 0 && (d.SSHAllowed || h.ManagementFallback == "ssh") {
		// Resolve now for redaction secrets and the jump host; a host
		// whose SSH can't be resolved simply has no SSH path.
		if c, err := r.Inv.Resolve(ctx, hostID, true); err == nil {
			p.conn = c
		} else if h.ManagementMethod != "agent" {
			return nil, err
		}
	}
	return p, nil
}

type path struct {
	r         *Router
	host      db.Host
	conn      *inventory.Conn
	pins      map[int]string // step → transport an operation was dispatched on
	transport string
	note      string
}

func (p *path) Transport() (string, string) { return p.transport, p.note }

func (p *path) JumpHostID() string {
	if p.conn != nil && p.conn.Target.Via != nil && p.host.ManagementMethod != "agent" {
		return p.conn.Target.Via.HostID
	}
	return ""
}

func (p *path) Secrets() []string {
	if p.conn == nil {
		return nil
	}
	return []string{p.conn.Target.Password, p.conn.Become.Password}
}

func (p *path) Execute(ctx context.Context, op model.Operation, out driver.OutputSink, started func()) (*driver.OpResult, error) {
	if op.Type == model.OpFirewallApply {
		if p.r.Firewall == nil {
			return nil, &model.OpError{Class: model.ClassConfiguration, Message: "firewall management is not available on this controller"}
		}
		res, err := p.r.Firewall.Apply(ctx, p.host.ID, op.FirewallApply, out, started)
		if res != nil {
			if tx, ok := res.Data["firewall"].(fwsvc.TxRow); ok {
				p.transport = tx.Transport
			} else if tr, ok := res.Data["transport"].(string); ok {
				p.transport = tr
			}
		}
		return res, err
	}
	if p.r.Firewall != nil && DisruptsFirewall(op) {
		if why := p.r.Firewall.PendingReason(ctx, p.host.ID); why != "" {
			return nil, &driver.NotStartedError{Err: &model.OpError{Class: model.ClassLock, Message: "refused: " + why,
				Hint: "Reboots and network restarts wait until the firewall transaction commits or rolls back."}}
		}
	}
	step := driver.StepInfoFrom(ctx).Step
	pin := p.pins[step]
	d := p.r.Decide(ctx, p.host)
	if d.Blocked {
		return nil, &model.OpError{Class: model.ClassAuthorization, Message: d.Reason}
	}
	agentErr := error(nil)
	if pin != "ssh" && d.Preferred == "agent" {
		if d.UseAgent {
			res, err := p.r.Agents.Execute(ctx, p.host.ID, op, out, started, func() { p.pins[step] = "agent" })
			p.transport, p.note = "agent", ""
			if err == nil || !driver.IsNotStarted(err) || p.pins[step] == "agent" {
				return res, err // done, refused, or possibly started: never re-run elsewhere
			}
			agentErr = err
			d.Reason = model.AsOpError(err).Message
		}
		if pin == "agent" {
			return nil, &model.OpError{Class: model.ClassConnection,
				Message: "the agent is unavailable; not retrying over SSH because this operation was already sent to the agent",
				Hint:    "Check the host before running the operation again."}
		}
		if !d.SSHAllowed || p.conn == nil {
			if agentErr != nil {
				return nil, agentErr
			}
			if err := d.unavailableError(); err != nil {
				return nil, err
			}
			return nil, &driver.NotStartedError{Err: &model.OpError{Class: model.ClassConnection, Message: d.Reason,
				Hint: "Start drool-agent on the host, or configure SSH as the fallback path."}}
		}
		p.note = "SSH fallback: " + d.Reason
		fmt.Fprintf(out.Stderr(), "[drool] %s; using the SSH fallback path\n", d.Reason)
	} else if p.conn == nil {
		return nil, &model.OpError{Class: model.ClassConfiguration, Message: "this host has no usable SSH configuration"}
	}
	p.transport = "ssh"
	wrapped := func() {
		p.pins[step] = "ssh"
		if started != nil {
			started()
		}
	}
	return p.r.SSH.Execute(ctx, p.conn.Target, p.conn.Become, op, out, wrapped)
}

// --- files ---

// OpenFS implements files.Opener. Agent hosts use the agent (root within
// its allowed paths); a capability denial is final, never a reason to use
// SSH instead.
func (r *Router) OpenFS(ctx context.Context, hostID string, elevated bool) (files.FS, error) {
	h, err := r.St.R.GetHost(ctx, hostID)
	if err != nil {
		return nil, err
	}
	d := r.Decide(ctx, h)
	if d.Blocked {
		return nil, &model.OpError{Class: model.ClassAuthorization, Message: d.Reason}
	}
	if d.UseAgent {
		fsys, err := r.Agents.OpenFS(ctx, hostID, nil, "file manager", time.Hour)
		if err == nil || !driver.IsNotStarted(err) {
			return fsys, err
		}
		d.Reason = model.AsOpError(err).Message
		d.SSHAllowed = h.ManagementFallback == "ssh" && h.SshConfigured != 0
	}
	if !d.SSHAllowed {
		if err := d.unavailableError(); err != nil {
			return nil, err
		}
		return nil, &model.OpError{Class: model.ClassConnection, Message: d.Reason}
	}
	return r.Inv.OpenFS(ctx, hostID, elevated)
}

// --- dry runs ---

// Simulate runs a package/system-update dry run on the host's path.
func (r *Router) Simulate(ctx context.Context, hostID string, op model.Operation) (*hostops.SimResult, error) {
	h, err := r.St.R.GetHost(ctx, hostID)
	if err != nil {
		return nil, err
	}
	d := r.Decide(ctx, h)
	if d.Blocked {
		return nil, &model.OpError{Class: model.ClassAuthorization, Message: d.Reason}
	}
	if d.UseAgent {
		res, err := r.Agents.Simulate(ctx, hostID, op)
		if err == nil || !driver.IsNotStarted(err) {
			return res, err
		}
		d.Reason = model.AsOpError(err).Message
		d.SSHAllowed = h.ManagementFallback == "ssh" && h.SshConfigured != 0
	}
	if !d.SSHAllowed {
		if err := d.unavailableError(); err != nil {
			return nil, err
		}
		return nil, &model.OpError{Class: model.ClassConnection, Message: d.Reason}
	}
	conn, err := r.Inv.Resolve(ctx, hostID, true)
	if err != nil {
		return nil, err
	}
	return r.SSH.Simulate(ctx, conn.Target, conn.Become, op)
}

// PackageVersions reads which packages are installed on the host's path
// (desired state, M14): the agent's read-only query, or SSH as the login
// user. Nothing changes on the host.
func (r *Router) PackageVersions(ctx context.Context, hostID string, names []string) (string, map[string]string, error) {
	h, err := r.St.R.GetHost(ctx, hostID)
	if err != nil {
		return "", nil, err
	}
	d := r.Decide(ctx, h)
	if d.Blocked {
		return "", nil, &model.OpError{Class: model.ClassAuthorization, Message: d.Reason}
	}
	if d.UseAgent {
		mgr, vs, err := r.Agents.PackageVersions(ctx, hostID, names)
		if err == nil || !driver.IsNotStarted(err) {
			return mgr, vs, err
		}
		d.Reason = model.AsOpError(err).Message
		d.SSHAllowed = h.ManagementFallback == "ssh" && h.SshConfigured != 0
	}
	if !d.SSHAllowed {
		return "", nil, &model.OpError{Class: model.ClassConnection, Message: d.Reason}
	}
	conn, err := r.Inv.Resolve(ctx, hostID, true)
	if err != nil {
		return "", nil, err
	}
	return r.SSH.PackageVersions(ctx, conn.Target, conn.Become, names)
}

// AgentHealthy reports whether a host's inventory comes from its agent
// (so the poller doesn't also poll it over SSH).
func (r *Router) AgentHealthy(ctx context.Context, h db.Host) bool {
	if h.ManagementMethod != "agent" {
		return false
	}
	if h.SshConfigured == 0 {
		return true // nothing to poll over SSH anyway
	}
	d := r.Decide(ctx, h)
	return d.UseAgent || d.Blocked || !d.SSHAllowed
}
