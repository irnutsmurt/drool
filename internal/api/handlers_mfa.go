package api

import (
	"errors"
	"net/http"
	"time"

	"drool/internal/audit"
	"drool/internal/auth"
	"drool/internal/store/db"
)

// Re-authentication and two-factor authentication (ARCHITECTURE-ALPHA
// §33.3).

type loginMFARequest struct {
	Challenge    string `json:"challenge"`
	Code         string `json:"code"`
	RecoveryCode string `json:"recovery_code"`
}

type mfaChallengeResponse struct {
	MFARequired bool     `json:"mfa_required"`
	Challenge   string   `json:"challenge"`
	Methods     []string `json:"methods"`
}

func (s *Server) loginFailed(w http.ResponseWriter, r *http.Request, username string, err error) {
	ctx := r.Context()
	var lf *auth.LoginFailure
	if !errors.As(err, &lf) {
		s.log.Error("login", "err", err)
		writeProblem(w, r, errInternal)
		return
	}
	actor := audit.Actor{Type: audit.ActorAnonymous, ID: lf.UserID, Name: username}
	if err := s.d.Audit.Record(ctx, audit.Event{
		Action: "auth.login", Result: audit.Failure, Actor: &actor,
		TargetType: "user", TargetID: lf.UserID,
		Details: map[string]any{"reason": lf.Reason},
	}); err != nil {
		s.log.Error("audit login failure", "err", err)
	}
	s.secLog.Warn("login failed", "username", username, "reason", lf.Reason,
		"source_ip", clientIPFrom(ctx), "request_id", requestIDFrom(ctx))
	switch {
	case errors.Is(err, auth.ErrRateLimited):
		writeProblem(w, r, problem(http.StatusTooManyRequests, "rate_limited", err.Error()))
	case errors.Is(err, auth.ErrInvalidCode):
		writeProblem(w, r, problem(http.StatusUnauthorized, "invalid_code", err.Error()))
	case errors.Is(err, auth.ErrChallengeExpired):
		writeProblem(w, r, problem(http.StatusUnauthorized, "challenge_expired", err.Error()))
	default:
		writeProblem(w, r, problem(http.StatusUnauthorized, "invalid_credentials", auth.ErrInvalidCredentials.Error()))
	}
}

// loggedIn audits a completed sign-in and sets the session cookie.
func (s *Server) loggedIn(w http.ResponseWriter, r *http.Request, res *auth.LoginResult) {
	p := res.Principal
	ctx := withPrincipal(r.Context(), p)
	details := map[string]any{}
	if res.SecondFactor != "" {
		details["second_factor"] = res.SecondFactor
		if res.SecondFactor == "recovery_code" {
			if st, err := s.d.Auth.MFAStatus(ctx, nil, p.UserID); err == nil {
				details["recovery_codes_left"] = st.RecoveryRemaining
			}
		}
	}
	if err := s.d.Audit.Record(ctx, audit.Event{Action: "auth.login", TargetType: "user", TargetID: p.UserID, Details: details}); err != nil {
		// Do not grant a session we could not audit.
		_ = s.d.Auth.Logout(ctx, p)
		s.log.Error("audit login", "err", err)
		writeProblem(w, r, errInternal)
		return
	}
	s.setSessionCookie(w, res.Cookie, res.ExpiresAt)
	writeJSON(w, http.StatusOK, s.meFor(r, p))
}

func (s *Server) handleLoginMFA(w http.ResponseWriter, r *http.Request) {
	var req loginMFARequest
	if p := decodeJSON(w, r, &req); p != nil {
		writeProblem(w, r, p)
		return
	}
	ctx := r.Context()
	if (req.Code == "") == (req.RecoveryCode == "") {
		writeProblem(w, r, errValidation("enter either a code from your authenticator or a recovery code"))
		return
	}
	res, err := s.d.Auth.CompleteLogin(ctx, req.Challenge, req.Code, req.RecoveryCode, clientIPFrom(ctx), r.UserAgent())
	if err != nil {
		s.loginFailed(w, r, "", err)
		return
	}
	s.loggedIn(w, r, res)
}

type reauthRequest struct {
	Password     string `json:"password"`
	Code         string `json:"code"`
	RecoveryCode string `json:"recovery_code"`
}

type reauthResponse struct {
	ReauthUntil string `json:"reauth_until"`
}

func (s *Server) handleReauth(w http.ResponseWriter, r *http.Request) {
	var req reauthRequest
	if p := decodeJSON(w, r, &req); p != nil {
		writeProblem(w, r, p)
		return
	}
	ctx := r.Context()
	p := principalFrom(ctx)
	method, err := s.d.Auth.Reauthenticate(ctx, p, req.Password, req.Code, req.RecoveryCode, clientIPFrom(ctx))
	if err != nil {
		var lf *auth.LoginFailure
		if !errors.As(err, &lf) {
			s.log.Error("reauth", "err", err)
			writeProblem(w, r, errInternal)
			return
		}
		_ = s.d.Audit.Record(ctx, audit.Event{Action: "auth.reauth", Result: audit.Failure, TargetType: "user", TargetID: p.UserID,
			Details: map[string]any{"reason": lf.Reason}})
		s.secLog.Warn("re-authentication failed", "user_id", p.UserID, "reason", lf.Reason, "source_ip", clientIPFrom(ctx))
		switch {
		case errors.Is(err, auth.ErrRateLimited):
			writeProblem(w, r, problem(http.StatusTooManyRequests, "rate_limited", err.Error()))
		case errors.Is(err, auth.ErrInvalidCode):
			writeProblem(w, r, problem(http.StatusForbidden, "invalid_code", "the password or the authenticator code is not right"))
		default:
			writeProblem(w, r, problem(http.StatusForbidden, "invalid_credentials", "the password is not right"))
		}
		return
	}
	_ = s.d.Audit.Record(ctx, audit.Event{Action: "auth.reauth", TargetType: "user", TargetID: p.UserID, Details: map[string]any{"method": method}})
	writeJSON(w, http.StatusOK, reauthResponse{ReauthUntil: msTime(p.ReauthAt + s.d.Auth.ReauthWindow().Milliseconds())})
}

type mfaStatusJSON struct {
	Enabled                bool    `json:"enabled"`
	Pending                bool    `json:"pending"`
	EnabledAt              *string `json:"enabled_at"`
	RecoveryCodesRemaining int64   `json:"recovery_codes_remaining"`
}

func toMFAStatusJSON(st auth.MFAStatus) mfaStatusJSON {
	out := mfaStatusJSON{Enabled: st.Enabled, Pending: st.Pending, RecoveryCodesRemaining: st.RecoveryRemaining}
	if st.Enabled {
		t := st.EnabledAt.UTC().Format(time.RFC3339)
		out.EnabledAt = &t
	}
	return out
}

func (s *Server) handleGetMFA(w http.ResponseWriter, r *http.Request) {
	st, err := s.d.Auth.MFAStatus(r.Context(), nil, principalFrom(r.Context()).UserID)
	if err != nil {
		writeProblem(w, r, errInternal)
		return
	}
	writeJSON(w, http.StatusOK, toMFAStatusJSON(st))
}

type mfaEnrollResponse struct {
	Secret string `json:"secret"`
	URI    string `json:"otpauth_uri"`
	Issuer string `json:"issuer"`
}

func (s *Server) mfaError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, auth.ErrMFAAlreadyEnabled), errors.Is(err, auth.ErrMFANotEnabled), errors.Is(err, auth.ErrMFANoEnrollment):
		writeProblem(w, r, errConflict("mfa_state", err.Error()))
	case errors.Is(err, auth.ErrInvalidCode):
		writeProblem(w, r, problem(http.StatusUnprocessableEntity, "invalid_code", err.Error()))
	default:
		s.log.Error("two-factor", "err", err)
		writeProblem(w, r, errInternal)
	}
}

func (s *Server) handleEnrollMFA(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := principalFrom(ctx)
	en, err := s.d.Auth.BeginMFAEnrollment(ctx, p.UserID, p.Username)
	if err != nil {
		s.mfaError(w, r, err)
		return
	}
	// Never the secret: only that enrollment started.
	_ = s.d.Audit.Record(ctx, audit.Event{Action: "auth.mfa.enroll", TargetType: "user", TargetID: p.UserID})
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, mfaEnrollResponse{Secret: en.Secret, URI: en.URI, Issuer: auth.MFAIssuer})
}

type mfaConfirmRequest struct {
	Code string `json:"code"`
}

type recoveryCodesResponse struct {
	RecoveryCodes []string `json:"recovery_codes"`
}

func (s *Server) handleConfirmMFA(w http.ResponseWriter, r *http.Request) {
	var req mfaConfirmRequest
	if p := decodeJSON(w, r, &req); p != nil {
		writeProblem(w, r, p)
		return
	}
	ctx := r.Context()
	p := principalFrom(ctx)
	var codes []string
	err := s.inTxAudited(ctx, func(q *db.Queries) (audit.Event, error) {
		var err error
		if codes, err = s.d.Auth.ConfirmMFAEnrollmentTx(ctx, q, p.UserID, req.Code); err != nil {
			return audit.Event{}, err
		}
		// Like a password change: other sessions sign in again, now with
		// the second factor.
		return audit.Event{Action: "auth.mfa.enable", TargetType: "user", TargetID: p.UserID},
			q.DeleteUserSessionsExcept(ctx, db.DeleteUserSessionsExceptParams{UserID: p.UserID, TokenHash: p.SessionHash})
	})
	if err != nil {
		if errors.Is(err, auth.ErrInvalidCode) {
			_ = s.d.Audit.Record(ctx, audit.Event{Action: "auth.mfa.enable", Result: audit.Failure, TargetType: "user", TargetID: p.UserID,
				Details: map[string]any{"reason": "bad_code"}})
		}
		s.mfaError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, recoveryCodesResponse{RecoveryCodes: codes})
}

func (s *Server) handleRegenerateRecoveryCodes(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := principalFrom(ctx)
	var codes []string
	err := s.inTxAudited(ctx, func(q *db.Queries) (audit.Event, error) {
		var err error
		codes, err = s.d.Auth.RegenerateRecoveryCodesTx(ctx, q, p.UserID)
		return audit.Event{Action: "auth.mfa.recovery_codes", TargetType: "user", TargetID: p.UserID}, err
	})
	if err != nil {
		s.mfaError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, recoveryCodesResponse{RecoveryCodes: codes})
}

func (s *Server) handleDisableMFA(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := principalFrom(ctx)
	err := s.inTxAudited(ctx, func(q *db.Queries) (audit.Event, error) {
		had, err := s.d.Auth.DisableMFATx(ctx, q, p.UserID)
		if err == nil && !had {
			err = auth.ErrMFANotEnabled
		}
		if err != nil {
			return audit.Event{}, err
		}
		return audit.Event{Action: "auth.mfa.disable", TargetType: "user", TargetID: p.UserID},
			q.DeleteUserSessionsExcept(ctx, db.DeleteUserSessionsExceptParams{UserID: p.UserID, TokenHash: p.SessionHash})
	})
	if err != nil {
		s.mfaError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleResetUserMFA turns another user's two-factor off (lost phone and
// recovery codes). Their sessions end; they sign in with the password and
// can set up a new authenticator.
func (s *Server) handleResetUserMFA(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := r.PathValue("id")
	u, err := s.d.Store.R.GetUser(ctx, id)
	if err != nil {
		writeProblem(w, r, errNotFound("user"))
		return
	}
	err = s.inTxAudited(ctx, func(q *db.Queries) (audit.Event, error) {
		had, err := s.d.Auth.DisableMFATx(ctx, q, id)
		if err == nil && !had {
			err = auth.ErrMFANotEnabled
		}
		if err != nil {
			return audit.Event{}, err
		}
		return audit.Event{Action: "user.mfa_reset", TargetType: "user", TargetID: id, Details: map[string]any{"username": u.Username}},
			q.DeleteUserSessions(ctx, id)
	})
	if err != nil {
		s.mfaError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
