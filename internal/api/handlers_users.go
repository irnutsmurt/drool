package api

import (
	"database/sql"
	"errors"
	"net/http"
	"time"

	"fleetmgr/internal/audit"
	"fleetmgr/internal/auth"
	"fleetmgr/internal/store/db"
)

type userJSON struct {
	ID          string  `json:"id"`
	Username    string  `json:"username"`
	Role        string  `json:"role"`
	Disabled    bool    `json:"disabled"`
	CreatedAt   string  `json:"created_at,omitempty"`
	UpdatedAt   string  `json:"updated_at,omitempty"`
	LastLoginAt *string `json:"last_login_at"`
}

func toUserJSON(u db.User) userJSON {
	return userJSON{
		ID: u.ID, Username: u.Username, Role: u.Role, Disabled: u.Disabled != 0,
		CreatedAt: msTime(u.CreatedAt), UpdatedAt: msTime(u.UpdatedAt),
		LastLoginAt: nullMsTime(u.LastLoginAt),
	}
}

// msTime formats Unix milliseconds as RFC 3339 UTC.
func msTime(ms int64) string {
	return time.UnixMilli(ms).UTC().Format(time.RFC3339Nano)
}

func nullMsTime(v sql.NullInt64) *string {
	if !v.Valid {
		return nil
	}
	s := msTime(v.Int64)
	return &s
}

type listResponse[T any] struct {
	Items      []T    `json:"items"`
	NextCursor string `json:"next_cursor,omitempty"`
}

func (s *Server) userError(err error) *Problem {
	switch {
	case errors.Is(err, auth.ErrUserNotFound):
		return errNotFound("user")
	case errors.Is(err, auth.ErrUsernameTaken):
		return errConflict("username_taken", err.Error())
	case errors.Is(err, auth.ErrLastAdmin):
		return errConflict("last_admin", err.Error())
	case errors.Is(err, auth.ErrSelfModify):
		return errConflict("self_modify", err.Error())
	case errors.Is(err, auth.ErrInvalidUsername), errors.Is(err, auth.ErrInvalidRole), errors.Is(err, auth.ErrWeakPassword):
		return errValidation(err.Error())
	}
	s.log.Error("user operation", "err", err)
	return errInternal
}

func (s *Server) handleListUsers(w http.ResponseWriter, r *http.Request) {
	us, err := s.d.Store.R.ListUsers(r.Context())
	if err != nil {
		s.log.Error("list users", "err", err)
		writeProblem(w, r, errInternal)
		return
	}
	out := listResponse[userJSON]{Items: make([]userJSON, 0, len(us))}
	for _, u := range us {
		out.Items = append(out.Items, toUserJSON(u))
	}
	writeJSON(w, http.StatusOK, out)
}

type createUserRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Role     string `json:"role"`
}

func (s *Server) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	var req createUserRequest
	if p := decodeJSON(w, r, &req); p != nil {
		writeProblem(w, r, p)
		return
	}
	ctx := r.Context()
	u, err := s.d.Auth.CreateUser(ctx, req.Username, req.Password, req.Role)
	if err != nil {
		_ = s.d.Audit.Record(ctx, audit.Event{Action: "user.create", Result: audit.Failure,
			Details: map[string]any{"username": req.Username, "role": req.Role, "error": err.Error()}})
		writeProblem(w, r, s.userError(err))
		return
	}
	_ = s.d.Audit.Record(ctx, audit.Event{Action: "user.create", TargetType: "user", TargetID: u.ID,
		Details: map[string]any{"username": u.Username, "role": u.Role}})
	writeJSON(w, http.StatusCreated, toUserJSON(u))
}

func (s *Server) handleGetUser(w http.ResponseWriter, r *http.Request) {
	u, err := s.d.Store.R.GetUser(r.Context(), r.PathValue("id"))
	if errors.Is(err, sql.ErrNoRows) {
		writeProblem(w, r, errNotFound("user"))
		return
	}
	if err != nil {
		writeProblem(w, r, errInternal)
		return
	}
	writeJSON(w, http.StatusOK, toUserJSON(u))
}

type updateUserRequest struct {
	Role     *string `json:"role"`
	Disabled *bool   `json:"disabled"`
	Password *string `json:"password"`
}

func (s *Server) handleUpdateUser(w http.ResponseWriter, r *http.Request) {
	var req updateUserRequest
	if p := decodeJSON(w, r, &req); p != nil {
		writeProblem(w, r, p)
		return
	}
	ctx := r.Context()
	id := r.PathValue("id")
	details := map[string]any{}
	if req.Role != nil {
		details["role"] = *req.Role
	}
	if req.Disabled != nil {
		details["disabled"] = *req.Disabled
	}
	if req.Password != nil {
		details["password_changed"] = true
	}
	u, err := s.d.Auth.UpdateUser(ctx, principalFrom(ctx), id, auth.UserUpdate{
		Role: req.Role, Disabled: req.Disabled, Password: req.Password,
	})
	if err != nil {
		details["error"] = err.Error()
		_ = s.d.Audit.Record(ctx, audit.Event{Action: "user.update", Result: audit.Failure,
			TargetType: "user", TargetID: id, Details: details})
		writeProblem(w, r, s.userError(err))
		return
	}
	_ = s.d.Audit.Record(ctx, audit.Event{Action: "user.update", TargetType: "user", TargetID: id, Details: details})
	writeJSON(w, http.StatusOK, toUserJSON(u))
}

func (s *Server) handleDeleteUser(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := r.PathValue("id")
	if err := s.d.Auth.DeleteUser(ctx, principalFrom(ctx), id); err != nil {
		_ = s.d.Audit.Record(ctx, audit.Event{Action: "user.delete", Result: audit.Failure,
			TargetType: "user", TargetID: id, Details: map[string]any{"error": err.Error()}})
		writeProblem(w, r, s.userError(err))
		return
	}
	_ = s.d.Audit.Record(ctx, audit.Event{Action: "user.delete", TargetType: "user", TargetID: id})
	w.WriteHeader(http.StatusNoContent)
}
