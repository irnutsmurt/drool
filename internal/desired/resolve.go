package desired

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
)

// Scope levels, most specific first. The most specific level that
// declares a resource decides it; less specific levels are shadowed.
const (
	LevelHost     = "host"     // a host-specific declaration ("Custom for this host")
	LevelAssigned = "assigned" // a baseline assigned directly to the host
	LevelGroup    = "group"    // a baseline assigned to one of the host's groups
	LevelGlobal   = "global"   // a baseline that applies everywhere
)

// BaselineRef names a baseline revision.
type BaselineRef struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Revision int    `json:"revision"`
}

// GroupRef names a group with its desired-state priority.
type GroupRef struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Priority int    `json:"priority"`
}

// Declaration is one declaration competing for, or shadowed by, the
// effective one.
type Declaration struct {
	Label    string       `json:"label"`
	Want     string       `json:"want"`
	Level    string       `json:"level"`
	Baseline *BaselineRef `json:"baseline,omitempty"`
	Group    *GroupRef    `json:"group,omitempty"`
}

// Source explains where an effective value comes from. The controller
// computes it; the browser only shows it.
type Source struct {
	Level string `json:"level"`
	// Label: "Required by Global baseline Server Defaults", "Inherited
	// from group: Media Servers", "Custom for this host", "Conflict
	// between groups Web Servers and Maintenance Lab".
	Label     string        `json:"label"`
	Baselines []BaselineRef `json:"baselines,omitempty"`
	Groups    []GroupRef    `json:"groups,omitempty"`
	// Override: the host-specific declaration's metadata.
	Override *OverrideInfo `json:"override,omitempty"`
	// Competing: a conflict's declarations (every one of them).
	Competing []Declaration `json:"competing,omitempty"`
	// Shadowed: less specific declarations this one replaces.
	Shadowed []Declaration `json:"shadowed,omitempty"`
	// Auto: a baseline behind this value restores it automatically;
	// AutoBaseline is the one whose owner and rollout apply.
	Auto         bool         `json:"auto"`
	AutoBaseline *BaselineRef `json:"auto_baseline,omitempty"`
	// Elevated: a competing file declaration deploys as root, so the file
	// is read as root (conflicts only).
	Elevated bool `json:"-"`
}

// OverrideInfo describes a host-specific declaration.
type OverrideInfo struct {
	Origin    string `json:"origin"` // accept | manual
	Note      string `json:"note,omitempty"`
	Version   int    `json:"version"`
	UpdatedBy string `json:"updated_by,omitempty"`
	UpdatedAt int64  `json:"updated_at"`
	Excluded  bool   `json:"excluded,omitempty"`
}

// Effective is one resource's effective desired state on one host.
type Effective struct {
	Key  string `json:"key"`
	Kind string `json:"kind"`
	// Resource is the declaration that wins; nil for a conflict.
	Resource *Resource `json:"resource,omitempty"`
	Conflict bool      `json:"conflict"`
	Source   Source    `json:"source"`
}

// SourceBaseline is the baseline revision a value came from (the first
// one when several agree), or nil (host-specific, conflict).
func (e Effective) SourceBaseline() *BaselineRef {
	if e.Conflict || len(e.Source.Baselines) == 0 {
		return nil
	}
	b := e.Source.Baselines[0]
	return &b
}

// Baseline is a baseline's current revision, as the resolver needs it.
type Baseline struct {
	ID        string
	Name      string
	Revision  int
	Resources map[string]Resource // by key
	Auto      bool                // automatic restore on (and not paused)
}

// Override is a host-specific declaration.
type Override struct {
	Resource Resource
	Info     OverrideInfo
}

// Group is a group as the resolver needs it.
type Group struct {
	Name     string
	Priority int
}

// Snapshot is the fleet's desired-state configuration at one generation.
// It is immutable once built.
type Snapshot struct {
	Gen       int64
	Baselines map[string]*Baseline           // non-archived baselines
	Global    []string                       // baseline ids assigned everywhere
	ByGroup   map[string][]string            // group id → baseline ids
	ByHost    map[string][]string            // host id → baseline ids assigned directly
	Overrides map[string]map[string]Override // host id → key → declaration
	Groups    map[string]Group               // group id → name and priority
	Members   map[string][]string            // host id → group ids
	Hosts     map[string]string              // host id → name
}

// HostState is a host's effective desired state.
type HostState struct {
	Resources map[string]Effective // managed (incl. conflicts)
	Excluded  map[string]Source    // keys a host-specific setting leaves unmanaged here
	Hash      string
}

type decl struct {
	level    string
	baseline *Baseline
	group    *GroupRef
	res      Resource
	excluded bool
	override *OverrideInfo
}

func (d decl) ref() *BaselineRef {
	if d.baseline == nil {
		return nil
	}
	return &BaselineRef{ID: d.baseline.ID, Name: d.baseline.Name, Revision: d.baseline.Revision}
}

func (d decl) label() string {
	switch d.level {
	case LevelHost:
		if d.excluded {
			return "Not managed on this host"
		}
		return "Custom for this host"
	case LevelAssigned:
		return "Baseline " + d.baseline.Name + ", assigned to this host"
	case LevelGroup:
		return "Inherited from group: " + d.group.Name + " (baseline " + d.baseline.Name + ")"
	}
	return "Required by Global baseline " + d.baseline.Name
}

func (d decl) declaration() Declaration {
	want := d.res.Want()
	if d.excluded {
		want = "not managed"
	}
	return Declaration{Label: d.label(), Want: want, Level: d.level, Baseline: d.ref(), Group: d.group}
}

var levelOrder = []string{LevelHost, LevelAssigned, LevelGroup, LevelGlobal}

// Resolve computes a host's effective desired state: for each resource,
// the most specific level that declares it decides. Within a level,
// declarations that ask for exactly the same thing agree; groups are
// ranked by priority first; anything else is a conflict, which has no
// desired value until someone resolves it (a priority, a host-specific
// declaration, or a baseline change). Names, ids and query order never
// decide.
func (s *Snapshot) Resolve(hostID string) HostState {
	byKey := map[string]map[string][]decl{} // key → level → declarations
	add := func(d decl) {
		k := d.res.Key()
		if byKey[k] == nil {
			byKey[k] = map[string][]decl{}
		}
		byKey[k][d.level] = append(byKey[k][d.level], d)
	}
	for k, o := range s.Overrides[hostID] {
		o := o
		d := decl{level: LevelHost, res: o.Resource, excluded: o.Info.Excluded, override: &o.Info}
		if o.Info.Excluded {
			d.res = Resource{Kind: KeyKind(k)}
			switch d.res.Kind {
			case KindFile:
				d.res.Path = KeyName(k)
			default:
				d.res.Name = KeyName(k)
			}
		}
		add(d)
	}
	for _, id := range s.ByHost[hostID] {
		if b := s.Baselines[id]; b != nil {
			for _, r := range b.Resources {
				add(decl{level: LevelAssigned, baseline: b, res: r})
			}
		}
	}
	for _, gid := range s.Members[hostID] {
		g, ok := s.Groups[gid]
		if !ok {
			continue
		}
		gr := &GroupRef{ID: gid, Name: g.Name, Priority: g.Priority}
		for _, id := range s.ByGroup[gid] {
			if b := s.Baselines[id]; b != nil {
				for _, r := range b.Resources {
					add(decl{level: LevelGroup, baseline: b, group: gr, res: r})
				}
			}
		}
	}
	for _, id := range s.Global {
		if b := s.Baselines[id]; b != nil {
			for _, r := range b.Resources {
				add(decl{level: LevelGlobal, baseline: b, res: r})
			}
		}
	}

	out := HostState{Resources: map[string]Effective{}, Excluded: map[string]Source{}}
	for key, levels := range byKey {
		var deciding []decl
		var shadowed []decl
		lvl := ""
		for _, l := range levelOrder {
			ds := levels[l]
			if len(ds) == 0 {
				continue
			}
			if lvl == "" {
				lvl, deciding = l, ds
			} else {
				shadowed = append(shadowed, ds...)
			}
		}
		if lvl == LevelGroup {
			top := deciding[0].group.Priority
			for _, d := range deciding {
				top = max(top, d.group.Priority)
			}
			var keep []decl
			for _, d := range deciding {
				if d.group.Priority == top {
					keep = append(keep, d)
				} else {
					shadowed = append(shadowed, d)
				}
			}
			deciding = keep
		}
		sortDecls(deciding)
		sortDecls(shadowed)
		src := Source{Level: lvl}
		for _, d := range shadowed {
			if len(src.Shadowed) < 10 {
				src.Shadowed = append(src.Shadowed, d.declaration())
			}
		}
		first := deciding[0]
		if first.level == LevelHost {
			src.Label = first.label()
			src.Override = first.override
			if first.excluded {
				out.Excluded[key] = src
				continue
			}
			r := first.res
			out.Resources[key] = Effective{Key: key, Kind: r.Kind, Resource: &r, Source: src}
			continue
		}
		agree := true
		for _, d := range deciding[1:] {
			agree = agree && d.res.Same(first.res)
		}
		for _, d := range deciding {
			if ref := d.ref(); ref != nil && !hasBaseline(src.Baselines, ref.ID) {
				src.Baselines = append(src.Baselines, *ref)
			}
			if d.group != nil && !hasGroup(src.Groups, d.group.ID) {
				src.Groups = append(src.Groups, *d.group)
			}
		}
		if !agree {
			for _, d := range deciding {
				src.Competing = append(src.Competing, d.declaration())
				src.Elevated = src.Elevated || d.res.Elevated
			}
			src.Label = conflictLabel(lvl, src)
			out.Resources[key] = Effective{Key: key, Kind: first.res.Kind, Conflict: true, Source: src}
			continue
		}
		src.Label = agreeLabel(lvl, deciding)
		for _, d := range deciding {
			if d.baseline.Auto {
				src.Auto = true
				if src.AutoBaseline == nil {
					src.AutoBaseline = d.ref()
				}
			}
		}
		r := first.res
		out.Resources[key] = Effective{Key: key, Kind: r.Kind, Resource: &r, Source: src}
	}
	out.Hash = hashState(out)
	return out
}

func hasBaseline(bs []BaselineRef, id string) bool {
	for _, b := range bs {
		if b.ID == id {
			return true
		}
	}
	return false
}

func hasGroup(gs []GroupRef, id string) bool {
	for _, g := range gs {
		if g.ID == id {
			return true
		}
	}
	return false
}

// sortDecls orders declarations for display only (labels are stable);
// resolution never depends on this order.
func sortDecls(ds []decl) {
	sort.SliceStable(ds, func(i, j int) bool {
		li, lj := ds[i].label(), ds[j].label()
		if li != lj {
			return li < lj
		}
		return string(ds[i].res.canonical()) < string(ds[j].res.canonical())
	})
}

func agreeLabel(level string, ds []decl) string {
	if len(ds) == 1 {
		return ds[0].label()
	}
	names := func(f func(decl) string) string {
		seen := map[string]bool{}
		var out []string
		for _, d := range ds {
			if n := f(d); !seen[n] {
				seen[n] = true
				out = append(out, n)
			}
		}
		sort.Strings(out)
		return strings.Join(out, ", ")
	}
	switch level {
	case LevelAssigned:
		return "Baselines " + names(func(d decl) string { return d.baseline.Name }) + ", assigned to this host (they agree)"
	case LevelGroup:
		return "Inherited from groups: " + names(func(d decl) string { return d.group.Name }) + " (they agree)"
	}
	return "Required by Global baselines " + names(func(d decl) string { return d.baseline.Name }) + " (they agree)"
}

func conflictLabel(level string, src Source) string {
	switch level {
	case LevelGroup:
		names := make([]string, 0, len(src.Groups))
		for _, g := range src.Groups {
			names = append(names, g.Name)
		}
		sort.Strings(names)
		if len(names) == 1 {
			// One group, two of its baselines.
			bs := make([]string, 0, len(src.Baselines))
			for _, b := range src.Baselines {
				bs = append(bs, b.Name)
			}
			sort.Strings(bs)
			return fmt.Sprintf("Conflict between baselines %s of group %s", joinAnd(bs), names[0])
		}
		return fmt.Sprintf("Conflict between groups %s (same priority %d)", joinAnd(names), src.Groups[0].Priority)
	case LevelAssigned:
		bs := make([]string, 0, len(src.Baselines))
		for _, b := range src.Baselines {
			bs = append(bs, b.Name)
		}
		sort.Strings(bs)
		return "Conflict between baselines " + joinAnd(bs) + " (both assigned to this host)"
	}
	bs := make([]string, 0, len(src.Baselines))
	for _, b := range src.Baselines {
		bs = append(bs, b.Name)
	}
	sort.Strings(bs)
	return "Conflict between Global baselines " + joinAnd(bs)
}

func joinAnd(xs []string) string {
	switch len(xs) {
	case 0:
		return ""
	case 1:
		return xs[0]
	}
	return strings.Join(xs[:len(xs)-1], ", ") + " and " + xs[len(xs)-1]
}

// hashState is a host's effective desired state's identity: values and
// where they come from (so a provenance change is noticed too).
func hashState(hs HostState) string {
	keys := make([]string, 0, len(hs.Resources)+len(hs.Excluded))
	for k := range hs.Resources {
		keys = append(keys, k)
	}
	for k := range hs.Excluded {
		keys = append(keys, "!"+k)
	}
	sort.Strings(keys)
	h := sha256.New()
	for _, k := range keys {
		h.Write([]byte(k))
		h.Write([]byte{0})
		if strings.HasPrefix(k, "!") {
			h.Write([]byte("excluded"))
			continue
		}
		e := hs.Resources[k]
		if e.Conflict {
			h.Write([]byte("conflict"))
			for _, c := range e.Source.Competing {
				h.Write([]byte(c.Label + "=" + c.Want + ";"))
			}
		} else {
			h.Write(e.Resource.canonical())
		}
		h.Write([]byte(e.Source.Label))
		for _, b := range e.Source.Baselines {
			fmt.Fprintf(h, "|%s@%d", b.ID, b.Revision)
		}
		if e.Source.Auto {
			h.Write([]byte("|auto"))
		}
		h.Write([]byte{'\n'})
	}
	return hex.EncodeToString(h.Sum(nil))
}
