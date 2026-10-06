// Package driver defines the boundary between what to do on a host and how
// the host is reached (ARCHITECTURE-ALPHA §4). The SSH driver implements it
// in the alpha; the agent driver arrives in Alpha 2.
package driver

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
