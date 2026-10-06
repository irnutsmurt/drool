// Package audit records append-only audit events (DESIGN §22.6).
//
// Events are written to the audit_events table (protected against UPDATE and
// DELETE by triggers) and mirrored to the audit log stream.
package audit

import (
	"context"
	"encoding/json"
	"log/slog"

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
}

func NewWriter(st *store.Store, log *slog.Logger) *Writer {
	return &Writer{st: st, log: log}
}

// Record writes the event. A failure to write an audit record is returned to
// the caller: privileged actions should not silently proceed unaudited.
func (w *Writer) Record(ctx context.Context, e Event) error {
	row, err := w.build(ctx, e)
	if err != nil {
		return err
	}
	err = w.st.W.InsertAuditEvent(ctx, row)
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
	if err := q.InsertAuditEvent(ctx, row); err != nil {
		return func() {}, err
	}
	return func() { w.emit(ctx, row, nil) }, nil
}

func (w *Writer) build(ctx context.Context, e Event) (db.InsertAuditEventParams, error) {
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
			return db.InsertAuditEventParams{}, err
		}
		details = string(b)
	}
	return db.InsertAuditEventParams{
		ID: ids.New(ids.Audit), Ts: store.Now(),
		ActorType: actor.Type, ActorID: actor.ID, ActorName: actor.Name,
		Action: e.Action, TargetType: e.TargetType, TargetID: e.TargetID,
		Result: string(e.Result), SourceIp: req.SourceIP, RequestID: req.ID,
		DetailsJson: details,
	}, nil
}

func (w *Writer) emit(ctx context.Context, r db.InsertAuditEventParams, persistErr error) {
	level := slog.LevelInfo
	if persistErr != nil {
		level = slog.LevelError
	}
	w.log.Log(ctx, level, "audit",
		"audit_id", r.ID, "action", r.Action, "result", r.Result,
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
