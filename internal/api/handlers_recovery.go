package api

import (
	"errors"
	"net/http"

	"drool/internal/agentsvc"
	"drool/internal/audit"
	"drool/internal/recovery"
)

// Restore reconciliation (M16, ARCHITECTURE-ALPHA §33.1): Settings → System
// shows the latest restore and its hosts; accepting a host's state is a
// decision about which side's state counts, so it requires a recent
// sign-in.

func (s *Server) recoveryEnabled(w http.ResponseWriter, r *http.Request) bool {
	if s.d.Recovery == nil {
		writeProblem(w, r, errNotFound("restore reconciliation"))
		return false
	}
	return true
}

func (s *Server) handleRestoreStatus(w http.ResponseWriter, r *http.Request) {
	if !s.recoveryEnabled(w, r) {
		return
	}
	st, err := s.d.Recovery.Status(r.Context())
	if err != nil {
		writeProblem(w, r, errInternal)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (s *Server) handleHostReconciliation(w http.ResponseWriter, r *http.Request) {
	if !s.recoveryEnabled(w, r) {
		return
	}
	rec, err := s.d.Recovery.HostStatus(r.Context(), r.PathValue("id"))
	if err != nil {
		writeProblem(w, r, errInternal)
		return
	}
	if rec == nil {
		writeProblem(w, r, errNotFound("reconciliation"))
		return
	}
	writeJSON(w, http.StatusOK, rec)
}

func recoveryProblem(err error) *Problem {
	var se *recovery.ErrState
	switch {
	case errors.Is(err, recovery.ErrNoRestore):
		return errConflict("no_restore", err.Error())
	case errors.As(err, &se):
		return errConflict("reconciliation_state", se.Msg)
	}
	return errInternal
}

func (s *Server) handleAcceptHostState(w http.ResponseWriter, r *http.Request) {
	if !s.recoveryEnabled(w, r) {
		return
	}
	ctx := r.Context()
	if err := s.d.Recovery.Accept(ctx, r.PathValue("id"), audit.ActorFrom(ctx)); err != nil {
		writeProblem(w, r, recoveryProblem(err))
		return
	}
	s.handleHostReconciliation(w, r)
}

func (s *Server) handleRetryHostReconciliation(w http.ResponseWriter, r *http.Request) {
	if !s.recoveryEnabled(w, r) {
		return
	}
	if err := s.d.Recovery.Retry(r.Context(), r.PathValue("id")); err != nil {
		writeProblem(w, r, recoveryProblem(err))
		return
	}
	s.handleHostReconciliation(w, r)
}

func (s *Server) handleFinishRestore(w http.ResponseWriter, r *http.Request) {
	if !s.recoveryEnabled(w, r) {
		return
	}
	ctx := r.Context()
	if err := s.d.Recovery.Finish(ctx, audit.ActorFrom(ctx)); err != nil {
		writeProblem(w, r, recoveryProblem(err))
		return
	}
	s.handleRestoreStatus(w, r)
}

func (s *Server) handleListUnknownAgents(w http.ResponseWriter, r *http.Request) {
	if !s.agentsEnabled(w, r) {
		return
	}
	list, err := s.d.Agents.UnknownAgents(r.Context())
	if err != nil {
		writeProblem(w, r, errInternal)
		return
	}
	writeJSON(w, http.StatusOK, listResponse[agentsvc.UnknownAgent]{Items: list})
}

func (s *Server) handleDismissUnknownAgent(w http.ResponseWriter, r *http.Request) {
	if !s.agentsEnabled(w, r) {
		return
	}
	ctx := r.Context()
	id := r.PathValue("id")
	if err := s.d.Agents.DismissUnknownAgent(ctx, id); err != nil {
		if errors.Is(err, agentsvc.ErrNoUnknownAgent) {
			writeProblem(w, r, errNotFound("unknown agent"))
			return
		}
		writeProblem(w, r, errInternal)
		return
	}
	s.d.Audit.Record(ctx, audit.Event{Action: "agent.unknown.dismiss", TargetType: "agent_identity", TargetID: id})
	w.WriteHeader(http.StatusNoContent)
}
