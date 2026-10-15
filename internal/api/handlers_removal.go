package api

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"drool/internal/agentsvc"
	"drool/internal/audit"
	"drool/internal/model"
	"drool/internal/store/db"
)

// Removing a host with "Clean up DROOL access" (dogfooding correction
// batch, ARCHITECTURE-ALPHA §40). DELETE /hosts/{id} stays what it was:
// remove from DROOL only, nothing on the host changes. The cleanup path
// first removes what DROOL installed there, then forgets the host:
//
//   - its Shepherd, which removes itself (or, when it can't be asked, over
//     SSH): agentsvc.Uninstall;
//   - the SSH account DROOL created (proven DROOL's on the host: the
//     marker, DROOL's key, the user ID), whose last session starts a
//     detached helper that removes it: sshaccount.Cleanup.
//
// Every part is checked first; if one can't start, nothing changes and the
// user is told why (they can still choose "Remove from DROOL only"). If a
// part fails once others ran, the host stays in DROOL and the result says
// exactly what was done and what wasn't. Files DROOL deployed, firewall
// rules and packages stay on the host either way.

// removalPart is one thing DROOL installed on the host and whether it can
// be cleaned up now.
type removalPart struct {
	What    string   `json:"what"` // shepherd | account
	Label   string   `json:"label"`
	Ready   bool     `json:"ready"`
	Method  string   `json:"method,omitempty"` // shepherd: shepherd | ssh
	Reason  string   `json:"reason,omitempty"`
	Removes []string `json:"removes"`
}

// removalPlan is what removing a host can do.
type removalPlan struct {
	HostID   string `json:"host_id"`
	HostName string `json:"host_name"`
	// Parts: what DROOL installed on the host and can remove (none: the
	// cleanup has nothing to do).
	Parts []removalPart `json:"parts"`
	// CanCleanUp: there's something to remove and every part can start.
	CanCleanUp bool `json:"can_clean_up"`
	// Blocked: the host can't be removed at all now (other hosts reach
	// DROOL through it).
	Blocked string   `json:"blocked,omitempty"`
	After   string   `json:"after"` // what the cleanup can and can't confirm
	Stays   []string `json:"stays"` // what DROOL leaves on the host either way
}

func (s *Server) planRemoval(ctx context.Context, id string) (*removalPlan, error) {
	h, err := s.d.Store.R.GetHost(ctx, id)
	if err != nil {
		return nil, err
	}
	p := &removalPlan{HostID: h.ID, HostName: h.Name, Parts: []removalPart{},
		Stays: []string{"files DROOL deployed, packages it installed and services it changed",
			"DROOL's firewall rules and firewall tooling (remove them from the host's Firewall page first if you want them gone)"}}
	if deps, err := s.d.Store.R.ListHostsViaHop(ctx, id); err == nil && len(deps) > 0 {
		names := make([]string, len(deps))
		for i, d := range deps {
			names[i] = d.Name
		}
		p.Blocked = "DROOL reaches " + strings.Join(names, ", ") + " through " + h.Name + ": remove those first, or give them another way in."
	}
	if s.d.Agents != nil {
		ids, _ := s.d.Store.R.ListIdentitiesForHost(ctx, id)
		if len(ids) > 0 && ids[0].Status != "revoked" {
			up, err := s.d.Agents.PlanUninstall(ctx, id)
			if err != nil {
				return nil, err
			}
			part := removalPart{What: "shepherd", Label: "The Shepherd", Ready: up.CanUninstall, Method: up.Method}
			for _, it := range up.Items {
				part.Removes = append(part.Removes, it.Path+" ("+it.What+")")
			}
			switch {
			case up.Method == agentsvc.MethodShepherd:
				part.Label += " removes itself"
			case up.Method == agentsvc.MethodSSH:
				part.Label += ", removed over SSH"
			}
			if !up.CanUninstall {
				part.Reason = up.Refusal
			}
			p.Parts = append(p.Parts, part)
		}
	}
	if s.d.SSHAccount != nil {
		cp, err := s.d.SSHAccount.PlanCleanup(ctx, id)
		if err != nil {
			return nil, err
		}
		if cp.Applies {
			part := removalPart{What: "account", Label: "DROOL's SSH account " + cp.User, Ready: cp.OK, Removes: cp.Removes}
			if !cp.OK {
				part.Reason = cp.Message
			}
			p.Parts = append(p.Parts, part)
		}
	}
	p.CanCleanUp = len(p.Parts) > 0 && p.Blocked == ""
	for _, part := range p.Parts {
		if !part.Ready {
			p.CanCleanUp = false
		}
	}
	p.After = "DROOL starts each removal on the host, then forgets it. Removing its own way in, DROOL can't watch the end of it: " +
		"the result says what was started and what DROOL could confirm."
	return p, nil
}

func (s *Server) handlePlanRemoval(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p, err := s.planRemoval(ctx, r.PathValue("id"))
	if errors.Is(err, sql.ErrNoRows) {
		writeProblem(w, r, errNotFound("host"))
		return
	}
	if err != nil {
		writeProblem(w, r, s.inventoryError(err))
		return
	}
	writeJSON(w, http.StatusOK, p)
}

// removalStep is what one part of the cleanup did.
type removalStep struct {
	What    string `json:"what"`
	Outcome string `json:"outcome"`
	Message string `json:"message"`
}

// handleRemoveWithCleanup removes what DROOL installed on the host, then
// the host (hosts.write, a recent sign-in: DROOL removes its own access).
func (s *Server) handleRemoveWithCleanup(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := r.PathValue("id")
	if _, err := s.d.Store.R.GetHost(ctx, id); err != nil {
		writeProblem(w, r, errNotFound("host"))
		return
	}
	release, ok := s.d.Engine.Locks().TryAcquire(id, "host-removal", model.LockExclusive)
	if !ok {
		writeProblem(w, r, errConflict("host_busy", "a job is using this host right now; try again when it has finished"))
		return
	}
	defer release()
	uctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	plan, err := s.planRemoval(uctx, id)
	if err != nil {
		writeProblem(w, r, s.inventoryError(err))
		return
	}
	refuse := func(code, msg string) {
		s.d.Audit.Record(context.WithoutCancel(ctx), audit.Event{Action: "host.remove_cleanup", Result: audit.Denied, TargetType: "host", TargetID: id,
			Details: map[string]any{"name": plan.HostName, "refused": code, "reason": msg}})
		writeProblem(w, r, errConflict(code, msg))
	}
	switch {
	case plan.Blocked != "":
		refuse("jump_host", plan.Blocked)
		return
	case len(plan.Parts) == 0:
		refuse("nothing_to_clean_up", "DROOL installed nothing on "+plan.HostName+" that it can remove (no Shepherd, no SSH account of its own): remove it from DROOL only.")
		return
	case !plan.CanCleanUp:
		var why []string
		for _, p := range plan.Parts {
			if !p.Ready {
				why = append(why, p.Label+": "+p.Reason)
			}
		}
		refuse("cleanup_unavailable", "DROOL can't clean up "+plan.HostName+" now, so nothing was changed. "+strings.Join(why, " ")+
			" You can remove the host from DROOL only, which leaves the host as it is.")
		return
	}
	user := principalFrom(ctx).UserID
	var steps []removalStep
	stop := func(msg string) {
		details := map[string]any{"name": plan.HostName, "steps": steps, "host_deleted": false}
		s.d.Audit.Record(context.WithoutCancel(ctx), audit.Event{Action: "host.remove_cleanup", Result: audit.Failure, TargetType: "host", TargetID: id, Details: details})
		writeJSON(w, http.StatusOK, map[string]any{"outcome": "not_removed", "host_deleted": false, "steps": steps, "message": msg})
	}
	done := func() string {
		var out []string
		for _, st := range steps {
			out = append(out, st.Message)
		}
		return strings.Join(out, " ")
	}
	// The Shepherd first: it needs its session (or the SSH account).
	for _, part := range plan.Parts {
		if part.What != "shepherd" {
			continue
		}
		res, err := s.d.Agents.Uninstall(uctx, id, user, part.Method)
		var refused *agentsvc.ErrUninstallRefused
		switch {
		case errors.As(err, &refused):
			steps = append(steps, removalStep{"shepherd", "refused", refused.Msg})
			stop("The Shepherd couldn't be removed, so DROOL stopped there and kept the host: " + refused.Msg)
			return
		case err != nil:
			writeProblem(w, r, s.internalError(r, err))
			return
		}
		steps = append(steps, removalStep{"shepherd", res.Outcome, res.Message})
		if res.Outcome != "uninstalled" && res.Outcome != "initiated" && res.Outcome != "nothing" {
			stop("DROOL stopped after the Shepherd and kept the host in DROOL: " + res.Message + " Remove the host again to continue, or remove it from DROOL only.")
			return
		}
	}
	for _, part := range plan.Parts {
		if part.What != "account" {
			continue
		}
		res, err := s.d.SSHAccount.Cleanup(uctx, id)
		if err != nil {
			writeProblem(w, r, s.internalError(r, err))
			return
		}
		steps = append(steps, removalStep{"account", res.Outcome, res.Message})
		if res.Outcome != "initiated" {
			what := "DROOL kept the host: the cleanup of its SSH account didn't start. " + res.Message
			if len(steps) > 1 {
				what = "Done so far: " + steps[0].Message + " Not done: the cleanup of DROOL's SSH account didn't start (" + res.Message + "). DROOL kept the host: remove it again, or remove it from DROOL only."
			}
			stop(what)
			return
		}
	}
	// Everything started: DROOL forgets the host.
	var emits []func()
	err = s.inTxAudited(ctx, func(q *db.Queries) (audit.Event, error) {
		h, err := s.d.Inventory.DeleteHostTx(ctx, q, id)
		if err != nil {
			return audit.Event{}, err
		}
		forgot, emit, err := s.forgetSetupCredentials(ctx, q, h.Name, "it was removed from DROOL", h.SshCredentialID.String, h.BecomeCredentialID.String)
		if err != nil {
			return audit.Event{}, err
		}
		emits = emit
		details := map[string]any{"name": h.Name, "address": h.ConnectAddress, "cleanup": steps}
		if len(forgot) > 0 {
			details["setup_credentials_deleted"] = forgot
		}
		return audit.Event{Action: "host.delete", TargetType: "host", TargetID: id, Details: details}, nil
	})
	if err != nil {
		stop(fmt.Sprintf("%s But DROOL couldn't forget the host (%v): remove it from DROOL only to finish.", done(), err))
		return
	}
	for _, e := range emits {
		e()
	}
	writeJSON(w, http.StatusOK, map[string]any{"outcome": "removed", "host_deleted": true, "steps": steps,
		"message": plan.HostName + " is removed from DROOL. " + done()})
}
