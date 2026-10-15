package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"drool/internal/agentsvc"
	"drool/internal/audit"
	"drool/internal/auth"
	"drool/internal/firewall/fwsvc"
	"drool/internal/firewall/nftables"
	"drool/internal/firewall/policy"
	"drool/internal/model"
	"drool/internal/store/db"
)

// Firewall (M8, DESIGN §15–19, ARCHITECTURE-ALPHA §24): intent-first
// policies, per-host plans, transactions, simulation and Explain Host.

func (s *Server) firewallEnabled(w http.ResponseWriter, r *http.Request) bool {
	if s.d.Firewall == nil {
		writeProblem(w, r, problem(http.StatusNotImplemented, "unavailable", "firewall management is not configured"))
		return false
	}
	return true
}

// firewallError maps service errors to problems.
func (s *Server) firewallError(err error) *Problem {
	var oe *model.OpError
	var re *agentsvc.ReconcileError
	var pe *fwsvc.ProblemsError
	switch {
	case errors.As(err, &pe):
		p := errValidation(pe.Error())
		p.Problems = pe.Problems
		return p
	case errors.Is(err, fwsvc.ErrRevisionConflict):
		return errConflict("revision_conflict", err.Error()+": reload the designer (your draft stays in the YAML view to copy)")
	case errors.Is(err, fwsvc.ErrGroupsForbidden):
		return errForbidden(err.Error())
	case errors.Is(err, fwsvc.ErrNotFound):
		return errNotFound("resource")
	case errors.As(err, &re):
		return errConflict("reconciliation_required", re.Error())
	case errors.As(err, &oe):
		switch oe.Class {
		case model.ClassValidation:
			p := errValidation(oe.Message)
			if oe.Hint != "" {
				p.Detail += " — " + oe.Hint
			}
			return p
		case model.ClassCapability:
			p := problem(http.StatusForbidden, "capability", oe.Message)
			p.ErrorClass = string(oe.Class)
			return p
		}
	}
	return s.inventoryError(err)
}

// --- settings, services, groups ---

func (s *Server) handleFirewallSettings(w http.ResponseWriter, r *http.Request) {
	if !s.firewallEnabled(w, r) {
		return
	}
	writeJSON(w, http.StatusOK, s.d.Firewall.GetSettings(r.Context()))
}

func (s *Server) handleSetFirewallSettings(w http.ResponseWriter, r *http.Request) {
	if !s.firewallEnabled(w, r) {
		return
	}
	var in fwsvc.Settings
	if p := decodeJSON(w, r, &in); p != nil {
		writeProblem(w, r, p)
		return
	}
	ctx := r.Context()
	err := s.inTxAudited(ctx, func(q *db.Queries) (audit.Event, error) {
		if err := s.d.Firewall.SetSettingsTx(ctx, q, in, principalFrom(ctx).UserID); err != nil {
			return audit.Event{}, err
		}
		return audit.Event{Action: "firewall.settings.update", TargetType: "settings", TargetID: "firewall",
			Details: map[string]any{"internal_ranges": in.InternalRanges, "default_timeout_s": in.DefaultTimeoutS}}, nil
	})
	if err != nil {
		writeProblem(w, r, s.firewallError(err))
		return
	}
	s.d.Firewall.Kick()
	// internal_ranges decide which observed addresses are internal (M10).
	go s.d.Firewall.Rescope(context.WithoutCancel(ctx))
	writeJSON(w, http.StatusOK, s.d.Firewall.GetSettings(ctx))
}

type fwServiceJSON struct {
	ID string `json:"id,omitempty"`
	policy.Service
}

func (s *Server) handleListFirewallServices(w http.ResponseWriter, r *http.Request) {
	if !s.firewallEnabled(w, r) {
		return
	}
	svcs, idByName, err := s.d.Firewall.Services(r.Context())
	if err != nil {
		writeProblem(w, r, s.firewallError(err))
		return
	}
	out := make([]fwServiceJSON, len(svcs))
	for i, sv := range svcs {
		if sv.Entries == nil {
			sv.Entries = []policy.Entry{}
		}
		out[i] = fwServiceJSON{ID: idByName[sv.Name], Service: sv}
	}
	writeJSON(w, http.StatusOK, listResponse[fwServiceJSON]{Items: out})
}

func (s *Server) handleCreateFirewallService(w http.ResponseWriter, r *http.Request) {
	if !s.firewallEnabled(w, r) {
		return
	}
	var in policy.Service
	if p := decodeJSON(w, r, &in); p != nil {
		writeProblem(w, r, p)
		return
	}
	ctx := r.Context()
	var id string
	err := s.inTxAudited(ctx, func(q *db.Queries) (audit.Event, error) {
		var err error
		if id, err = s.d.Firewall.CreateServiceTx(ctx, q, in); err != nil {
			return audit.Event{}, err
		}
		return audit.Event{Action: "firewall.service.create", TargetType: "firewall_service", TargetID: id,
			Details: map[string]any{"name": in.Name, "entries": in.Entries}}, nil
	})
	if err != nil {
		writeProblem(w, r, s.firewallError(err))
		return
	}
	in.Builtin = false
	writeJSON(w, http.StatusCreated, fwServiceJSON{ID: id, Service: in})
}

func (s *Server) handleDeleteFirewallService(w http.ResponseWriter, r *http.Request) {
	if !s.firewallEnabled(w, r) {
		return
	}
	ctx := r.Context()
	id := r.PathValue("id")
	err := s.inTxAudited(ctx, func(q *db.Queries) (audit.Event, error) {
		name, err := s.d.Firewall.DeleteServiceTx(ctx, q, id)
		if err != nil {
			return audit.Event{}, err
		}
		return audit.Event{Action: "firewall.service.delete", TargetType: "firewall_service", TargetID: id, Details: map[string]any{"name": name}}, nil
	})
	if err != nil {
		writeProblem(w, r, s.firewallError(err))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type fwGroupJSON struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	TimeoutS int    `json:"rollback_timeout_s"`
}

func (s *Server) handleListFirewallGroups(w http.ResponseWriter, r *http.Request) {
	if !s.firewallEnabled(w, r) {
		return
	}
	groups, err := s.d.Store.R.ListGroups(r.Context())
	if err != nil {
		writeProblem(w, r, s.firewallError(err))
		return
	}
	ts := s.d.Firewall.GroupTimeouts(r.Context())
	out := make([]fwGroupJSON, len(groups))
	for i, g := range groups {
		out[i] = fwGroupJSON{ID: g.ID, Name: g.Name, TimeoutS: ts[g.ID]}
	}
	writeJSON(w, http.StatusOK, listResponse[fwGroupJSON]{Items: out})
}

type fwTimeoutRequest struct {
	TimeoutS int `json:"rollback_timeout_s"`
}

func (s *Server) handleSetFirewallGroup(w http.ResponseWriter, r *http.Request) {
	if !s.firewallEnabled(w, r) {
		return
	}
	var in fwTimeoutRequest
	if p := decodeJSON(w, r, &in); p != nil {
		writeProblem(w, r, p)
		return
	}
	ctx := r.Context()
	id := r.PathValue("id")
	err := s.inTxAudited(ctx, func(q *db.Queries) (audit.Event, error) {
		if err := s.d.Firewall.SetGroupTimeoutTx(ctx, q, id, in.TimeoutS); err != nil {
			return audit.Event{}, err
		}
		return audit.Event{Action: "firewall.group.update", TargetType: "group", TargetID: id, Details: map[string]any{"rollback_timeout_s": in.TimeoutS}}, nil
	})
	if err != nil {
		writeProblem(w, r, s.firewallError(err))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- policies ---

type fwPolicyRequest struct {
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Enabled     *bool             `json:"enabled"`
	Action      string            `json:"action"`
	From        []policy.Selector `json:"from"`
	To          []policy.Selector `json:"to"`
	Services    []string          `json:"services"`
	Position    int64             `json:"position"`
}

func (s *Server) handleListFirewallPolicies(w http.ResponseWriter, r *http.Request) {
	if !s.firewallEnabled(w, r) {
		return
	}
	ps, err := s.d.Firewall.Policies(r.Context())
	if err != nil {
		writeProblem(w, r, s.firewallError(err))
		return
	}
	writeJSON(w, http.StatusOK, listResponse[fwsvc.PolicyRow]{Items: ps})
}

func (s *Server) handleGetFirewallPolicy(w http.ResponseWriter, r *http.Request) {
	if !s.firewallEnabled(w, r) {
		return
	}
	p, err := s.d.Firewall.Policy(r.Context(), r.PathValue("id"))
	if err != nil {
		writeProblem(w, r, s.firewallError(err))
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) savePolicy(w http.ResponseWriter, r *http.Request, id string) {
	var in fwPolicyRequest
	if p := decodeJSON(w, r, &in); p != nil {
		writeProblem(w, r, p)
		return
	}
	pol := policy.Policy{ID: id, Name: in.Name, Description: in.Description, Enabled: in.Enabled == nil || *in.Enabled,
		Action: in.Action, From: in.From, To: in.To, Services: in.Services}
	if pol.Action == "" {
		pol.Action = policy.Allow
	}
	ctx := r.Context()
	var saved fwsvc.PolicyRow
	err := s.inTxAudited(ctx, func(q *db.Queries) (audit.Event, error) {
		var err error
		if saved, err = s.d.Firewall.SavePolicyTx(ctx, q, pol, in.Position, principalFrom(ctx).UserID); err != nil {
			return audit.Event{}, err
		}
		action := "firewall.policy.update"
		if id == "" {
			action = "firewall.policy.create"
		}
		return audit.Event{Action: action, TargetType: "firewall_policy", TargetID: saved.ID, Details: map[string]any{
			"name": saved.Name, "action": saved.Action, "enabled": saved.Enabled, "from": saved.From, "to": saved.To, "services": saved.Services}}, nil
	})
	if err != nil {
		writeProblem(w, r, s.firewallError(err))
		return
	}
	s.d.Firewall.Kick() // after the commit: the converger must see the change
	status := http.StatusOK
	if id == "" {
		status = http.StatusCreated
	}
	writeJSON(w, status, saved)
}

type fwPreviewRequest struct {
	ID     string           `json:"id"`
	Delete bool             `json:"delete"`
	Policy *fwPolicyRequest `json:"policy"`
}

// handleFirewallPolicyPreview shows the hosts a draft policy would change.
func (s *Server) handleFirewallPolicyPreview(w http.ResponseWriter, r *http.Request) {
	if !s.firewallEnabled(w, r) {
		return
	}
	var in fwPreviewRequest
	if p := decodeJSON(w, r, &in); p != nil {
		writeProblem(w, r, p)
		return
	}
	row := fwsvc.PolicyRow{Policy: policy.Policy{ID: in.ID, Enabled: true, Action: policy.Allow}}
	if in.Policy != nil {
		row.Policy = policy.Policy{ID: in.ID, Name: in.Policy.Name, Enabled: in.Policy.Enabled == nil || *in.Policy.Enabled,
			Action: in.Policy.Action, From: in.Policy.From, To: in.Policy.To, Services: in.Policy.Services}
		if row.Action == "" {
			row.Action = policy.Allow
		}
	} else if !in.Delete {
		writeProblem(w, r, errValidation("give a policy, or delete with an id"))
		return
	}
	impact, err := s.d.Firewall.PreviewPolicy(r.Context(), row, in.Delete)
	if err != nil {
		writeProblem(w, r, s.firewallError(err))
		return
	}
	writeJSON(w, http.StatusOK, listResponse[fwsvc.PolicyImpact]{Items: impact})
}

func (s *Server) handleCreateFirewallPolicy(w http.ResponseWriter, r *http.Request) {
	if s.firewallEnabled(w, r) {
		s.savePolicy(w, r, "")
	}
}

func (s *Server) handleUpdateFirewallPolicy(w http.ResponseWriter, r *http.Request) {
	if s.firewallEnabled(w, r) {
		s.savePolicy(w, r, r.PathValue("id"))
	}
}

func (s *Server) handleDeleteFirewallPolicy(w http.ResponseWriter, r *http.Request) {
	if !s.firewallEnabled(w, r) {
		return
	}
	ctx := r.Context()
	id := r.PathValue("id")
	err := s.inTxAudited(ctx, func(q *db.Queries) (audit.Event, error) {
		p, err := s.d.Firewall.DeletePolicyTx(ctx, q, id)
		if err != nil {
			return audit.Event{}, err
		}
		return audit.Event{Action: "firewall.policy.delete", TargetType: "firewall_policy", TargetID: id, Details: map[string]any{"name": p.Name}}, nil
	})
	if err != nil {
		writeProblem(w, r, s.firewallError(err))
		return
	}
	s.d.Firewall.Kick()
	w.WriteHeader(http.StatusNoContent)
}

// --- hosts ---

type fwHostSummary struct {
	HostID      string `json:"host_id"`
	HostName    string `json:"host_name"`
	Mode        string `json:"mode"`
	Outbound    string `json:"outbound"`
	Transport   string `json:"transport"`
	Learning    string `json:"learning"`
	Checked     bool   `json:"checked"`
	Supported   *bool  `json:"supported"`
	Reason      string `json:"reason,omitempty"`
	Enforced    bool   `json:"enforced"`
	Revision    int64  `json:"revision"`
	InSync      bool   `json:"in_sync"`
	Status      string `json:"status"` // not_enforced | in_sync | changes_pending | sets_pending | drift | pending_transaction | conflicts
	Drift       string `json:"drift,omitempty"`
	Conflicts   int    `json:"conflicts"`
	PendingTxID string `json:"pending_transaction_id,omitempty"`
}

func summarize(p *fwsvc.Plan) fwHostSummary {
	out := fwHostSummary{HostID: p.HostID, HostName: p.HostName, Mode: p.Config.Mode, Outbound: p.Config.Outbound, Transport: p.Transport,
		Learning: p.Learning, Checked: p.Eligibility != nil, Enforced: p.Applied.Enforced, Revision: p.Applied.Revision,
		Drift: p.Applied.Drift, Conflicts: len(p.Policy.Conflicts)}
	if p.Eligibility != nil {
		sup := p.Eligibility.Supported
		out.Supported = &sup
		if len(p.Eligibility.Reasons) > 0 {
			out.Reason = p.Eligibility.Reasons[0]
		}
	}
	switch {
	case p.Pending != nil:
		out.Status, out.PendingTxID = "pending_transaction", p.Pending.ID
	case p.Applied.Drift != "":
		out.Status = "drift"
	case !p.Applied.Enforced:
		out.Status = "not_enforced"
	case len(p.Policy.Conflicts) > 0:
		out.Status = "conflicts"
	case p.Applied.RulesChanged:
		out.Status = "changes_pending"
	case p.Applied.SetsPending:
		out.Status = "sets_pending"
	default:
		out.Status, out.InSync = "in_sync", true
	}
	return out
}

func (s *Server) handleListFirewallHosts(w http.ResponseWriter, r *http.Request) {
	if !s.firewallEnabled(w, r) {
		return
	}
	ctx := r.Context()
	hosts, err := s.d.Store.R.ListHosts(ctx)
	if err != nil {
		writeProblem(w, r, s.firewallError(err))
		return
	}
	out := []fwHostSummary{}
	for _, h := range hosts {
		p, err := s.d.Firewall.Plan(ctx, h.ID, false)
		if err != nil {
			continue
		}
		out = append(out, summarize(p))
	}
	prop, _ := s.d.Firewall.Propagation(ctx)
	writeJSON(w, http.StatusOK, map[string]any{"items": out, "propagation": prop})
}

type fwPlanJSON struct {
	*fwsvc.Plan
	Summary fwHostSummary `json:"summary"`
}

func (s *Server) handleGetFirewallHost(w http.ResponseWriter, r *http.Request) {
	if !s.firewallEnabled(w, r) {
		return
	}
	override := r.URL.Query().Get("override") == "true"
	p, err := s.d.Firewall.Plan(r.Context(), r.PathValue("id"), override)
	if err != nil {
		writeProblem(w, r, s.firewallError(err))
		return
	}
	writeJSON(w, http.StatusOK, fwPlanJSON{Plan: p, Summary: summarize(p)})
}

func (s *Server) handleSetFirewallHost(w http.ResponseWriter, r *http.Request) {
	if !s.firewallEnabled(w, r) {
		return
	}
	var in fwsvc.HostConfig
	if p := decodeJSON(w, r, &in); p != nil {
		writeProblem(w, r, p)
		return
	}
	ctx := r.Context()
	id := r.PathValue("id")
	prev := s.d.Firewall.HostConfig(ctx, id)
	err := s.inTxAudited(ctx, func(q *db.Queries) (audit.Event, error) {
		if err := s.d.Firewall.SetHostConfigTx(ctx, q, id, in); err != nil {
			return audit.Event{}, err
		}
		return audit.Event{Action: "firewall.host.update", TargetType: "host", TargetID: id, Details: map[string]any{
			"mode": in.Mode, "previous_mode": prev.Mode, "outbound": in.Outbound, "rollback_timeout_s": in.TimeoutS}}, nil
	})
	if err != nil {
		writeProblem(w, r, s.firewallError(err))
		return
	}
	s.d.Firewall.HostConfigChanged(ctx, id)
	s.handleGetFirewallHost(w, r)
}

type fwDetectJSON struct {
	Environment *nftables.Env        `json:"environment"`
	Eligibility nftables.Eligibility `json:"eligibility"`
}

func (s *Server) handleFirewallDetect(w http.ResponseWriter, r *http.Request) {
	if !s.firewallEnabled(w, r) {
		return
	}
	env, el, err := s.d.Firewall.Detect(r.Context(), r.PathValue("id"))
	if err != nil {
		writeProblem(w, r, s.firewallError(err))
		return
	}
	writeJSON(w, http.StatusOK, fwDetectJSON{Environment: env, Eligibility: el})
}

func (s *Server) handleFirewallSnapshot(w http.ResponseWriter, r *http.Request) {
	if !s.firewallEnabled(w, r) {
		return
	}
	n, err := s.d.Firewall.Snapshot(r.Context(), r.PathValue("id"))
	if err != nil {
		writeProblem(w, r, s.firewallError(err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"flows": n})
}

func (s *Server) handleFirewallObservations(w http.ResponseWriter, r *http.Request) {
	if !s.firewallEnabled(w, r) {
		return
	}
	obs, err := s.d.Firewall.Observations(r.Context(), r.PathValue("id"), 500)
	if err != nil {
		writeProblem(w, r, s.firewallError(err))
		return
	}
	writeJSON(w, http.StatusOK, listResponse[fwsvc.Observation]{Items: obs})
}

func (s *Server) handleFirewallSuggestions(w http.ResponseWriter, r *http.Request) {
	if !s.firewallEnabled(w, r) {
		return
	}
	sg, err := s.d.Firewall.Suggestions(r.Context(), r.PathValue("id"))
	if err != nil {
		writeProblem(w, r, s.firewallError(err))
		return
	}
	writeJSON(w, http.StatusOK, listResponse[fwsvc.Suggestion]{Items: sg})
}

func (s *Server) handleFirewallAdopt(w http.ResponseWriter, r *http.Request) {
	if !s.firewallEnabled(w, r) {
		return
	}
	ctx := r.Context()
	id := r.PathValue("id")
	if err := s.d.Firewall.AdoptHostState(ctx, id); err != nil {
		writeProblem(w, r, s.firewallError(err))
		return
	}
	s.d.Audit.Record(ctx, audit.Event{Action: "firewall.state.adopt", TargetType: "host", TargetID: id})
	w.WriteHeader(http.StatusNoContent)
}

// --- apply ---

type fwApplyRequest struct {
	HostIDs         []string `json:"host_ids"`
	Remove          bool     `json:"remove"`
	OverrideMgmt    bool     `json:"override_mgmt"`
	OverrideTimeout bool     `json:"override_timeout"`
	AllowDrift      bool     `json:"allow_drift"`
	ExpectRulesHash string   `json:"expect_rules_hash"`
	Reason          string   `json:"reason"` // required with any override
}

// handleFirewallApply starts a job with one firewall transaction per host.
func (s *Server) handleFirewallApply(w http.ResponseWriter, r *http.Request) {
	if !s.firewallEnabled(w, r) {
		return
	}
	var in fwApplyRequest
	if p := decodeJSON(w, r, &in); p != nil {
		writeProblem(w, r, p)
		return
	}
	if len(in.HostIDs) == 0 {
		writeProblem(w, r, errValidation("select at least one host"))
		return
	}
	ctx := r.Context()
	override := in.OverrideMgmt || in.OverrideTimeout || in.AllowDrift
	if override {
		if !principalFrom(ctx).Can(auth.PermFirewallOverride) {
			writeProblem(w, r, errForbidden("overrides require firewall.override"))
			return
		}
		if len(in.Reason) < 5 {
			writeProblem(w, r, errValidation("give a reason for the override (it is recorded in the audit log)"))
			return
		}
		// An override can cut DROOL off from the host: a stolen session
		// alone isn't enough (M16).
		if !s.requireReauth(w, r, reauthFirewall) {
			return
		}
		s.d.Audit.Record(ctx, audit.Event{Action: "firewall.override", TargetType: "hosts", TargetID: fmt.Sprint(in.HostIDs),
			Details: map[string]any{"override_mgmt": in.OverrideMgmt, "override_timeout": in.OverrideTimeout, "allow_drift": in.AllowDrift, "reason": in.Reason}})
	}
	// The job must outlive the longest rollback window plus verification.
	timeout := 900
	for _, id := range in.HostIDs {
		if t, _ := s.d.Firewall.EffectiveTimeout(ctx, id); t+600 > timeout {
			timeout = t + 600
		}
	}
	step := model.Operation{Type: model.OpFirewallApply, FirewallApply: &model.FirewallApplyOp{Remove: in.Remove, OverrideMgmt: in.OverrideMgmt,
		OverrideTimeout: in.OverrideTimeout, AllowDrift: in.AllowDrift, ExpectRulesHash: in.ExpectRulesHash, TimeoutS: timeout}}
	name := "Apply firewall policy"
	if in.Remove {
		name = "Remove DROOL firewall policy"
	}
	conc := 1 // one host at a time: a bad policy stops at the first host
	spec := model.JobSpec{Name: name, Steps: []model.Operation{step}, Targets: model.TargetSpec{Hosts: in.HostIDs},
		Strategy: model.Strategy{Concurrency: conc}}
	one := 1
	spec.Strategy.FailureThreshold = &one
	if err := spec.Normalize(); err != nil {
		writeProblem(w, r, errValidation(err.Error()))
		return
	}
	s.submitJob(w, r, spec, "")
}

// --- transactions, simulation, propagation ---

func (s *Server) handleListFirewallTransactions(w http.ResponseWriter, r *http.Request) {
	if !s.firewallEnabled(w, r) {
		return
	}
	limit := 100
	if n, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && n > 0 && n <= 1000 {
		limit = n
	}
	txs, err := s.d.Firewall.Transactions(r.Context(), r.URL.Query().Get("host_id"), limit)
	if err != nil {
		writeProblem(w, r, s.firewallError(err))
		return
	}
	writeJSON(w, http.StatusOK, listResponse[fwsvc.TxRow]{Items: txs})
}

func (s *Server) handleGetFirewallTransaction(w http.ResponseWriter, r *http.Request) {
	if !s.firewallEnabled(w, r) {
		return
	}
	tx, err := s.d.Firewall.Transaction(r.Context(), r.PathValue("id"))
	if err != nil {
		writeProblem(w, r, s.firewallError(err))
		return
	}
	writeJSON(w, http.StatusOK, tx)
}

func (s *Server) handleFirewallSimulate(w http.ResponseWriter, r *http.Request) {
	if !s.firewallEnabled(w, r) {
		return
	}
	var in fwsvc.SimRequest
	if p := decodeJSON(w, r, &in); p != nil {
		writeProblem(w, r, p)
		return
	}
	res, err := s.d.Firewall.Simulate(r.Context(), in)
	if err != nil {
		writeProblem(w, r, s.firewallError(err))
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleFirewallPropagation(w http.ResponseWriter, r *http.Request) {
	if !s.firewallEnabled(w, r) {
		return
	}
	p, err := s.d.Firewall.Propagation(r.Context())
	if err != nil {
		writeProblem(w, r, s.firewallError(err))
		return
	}
	writeJSON(w, http.StatusOK, p)
}

// firewallHostSummary is a host's firewall line for the hosts API.
func (s *Server) firewallHostSummary(ctx context.Context, hostID string) *fwHostSummary {
	if s.d.Firewall == nil {
		return nil
	}
	p, err := s.d.Firewall.Plan(ctx, hostID, false)
	if err != nil {
		return nil
	}
	sum := summarize(p)
	return &sum
}
