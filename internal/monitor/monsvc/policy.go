package monsvc

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

// Health configuration (M12.1). The Recommended profile below is what
// every host gets with no configuration. Overrides change it sparsely, at
// four scopes, the most specific winning:
//
//	subject (one filesystem, disk, GPU, sensor, interface, service or
//	probe target of one host) → host → group → global → Recommended
//
// A service (a unit name means the same on every host) can also be set
// for a group's hosts or every host, and within each scope the service's
// own setting comes before the scope's check-wide one: service on the host
// → host → service in a group → group → service everywhere → global
// (final After-M17 batch: the expected state of services).
//
// An override stores only the fields it changes; everything else is
// inherited from the next scope. When a host is in several groups that set
// the same field, the group with the higher monitoring priority wins;
// equal priorities with different values are a conflict: the field then
// ignores those groups (using the global or Recommended value) and the
// conflict is reported until a priority, a host or a subject override
// settles it. Nothing depends on names, ids or query order.
//
// This decides what M11 considers unhealthy. Who gets notified about it
// is M12's routing, configured separately.

// CheckGPUUtil is sustained high GPU utilization: off in Recommended (a
// busy GPU is usually working, not failing).
const CheckGPUUtil = "gpu_utilization"

// Override fields.
const (
	FieldEnabled       = "enabled"
	FieldWarning       = "warning"
	FieldCritical      = "critical"
	FieldDuration      = "duration_min"
	FieldClear         = "clear"
	FieldClearDuration = "clear_duration_min"
	FieldIntervals     = "missed_intervals"
	// FieldExpected is a unit's expected state (Expect*), in JSON by name.
	FieldExpected = "expected"
)

var fieldOrder = []string{FieldExpected, FieldEnabled, FieldWarning, FieldCritical, FieldDuration, FieldClear, FieldClearDuration, FieldIntervals}

// Expected service states (FieldExpected). Monitoring a service is
// opt-in (dogfooding correction batch): without a setting a unit is
// inventory only, its state shown and never a health issue. Running or
// stopped means it is monitored.
const (
	ExpectAuto    = 0
	ExpectRunning = 1 // stopped (or not installed) is a problem; failed is critical
	ExpectStopped = 2 // running is a problem; stopped or failed is what was wanted
	ExpectIgnored = 3 // shown as it is, never a health issue
)

var expectNames = map[float64]string{ExpectRunning: "running", ExpectStopped: "stopped", ExpectIgnored: "ignored"}

// ExpectName is an expected state's name ("" for none: not monitored).
func ExpectName(v float64) string { return expectNames[v] }

// ExpectValue parses an expected state's name.
func ExpectValue(name string) (float64, bool) {
	for v, n := range expectNames {
		if n == name {
			return v, true
		}
	}
	return 0, false
}

// Subject kinds: what a subject override of a check binds to.
const (
	SubjectFilesystem = "filesystem"
	SubjectDisk       = "disk"
	SubjectGPU        = "gpu"
	SubjectSensor     = "sensor"
	SubjectInterface  = "interface"
	SubjectService    = "service"
	SubjectProbe      = "probe"
)

// FieldSpec says what one field of a check means and accepts.
type FieldSpec struct {
	Name  string  `json:"name"`
	Label string  `json:"label"`
	Type  string  `json:"type"`           // bool | percent | celsius | minutes | count
	Unit  string  `json:"unit,omitempty"` // %, °C, min, ×
	Min   float64 `json:"min"`
	Max   float64 `json:"max"`
	Help  string  `json:"help,omitempty"`
	// Options are a choice field's values (type "choice").
	Options []FieldOption `json:"options,omitempty"`
}

// FieldOption is one value of a choice field.
type FieldOption struct {
	Value string `json:"value"`
	Label string `json:"label"`
	Help  string `json:"help,omitempty"`
}

// CheckSpec is one health check and the fields it honestly supports (a
// SMART failure has no numeric threshold; a filesystem has no duration
// because two readings in a row decide it).
type CheckSpec struct {
	ID          string      `json:"id"`
	Label       string      `json:"label"`
	Category    string      `json:"category"`
	Description string      `json:"description"`
	Subject     string      `json:"subject,omitempty"` // subject kind for subject overrides
	Fields      []FieldSpec `json:"fields"`
	// Informational checks never alert.
	Info bool `json:"info,omitempty"`
	// DefaultOff: not evaluated unless someone enables it.
	DefaultOff bool `json:"default_off,omitempty"`
}

func fEnabled() FieldSpec {
	return FieldSpec{Name: FieldEnabled, Label: "Check enabled", Type: "bool", Min: 0, Max: 1}
}
func fPct(name, label string) FieldSpec {
	return FieldSpec{Name: name, Label: label, Type: "percent", Unit: "%", Min: 1, Max: 100}
}
func fMin(name, label string) FieldSpec {
	return FieldSpec{Name: name, Label: label, Type: "minutes", Unit: "min", Min: 0, Max: 1440}
}
func fTemp(name, label string) FieldSpec {
	return FieldSpec{Name: name, Label: label, Type: "celsius", Unit: "°C", Min: 1, Max: 200}
}
func fExpected() FieldSpec {
	return FieldSpec{Name: FieldExpected, Label: "Expected state", Type: "choice", Min: ExpectRunning, Max: ExpectIgnored, Options: []FieldOption{
		{Value: "running", Label: "Running", Help: "Stopped or missing is a problem; failed is critical."},
		{Value: "stopped", Label: "Stopped", Help: "Running is a problem; stopped or failed is what you want."},
		{Value: "ignored", Label: "Ignored", Help: "Not monitored, even where a pack or every-host setting monitors it: its state stays visible but never raises a health issue or alert."},
	}}
}

var checkSpecs = []CheckSpec{
	{ID: CheckCPU, Label: "High CPU utilization", Category: "cpu", Description: "All CPUs busy for a sustained time; momentary spikes never count.",
		Fields: []FieldSpec{fEnabled(), fPct(FieldWarning, "Warning at"), fMin(FieldDuration, "For at least"),
			fPct(FieldClear, "Clears below"), fMin(FieldClearDuration, "Clears after")}},
	{ID: CheckMemory, Label: "Memory pressure", Category: "memory", Description: "Memory in use, excluding reclaimable cache.",
		Fields: []FieldSpec{fEnabled(), fPct(FieldWarning, "Warning at"), fPct(FieldCritical, "Critical at"), fMin(FieldDuration, "For at least"),
			fPct(FieldClear, "Clears below"), fMin(FieldClearDuration, "Clears after")}},
	{ID: CheckFilesystem, Label: "Disk space", Category: "disk", Subject: SubjectFilesystem,
		Description: "How full a filesystem is; two readings in a row decide it. Physical disk health is a separate check.",
		Fields:      []FieldSpec{fEnabled(), fPct(FieldWarning, "Warning at"), fPct(FieldCritical, "Critical at"), fPct(FieldClear, "Clears below")}},
	{ID: CheckInodes, Label: "Filesystem inodes", Category: "disk", Subject: SubjectFilesystem, Description: "Running out of inodes (files can't be created even with free space).",
		Fields: []FieldSpec{fEnabled(), fPct(FieldWarning, "Warning at"), fPct(FieldCritical, "Critical at"), fPct(FieldClear, "Clears below")}},
	{ID: CheckFSReadOnly, Label: "Filesystem turned read-only", Category: "storage", Subject: SubjectFilesystem,
		Description: "A filesystem that was writable earlier in this boot became read-only.", Fields: []FieldSpec{fEnabled()}},
	{ID: CheckDeviceReadOnly, Label: "Device turned read-only", Category: "storage", Subject: SubjectDisk,
		Description: "A disk or card the kernel or device switched to read-only.", Fields: []FieldSpec{fEnabled()}},
	{ID: CheckStorageHealth, Label: "Disk health (SMART / NVMe)", Category: "storage", Subject: SubjectDisk,
		Description: "SMART failure, NVMe critical warnings and wear-out, as the device reports them.", Fields: []FieldSpec{fEnabled()}},
	{ID: CheckMediaErrors, Label: "New media errors", Category: "storage", Subject: SubjectDisk,
		Description: "Error counters (reallocated or pending sectors, media errors) that grew.", Fields: []FieldSpec{fEnabled()}},
	{ID: CheckKernelStorage, Label: "Kernel storage errors", Category: "storage", Description: "IO errors, resets and timeouts the kernel logs.", Fields: []FieldSpec{fEnabled()}},
	{ID: CheckProbe, Label: "Write/read probe", Category: "storage", Subject: SubjectProbe, Description: "The active write/read probe of a filesystem failed.",
		Fields: []FieldSpec{fEnabled()}},
	{ID: CheckServiceExpected, Label: "Expected state", Category: "services", Subject: SubjectService,
		Description: "Whether DROOL monitors a service, and the state it should be in. Without a setting a service isn't monitored: its state is shown, never a health issue. Set per host, or for a pack's hosts or every host.",
		Fields:      []FieldSpec{fExpected()}},
	{ID: CheckServiceFailed, Label: "Service failed", Category: "services", Subject: SubjectService,
		Description: "A unit expected to run failed (a service nobody monitors never alerts).", Fields: []FieldSpec{fEnabled(), fMin(FieldDuration, "Failed for at least")}},
	{ID: CheckServiceStopped, Label: "Service not running", Category: "services", Subject: SubjectService,
		Description: "A unit expected to run stayed stopped (or isn't installed).", Fields: []FieldSpec{fEnabled(), fMin(FieldDuration, "Stopped for at least")}},
	{ID: CheckServiceUnexpected, Label: "Service running unexpectedly", Category: "services", Subject: SubjectService,
		Description: "A unit expected to be stopped is running.", Fields: []FieldSpec{fEnabled(), fMin(FieldDuration, "Running for at least")}},
	{ID: CheckInterfaceDown, Label: "Network interface down", Category: "network", Subject: SubjectInterface,
		Description: "A physical interface that was up earlier in this boot went down.", Fields: []FieldSpec{fEnabled(), fMin(FieldDuration, "Down for at least")}},
	{ID: CheckTemperature, Label: "Temperature", Category: "temperature", Subject: SubjectSensor,
		Description: "Judged against each sensor's own hardware limits; a custom limit replaces them only where you set one.",
		Fields: []FieldSpec{fEnabled(), fTemp(FieldWarning, "Warning at"), fTemp(FieldCritical, "Critical at"), fMin(FieldDuration, "For at least"),
			fTemp(FieldClear, "Clears below"), fMin(FieldClearDuration, "Clears after")}},
	{ID: CheckGPUUtil, Label: "High GPU utilization", Category: "gpu", Subject: SubjectGPU, DefaultOff: true,
		Description: "Off by default: a busy GPU is usually working, not unhealthy. Enable it where sustained saturation means trouble. GPU temperature is a separate check.",
		Fields: []FieldSpec{fEnabled(), fPct(FieldWarning, "Warning at"), fMin(FieldDuration, "For at least"),
			fPct(FieldClear, "Clears below"), fMin(FieldClearDuration, "Clears after")}},
	{ID: CheckStale, Label: "No monitoring data", Category: "stale", Description: "The host stopped reporting.",
		Fields: []FieldSpec{fEnabled(), {Name: FieldIntervals, Label: "After missed reports", Type: "count", Unit: "×", Min: 2, Max: 20}}},
	{ID: CheckFidelity, Label: "Reduced monitoring fidelity", Category: "fidelity", Info: true,
		Description: "The agent is saving resources (informational; never an alert).", Fields: []FieldSpec{fEnabled()}},
}

var specByID = func() map[string]*CheckSpec {
	m := map[string]*CheckSpec{}
	for i := range checkSpecs {
		m[checkSpecs[i].ID] = &checkSpecs[i]
	}
	return m
}()

// HealthChecks lists the checks with their capabilities.
func HealthChecks() []CheckSpec { return append([]CheckSpec(nil), checkSpecs...) }

// Spec returns a check's spec.
func Spec(check string) (*CheckSpec, bool) {
	s, ok := specByID[check]
	return s, ok
}

func (c *CheckSpec) field(name string) (FieldSpec, bool) {
	for _, f := range c.Fields {
		if f.Name == name {
			return f, true
		}
	}
	return FieldSpec{}, false
}

// recommended are the built-in values (enabled as 1/0). Temperature has no
// numeric defaults: the hardware's own limits apply. Clear levels are
// derived from the warning level (clearMargin) unless set.
var recommended = map[string]map[string]float64{
	CheckCPU:               {FieldEnabled: 1, FieldWarning: 95, FieldDuration: 5, FieldClearDuration: 3},
	CheckMemory:            {FieldEnabled: 1, FieldWarning: 90, FieldCritical: 97, FieldDuration: 5, FieldClearDuration: 3},
	CheckFilesystem:        {FieldEnabled: 1, FieldWarning: 85, FieldCritical: 95},
	CheckInodes:            {FieldEnabled: 1, FieldWarning: 90, FieldCritical: 97},
	CheckFSReadOnly:        {FieldEnabled: 1},
	CheckDeviceReadOnly:    {FieldEnabled: 1},
	CheckStorageHealth:     {FieldEnabled: 1},
	CheckMediaErrors:       {FieldEnabled: 1},
	CheckKernelStorage:     {FieldEnabled: 1},
	CheckProbe:             {FieldEnabled: 1},
	CheckServiceFailed:     {FieldEnabled: 1, FieldDuration: 1},
	CheckServiceStopped:    {FieldEnabled: 1, FieldDuration: 2},
	CheckServiceUnexpected: {FieldEnabled: 1, FieldDuration: 2},
	CheckInterfaceDown:     {FieldEnabled: 1, FieldDuration: 1},
	CheckTemperature:       {FieldEnabled: 1, FieldDuration: 2, FieldClearDuration: 2},
	CheckGPUUtil:           {FieldEnabled: 0, FieldWarning: 95, FieldDuration: 30, FieldClearDuration: 5},
	CheckStale:             {FieldEnabled: 1, FieldIntervals: 3},
	CheckFidelity:          {FieldEnabled: 1},
}

var clearMargin = map[string]float64{CheckCPU: 10, CheckMemory: 5, CheckFilesystem: 2, CheckInodes: 2, CheckTemperature: 3, CheckGPUUtil: 10}

// --- sparse field sets ---

// Fields is a sparse set of overridden fields. "enabled" is a boolean in
// JSON and 1/0 here.
type Fields map[string]float64

// MarshalJSON writes enabled as a boolean.
func (f Fields) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	first := true
	for _, k := range fieldOrder {
		v, ok := f[k]
		if !ok {
			continue
		}
		if !first {
			b.WriteByte(',')
		}
		first = false
		fmt.Fprintf(&b, "%q:", k)
		if k == FieldEnabled {
			fmt.Fprint(&b, v != 0)
		} else if k == FieldExpected {
			fmt.Fprintf(&b, "%q", ExpectName(v))
		} else {
			nb, _ := json.Marshal(v)
			b.Write(nb)
		}
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

// UnmarshalJSON reads enabled as a boolean and the rest as numbers; null
// means "not overridden".
func (f *Fields) UnmarshalJSON(b []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	out := Fields{}
	for k, v := range raw {
		if string(v) == "null" {
			continue
		}
		if k == FieldEnabled {
			var on bool
			if err := json.Unmarshal(v, &on); err != nil {
				return fmt.Errorf("%s must be true or false", k)
			}
			out[k] = 0
			if on {
				out[k] = 1
			}
			continue
		}
		if k == FieldExpected {
			var name string
			if err := json.Unmarshal(v, &name); err != nil {
				return fmt.Errorf("%s must be running, stopped or ignored", k)
			}
			x, ok := ExpectValue(name)
			if !ok {
				return fmt.Errorf("%s must be running, stopped or ignored, not %q", k, name)
			}
			out[k] = x
			continue
		}
		var n float64
		if err := json.Unmarshal(v, &n); err != nil {
			return fmt.Errorf("%s must be a number", k)
		}
		out[k] = n
	}
	*f = out
	return nil
}

func (f Fields) has(k string) bool { _, ok := f[k]; return ok }

func (f Fields) clone() Fields {
	out := Fields{}
	for k, v := range f {
		out[k] = v
	}
	return out
}

// Validate checks a field set against the check's capabilities.
func (f Fields) Validate(check string) error {
	spec, ok := Spec(check)
	if !ok {
		return fmt.Errorf("unknown check %q", check)
	}
	var errs []error
	for k, v := range f {
		fs, ok := spec.field(k)
		if !ok {
			errs = append(errs, fmt.Errorf("%s has no %q setting", spec.Label, k))
			continue
		}
		if math.IsNaN(v) || math.IsInf(v, 0) || v < fs.Min || v > fs.Max {
			errs = append(errs, fmt.Errorf("%s: %s must be %g–%g%s", spec.Label, strings.ToLower(fs.Label), fs.Min, fs.Max, fs.Unit))
			continue
		}
		if (fs.Type == "minutes" || fs.Type == "count" || fs.Type == "choice") && v != math.Trunc(v) {
			errs = append(errs, fmt.Errorf("%s: %s must be a whole number", spec.Label, strings.ToLower(fs.Label)))
		}
	}
	if f.has(FieldWarning) && f.has(FieldCritical) && f[FieldWarning] > f[FieldCritical] {
		errs = append(errs, errors.New("the warning level must not exceed the critical level"))
	}
	if f.has(FieldClear) && f.has(FieldWarning) && f[FieldClear] >= f[FieldWarning] {
		errs = append(errs, errors.New("the clear level must be below the warning level"))
	}
	return errors.Join(errs...)
}

// --- stored overrides ---

// Scopes.
const (
	ScopeGlobal = "global"
	ScopeGroup  = "group"
	ScopeHost   = "host"
)

// Override is one stored override row.
type Override struct {
	ID           string `json:"id"`
	Scope        string `json:"scope"`
	GroupID      string `json:"group_id,omitempty"`
	GroupName    string `json:"group_name,omitempty"`
	HostID       string `json:"host_id,omitempty"`
	HostName     string `json:"host_name,omitempty"`
	Check        string `json:"check"`
	Subject      string `json:"subject,omitempty"`
	SubjectLabel string `json:"subject_label,omitempty"`
	Fields       Fields `json:"fields"`
	UpdatedBy    string `json:"updated_by,omitempty"`
	UpdatedAt    int64  `json:"updated_at"`
	CreatedAt    int64  `json:"created_at"`
}

type groupInfo struct {
	ID, Name string
	Priority int
}

// policy is a snapshot of every health setting (bounded: rows are few,
// groups and members scale with the fleet).
type policy struct {
	gen     int64
	global  map[string]Fields
	groups  map[string]*groupInfo
	groupOv map[string]map[string]Fields // group → check → fields
	members map[string][]string          // host → groups
	host    map[string]map[string]Fields
	subj    map[string]map[string]map[string]Fields // host → check → subject → fields
	// Services set for a group's hosts or every host.
	groupSubj  map[string]map[string]map[string]Fields // group → check → subject → fields
	globalSubj map[string]map[string]Fields            // check → subject → fields
	rows       []Override
}

func emptyPolicy() *policy {
	return &policy{global: map[string]Fields{}, groups: map[string]*groupInfo{}, groupOv: map[string]map[string]Fields{},
		members: map[string][]string{}, host: map[string]map[string]Fields{}, subj: map[string]map[string]map[string]Fields{},
		groupSubj: map[string]map[string]map[string]Fields{}, globalSubj: map[string]map[string]Fields{}}
}

const maxOverrides = 20000

func readGen(ctx context.Context, db *sql.DB) int64 {
	var g int64
	if err := db.QueryRowContext(ctx, `SELECT gen FROM mon_health_gen WHERE id = 1`).Scan(&g); err != nil {
		return -1
	}
	return g
}

func loadPolicy(ctx context.Context, db *sql.DB) (*policy, error) {
	p := emptyPolicy()
	p.gen = readGen(ctx, db)
	rows, err := db.QueryContext(ctx, `SELECT g.id, g.name, ifnull(h.priority, 0) FROM groups g LEFT JOIN mon_group_health h ON h.group_id = g.id`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var g groupInfo
		if err := rows.Scan(&g.ID, &g.Name, &g.Priority); err != nil {
			rows.Close()
			return nil, err
		}
		p.groups[g.ID] = &g
	}
	rows.Close()
	rows, err = db.QueryContext(ctx, `SELECT group_id, host_id FROM group_members ORDER BY host_id, group_id`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var g, h string
		if err := rows.Scan(&g, &h); err != nil {
			rows.Close()
			return nil, err
		}
		p.members[h] = append(p.members[h], g)
	}
	rows.Close()
	rows, err = db.QueryContext(ctx, `SELECT o.id, o.scope, ifnull(o.group_id, ''), ifnull(g.name, ''), ifnull(o.host_id, ''), ifnull(h.name, ''),
		o.check_id, o.subject, o.subject_label, o.fields_json, ifnull(u.username, ''), o.updated_at, o.created_at
		FROM mon_health_overrides o LEFT JOIN groups g ON g.id = o.group_id LEFT JOIN hosts h ON h.id = o.host_id
		LEFT JOIN users u ON u.id = o.updated_by ORDER BY o.scope, o.check_id, o.id LIMIT ?`, maxOverrides)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var o Override
		var fj string
		if err := rows.Scan(&o.ID, &o.Scope, &o.GroupID, &o.GroupName, &o.HostID, &o.HostName, &o.Check, &o.Subject, &o.SubjectLabel,
			&fj, &o.UpdatedBy, &o.UpdatedAt, &o.CreatedAt); err != nil {
			rows.Close()
			return nil, err
		}
		if json.Unmarshal([]byte(fj), &o.Fields) != nil || o.Fields == nil {
			o.Fields = Fields{}
		}
		p.add(o)
	}
	rows.Close()
	return p, nil
}

func (p *policy) add(o Override) {
	if _, ok := Spec(o.Check); !ok {
		return // a check this version doesn't know (kept, ignored)
	}
	p.rows = append(p.rows, o)
	switch {
	case o.Scope == ScopeGlobal && o.Subject != "":
		if p.globalSubj[o.Check] == nil {
			p.globalSubj[o.Check] = map[string]Fields{}
		}
		p.globalSubj[o.Check][o.Subject] = o.Fields
	case o.Scope == ScopeGlobal:
		p.global[o.Check] = o.Fields
	case o.Scope == ScopeGroup && o.Subject != "":
		if p.groupSubj[o.GroupID] == nil {
			p.groupSubj[o.GroupID] = map[string]map[string]Fields{}
		}
		if p.groupSubj[o.GroupID][o.Check] == nil {
			p.groupSubj[o.GroupID][o.Check] = map[string]Fields{}
		}
		p.groupSubj[o.GroupID][o.Check][o.Subject] = o.Fields
	case o.Scope == ScopeGroup:
		if p.groupOv[o.GroupID] == nil {
			p.groupOv[o.GroupID] = map[string]Fields{}
		}
		p.groupOv[o.GroupID][o.Check] = o.Fields
	case o.Scope == ScopeHost:
		if o.Subject == "" {
			if p.host[o.HostID] == nil {
				p.host[o.HostID] = map[string]Fields{}
			}
			p.host[o.HostID][o.Check] = o.Fields
			return
		}
		if p.subj[o.HostID] == nil {
			p.subj[o.HostID] = map[string]map[string]Fields{}
		}
		if p.subj[o.HostID][o.Check] == nil {
			p.subj[o.HostID][o.Check] = map[string]Fields{}
		}
		p.subj[o.HostID][o.Check][o.Subject] = o.Fields
	}
}

// --- resolution ---

// Resolution levels, least specific first.
const (
	levelRecommended = iota
	levelGlobal
	levelGroup
	levelHost
	levelSubject
	levelAll = levelSubject + 1
)

// GroupRef names a group in a provenance or a conflict.
type GroupRef struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Priority int      `json:"priority"`
	Value    *float64 `json:"value,omitempty"`
}

// Provenance says where an effective value comes from.
type Provenance struct {
	// recommended | hardware | derived | global | group | host | subject | unset
	Kind         string     `json:"kind"`
	Label        string     `json:"label"`
	Groups       []GroupRef `json:"groups,omitempty"` // the group(s) that set it
	Subject      string     `json:"subject,omitempty"`
	SubjectLabel string     `json:"subject_label,omitempty"`
}

// Conflict is groups of equal priority setting different values.
type Conflict struct {
	Priority int        `json:"priority"`
	Groups   []GroupRef `json:"groups"`
}

// Resolved is one field's effective value.
type Resolved struct {
	Value    *float64   `json:"value"` // nil: no value (no hardware limit, or no critical level)
	Source   Provenance `json:"source"`
	Conflict *Conflict  `json:"conflict,omitempty"`
}

// hwLimits are a temperature sensor's own thresholds.
type hwLimits struct{ high, crit *float64 }

func (h *hwLimits) warn() *float64 {
	if h == nil {
		return nil
	}
	if h.high != nil {
		return h.high
	}
	return h.crit
}

func fp(v float64) *float64 { return &v }

// subjectLabels, by key, label subject provenance.
type subjectCtx struct {
	keys  []string // most trusted identity first
	label string
}

// resolve computes one field below level upto (exclusive): levelAll for
// the effective value, a scope's own level for what that scope inherits.
func (p *policy) resolve(host, check string, sc subjectCtx, hw *hwLimits, field string, upto int) Resolved {
	var conflict *Conflict
	if upto > levelSubject {
		for _, k := range sc.keys {
			if f, ok := p.subj[host][check][k]; ok && f.has(field) {
				lbl := sc.label
				if lbl == "" {
					lbl = k
				}
				return Resolved{Value: fp(f[field]), Source: Provenance{Kind: "subject", Label: "Custom for " + lbl, Subject: k, SubjectLabel: sc.label}}
			}
		}
	}
	if upto > levelHost {
		if f, ok := p.host[host][check]; ok && f.has(field) {
			return Resolved{Value: fp(f[field]), Source: Provenance{Kind: "host", Label: "Custom for this host"}}
		}
	}
	if upto > levelGroup {
		var cands []GroupRef
		top := math.MinInt
		for _, g := range p.members[host] {
			gi := p.groups[g]
			if gi == nil {
				continue
			}
			// The group's setting for the service, else its check-wide one.
			f, ok := p.groupSubjFields(g, check, sc, field)
			if !ok {
				f, ok = p.groupOv[g][check]
			}
			if !ok || !f.has(field) {
				continue
			}
			cands = append(cands, GroupRef{ID: g, Name: gi.Name, Priority: gi.Priority, Value: fp(f[field])})
			top = max(top, gi.Priority)
		}
		var win []GroupRef
		for _, c := range cands {
			if c.Priority == top {
				win = append(win, c)
			}
		}
		sort.Slice(win, func(i, j int) bool {
			return win[i].Name < win[j].Name || win[i].Name == win[j].Name && win[i].ID < win[j].ID
		})
		if len(win) > 0 {
			same := true
			for _, w := range win[1:] {
				if *w.Value != *win[0].Value {
					same = false
				}
			}
			if same {
				names := make([]string, len(win))
				for i, w := range win {
					names[i] = w.Name
				}
				lbl := "Inherited from pack: " + names[0]
				if len(win) > 1 {
					lbl = "Inherited from packs: " + strings.Join(names, ", ")
				}
				return Resolved{Value: fp(*win[0].Value), Source: Provenance{Kind: "group", Label: lbl, Groups: win}}
			}
			conflict = &Conflict{Priority: top, Groups: win}
		}
	}
	r := p.resolveBase(check, field, hw, upto, sc)
	if conflict != nil {
		r.Conflict = conflict
		names := make([]string, len(conflict.Groups))
		for i, g := range conflict.Groups {
			names[i] = g.Name
		}
		r.Source.Label = fmt.Sprintf("Conflict: groups %s (priority %d) disagree; using %s", strings.Join(names, ", "), conflict.Priority,
			strings.ToLower(r.Source.Label[:1])+r.Source.Label[1:])
	}
	return r
}

// groupSubjFields is a group's setting for the subject that has field.
func (p *policy) groupSubjFields(group, check string, sc subjectCtx, field string) (Fields, bool) {
	for _, k := range sc.keys {
		if f, ok := p.groupSubj[group][check][k]; ok && f.has(field) {
			return f, true
		}
	}
	return nil, false
}

func (p *policy) resolveBase(check, field string, hw *hwLimits, upto int, sc subjectCtx) Resolved {
	if upto > levelGlobal {
		for _, k := range sc.keys {
			if f, ok := p.globalSubj[check][k]; ok && f.has(field) {
				return Resolved{Value: fp(f[field]), Source: Provenance{Kind: "global", Label: "Global custom setting"}}
			}
		}
		if f, ok := p.global[check]; ok && f.has(field) {
			return Resolved{Value: fp(f[field]), Source: Provenance{Kind: "global", Label: "Global custom setting"}}
		}
	}
	if check == CheckTemperature && (field == FieldWarning || field == FieldCritical) {
		if hw == nil { // not one sensor (the fleet, a group, a host): each sensor's own
			return Resolved{Source: Provenance{Kind: "hardware", Label: "Each sensor's hardware limit"}}
		}
		var v *float64
		if field == FieldWarning {
			v = hw.warn()
		} else {
			v = hw.crit
		}
		if v != nil {
			return Resolved{Value: fp(*v), Source: Provenance{Kind: "hardware", Label: "Hardware-reported limit"}}
		}
		return Resolved{Source: Provenance{Kind: "unset", Label: "No hardware limit reported"}}
	}
	if v, ok := recommended[check][field]; ok {
		return Resolved{Value: fp(v), Source: Provenance{Kind: "recommended", Label: "Recommended default"}}
	}
	return Resolved{Source: Provenance{Kind: "unset", Label: "Not used"}}
}

// effective resolves every field of a check (with the clear level derived
// from the warning level when nobody set one).
func (p *policy) effective(host, check string, sc subjectCtx, hw *hwLimits, upto int) map[string]Resolved {
	spec, ok := Spec(check)
	if !ok {
		return nil
	}
	out := map[string]Resolved{}
	for _, f := range spec.Fields {
		out[f.Name] = p.resolve(host, check, sc, hw, f.Name, upto)
	}
	if m, ok := clearMargin[check]; ok {
		c, w := out[FieldClear], out[FieldWarning]
		if w.Value == nil && (c.Value == nil || c.Source.Kind == "unset") {
			out[FieldClear] = Resolved{Source: Provenance{Kind: "derived", Label: fmt.Sprintf("Derived: %g below the limit in use", m)}, Conflict: c.Conflict}
		}
		if w.Value != nil && (c.Value == nil || c.Source.Kind == "unset" || *c.Value >= *w.Value) {
			lbl := fmt.Sprintf("Derived: %g below the warning level", m)
			if c.Value != nil && c.Source.Kind != "unset" {
				lbl += " (the set clear level wasn't below the warning level)"
			}
			out[FieldClear] = Resolved{Value: fp(*w.Value - m), Source: Provenance{Kind: "derived", Label: lbl}, Conflict: c.Conflict}
		}
	}
	return out
}

// rule is what the evaluator applies to one check of one subject.
type rule struct {
	on              bool
	warn, crit, clr *float64
	dur, clearDur   time.Duration
	intervals       int
	warnKind        string // provenance of the warning level (hardware | subject | …)
	warnSource      string // its label
	enabledSource   string
}

func (p *policy) rule(host, check string, sc subjectCtx, hw *hwLimits) rule {
	e := p.effective(host, check, sc, hw, levelAll)
	r := rule{}
	if v := e[FieldEnabled].Value; v != nil && *v != 0 {
		r.on = true
	}
	r.enabledSource = e[FieldEnabled].Source.Label
	r.warn, r.crit, r.clr = e[FieldWarning].Value, e[FieldCritical].Value, e[FieldClear].Value
	r.warnKind, r.warnSource = e[FieldWarning].Source.Kind, e[FieldWarning].Source.Label
	if v := e[FieldDuration].Value; v != nil {
		r.dur = time.Duration(*v) * time.Minute
	}
	if v := e[FieldClearDuration].Value; v != nil {
		r.clearDur = time.Duration(*v) * time.Minute
	}
	if v := e[FieldIntervals].Value; v != nil {
		r.intervals = int(*v)
	}
	if r.warn != nil && r.crit != nil && *r.crit < *r.warn {
		r.crit = r.warn // mixed scopes: critical never below warning
	}
	return r
}

// sig is everything that affects one check on one host: a change means
// the check was reconfigured there.
func (p *policy) sig(host, check string) string {
	var b strings.Builder
	w := func(f Fields) {
		j, _ := json.Marshal(f)
		b.Write(j)
		b.WriteByte(';')
	}
	w(p.global[check])
	gs := append([]string(nil), p.members[host]...)
	sort.Strings(gs)
	for _, g := range gs {
		if f, ok := p.groupOv[g][check]; ok {
			fmt.Fprintf(&b, "g%s:%d:", g, p.groups[g].Priority)
			w(f)
		}
	}
	w(p.host[host][check])
	subs := make([]string, 0, len(p.subj[host][check]))
	for k := range p.subj[host][check] {
		subs = append(subs, k)
	}
	sort.Strings(subs)
	for _, k := range subs {
		b.WriteString(k)
		w(p.subj[host][check][k])
	}
	// Single services set for the host's groups and for every host.
	subjects := func(m map[string]Fields) {
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			b.WriteString(k)
			w(m[k])
		}
	}
	for _, g := range gs {
		if m := p.groupSubj[g][check]; len(m) > 0 {
			fmt.Fprintf(&b, "gs%s:%d:", g, p.groups[g].Priority)
			subjects(m)
		}
	}
	b.WriteString("all:")
	subjects(p.globalSubj[check])
	// The service checks follow each unit's expected state.
	if check == CheckServiceStopped || check == CheckServiceFailed || check == CheckServiceUnexpected {
		b.WriteString("expected:")
		b.WriteString(p.sig(host, CheckServiceExpected))
	}
	return b.String()
}

// expected is a unit's expected state on a host (ExpectAuto without a
// setting) and where it comes from.
func (p *policy) expected(host, unit string) (float64, Resolved) {
	r := p.resolve(host, CheckServiceExpected, unitCtx(unit), nil, FieldExpected, levelAll)
	if r.Value == nil {
		return ExpectAuto, r
	}
	return *r.Value, r
}

// isWatched reports whether a unit is expected to run on a host.
func (p *policy) isWatched(host, unit string) bool {
	v, _ := p.expected(host, unit)
	return v == ExpectRunning
}

// expectedUnits lists the units with an expected state on a host
// (running, stopped or ignored), from any scope.
func (p *policy) expectedUnits(host string) []string {
	cand := map[string]bool{}
	for k := range p.subj[host][CheckServiceExpected] {
		cand[k] = true
	}
	for _, g := range p.members[host] {
		for k := range p.groupSubj[g][CheckServiceExpected] {
			cand[k] = true
		}
	}
	for k := range p.globalSubj[CheckServiceExpected] {
		cand[k] = true
	}
	out := []string{}
	for k := range cand {
		unit := strings.TrimPrefix(k, "unit:")
		if v, _ := p.expected(host, unit); v != ExpectAuto {
			out = append(out, unit)
		}
	}
	sort.Strings(out)
	return out
}

// expectedRunning lists the units expected to run on a host, from any
// scope (one the host doesn't report isn't installed).
func (p *policy) expectedRunning(host string) []string {
	cand := map[string]bool{}
	for k := range p.subj[host][CheckServiceExpected] {
		cand[k] = true
	}
	for _, g := range p.members[host] {
		for k := range p.groupSubj[g][CheckServiceExpected] {
			cand[k] = true
		}
	}
	for k := range p.globalSubj[CheckServiceExpected] {
		cand[k] = true
	}
	out := []string{}
	for k := range cand {
		unit := strings.TrimPrefix(k, "unit:")
		if p.isWatched(host, unit) {
			out = append(out, unit)
		}
	}
	sort.Strings(out)
	return out
}

// counts summarises what applies to a host.
type policyCounts struct {
	Global, Group, Host, Subject, Conflicts int
}

func (p *policy) counts(host string) policyCounts {
	var c policyCounts
	for _, f := range p.global {
		c.Global += len(f)
	}
	for _, g := range p.members[host] {
		for _, f := range p.groupOv[g] {
			c.Group += len(f)
		}
	}
	for _, f := range p.host[host] {
		c.Host += len(f)
	}
	for _, m := range p.subj[host] {
		for _, f := range m {
			c.Subject += len(f)
		}
	}
	for _, spec := range checkSpecs {
		for _, f := range spec.Fields {
			if p.resolve(host, spec.ID, subjectCtx{}, nil, f.Name, levelAll).Conflict != nil {
				c.Conflicts++
			}
		}
	}
	return c
}
