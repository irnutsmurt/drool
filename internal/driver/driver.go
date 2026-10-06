// Package driver defines the boundary between what to do on a host and how
// the host is reached (ARCHITECTURE-ALPHA §4). The SSH driver implements it
// in the alpha; the agent driver arrives in Alpha 2.
package driver

import (
	"errors"
	"io"
)

// Probe step statuses.
const (
	StepOK      = "ok"
	StepFailed  = "failed"
	StepSkipped = "skipped"
)

// ProbeStep is one check in a connection test.
type ProbeStep struct {
	Name       string `json:"name"`
	Status     string `json:"status"`
	Detail     string `json:"detail,omitempty"`
	ErrorClass string `json:"error_class,omitempty"`
	Hint       string `json:"hint,omitempty"`
}

// ProbeResult is the outcome of a connection test, without changing anything
// on the host.
type ProbeResult struct {
	OK    bool        `json:"ok"`
	Steps []ProbeStep `json:"steps"`
}

// FirstFailure returns the first failed step, or nil.
func (r *ProbeResult) FirstFailure() *ProbeStep {
	for i := range r.Steps {
		if r.Steps[i].Status == StepFailed {
			return &r.Steps[i]
		}
	}
	return nil
}

// OpResult is the outcome of an operation that ran to completion (whatever
// its exit status).
type OpResult struct {
	ExitCode *int
	Changed  *bool
	Data     map[string]any
}

// OutputSink receives an operation's output streams.
type OutputSink interface {
	Stdout() io.Writer
	Stderr() io.Writer
}

// NotStartedError marks a failure that happened before anything ran on the
// host, so even non-idempotent operations may be retried.
type NotStartedError struct{ Err error }

func (e *NotStartedError) Error() string { return e.Err.Error() }
func (e *NotStartedError) Unwrap() error { return e.Err }

// IsNotStarted reports whether err happened before the operation started.
func IsNotStarted(err error) bool {
	var ns *NotStartedError
	return errors.As(err, &ns)
}

// PhaseSink is implemented by output sinks that can show progress phases
// (rebooting, waiting_host, verifying) on the job's host row.
type PhaseSink interface {
	Phase(state string)
}

// SetPhase reports a phase if the sink supports it.
func SetPhase(out OutputSink, state string) {
	if p, ok := out.(PhaseSink); ok {
		p.Phase(state)
	}
}
