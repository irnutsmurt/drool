// Package engine runs jobs: it resolves targets, schedules hosts under
// concurrency limits and host locks, executes typed operations with retries,
// and records per-host state and output (ARCHITECTURE-ALPHA §11).
package engine

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"strings"
	"sync"
	"time"

	"drool/internal/audit"
	"drool/internal/driver"
	"drool/internal/engine/lock"
	"drool/internal/ids"
	"drool/internal/model"
	"drool/internal/store"
	"drool/internal/store/db"
)

// Job statuses.
const (
	JobPending     = "pending"
	JobRunning     = "running"
	JobPaused      = "paused"
	JobCompleted   = "completed"
	JobCancelled   = "cancelled"
	JobInterrupted = "interrupted"
)

// Host states.
const (
	HostQueued      = "queued"
	HostWaitingLock = "waiting_lock"
	HostConnecting  = "connecting"
	HostRunning     = "running"
	HostSucceeded   = "succeeded"
	HostFailed      = "failed"
	HostTimedOut    = "timed_out"
	HostCancelled   = "cancelled"
	HostSkipped     = "skipped"
)

var (
	ErrJobNotFound   = errors.New("job not found")
	ErrJobNotActive  = errors.New("job is not running")
	ErrJobNotPaused  = errors.New("job is not paused")
	ErrJobIdempotent = errors.New("job already submitted with this idempotency key")
)

// Executor chooses how a job reaches a host (its management path: SSH,
// or the agent with an optional SSH fallback; internal/mgmt).
type Executor interface {
	Prepare(ctx context.Context, hostID string) (Path, error)
}

// Path runs one job's operations on one host. Steps of a host run in
// order, so implementations may keep per-step state (e.g. which transport
// an operation was dispatched on, so a retry never switches transports
// after the operation may have started).
type Path interface {
	Execute(ctx context.Context, op model.Operation, out driver.OutputSink, started func()) (*driver.OpResult, error)
	// Transport is the path used last ("ssh", "agent") and a note such as
	// "SSH fallback: the agent is not connected".
	Transport() (string, string)
	// JumpHostID is the SSH jump host, for per-jump concurrency ("" none).
	JumpHostID() string
	// Secrets are values to redact from output (passwords).
	Secrets() []string
}

// Resolver provides host facts for job conditions.
type Resolver interface {
	HostOS(ctx context.Context, hostID string) (model.HostOS, error)
}

// Config tunes the engine.
type Config struct {
	GlobalConcurrency  int
	PerJumpConcurrency int
	OutputCap          int64
	LockWait           time.Duration
	RetryAttempts      int           // total tries for retryable failures
	RetryDelay         time.Duration // first backoff delay
	BreakerThreshold   int           // consecutive same-class auth failures that pause a job
	RetentionDays      int
}

func (c *Config) defaults() {
	if c.GlobalConcurrency <= 0 {
		c.GlobalConcurrency = 20
	}
	if c.PerJumpConcurrency <= 0 {
		c.PerJumpConcurrency = 10
	}
	if c.OutputCap <= 0 {
		c.OutputCap = 10 << 20
	}
	if c.LockWait <= 0 {
		c.LockWait = 30 * time.Minute
	}
	if c.RetryAttempts <= 0 {
		c.RetryAttempts = 3
	}
	if c.RetryDelay <= 0 {
		c.RetryDelay = 5 * time.Second
	}
	if c.BreakerThreshold <= 0 {
		c.BreakerThreshold = 5
	}
}

// Engine schedules and runs jobs. One engine per controller process.
type Engine struct {
	st    *store.Store
	inv   Resolver
	exec  Executor
	locks *lock.Manager
	hub   *Hub
	audit *audit.Writer
	log   *slog.Logger
	cfg   Config
	now   func() time.Time

	global  chan struct{}
	jumpMu  sync.Mutex
	jumpSem map[string]chan struct{}

	mu          sync.Mutex
	runs        map[string]*jobRun
	hostChanged func(hostID string)

	base      context.Context // never cancelled: jobs are not aborted on shutdown
	pruneStop context.CancelFunc
	pruneCtx  context.Context
	wg        sync.WaitGroup
}

// New creates an engine. Call Start before submitting jobs.
func New(st *store.Store, inv Resolver, exec Executor, aw *audit.Writer, log *slog.Logger, cfg Config) *Engine {
	cfg.defaults()
	pruneCtx, pruneStop := context.WithCancel(context.Background())
	return &Engine{
		st: st, inv: inv, exec: exec, locks: lock.New(), hub: NewHub(), audit: aw, log: log, cfg: cfg,
		now: time.Now, global: make(chan struct{}, cfg.GlobalConcurrency), jumpSem: map[string]chan struct{}{},
		runs: map[string]*jobRun{}, base: context.Background(), pruneCtx: pruneCtx, pruneStop: pruneStop,
	}
}

// Hub exposes job events for SSE.
func (e *Engine) Hub() *Hub { return e.hub }

// Saturated reports whether every global job worker is busy; background
// polling yields while it is (decision D6).
func (e *Engine) Saturated() bool { return len(e.global) >= cap(e.global) }

// Locks exposes the host lock manager (shared with the status poller later).
func (e *Engine) Locks() *lock.Manager { return e.locks }

// Start marks work left over from a previous run as interrupted (outcome
// unknown, never resubmitted automatically) and starts retention pruning.
func (e *Engine) Start(ctx context.Context) error {
	now := e.now().UnixMilli()
	var jobs []string
	err := e.st.InTx(ctx, func(q *db.Queries) error {
		var err error
		if jobs, err = q.InterruptActiveJobs(ctx, sql.NullInt64{Int64: now, Valid: true}); err != nil {
			return err
		}
		if err := q.InterruptActiveJobHosts(ctx, sql.NullInt64{Int64: now, Valid: true}); err != nil {
			return err
		}
		return q.InterruptActiveJobSteps(ctx, sql.NullInt64{Int64: now, Valid: true})
	})
	if err != nil {
		return err
	}
	for _, id := range jobs {
		e.log.Warn("job interrupted by controller restart", "job_id", id)
		_ = e.audit.Record(ctx, audit.Event{Action: "job.interrupted", Actor: &audit.System, Result: audit.Failure,
			TargetType: "job", TargetID: id})
	}
	if e.cfg.RetentionDays > 0 {
		go e.pruneLoop()
	}
	return nil
}

// Stop ends background maintenance. Running jobs are deliberately not
// cancelled: their remote commands keep running, and hosts still in progress
// are recorded as interrupted (outcome unknown) on the next start
// (ARCHITECTURE-ALPHA §11.6).
func (e *Engine) Stop() {
	e.pruneStop()
}

// Wait blocks until all launched jobs have finished (tests).
func (e *Engine) Wait() { e.wg.Wait() }

func (e *Engine) pruneLoop() {
	t := time.NewTicker(6 * time.Hour)
	defer t.Stop()
	for {
		cutoff := e.now().AddDate(0, 0, -e.cfg.RetentionDays).UnixMilli()
		if n, err := e.st.W.DeleteFinishedJobsBefore(e.pruneCtx, sql.NullInt64{Int64: cutoff, Valid: true}); err != nil {
			e.log.Error("prune jobs", "err", err)
		} else if n > 0 {
			e.log.Info("pruned old jobs", "deleted", n, "retention_days", e.cfg.RetentionDays)
		}
		select {
		case <-e.pruneCtx.Done():
			return
		case <-t.C:
		}
	}
}

// Submission is a validated job ready to launch.
type Submission struct {
	Job   db.Job
	Hosts []TargetHost
}

// SubmitTx validates spec, resolves its targets and stores the job and its
// host rows in q's transaction. Call Launch after the transaction commits.
func (e *Engine) SubmitTx(ctx context.Context, q *db.Queries, spec model.JobSpec, userID, idemKey, rerunOf string) (*Submission, error) {
	if err := spec.Normalize(); err != nil {
		return nil, &ValidationError{Msg: err.Error()}
	}
	if idemKey != "" {
		if j, err := q.GetJobByIdempotencyKey(ctx, db.GetJobByIdempotencyKeyParams{
			CreatedBy: userID, IdempotencyKey: sql.NullString{String: idemKey, Valid: true}}); err == nil {
			return &Submission{Job: j}, ErrJobIdempotent
		} else if !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
	}
	hosts, _, err := ResolveTargets(ctx, q, spec.Targets)
	if err != nil {
		return nil, err
	}
	if len(hosts) == 0 {
		return nil, invalid("the selected targets match no hosts")
	}
	specJSON, err := json.Marshal(spec)
	if err != nil {
		return nil, err
	}
	now := e.now().UnixMilli()
	j := db.Job{ID: ids.New(ids.Job), Name: spec.Name, SpecJson: string(specJSON), SpecVersion: model.SpecVersion,
		IdempotencyKey: sql.NullString{String: idemKey, Valid: idemKey != ""}, Status: JobPending,
		RerunOf: sql.NullString{String: rerunOf, Valid: rerunOf != ""}, CreatedBy: userID, CreatedAt: now}
	if err := q.InsertJob(ctx, db.InsertJobParams{ID: j.ID, Name: j.Name, SpecJson: j.SpecJson,
		SpecVersion: j.SpecVersion, IdempotencyKey: j.IdempotencyKey, RerunOf: j.RerunOf,
		CreatedBy: userID, CreatedAt: now}); err != nil {
		return nil, err
	}
	for i, h := range hosts {
		if err := q.InsertJobHost(ctx, db.InsertJobHostParams{JobID: j.ID, HostID: h.ID, HostName: h.Name, Position: int64(i)}); err != nil {
			return nil, err
		}
	}
	return &Submission{Job: j, Hosts: hosts}, nil
}

// jobRun is the in-memory state of a running job.
type jobRun struct {
	id        string
	createdBy string
	spec      model.JobSpec
	ctx       context.Context
	cancel    context.CancelFunc

	mu              sync.Mutex
	paused          bool
	resume          chan struct{}
	cancelRequested bool
	failures        int
	thresholdHit    bool
	breakerClass    model.ErrorClass
	breakerCount    int
}

// Launch starts running a submitted job in the background.
func (e *Engine) Launch(jobID string) {
	e.mu.Lock()
	if _, ok := e.runs[jobID]; ok {
		e.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(e.base)
	jr := &jobRun{id: jobID, ctx: ctx, cancel: cancel}
	e.runs[jobID] = jr
	e.mu.Unlock()
	e.hub.Publish(jobID, "job", map[string]string{"id": jobID, "status": JobPending})
	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		defer func() {
			e.mu.Lock()
			delete(e.runs, jobID)
			e.mu.Unlock()
			cancel()
		}()
		if err := e.runJob(jr); err != nil {
			e.log.Error("job failed to run", "job_id", jobID, "err", err)
		}
	}()
}

func (e *Engine) run(jobID string) *jobRun {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.runs[jobID]
}

// Cancel stops a job: running hosts are terminated, queued hosts cancelled.
func (e *Engine) Cancel(ctx context.Context, jobID string) error {
	jr := e.run(jobID)
	if jr == nil {
		j, err := e.st.R.GetJob(ctx, jobID)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrJobNotFound
		} else if err != nil {
			return err
		}
		if j.Status == JobPending {
			return e.st.W.FinishJob(ctx, db.FinishJobParams{Status: JobCancelled, FinishedAt: e.nowNull(), ID: jobID})
		}
		return ErrJobNotActive
	}
	jr.mu.Lock()
	jr.cancelRequested = true
	if jr.paused {
		jr.paused = false
		close(jr.resume)
	}
	jr.mu.Unlock()
	jr.cancel()
	return nil
}

// Resume continues a job paused by the circuit breaker.
func (e *Engine) Resume(ctx context.Context, jobID string) error {
	jr := e.run(jobID)
	if jr == nil {
		return ErrJobNotPaused
	}
	jr.mu.Lock()
	if !jr.paused {
		jr.mu.Unlock()
		return ErrJobNotPaused
	}
	jr.paused = false
	jr.breakerCount = 0
	jr.breakerClass = ""
	close(jr.resume)
	jr.mu.Unlock()
	if err := e.st.W.SetJobRunning(ctx, db.SetJobRunningParams{StartedAt: e.nowNull(), ID: jobID}); err != nil {
		return err
	}
	e.publishJob(jobID)
	return nil
}

func (e *Engine) nowNull() sql.NullInt64 {
	return sql.NullInt64{Int64: e.now().UnixMilli(), Valid: true}
}

func (e *Engine) pause(jr *jobRun, reason string) {
	jr.mu.Lock()
	if jr.paused || jr.cancelRequested {
		jr.mu.Unlock()
		return
	}
	jr.paused = true
	jr.resume = make(chan struct{})
	jr.mu.Unlock()
	if err := e.st.W.SetJobPaused(e.base, db.SetJobPausedParams{PauseReason: reason, ID: jr.id}); err != nil {
		e.log.Error("pause job", "job_id", jr.id, "err", err)
	}
	_ = e.audit.Record(e.base, audit.Event{Action: "job.pause", Actor: &audit.System, TargetType: "job", TargetID: jr.id,
		Details: map[string]any{"reason": reason}})
	e.log.Warn("job paused", "job_id", jr.id, "reason", reason)
	e.publishJob(jr.id)
}

// waitIfPaused blocks while the job is paused. It returns false if the job
// was cancelled.
func (jr *jobRun) waitIfPaused() bool {
	for {
		jr.mu.Lock()
		paused, ch := jr.paused, jr.resume
		jr.mu.Unlock()
		if !paused {
			return jr.ctx.Err() == nil
		}
		select {
		case <-ch:
		case <-jr.ctx.Done():
			return false
		}
	}
}

func (e *Engine) runJob(jr *jobRun) error {
	ctx := e.base
	j, err := e.st.R.GetJob(ctx, jr.id)
	if err != nil {
		return err
	}
	if err := json.Unmarshal([]byte(j.SpecJson), &jr.spec); err != nil {
		return fmt.Errorf("decode spec: %w", err)
	}
	jr.createdBy = j.CreatedBy
	hosts, err := e.st.R.ListJobHosts(ctx, jr.id)
	if err != nil {
		return err
	}
	if err := e.st.W.SetJobRunning(ctx, db.SetJobRunningParams{StartedAt: e.nowNull(), ID: jr.id}); err != nil {
		return err
	}
	e.publishJob(jr.id)

	slots := make(chan struct{}, jr.spec.Strategy.Concurrency)
	var wg sync.WaitGroup
	next := 0
	for next < len(hosts) {
		if hosts[next].State != HostQueued {
			next++
			continue
		}
		if !jr.waitIfPaused() || jr.stopScheduling() {
			break
		}
		if !acquire(jr.ctx, slots) {
			break
		}
		if !acquire(jr.ctx, e.global) {
			<-slots
			break
		}
		// A failure elsewhere may have paused the job or hit the threshold while
		// this host waited for a slot; go back to the top to wait or stop.
		if jr.isPaused() || jr.stopScheduling() {
			<-e.global
			<-slots
			continue
		}
		h := hosts[next]
		next++
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-e.global; <-slots }()
			e.runHost(jr, h)
		}()
	}
	wg.Wait()

	// Hosts that never started.
	jr.mu.Lock()
	cancelled, threshold := jr.cancelRequested, jr.thresholdHit
	jr.mu.Unlock()
	for ; next < len(hosts); next++ {
		h := hosts[next]
		if h.State != HostQueued {
			continue
		}
		switch {
		case cancelled:
			e.finishHost(jr.id, h, HostCancelled, nil, &model.OpError{Class: model.ClassCancelled, Message: "job cancelled before this host started"})
		case threshold:
			e.finishHost(jr.id, h, HostSkipped, nil, &model.OpError{Class: model.ClassExecution, Message: "skipped: the job's failure threshold was reached"})
		}
	}
	status := JobCompleted
	if cancelled {
		status = JobCancelled
	}
	if err := e.st.W.FinishJob(ctx, db.FinishJobParams{Status: status, FinishedAt: e.nowNull(), ID: jr.id}); err != nil {
		return err
	}
	e.publishJob(jr.id)
	e.hub.Publish(jr.id, "end", map[string]string{"status": status})
	return nil
}

func (jr *jobRun) stopScheduling() bool {
	jr.mu.Lock()
	defer jr.mu.Unlock()
	return jr.thresholdHit || jr.cancelRequested
}

func (jr *jobRun) isPaused() bool {
	jr.mu.Lock()
	defer jr.mu.Unlock()
	return jr.paused
}

// acquire takes a slot from sem unless ctx ends first.
func acquire(ctx context.Context, sem chan struct{}) bool {
	select {
	case sem <- struct{}{}:
		return true
	case <-ctx.Done():
		return false
	}
}

func (e *Engine) jumpSlot(via string) chan struct{} {
	e.jumpMu.Lock()
	defer e.jumpMu.Unlock()
	s := e.jumpSem[via]
	if s == nil {
		s = make(chan struct{}, e.cfg.PerJumpConcurrency)
		e.jumpSem[via] = s
	}
	return s
}

// runHost executes all steps on one host.
// OnHostChanged registers fn to be called after a job with typed package,
// service or reboot steps releases a host, so inventory can be re-checked
// instead of showing pre-job update counts until the next poll.
func (e *Engine) OnHostChanged(fn func(hostID string)) {
	e.mu.Lock()
	e.hostChanged = fn
	e.mu.Unlock()
}

func changesInventory(spec model.JobSpec) bool {
	for _, op := range spec.Steps {
		switch op.Type {
		case model.OpPackage, model.OpSystemUpdate, model.OpService, model.OpReboot:
			return true
		}
	}
	return false
}

func (e *Engine) runHost(jr *jobRun, h db.JobHost) {
	ctx := jr.ctx
	mode := jr.spec.LockMode()

	release, ok := e.locks.TryAcquire(h.HostID, jr.id, mode)
	if !ok {
		e.setState(jr.id, h, HostWaitingLock, 0, 0, e.locks.Holders(h.HostID))
		lctx, cancel := context.WithTimeout(ctx, e.cfg.LockWait)
		var err error
		release, err = e.locks.Acquire(lctx, h.HostID, jr.id, mode)
		cancel()
		if err != nil {
			if ctx.Err() != nil {
				e.finishHost(jr.id, h, HostCancelled, nil, &model.OpError{Class: model.ClassCancelled, Message: "cancelled while waiting for the host lock"})
			} else {
				e.finishHost(jr.id, h, HostSkipped, nil, &model.OpError{Class: model.ClassLock,
					Message: "skipped: another job held this host for too long", Hint: "Re-run this host when the other job has finished."})
			}
			return
		}
	}
	defer func() {
		release()
		e.mu.Lock()
		fn := e.hostChanged
		e.mu.Unlock()
		if fn != nil && changesInventory(jr.spec) {
			fn(h.HostID)
		}
	}()

	e.setState(jr.id, h, HostConnecting, 0, 1, nil)
	path, err := e.exec.Prepare(ctx, h.HostID)
	if err != nil {
		e.hostFailed(jr, h, nil, err)
		return
	}
	defer func() {
		if tr, note := path.Transport(); tr != "" {
			e.st.W.SetJobHostTransport(e.base, db.SetJobHostTransportParams{Transport: tr, TransportNote: note, JobID: jr.id, HostID: h.HostID})
		}
	}()
	if via := path.JumpHostID(); via != "" {
		slot := e.jumpSlot(via)
		select {
		case slot <- struct{}{}:
			defer func() { <-slot }()
		case <-ctx.Done():
			e.finishHost(jr.id, h, HostCancelled, nil, &model.OpError{Class: model.ClassCancelled, Message: "cancelled"})
			return
		}
	}
	secrets := path.Secrets()
	var last *driver.OpResult

	var hostOS model.HostOS
	if jr.spec.NeedsFacts() {
		if hostOS, err = e.inv.HostOS(ctx, h.HostID); err != nil {
			e.hostFailed(jr, h, nil, err)
			return
		}
	}
	ran := 0
	var skipped []string
	for i, spec := range jr.spec.Steps {
		step, applies, why := spec.ForHost(hostOS)
		if !applies {
			skipped = append(skipped, fmt.Sprintf("step %d: %s", i+1, why))
			continue
		}
		ran++
		meta := step.Meta()
		for attempt := 1; ; attempt++ {
			e.setState(jr.id, h, HostConnecting, i, attempt, nil)
			if err := e.st.W.InsertJobStep(e.base, db.InsertJobStepParams{JobID: jr.id, HostID: h.HostID,
				StepIndex: int64(i), OpType: step.Type, Attempt: int64(attempt), StartedAt: e.now().UnixMilli()}); err != nil {
				e.log.Error("insert job step", "err", err)
			}
			out := e.newOutput(jr.id, h.HostID, i, secrets)
			stepIdx, att := i, attempt
			out.phase = func(state string) { e.setState(jr.id, h, state, stepIdx, att, nil) }
			if attempt > 1 {
				out.Note(fmt.Sprintf("attempt %d of %d", attempt, e.cfg.RetryAttempts))
			}
			sctx, cancel := context.WithTimeout(ctx, time.Duration(meta.TimeoutS)*time.Second)
			sctx = driver.WithStepInfo(sctx, driver.StepInfo{JobID: jr.id, HostID: h.HostID, UserID: jr.createdBy, Step: i, Attempt: attempt})
			res, err := path.Execute(sctx, step, out,
				func() { e.setState(jr.id, h, HostRunning, i, attempt, nil) })
			cancel()
			if err != nil && ctx.Err() != nil && model.ClassOf(err) != model.ClassCancelled {
				err = &model.OpError{Class: model.ClassCancelled, Message: "cancelled", Err: err}
			}
			e.finishStep(jr.id, h.HostID, i, res, err, out)
			if err == nil {
				last = res
				break
			}
			if attempt < e.cfg.RetryAttempts && ctx.Err() == nil && retryable(err, meta) {
				delay := backoff(e.cfg.RetryDelay, attempt)
				e.log.Info("retrying host", "job_id", jr.id, "host_id", h.HostID, "attempt", attempt+1, "in", delay, "err", err)
				select {
				case <-time.After(delay):
					continue
				case <-ctx.Done():
					err = &model.OpError{Class: model.ClassCancelled, Message: "cancelled"}
				}
			}
			e.hostFailed(jr, h, res, err)
			return
		}
	}
	if ran == 0 {
		e.finishHost(jr.id, h, HostSkipped, nil, &model.OpError{Class: model.ClassValidation,
			Message: "skipped: " + strings.Join(skipped, "; "),
			Hint:    "Conditions are evaluated against discovered facts; refresh the host's inventory if they look wrong."})
		return
	}
	var note error
	if len(skipped) > 0 {
		note = &model.OpError{Class: "", Message: "some steps were skipped: " + strings.Join(skipped, "; ")}
	}
	e.finishHost(jr.id, h, HostSucceeded, last, note)
	jr.mu.Lock()
	jr.breakerCount, jr.breakerClass = 0, ""
	jr.mu.Unlock()
}

// retryable implements ARCHITECTURE-ALPHA §11.4.
func retryable(err error, meta model.Meta) bool {
	oe := model.AsOpError(err)
	switch oe.Class {
	case model.ClassConnection:
		return driver.IsNotStarted(err) || meta.Idempotent
	case model.ClassTimeout:
		return meta.Idempotent
	case model.ClassPackageManager:
		return oe.Retryable
	}
	return false
}

func backoff(base time.Duration, attempt int) time.Duration {
	d := base << (attempt - 1)
	if d > 2*time.Minute {
		d = 2 * time.Minute
	}
	jitter := 0.8 + 0.4*rand.Float64()
	return time.Duration(float64(d) * jitter)
}

// breakerClasses are failures that usually indicate a shared problem
// (credential, key, sudo policy) rather than one broken host.
var breakerClasses = map[model.ErrorClass]bool{
	model.ClassAuthentication: true, model.ClassHostKey: true, model.ClassAuthorization: true,
}

func (e *Engine) hostFailed(jr *jobRun, h db.JobHost, res *driver.OpResult, err error) {
	oe := model.AsOpError(err)
	state := HostFailed
	switch oe.Class {
	case model.ClassTimeout:
		state = HostTimedOut
	case model.ClassCancelled:
		state = HostCancelled
	}
	e.finishHost(jr.id, h, state, res, err)
	if state == HostCancelled {
		return
	}
	jr.mu.Lock()
	jr.failures++
	if t := jr.spec.Strategy.FailureThreshold; t != nil && jr.failures >= *t {
		jr.thresholdHit = true
	}
	trip := false
	if breakerClasses[oe.Class] {
		if jr.breakerClass == oe.Class {
			jr.breakerCount++
		} else {
			jr.breakerClass, jr.breakerCount = oe.Class, 1
		}
		trip = jr.breakerCount >= e.cfg.BreakerThreshold
	} else {
		jr.breakerCount, jr.breakerClass = 0, ""
	}
	n := jr.breakerCount
	jr.mu.Unlock()
	if trip {
		e.pause(jr, fmt.Sprintf("%d consecutive %s failures; likely a shared credential or configuration problem", n, oe.Class))
	}
}

func (e *Engine) setState(jobID string, h db.JobHost, state string, step, attempt int, waitingFor []string) {
	if err := e.st.W.SetJobHostState(e.base, db.SetJobHostStateParams{State: state, CurrentStep: int64(step),
		Attempt: int64(attempt), StartedAt: e.nowNull(), JobID: jobID, HostID: h.HostID}); err != nil {
		e.log.Error("set job host state", "err", err)
	}
	e.hub.Publish(jobID, "host", HostEvent{HostID: h.HostID, HostName: h.HostName, State: state,
		CurrentStep: step, Attempt: attempt, WaitingFor: waitingFor})
}

func (e *Engine) finishHost(jobID string, h db.JobHost, state string, res *driver.OpResult, err error) {
	var exit sql.NullInt64
	if res != nil && res.ExitCode != nil {
		exit = sql.NullInt64{Int64: int64(*res.ExitCode), Valid: true}
	}
	ev := HostEvent{HostID: h.HostID, HostName: h.HostName, State: state}
	params := db.FinishJobHostParams{State: state, ExitCode: exit, FinishedAt: e.nowNull(), JobID: jobID, HostID: h.HostID}
	if err != nil {
		oe := model.AsOpError(err)
		params.ErrorClass, params.ErrorMessage, params.ErrorHint = string(oe.Class), errMessage(oe), oe.Hint
		params.Retryable = boolInt(oe.Retryable)
		ev.ErrorClass, ev.ErrorMessage, ev.ErrorHint = params.ErrorClass, params.ErrorMessage, params.ErrorHint
	}
	if exit.Valid {
		c := int(exit.Int64)
		ev.ExitCode = &c
	}
	if err := e.st.W.FinishJobHost(e.base, params); err != nil {
		e.log.Error("finish job host", "err", err)
	}
	e.hub.Publish(jobID, "host", ev)
	e.publishJob(jobID)
}

func (e *Engine) finishStep(jobID, hostID string, step int, res *driver.OpResult, err error, out *outputWriter) {
	stdout, stderr, truncated := out.Close()
	state := "succeeded"
	if err != nil {
		switch model.ClassOf(err) {
		case model.ClassTimeout:
			state = "timed_out"
		case model.ClassCancelled:
			state = "cancelled"
		default:
			state = "failed"
		}
	}
	var exit, changed sql.NullInt64
	result := "{}"
	if res != nil {
		if res.ExitCode != nil {
			exit = sql.NullInt64{Int64: int64(*res.ExitCode), Valid: true}
		}
		if res.Changed != nil {
			changed = sql.NullInt64{Int64: boolInt(*res.Changed), Valid: true}
		}
		if res.Data != nil {
			if b, err := json.Marshal(res.Data); err == nil {
				result = string(b)
			}
		}
	}
	if err := e.st.W.FinishJobStep(e.base, db.FinishJobStepParams{State: state, ExitCode: exit, Changed: changed, ResultJson: result,
		StdoutBytes: stdout, StderrBytes: stderr, Truncated: boolInt(truncated), FinishedAt: e.nowNull(),
		JobID: jobID, HostID: hostID, StepIndex: int64(step)}); err != nil {
		e.log.Error("finish job step", "err", err)
	}
}

func errMessage(oe *model.OpError) string {
	if oe.Message != "" {
		return oe.Message
	}
	if oe.Err != nil {
		return oe.Err.Error()
	}
	return string(oe.Class)
}

func boolInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

// HostEvent is the payload of a "host" event.
type HostEvent struct {
	HostID       string   `json:"host_id"`
	HostName     string   `json:"host_name"`
	State        string   `json:"state"`
	CurrentStep  int      `json:"current_step"`
	Attempt      int      `json:"attempt"`
	ExitCode     *int     `json:"exit_code,omitempty"`
	ErrorClass   string   `json:"error_class,omitempty"`
	ErrorMessage string   `json:"error_message,omitempty"`
	ErrorHint    string   `json:"error_hint,omitempty"`
	WaitingFor   []string `json:"waiting_for,omitempty"`
}

// JobEvent is the payload of a "job" event.
type JobEvent struct {
	ID          string         `json:"id"`
	Status      string         `json:"status"`
	PauseReason string         `json:"pause_reason,omitempty"`
	Counts      map[string]int `json:"counts"`
}

func (e *Engine) publishJob(jobID string) {
	j, err := e.st.R.GetJob(e.base, jobID)
	if err != nil {
		return
	}
	counts, _ := Counts(e.base, e.st.R, jobID)
	e.hub.Publish(jobID, "job", JobEvent{ID: jobID, Status: j.Status, PauseReason: j.PauseReason, Counts: counts})
}

// Counts returns the number of hosts in each state.
func Counts(ctx context.Context, q *db.Queries, jobID string) (map[string]int, error) {
	rows, err := q.CountJobHostStates(ctx, jobID)
	if err != nil {
		return nil, err
	}
	out := map[string]int{}
	for _, r := range rows {
		out[r.State] = int(r.N)
	}
	return out, nil
}
