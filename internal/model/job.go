package model

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// SpecVersion is the current job spec schema version.
const SpecVersion = 1

// Operation types.
const (
	OpCommand = "command"
	OpScript  = "script"
)

// LockMode is the host lock an operation needs (ARCHITECTURE-ALPHA §11.3).
type LockMode int

const (
	LockNone LockMode = iota
	LockShared
	LockExclusive
)

// Operation is one typed step. Exactly one of the typed fields is set,
// matching Type.
type Operation struct {
	Type          string
	Command       *CommandOp
	Script        *ScriptOp
	Package       *PackageOp
	SystemUpdate  *SystemUpdateOp
	Service       *ServiceOp
	Reboot        *RebootOp
	FileDeploy    *FileDeployOp
	FirewallApply *FirewallApplyOp
	Firewall      *FirewallOp
	// Controller-side workflow steps (M13).
	Wait           *WaitOp
	VerifyHost     *VerifyHostOp
	VerifyServices *VerifyServicesOp
	VerifyHealth   *VerifyHealthOp
	// Controller-side SSH certificate authority trust (M16).
	SSHCATrust *SSHCATrustOp
	// Controller-side setup of DROOL's own SSH account (After M17).
	SSHAccount *SSHAccountOp
}

// CommandOp runs a single shell command line.
type CommandOp struct {
	Command  string `json:"command"`
	Become   bool   `json:"become"`
	ReadOnly bool   `json:"read_only"`
	TimeoutS int    `json:"timeout_s"`
	When     *When  `json:"when,omitempty"`
}

// ScriptOp runs a script body with an interpreter.
type ScriptOp struct {
	Interpreter string     `json:"interpreter"` // sh | bash
	Body        ScriptBody `json:"body"`
	Become      bool       `json:"become"`
	ReadOnly    bool       `json:"read_only"`
	TimeoutS    int        `json:"timeout_s"`
	When        *When      `json:"when,omitempty"`
}

// Limits.
const (
	MaxCommandBytes = 64 << 10
	MaxScriptBytes  = 1 << 20
	MaxTimeoutS     = 24 * 3600
	MaxSteps        = 10
	MaxConcurrency  = 100
)

func (o Operation) MarshalJSON() ([]byte, error) {
	var body any
	switch o.Type {
	case OpCommand:
		body = struct {
			Type string `json:"type"`
			*CommandOp
		}{o.Type, o.Command}
	case OpScript:
		body = struct {
			Type string `json:"type"`
			*ScriptOp
		}{o.Type, o.Script}
	case OpPackage:
		body = struct {
			Type string `json:"type"`
			*PackageOp
		}{o.Type, o.Package}
	case OpSystemUpdate:
		body = struct {
			Type string `json:"type"`
			*SystemUpdateOp
		}{o.Type, o.SystemUpdate}
	case OpService:
		body = struct {
			Type string `json:"type"`
			*ServiceOp
		}{o.Type, o.Service}
	case OpReboot:
		body = struct {
			Type string `json:"type"`
			*RebootOp
		}{o.Type, o.Reboot}
	case OpFileDeploy:
		body = struct {
			Type string `json:"type"`
			*FileDeployOp
		}{o.Type, o.FileDeploy}
	case OpFirewallApply:
		body = struct {
			Type string `json:"type"`
			*FirewallApplyOp
		}{o.Type, o.FirewallApply}
	case OpFirewall:
		body = struct {
			Type string `json:"type"`
			*FirewallOp
		}{o.Type, o.Firewall}
	case OpWait:
		body = struct {
			Type string `json:"type"`
			*WaitOp
		}{o.Type, o.Wait}
	case OpVerifyHost:
		body = struct {
			Type string `json:"type"`
			*VerifyHostOp
		}{o.Type, o.VerifyHost}
	case OpVerifyServices:
		body = struct {
			Type string `json:"type"`
			*VerifyServicesOp
		}{o.Type, o.VerifyServices}
	case OpVerifyHealth:
		body = struct {
			Type string `json:"type"`
			*VerifyHealthOp
		}{o.Type, o.VerifyHealth}
	case OpSSHCATrust:
		body = struct {
			Type string `json:"type"`
			*SSHCATrustOp
		}{o.Type, o.SSHCATrust}
	case OpSSHAccount:
		body = struct {
			Type string `json:"type"`
			*SSHAccountOp
		}{o.Type, o.SSHAccount}
	default:
		return nil, fmt.Errorf("unknown operation type %q", o.Type)
	}
	return json.Marshal(body)
}

func (o *Operation) UnmarshalJSON(data []byte) error {
	var head struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(data, &head); err != nil {
		return err
	}
	strict := func(v any) error {
		// Strip "type" and decode the rest strictly.
		var m map[string]json.RawMessage
		if err := json.Unmarshal(data, &m); err != nil {
			return err
		}
		delete(m, "type")
		rest, _ := json.Marshal(m)
		dec := json.NewDecoder(bytes.NewReader(rest))
		dec.DisallowUnknownFields()
		return dec.Decode(v)
	}
	o.Type = head.Type
	switch head.Type {
	case OpCommand:
		o.Command = &CommandOp{}
		return strict(o.Command)
	case OpScript:
		o.Script = &ScriptOp{}
		return strict(o.Script)
	case OpPackage:
		o.Package = &PackageOp{}
		return strict(o.Package)
	case OpSystemUpdate:
		o.SystemUpdate = &SystemUpdateOp{}
		return strict(o.SystemUpdate)
	case OpService:
		o.Service = &ServiceOp{}
		return strict(o.Service)
	case OpReboot:
		o.Reboot = &RebootOp{}
		return strict(o.Reboot)
	case OpFileDeploy:
		o.FileDeploy = &FileDeployOp{}
		return strict(o.FileDeploy)
	case OpFirewallApply:
		o.FirewallApply = &FirewallApplyOp{}
		return strict(o.FirewallApply)
	case OpFirewall:
		o.Firewall = &FirewallOp{}
		return strict(o.Firewall)
	case OpWait:
		o.Wait = &WaitOp{}
		return strict(o.Wait)
	case OpVerifyHost:
		o.VerifyHost = &VerifyHostOp{}
		return strict(o.VerifyHost)
	case OpVerifyServices:
		o.VerifyServices = &VerifyServicesOp{}
		return strict(o.VerifyServices)
	case OpVerifyHealth:
		o.VerifyHealth = &VerifyHealthOp{}
		return strict(o.VerifyHealth)
	case OpSSHCATrust:
		o.SSHCATrust = &SSHCATrustOp{}
		return strict(o.SSHCATrust)
	case OpSSHAccount:
		o.SSHAccount = &SSHAccountOp{}
		return strict(o.SSHAccount)
	case "":
		return errors.New("operation type is required")
	default:
		return fmt.Errorf("unknown operation type %q (supported: command, script, package, system_update, service, reboot, file_deploy, firewall_apply, wait, verify_host, verify_services, verify_health, ssh_ca_trust)", head.Type)
	}
}

// Normalize applies defaults and validates the operation.
func (o *Operation) Normalize() error {
	switch o.Type {
	case OpCommand:
		c := o.Command
		c.Command = strings.TrimSpace(c.Command)
		if c.Command == "" {
			return errors.New("command must not be empty")
		}
		if len(c.Command) > MaxCommandBytes {
			return fmt.Errorf("command is longer than %d bytes", MaxCommandBytes)
		}
		if err := c.When.normalize(); err != nil {
			return err
		}
		return normTimeout(&c.TimeoutS, 300)
	case OpScript:
		s := o.Script
		if s.Interpreter == "" {
			s.Interpreter = "sh"
		}
		if s.Interpreter != "sh" && s.Interpreter != "bash" {
			return errors.New("interpreter must be sh or bash")
		}
		if err := s.Body.normalize(); err != nil {
			return err
		}
		if err := s.When.normalize(); err != nil {
			return err
		}
		return normTimeout(&s.TimeoutS, 600)
	case OpPackage:
		return o.Package.normalize()
	case OpSystemUpdate:
		return o.SystemUpdate.normalize()
	case OpService:
		return o.Service.normalize()
	case OpReboot:
		return o.Reboot.normalize()
	case OpFileDeploy:
		return o.FileDeploy.normalize()
	case OpFirewallApply:
		if err := o.FirewallApply.When.normalize(); err != nil {
			return err
		}
		return o.FirewallApply.normalize()
	case OpFirewall:
		return o.Firewall.normalize()
	case OpWait:
		return o.Wait.normalize()
	case OpVerifyHost:
		return o.VerifyHost.normalize()
	case OpVerifyServices:
		return o.VerifyServices.normalize()
	case OpVerifyHealth:
		return o.VerifyHealth.normalize()
	case OpSSHCATrust:
		return o.SSHCATrust.normalize()
	case OpSSHAccount:
		return o.SSHAccount.normalize()
	}
	return fmt.Errorf("unknown operation type %q", o.Type)
}

func normTimeout(t *int, def int) error {
	if *t == 0 {
		*t = def
	}
	if *t < 1 || *t > MaxTimeoutS {
		return fmt.Errorf("timeout_s must be between 1 and %d", MaxTimeoutS)
	}
	return nil
}

// Meta describes engine-relevant properties of an operation.
type Meta struct {
	Lock       LockMode
	Idempotent bool
	Become     bool
	TimeoutS   int
}

// Meta returns the operation's metadata (ARCHITECTURE-ALPHA §5.1 table).
func (o Operation) Meta() Meta {
	switch o.Type {
	case OpCommand:
		m := Meta{Lock: LockExclusive, Become: o.Command.Become, TimeoutS: o.Command.TimeoutS}
		if o.Command.ReadOnly {
			m.Lock = LockShared
		}
		return m
	case OpScript:
		m := Meta{Lock: LockExclusive, Become: o.Script.Become, TimeoutS: o.Script.TimeoutS}
		if o.Script.ReadOnly {
			m.Lock = LockShared
		}
		return m
	case OpPackage:
		return Meta{Lock: LockExclusive, Idempotent: true, Become: true, TimeoutS: o.Package.TimeoutS}
	case OpSystemUpdate:
		return Meta{Lock: LockExclusive, Idempotent: o.SystemUpdate.Reboot == "never", Become: true, TimeoutS: o.SystemUpdate.TimeoutS}
	case OpService:
		idem := o.Service.State != "restarted" && o.Service.State != "reloaded"
		return Meta{Lock: LockExclusive, Idempotent: idem, Become: true, TimeoutS: o.Service.TimeoutS}
	case OpReboot:
		return Meta{Lock: LockExclusive, Become: true, TimeoutS: o.Reboot.TimeoutS}
	case OpFileDeploy:
		// Deploying the same content again is a no-op; a service restart
		// is not, so only then is a retry unsafe.
		idem := o.FileDeploy.Service == nil || o.FileDeploy.Service.Action == "reload"
		return Meta{Lock: LockExclusive, Idempotent: idem, Become: o.FileDeploy.Elevated, TimeoutS: o.FileDeploy.TimeoutS}
	case OpFirewallApply:
		// Never retried automatically: a transaction whose outcome is
		// unknown is resolved by asking the host, not by applying again.
		return Meta{Lock: LockExclusive, Become: true, TimeoutS: o.FirewallApply.TimeoutS}
	case OpFirewall:
		return Meta{Lock: LockNone, Become: true, TimeoutS: 300}
	case OpWait:
		return Meta{Lock: LockShared, Idempotent: true, TimeoutS: o.Wait.Seconds + 30}
	case OpVerifyHost:
		return Meta{Lock: LockShared, Idempotent: true, TimeoutS: o.VerifyHost.TimeoutS + 60}
	case OpVerifyServices:
		return Meta{Lock: LockShared, Idempotent: true, TimeoutS: o.VerifyServices.TimeoutS + 180}
	case OpVerifyHealth:
		return Meta{Lock: LockShared, Idempotent: true, TimeoutS: o.VerifyHealth.TimeoutS + 180}
	case OpSSHCATrust:
		// Writing the same trust again changes nothing; sshd is reloaded
		// only when a file changed.
		return Meta{Lock: LockExclusive, Idempotent: true, Become: true, TimeoutS: 300}
	case OpSSHAccount:
		// Never retried automatically: after a lost connection DROOL
		// doesn't know how far the setup got; running it again (by hand)
		// checks what is there and continues. Slow hosts (emulated ones
		// take a minute to sign in) get room for the setup, the proof and
		// the connection test after the switch.
		return Meta{Lock: LockExclusive, Become: true, TimeoutS: 600}
	}
	return Meta{Lock: LockExclusive}
}

// Summary is a short human description for listings.
func (o Operation) Summary() string {
	switch o.Type {
	case OpCommand:
		return o.Command.Command
	case OpScript:
		if o.Script.Body.ByOS != nil {
			return o.Script.Interpreter + " script (" + strings.Join(o.Script.Body.Keys(), "/") + ")"
		}
		// The first line that says something: not a shebang, comment or "set -e".
		first := ""
		for _, l := range strings.Split(o.Script.Body.Text, "\n") {
			l = strings.TrimSpace(l)
			if l != "" && !strings.HasPrefix(l, "#") && !strings.HasPrefix(l, "set -") {
				first = l
				break
			}
		}
		if first == "" {
			return o.Script.Interpreter + " script"
		}
		return o.Script.Interpreter + " script: " + first
	case OpPackage:
		return o.Package.Action + " " + strings.Join(o.Package.Names, " ")
	case OpSystemUpdate:
		s := map[string]string{"upgrade": "System update", "full_upgrade": "Full system upgrade", "security": "Security updates"}[o.SystemUpdate.Mode]
		if o.SystemUpdate.Reboot != "never" {
			s += map[string]string{"if_required": " (reboot if required)", "always": " and reboot"}[o.SystemUpdate.Reboot]
		}
		return s
	case OpService:
		var parts []string
		if o.Service.State != "" {
			parts = append(parts, map[string]string{"running": "start", "stopped": "stop", "restarted": "restart", "reloaded": "reload"}[o.Service.State])
		}
		if o.Service.Enabled != nil {
			parts = append(parts, map[bool]string{true: "enable", false: "disable"}[*o.Service.Enabled])
		}
		return strings.Join(parts, " + ") + " " + o.Service.Name
	case OpReboot:
		return "Reboot"
	case OpFileDeploy:
		switch {
		case o.FileDeploy.RollbackOf != "" && o.FileDeploy.Remove:
			return "Roll back " + o.FileDeploy.Path + " (remove)"
		case o.FileDeploy.RollbackOf != "":
			return "Roll back " + o.FileDeploy.Path
		}
		return "Deploy " + o.FileDeploy.Path
	case OpFirewallApply:
		if o.FirewallApply.Remove {
			return "Remove DROOL firewall policy"
		}
		return "Apply firewall policy"
	case OpFirewall:
		return "firewall " + o.Firewall.Kind
	case OpWait:
		return fmt.Sprintf("Wait %s", secondsText(o.Wait.Seconds))
	case OpVerifyHost:
		return "Verify the host is reachable"
	case OpVerifyServices:
		return "Verify services: " + strings.Join(o.VerifyServices.Services, ", ")
	case OpVerifyHealth:
		if o.VerifyHealth.Policy == HealthNoWarning {
			return "Verify health: no Warning or Critical issues"
		}
		return "Verify health: no Critical issues"
	case OpSSHCATrust:
		if o.SSHCATrust.Action == "remove" {
			return "Remove trust in DROOL's SSH certificate authority"
		}
		return "Trust DROOL's SSH certificate authority"
	case OpSSHAccount:
		return "Set up DROOL's own SSH account (" + o.SSHAccount.User + ")"
	}
	return o.Type
}

func secondsText(s int) string {
	switch {
	case s%3600 == 0:
		return fmt.Sprintf("%d h", s/3600)
	case s%60 == 0:
		return fmt.Sprintf("%d min", s/60)
	}
	return fmt.Sprintf("%d s", s)
}

// TargetSpec selects hosts: union of hosts, groups and tag matches, minus
// excluded hosts (ARCHITECTURE-ALPHA §5.2).
type TargetSpec struct {
	Hosts        []string `json:"hosts"`
	Groups       []string `json:"groups"`
	Tags         []string `json:"tags"`
	ExcludeHosts []string `json:"exclude_hosts"`
}

// Empty reports whether nothing is selected.
func (t TargetSpec) Empty() bool {
	return len(t.Hosts) == 0 && len(t.Groups) == 0 && len(t.Tags) == 0
}

// Strategy controls rollout. Hosts run in waves: optional canaries first
// (the job pauses if one fails), then batches of BatchSize hosts (0: all
// remaining at once); a wave starts only when the previous one finished.
// Concurrency limits hosts running at once inside a wave. Reaching
// FailureThreshold failures stops starting hosts and pauses the job.
type Strategy struct {
	Concurrency      int      `json:"concurrency"`
	FailureThreshold *int     `json:"failure_threshold"`
	CanaryCount      int      `json:"canary_count,omitempty"`
	CanaryHosts      []string `json:"canary_hosts,omitempty"`
	BatchSize        int      `json:"batch_size,omitempty"`
}

// JobSpec is the canonical job definition.
type JobSpec struct {
	Name     string      `json:"name"`
	Steps    []Operation `json:"steps"`
	Targets  TargetSpec  `json:"targets"`
	Strategy Strategy    `json:"strategy"`
}

// MaxCorrectiveSteps bounds a desired-state correction (M14): one step
// per resource that differs somewhere among its hosts; each host runs only
// the steps it needs (engine.SubmitOptions.HostSteps).
const MaxCorrectiveSteps = 50

// Normalize validates the spec and applies defaults.
func (j *JobSpec) Normalize() error { return j.NormalizeLimit(MaxSteps) }

// NormalizeLimit is Normalize with another step limit (corrections).
func (j *JobSpec) NormalizeLimit(maxSteps int) error { return j.normalize(maxSteps, false) }

// NormalizeInternal is Normalize for a job DROOL itself submits: it may
// be DROOL's own SSH account setup, alone in its job.
func (j *JobSpec) NormalizeInternal() error { return j.normalize(MaxSteps, true) }

func (j *JobSpec) normalize(maxSteps int, internal bool) error {
	if len(j.Steps) == 0 {
		return errors.New("a job needs at least one step")
	}
	if len(j.Steps) > maxSteps {
		return fmt.Errorf("a job may have at most %d steps", maxSteps)
	}
	for i := range j.Steps {
		t := j.Steps[i].Type
		if t == OpFirewall || t == OpSSHAccount && !internal {
			return fmt.Errorf("step %d: %w", i+1, internalOpError(t))
		}
		if t == OpSSHAccount && (len(j.Steps) != 1 || len(j.Targets.Hosts) != 1 || len(j.Targets.Groups)+len(j.Targets.Tags) > 0) {
			return errors.New("DROOL's own SSH account is set up on one host at a time, in a job of its own")
		}
		if err := j.Steps[i].Normalize(); err != nil {
			return fmt.Errorf("step %d: %w", i+1, err)
		}
	}
	j.Name = strings.TrimSpace(j.Name)
	if j.Name == "" {
		j.Name = j.Steps[0].Summary()
	}
	if len(j.Name) > 120 {
		j.Name = j.Name[:117] + "..."
	}
	if j.Targets.Empty() {
		return errors.New("select at least one target host, group or tag")
	}
	if j.Strategy.Concurrency == 0 {
		j.Strategy.Concurrency = 10
	}
	if j.Strategy.Concurrency < 1 || j.Strategy.Concurrency > MaxConcurrency {
		return fmt.Errorf("concurrency must be between 1 and %d", MaxConcurrency)
	}
	if t := j.Strategy.FailureThreshold; t != nil && *t < 1 {
		return errors.New("failure_threshold must be at least 1")
	}
	return j.Strategy.normalizeRollout()
}

// NormalizeSteps validates steps without targets (workflows).
func NormalizeSteps(steps []Operation) error {
	if len(steps) == 0 {
		return errors.New("add at least one step")
	}
	if len(steps) > MaxSteps {
		return fmt.Errorf("at most %d steps", MaxSteps)
	}
	for i := range steps {
		if internalOp(steps[i].Type) {
			return fmt.Errorf("step %d: %w", i+1, internalOpError(steps[i].Type))
		}
		if err := steps[i].Normalize(); err != nil {
			return fmt.Errorf("step %d: %w", i+1, err)
		}
	}
	return nil
}

// NormalizeStrategy validates a strategy on its own (workflow defaults,
// schedules).
func NormalizeStrategy(s *Strategy) error {
	j := JobSpec{Steps: []Operation{{Type: OpWait, Wait: &WaitOp{Seconds: 1}}}, Targets: TargetSpec{Hosts: []string{"x"}}, Strategy: *s}
	if err := j.Normalize(); err != nil {
		return err
	}
	*s = j.Strategy
	return nil
}

// LockMode is the strongest lock any step needs.
func (j JobSpec) LockMode() LockMode {
	m := LockNone
	for _, s := range j.Steps {
		if l := s.Meta().Lock; l > m {
			m = l
		}
	}
	return m
}
