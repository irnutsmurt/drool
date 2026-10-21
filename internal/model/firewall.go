package model

import (
	"fmt"
	"time"
)

// Firewall operation types.
const (
	// OpFirewallApply is the job step: apply the host's current compiled
	// policy (or remove DROOL's policy) as a dead-man transaction
	// (DESIGN §17). The policy itself is computed when the step runs.
	OpFirewallApply = "firewall_apply"
	// OpFirewall is internal (controller → agent): one action of the host
	// firewall tooling. Never accepted in job specs.
	OpFirewall = "firewall"
)

// FirewallApplyOp configures a firewall transaction.
type FirewallApplyOp struct {
	// Remove takes DROOL's policy off the host (still a transaction).
	Remove bool `json:"remove,omitempty"`
	// OverrideMgmt is the audited emergency override (DESIGN §18): apply
	// even though a deny removes a management path. Requires
	// firewall.override; the dead-man rollback still applies.
	OverrideMgmt bool `json:"override_mgmt,omitempty"`
	// OverrideTimeout accepts a rollback timeout shorter than 3x the
	// measured connection time (DESIGN §17.4).
	OverrideTimeout bool `json:"override_timeout,omitempty"`
	// AllowDrift replaces DROOL rules that were changed outside DROOL.
	AllowDrift bool `json:"allow_drift,omitempty"`
	// ExpectRulesHash, when set, refuses to apply if the policy changed
	// since it was previewed.
	ExpectRulesHash string `json:"expect_rules_hash,omitempty"`
	// ExpectRulesHashes is ExpectRulesHash per host (host ID → rules hash),
	// for several hosts reviewed together.
	ExpectRulesHashes map[string]string `json:"expect_rules_hashes,omitempty"`
	// SetEnforceHosts are the hosts this apply switches to Enforce mode
	// when they aren't in it: those whose review showed the switch and where
	// the person chose Enforce. The mode changes only once every check
	// before the transaction has passed, and is audited; a host refused
	// before that keeps its mode. Any other host not in Enforce mode is
	// refused.
	SetEnforceHosts []string `json:"set_enforce_hosts,omitempty"`
	TimeoutS        int      `json:"timeout_s"`
	When            *When    `json:"when,omitempty"`
}

// FirewallOp is one action of the on-host firewall tooling (drool-fw).
type FirewallOp struct {
	Kind           string    `json:"kind"` // detect | status | snapshot | apply | commit | rollback | sets | rebase
	TxID           string    `json:"tx_id,omitempty"`
	Epoch          string    `json:"epoch,omitempty"`
	FromEpoch      string    `json:"from_epoch,omitempty"` // rebase: the epoch the committed state must have
	Revision       int64     `json:"revision,omitempty"`
	Hash           string    `json:"hash,omitempty"`
	TimeoutS       int       `json:"timeout_s,omitempty"` // rollback timeout (apply)
	Expires        time.Time `json:"expires,omitempty"`
	AllowDrift     bool      `json:"allow_drift,omitempty"`
	SetsGeneration int64     `json:"sets_generation,omitempty"`
	RulesHash      string    `json:"rules_hash,omitempty"`
	Candidate      string    `json:"candidate,omitempty"`
	Delta          string    `json:"delta,omitempty"`
	Full           string    `json:"full,omitempty"`
	Reason         string    `json:"reason,omitempty"`
}

// Mutating reports whether the action changes the host.
func (o FirewallOp) Mutating() bool {
	switch o.Kind {
	case "detect", "status", "snapshot":
		return false
	}
	return true
}

func (o *FirewallOp) normalize() error {
	switch o.Kind {
	case "detect", "status", "snapshot", "apply", "commit", "rollback", "sets", "rebase":
		return nil
	}
	return fmt.Errorf("unknown firewall action %q", o.Kind)
}

func (o *FirewallApplyOp) normalize() error {
	if o.TimeoutS == 0 {
		o.TimeoutS = 900
	}
	return normTimeout(&o.TimeoutS, 900)
}
