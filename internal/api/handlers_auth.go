package api

import (
	"errors"
	"net/http"

	"fleetmgr/internal/audit"
	"fleetmgr/internal/auth"
	"fleetmgr/internal/store/db"
)

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type meResponse struct {
	User        userJSON `json:"user"`
	Permissions []string `json:"permissions"`
	AuthKind    string   `json:"auth_kind"`
	CSRFToken   string   `json:"csrf_token,omitempty"`
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if p := decodeJSON(w, r, &req); p != nil {
		writeProblem(w, r, p)
		return
	}
	ctx := r.Context()
	res, err := s.d.Auth.Login(ctx, req.Username, req.Password, clientIPFrom(ctx), r.UserAgent())
	if err != nil {
		var lf *auth.LoginFailure
		if !errors.As(err, &lf) {
			s.log.Error("login", "err", err)
			writeProblem(w, r, errInternal)
			return
		}
		actor := audit.Actor{Type: audit.ActorAnonymous, ID: lf.UserID, Name: req.Username}
		if err := s.d.Audit.Record(ctx, audit.Event{
			Action: "auth.login", Result: audit.Failure, Actor: &actor,
			TargetType: "user", TargetID: lf.UserID,
			Details: map[string]any{"reason": lf.Reason},
		}); err != nil {
			s.log.Error("audit login failure", "err", err)
		}
		s.secLog.Warn("login failed", "username", req.Username, "reason", lf.Reason,
			"source_ip", clientIPFrom(ctx), "request_id", requestIDFrom(ctx))
		if errors.Is(err, auth.ErrRateLimited) {
			writeProblem(w, r, problem(http.StatusTooManyRequests, "rate_limited", err.Error()))
			return
		}
		writeProblem(w, r, problem(http.StatusUnauthorized, "invalid_credentials", auth.ErrInvalidCredentials.Error()))
		return
	}

	p := res.Principal
	ctx = withPrincipal(ctx, p)
	if err := s.d.Audit.Record(ctx, audit.Event{Action: "auth.login", TargetType: "user", TargetID: p.UserID}); err != nil {
		// Do not grant a session we could not audit.
		_ = s.d.Auth.Logout(ctx, p)
		s.log.Error("audit login", "err", err)
		writeProblem(w, r, errInternal)
		return
	}
	s.setSessionCookie(w, res.Cookie, res.ExpiresAt)
	writeJSON(w, http.StatusOK, s.meFor(r, p))
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r.Context())
	if err := s.d.Auth.Logout(r.Context(), p); err != nil {
		s.log.Error("logout", "err", err)
	}
	_ = s.d.Audit.Record(r.Context(), audit.Event{Action: "auth.logout", TargetType: "user", TargetID: p.UserID})
	s.clearSessionCookie(w)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r.Context())
	writeJSON(w, http.StatusOK, s.meFor(r, p))
}

func (s *Server) meFor(r *http.Request, p *auth.Principal) meResponse {
	u, err := s.d.Store.R.GetUser(r.Context(), p.UserID)
	uj := userJSON{ID: p.UserID, Username: p.Username, Role: p.Role}
	if err == nil {
		uj = toUserJSON(u)
	}
	perms := []string{}
	for _, perm := range auth.PermissionsFor(p.Role) {
		perms = append(perms, string(perm))
	}
	return meResponse{User: uj, Permissions: perms, AuthKind: string(p.Kind), CSRFToken: p.CSRFToken}
}

type changePasswordRequest struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
}

func (s *Server) handleChangeOwnPassword(w http.ResponseWriter, r *http.Request) {
	var req changePasswordRequest
	if p := decodeJSON(w, r, &req); p != nil {
		writeProblem(w, r, p)
		return
	}
	ctx := r.Context()
	p := principalFrom(ctx)
	u, err := s.d.Store.R.GetUser(ctx, p.UserID)
	if err != nil {
		writeProblem(w, r, errInternal)
		return
	}
	ok, err := auth.VerifyPassword(u.PasswordHash, req.CurrentPassword)
	if err != nil || !ok {
		_ = s.d.Audit.Record(ctx, audit.Event{Action: "user.password_change", Result: audit.Failure,
			TargetType: "user", TargetID: p.UserID, Details: map[string]any{"reason": "bad_current_password"}})
		writeProblem(w, r, problem(http.StatusForbidden, "invalid_credentials", "current password is incorrect"))
		return
	}
	up := auth.UserUpdate{Password: &req.NewPassword}
	err = up.Prepare()
	if err == nil {
		err = s.inTxAudited(ctx, func(q *db.Queries) (audit.Event, error) {
			_, err := s.d.Auth.UpdateUserTx(ctx, q, p, p.UserID, up)
			return audit.Event{Action: "user.password_change", TargetType: "user", TargetID: p.UserID}, err
		})
	}
	if err != nil {
		writeProblem(w, r, s.userError(err))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
