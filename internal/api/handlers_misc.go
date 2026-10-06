package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"runtime"
	"strconv"
	"strings"
	"time"

	"drool/internal/audit"
	"drool/internal/auth"
	"drool/internal/config"
	"drool/internal/store/db"
	"drool/internal/version"
)

// ---- API tokens ----

type tokenJSON struct {
	ID         string  `json:"id"`
	Name       string  `json:"name"`
	CreatedAt  string  `json:"created_at"`
	LastUsedAt *string `json:"last_used_at"`
	ExpiresAt  *string `json:"expires_at"`
}

func toTokenJSON(t db.ApiToken) tokenJSON {
	return tokenJSON{ID: t.ID, Name: t.Name, CreatedAt: msTime(t.CreatedAt),
		LastUsedAt: nullMsTime(t.LastUsedAt), ExpiresAt: nullMsTime(t.ExpiresAt)}
}

func (s *Server) handleListTokens(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r.Context())
	ts, err := s.d.Store.R.ListAPITokens(r.Context(), p.UserID)
	if err != nil {
		writeProblem(w, r, errInternal)
		return
	}
	out := listResponse[tokenJSON]{Items: make([]tokenJSON, 0, len(ts))}
	for _, t := range ts {
		out.Items = append(out.Items, toTokenJSON(t))
	}
	writeJSON(w, http.StatusOK, out)
}

type createTokenRequest struct {
	Name          string `json:"name"`
	ExpiresInDays *int   `json:"expires_in_days"`
}

type createTokenResponse struct {
	tokenJSON
	Token string `json:"token"`
}

func (s *Server) handleCreateToken(w http.ResponseWriter, r *http.Request) {
	var req createTokenRequest
	if p := decodeJSON(w, r, &req); p != nil {
		writeProblem(w, r, p)
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" || len(req.Name) > 100 {
		writeProblem(w, r, errValidation("name must be 1-100 characters"))
		return
	}
	var expires time.Time
	if req.ExpiresInDays != nil {
		if *req.ExpiresInDays < 1 || *req.ExpiresInDays > 3650 {
			writeProblem(w, r, errValidation("expires_in_days must be between 1 and 3650"))
			return
		}
		expires = time.Now().AddDate(0, 0, *req.ExpiresInDays)
	}
	ctx := r.Context()
	p := principalFrom(ctx)
	var res *auth.CreateTokenResult
	err := s.inTxAudited(ctx, func(q *db.Queries) (audit.Event, error) {
		var err error
		res, err = s.d.Auth.CreateAPITokenTx(ctx, q, p.UserID, req.Name, expires)
		if err != nil {
			return audit.Event{}, err
		}
		return audit.Event{Action: "token.create", TargetType: "api_token", TargetID: res.Row.ID,
			Details: map[string]any{"name": req.Name, "expires_in_days": req.ExpiresInDays}}, nil
	})
	if err != nil {
		s.log.Error("create token", "err", err)
		writeProblem(w, r, errInternal)
		return
	}
	writeJSON(w, http.StatusCreated, createTokenResponse{tokenJSON: toTokenJSON(res.Row), Token: res.Token})
}

func (s *Server) handleDeleteToken(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := principalFrom(ctx)
	id := r.PathValue("id")
	err := s.inTxAudited(ctx, func(q *db.Queries) (audit.Event, error) {
		t, err := s.d.Auth.DeleteAPITokenTx(ctx, q, p.UserID, id)
		return audit.Event{Action: "token.delete", TargetType: "api_token", TargetID: id,
			Details: map[string]any{"name": t.Name}}, err
	})
	if errors.Is(err, auth.ErrTokenNotFound) {
		writeProblem(w, r, errNotFound("token"))
		return
	}
	if err != nil {
		writeProblem(w, r, errInternal)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- Audit ----

type auditJSON struct {
	ID        string          `json:"id"`
	Timestamp string          `json:"ts"`
	Actor     auditActorJSON  `json:"actor"`
	Action    string          `json:"action"`
	Target    auditTargetJSON `json:"target"`
	Result    string          `json:"result"`
	SourceIP  string          `json:"source_ip"`
	RequestID string          `json:"request_id"`
	Details   json.RawMessage `json:"details"`
}

type auditActorJSON struct {
	Type string `json:"type"`
	ID   string `json:"id"`
	Name string `json:"name"`
}

type auditTargetJSON struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

func (s *Server) handleListAudit(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit := 50
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 500 {
			writeProblem(w, r, errValidation("limit must be between 1 and 500"))
			return
		}
		limit = n
	}
	var since int64
	if v := q.Get("since"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			writeProblem(w, r, errValidation("since must be an RFC 3339 timestamp"))
			return
		}
		since = t.UnixMilli()
	}
	rows, err := s.d.Store.R.ListAuditEvents(r.Context(), db.ListAuditEventsParams{
		BeforeID:     q.Get("cursor"),
		ActorID:      q.Get("actor"),
		ActionPrefix: q.Get("action"),
		TargetID:     q.Get("target"),
		Since:        since,
		Lim:          int64(limit + 1),
	})
	if err != nil {
		s.log.Error("list audit", "err", err)
		writeProblem(w, r, errInternal)
		return
	}
	out := listResponse[auditJSON]{Items: make([]auditJSON, 0, len(rows))}
	if len(rows) > limit {
		rows = rows[:limit]
		out.NextCursor = rows[limit-1].ID
	}
	for _, e := range rows {
		out.Items = append(out.Items, auditJSON{
			ID: e.ID, Timestamp: msTime(e.Ts),
			Actor:  auditActorJSON{Type: e.ActorType, ID: e.ActorID, Name: e.ActorName},
			Action: e.Action,
			Target: auditTargetJSON{Type: e.TargetType, ID: e.TargetID},
			Result: e.Result, SourceIP: e.SourceIp, RequestID: e.RequestID,
			Details: json.RawMessage(e.DetailsJson),
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// ---- System info ----

type systemInfo struct {
	Version           string         `json:"version"`
	Commit            string         `json:"commit"`
	BuildDate         string         `json:"build_date"`
	GoVersion         string         `json:"go_version"`
	SchemaVersion     int            `json:"schema_version"`
	TLSMode           string         `json:"tls_mode"`
	InsecurePlaintext bool           `json:"insecure_plaintext"`
	ConfigFile        string         `json:"config_file,omitempty"`
	Config            []config.Value `json:"config,omitempty"`
}

func (s *Server) handleSystemInfo(w http.ResponseWriter, r *http.Request) {
	sv, err := s.d.Store.SchemaVersion(r.Context())
	if err != nil {
		writeProblem(w, r, errInternal)
		return
	}
	info := systemInfo{
		Version: version.Version, Commit: version.Commit, BuildDate: version.Date,
		GoVersion: runtime.Version(), SchemaVersion: sv,
		TLSMode: s.d.Config.Server.TLS.Mode, InsecurePlaintext: s.d.Config.IsInsecurePlaintext(),
	}
	if principalFrom(r.Context()).Can(auth.PermSettingsRead) {
		info.ConfigFile = s.d.Config.FilePath
		info.Config = s.d.Config.Values()
	}
	writeJSON(w, http.StatusOK, info)
}
