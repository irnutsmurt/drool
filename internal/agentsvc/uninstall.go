package agentsvc

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"drool/internal/audit"
	"drool/internal/discovery"
	"drool/internal/store/db"
)

// Removing a Shepherd (final After-M17 batch, ARCHITECTURE-ALPHA §38).
// DROOL installs a Shepherd over SSH and now removes one the same way: the
// host's SSH path, checked to work as root before anything changes, is
// what manages the host afterwards, so a host is never left without one.
// The removal runs on the host as one script read from stdin (its text is
// in no process's command line, which the script itself searches for
// Shepherd processes); then the controller revokes the identity, revokes
// enrollment tokens still able to enroll the host, forgets the capability
// narrowing and switches the host to SSH, in one audited transaction; then
// it checks that no session is left. Anything DROOL can't confirm is
// reported as such, never as success; running it again continues.

// Shepherd files on a host (drool-agent install writes them).
var uninstallItems = []struct{ Path, What string }{
	{"/etc/systemd/system/drool-agent.service", "the drool-agent service"},
	{"/usr/libexec/drool-agent", "its programs (every installed version)"},
	{"/etc/drool/agent.yaml", "its capability ceiling"},
	{"/etc/drool/release-keys.d", "the release keys it trusts for updates"},
	{"/var/lib/drool-agent", "its identity (private key and certificates) and state"},
	{"/var/log/drool-agent", "its log"},
}

// uninstallScript removes (MODE=remove) or only reports (MODE=check) a
// Shepherd, as root. Every step tolerates absence, so running it again
// finishes what an interrupted run started. It ignores SIGHUP and SIGPIPE:
// a dropped connection doesn't stop it halfway.
const uninstallScript = `set -u
trap '' HUP PIPE
U=/etc/systemd/system/drool-agent.service
I=/usr/libexec/drool-agent
kv() { printf '@@%s %s\n' "$1" "$2"; }
` + discovery.SystemdProbe + `sd=$(sdstate)
procs() {
	for d in /proc/[0-9]*; do
		p=${d#/proc/}
		[ "$p" = "$$" ] && continue
		[ "$p" = "$PPID" ] && continue
		exe=$(readlink "$d/exe" 2>/dev/null) || exe=""
		case "$exe" in "$I"/*) echo "$p"; continue ;; esac
		cmd=$(tr '\0' ' ' < "$d/cmdline" 2>/dev/null) || continue
		case "$cmd" in *"$I/drool-agent-supervise"*) echo "$p" ;; esac
	done
}
report() {
	for p in "$U" "$I" /etc/drool/agent.yaml /etc/drool/release-keys.d /var/lib/drool-agent /var/log/drool-agent; do
		if [ -e "$p" ] || [ -L "$p" ]; then kv present "$p"; fi
	done
	if [ "$sd" = running ]; then
		kv active "$(systemctl is-active drool-agent.service 2>/dev/null)"
		kv enabled "$(systemctl is-enabled drool-agent.service 2>/dev/null)"
	fi
	for p in $(procs); do kv pid "$p"; done
}
kv systemd "$sd"
if [ "$MODE" = remove ]; then
	if [ "$sd" = running ]; then systemctl disable --now drool-agent.service >/dev/null 2>&1; fi
	# The supervisor of a host without systemd goes with the agent, or it
	# would start the agent again.
	for p in $(procs); do kill "$p" 2>/dev/null; done
	n=0
	while [ -n "$(procs)" ] && [ $n -lt 10 ]; do sleep 1; n=$((n+1)); done
	for p in $(procs); do kill -9 "$p" 2>/dev/null; done
	rm -f "$U"
	if [ "$sd" = running ]; then systemctl daemon-reload >/dev/null 2>&1; systemctl reset-failed drool-agent.service >/dev/null 2>&1; fi
	rm -rf "$I" /var/lib/drool-agent /var/log/drool-agent /etc/drool/release-keys.d
	rm -f /etc/drool/agent.yaml
	# Only when empty: DROOL's own configuration may live there.
	rmdir /etc/drool 2>/dev/null
	kv removed 1
fi
report
kv done 1
`

// UninstallItem is one Shepherd file or folder and whether it is there.
type UninstallItem struct {
	Path    string `json:"path"`
	What    string `json:"what"`
	Present bool   `json:"present"`
}

// UninstallIdentity is the Shepherd identity DROOL holds for the host.
type UninstallIdentity struct {
	ID          string `json:"id"`
	Fingerprint string `json:"fingerprint"`
	Status      string `json:"status"`
	Connected   bool   `json:"connected"`
}

// UninstallPlan is what removing a host's Shepherd would do, read from the
// host over SSH as root (which also proves that path works).
type UninstallPlan struct {
	HostID   string `json:"host_id"`
	HostName string `json:"host_name"`
	// CanUninstall is false with Refusal (and RefusalCode) when DROOL won't:
	// no SSH path to manage the host afterwards, SSH not working as root,
	// or nothing left to remove.
	CanUninstall bool               `json:"can_uninstall"`
	RefusalCode  string             `json:"refusal_code,omitempty"`
	Refusal      string             `json:"refusal,omitempty"`
	SignIn       string             `json:"sign_in"` // how DROOL manages the host afterwards
	Identity     *UninstallIdentity `json:"identity"`
	Systemd      string             `json:"systemd"`
	Service      string             `json:"service"` // systemd's state of drool-agent.service ("" without systemd)
	Processes    int                `json:"processes"`
	Items        []UninstallItem    `json:"items"`
	Controller   []string           `json:"controller"` // what changes in DROOL
}

// UninstallResult is what removing a Shepherd did, as far as DROOL can
// confirm it.
type UninstallResult struct {
	// Outcome: uninstalled (confirmed on the host and in DROOL), partial
	// (something is left, listed), unknown (the removal on the host didn't
	// report back; nothing changed in DROOL), nothing (there was nothing
	// left to remove).
	Outcome         string   `json:"outcome"`
	Removed         []string `json:"removed"`
	Remaining       []string `json:"remaining"`
	StillRunning    int      `json:"still_running"`
	SessionLeft     bool     `json:"session_left"`
	IdentityRevoked bool     `json:"identity_revoked"`
	TokensRevoked   int64    `json:"tokens_revoked"`
	Management      string   `json:"management"`
	Message         string   `json:"message"`
	Output          string   `json:"output"`
}

// ErrUninstallRefused is a refusal before anything changed.
type ErrUninstallRefused struct {
	Code, Msg string
}

func (e *ErrUninstallRefused) Error() string { return e.Msg }

type uninstallReport struct {
	systemd, active, enabled string
	present                  map[string]bool
	pids                     int
	removed, done            bool
}

func parseUninstall(out []byte) uninstallReport {
	r := uninstallReport{present: map[string]bool{}}
	for _, ln := range strings.Split(string(out), "\n") {
		k, v, _ := strings.Cut(strings.TrimSpace(strings.TrimPrefix(ln, "@@")), " ")
		if !strings.HasPrefix(ln, "@@") {
			continue
		}
		switch k {
		case "systemd":
			r.systemd = v
		case "active":
			r.active = v
		case "enabled":
			r.enabled = v
		case "present":
			r.present[v] = true
		case "pid":
			r.pids++
		case "removed":
			r.removed = true
		case "done":
			r.done = true
		}
	}
	return r
}

func (s *Service) runUninstallScript(ctx context.Context, hostID, mode string, timeout time.Duration) ([]byte, error) {
	r, err := s.inv.RunOnHost(ctx, hostID, "sh -s", []byte("MODE="+mode+"\n"+uninstallScript), true, timeout)
	if err != nil {
		return nil, err
	}
	if r.ExitCode != 0 && len(r.Stdout) == 0 {
		return nil, fmt.Errorf("exit status %d: %s", r.ExitCode, strings.TrimSpace(string(r.Stderr)))
	}
	return r.Stdout, nil
}

// PlanUninstall reads what removing the host's Shepherd would do. It
// changes nothing.
func (s *Service) PlanUninstall(ctx context.Context, hostID string) (*UninstallPlan, error) {
	h, err := s.st.R.GetHost(ctx, hostID)
	if err != nil {
		return nil, err
	}
	p := &UninstallPlan{HostID: h.ID, HostName: h.Name, Items: []UninstallItem{}, Controller: []string{}}
	ids, _ := s.st.R.ListIdentitiesForHost(ctx, h.ID)
	if len(ids) > 0 {
		_, connected := s.SessionFor(h.ID)
		p.Identity = &UninstallIdentity{ID: ids[0].ID, Fingerprint: ids[0].Fingerprint, Status: ids[0].Status, Connected: connected && ids[0].Status != "revoked"}
	}
	via := ""
	if j := s.inv.JumpHostID(ctx, h.ID); j != "" {
		if jh, err := s.st.R.GetHost(ctx, j); err == nil {
			via = " through " + jh.Name
		}
	}
	become := map[string]string{"sudo": " (root through sudo)", "none": ""}[h.BecomeMethod]
	p.SignIn = fmt.Sprintf("%s@%s%s%s", h.SshUser, h.ConnectAddress, via, become)
	if h.SshConfigured == 0 {
		p.RefusalCode = "last_management_path"
		p.Refusal = fmt.Sprintf("DROOL manages %s only through its Shepherd: there's no SSH access to fall back on. Removing the Shepherd would leave DROOL "+
			"with no way to manage the host. Set up SSH access first (the host's connection settings), then remove the Shepherd; or remove it on the "+
			"host by hand and revoke its identity (user guide: Removing a Host, “By hand”).", h.Name)
		return p, nil
	}
	out, err := s.runUninstallScript(ctx, h.ID, "check", time.Minute)
	if err != nil {
		p.RefusalCode = "ssh_unavailable"
		p.Refusal = fmt.Sprintf("DROOL can't run commands as root on %s over SSH (%v). It removes a Shepherd only while SSH works, because SSH is what "+
			"manages the host afterwards: fix SSH first (Test connection on the host's page), or remove the Shepherd on the host by hand (user guide: Removing a Host, “By hand”).", h.Name, err)
		return p, nil
	}
	rep := parseUninstall(out)
	p.Systemd, p.Service, p.Processes = rep.systemd, rep.active, rep.pids
	for _, it := range uninstallItems {
		p.Items = append(p.Items, UninstallItem{Path: it.Path, What: it.What, Present: rep.present[it.Path]})
	}
	live := p.Identity != nil && p.Identity.Status != "revoked"
	if !live && len(rep.present) == 0 && rep.pids == 0 && rep.active != "active" {
		p.RefusalCode = "nothing_to_remove"
		p.Refusal = fmt.Sprintf("There's no Shepherd on %s any more (no files, no process, no service), and DROOL trusts no identity for it.", h.Name)
		return p, nil
	}
	p.CanUninstall = true
	if live {
		p.Controller = append(p.Controller, fmt.Sprintf("Revoke the Shepherd's identity (key %s): it can never connect again.", short(p.Identity.Fingerprint)))
	}
	p.Controller = append(p.Controller, "Revoke enrollment tokens that could still enroll a Shepherd for this host.",
		"Forget the capability narrowing set for this Shepherd.",
		fmt.Sprintf("Manage %s over SSH as %s, which DROOL just checked.", h.Name, p.SignIn))
	return p, nil
}

func short(fp string) string {
	if len(fp) > 30 {
		return fp[:30] + "…"
	}
	return fp
}

// UninstallViaSSH removes a host's Shepherd (PlanUninstall says what that
// means). The caller holds the host's exclusive lock and audits a refusal.
func (s *Service) UninstallViaSSH(ctx context.Context, hostID, userID string) (*UninstallResult, error) {
	plan, err := s.PlanUninstall(ctx, hostID)
	if err != nil {
		return nil, err
	}
	if !plan.CanUninstall {
		if plan.RefusalCode == "nothing_to_remove" {
			return &UninstallResult{Outcome: "nothing", Removed: []string{}, Remaining: []string{}, Management: "ssh", Message: plan.Refusal}, nil
		}
		return nil, &ErrUninstallRefused{Code: plan.RefusalCode, Msg: plan.Refusal}
	}
	res := &UninstallResult{Removed: []string{}, Remaining: []string{}}
	details := map[string]any{"sign_in": plan.SignIn}
	if plan.Identity != nil {
		details["identity_id"], details["key_fingerprint"] = plan.Identity.ID, plan.Identity.Fingerprint
	}
	failed := func(msg string) (*UninstallResult, error) {
		res.Outcome, res.Message = "unknown", msg
		details["outcome"], details["error"] = res.Outcome, msg
		s.audit.Record(context.WithoutCancel(ctx), audit.Event{Action: "agent.uninstall", Result: audit.Failure, TargetType: "host", TargetID: hostID, Details: details})
		return res, nil
	}
	out, err := s.runUninstallScript(ctx, hostID, "remove", 3*time.Minute)
	res.Output = string(out)
	if err != nil {
		return failed(fmt.Sprintf("The removal on %s didn't report back (%v): the Shepherd may be partly removed. Nothing changed in DROOL: its identity is still "+
			"trusted. Remove it again to finish (it continues where it stopped).", plan.HostName, err))
	}
	rep := parseUninstall(out)
	if !rep.done || !rep.removed {
		return failed(fmt.Sprintf("The removal on %s stopped before it finished: the Shepherd may be partly removed. Nothing changed in DROOL. Remove it again to finish.", plan.HostName))
	}
	for _, it := range plan.Items {
		switch {
		case rep.present[it.Path]:
			res.Remaining = append(res.Remaining, it.Path)
		case it.Present:
			res.Removed = append(res.Removed, it.Path)
		}
	}
	res.StillRunning = rep.pids
	if rep.active == "active" {
		res.StillRunning++
	}

	// The controller's side, in one transaction: the host is managed over
	// the SSH path the removal just used.
	now := s.now()
	var emit func()
	err = s.st.InRawTx(ctx, func(tx *sql.Tx) error {
		q := db.New(tx)
		if plan.Identity != nil && plan.Identity.Status != "revoked" {
			if _, err := s.RevokeIdentity(ctx, q, plan.Identity.ID, userID, "the Shepherd was uninstalled"); err != nil {
				return err
			}
			res.IdentityRevoked = true
		}
		r, err := tx.ExecContext(ctx, `UPDATE agent_enrollment_tokens SET revoked_at = ? WHERE host_id = ? AND used_at IS NULL AND revoked_at IS NULL`, now.UnixMilli(), hostID)
		if err != nil {
			return err
		}
		res.TokensRevoked, _ = r.RowsAffected()
		if err := q.SetHostManagement(ctx, db.SetHostManagementParams{ManagementMethod: "ssh", ManagementFallback: "none", UpdatedAt: now.UnixMilli(), ID: hostID}); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE hosts SET requested_caps_json = '' WHERE id = ?`, hostID); err != nil {
			return err
		}
		res.Management = "ssh"
		details["removed"], details["remaining"], details["still_running"] = res.Removed, res.Remaining, res.StillRunning
		details["identity_revoked"], details["unused_enrollments_revoked"] = res.IdentityRevoked, res.TokensRevoked
		result := audit.Success
		if len(res.Remaining) > 0 || res.StillRunning > 0 {
			result = audit.Failure
		}
		emit, err = s.audit.RecordTx(ctx, q, audit.Event{Action: "agent.uninstall", Result: result, TargetType: "host", TargetID: hostID, Details: details})
		return err
	})
	if err != nil {
		return failed(fmt.Sprintf("The Shepherd was removed from %s, but DROOL couldn't record it (%v): its identity may still be trusted. Remove it again to finish.", plan.HostName, err))
	}
	emit()
	if plan.Identity != nil {
		s.DisconnectIdentity(plan.Identity.ID, "revoked", "the Shepherd was uninstalled")
	}
	// No session may be left: the Shepherd stopped, and its identity is
	// revoked. Bounded: a stopped agent's connection closes within seconds.
	deadline := time.Now().Add(15 * time.Second)
	for {
		if _, ok := s.SessionFor(hostID); !ok {
			break
		}
		if time.Now().After(deadline) {
			res.SessionLeft = true
			break
		}
		select {
		case <-ctx.Done():
			res.SessionLeft = true
			deadline = time.Now()
		case <-time.After(250 * time.Millisecond):
		}
	}
	switch {
	case len(res.Remaining) == 0 && res.StillRunning == 0 && !res.SessionLeft:
		res.Outcome = "uninstalled"
		res.Message = fmt.Sprintf("The Shepherd is gone from %s: DROOL checked that nothing of it is left on the host and no connection remains. DROOL manages the host over SSH as %s.",
			plan.HostName, plan.SignIn)
	default:
		res.Outcome = "partial"
		var left []string
		if len(res.Remaining) > 0 {
			left = append(left, "these are still on the host: "+strings.Join(res.Remaining, ", "))
		}
		if res.StillRunning > 0 {
			left = append(left, fmt.Sprintf("%d Shepherd process(es) still run", res.StillRunning))
		}
		if res.SessionLeft {
			left = append(left, "a connection with its identity hasn't closed yet")
		}
		res.Message = fmt.Sprintf("Not everything could be confirmed on %s: %s. Its identity is revoked and DROOL manages the host over SSH. Remove it again to finish, "+
			"or see the user guide (Removing a Shepherd) to clean up by hand.", plan.HostName, strings.Join(left, "; "))
	}
	return res, nil
}
