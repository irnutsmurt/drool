package api

import (
	"encoding/json"
	"io"
	"net/http"

	"drool/internal/audit"
	"drool/internal/auth"
	"drool/internal/firewall/fwsvc"
	"drool/internal/store/db"
)

// The Firewall Designer: one canonical document, edited as a topology, as
// rules or as YAML (ARCHITECTURE-ALPHA §25).

func (s *Server) handleGetFirewallDocument(w http.ResponseWriter, r *http.Request) {
	if !s.firewallEnabled(w, r) {
		return
	}
	v, err := s.d.Firewall.LoadDocument(r.Context())
	if err != nil {
		writeProblem(w, r, s.firewallError(err))
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) handleCheckFirewallDocument(w http.ResponseWriter, r *http.Request) {
	if !s.firewallEnabled(w, r) {
		return
	}
	var in fwsvc.DocInput
	if p := decodeJSON(w, r, &in); p != nil {
		writeProblem(w, r, p)
		return
	}
	res, err := s.d.Firewall.CheckDocument(r.Context(), in)
	if err != nil {
		writeProblem(w, r, s.firewallError(err))
		return
	}
	writeJSON(w, http.StatusOK, res)
}

type fwSaveDocRequest struct {
	fwsvc.DocInput
	// Revision is the stored revision the draft started from; a different
	// stored revision means someone else saved meanwhile (409).
	Revision string `json:"revision"`
}

func (s *Server) handleSaveFirewallDocument(w http.ResponseWriter, r *http.Request) {
	if !s.firewallEnabled(w, r) {
		return
	}
	var in fwSaveDocRequest
	if p := decodeJSON(w, r, &in); p != nil {
		writeProblem(w, r, p)
		return
	}
	ctx := r.Context()
	pr := principalFrom(ctx)
	var sum *fwsvc.SaveSummary
	err := s.inTxAudited(ctx, func(q *db.Queries) (audit.Event, error) {
		var err error
		sum, err = s.d.Firewall.SaveDocumentTx(ctx, q, in.DocInput, in.Revision, pr.UserID, pr.Can(auth.PermHostsWrite))
		if err != nil {
			return audit.Event{}, err
		}
		return audit.Event{Action: "firewall.document.save", TargetType: "firewall", TargetID: "policy", Details: map[string]any{
			"networks": sum.Networks, "groups": sum.Groups, "services": sum.Services, "policies": sum.Policies,
			"via": map[bool]string{true: "yaml", false: "designer"}[in.YAML != nil]}}, nil
	})
	if err != nil {
		writeProblem(w, r, s.firewallError(err))
		return
	}
	s.d.Firewall.SaveDone()
	v, err := s.d.Firewall.LoadDocument(ctx)
	if err != nil {
		writeProblem(w, r, s.firewallError(err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"document": v.Document, "yaml": v.YAML, "revision": v.Revision, "summary": sum})
}

type fwExplainRequest struct {
	fwsvc.DocInput
	HostID string `json:"host_id"`
}

func (s *Server) handleFirewallDraftExplain(w http.ResponseWriter, r *http.Request) {
	if !s.firewallEnabled(w, r) {
		return
	}
	var in fwExplainRequest
	if p := decodeJSON(w, r, &in); p != nil {
		writeProblem(w, r, p)
		return
	}
	res, err := s.d.Firewall.ExplainDraft(r.Context(), &in.DocInput, in.HostID)
	if err != nil {
		writeProblem(w, r, s.firewallError(err))
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleGetFirewallLayout(w http.ResponseWriter, r *http.Request) {
	if !s.firewallEnabled(w, r) {
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(s.d.Firewall.Layout(r.Context()))
}

func (s *Server) handleSetFirewallLayout(w http.ResponseWriter, r *http.Request) {
	if !s.firewallEnabled(w, r) {
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 600<<10))
	if err != nil {
		writeProblem(w, r, errBadRequest("unreadable body"))
		return
	}
	ctx := r.Context()
	if err := s.d.Store.InTx(ctx, func(q *db.Queries) error {
		return s.d.Firewall.SetLayoutTx(ctx, q, json.RawMessage(body), principalFrom(ctx).UserID)
	}); err != nil {
		writeProblem(w, r, s.firewallError(err))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
