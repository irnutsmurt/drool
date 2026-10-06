package api

import (
	"errors"
	"net/http"

	"drool/internal/audit"
	"drool/internal/auth"
	"drool/internal/store/db"
)

type setupStatusResponse struct {
	SetupRequired bool `json:"setup_required"`
	CodeAvailable bool `json:"code_available"`
}

func (s *Server) handleSetupStatus(w http.ResponseWriter, r *http.Request) {
	required, has, err := s.d.Auth.SetupStatus(r.Context())
	if err != nil {
		s.log.Error("setup status", "err", err)
		writeProblem(w, r, errInternal)
		return
	}
	writeJSON(w, http.StatusOK, setupStatusResponse{SetupRequired: required, CodeAvailable: has})
}

type setupClaimRequest struct {
	Code     string `json:"code"`
	Username string `json:"username"`
	Password string `json:"password"`
}

// handleSetupClaim creates the first administrator using a one-time setup
// code. The code itself is never logged or audited.
func (s *Server) handleSetupClaim(w http.ResponseWriter, r *http.Request) {
	var req setupClaimRequest
	if p := decodeJSON(w, r, &req); p != nil {
		writeProblem(w, r, p)
		return
	}
	ctx := r.Context()
	var emit func()
	res, err := s.d.Auth.ClaimSetup(ctx, req.Code, req.Username, req.Password, clientIPFrom(ctx), r.UserAgent(),
		func(q *db.Queries, u db.User) error {
			actor := audit.Actor{Type: audit.ActorUser, ID: u.ID, Name: u.Username}
			var err error
			emit, err = s.d.Audit.RecordTx(ctx, q, audit.Event{
				Action: "setup.claim", TargetType: "user", TargetID: u.ID, Actor: &actor,
				Details: map[string]any{"username": u.Username},
			})
			return err
		})
	if err != nil {
		var cf *auth.ClaimFailure
		reason := "validation"
		if errors.As(err, &cf) {
			reason = cf.Reason
		}
		actor := audit.Actor{Type: audit.ActorAnonymous, Name: req.Username}
		_ = s.d.Audit.Record(ctx, audit.Event{
			Action: "setup.claim", Result: audit.Failure, Actor: &actor,
			Details: map[string]any{"reason": reason, "username": req.Username},
		})
		if reason == "invalid_code" || reason == "rate_limited" {
			s.secLog.Warn("setup claim failed", "reason", reason, "source_ip", clientIPFrom(ctx), "request_id", requestIDFrom(ctx))
		}
		switch {
		case errors.Is(err, auth.ErrSetupRateLimited):
			writeProblem(w, r, problem(http.StatusTooManyRequests, "rate_limited", err.Error()))
		case errors.Is(err, auth.ErrSetupClosed):
			writeProblem(w, r, errConflict("setup_closed", err.Error()))
		case errors.Is(err, auth.ErrSetupCodeInvalid):
			writeProblem(w, r, problem(http.StatusForbidden, "invalid_setup_code", err.Error()))
		default:
			writeProblem(w, r, s.userError(err))
		}
		return
	}
	emit()
	s.log.Info("first administrator created via setup", "user_id", res.Principal.UserID, "username", res.Principal.Username)
	s.setSessionCookie(w, res.Cookie, res.ExpiresAt)
	writeJSON(w, http.StatusCreated, s.meFor(r.WithContext(withPrincipal(ctx, res.Principal)), res.Principal))
}
