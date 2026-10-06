// Package audit records append-only audit events (DESIGN §22.6).
//
// Events are written to the audit_events table (protected against UPDATE and
// DELETE by triggers) and mirrored to the audit log stream.
package audit

import (
	"context"
	"encoding/json"
	"log/slog"

	"fleetmgr/internal/ids"
	"fleetmgr/internal/logging"
	"fleetmgr/internal/store"
	"fleetmgr/internal/store/db"
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
			return err
		}
		details = string(b)
	}
	id := ids.New(ids.Audit)
	ts := store.Now()
	err := w.st.W.InsertAuditEvent(ctx, db.InsertAuditEventParams{
		ID: id, Ts: ts,
		ActorType: actor.Type, ActorID: actor.ID, ActorName: actor.Name,
		Action: e.Action, TargetType: e.TargetType, TargetID: e.TargetID,
		Result: string(e.Result), SourceIp: req.SourceIP, RequestID: req.ID,
		DetailsJson: details,
	})
	level := slog.LevelInfo
	if err != nil {
		level = slog.LevelError
	}
	w.log.Log(ctx, level, "audit",
		"audit_id", id, "action", e.Action, "result", string(e.Result),
		"actor_type", actor.Type, "actor_id", actor.ID, "actor_name", actor.Name,
		"target_type", e.TargetType, "target_id", e.TargetID,
		"request_id", req.ID, "source_ip", req.SourceIP, "details", json.RawMessage(details),
		"persist_error", errString(err))
	return err
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
