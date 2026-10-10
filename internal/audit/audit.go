// Package audit records append-only audit events (DESIGN §22.6).
//
// Events are written to the audit_events table (protected against UPDATE and
// DELETE by triggers) as links of a hash chain (chain.go), and mirrored to
// the audit log stream.
package audit

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"

	"drool/internal/ids"
	"drool/internal/logging"
	"drool/internal/store"
	"drool/internal/store/db"
)

type Result string

const (
	Success Result = "success"
	Failure Result = "failure"
	Denied  Result = "denied"
)

// Actor types.
const (
	ActorUser      = "user"
	ActorToken     = "token"
	ActorSystem    = "system"
	ActorAnonymous = "anonymous"
)

// Actor identifies who performed an action.
type Actor struct {
	Type string
	ID   string
	Name string
}

// System is the actor for controller-initiated actions.
var System = Actor{Type: ActorSystem, Name: "system"}

// Request carries per-request metadata.
type Request struct {
	ID       string
	SourceIP string
}

type ctxKey int

const (
	actorKey ctxKey = iota
	requestKey
)

func WithActor(ctx context.Context, a Actor) context.Context {
	return context.WithValue(ctx, actorKey, a)
}

func WithRequest(ctx context.Context, r Request) context.Context {
	return context.WithValue(ctx, requestKey, r)
}

func ActorFrom(ctx context.Context) Actor {
	if a, ok := ctx.Value(actorKey).(Actor); ok {
		return a
	}
	return Actor{Type: ActorAnonymous}
}

func RequestFrom(ctx context.Context) Request {
	r, _ := ctx.Value(requestKey).(Request)
	return r
}

// Event is one audit record. Actor and request metadata come from the
// context unless Actor is set explicitly.
type Event struct {
	Action     string // dotted, e.g. "auth.login", "user.create"
	TargetType string
	TargetID   string
	Result     Result
	Details    map[string]any
	Actor      *Actor
}

// Writer persists audit events.
type Writer struct {
	st  *store.Store
	log *slog.Logger

	mu       sync.Mutex
	seen     int64             // newest chain position this process committed
	appended []func(seq int64) // called after a record committed (remote sink)
}

func NewWriter(st *store.Store, log *slog.Logger) *Writer {
	return &Writer{st: st, log: log}
}

// OnAppend registers fn to be called (outside any transaction) after each
// record this writer committed.
func (w *Writer) OnAppend(fn func(seq int64)) {
	w.mu.Lock()
	w.appended = append(w.appended, fn)
	w.mu.Unlock()
}

// Seen is the newest chain position this process committed: if the stored
// chain ever ends before it, records were removed from the end.
func (w *Writer) Seen() int64 {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.seen
}

func (w *Writer) committed(seq int64) {
	w.mu.Lock()
	if seq > w.seen {
		w.seen = seq
	}
	fns := append([]func(int64){}, w.appended...)
	w.mu.Unlock()
	for _, fn := range fns {
		fn(seq)
	}
}

// Record writes the event. A failure to write an audit record is returned to
// the caller: privileged actions should not silently proceed unaudited.
func (w *Writer) Record(ctx context.Context, e Event) error {
	row, err := w.build(ctx, e)
	if err != nil {
		return err
	}
	err = w.st.InTx(ctx, func(q *db.Queries) error { return link(ctx, q, &row) })
	if err == nil {
		w.committed(row.Seq.Int64)
	}
	w.emit(ctx, row, err)
	return err
}

// RecordTx writes the event using q, normally a transaction, so the audit
// record commits or rolls back together with the change it describes. Call
// the returned function after the transaction commits to mirror the event to
// the audit log stream.
func (w *Writer) RecordTx(ctx context.Context, q *db.Queries, e Event) (emit func(), err error) {
	row, err := w.build(ctx, e)
	if err != nil {
		return func() {}, err
	}
	if err := link(ctx, q, &row); err != nil {
		return func() {}, err
	}
	return func() {
		w.committed(row.Seq.Int64)
		w.emit(ctx, row, nil)
	}, nil
}

// link makes row the next link of the chain, starting the chain first if
// this is the first record written since migration 0016. q must be a write
// transaction (BEGIN IMMEDIATE), so reading the head and inserting the next
// link can't interleave with another writer, in this process or another.
func link(ctx context.Context, q *db.Queries, row *db.InsertChainedAuditEventParams) error {
	head, err := q.AuditChainHead(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		head, err = startChain(ctx, q)
	}
	if err != nil {
		return err
	}
	row.Seq = sql.NullInt64{Int64: head.Seq.Int64 + 1, Valid: true}
	row.PrevHash = head.Hash
	row.Hash = linkOfParams(row).ComputeHash()
	return q.InsertChainedAuditEvent(ctx, *row)
}

// chainStart is the first link's details. A struct, not a map: the stored
// text is what the hash covers, and its field order should be fixed.
type chainStart struct {
	ChainID       string `json:"chain_id"`
	Format        string `json:"format"`
	LegacyRecords int64  `json:"legacy_records"`
	LegacyDigest  string `json:"legacy_digest"`
	LegacyFirstID string `json:"legacy_first_id,omitempty"`
	LegacyLastID  string `json:"legacy_last_id,omitempty"`
}

// startChain writes the first link: it names the chain and commits to every
// record written before the chain existed with a digest.
func startChain(ctx context.Context, q *db.Queries) (db.AuditChainHeadRow, error) {
	legacy, err := q.ListUnchainedAuditEvents(ctx)
	if err != nil {
		return db.AuditChainHeadRow{}, err
	}
	d := newLegacyDigest()
	for i := range legacy {
		d.add(linkOfEvent(&legacy[i]))
	}
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return db.AuditChainHeadRow{}, err
	}
	details, _ := json.Marshal(chainStart{ChainID: hex.EncodeToString(b), Format: ChainFormat,
		LegacyRecords: d.n, LegacyDigest: d.hex(), LegacyFirstID: d.first, LegacyLastID: d.last})
	g := db.InsertChainedAuditEventParams{ID: ids.New(ids.Audit), Ts: store.Now(), ActorType: ActorSystem, ActorName: System.Name,
		Action: ActionChainStart, Result: string(Success), DetailsJson: string(details),
		Seq: sql.NullInt64{Int64: 1, Valid: true}, PrevHash: zeroHash}
	g.Hash = linkOfParams(&g).ComputeHash()
	if err := q.InsertChainedAuditEvent(ctx, g); err != nil {
		return db.AuditChainHeadRow{}, err
	}
	return db.AuditChainHeadRow{Seq: g.Seq, Hash: g.Hash}, nil
}

// Checkpoint seals the chain as it stands: a record whose MAC (keyed with a
// key derived from the master key, keyVersion) covers the newest record and,
// through the chain, everything before it. Nothing is written when there is
// no chain yet or the newest record already is a checkpoint.
func (w *Writer) Checkpoint(ctx context.Context, key []byte, keyVersion int) (bool, error) {
	var row db.InsertChainedAuditEventParams
	wrote := false
	err := w.st.InTx(ctx, func(q *db.Queries) error {
		head, err := q.AuditChainHead(ctx)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		last, err := q.GetAuditEventBySeq(ctx, head.Seq)
		if err != nil {
			return err
		}
		if last.Action == ActionCheckpoint {
			return nil
		}
		first, err := q.GetAuditEventBySeq(ctx, sql.NullInt64{Int64: 1, Valid: true})
		var cs chainStart
		if err != nil || json.Unmarshal([]byte(first.DetailsJson), &cs) != nil || cs.ChainID == "" {
			return errors.New("the audit chain has no valid first record; not sealing it")
		}
		mac := checkpointMAC(key, cs.ChainID, head.Seq.Int64, head.Hash)
		details, _ := json.Marshal(checkpointDetails{CoversSeq: head.Seq.Int64, CoversHash: hex.EncodeToString(head.Hash),
			KeyVersion: keyVersion, MAC: hex.EncodeToString(mac)})
		row = db.InsertChainedAuditEventParams{ID: ids.New(ids.Audit), Ts: store.Now(), ActorType: ActorSystem, ActorName: System.Name,
			Action: ActionCheckpoint, Result: string(Success), DetailsJson: string(details)}
		wrote = true
		return link(ctx, q, &row)
	})
	if err == nil && wrote {
		w.committed(row.Seq.Int64)
	}
	return wrote, err
}

func linkOfParams(p *db.InsertChainedAuditEventParams) *Link {
	return &Link{Seq: p.Seq.Int64, ID: p.ID, Ts: p.Ts, ActorType: p.ActorType, ActorID: p.ActorID, ActorName: p.ActorName,
		Action: p.Action, TargetType: p.TargetType, TargetID: p.TargetID, Result: p.Result, SourceIP: p.SourceIp,
		RequestID: p.RequestID, Details: p.DetailsJson, PrevHash: p.PrevHash, Hash: p.Hash}
}

func linkOfEvent(e *db.AuditEvent) *Link {
	return &Link{Seq: e.Seq.Int64, ID: e.ID, Ts: e.Ts, ActorType: e.ActorType, ActorID: e.ActorID, ActorName: e.ActorName,
		Action: e.Action, TargetType: e.TargetType, TargetID: e.TargetID, Result: e.Result, SourceIP: e.SourceIp,
		RequestID: e.RequestID, Details: e.DetailsJson, PrevHash: e.PrevHash, Hash: e.Hash}
}

func (w *Writer) build(ctx context.Context, e Event) (db.InsertChainedAuditEventParams, error) {
	actor := ActorFrom(ctx)
	if e.Actor != nil {
		actor = *e.Actor
	}
	req := RequestFrom(ctx)
	if e.Result == "" {
		e.Result = Success
	}
	details := "{}"
	if len(e.Details) > 0 {
		clean := make(map[string]any, len(e.Details))
		for k, v := range e.Details {
			if logging.IsSensitiveKey(k) {
				v = logging.RedactedValue
			}
			clean[k] = v
		}
		b, err := json.Marshal(clean)
		if err != nil {
			return db.InsertChainedAuditEventParams{}, err
		}
		details = string(b)
	}
	return db.InsertChainedAuditEventParams{
		ID: ids.New(ids.Audit), Ts: store.Now(),
		ActorType: actor.Type, ActorID: actor.ID, ActorName: actor.Name,
		Action: e.Action, TargetType: e.TargetType, TargetID: e.TargetID,
		Result: string(e.Result), SourceIp: req.SourceIP, RequestID: req.ID,
		DetailsJson: details,
	}, nil
}

func (w *Writer) emit(ctx context.Context, r db.InsertChainedAuditEventParams, persistErr error) {
	level := slog.LevelInfo
	if persistErr != nil {
		level = slog.LevelError
	}
	w.log.Log(ctx, level, "audit",
		"audit_id", r.ID, "seq", r.Seq.Int64, "action", r.Action, "result", r.Result,
		"actor_type", r.ActorType, "actor_id", r.ActorID, "actor_name", r.ActorName,
		"target_type", r.TargetType, "target_id", r.TargetID,
		"request_id", r.RequestID, "source_ip", r.SourceIp, "details", json.RawMessage(r.DetailsJson),
		"persist_error", errString(persistErr))
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
