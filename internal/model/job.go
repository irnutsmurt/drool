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
	Type    string
	Command *CommandOp
	Script  *ScriptOp
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
	case "":
		return errors.New("operation type is required")
	default:
		return fmt.Errorf("unknown operation type %q (supported: command, script)", head.Type)
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
		first, _, _ := strings.Cut(strings.TrimSpace(o.Script.Body.Text), "\n")
		return o.Script.Interpreter + " script: " + first
	}
	return o.Type
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

// Strategy controls rollout.
type Strategy struct {
	Concurrency      int  `json:"concurrency"`
	FailureThreshold *int `json:"failure_threshold"`
}

// JobSpec is the canonical job definition.
type JobSpec struct {
	Name     string      `json:"name"`
	Steps    []Operation `json:"steps"`
	Targets  TargetSpec  `json:"targets"`
	Strategy Strategy    `json:"strategy"`
}

// Normalize validates the spec and applies defaults.
func (j *JobSpec) Normalize() error {
	if len(j.Steps) == 0 {
		return errors.New("a job needs at least one step")
	}
	if len(j.Steps) > MaxSteps {
		return fmt.Errorf("a job may have at most %d steps", MaxSteps)
	}
	for i := range j.Steps {
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
