package api

import (
	"context"
	"net/http"
	"time"
)

type healthResponse struct {
	Status   string `json:"status"`
	Database string `json:"database"`
}

func (s *Server) checkDB(ctx context.Context) string {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err := s.d.Store.Ping(ctx); err != nil {
		s.log.Error("health: database ping failed", "err", err)
		return "error"
	}
	return "ok"
}

// handleHealth reports liveness and dependency health.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	resp := healthResponse{Status: "healthy", Database: s.checkDB(r.Context())}
	status := http.StatusOK
	if resp.Database != "ok" {
		resp.Status = "unhealthy"
		status = http.StatusServiceUnavailable
	}
	writeJSON(w, status, resp)
}

// handleReady reports whether startup finished and the database is usable.
func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	if !s.ready.Load() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "starting"})
		return
	}
	if s.checkDB(r.Context()) != "ok" {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "database unavailable"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}
