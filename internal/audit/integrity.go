package audit

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"drool/internal/ids"
	"drool/internal/store"
)

// KeyDeriver derives keys from the master key (secrets.Box).
type KeyDeriver interface {
	DeriveKey(purpose string, version int) ([]byte, int, error)
}

// IntegrityDeps are the integrity service's dependencies.
type IntegrityDeps struct {
	Store   *store.Store
	Writer  *Writer
	Keys    KeyDeriver // nil: checkpoints are neither written nor checked
	DataDir string     // evidence files are written here
	Log     *slog.Logger
	// onFailure is called once per failed verification (an event on the
	// Overview, for example); set with OnFailure.
	onFailure func(v Verification)
	// SealEvery is how often new records are sealed (default 1 h);
	// VerifyEvery how often the whole chain is checked (default 24 h).
	SealEvery, VerifyEvery time.Duration
	Now                    func() time.Time
}

// Integrity keeps the audit chain sealed and verified (ARCHITECTURE-ALPHA
// §33.4): checkpoints at start, hourly while there are new records and at
// shutdown; a full verification at start (in the background), daily and
// on request.
type Integrity struct {
	d       IntegrityDeps
	mu      sync.Mutex
	running string // id of the verification in progress
	wake    chan struct{}
}

// Verification is one verification run as stored.
type Verification struct {
	ID              string    `json:"id"`
	Cause           string    `json:"cause"`
	RequestedBy     string    `json:"requested_by,omitempty"`
	StartedAt       int64     `json:"-"`
	FinishedAt      int64     `json:"-"`
	Result          string    `json:"result"` // running, ok, failed, error
	Records         int64     `json:"records"`
	LegacyRecords   int64     `json:"legacy_records"`
	HeadSeq         int64     `json:"head_seq"`
	SealedThrough   int64     `json:"sealed_through"`
	Checkpoints     int64     `json:"checkpoints"`
	Unverifiable    int64     `json:"checkpoints_unverifiable"`
	FirstBadSeq     int64     `json:"first_bad_seq,omitempty"`
	Problem         string    `json:"problem,omitempty"`
	Detail          string    `json:"detail,omitempty"`
	Problems        []Problem `json:"problems"`
	ProblemCount    int64     `json:"problem_count"`
	EvidenceFile    string    `json:"evidence_file,omitempty"`
	ChainID         string    `json:"-"`
	UnchainedRecord string    `json:"-"`
}

// Signature identifies a set of problems (where and what), so an
// acknowledged break can be told apart from a new one.
func (v *Verification) Signature() string {
	var b []byte
	for _, p := range v.Problems {
		b = fmt.Appendf(b, "%s@%d;", p.Kind, p.Seq)
	}
	return fmt.Sprintf("%d:%s", v.ProblemCount, b)
}

// NewIntegrity creates the service.
func NewIntegrity(d IntegrityDeps) *Integrity {
	if d.SealEvery == 0 {
		d.SealEvery = time.Hour
	}
	if d.VerifyEvery == 0 {
		d.VerifyEvery = 24 * time.Hour
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Log == nil {
		d.Log = slog.New(slog.DiscardHandler)
	}
	return &Integrity{d: d, wake: make(chan struct{}, 1)}
}

// OnFailure sets what happens when a verification fails (in addition to
// the evidence file, the security log and the audit record).
func (i *Integrity) OnFailure(fn func(v Verification)) {
	i.mu.Lock()
	i.d.onFailure = fn
	i.mu.Unlock()
}

// ErrVerifying means a verification is already running.
var ErrVerifying = errors.New("an audit verification is already running")

// Seal writes a checkpoint if anything was recorded since the last one.
func (i *Integrity) Seal(ctx context.Context) error {
	if i.d.Keys == nil {
		return nil
	}
	key, v, err := i.d.Keys.DeriveKey(CheckpointPurpose, 0)
	if err != nil {
		return err
	}
	_, err = i.d.Writer.Checkpoint(ctx, key, v)
	return err
}

// Run seals and verifies until ctx ends, and seals once more at the end.
func (i *Integrity) Run(ctx context.Context) {
	if err := i.Seal(ctx); err != nil {
		i.d.Log.Error("audit checkpoint", "err", err)
	}
	if _, err := i.Start(ctx, "startup", ""); err != nil {
		i.d.Log.Error("audit verification", "err", err)
	}
	seal := time.NewTicker(i.d.SealEvery)
	verify := time.NewTicker(i.d.VerifyEvery)
	defer seal.Stop()
	defer verify.Stop()
	for {
		select {
		case <-ctx.Done():
			bg, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
			if err := i.Seal(bg); err != nil {
				i.d.Log.Error("audit checkpoint at shutdown", "err", err)
			}
			cancel()
			return
		case <-seal.C:
			if err := i.Seal(ctx); err != nil {
				i.d.Log.Error("audit checkpoint", "err", err)
			}
		case <-verify.C:
			if _, err := i.Start(ctx, "scheduled", ""); err != nil && !errors.Is(err, ErrVerifying) {
				i.d.Log.Error("audit verification", "err", err)
			}
		}
	}
}

// Start begins a verification in the background and returns its id.
func (i *Integrity) Start(ctx context.Context, cause, requestedBy string) (string, error) {
	i.mu.Lock()
	if i.running != "" {
		i.mu.Unlock()
		return "", ErrVerifying
	}
	id := ids.New("aver")
	i.running = id
	i.mu.Unlock()
	now := i.d.Now().UnixMilli()
	if _, err := i.d.Store.Writer.ExecContext(ctx, `INSERT INTO audit_verifications (id, cause, requested_by, started_at, result) VALUES (?, ?, ?, ?, 'running')`,
		id, cause, requestedBy, now); err != nil {
		i.done()
		return "", err
	}
	go func() {
		defer i.done()
		i.verify(context.WithoutCancel(ctx), id)
	}()
	return id, nil
}

func (i *Integrity) done() {
	i.mu.Lock()
	i.running = ""
	i.mu.Unlock()
	select {
	case i.wake <- struct{}{}:
	default:
	}
}

// VerifyNow runs a verification and waits for it (tests, the CLI).
func (i *Integrity) VerifyNow(ctx context.Context, cause, requestedBy string) (*Verification, error) {
	id, err := i.Start(ctx, cause, requestedBy)
	if err != nil {
		return nil, err
	}
	for {
		v, err := i.Get(ctx, id)
		if err != nil {
			return nil, err
		}
		if v.Result != "running" {
			return v, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-i.wake:
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func (i *Integrity) checkpointKey(v int) ([]byte, bool) {
	if i.d.Keys == nil {
		return nil, false
	}
	k, _, err := i.d.Keys.DeriveKey(CheckpointPurpose, v)
	return k, err == nil
}

func (i *Integrity) verify(ctx context.Context, id string) {
	opt := VerifyOptions{Seen: i.d.Writer.Seen()}
	if i.d.Keys != nil {
		opt.CheckpointKey = i.checkpointKey
	}
	rep, err := Verify(ctx, i.d.Store.Reader, opt)
	v := Verification{ID: id, FinishedAt: i.d.Now().UnixMilli()}
	switch {
	case err != nil:
		v.Result, v.Problem, v.Detail = "error", "The audit history could not be checked.", err.Error()
	case rep.OK:
		v.Result = "ok"
	default:
		v.Result = "failed"
	}
	if rep != nil {
		v.Records, v.LegacyRecords, v.HeadSeq, v.SealedThrough = rep.Records, rep.LegacyRecords, rep.HeadSeq, rep.SealedThrough
		v.Checkpoints, v.Unverifiable, v.ChainID = rep.Checkpoints, rep.Unverifiable, rep.ChainID
		if !rep.OK {
			v.FirstBadSeq, v.Problem, v.Detail = rep.FirstBadSeq, rep.Problem, rep.Detail
			v.Problems, v.ProblemCount = rep.Problems, rep.ProblemCount
			v.EvidenceFile = i.writeEvidence(id, rep)
		}
	}
	problems, _ := json.Marshal(v.Problems)
	if v.Problems == nil {
		problems = []byte("[]")
	}
	if _, err := i.d.Store.Writer.ExecContext(ctx, `UPDATE audit_verifications SET finished_at = ?, result = ?, records = ?, legacy_records = ?,
		head_seq = ?, sealed_through = ?, checkpoints = ?, checkpoints_unverifiable = ?, first_bad_seq = ?, problem = ?, detail = ?,
		problems_json = ?, problem_count = ?, evidence_file = ? WHERE id = ?`, v.FinishedAt, v.Result, v.Records, v.LegacyRecords, v.HeadSeq,
		v.SealedThrough, v.Checkpoints, v.Unverifiable, nullInt(v.FirstBadSeq), v.Problem, v.Detail, string(problems), v.ProblemCount,
		v.EvidenceFile, id); err != nil {
		i.d.Log.Error("record audit verification", "err", err)
	}
	if v.Result == "failed" {
		// Outside the chain first (it is what failed), then appended to it:
		// the history is never "repaired", only continued.
		i.d.Log.Error("AUDIT HISTORY VERIFICATION FAILED", "problem", v.Problem, "detail", v.Detail,
			"first_bad_seq", v.FirstBadSeq, "evidence", v.EvidenceFile)
		_ = i.d.Writer.Record(ctx, Event{Action: "audit.verify", Actor: &System, Result: Failure, TargetType: "audit",
			Details: map[string]any{"verification_id": id, "problem": v.Problem, "detail": v.Detail, "first_bad_seq": v.FirstBadSeq,
				"evidence_file": v.EvidenceFile}})
		i.mu.Lock()
		fn := i.d.onFailure
		i.mu.Unlock()
		if fn != nil {
			fn(v)
		}
	}
}

func nullInt(v int64) sql.NullInt64 { return sql.NullInt64{Int64: v, Valid: v != 0} }

// writeEvidence preserves what verification found next to the database,
// outside the chain (0600). It returns the file's path ("" on failure).
func (i *Integrity) writeEvidence(id string, rep *Report) string {
	if i.d.DataDir == "" {
		return ""
	}
	p := filepath.Join(i.d.DataDir, fmt.Sprintf("audit-evidence-%s-%s.json", i.d.Now().UTC().Format("20060102-150405"), id))
	b, err := json.MarshalIndent(map[string]any{
		"verification_id": id, "found_at": i.d.Now().UTC().Format(time.RFC3339), "chain_id": rep.ChainID,
		"problem": rep.Problem, "detail": rep.Detail, "first_bad_seq": rep.FirstBadSeq, "head_seq": rep.HeadSeq,
		"problems": rep.Problems, "problem_count": rep.ProblemCount,
		"records_checked": rep.Records, "records": rep.Evidence, "unchained_records_after_start": rep.UnchainedAfter,
		"note": "Written by DROOL when audit verification failed. The database was not changed. Keep this file with a copy of the database for investigation.",
	}, "", "  ")
	if err != nil {
		return ""
	}
	if err := os.WriteFile(p, b, 0o600); err != nil {
		i.d.Log.Error("write audit evidence", "err", err)
		return ""
	}
	return p
}

const verificationColumns = `id, cause, requested_by, started_at, coalesce(finished_at, 0), result, records, legacy_records, head_seq,
	sealed_through, checkpoints, checkpoints_unverifiable, coalesce(first_bad_seq, 0), problem, detail, problems_json, problem_count, evidence_file`

func scanVerification(row interface{ Scan(...any) error }) (*Verification, error) {
	var v Verification
	var problems string
	err := row.Scan(&v.ID, &v.Cause, &v.RequestedBy, &v.StartedAt, &v.FinishedAt, &v.Result, &v.Records, &v.LegacyRecords, &v.HeadSeq,
		&v.SealedThrough, &v.Checkpoints, &v.Unverifiable, &v.FirstBadSeq, &v.Problem, &v.Detail, &problems, &v.ProblemCount, &v.EvidenceFile)
	if err != nil {
		return nil, err
	}
	json.Unmarshal([]byte(problems), &v.Problems)
	if v.Problems == nil {
		v.Problems = []Problem{}
	}
	return &v, nil
}

// Get returns one verification.
func (i *Integrity) Get(ctx context.Context, id string) (*Verification, error) {
	return scanVerification(i.d.Store.Reader.QueryRowContext(ctx, `SELECT `+verificationColumns+` FROM audit_verifications WHERE id = ?`, id))
}

// Status is the audit chain's state for the UI.
type Status struct {
	HeadSeq       int64
	LegacyRecords int64
	Running       *Verification
	Last          *Verification // newest finished verification
	LastOK        *Verification // newest successful one
	LastFailed    *Verification // newest failed one
	Acknowledged  *Acknowledgement
}

// Acknowledgement is an administrator's note that a known break was
// investigated. It never makes a broken chain look intact: verifications
// still fail and say why; only exactly the acknowledged problems are shown
// as known instead of as an alarm.
type Acknowledgement struct {
	VerificationID string `json:"verification_id"`
	Signature      string `json:"signature"`
	Problem        string `json:"problem"`
	By             string `json:"by"`
	At             int64  `json:"at"`
	Note           string `json:"note"`
}

// Status reports the chain's head and the latest verifications.
func (i *Integrity) Status(ctx context.Context) (*Status, error) {
	st := &Status{}
	var head sql.NullInt64
	if err := i.d.Store.Reader.QueryRowContext(ctx, `SELECT max(seq) FROM audit_events`).Scan(&head); err != nil {
		return nil, err
	}
	st.HeadSeq = head.Int64
	if err := i.d.Store.Reader.QueryRowContext(ctx, `SELECT count(*) FROM audit_events WHERE seq IS NULL`).Scan(&st.LegacyRecords); err != nil {
		return nil, err
	}
	one := func(where string) (*Verification, error) {
		v, err := scanVerification(i.d.Store.Reader.QueryRowContext(ctx,
			`SELECT `+verificationColumns+` FROM audit_verifications WHERE `+where+` ORDER BY started_at DESC, id DESC LIMIT 1`))
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return v, err
	}
	var err error
	if st.Running, err = one(`result = 'running'`); err != nil {
		return nil, err
	}
	if st.Last, err = one(`result != 'running'`); err != nil {
		return nil, err
	}
	if st.LastOK, err = one(`result = 'ok'`); err != nil {
		return nil, err
	}
	if st.LastFailed, err = one(`result = 'failed'`); err != nil {
		return nil, err
	}
	var js string
	if err := i.d.Store.Reader.QueryRowContext(ctx, `SELECT value_json FROM settings WHERE key = 'audit_break_ack'`).Scan(&js); err == nil {
		var a Acknowledgement
		if json.Unmarshal([]byte(js), &a) == nil {
			st.Acknowledged = &a
		}
	}
	return st, nil
}

// Broken reports whether a verification found problems, and whether
// exactly those problems were acknowledged by an administrator. A failure
// stays reported until it is acknowledged, even if a later check passes
// (records removed from the end, for example, are only detectable by the
// process that wrote them).
func (st *Status) Broken() (broken, acknowledged bool) {
	if st.LastFailed == nil {
		return false, false
	}
	a := st.Acknowledged
	return true, a != nil && a.Signature == st.LastFailed.Signature()
}

// Acknowledge records that an administrator investigated the problems the
// newest failed verification found. The caller audits it.
func (i *Integrity) Acknowledge(ctx context.Context, by, note string) (*Acknowledgement, error) {
	st, err := i.Status(ctx)
	if err != nil {
		return nil, err
	}
	if st.LastFailed == nil {
		return nil, errors.New("no verification has found a problem; there is nothing to acknowledge")
	}
	a := &Acknowledgement{VerificationID: st.LastFailed.ID, Signature: st.LastFailed.Signature(), Problem: st.LastFailed.Problem, By: by,
		At: i.d.Now().UnixMilli(), Note: note}
	b, _ := json.Marshal(a)
	_, err = i.d.Store.Writer.ExecContext(ctx, `INSERT INTO settings (key, value_json, updated_at) VALUES ('audit_break_ack', ?, ?)
		ON CONFLICT (key) DO UPDATE SET value_json = excluded.value_json, updated_at = excluded.updated_at`, string(b), a.At)
	return a, err
}

// RecoverInterrupted marks verifications that were running when the
// controller stopped (call at start, before Run).
func (i *Integrity) RecoverInterrupted(ctx context.Context) error {
	_, err := i.d.Store.Writer.ExecContext(ctx, `UPDATE audit_verifications SET result = 'error', finished_at = ?,
		problem = 'The check was interrupted by a controller restart.' WHERE result = 'running'`, i.d.Now().UnixMilli())
	return err
}
