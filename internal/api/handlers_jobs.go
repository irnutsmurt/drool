package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"drool/internal/audit"
	"drool/internal/engine"
	"drool/internal/model"
	"drool/internal/store/db"
)

type jobJSON struct {
	ID          string          `json:"id"`
	Name        string          `json:"name"`
	Status      string          `json:"status"`
	PauseReason string          `json:"pause_reason"`
	Spec        json.RawMessage `json:"spec"`
	RerunOf     *string         `json:"rerun_of"`
	CreatedBy   string          `json:"created_by"`
	CreatedAt   string          `json:"created_at"`
	StartedAt   *string         `json:"started_at"`
	FinishedAt  *string         `json:"finished_at"`
	Counts      map[string]int  `json:"counts"`
	HostCount   int             `json:"host_count"`
}

func (s *Server) toJobJSON(r *http.Request, j db.Job) jobJSON {
	counts, _ := engine.Counts(r.Context(), s.d.Store.R, j.ID)
	total := 0
	for _, n := range counts {
		total += n
	}
	creator := j.CreatedBy
	if u, err := s.d.Store.R.GetUser(r.Context(), j.CreatedBy); err == nil {
		creator = u.Username
	}
	return jobJSON{ID: j.ID, Name: j.Name, Status: j.Status, PauseReason: j.PauseReason,
		Spec: json.RawMessage(j.SpecJson), RerunOf: strPtr(j.RerunOf.String), CreatedBy: creator,
		CreatedAt: msTime(j.CreatedAt), StartedAt: nullMsTime(j.StartedAt), FinishedAt: nullMsTime(j.FinishedAt),
		Counts: counts, HostCount: total}
}

type jobHostJSON struct {
	HostID       string  `json:"host_id"`
	HostName     string  `json:"host_name"`
	State        string  `json:"state"`
	CurrentStep  int64   `json:"current_step"`
	Attempt      int64   `json:"attempt"`
	ExitCode     *int64  `json:"exit_code"`
	ErrorClass   string  `json:"error_class"`
	ErrorMessage string  `json:"error_message"`
	ErrorHint    string  `json:"error_hint"`
	StartedAt    *string `json:"started_at"`
	FinishedAt   *string `json:"finished_at"`
}

func toJobHostJSON(h db.JobHost) jobHostJSON {
	out := jobHostJSON{HostID: h.HostID, HostName: h.HostName, State: h.State, CurrentStep: h.CurrentStep,
		Attempt: h.Attempt, ErrorClass: h.ErrorClass, ErrorMessage: h.ErrorMessage, ErrorHint: h.ErrorHint,
		StartedAt: nullMsTime(h.StartedAt), FinishedAt: nullMsTime(h.FinishedAt)}
	if h.ExitCode.Valid {
		out.ExitCode = &h.ExitCode.Int64
	}
	return out
}

func (s *Server) jobError(err error) *Problem {
	var ve *engine.ValidationError
	switch {
	case errors.As(err, &ve):
		return errValidation(err.Error())
	case errors.Is(err, engine.ErrJobNotFound):
		return errNotFound("job")
	case errors.Is(err, engine.ErrJobNotActive), errors.Is(err, engine.ErrJobNotPaused):
		return errConflict("job_state", err.Error())
	}
	s.log.Error("job operation", "err", err)
	return errInternal
}

type previewRequest struct {
	Targets model.TargetSpec `json:"targets"`
}

type previewResponse struct {
	Hosts    []engine.TargetHost `json:"hosts"`
	Warnings []string            `json:"warnings"`
}

func (s *Server) handlePreviewJob(w http.ResponseWriter, r *http.Request) {
	var req previewRequest
	if p := decodeJSON(w, r, &req); p != nil {
		writeProblem(w, r, p)
		return
	}
	hosts, warnings, err := engine.ResolveTargets(r.Context(), s.d.Store.R, req.Targets)
	if err != nil {
		writeProblem(w, r, s.jobError(err))
		return
	}
	if hosts == nil {
		hosts = []engine.TargetHost{}
	}
	if warnings == nil {
		warnings = []string{}
	}
	writeJSON(w, http.StatusOK, previewResponse{Hosts: hosts, Warnings: warnings})
}

func (s *Server) handleCreateJob(w http.ResponseWriter, r *http.Request) {
	var spec model.JobSpec
	if p := decodeJSON(w, r, &spec); p != nil {
		writeProblem(w, r, p)
		return
	}
	s.submitJob(w, r, spec, "")
}

func (s *Server) submitJob(w http.ResponseWriter, r *http.Request, spec model.JobSpec, rerunOf string) {
	ctx := r.Context()
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if len(key) > 200 {
		writeProblem(w, r, errValidation("Idempotency-Key is too long"))
		return
	}
	var sub *engine.Submission
	err := s.inTxAudited(ctx, func(q *db.Queries) (audit.Event, error) {
		var err error
		sub, err = s.d.Engine.SubmitTx(ctx, q, spec, principalFrom(ctx).UserID, key, rerunOf)
		if err != nil {
			return audit.Event{}, err
		}
		steps := make([]string, len(spec.Steps))
		for i, st := range spec.Steps {
			steps[i] = st.Type
		}
		return audit.Event{Action: "job.create", TargetType: "job", TargetID: sub.Job.ID,
			Details: map[string]any{"name": sub.Job.Name, "steps": steps, "hosts": len(sub.Hosts), "rerun_of": rerunOf}}, nil
	})
	if errors.Is(err, engine.ErrJobIdempotent) {
		writeJSON(w, http.StatusOK, s.toJobJSON(r, sub.Job))
		return
	}
	if err != nil {
		writeProblem(w, r, s.jobError(err))
		return
	}
	s.d.Engine.Launch(sub.Job.ID)
	writeJSON(w, http.StatusCreated, s.toJobJSON(r, sub.Job))
}

func (s *Server) handleListJobs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit := 50
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 200 {
			writeProblem(w, r, errValidation("limit must be between 1 and 200"))
			return
		}
		limit = n
	}
	rows, err := s.d.Store.R.ListJobs(r.Context(), db.ListJobsParams{
		BeforeID: q.Get("cursor"), Status: q.Get("status"), HostID: q.Get("host"), Lim: int64(limit + 1)})
	if err != nil {
		writeProblem(w, r, errInternal)
		return
	}
	out := listResponse[jobJSON]{Items: make([]jobJSON, 0, len(rows))}
	if len(rows) > limit {
		rows = rows[:limit]
		out.NextCursor = rows[limit-1].ID
	}
	for _, j := range rows {
		out.Items = append(out.Items, s.toJobJSON(r, j))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) getJob(w http.ResponseWriter, r *http.Request) (db.Job, bool) {
	j, err := s.d.Store.R.GetJob(r.Context(), r.PathValue("id"))
	if errors.Is(err, sql.ErrNoRows) {
		writeProblem(w, r, errNotFound("job"))
		return j, false
	}
	if err != nil {
		writeProblem(w, r, errInternal)
		return j, false
	}
	return j, true
}

func (s *Server) handleGetJob(w http.ResponseWriter, r *http.Request) {
	if j, ok := s.getJob(w, r); ok {
		writeJSON(w, http.StatusOK, s.toJobJSON(r, j))
	}
}

func (s *Server) handleListJobHosts(w http.ResponseWriter, r *http.Request) {
	j, ok := s.getJob(w, r)
	if !ok {
		return
	}
	hs, err := s.d.Store.R.ListJobHosts(r.Context(), j.ID)
	if err != nil {
		writeProblem(w, r, errInternal)
		return
	}
	out := listResponse[jobHostJSON]{Items: make([]jobHostJSON, 0, len(hs))}
	for _, h := range hs {
		out.Items = append(out.Items, toJobHostJSON(h))
	}
	writeJSON(w, http.StatusOK, out)
}

type jobStepJSON struct {
	Index       int64   `json:"index"`
	Type        string  `json:"type"`
	State       string  `json:"state"`
	Attempt     int64   `json:"attempt"`
	ExitCode    *int64  `json:"exit_code"`
	StdoutBytes int64   `json:"stdout_bytes"`
	StderrBytes int64   `json:"stderr_bytes"`
	Truncated   bool    `json:"truncated"`
	StartedAt   string  `json:"started_at"`
	FinishedAt  *string `json:"finished_at"`
}

type jobHostDetailJSON struct {
	jobHostJSON
	Steps []jobStepJSON `json:"steps"`
}

func (s *Server) handleGetJobHost(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	h, err := s.d.Store.R.GetJobHost(ctx, db.GetJobHostParams{JobID: r.PathValue("id"), HostID: r.PathValue("hostId")})
	if errors.Is(err, sql.ErrNoRows) {
		writeProblem(w, r, errNotFound("job host"))
		return
	} else if err != nil {
		writeProblem(w, r, errInternal)
		return
	}
	steps, err := s.d.Store.R.ListJobSteps(ctx, db.ListJobStepsParams{JobID: h.JobID, HostID: h.HostID})
	if err != nil {
		writeProblem(w, r, errInternal)
		return
	}
	out := jobHostDetailJSON{jobHostJSON: toJobHostJSON(h), Steps: make([]jobStepJSON, 0, len(steps))}
	for _, st := range steps {
		js := jobStepJSON{Index: st.StepIndex, Type: st.OpType, State: st.State, Attempt: st.Attempt,
			StdoutBytes: st.StdoutBytes, StderrBytes: st.StderrBytes, Truncated: st.Truncated != 0,
			StartedAt: msTime(st.StartedAt), FinishedAt: nullMsTime(st.FinishedAt)}
		if st.ExitCode.Valid {
			js.ExitCode = &st.ExitCode.Int64
		}
		out.Steps = append(out.Steps, js)
	}
	writeJSON(w, http.StatusOK, out)
}

type outputSegment struct {
	Seq    int64  `json:"seq"`
	Stream string `json:"stream"`
	Text   string `json:"text"`
	Ts     string `json:"ts"`
}

type outputResponse struct {
	Segments []outputSegment `json:"segments"`
	NextSeq  int64           `json:"next_seq"`
	More     bool            `json:"more"`
}

// handleJobOutput returns output segments as JSON (default) or the raw
// bytes of selected streams with ?format=text.
func (s *Server) handleJobOutput(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	q := r.URL.Query()
	step, _ := strconv.Atoi(q.Get("step"))
	after, _ := strconv.ParseInt(q.Get("after"), 10, 64)
	stream := q.Get("stream") // "", stdout, stderr
	params := db.ListJobOutputParams{JobID: r.PathValue("id"), HostID: r.PathValue("hostId"),
		StepIndex: int64(step), Seq: after, Limit: 500}
	if q.Get("format") == "text" {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s-%s-step%d.log"`,
			params.JobID, params.HostID, step))
		for {
			rows, err := s.d.Store.R.ListJobOutput(ctx, params)
			if err != nil || len(rows) == 0 {
				return
			}
			for _, row := range rows {
				if stream == "" || stream == streamOf(row.Stream) {
					w.Write(row.Chunk)
				}
				params.Seq = row.Seq
			}
		}
	}
	rows, err := s.d.Store.R.ListJobOutput(ctx, params)
	if err != nil {
		writeProblem(w, r, errInternal)
		return
	}
	out := outputResponse{Segments: make([]outputSegment, 0, len(rows)), NextSeq: after, More: len(rows) == int(params.Limit)}
	for _, row := range rows {
		out.NextSeq = row.Seq
		if stream != "" && stream != streamOf(row.Stream) {
			continue
		}
		out.Segments = append(out.Segments, outputSegment{Seq: row.Seq, Stream: streamOf(row.Stream),
			Text: string(row.Chunk), Ts: msTime(row.Ts)})
	}
	writeJSON(w, http.StatusOK, out)
}

func streamOf(n int64) string {
	if n == 2 {
		return "stderr"
	}
	return "stdout"
}

// handleJobEvents streams job events as server-sent events. Reconnecting
// clients send Last-Event-ID; if events were missed, a "reset" event tells
// the client to refetch state over REST.
func (s *Server) handleJobEvents(w http.ResponseWriter, r *http.Request) {
	j, ok := s.getJob(w, r)
	if !ok {
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeProblem(w, r, errInternal)
		return
	}
	lastID, _ := strconv.ParseInt(r.Header.Get("Last-Event-ID"), 10, 64)
	sub := s.d.Engine.Hub().Subscribe(j.ID, lastID)
	defer sub.Cancel()

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	send := func(ev engine.Event) {
		fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", ev.ID, ev.Type, ev.Data)
	}
	if !sub.Complete {
		fmt.Fprint(w, "event: reset\ndata: {}\n\n")
	}
	for _, ev := range sub.Replay {
		send(ev)
	}
	if sub.Ended {
		fmt.Fprint(w, "event: end\ndata: {}\n\n")
		flusher.Flush()
		return
	}
	flusher.Flush()
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-heartbeat.C:
			fmt.Fprint(w, ": keepalive\n\n")
			flusher.Flush()
		case ev, open := <-sub.Events:
			if !open {
				// Job ended (the end event was delivered) or we fell behind.
				fmt.Fprint(w, "event: reset\ndata: {}\n\n")
				flusher.Flush()
				return
			}
			send(ev)
			flusher.Flush()
			if ev.Type == "end" {
				return
			}
		}
	}
}

func (s *Server) handleCancelJob(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := r.PathValue("id")
	if err := s.d.Engine.Cancel(ctx, id); err != nil {
		writeProblem(w, r, s.jobError(err))
		return
	}
	_ = s.d.Audit.Record(ctx, audit.Event{Action: "job.cancel", TargetType: "job", TargetID: id})
	w.WriteHeader(http.StatusAccepted)
}

func (s *Server) handleResumeJob(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := r.PathValue("id")
	if err := s.d.Engine.Resume(ctx, id); err != nil {
		writeProblem(w, r, s.jobError(err))
		return
	}
	_ = s.d.Audit.Record(ctx, audit.Event{Action: "job.resume", TargetType: "job", TargetID: id})
	w.WriteHeader(http.StatusAccepted)
}

type rerunRequest struct {
	Hosts json.RawMessage `json:"hosts"` // "failed" | "interrupted" | "all" | ["host_id", ...]
}

var rerunStates = map[string][]string{
	"failed":      {engine.HostFailed, engine.HostTimedOut, engine.HostCancelled, engine.HostSkipped, "interrupted"},
	"interrupted": {"interrupted"},
}

func (s *Server) handleRerunJob(w http.ResponseWriter, r *http.Request) {
	j, ok := s.getJob(w, r)
	if !ok {
		return
	}
	var req rerunRequest
	if p := decodeJSON(w, r, &req); p != nil {
		writeProblem(w, r, p)
		return
	}
	var spec model.JobSpec
	if err := json.Unmarshal([]byte(j.SpecJson), &spec); err != nil {
		writeProblem(w, r, errInternal)
		return
	}
	hs, err := s.d.Store.R.ListJobHosts(r.Context(), j.ID)
	if err != nil {
		writeProblem(w, r, errInternal)
		return
	}
	var selector string
	var explicit []string
	if err := json.Unmarshal(req.Hosts, &selector); err != nil {
		if err := json.Unmarshal(req.Hosts, &explicit); err != nil {
			writeProblem(w, r, errValidation(`hosts must be "failed", "interrupted", "all" or a list of host ids`))
			return
		}
	}
	var targets []string
	for _, h := range hs {
		switch {
		case explicit != nil:
			for _, id := range explicit {
				if id == h.HostID {
					targets = append(targets, h.HostID)
				}
			}
		case selector == "all":
			targets = append(targets, h.HostID)
		default:
			states, ok := rerunStates[selector]
			if !ok {
				writeProblem(w, r, errValidation(`hosts must be "failed", "interrupted", "all" or a list of host ids`))
				return
			}
			for _, st := range states {
				if h.State == st {
					targets = append(targets, h.HostID)
				}
			}
		}
	}
	if len(targets) == 0 {
		writeProblem(w, r, errValidation("no hosts match"))
		return
	}
	spec.Targets = model.TargetSpec{Hosts: targets}
	spec.Name = strings.TrimPrefix(spec.Name, "Re-run: ")
	spec.Name = "Re-run: " + spec.Name
	s.submitJob(w, r, spec, j.ID)
}
