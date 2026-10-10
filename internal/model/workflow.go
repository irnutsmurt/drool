package model

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Controller-side workflow steps (M13). They run on the controller as part
// of a host's job, in order with the host's other steps, under the job's
// host lock: nothing is executed on the host for them, so they need no
// shell and never touch an agent's capability ceiling. A failed check
// fails that host's job like any failed step.
const (
	OpWait           = "wait"            // pause for a while (e.g. let services settle)
	OpVerifyHost     = "verify_host"     // the host answers on its management path
	OpVerifyServices = "verify_services" // the named systemd units are running
	OpVerifyHealth   = "verify_health"   // no active health issues of a severity
)

// ClassVerification: a verification step found the host not as expected.
const ClassVerification ErrorClass = "VerificationError"

// WaitOp pauses the host's run.
type WaitOp struct {
	Seconds int `json:"seconds"`
}

// VerifyHostOp checks that the host answers on its management path (the
// agent is connected, or an SSH connection test passes), retrying until
// TimeoutS.
type VerifyHostOp struct {
	TimeoutS int `json:"timeout_s"`
}

// VerifyServicesOp checks that each unit is active (running), from fresh
// monitoring data, retrying until TimeoutS.
type VerifyServicesOp struct {
	Services []string `json:"services"`
	TimeoutS int      `json:"timeout_s"`
}

// Health verification policies. Deliberately not an expression language.
const (
	HealthNoCritical = "no_critical" // no active Critical health issue
	HealthNoWarning  = "no_warning"  // no active Warning or Critical health issue
)

// VerifyHealthOp checks the host's active health issues (M11) after fresh
// data, retrying until TimeoutS.
type VerifyHealthOp struct {
	Policy   string `json:"policy"`
	TimeoutS int    `json:"timeout_s"`
}

// IsLocal reports whether an operation type runs on the controller.
func IsLocal(t string) bool {
	switch t {
	case OpWait, OpVerifyHost, OpVerifyServices, OpVerifyHealth, OpSSHCATrust:
		return true
	}
	return false
}

func (o *WaitOp) normalize() error {
	if o.Seconds < 1 || o.Seconds > 3600 {
		return errors.New("wait must be 1 second to 1 hour")
	}
	return nil
}

func (o *VerifyHostOp) normalize() error { return normTimeout(&o.TimeoutS, 300) }

func (o *VerifyServicesOp) normalize() error {
	if len(o.Services) == 0 {
		return errors.New("name at least one service to verify")
	}
	if len(o.Services) > 50 {
		return errors.New("at most 50 services per verification")
	}
	seen := map[string]bool{}
	var out []string
	for _, s := range o.Services {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if !unitNameRE.MatchString(s) {
			return fmt.Errorf("invalid service name %q", s)
		}
		if !strings.Contains(s, ".") {
			s += ".service"
		}
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		return errors.New("name at least one service to verify")
	}
	sort.Strings(out)
	o.Services = out
	if err := normTimeout(&o.TimeoutS, 120); err != nil {
		return err
	}
	if o.TimeoutS > 3600 {
		return errors.New("verification may wait at most 1 hour")
	}
	return nil
}

func (o *VerifyHealthOp) normalize() error {
	switch o.Policy {
	case "":
		o.Policy = HealthNoCritical
	case HealthNoCritical, HealthNoWarning:
	default:
		return errors.New(`policy must be "no_critical" or "no_warning"`)
	}
	if err := normTimeout(&o.TimeoutS, 120); err != nil {
		return err
	}
	if o.TimeoutS > 3600 {
		return errors.New("verification may wait at most 1 hour")
	}
	return nil
}

// --- rollout ---

// Rollout limits.
const (
	MaxCanaries  = 10
	MaxBatchSize = 1000
)

// Normalize validates the rollout part of a strategy.
func (s *Strategy) normalizeRollout() error {
	if s.CanaryCount < 0 || s.CanaryCount > MaxCanaries {
		return fmt.Errorf("canary_count must be 0–%d", MaxCanaries)
	}
	if len(s.CanaryHosts) > MaxCanaries {
		return fmt.Errorf("at most %d canary hosts", MaxCanaries)
	}
	if len(s.CanaryHosts) > 0 && s.CanaryCount > 0 {
		return errors.New("choose canary hosts or a canary count, not both")
	}
	seen := map[string]bool{}
	var hosts []string
	for _, h := range s.CanaryHosts {
		if h = strings.TrimSpace(h); h != "" && !seen[h] {
			seen[h] = true
			hosts = append(hosts, h)
		}
	}
	s.CanaryHosts = hosts
	if s.BatchSize < 0 || s.BatchSize > MaxBatchSize {
		return fmt.Errorf("batch_size must be 0–%d (0: all at once)", MaxBatchSize)
	}
	return nil
}

// HasCanary reports whether the strategy runs canaries first.
func (s Strategy) HasCanary() bool { return s.CanaryCount > 0 || len(s.CanaryHosts) > 0 }

// Wave plans: which hosts start together. Hosts come in their resolved
// order (by name); the plan is deterministic, so reviewing or restarting
// a job never reshuffles it.

// WavePlan is the rollout of one job.
type WavePlan struct {
	// Waves holds indexes into the eligible hosts, in execution order.
	Waves [][]int
	// Canary: Waves[0] is the canary wave.
	Canary bool
	// Note explains adjustments (explicit canaries no longer targeted).
	Note string
}

// PlanWaves splits hosts (already in their deterministic order) into
// waves: the canaries first (explicit hosts, or the first CanaryCount
// hosts), then the rest in batches of BatchSize (0: one batch).
func PlanWaves(hostIDs []string, s Strategy) WavePlan {
	var plan WavePlan
	used := map[int]bool{}
	var canary []int
	if len(s.CanaryHosts) > 0 {
		idx := map[string]int{}
		for i, h := range hostIDs {
			idx[h] = i
		}
		var missing int
		for _, h := range s.CanaryHosts {
			if i, ok := idx[h]; ok {
				canary = append(canary, i)
			} else {
				missing++
			}
		}
		sort.Ints(canary)
		if missing > 0 {
			plan.Note = fmt.Sprintf("%d chosen canary host(s) are not among this run's targets", missing)
			if len(canary) == 0 {
				// Keep a canary rather than silently dropping the safety net.
				n := min(len(s.CanaryHosts), len(hostIDs))
				for i := range n {
					canary = append(canary, i)
				}
				plan.Note += "; the first hosts by name are the canaries instead"
			}
		}
	} else if s.CanaryCount > 0 {
		for i := range min(s.CanaryCount, len(hostIDs)) {
			canary = append(canary, i)
		}
	}
	// A canary wave only makes sense with hosts after it.
	if len(canary) > 0 && len(canary) < len(hostIDs) {
		plan.Canary = true
		plan.Waves = append(plan.Waves, canary)
		for _, i := range canary {
			used[i] = true
		}
	} else if len(canary) > 0 {
		plan.Note = strings.TrimPrefix(plan.Note+"; every target is a canary, so there is no separate canary wave", "; ")
	}
	if plan.Note != "" { // shown as a sentence (previews, occurrence notes)
		plan.Note = strings.ToUpper(plan.Note[:1]) + plan.Note[1:]
	}
	var rest []int
	for i := range hostIDs {
		if !used[i] {
			rest = append(rest, i)
		}
	}
	size := s.BatchSize
	if size <= 0 {
		size = len(rest)
	}
	for len(rest) > 0 {
		n := min(size, len(rest))
		plan.Waves = append(plan.Waves, rest[:n])
		rest = rest[n:]
	}
	return plan
}
