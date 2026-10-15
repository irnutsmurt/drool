package agentsvc

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"drool/internal/agentrm"
	"drool/internal/audit"
	"drool/internal/store/db"
)

// Removing a Shepherd (final After-M17 batch, ARCHITECTURE-ALPHA §38; the
// dogfooding correction batch, §40, added the Shepherd's own way).
//
// A connected Shepherd that announces "uninstall-1" removes itself when
// asked (MethodShepherd): no SSH is needed. The controller sends a typed
// request over the Shepherd's authenticated session; the Shepherd starts a
// detached helper as root and answers; the controller then revokes the
// identity, revokes enrollment tokens still able to enroll the host,
// forgets the capability narrowing and switches the host to SSH (or to no
// management path at all when it has no SSH access: shown as such, never
// invented), in one audited transaction, and waits for the Shepherd to
// disconnect. It can't see the files go: the outcome is "initiated", and
// only a working SSH path, used afterwards to look, can make it
// "uninstalled" (verified) or "partial".
//
// Over SSH (MethodSSH, the way when the Shepherd can't be asked: offline,
// or too old), the removal runs on the host as one script read from stdin
// (its text is in no process's command line, which the script itself
// searches for Shepherd processes), and the same controller transaction
// follows; DROOL then checks that no session is left.
//
// MethodForget only revokes and forgets, changing nothing on the host: for
// a host neither way reaches, after removing the Shepherd by hand.
//
// Anything DROOL can't confirm is reported as such, never as success;
// running it again continues.

// Removal methods.
const (
	MethodShepherd = "shepherd" // the Shepherd removes itself
	MethodSSH      = "ssh"      // DROOL removes it over SSH, as root
	MethodForget   = "forget"   // DROOL forgets it; nothing on the host changes
)

// PathState says whether one way of removing the Shepherd works now, and
// why not.
type PathState struct {
	Available bool `json:"available"`
	// Code: no_identity | not_connected | too_old (the Shepherd's way);
	// no_ssh | ssh_unavailable (SSH).
	Code   string `json:"code,omitempty"`
	Reason string `json:"reason,omitempty"`
}

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

// UninstallPlan is what removing a host's Shepherd would do and which ways
// work now. With SSH, the files are read from the host over SSH as root
// (Checked), which also proves that path works.
type UninstallPlan struct {
	HostID   string `json:"host_id"`
	HostName string `json:"host_name"`
	// CanUninstall is false with Refusal (and RefusalCode) when DROOL
	// can't remove the Shepherd now: neither way works (no_path), or
	// nothing is left to remove (nothing_to_remove).
	CanUninstall bool   `json:"can_uninstall"`
	RefusalCode  string `json:"refusal_code,omitempty"`
	Refusal      string `json:"refusal,omitempty"`
	// Method is the way DROOL takes: shepherd when the Shepherd can remove
	// itself, else ssh; "" when neither works.
	Method   string             `json:"method"`
	Shepherd PathState          `json:"shepherd"`
	SSH      PathState          `json:"ssh"`
	SignIn   string             `json:"sign_in"` // DROOL's SSH sign-in ("" without SSH access)
	After    string             `json:"after"`   // how DROOL manages the host afterwards
	Identity *UninstallIdentity `json:"identity"`
	Version  string             `json:"version"` // the Shepherd's version
	Checked  bool               `json:"checked"` // Items, Systemd, Service and Processes were read over SSH
	Systemd  string             `json:"systemd"`
	Service  string             `json:"service"` // systemd's state of drool-agent.service ("" without systemd)
	// Processes: Shepherd processes running (read over SSH).
	Processes  int             `json:"processes"`
	Items      []UninstallItem `json:"items"`
	Controller []string        `json:"controller"` // what changes in DROOL

	sshConfigured bool
}

// UninstallResult is what removing a Shepherd did, as far as DROOL can
// confirm it.
type UninstallResult struct {
	Method string `json:"method"` // shepherd | ssh | forget
	// Outcome: uninstalled (confirmed on the host over SSH, and in DROOL),
	// initiated (the Shepherd accepted and is removing itself; DROOL
	// revoked it and can't look at the host), partial (something is left,
	// listed), unknown (the host didn't report back; nothing changed in
	// DROOL), refused (the Shepherd refused; nothing changed), forgotten
	// (DROOL forgot it; nothing on the host changed), nothing (there was
	// nothing left to remove).
	Outcome         string   `json:"outcome"`
	Removed         []string `json:"removed"`
	Remaining       []string `json:"remaining"`
	StillRunning    int      `json:"still_running"`
	SessionLeft     bool     `json:"session_left"`
	IdentityRevoked bool     `json:"identity_revoked"`
	TokensRevoked   int64    `json:"tokens_revoked"`
	Management      string   `json:"management"` // ssh | none
	Message         string   `json:"message"`
	Output          string   `json:"output"`
	// The Shepherd's way: how its helper runs, whether it disconnected as
	// expected, and whether SSH confirmed the result afterwards (and why
	// not).
	Helper       string `json:"helper,omitempty"`
	Disconnected bool   `json:"disconnected"`
	Verified     bool   `json:"verified"`
	VerifyError  string `json:"verify_error,omitempty"`
}

func newResult(method string) *UninstallResult {
	return &UninstallResult{Method: method, Removed: []string{}, Remaining: []string{}}
}

// ErrUninstallRefused is a refusal before anything changed.
type ErrUninstallRefused struct {
	Code, Msg string
}

func (e *ErrUninstallRefused) Error() string { return e.Msg }

func (s *Service) runUninstallScript(ctx context.Context, hostID, mode string, timeout time.Duration) ([]byte, error) {
	r, err := s.inv.RunOnHost(ctx, hostID, "sh -s", []byte("MODE="+mode+"\n"+agentrm.Script), true, timeout)
	if err != nil {
		return nil, err
	}
	if r.ExitCode != 0 && len(r.Stdout) == 0 {
		return nil, fmt.Errorf("exit status %d: %s", r.ExitCode, strings.TrimSpace(string(r.Stderr)))
	}
	return r.Stdout, nil
}

// PlanUninstall reads what removing the host's Shepherd would do, and
// which ways work now. It changes nothing.
func (s *Service) PlanUninstall(ctx context.Context, hostID string) (*UninstallPlan, error) {
	h, err := s.st.R.GetHost(ctx, hostID)
	if err != nil {
		return nil, err
	}
	p := &UninstallPlan{HostID: h.ID, HostName: h.Name, Items: []UninstallItem{}, Controller: []string{}, sshConfigured: h.SshConfigured != 0}
	ids, _ := s.st.R.ListIdentitiesForHost(ctx, h.ID)
	live := false
	if len(ids) > 0 {
		connected, has := s.SessionFeature(h.ID, FeatureUninstall)
		live = ids[0].Status != "revoked"
		p.Identity = &UninstallIdentity{ID: ids[0].ID, Fingerprint: ids[0].Fingerprint, Status: ids[0].Status, Connected: connected && live}
		p.Version = ids[0].AgentVersion
		switch {
		case !live:
			p.Shepherd = PathState{Code: "no_identity", Reason: "DROOL trusts no Shepherd identity for this host any more."}
		case !connected:
			p.Shepherd = PathState{Code: "not_connected", Reason: "The Shepherd isn't connected, so it can't be asked to remove itself."}
		case !has:
			p.Shepherd = PathState{Code: "too_old", Reason: fmt.Sprintf("This Shepherd (version %s) can't remove itself: it's from before Shepherds could. "+
				"Update it (Shepherds → Updates) and ask again, or remove it over SSH or by hand.", orUnknown(p.Version))}
		default:
			p.Shepherd.Available = true
		}
	} else {
		p.Shepherd = PathState{Code: "no_identity", Reason: "DROOL has no Shepherd identity for this host."}
	}
	var rep agentrm.Report
	if h.SshConfigured == 0 {
		p.SSH = PathState{Code: "no_ssh", Reason: "No SSH access is set up for this host."}
	} else {
		via := ""
		if j := s.inv.JumpHostID(ctx, h.ID); j != "" {
			if jh, err := s.st.R.GetHost(ctx, j); err == nil {
				via = " through " + jh.Name
			}
		}
		become := map[string]string{"sudo": " (root through sudo)", "none": ""}[h.BecomeMethod]
		p.SignIn = fmt.Sprintf("%s@%s%s%s", h.SshUser, h.ConnectAddress, via, become)
		out, err := s.runUninstallScript(ctx, h.ID, "check", time.Minute)
		if err != nil {
			p.SSH = PathState{Code: "ssh_unavailable", Reason: fmt.Sprintf("DROOL can't run commands as root on %s over SSH (%v).", h.Name, err)}
		} else {
			p.SSH.Available, p.Checked = true, true
			rep = agentrm.Parse(out)
			p.Systemd, p.Service, p.Processes = rep.Systemd, rep.Active, rep.PIDs
		}
	}
	for _, it := range agentrm.Items {
		p.Items = append(p.Items, UninstallItem{Path: it.Path, What: it.What, Present: p.Checked && rep.Present[it.Path]})
	}
	if p.Checked && !live && rep.Clean() {
		p.RefusalCode = "nothing_to_remove"
		p.Refusal = fmt.Sprintf("There's no Shepherd on %s any more (no files, no process, no service), and DROOL trusts no identity for it.", h.Name)
		return p, nil
	}
	if !live && !p.SSH.Available {
		p.RefusalCode = "no_identity"
		p.Refusal = fmt.Sprintf("DROOL trusts no Shepherd identity for %s any more (it was removed or revoked), and can't look at the host without working SSH. "+
			"If anything of a Shepherd is left there, remove it by hand (user guide: Removing a Host, “By hand”).", h.Name)
		return p, nil
	}
	switch {
	case p.Shepherd.Available:
		p.Method = MethodShepherd
	case p.SSH.Available:
		p.Method = MethodSSH
	default:
		p.RefusalCode = "no_path"
		p.Refusal = fmt.Sprintf("DROOL can't remove the Shepherd from %s now. %s %s Remove it on the host by hand (user guide: Removing a Host, “By hand”), "+
			"then forget it here: DROOL revokes its identity and changes nothing on the host.", h.Name, p.Shepherd.Reason, p.SSH.Reason)
		return p, nil
	}
	p.CanUninstall = true
	if p.sshConfigured {
		p.After = fmt.Sprintf("DROOL manages %s over SSH as %s.", h.Name, p.SignIn)
		if !p.SSH.Available {
			p.After += " That doesn't work right now: fix it in the host's connection settings."
		}
	} else {
		p.After = fmt.Sprintf("DROOL has no way to manage %s afterwards: no SSH access is set up. Set it up (the host's connection settings), "+
			"install a Shepherd again, or remove the host.", h.Name)
	}
	if live {
		p.Controller = append(p.Controller, fmt.Sprintf("Revoke the Shepherd's identity (key %s): it can never connect again.", short(p.Identity.Fingerprint)))
	}
	p.Controller = append(p.Controller, "Revoke enrollment tokens that could still enroll a Shepherd for this host.",
		"Forget the capability narrowing set for this Shepherd.", p.After)
	return p, nil
}

func orUnknown(v string) string {
	if v == "" {
		return "unknown"
	}
	return v
}

func short(fp string) string {
	if len(fp) > 30 {
		return fp[:30] + "…"
	}
	return fp
}

// Uninstall removes the host's Shepherd by method ("": the way the plan
// takes). The caller holds the host's exclusive lock and audits a refusal.
func (s *Service) Uninstall(ctx context.Context, hostID, userID, method string) (*UninstallResult, error) {
	plan, err := s.PlanUninstall(ctx, hostID)
	if err != nil {
		return nil, err
	}
	if plan.RefusalCode == "nothing_to_remove" {
		r := newResult(method)
		r.Outcome, r.Management, r.Message = "nothing", map[bool]string{true: "ssh", false: "none"}[plan.sshConfigured], plan.Refusal
		return r, nil
	}
	if method == "" {
		method = plan.Method
	}
	switch method {
	case MethodShepherd:
		if !plan.Shepherd.Available {
			return nil, &ErrUninstallRefused{Code: plan.Shepherd.Code, Msg: plan.Shepherd.Reason + fallbackHint(plan)}
		}
		return s.uninstallViaShepherd(ctx, plan, userID)
	case MethodSSH:
		if !plan.SSH.Available {
			return nil, &ErrUninstallRefused{Code: plan.SSH.Code, Msg: plan.SSH.Reason + " DROOL removes a Shepherd over SSH only while that works."}
		}
		return s.uninstallViaSSH(ctx, plan, userID)
	case MethodForget:
		return s.forgetShepherd(ctx, plan, userID)
	case "":
		return nil, &ErrUninstallRefused{Code: plan.RefusalCode, Msg: plan.Refusal}
	}
	return nil, &ErrUninstallRefused{Code: "bad_method", Msg: "the method must be shepherd, ssh or forget"}
}

// UninstallViaSSH removes a host's Shepherd over SSH (Uninstall, MethodSSH).
func (s *Service) UninstallViaSSH(ctx context.Context, hostID, userID string) (*UninstallResult, error) {
	return s.Uninstall(ctx, hostID, userID, MethodSSH)
}

func fallbackHint(p *UninstallPlan) string {
	if p.SSH.Available {
		return " DROOL can remove it over SSH instead."
	}
	return " " + p.SSH.Reason + " Remove it on the host by hand (user guide: Removing a Host, “By hand”), then forget it here."
}

func identityDetails(p *UninstallPlan, method string) map[string]any {
	d := map[string]any{"method": method}
	if p.SignIn != "" {
		d["sign_in"] = p.SignIn
	}
	if p.Identity != nil {
		d["identity_id"], d["key_fingerprint"] = p.Identity.ID, p.Identity.Fingerprint
	}
	return d
}

// forgetTx is the controller's side of a removal, in the caller's
// transaction: the identity revoked, enrollment tokens that could still
// enroll the host revoked, the capability narrowing forgotten, and the
// host managed over SSH (or, without SSH access, by nothing: the host page
// says so).
func (s *Service) forgetTx(ctx context.Context, tx *sql.Tx, p *UninstallPlan, userID, why string, res *UninstallResult) error {
	q := db.New(tx)
	now := s.now()
	if p.Identity != nil && p.Identity.Status != "revoked" {
		if _, err := s.RevokeIdentity(ctx, q, p.Identity.ID, userID, why); err != nil {
			return err
		}
		res.IdentityRevoked = true
	}
	r, err := tx.ExecContext(ctx, `UPDATE agent_enrollment_tokens SET revoked_at = ? WHERE host_id = ? AND used_at IS NULL AND revoked_at IS NULL`, now.UnixMilli(), p.HostID)
	if err != nil {
		return err
	}
	res.TokensRevoked, _ = r.RowsAffected()
	if err := q.SetHostManagement(ctx, db.SetHostManagementParams{ManagementMethod: "ssh", ManagementFallback: "none", UpdatedAt: now.UnixMilli(), ID: p.HostID}); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE hosts SET requested_caps_json = '' WHERE id = ?`, p.HostID); err != nil {
		return err
	}
	res.Management = "none"
	if p.sshConfigured {
		res.Management = "ssh"
	}
	return nil
}

// waitNoSession waits (at most d) until the host has no Shepherd session.
func (s *Service) waitNoSession(ctx context.Context, hostID string, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for {
		if _, ok := s.SessionFor(hostID); !ok {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func (s *Service) manageSentence(p *UninstallPlan) string {
	if p.sshConfigured {
		return fmt.Sprintf("DROOL manages %s over SSH as %s.", p.HostName, p.SignIn)
	}
	return fmt.Sprintf("DROOL now has no way to manage %s: no SSH access is set up. Set it up (the host's connection settings), install a Shepherd again, or remove the host.", p.HostName)
}

// uninstallViaShepherd asks the Shepherd to remove itself (MethodShepherd).
func (s *Service) uninstallViaShepherd(ctx context.Context, plan *UninstallPlan, userID string) (*UninstallResult, error) {
	res := newResult(MethodShepherd)
	details := identityDetails(plan, MethodShepherd)
	ack, err := s.RequestSelfUninstall(ctx, plan.HostID, "removed in DROOL", orDefault(s.removeAckWait, 30*time.Second))
	if err != nil {
		res.Outcome = "unknown"
		res.Message = fmt.Sprintf("The Shepherd on %s didn't confirm the removal (%v). It may have started removing itself, or not: nothing changed in DROOL, "+
			"and its identity is still trusted. Try again: a Shepherd that is still there answers again; one that is gone can be removed over SSH (if that works) or forgotten.",
			plan.HostName, err)
		details["outcome"], details["error"] = res.Outcome, err.Error()
		s.audit.Record(context.WithoutCancel(ctx), audit.Event{Action: "agent.uninstall", Result: audit.Failure, TargetType: "host", TargetID: plan.HostID, Details: details})
		return res, nil
	}
	if !ack.Accepted {
		res.Outcome = "refused"
		res.Message = fmt.Sprintf("The Shepherd on %s refused to remove itself: %s. Nothing changed.", plan.HostName, ack.Error)
		details["outcome"], details["refusal"], details["error"] = res.Outcome, ack.Code, ack.Error
		s.audit.Record(context.WithoutCancel(ctx), audit.Event{Action: "agent.uninstall", Result: audit.Failure, TargetType: "host", TargetID: plan.HostID, Details: details})
		return res, nil
	}
	res.Helper = ack.Helper
	var emit func()
	err = s.st.InRawTx(ctx, func(tx *sql.Tx) error {
		if err := s.forgetTx(ctx, tx, plan, userID, "the Shepherd removed itself, as asked", res); err != nil {
			return err
		}
		details["outcome"], details["helper"], details["repeated_request"] = "initiated", ack.Helper, ack.Already
		details["identity_revoked"], details["unused_enrollments_revoked"], details["management"] = res.IdentityRevoked, res.TokensRevoked, res.Management
		var err error
		emit, err = s.audit.RecordTx(ctx, db.New(tx), audit.Event{Action: "agent.uninstall", Result: audit.Success, TargetType: "host", TargetID: plan.HostID, Details: details})
		return err
	})
	if err != nil {
		res.Outcome = "unknown"
		res.Message = fmt.Sprintf("The Shepherd on %s is removing itself, but DROOL couldn't record it (%v): its identity may still be trusted. Remove it again to finish.", plan.HostName, err)
		return res, nil
	}
	emit()
	// Its helper stops it: the connection ends by itself. If it doesn't,
	// DROOL ends it (the identity is revoked already).
	res.Disconnected = s.waitNoSession(ctx, plan.HostID, orDefault(s.removeGoneWait, 30*time.Second))
	if !res.Disconnected && plan.Identity != nil {
		s.DisconnectIdentity(plan.Identity.ID, "revoked", "the Shepherd removed itself")
		res.SessionLeft = !s.waitNoSession(ctx, plan.HostID, 5*time.Second)
	}
	// Only a working SSH path can show that the files went; DROOL looks
	// for a bounded time (the helper needs a few seconds).
	if plan.SSH.Available {
		rep, err := s.pollRemoved(ctx, plan.HostID, orDefault(s.removeVerifyWait, time.Minute))
		if err != nil {
			res.VerifyError = err.Error()
		} else {
			res.Verified = true
			for _, it := range plan.Items {
				if rep.Present[it.Path] {
					res.Remaining = append(res.Remaining, it.Path)
				} else if it.Present {
					res.Removed = append(res.Removed, it.Path)
				}
			}
			res.StillRunning = rep.PIDs
			if rep.Active == "active" {
				res.StillRunning++
			}
		}
	}
	gone := "disconnected as expected"
	if !res.Disconnected {
		gone = "hadn't disconnected after a while, so DROOL closed its connection"
	}
	switch {
	case res.Verified && len(res.Remaining) == 0 && res.StillRunning == 0:
		res.Outcome = "uninstalled"
		res.Message = fmt.Sprintf("Verified removed: the Shepherd removed itself from %s (it %s), and DROOL checked over SSH that nothing of it is left. Its identity is revoked. %s",
			plan.HostName, gone, s.manageSentence(plan))
	case res.Verified:
		res.Outcome = "partial"
		var left []string
		if len(res.Remaining) > 0 {
			left = append(left, "these are still on the host: "+strings.Join(res.Remaining, ", "))
		}
		if res.StillRunning > 0 {
			left = append(left, fmt.Sprintf("%d Shepherd process(es) still run", res.StillRunning))
		}
		res.Message = fmt.Sprintf("The Shepherd started removing itself from %s (it %s), but DROOL's check over SSH still finds: %s. Its identity is revoked. "+
			"Remove it again (over SSH) to finish. %s", plan.HostName, gone, strings.Join(left, "; "), s.manageSentence(plan))
	default:
		res.Outcome = "initiated"
		why := "Without SSH access, DROOL can't check that every file is gone"
		if res.VerifyError != "" {
			why = "DROOL couldn't check over SSH that every file is gone (" + res.VerifyError + ")"
		}
		res.Message = fmt.Sprintf("Self-removal initiated: the Shepherd on %s accepted and %s. DROOL revoked its identity. %s; to be sure, look on the host "+
			"(user guide: Removing a Host). %s", plan.HostName, gone, why, s.manageSentence(plan))
	}
	return res, nil
}

// pollRemoved runs the check over SSH until nothing of the Shepherd is left
// or d passes, and returns the last report.
func (s *Service) pollRemoved(ctx context.Context, hostID string, d time.Duration) (agentrm.Report, error) {
	deadline := time.Now().Add(d)
	for {
		out, err := s.runUninstallScript(ctx, hostID, "check", time.Minute)
		if err != nil {
			return agentrm.Report{}, err
		}
		rep := agentrm.Parse(out)
		if rep.Clean() || time.Now().After(deadline) {
			return rep, nil
		}
		select {
		case <-ctx.Done():
			return rep, nil
		case <-time.After(time.Second):
		}
	}
}

// forgetShepherd revokes and forgets the Shepherd, changing nothing on the
// host (MethodForget).
func (s *Service) forgetShepherd(ctx context.Context, plan *UninstallPlan, userID string) (*UninstallResult, error) {
	res := newResult(MethodForget)
	if plan.Identity == nil || plan.Identity.Status == "revoked" {
		res.Outcome, res.Message = "nothing", fmt.Sprintf("DROOL trusts no Shepherd identity for %s: there's nothing to forget.", plan.HostName)
		res.Management = map[bool]string{true: "ssh", false: "none"}[plan.sshConfigured]
		return res, nil
	}
	details := identityDetails(plan, MethodForget)
	var emit func()
	err := s.st.InRawTx(ctx, func(tx *sql.Tx) error {
		if err := s.forgetTx(ctx, tx, plan, userID, "forgotten in DROOL; nothing on the host was changed", res); err != nil {
			return err
		}
		details["outcome"], details["identity_revoked"], details["unused_enrollments_revoked"], details["management"] = "forgotten", res.IdentityRevoked, res.TokensRevoked, res.Management
		var err error
		emit, err = s.audit.RecordTx(ctx, db.New(tx), audit.Event{Action: "agent.uninstall", Result: audit.Success, TargetType: "host", TargetID: plan.HostID, Details: details})
		return err
	})
	if err != nil {
		return nil, err
	}
	emit()
	s.DisconnectIdentity(plan.Identity.ID, "revoked", "the Shepherd was forgotten in DROOL")
	res.Outcome = "forgotten"
	res.Message = fmt.Sprintf("DROOL forgot the Shepherd of %s: its identity is revoked, so it can never connect again, and nothing on the host changed: "+
		"its files stay until they're removed by hand (user guide: Removing a Host, “By hand”). %s", plan.HostName, s.manageSentence(plan))
	return res, nil
}

// uninstallViaSSH removes the Shepherd over SSH (MethodSSH).
func (s *Service) uninstallViaSSH(ctx context.Context, plan *UninstallPlan, userID string) (*UninstallResult, error) {
	res := newResult(MethodSSH)
	details := identityDetails(plan, MethodSSH)
	failed := func(msg string) (*UninstallResult, error) {
		res.Outcome, res.Message = "unknown", msg
		details["outcome"], details["error"] = res.Outcome, msg
		s.audit.Record(context.WithoutCancel(ctx), audit.Event{Action: "agent.uninstall", Result: audit.Failure, TargetType: "host", TargetID: plan.HostID, Details: details})
		return res, nil
	}
	out, err := s.runUninstallScript(ctx, plan.HostID, "remove", 3*time.Minute)
	res.Output = string(out)
	if err != nil {
		return failed(fmt.Sprintf("The removal on %s didn't report back (%v): the Shepherd may be partly removed. Nothing changed in DROOL: its identity is still "+
			"trusted. Remove it again to finish (it continues where it stopped).", plan.HostName, err))
	}
	rep := agentrm.Parse(out)
	if !rep.Done || !rep.Removed {
		return failed(fmt.Sprintf("The removal on %s stopped before it finished: the Shepherd may be partly removed. Nothing changed in DROOL. Remove it again to finish.", plan.HostName))
	}
	for _, it := range plan.Items {
		switch {
		case rep.Present[it.Path]:
			res.Remaining = append(res.Remaining, it.Path)
		case it.Present:
			res.Removed = append(res.Removed, it.Path)
		}
	}
	res.StillRunning = rep.PIDs
	if rep.Active == "active" {
		res.StillRunning++
	}
	res.Verified = true

	// The controller's side, in one transaction: the host is managed over
	// the SSH path the removal just used.
	var emit func()
	err = s.st.InRawTx(ctx, func(tx *sql.Tx) error {
		if err := s.forgetTx(ctx, tx, plan, userID, "the Shepherd was uninstalled", res); err != nil {
			return err
		}
		details["removed"], details["remaining"], details["still_running"] = res.Removed, res.Remaining, res.StillRunning
		details["identity_revoked"], details["unused_enrollments_revoked"] = res.IdentityRevoked, res.TokensRevoked
		result := audit.Success
		if len(res.Remaining) > 0 || res.StillRunning > 0 {
			result = audit.Failure
		}
		var err error
		emit, err = s.audit.RecordTx(ctx, db.New(tx), audit.Event{Action: "agent.uninstall", Result: result, TargetType: "host", TargetID: plan.HostID, Details: details})
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
	res.SessionLeft = !s.waitNoSession(ctx, plan.HostID, 15*time.Second)
	res.Disconnected = !res.SessionLeft
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
