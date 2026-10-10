package monsvc

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"drool/internal/ids"
)

// Health configuration: reading, changing and explaining overrides
// (M12.1). The API layer validates permissions and audits; everything that
// decides a value lives here, so the browser never re-implements it.

// Errors.
var (
	ErrOverrideNotFound = errors.New("override not found")
	ErrBadOverride      = errors.New("invalid override")
)

// ApplyHealthConfig makes a configuration change take effect now: the
// evaluator reloads it and re-judges the affected hosts (tests and the API
// call it right after a change; otherwise the next evaluation notices).
func (s *Service) ApplyHealthConfig(ctx context.Context) { s.eval.current(ctx) }

// Overrides lists stored overrides (optionally of one scope, group or
// host), most general first.
func (s *Service) Overrides(ctx context.Context, scope, groupID, hostID, check string) []Override {
	p := s.eval.current(ctx)
	out := []Override{}
	for _, o := range p.rows {
		if scope != "" && o.Scope != scope || groupID != "" && o.GroupID != groupID || hostID != "" && o.HostID != hostID || check != "" && o.Check != check {
			continue
		}
		out = append(out, o)
	}
	return out
}

// OverrideByID returns one override.
func (s *Service) OverrideByID(ctx context.Context, id string) (Override, error) {
	for _, o := range s.eval.current(ctx).rows {
		if o.ID == id {
			return o, nil
		}
	}
	return Override{}, ErrOverrideNotFound
}

// OverrideKey names the row an override lives in.
type OverrideKey struct {
	Scope   string `json:"scope"`
	GroupID string `json:"group_id,omitempty"`
	HostID  string `json:"host_id,omitempty"`
	Check   string `json:"check"`
	Subject string `json:"subject,omitempty"`
}

// Validate checks the key's shape.
func (k OverrideKey) Validate() error {
	spec, ok := Spec(k.Check)
	if !ok {
		return fmt.Errorf("%w: unknown check %q", ErrBadOverride, k.Check)
	}
	switch k.Scope {
	case ScopeGlobal:
		if k.GroupID != "" || k.HostID != "" || k.Subject != "" {
			return fmt.Errorf("%w: a global override has no group, host or subject", ErrBadOverride)
		}
	case ScopeGroup:
		if k.GroupID == "" || k.HostID != "" || k.Subject != "" {
			return fmt.Errorf("%w: a group override names a group (subjects are per host)", ErrBadOverride)
		}
	case ScopeHost:
		if k.HostID == "" || k.GroupID != "" {
			return fmt.Errorf("%w: a host override names a host", ErrBadOverride)
		}
		if k.Subject != "" && spec.Subject == "" {
			return fmt.Errorf("%w: %s has no individual subjects", ErrBadOverride, spec.Label)
		}
		if k.Subject != "" && !validSubjectKey(k.Check, k.Subject) {
			return fmt.Errorf("%w: %q is not a %s subject", ErrBadOverride, k.Subject, spec.Subject)
		}
	default:
		return fmt.Errorf("%w: scope must be global, group or host", ErrBadOverride)
	}
	return nil
}

func (k OverrideKey) find(p *policy) (Override, bool) {
	for _, o := range p.rows {
		if o.Scope == k.Scope && o.GroupID == k.GroupID && o.HostID == k.HostID && o.Check == k.Check && o.Subject == k.Subject {
			return o, true
		}
	}
	return Override{}, false
}

// Current returns the stored override for a key, if any.
func (s *Service) Current(ctx context.Context, k OverrideKey) (Override, bool) {
	return k.find(s.eval.current(ctx))
}

// FieldChange is one field's before and after (nil: inherited).
type FieldChange struct {
	Before *float64 `json:"before"`
	After  *float64 `json:"after"`
}

// Diff lists changed fields.
func Diff(before, after Fields) map[string]FieldChange {
	out := map[string]FieldChange{}
	for _, k := range fieldOrder {
		b, bok := before[k]
		a, aok := after[k]
		if bok == aok && b == a {
			continue
		}
		var c FieldChange
		if bok {
			c.Before = fp(b)
		}
		if aok {
			c.After = fp(a)
		}
		out[k] = c
	}
	return out
}

// PutOverrideTx stores a key's complete sparse field set (an empty set
// removes the row: everything inherited). It returns the row before and
// after (nil when absent). The caller audits.
func (s *Service) PutOverrideTx(ctx context.Context, tx *sql.Tx, k OverrideKey, fields Fields, label, by string, now int64) (before, after *Override, err error) {
	if err := k.Validate(); err != nil {
		return nil, nil, err
	}
	if err := fields.Validate(k.Check); err != nil {
		return nil, nil, fmt.Errorf("%w: %v", ErrBadOverride, err)
	}
	if err := s.consistent(ctx, k, fields); err != nil {
		return nil, nil, fmt.Errorf("%w: %v", ErrBadOverride, err)
	}
	if b, ok := findTx(ctx, tx, k); ok {
		before = &b
	}
	var byv any
	if by != "" {
		byv = by
	}
	if len(fields) == 0 {
		if before != nil {
			if _, err := tx.ExecContext(ctx, `DELETE FROM mon_health_overrides WHERE id = ?`, before.ID); err != nil {
				return nil, nil, err
			}
		}
		return before, nil, nil
	}
	fj, _ := json.Marshal(fields)
	label = trunc(strings.TrimSpace(label), 200)
	a := Override{Scope: k.Scope, GroupID: k.GroupID, HostID: k.HostID, Check: k.Check, Subject: k.Subject, SubjectLabel: label, Fields: fields.clone(), UpdatedAt: now}
	nullable := func(v string) any {
		if v == "" {
			return nil
		}
		return v
	}
	if before != nil {
		a.ID, a.CreatedAt = before.ID, before.CreatedAt
		if label == "" {
			a.SubjectLabel = before.SubjectLabel
		}
		_, err = tx.ExecContext(ctx, `UPDATE mon_health_overrides SET fields_json = ?, subject_label = ?, updated_by = ?, updated_at = ? WHERE id = ?`,
			string(fj), a.SubjectLabel, byv, now, a.ID)
	} else {
		a.ID, a.CreatedAt = ids.New(ids.HealthOverride), now
		_, err = tx.ExecContext(ctx, `INSERT INTO mon_health_overrides (id, scope, group_id, host_id, check_id, subject, subject_label, fields_json, created_by, created_at, updated_by, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, a.ID, k.Scope, nullable(k.GroupID), nullable(k.HostID), k.Check, k.Subject, a.SubjectLabel, string(fj), byv, now, byv, now)
	}
	if err != nil {
		return nil, nil, err
	}
	return before, &a, nil
}

// findTx reads a key's row inside a transaction.
func findTx(ctx context.Context, tx *sql.Tx, k OverrideKey) (Override, bool) {
	var o Override
	var fj string
	err := tx.QueryRowContext(ctx, `SELECT id, subject_label, fields_json, created_at, updated_at FROM mon_health_overrides
		WHERE scope = ? AND ifnull(group_id, '') = ? AND ifnull(host_id, '') = ? AND check_id = ? AND subject = ?`,
		k.Scope, k.GroupID, k.HostID, k.Check, k.Subject).Scan(&o.ID, &o.SubjectLabel, &fj, &o.CreatedAt, &o.UpdatedAt)
	if err != nil {
		return o, false
	}
	o.Scope, o.GroupID, o.HostID, o.Check, o.Subject = k.Scope, k.GroupID, k.HostID, k.Check, k.Subject
	if json.Unmarshal([]byte(fj), &o.Fields) != nil || o.Fields == nil {
		o.Fields = Fields{}
	}
	return o, true
}

// DeleteOverrideTx removes an override (reset to inherited).
func (s *Service) DeleteOverrideTx(ctx context.Context, tx *sql.Tx, id string) (Override, error) {
	var k OverrideKey
	err := tx.QueryRowContext(ctx, `SELECT scope, ifnull(group_id, ''), ifnull(host_id, ''), check_id, subject FROM mon_health_overrides WHERE id = ?`, id).
		Scan(&k.Scope, &k.GroupID, &k.HostID, &k.Check, &k.Subject)
	if errors.Is(err, sql.ErrNoRows) {
		return Override{}, ErrOverrideNotFound
	}
	if err != nil {
		return Override{}, err
	}
	o, _ := findTx(ctx, tx, k)
	res, err := tx.ExecContext(ctx, `DELETE FROM mon_health_overrides WHERE id = ?`, id)
	if err != nil {
		return o, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return o, ErrOverrideNotFound
	}
	return o, nil
}

// consistent checks that the levels the scope ends up with make sense
// together (a warning above the inherited critical level, say).
func (s *Service) consistent(ctx context.Context, k OverrideKey, fields Fields) error {
	p := s.eval.peek(ctx)
	np := p.with(k, fields)
	host := k.HostID
	sc := subjectCtx{}
	if k.Subject != "" {
		sc.keys = []string{k.Subject}
	}
	upto := map[string]int{ScopeGlobal: levelGroup, ScopeGroup: levelHost, ScopeHost: levelAll}[k.Scope]
	if k.Scope == ScopeGroup {
		// judge the group's own view: global and the group itself
		host = "\x00group:" + k.GroupID
		np.members[host] = []string{k.GroupID}
	}
	e := np.effective(host, k.Check, sc, nil, upto)
	w, c := e[FieldWarning].Value, e[FieldCritical].Value
	if w != nil && c != nil && *w > *c && (fields.has(FieldWarning) || fields.has(FieldCritical)) {
		return fmt.Errorf("the warning level (%g) would be above the critical level (%g, %s)", *w, *c, strings.ToLower(e[FieldCritical].Source.Label))
	}
	return nil
}

// clone copies the policy (members and groups maps are fresh).
func (p *policy) clone() *policy { return p.with(OverrideKey{}, nil) }

// with returns a copy of the policy with one key's fields replaced.
func (p *policy) with(k OverrideKey, fields Fields) *policy {
	np := emptyPolicy()
	np.gen = p.gen
	for id, g := range p.groups {
		np.groups[id] = g
	}
	for h, gs := range p.members {
		np.members[h] = gs
	}
	if k.Check == "" {
		for _, o := range p.rows {
			np.add(o)
		}
		return np
	}
	np.watched = p.watched
	found := false
	for _, o := range p.rows {
		if o.Scope == k.Scope && o.GroupID == k.GroupID && o.HostID == k.HostID && o.Check == k.Check && o.Subject == k.Subject {
			found = true
			if len(fields) > 0 {
				o.Fields = fields
				np.add(o)
			}
			continue
		}
		np.add(o)
	}
	if !found && len(fields) > 0 {
		np.add(Override{Scope: k.Scope, GroupID: k.GroupID, HostID: k.HostID, Check: k.Check, Subject: k.Subject, Fields: fields})
	}
	return np
}

// withPriority returns a copy with one group's priority changed.
func (p *policy) withPriority(groupID string, prio int) *policy {
	np := p.clone()
	if g := p.groups[groupID]; g != nil {
		c := *g
		c.Priority = prio
		np.groups[groupID] = &c
	}
	return np
}

// --- group priority ---

// GroupHealth is a group's monitoring override state.
type GroupHealth struct {
	GroupID   string `json:"group_id"`
	Name      string `json:"name"`
	Priority  int    `json:"priority"`
	Hosts     int    `json:"hosts"`
	Overrides int    `json:"overrides"` // overridden fields
}

// GroupsHealth lists every group with its priority and override count.
func (s *Service) GroupsHealth(ctx context.Context) []GroupHealth {
	p := s.eval.current(ctx)
	n := map[string]int{}
	for _, gs := range p.members {
		for _, g := range gs {
			n[g]++
		}
	}
	out := []GroupHealth{}
	for _, g := range p.groups {
		c := 0
		for _, f := range p.groupOv[g.ID] {
			c += len(f)
		}
		out = append(out, GroupHealth{GroupID: g.ID, Name: g.Name, Priority: g.Priority, Hosts: n[g.ID], Overrides: c})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// SetGroupPriorityTx sets a group's monitoring override priority and
// returns the one before.
func (s *Service) SetGroupPriorityTx(ctx context.Context, tx *sql.Tx, groupID string, prio int, by string, now int64) (int, error) {
	if prio < -1000 || prio > 1000 {
		return 0, fmt.Errorf("%w: priority must be -1000–1000", ErrBadOverride)
	}
	p := s.eval.peek(ctx)
	g := p.groups[groupID]
	if g == nil {
		return 0, ErrOverrideNotFound
	}
	var byv any
	if by != "" {
		byv = by
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO mon_group_health (group_id, priority, updated_by, updated_at) VALUES (?, ?, ?, ?)
		ON CONFLICT(group_id) DO UPDATE SET priority = excluded.priority, updated_by = excluded.updated_by, updated_at = excluded.updated_at`,
		groupID, prio, byv, now)
	return g.Priority, err
}

// --- effective configuration and provenance ---

// EffectiveField is one field as a scope sees it.
type EffectiveField struct {
	FieldSpec
	Resolved
	// Own is this scope's override of the field (nil: inherited).
	Own *float64 `json:"own"`
	// Inherited is the value without this scope's override ("Reset to
	// inherited" restores it).
	Inherited Resolved `json:"inherited"`
}

// EffectiveCheck is one check's effective configuration.
type EffectiveCheck struct {
	Check       string           `json:"check"`
	Label       string           `json:"label"`
	Category    string           `json:"category"`
	Description string           `json:"description"`
	SubjectKind string           `json:"subject_kind,omitempty"`
	Info        bool             `json:"info,omitempty"`
	DefaultOff  bool             `json:"default_off,omitempty"`
	Enabled     bool             `json:"enabled"`
	Customized  bool             `json:"customized"` // this scope overrides something
	Conflicts   int              `json:"conflicts"`
	OverrideID  string           `json:"override_id,omitempty"`
	Fields      []EffectiveField `json:"fields"`
}

// HealthView is a scope's effective configuration (global, a group, a
// host, or one subject of a host).
type HealthView struct {
	Scope        string           `json:"scope"`
	GroupID      string           `json:"group_id,omitempty"`
	HostID       string           `json:"host_id,omitempty"`
	Subject      *Subject         `json:"subject,omitempty"`
	Checks       []EffectiveCheck `json:"checks"`
	Summary      *HealthSummary   `json:"summary,omitempty"`
	Subjects     []Subject        `json:"subjects,omitempty"`
	GroupDetails []GroupHealth    `json:"groups,omitempty"` // a host's groups
}

// HealthSummary says in a line where a host's settings come from.
type HealthSummary struct {
	Text            string `json:"text"`
	GlobalFields    int    `json:"global_fields"`
	GroupFields     int    `json:"group_fields"`
	HostFields      int    `json:"host_fields"`
	SubjectFields   int    `json:"subject_fields"`
	Conflicts       int    `json:"conflicts"`
	UsingRecommends bool   `json:"using_recommended"`
}

func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}

func (c policyCounts) summary() *HealthSummary {
	h := &HealthSummary{GlobalFields: c.Global, GroupFields: c.Group, HostFields: c.Host, SubjectFields: c.Subject, Conflicts: c.Conflicts}
	var parts []string
	if c.Group > 0 {
		parts = append(parts, plural(c.Group, "inherited pack override", "inherited pack overrides"))
	}
	if c.Host > 0 {
		parts = append(parts, plural(c.Host, "host override", "host overrides"))
	}
	if c.Subject > 0 {
		parts = append(parts, plural(c.Subject, "override for individual devices or services", "overrides for individual devices or services"))
	}
	if c.Conflicts > 0 {
		parts = append(parts, plural(c.Conflicts, "pack conflict", "pack conflicts"))
	}
	switch {
	case len(parts) == 0 && c.Global == 0:
		h.Text, h.UsingRecommends = "Using recommended defaults", true
	case len(parts) == 0:
		h.Text = "Using global custom settings"
	default:
		h.Text = strings.Join(parts, ", ")
		if c.Global > 0 {
			h.Text = "Global custom settings with " + h.Text
		}
	}
	return h
}

func (p *policy) view(host, check string, sc subjectCtx, hw *hwLimits, scopeLevel int, own Fields, ownID string) EffectiveCheck {
	spec, _ := Spec(check)
	eff := p.effective(host, check, sc, hw, levelAll)
	if scopeLevel < levelAll {
		eff = p.effective(host, check, sc, hw, scopeLevel+1)
	}
	inh := p.effective(host, check, sc, hw, scopeLevel)
	ec := EffectiveCheck{Check: check, Label: spec.Label, Category: spec.Category, Description: spec.Description, SubjectKind: spec.Subject,
		Info: spec.Info, DefaultOff: spec.DefaultOff, OverrideID: ownID, Fields: []EffectiveField{}}
	if check == CheckTemperature && len(sc.keys) > 0 && strings.HasPrefix(sc.keys[0], "gpu:") {
		ec.SubjectKind = SubjectGPU
	}
	for _, f := range spec.Fields {
		ef := EffectiveField{FieldSpec: f, Resolved: eff[f.Name], Inherited: inh[f.Name]}
		if v, ok := own[f.Name]; ok {
			ef.Own = fp(v)
			ec.Customized = true
		}
		if ef.Conflict != nil {
			ec.Conflicts++
		}
		ec.Fields = append(ec.Fields, ef)
	}
	if v := eff[FieldEnabled].Value; v != nil && *v != 0 {
		ec.Enabled = true
	}
	return ec
}

// GlobalView is the fleet-wide configuration: Recommended plus global
// overrides.
func (s *Service) GlobalView(ctx context.Context) HealthView {
	p := s.eval.current(ctx)
	v := HealthView{Scope: ScopeGlobal, Checks: []EffectiveCheck{}}
	for _, spec := range checkSpecs {
		var id string
		if o, ok := (OverrideKey{Scope: ScopeGlobal, Check: spec.ID}).find(p); ok {
			id = o.ID
		}
		v.Checks = append(v.Checks, p.view("", spec.ID, subjectCtx{}, nil, levelGlobal, p.global[spec.ID], id))
	}
	n := 0
	for _, f := range p.global {
		n += len(f)
	}
	v.Summary = policyCounts{Global: n}.summary()
	return v
}

// GroupView is one group's configuration: what it inherits and what it
// overrides.
func (s *Service) GroupView(ctx context.Context, groupID string) (HealthView, error) {
	p := s.eval.current(ctx)
	if p.groups[groupID] == nil {
		return HealthView{}, ErrOverrideNotFound
	}
	// A pseudo-host in only this group shows the group's own view.
	np := p.clone()
	pseudo := "\x00group:" + groupID
	np.members[pseudo] = []string{groupID}
	v := HealthView{Scope: ScopeGroup, GroupID: groupID, Checks: []EffectiveCheck{}}
	for _, spec := range checkSpecs {
		var id string
		if o, ok := (OverrideKey{Scope: ScopeGroup, GroupID: groupID, Check: spec.ID}).find(p); ok {
			id = o.ID
		}
		ec := np.view(pseudo, spec.ID, subjectCtx{}, nil, levelGroup, p.groupOv[groupID][spec.ID], id)
		for i := range ec.Fields {
			if ec.Fields[i].Own != nil {
				ec.Fields[i].Source = Provenance{Kind: "group", Label: "Custom for this group", Groups: ec.Fields[i].Source.Groups}
			}
		}
		v.Checks = append(v.Checks, ec)
	}
	for _, g := range s.GroupsHealth(ctx) {
		if g.GroupID == groupID {
			v.GroupDetails = []GroupHealth{g}
		}
	}
	return v, nil
}

// HostView is a host's effective configuration with provenance; with a
// subject key, that subject's (its checks only). check narrows it to one
// check.
func (s *Service) HostView(ctx context.Context, hostID, check, subject string) (HealthView, error) {
	p := s.eval.current(ctx)
	subjects := s.hostSubjectList(ctx, p, hostID)
	v := HealthView{Scope: ScopeHost, HostID: hostID, Checks: []EffectiveCheck{}}
	pc := p.counts(hostID)
	v.Summary = pc.summary()
	all := s.GroupsHealth(ctx)
	for _, g := range p.members[hostID] {
		for _, gh := range all {
			if gh.GroupID == g {
				v.GroupDetails = append(v.GroupDetails, gh)
			}
		}
	}
	if subject == "" {
		v.Subjects = subjects
		for _, spec := range checkSpecs {
			if check != "" && spec.ID != check {
				continue
			}
			var id string
			if o, ok := (OverrideKey{Scope: ScopeHost, HostID: hostID, Check: spec.ID}).find(p); ok {
				id = o.ID
			}
			v.Checks = append(v.Checks, p.view(hostID, spec.ID, subjectCtx{}, nil, levelHost, p.host[hostID][spec.ID], id))
		}
		return v, nil
	}
	var sub *Subject
	for i := range subjects {
		if subjects[i].Key == subject {
			sub = &subjects[i]
		}
	}
	if sub == nil {
		return v, ErrOverrideNotFound
	}
	v.Subject = sub
	sc, hw := s.subjectCtxFor(hostID, *sub)
	for _, c := range sub.Checks {
		if check != "" && c != check {
			continue
		}
		var id string
		if o, ok := (OverrideKey{Scope: ScopeHost, HostID: hostID, Check: c, Subject: subject}).find(p); ok {
			id = o.ID
		}
		h := hw
		if c != CheckTemperature {
			h = nil
		}
		v.Checks = append(v.Checks, p.view(hostID, c, sc, h, levelSubject, p.subj[hostID][c][subject], id))
	}
	return v, nil
}

// subjectCtxFor builds the resolution context of a listed subject. A
// filesystem keeps its mount path as the second, weaker identity.
func (s *Service) subjectCtxFor(hostID string, sub Subject) (subjectCtx, *hwLimits) {
	sc := subjectCtx{keys: []string{sub.Key}, label: sub.Label}
	if sub.Kind == SubjectFilesystem && strings.HasPrefix(sub.Key, "fsuuid:") && sub.Issue != "" {
		sc.keys = append(sc.keys, "mount:"+sub.Issue)
	}
	switch {
	case sub.Kind == SubjectSensor && sub.Present:
		st, _ := s.eval.snapshot(hostID)
		if st != nil {
			for _, sn := range st.Sensors {
				if sn.ID == sub.Issue {
					return sc, &hwLimits{high: sn.High, crit: sn.Critical}
				}
			}
		}
	case sub.Kind == SubjectGPU:
		return sc, &hwLimits{} // a GPU reports no temperature limit
	}
	return sc, nil
}

// hostSubjectList is a host's subjects: what it reports now, plus the
// configured subjects it doesn't report any more ("currently
// unavailable": their overrides stay and apply again if they return).
func (s *Service) hostSubjectList(ctx context.Context, p *policy, hostID string) []Subject {
	st, rep := s.eval.snapshot(hostID)
	units, _ := s.Services(ctx, hostID)
	active := map[string]bool{}
	for _, k := range s.eval.activeKeys(hostID, CheckServiceFailed) {
		active[k] = true
	}
	for _, k := range s.eval.activeKeys(hostID, CheckServiceStopped) {
		active[k] = true
	}
	hs := s.HostSettings(ctx, hostID)
	subs := hostSubjects(st, rep, units, p.watched[hostID], hs.ProbeMounts, active)
	idx := map[string]int{}
	for i, x := range subs {
		idx[x.Key] = i
		if x.Kind == SubjectFilesystem && strings.HasPrefix(x.Key, "fsuuid:") {
			idx["mount:"+x.Issue] = i // a mount-path override of the same filesystem
		}
	}
	for _, o := range p.rows {
		if o.Scope != ScopeHost || o.HostID != hostID || o.Subject == "" {
			continue
		}
		if i, ok := idx[o.Subject]; ok {
			subs[i].Customized = true
			continue
		}
		kind := subjectKind(o.Check, o.Subject)
		label := o.SubjectLabel
		if label == "" {
			label = o.Subject
		}
		ns := Subject{Key: o.Subject, Kind: kind, Label: label, Identity: "Not reported by the host now", Strength: "", Present: false,
			Checks: subjectChecks[kind], Customized: true}
		if kind == SubjectGPU {
			ns.Checks = []string{CheckGPUUtil, CheckTemperature}
		}
		idx[o.Subject] = len(subs)
		subs = append(subs, ns)
	}
	if subs == nil {
		subs = []Subject{}
	}
	return subs
}

// --- weakening (permissions) ---

// Weakens reports whether changing a host-scope key to fields would make
// detection less sensitive anywhere it applies: a check turned off, a
// higher threshold, a longer duration, a sooner clear. Raising the bar is
// an administrator's decision (monitoring.settings); tightening it is
// day-to-day operation (monitoring.manage).
func (s *Service) Weakens(ctx context.Context, k OverrideKey, fields Fields) bool {
	p := s.eval.current(ctx)
	np := p.with(k, fields)
	var ctxs []subjectCtx
	if k.Subject != "" {
		ctxs = []subjectCtx{{keys: []string{k.Subject}}}
	} else {
		ctxs = []subjectCtx{{}}
		for _, sub := range s.hostSubjectList(ctx, p, k.HostID) {
			for _, c := range sub.Checks {
				if c == k.Check {
					sc, _ := s.subjectCtxFor(k.HostID, sub)
					ctxs = append(ctxs, sc)
				}
			}
		}
	}
	hw := &hwLimits{high: fp(80), crit: fp(100)} // any limits: what matters is the direction
	for _, sc := range ctxs {
		if weaker(p.effective(k.HostID, k.Check, sc, hw, levelAll), np.effective(k.HostID, k.Check, sc, hw, levelAll)) {
			return true
		}
	}
	return false
}

func weaker(a, b map[string]Resolved) bool {
	val := func(m map[string]Resolved, f string) (float64, bool) {
		if r, ok := m[f]; ok && r.Value != nil {
			return *r.Value, true
		}
		return 0, false
	}
	if x, ok := val(a, FieldEnabled); ok {
		if y, _ := val(b, FieldEnabled); x != 0 && y == 0 {
			return true
		}
	}
	for _, f := range []string{FieldWarning, FieldCritical, FieldDuration, FieldIntervals, FieldClearDuration} {
		x, xok := val(a, f)
		y, yok := val(b, f)
		if f == FieldClearDuration {
			if xok && yok && y < x { // clears sooner
				return true
			}
			continue
		}
		if xok && (!yok || y > x) {
			return true
		}
	}
	if x, xok := val(a, FieldClear); xok {
		if y, yok := val(b, FieldClear); yok && y > x { // clears sooner
			return true
		}
	}
	return false
}

// --- impact preview ---

// PreviewHost is one affected host.
type PreviewHost struct {
	HostID  string   `json:"host_id"`
	Name    string   `json:"name"`
	Effect  string   `json:"effect"` // what changes for it, in a sentence
	Closes  []string `json:"closes,omitempty"`
	Changes []string `json:"changes,omitempty"`
}

// Preview is what a change would do across the fleet.
type Preview struct {
	Check string `json:"check"`
	// Hosts whose effective rule for the check changes.
	Affected int `json:"affected"`
	// Hosts the scope covers (all hosts, a group's members, one host).
	InScope int `json:"in_scope"`
	// Hosts whose current value is at or above the previous and the new
	// warning level (numeric checks with live values).
	AboveBefore int `json:"above_before"`
	AboveAfter  int `json:"above_after"`
	// Active issues that would close (and why) or stay.
	WouldClose int           `json:"would_close"`
	WouldStay  int           `json:"would_stay"`
	Disables   bool          `json:"disables"`
	Hosts      []PreviewHost `json:"hosts"` // the first 50 affected
	Summary    string        `json:"summary"`
}

// PreviewChange computes the effect of replacing a key's fields (nil or
// empty: removing the override) or, with priority set, a group's
// priority. It changes nothing.
func (s *Service) PreviewChange(ctx context.Context, k OverrideKey, fields Fields, priority *int) (*Preview, error) {
	if priority == nil {
		if err := k.Validate(); err != nil {
			return nil, err
		}
		if err := fields.Validate(k.Check); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrBadOverride, err)
		}
	}
	p := s.eval.current(ctx)
	var np *policy
	var checks []string
	if priority != nil {
		if p.groups[k.GroupID] == nil {
			return nil, ErrOverrideNotFound
		}
		np = p.withPriority(k.GroupID, *priority)
		for c := range p.groupOv[k.GroupID] {
			checks = append(checks, c)
		}
		sort.Strings(checks)
	} else {
		np = p.with(k, fields)
		checks = []string{k.Check}
	}
	hosts, err := s.d.Store.R.ListHosts(ctx)
	if err != nil {
		return nil, err
	}
	pv := &Preview{Check: k.Check, Hosts: []PreviewHost{}}
	for _, h := range hosts {
		if h.JumpOnly != 0 {
			continue
		}
		switch k.Scope {
		case ScopeHost:
			if h.ID != k.HostID {
				continue
			}
		case ScopeGroup:
			if !contains(p.members[h.ID], k.GroupID) {
				continue
			}
		}
		pv.InScope++
		ph := PreviewHost{HostID: h.ID, Name: h.Name}
		affected, above0, above1 := false, false, false
		for _, chk := range checks {
			if p.sig(h.ID, chk) == np.sig(h.ID, chk) {
				continue
			}
			for _, sx := range s.previewSubjects(ctx, p, h.ID, chk, k.Subject) {
				r0 := p.rule(h.ID, chk, sx.sc, sx.hw)
				r1 := np.rule(h.ID, chk, sx.sc, sx.hw)
				if ruleEqual(r0, r1) {
					continue
				}
				affected = true
				if r0.on && !r1.on {
					pv.Disables = true
				}
				if sx.hasV {
					if r0.on && r0.warn != nil && sx.v >= *r0.warn {
						above0 = true
					}
					if r1.on && r1.warn != nil && sx.v >= *r1.warn {
						above1 = true
					}
				}
				if sx.issue != "" && s.eval.isActive(h.ID, chk, sx.issue) {
					stays := r1.on && (!sx.hasV || r1.warn != nil && sx.v >= *r1.warn || r1.warn == nil && r1.crit == nil && chk != CheckTemperature)
					name := checkLabel(chk)
					if sx.label != "" {
						name += " (" + sx.label + ")"
					}
					if stays {
						pv.WouldStay++
						ph.Changes = append(ph.Changes, name+" stays open under the new rule")
					} else {
						pv.WouldClose++
						why := "the check would be off"
						if r1.on {
							why = "the current value no longer qualifies"
						}
						ph.Closes = append(ph.Closes, name+": "+why)
					}
				}
			}
		}
		if !affected {
			continue
		}
		pv.Affected++
		if above0 {
			pv.AboveBefore++
		}
		if above1 {
			pv.AboveAfter++
		}
		switch {
		case len(ph.Closes) > 0:
			ph.Effect = fmt.Sprintf("%s would close", plural(len(ph.Closes), "active issue", "active issues"))
		case above1 && !above0:
			ph.Effect = "currently qualifies under the new rule (it would open after the full duration, counted from the change)"
		default:
			ph.Effect = "rule changes; nothing currently open or qualifying"
		}
		if len(pv.Hosts) < 50 {
			pv.Hosts = append(pv.Hosts, ph)
		}
	}
	parts := []string{fmt.Sprintf("%s affected", plural(pv.Affected, "host", "hosts"))}
	if pv.AboveBefore > 0 || pv.AboveAfter > 0 {
		parts = append(parts, fmt.Sprintf("%d currently above the previous threshold, %d above the new one", pv.AboveBefore, pv.AboveAfter))
	}
	if pv.WouldClose > 0 {
		parts = append(parts, fmt.Sprintf("%s would close as a configuration change (not a recovery)", plural(pv.WouldClose, "active issue", "active issues")))
	}
	pv.Summary = strings.Join(parts, "; ")
	return pv, nil
}

func ruleEqual(a, b rule) bool {
	eq := func(x, y *float64) bool { return x == nil && y == nil || x != nil && y != nil && *x == *y }
	return a.on == b.on && eq(a.warn, b.warn) && eq(a.crit, b.crit) && eq(a.clr, b.clr) && a.dur == b.dur && a.clearDur == b.clearDur && a.intervals == b.intervals
}

type previewSubject struct {
	sc    subjectCtx
	hw    *hwLimits
	issue string
	label string
	v     float64
	hasV  bool
}

// previewSubjects are what a check judges on a host, with live values.
func (s *Service) previewSubjects(ctx context.Context, p *policy, hostID, check, onlyKey string) []previewSubject {
	st, rep := s.eval.snapshot(hostID)
	val := func(issue string) (float64, bool) {
		s.eval.mu.Lock()
		defer s.eval.mu.Unlock()
		if he := s.eval.hosts[hostID]; he != nil {
			if c := he.conds[ikey(check, issue)]; c != nil && c.hasV {
				return c.v, true
			}
		}
		return 0, false
	}
	var out []previewSubject
	switch check {
	case CheckCPU, CheckMemory:
		v, ok := val("")
		if !ok && st != nil {
			if check == CheckCPU && st.CPU != nil && st.CPU.Percent != nil {
				v, ok = *st.CPU.Percent, true
			}
			if check == CheckMemory && st.Memory != nil {
				v, ok = st.Memory.UsedPercent, true
			}
		}
		out = append(out, previewSubject{issue: "", v: v, hasV: ok})
	case CheckFilesystem, CheckInodes, CheckFSReadOnly:
		if st == nil {
			break
		}
		uuids := mountUUIDs(rep)
		for _, f := range st.Filesystems {
			ps := previewSubject{sc: fsCtx(f.Mount, uuids), issue: f.Mount, label: f.Mount}
			switch {
			case check == CheckFilesystem && f.TotalBytes > 0:
				ps.v, ps.hasV = f.UsedPercent, true
			case check == CheckInodes && f.InodesPercent != nil:
				ps.v, ps.hasV = *f.InodesPercent, true
			}
			out = append(out, ps)
		}
	case CheckTemperature:
		if st == nil {
			break
		}
		disks := diskKeysByName(rep)
		for _, sn := range st.Sensors {
			sub := sensorSubject(sn, disks)
			out = append(out, previewSubject{sc: subjectCtx{keys: []string{sub.Key}, label: sub.Label}, hw: &hwLimits{high: sn.High, crit: sn.Critical},
				issue: sn.ID, label: sub.Label, v: sn.Celsius, hasV: true})
		}
		for _, g := range st.GPUs {
			if g.Source == "nvidia-smi" && g.Celsius != nil {
				out = append(out, previewSubject{sc: subjectCtx{keys: []string{gpuTempIssue(g)}, label: g.Model}, hw: &hwLimits{}, issue: gpuTempIssue(g), label: g.Model, v: *g.Celsius, hasV: true})
			}
		}
	case CheckGPUUtil:
		if st == nil {
			break
		}
		for _, g := range st.GPUs {
			ps := previewSubject{sc: subjectCtx{keys: []string{"gpu:" + gpuID(g)}, label: g.Model}, issue: gpuID(g), label: g.Model}
			if v, ok := val(gpuID(g)); ok {
				ps.v, ps.hasV = v, true
			} else if g.UtilPercent != nil {
				ps.v, ps.hasV = *g.UtilPercent, true
			}
			out = append(out, ps)
		}
	default:
		// Event and state checks: the host level, plus each active issue.
		out = append(out, previewSubject{})
		for _, k := range s.eval.activeKeys(hostID, check) {
			sc, hw := s.eval.subjectOf(hostID, check, k)
			out = append(out, previewSubject{sc: sc, hw: hw, issue: k, label: k})
		}
	}
	if onlyKey != "" {
		var f []previewSubject
		for _, x := range out {
			for _, k := range x.sc.keys {
				if k == onlyKey {
					f = append(f, x)
				}
			}
		}
		out = f
	}
	return out
}

// --- legacy settings (M11) ---

// migrateLegacy moves M11's settings into overrides once: a Custom
// profile becomes global overrides of the fields that differ from
// Recommended, and each host's "disabled checks" become host overrides.
func (s *Service) migrateLegacy(ctx context.Context) {
	set := s.Settings(ctx)
	if set.Profile == ProfileCustom && set.Custom != nil {
		r, c := Recommended(), *set.Custom
		type m struct {
			check, field string
			v, def       float64
		}
		ms := []m{{CheckCPU, FieldWarning, c.CPUPercent, r.CPUPercent}, {CheckCPU, FieldDuration, float64(c.CPUMinutes), float64(r.CPUMinutes)},
			{CheckMemory, FieldWarning, c.MemoryPercent, r.MemoryPercent}, {CheckMemory, FieldCritical, c.MemoryCritical, r.MemoryCritical},
			{CheckMemory, FieldDuration, float64(c.MemoryMinutes), float64(r.MemoryMinutes)},
			{CheckFilesystem, FieldWarning, c.DiskWarning, r.DiskWarning}, {CheckFilesystem, FieldCritical, c.DiskCritical, r.DiskCritical},
			{CheckInodes, FieldWarning, c.InodeWarning, r.InodeWarning}, {CheckInodes, FieldCritical, c.InodeCritical, r.InodeCritical},
			{CheckServiceFailed, FieldDuration, float64(c.ServiceMinutes), float64(r.ServiceMinutes)},
			{CheckServiceStopped, FieldDuration, float64(c.WatchedMinutes), float64(r.WatchedMinutes)},
			{CheckInterfaceDown, FieldDuration, float64(c.InterfaceMinutes), float64(r.InterfaceMinutes)},
			{CheckTemperature, FieldDuration, float64(c.TempMinutes), float64(r.TempMinutes)},
			{CheckStale, FieldIntervals, float64(c.StaleIntervals), float64(r.StaleIntervals)}}
		byCheck := map[string]Fields{}
		for _, x := range ms {
			if x.v != x.def {
				if byCheck[x.check] == nil {
					byCheck[x.check] = Fields{}
				}
				byCheck[x.check][x.field] = x.v
			}
		}
		err := s.d.Store.InRawTx(ctx, func(tx *sql.Tx) error {
			for _, chk := range sortedKeys(byCheck) {
				if _, _, err := s.PutOverrideTx(ctx, tx, OverrideKey{Scope: ScopeGlobal, Check: chk}, byCheck[chk], "", "", s.now().UnixMilli()); err != nil {
					return err
				}
			}
			set.Profile, set.Custom = "", nil
			b, _ := json.Marshal(set)
			_, err := tx.ExecContext(ctx, `UPDATE settings SET value_json = ? WHERE key = ?`, string(b), settingKey)
			return err
		})
		if err != nil {
			s.log.Warn("move the custom health profile into global overrides", "err", err)
		} else {
			s.log.Info("the custom health profile became global overrides (M12.1)", "checks", len(byCheck))
		}
	}
	rows, err := s.d.Store.Reader.QueryContext(ctx, `SELECT host_id, settings_json FROM mon_host_settings WHERE settings_json LIKE '%"disabled_checks":["%'`)
	if err != nil {
		return
	}
	type legacy struct {
		host string
		raw  map[string]json.RawMessage
		off  []string
	}
	var ls []legacy
	for rows.Next() {
		var h, j string
		if rows.Scan(&h, &j) != nil {
			continue
		}
		var raw map[string]json.RawMessage
		var off struct {
			DisabledChecks []string `json:"disabled_checks"`
		}
		if json.Unmarshal([]byte(j), &raw) != nil || json.Unmarshal([]byte(j), &off) != nil {
			continue
		}
		ls = append(ls, legacy{h, raw, off.DisabledChecks})
	}
	rows.Close()
	for _, l := range ls {
		err := s.d.Store.InRawTx(ctx, func(tx *sql.Tx) error {
			for _, c := range l.off {
				if _, ok := Spec(c); !ok {
					continue
				}
				if _, _, err := s.PutOverrideTx(ctx, tx, OverrideKey{Scope: ScopeHost, HostID: l.host, Check: c}, Fields{FieldEnabled: 0}, "", "", s.now().UnixMilli()); err != nil {
					return err
				}
			}
			delete(l.raw, "disabled_checks")
			b, _ := json.Marshal(l.raw)
			_, err := tx.ExecContext(ctx, `UPDATE mon_host_settings SET settings_json = ? WHERE host_id = ?`, string(b), l.host)
			return err
		})
		if err != nil {
			s.log.Warn("move a host's disabled checks into overrides", "host", l.host, "err", err)
		}
	}
}
