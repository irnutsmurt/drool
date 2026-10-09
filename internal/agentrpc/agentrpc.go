// Package agentrpc holds what the agent and controller share about the
// agent protocol beyond the generated protobuf code (internal/agentrpc/pb):
// protocol versions and negotiation, operation envelopes, channel frame
// kinds and file-operation messages (ARCHITECTURE-ALPHA §23.4).
package agentrpc

import (
	"encoding/json"
	"fmt"
	"time"

	"drool/internal/files"
	"drool/internal/model"
)

// Protocol versions. The protocol is versioned independently of the
// product: an agent and a controller of different releases interoperate as
// long as their ranges overlap. These are variables so tests can build
// binaries that speak other versions (-ldflags -X).
var (
	// ProtocolMin and ProtocolMax are what this build speaks. The
	// controller and the agent share them; the controller additionally
	// documents its supported range (ControllerMin/Max).
	ProtocolMin = "1"
	ProtocolMax = "1"
)

// Range is an inclusive protocol version range.
type Range struct{ Min, Max uint32 }

// Local returns this build's range.
func Local() Range {
	var min, max uint32
	fmt.Sscan(ProtocolMin, &min)
	fmt.Sscan(ProtocolMax, &max)
	if min == 0 {
		min = 1
	}
	if max < min {
		max = min
	}
	return Range{min, max}
}

// Negotiate picks the highest version both sides speak; 0 when the ranges
// don't overlap.
func Negotiate(a, b Range) uint32 {
	hi := min(a.Max, b.Max)
	lo := max(a.Min, b.Min)
	if hi < lo || hi == 0 {
		return 0
	}
	return hi
}

// Incompatibility explains a failed negotiation in plain words.
func Incompatibility(controller, agent Range) string {
	if agent.Max < controller.Min {
		return fmt.Sprintf("the agent is too old: it speaks protocol %d–%d, this controller needs %d–%d; update the agent", agent.Min, agent.Max, controller.Min, controller.Max)
	}
	return fmt.Sprintf("the agent is newer than this controller: it speaks protocol %d–%d, this controller supports %d–%d; upgrade the controller", agent.Min, agent.Max, controller.Min, controller.Max)
}

// --- operations ---

// OpEnvelope is what the controller sends to run one operation. Every
// command carries a unique id, an issue time and an expiry (DESIGN §9);
// state-changing domains also carry their state version.
type OpEnvelope struct {
	OpID      string           `json:"op_id"`
	IssuedAt  time.Time        `json:"issued_at"`
	ExpiresAt time.Time        `json:"expires_at"`
	Epoch     string           `json:"controller_epoch"`
	State     *StateVersion    `json:"state_version,omitempty"`
	Operation *model.Operation `json:"operation,omitempty"` // nil for file sessions
	TimeoutS  int              `json:"timeout_s"`
	JobID     string           `json:"job_id,omitempty"`
	HostID    string           `json:"host_id,omitempty"`
	// Purpose is "op" (run Operation) or "files" (a files.FS session for
	// the file manager or a file deployment; Operation is empty).
	Purpose string `json:"purpose"`
	// Summary is a short, non-secret description for the agent's own log
	// (e.g. the deployment's path).
	Summary string `json:"summary,omitempty"`
	// Query is a read-only state query (Purpose "query", M14).
	Query *StateQuery `json:"query,omitempty"`
}

// StateQuery asks for the state of named resources without changing
// anything (desired state, M14). Result data "packages": {"manager",
// "versions": name → version ("" not installed)}.
type StateQuery struct {
	Packages []string `json:"packages,omitempty"`
}

// MaxQueryPackages bounds one query.
const MaxQueryPackages = 500

// Purposes.
const (
	PurposeOp       = "op"
	PurposeFiles    = "files"
	PurposeSimulate = "simulate" // package/system-update dry run; result data "simulation"
	PurposeQuery    = "query"    // read-only state query (StateQuery); agents before M14 refuse it
)

// StateVersion is a managed state domain's version (DESIGN §9). Revisions
// are controller-owned: agents store and report them, never advance them.
type StateVersion struct {
	Domain      string `json:"domain"`
	Epoch       string `json:"epoch"`
	Revision    uint64 `json:"revision"`
	ContentHash string `json:"content_hash"`
}

// Rejection reasons for an operation that never started.
const (
	RejectExpired    = "expired"
	RejectDuplicate  = "duplicate"
	RejectStale      = "stale_revision"
	RejectEpoch      = "epoch_mismatch"
	RejectCapability = "capability"
	RejectInvalid    = "invalid"
	RejectBusy       = "busy"
)

// Rejection is the agent's refusal before anything ran.
type Rejection struct {
	Reason     string `json:"reason"`
	Message    string `json:"message"`
	Capability string `json:"capability,omitempty"`
}

// OpResult is an operation's outcome as the agent reports it.
type OpResult struct {
	ExitCode *int           `json:"exit_code,omitempty"`
	Changed  *bool          `json:"changed,omitempty"`
	Data     map[string]any `json:"data,omitempty"`
	Error    *OpError       `json:"error,omitempty"`
}

// OpError carries a model.OpError across the wire.
type OpError struct {
	Class     string `json:"class"`
	Message   string `json:"message"`
	Hint      string `json:"hint,omitempty"`
	Retryable bool   `json:"retryable,omitempty"`
}

// ToWire converts an error for transport.
func ToWire(err error) *OpError {
	if err == nil {
		return nil
	}
	oe := model.AsOpError(err)
	msg := oe.Message
	if msg == "" {
		msg = oe.Error()
	}
	return &OpError{Class: string(oe.Class), Message: msg, Hint: oe.Hint, Retryable: oe.Retryable}
}

// Err converts back.
func (e *OpError) Err() error {
	if e == nil {
		return nil
	}
	return &model.OpError{Class: model.ErrorClass(e.Class), Message: e.Message, Hint: e.Hint, Retryable: e.Retryable}
}

// Channel frame kinds.
const (
	FrameOpen     = "open"     // agent → controller: first frame, identifies the channel
	FrameOp       = "op"       // controller → agent: OpEnvelope
	FrameAccepted = "accepted" // agent: recorded, may start (from here on, outcome may be unknown)
	FrameRejected = "rejected" // agent: Rejection; nothing ran
	FrameStarted  = "started"
	FrameStdout   = "stdout"
	FrameStderr   = "stderr"
	FramePhase    = "phase"
	FrameResult   = "result" // agent: OpResult
	FrameCancel   = "cancel" // controller
	FrameFSReq    = "fs_req"
	FrameFSResp   = "fs_resp"
	FrameData     = "data"
	FrameEOF      = "eof"
)

// --- file operations (files.FS over a channel) ---

// FSRequest is one files.FS call.
type FSRequest struct {
	Method    string              `json:"method"`
	Path      string              `json:"path,omitempty"`
	To        string              `json:"to,omitempty"`
	Mode      uint32              `json:"mode,omitempty"`
	Owner     string              `json:"owner,omitempty"`
	Group     string              `json:"group,omitempty"`
	Recursive bool                `json:"recursive,omitempty"`
	Stage     *files.StageOptions `json:"stage,omitempty"`
	Pre       *files.Precondition `json:"pre,omitempty"`
	Command   string              `json:"command,omitempty"`
	// Op is the deployment's envelope for staged writes and validation in
	// a file deployment (capability checks need the whole operation).
	OpID string `json:"op_id,omitempty"`
}

// FSResponse answers an FSRequest.
type FSResponse struct {
	Error      *FSError                `json:"error,omitempty"`
	Info       *files.Info             `json:"info,omitempty"`
	Infos      []files.Info            `json:"infos,omitempty"`
	Path       string                  `json:"path,omitempty"`
	SHA256     string                  `json:"sha256,omitempty"`
	Size       int64                   `json:"size,omitempty"`
	Caps       *files.Caps             `json:"caps,omitempty"`
	Installed  *files.Installed        `json:"installed,omitempty"`
	Validation *files.ValidationResult `json:"validation,omitempty"`
	Output     string                  `json:"output,omitempty"`
}

// FSError carries a files.Error or capability denial.
type FSError struct {
	Code       string `json:"code"`
	Message    string `json:"message"`
	Hint       string `json:"hint,omitempty"`
	Capability string `json:"capability,omitempty"`
}

// MustJSON marshals v or panics (only for types that can't fail).
func MustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}
