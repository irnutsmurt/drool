package api

import (
	"errors"
	"net/http"
	"strings"

	"drool/internal/audit"
)

// The audit chain's integrity and the remote audit copy (ARCHITECTURE-ALPHA
// §33.4–33.5).

type verificationJSON struct {
	audit.Verification
	StartedAt  string  `json:"started_at"`
	FinishedAt *string `json:"finished_at"`
}

func toVerificationJSON(v *audit.Verification) *verificationJSON {
	if v == nil {
		return nil
	}
	out := &verificationJSON{Verification: *v, StartedAt: msTime(v.StartedAt)}
	if v.FinishedAt > 0 {
		t := msTime(v.FinishedAt)
		out.FinishedAt = &t
	}
	if out.Problems == nil {
		out.Problems = []audit.Problem{}
	}
	return out
}

type acknowledgementJSON struct {
	VerificationID string `json:"verification_id"`
	Problem        string `json:"problem"`
	By             string `json:"by"`
	At             string `json:"at"`
	Note           string `json:"note"`
}

type integrityJSON struct {
	// State: ok (the newest check passed and nothing unacknowledged failed),
	// failed, acknowledged (a known break an administrator looked at),
	// unverified (never checked yet), running.
	State           string               `json:"state"`
	HeadSeq         int64                `json:"head_seq"`
	LegacyRecords   int64                `json:"legacy_records"`
	Running         *verificationJSON    `json:"running"`
	Last            *verificationJSON    `json:"last"`
	LastOK          *verificationJSON    `json:"last_ok"`
	LastFailed      *verificationJSON    `json:"last_failed"`
	Acknowledgement *acknowledgementJSON `json:"acknowledgement"`
}

func (s *Server) integrityEnabled(w http.ResponseWriter, r *http.Request) bool {
	if s.d.Integrity == nil {
		writeProblem(w, r, errNotFound("audit integrity"))
		return false
	}
	return true
}

func (s *Server) integrityState(r *http.Request) (*integrityJSON, error) {
	st, err := s.d.Integrity.Status(r.Context())
	if err != nil {
		return nil, err
	}
	out := &integrityJSON{HeadSeq: st.HeadSeq, LegacyRecords: st.LegacyRecords, Running: toVerificationJSON(st.Running),
		Last: toVerificationJSON(st.Last), LastOK: toVerificationJSON(st.LastOK), LastFailed: toVerificationJSON(st.LastFailed)}
	if a := st.Acknowledged; a != nil {
		out.Acknowledgement = &acknowledgementJSON{VerificationID: a.VerificationID, Problem: a.Problem, By: a.By, At: msTime(a.At), Note: a.Note}
	}
	broken, acked := st.Broken()
	switch {
	case broken && !acked:
		out.State = "failed"
	case broken:
		out.State = "acknowledged"
	case st.Last == nil && st.Running != nil:
		out.State = "running"
	case st.Last == nil:
		out.State = "unverified"
	case st.Last.Result == "error":
		out.State = "unverified"
	default:
		out.State = "ok"
	}
	return out, nil
}

func (s *Server) handleAuditIntegrity(w http.ResponseWriter, r *http.Request) {
	if !s.integrityEnabled(w, r) {
		return
	}
	out, err := s.integrityState(r)
	if err != nil {
		s.log.Error("audit integrity", "err", err)
		writeProblem(w, r, errInternal)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

type startVerificationResponse struct {
	ID string `json:"id"`
}

func (s *Server) handleVerifyAudit(w http.ResponseWriter, r *http.Request) {
	if !s.integrityEnabled(w, r) {
		return
	}
	ctx := r.Context()
	p := principalFrom(ctx)
	id, err := s.d.Integrity.Start(ctx, "manual", p.Username)
	if errors.Is(err, audit.ErrVerifying) {
		writeProblem(w, r, errConflict("verifying", err.Error()))
		return
	}
	if err != nil {
		s.log.Error("start audit verification", "err", err)
		writeProblem(w, r, errInternal)
		return
	}
	_ = s.d.Audit.Record(ctx, audit.Event{Action: "audit.verify.request", TargetType: "audit", TargetID: id})
	writeJSON(w, http.StatusAccepted, startVerificationResponse{ID: id})
}

type acknowledgeRequest struct {
	Note string `json:"note"`
}

func (s *Server) handleAcknowledgeAuditBreak(w http.ResponseWriter, r *http.Request) {
	if !s.integrityEnabled(w, r) {
		return
	}
	var req acknowledgeRequest
	if p := decodeJSON(w, r, &req); p != nil {
		writeProblem(w, r, p)
		return
	}
	note := strings.TrimSpace(req.Note)
	if note == "" || len(note) > 2000 {
		writeProblem(w, r, errValidation("say what you found (up to 2000 characters): the note is kept with the acknowledgement"))
		return
	}
	ctx := r.Context()
	p := principalFrom(ctx)
	a, err := s.d.Integrity.Acknowledge(ctx, p.Username, note)
	if err != nil {
		writeProblem(w, r, errConflict("nothing_to_acknowledge", err.Error()))
		return
	}
	_ = s.d.Audit.Record(ctx, audit.Event{Action: "audit.integrity.acknowledge", TargetType: "audit", TargetID: a.VerificationID,
		Details: map[string]any{"problem": a.Problem, "note": note}})
	out, err := s.integrityState(r)
	if err != nil {
		writeProblem(w, r, errInternal)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// --- remote audit sink ---

func (s *Server) sinkEnabled(w http.ResponseWriter, r *http.Request) bool {
	if s.d.AuditSink == nil {
		writeProblem(w, r, errNotFound("audit sink"))
		return false
	}
	return true
}

func (s *Server) handleGetAuditSink(w http.ResponseWriter, r *http.Request) {
	if !s.sinkEnabled(w, r) {
		return
	}
	st, err := s.d.AuditSink.Status(r.Context())
	if err != nil {
		s.log.Error("audit sink status", "err", err)
		writeProblem(w, r, errInternal)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

type auditSinkRequest struct {
	Enabled     bool    `json:"enabled"`
	URL         string  `json:"url"`
	CAPEM       string  `json:"ca_pem"`
	HeaderName  string  `json:"header_name"`
	HeaderValue *string `json:"header_value"` // omitted: keep; "": remove
	StartFrom   string  `json:"start_from"`   // all (default) | now
}

func (req *auditSinkRequest) config() (audit.SinkConfig, *Problem) {
	if req.StartFrom != "" && req.StartFrom != "all" && req.StartFrom != "now" {
		return audit.SinkConfig{}, errValidation("start_from must be all or now")
	}
	if req.HeaderValue != nil && *req.HeaderValue != "" && req.HeaderName == "" {
		return audit.SinkConfig{}, errValidation("a header value needs a header name")
	}
	return audit.SinkConfig{Enabled: req.Enabled, URL: req.URL, CAPEM: req.CAPEM, HeaderName: req.HeaderName,
		HeaderValue: req.HeaderValue, StartFrom: req.StartFrom}, nil
}

func (s *Server) sinkProblem(w http.ResponseWriter, r *http.Request, err error) {
	var ve *audit.ValidationError
	if errors.As(err, &ve) {
		writeProblem(w, r, errValidation(ve.Msg))
		return
	}
	s.log.Error("audit sink", "err", err)
	writeProblem(w, r, errInternal)
}

func (s *Server) handlePutAuditSink(w http.ResponseWriter, r *http.Request) {
	if !s.sinkEnabled(w, r) {
		return
	}
	var req auditSinkRequest
	if p := decodeJSON(w, r, &req); p != nil {
		writeProblem(w, r, p)
		return
	}
	cfg, prob := req.config()
	if prob != nil {
		writeProblem(w, r, prob)
		return
	}
	ctx := r.Context()
	p := principalFrom(ctx)
	before, _ := s.d.AuditSink.Status(ctx)
	if err := s.d.AuditSink.Configure(ctx, cfg, p.UserID); err != nil {
		s.sinkProblem(w, r, err)
		return
	}
	details := map[string]any{"enabled": cfg.Enabled, "url": displayHost(cfg.URL), "header_name": cfg.HeaderName,
		"custom_ca": strings.TrimSpace(cfg.CAPEM) != "", "start_from": cfg.StartFrom}
	if cfg.HeaderValue != nil {
		details["header_value_changed"] = true // never the value
	}
	if before != nil && before.Enabled && !cfg.Enabled {
		details["disabled"] = true
	}
	_ = s.d.Audit.Record(ctx, audit.Event{Action: "audit.sink.update", TargetType: "audit", TargetID: "sink", Details: details})
	st, err := s.d.AuditSink.Status(ctx)
	if err != nil {
		writeProblem(w, r, errInternal)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

// displayHost is the scheme and host of a URL (audit records never carry
// paths or queries, which can hold tokens).
func displayHost(raw string) string {
	u, err := audit.CheckURL(raw)
	if err != nil {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

type sinkTestResponse struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

func (s *Server) handleTestAuditSink(w http.ResponseWriter, r *http.Request) {
	if !s.sinkEnabled(w, r) {
		return
	}
	var req auditSinkRequest
	var cfg *audit.SinkConfig
	if r.ContentLength != 0 {
		if p := decodeJSON(w, r, &req); p != nil {
			writeProblem(w, r, p)
			return
		}
		if req.URL != "" {
			c, prob := req.config()
			if prob != nil {
				writeProblem(w, r, prob)
				return
			}
			cfg = &c
		}
	}
	ctx := r.Context()
	err := s.d.AuditSink.Test(ctx, cfg)
	var ve *audit.ValidationError
	if errors.As(err, &ve) {
		writeProblem(w, r, errValidation(ve.Msg))
		return
	}
	res := sinkTestResponse{OK: err == nil}
	if err != nil {
		res.Error = err.Error()
	}
	_ = s.d.Audit.Record(ctx, audit.Event{Action: "audit.sink.test", TargetType: "audit", TargetID: "sink",
		Result: map[bool]audit.Result{true: audit.Success, false: audit.Failure}[res.OK], Details: map[string]any{"error": res.Error}})
	writeJSON(w, http.StatusOK, res)
}
