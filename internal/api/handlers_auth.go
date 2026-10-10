package api

import (
	"net/http"
	"time"

	"drool/internal/audit"
	"drool/internal/auth"
	"drool/internal/store/db"
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
	MFAEnabled  bool     `json:"mfa_enabled"`
	// ReauthUntil: until when high-impact actions go through without asking
	// for the password again (sessions only).
	ReauthUntil   *string `json:"reauth_until,omitempty"`
	ReauthWindowS int     `json:"reauth_window_s"`
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
		s.loginFailed(w, r, req.Username, err)
		return
	}
	if res.MFARequired {
		// The password was right; the session waits for the second factor.
		actor := audit.Actor{Type: audit.ActorAnonymous, ID: res.UserID, Name: req.Username}
		_ = s.d.Audit.Record(ctx, audit.Event{Action: "auth.login.password", Actor: &actor, TargetType: "user", TargetID: res.UserID,
			Details: map[string]any{"next": "second_factor"}})
		writeJSON(w, http.StatusOK, mfaChallengeResponse{MFARequired: true, Challenge: res.Challenge, Methods: []string{"totp", "recovery_code"}})
		return
	}
	s.loggedIn(w, r, res)
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
	me := meResponse{User: uj, Permissions: perms, AuthKind: string(p.Kind), CSRFToken: p.CSRFToken,
		ReauthWindowS: int(s.d.Auth.ReauthWindow() / time.Second)}
	if st, err := s.d.Auth.MFAStatus(r.Context(), nil, p.UserID); err == nil {
		me.MFAEnabled = st.Enabled
		uj.MFAEnabled = st.Enabled
		me.User = uj
	}
	if p.Kind == auth.KindSession && s.d.Auth.RecentlyAuthenticated(p) {
		t := msTime(p.ReauthAt + s.d.Auth.ReauthWindow().Milliseconds())
		me.ReauthUntil = &t
	}
	return me
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
