package desired

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Saving a baseline (or a group priority) first shows what it would do:
// the resources it adds, removes and changes, the hosts it affects, and
// — judged against what DROOL observed most recently — which compliant
// hosts would drift, which drifted ones would comply, which conflicts
// appear or are resolved, and what can't be predicted until the hosts
// are checked. Nothing is saved by a preview.

// ResourceChange is a resource added, removed or changed by an edit.
type ResourceChange struct {
	Key  string `json:"key"`
	Name string `json:"name"`
	Kind string `json:"kind"`
	From string `json:"from,omitempty"`
	To   string `json:"to,omitempty"`
}

// HostImpact is what an edit does to one host.
type HostImpact struct {
	HostID   string   `json:"host_id"`
	HostName string   `json:"host_name"`
	Now      string   `json:"now"`
	After    string   `json:"after"`
	Detail   []string `json:"detail"`
}

// Impact is an edit's predicted effect.
type Impact struct {
	Changed            bool             `json:"changed"`
	Added              []ResourceChange `json:"added"`
	Removed            []ResourceChange `json:"removed"`
	Modified           []ResourceChange `json:"modified"`
	AssignmentsAdded   []Assignment     `json:"assignments_added"`
	AssignmentsRemoved []Assignment     `json:"assignments_removed"`
	HostsAffected      int              `json:"hosts_affected"`
	// The hosts, by what happens to them (each list at most 50; the
	// counts are complete).
	BecomeDrifted     []HostImpact `json:"become_drifted"`
	BecomeCompliant   []HostImpact `json:"become_compliant"`
	NewConflicts      []HostImpact `json:"new_conflicts"`
	ResolvedConflicts []HostImpact `json:"resolved_conflicts"`
	ToCheck           []HostImpact `json:"to_check"`
	Counts            ImpactCounts `json:"counts"`
	// Auto: the baseline restores automatically, so new drift would be
	// corrected without asking.
	Auto     bool     `json:"auto"`
	Warnings []string `json:"warnings"`
}

// ImpactCounts are the complete counts behind the lists.
type ImpactCounts struct {
	BecomeDrifted     int `json:"become_drifted"`
	BecomeCompliant   int `json:"become_compliant"`
	NewConflicts      int `json:"new_conflicts"`
	ResolvedConflicts int `json:"resolved_conflicts"`
	ToCheck           int `json:"to_check"`
	Unchanged         int `json:"unchanged"`
}

// clone copies a snapshot's maps (values are shared and never modified).
func (s *Snapshot) clone() *Snapshot {
	c := &Snapshot{Gen: s.Gen, Baselines: map[string]*Baseline{}, ByGroup: map[string][]string{}, ByHost: map[string][]string{},
		Overrides: s.Overrides, Groups: map[string]Group{}, Members: s.Members, Hosts: s.Hosts}
	for k, v := range s.Baselines {
		c.Baselines[k] = v
	}
	c.Global = append([]string(nil), s.Global...)
	for k, v := range s.ByGroup {
		c.ByGroup[k] = append([]string(nil), v...)
	}
	for k, v := range s.ByHost {
		c.ByHost[k] = append([]string(nil), v...)
	}
	for k, v := range s.Groups {
		c.Groups[k] = v
	}
	return c
}

func without(xs []string, x string) []string {
	out := xs[:0]
	for _, y := range xs {
		if y != x {
			out = append(out, y)
		}
	}
	return out
}

// withBaseline returns the snapshot with a baseline replaced (or added).
func (s *Snapshot) withBaseline(id, name string, rev int, rs []Resource, as []Assignment) *Snapshot {
	c := s.clone()
	auto := false
	if old := s.Baselines[id]; old != nil {
		auto = old.Auto
	}
	b := &Baseline{ID: id, Name: name, Revision: rev, Resources: map[string]Resource{}, Auto: auto}
	for _, r := range rs {
		b.Resources[r.Key()] = r
	}
	c.Baselines[id] = b
	c.Global = without(c.Global, id)
	for g := range c.ByGroup {
		c.ByGroup[g] = without(c.ByGroup[g], id)
	}
	for h := range c.ByHost {
		c.ByHost[h] = without(c.ByHost[h], id)
	}
	for _, a := range as {
		switch a.Scope {
		case "global":
			c.Global = append(c.Global, id)
		case "group":
			c.ByGroup[a.GroupID] = append(c.ByGroup[a.GroupID], id)
		case "host":
			c.ByHost[a.HostID] = append(c.ByHost[a.HostID], id)
		}
	}
	return c
}

// PreviewBaseline shows what saving in would do (id "" for a new one).
func (s *Service) PreviewBaseline(ctx context.Context, id string, in BaselineInput) (*Impact, error) {
	if err := s.normalizeInput(ctx, s.d.Store.Reader, &in); err != nil {
		return nil, err
	}
	s.fresh(ctx)
	snap := s.Snapshot()
	if snap == nil {
		return nil, fmt.Errorf("desired state is starting")
	}
	imp := &Impact{Added: []ResourceChange{}, Removed: []ResourceChange{}, Modified: []ResourceChange{}, AssignmentsAdded: []Assignment{},
		AssignmentsRemoved: []Assignment{}, Warnings: []string{}}
	var old []Resource
	var oldAs []Assignment
	rev := 1
	key := "new"
	if id != "" {
		cur, err := currentRevision(ctx, s.d.Store.Reader, id)
		if err != nil {
			return nil, err
		}
		old, oldAs, rev, key = cur.Resources, cur.Assignments, cur.Revision+1, id
		imp.Changed = cur.Name != in.Name || cur.Description != in.Description || HashResources(old) != HashResources(in.Resources) ||
			mustJSON(stripNames(oldAs)) != mustJSON(stripNames(in.Assignments))
		if !imp.Changed {
			rev = cur.Revision
		}
		if b := snap.Baselines[id]; b != nil {
			imp.Auto = b.Auto
		}
	} else {
		imp.Changed = true
	}
	ch := diffResources(old, in.Resources)
	byKey := func(rs []Resource) map[string]Resource {
		m := map[string]Resource{}
		for _, r := range rs {
			m[r.Key()] = r
		}
		return m
	}
	om, nm := byKey(old), byKey(in.Resources)
	for _, k := range ch.added {
		imp.Added = append(imp.Added, ResourceChange{Key: k, Name: Describe(k), Kind: KeyKind(k), To: nm[k].Want()})
	}
	for _, k := range ch.removed {
		imp.Removed = append(imp.Removed, ResourceChange{Key: k, Name: Describe(k), Kind: KeyKind(k), From: om[k].Want()})
	}
	for _, k := range ch.changed {
		imp.Modified = append(imp.Modified, ResourceChange{Key: k, Name: Describe(k), Kind: KeyKind(k), From: om[k].Want(), To: nm[k].Want()})
	}
	oa, na := map[string]Assignment{}, map[string]Assignment{}
	for _, a := range oldAs {
		oa[a.key()] = a
	}
	for _, a := range in.Assignments {
		na[a.key()] = a
		if _, ok := oa[a.key()]; !ok {
			imp.AssignmentsAdded = append(imp.AssignmentsAdded, a)
		}
	}
	for _, a := range oldAs {
		if _, ok := na[a.key()]; !ok {
			imp.AssignmentsRemoved = append(imp.AssignmentsRemoved, a)
		}
	}
	if len(in.Assignments) == 0 && len(in.Resources) > 0 {
		imp.Warnings = append(imp.Warnings, "the baseline isn't assigned anywhere yet, so it applies to no host")
	}
	mod := snap.withBaseline(key, in.Name, rev, in.Resources, in.Assignments)
	if err := s.predict(ctx, snap, mod, imp); err != nil {
		return nil, err
	}
	if imp.Auto && imp.Counts.BecomeDrifted > 0 {
		imp.Warnings = append(imp.Warnings, fmt.Sprintf("automatic restore is on for this baseline: DROOL will correct %s without asking, in one rollout",
			plural(imp.Counts.BecomeDrifted, "host", "hosts")))
	}
	return imp, nil
}

// PreviewPriority shows what changing a group's priority would do.
func (s *Service) PreviewPriority(ctx context.Context, groupID string, priority int) (*Impact, error) {
	s.fresh(ctx)
	snap := s.Snapshot()
	if snap == nil {
		return nil, fmt.Errorf("desired state is starting")
	}
	g, ok := snap.Groups[groupID]
	if !ok {
		return nil, ErrNotFound
	}
	imp := &Impact{Added: []ResourceChange{}, Removed: []ResourceChange{}, Modified: []ResourceChange{}, AssignmentsAdded: []Assignment{},
		AssignmentsRemoved: []Assignment{}, Warnings: []string{}, Changed: g.Priority != priority}
	mod := snap.clone()
	g.Priority = priority
	mod.Groups[groupID] = g
	if err := s.predict(ctx, snap, mod, imp); err != nil {
		return nil, err
	}
	return imp, nil
}

// predict compares every host's effective state under two snapshots and
// judges the new one against the latest observations.
func (s *Service) predict(ctx context.Context, before, after *Snapshot, imp *Impact) error {
	ids := make([]string, 0, len(after.Hosts))
	for id := range after.Hosts {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		return strings.ToLower(after.Hosts[ids[i]]) < strings.ToLower(after.Hosts[ids[j]])
	})
	add := func(list *[]HostImpact, n *int, hi HostImpact) {
		*n++
		if len(*list) < 50 {
			*list = append(*list, hi)
		}
	}
	for _, id := range ids {
		o, n := before.Resolve(id), after.Resolve(id)
		if valueHash(o) == valueHash(n) {
			continue // the same desired values (a new revision number alone changes nothing)
		}
		imp.HostsAffected++
		rows, err := loadRows(ctx, s.d.Store.Reader, id)
		if err != nil {
			return err
		}
		host, err := loadHost(ctx, s.d.Store.Reader, id)
		if err != nil {
			return err
		}
		var units []string
		for k, e := range n.Resources {
			if e.Kind == KindService {
				units = append(units, KeyName(k))
			}
		}
		svc, err := loadServices(ctx, s.d.Store.Reader, id, units)
		if err != nil {
			return err
		}
		now := s.nowMs()
		var cAfter counts
		var detail []string
		unknown := false
		// Conflicts appear or go per resource, whatever else the host's
		// status is dominated by (a failed restore elsewhere hides nothing).
		resolvedConflict, newConflict, resolvedToDrift := false, false, false
		keys := map[string]bool{}
		for k := range o.Resources {
			keys[k] = true
		}
		for k := range n.Resources {
			keys[k] = true
		}
		sorted := make([]string, 0, len(keys))
		for k := range keys {
			sorted = append(sorted, k)
		}
		sort.Strings(sorted)
		for _, k := range sorted {
			oe, had := o.Resources[k]
			ne, has := n.Resources[k]
			wasConflict, isConflict := had && oe.Conflict, has && ne.Conflict
			if wasConflict && !isConflict {
				resolvedConflict = true
			}
			if isConflict && !wasConflict {
				newConflict = true
			}
			cur := StatusUnknown
			if r := rows[k]; r != nil {
				cur = r.Status
			}
			if !has {
				if had {
					detail = append(detail, Describe(k)+": no longer managed")
				}
				continue
			}
			same := had && oe.Conflict == ne.Conflict && (ne.Conflict || oe.Resource.Same(*ne.Resource))
			if same {
				cAfter.add(cur, false)
				continue
			}
			if ne.Conflict {
				cAfter.add(StatusConflict, false)
				detail = append(detail, Describe(k)+": "+ne.Source.Label)
				continue
			}
			var obs Observation
			have := false
			if ne.Kind == KindService {
				obs, have, _, _ = s.observeService(ctx, svc, KeyName(k), now, 0)
			} else if r := rows[k]; r != nil && r.Observed != "" && json.Unmarshal([]byte(r.Observed), &obs) == nil && obs.Error == "" {
				have = true
			}
			if !have {
				unknown = true
				cAfter.add(StatusUnknown, false)
				detail = append(detail, Describe(k)+": "+ne.Resource.Want()+" — checked after saving")
				continue
			}
			j := Compare(*ne.Resource, obs)
			if j.Compliant() {
				cAfter.add(StatusCompliant, false)
				detail = append(detail, Describe(k)+": "+ne.Resource.Want()+" — already so")
			} else {
				cAfter.add(StatusDrift, false)
				detail = append(detail, Describe(k)+": "+ne.Resource.Want()+" — differs ("+DiffText(j.Diffs)+")")
				if wasConflict {
					resolvedToDrift = true
				}
			}
		}
		nowStatus := StatusNone
		if host.exists {
			nowStatus = host.Status
		}
		afterStatus := hostStatus(cAfter)
		if len(detail) > 8 {
			detail = append(detail[:8], fmt.Sprintf("and %d more", len(detail)-8))
		}
		hi := HostImpact{HostID: id, HostName: after.Hosts[id], Now: nowStatus, After: afterStatus, Detail: detail}
		bad := func(st string) bool { return st == StatusDrift || st == StatusFailed || st == StatusConflict }
		listed := false
		if newConflict {
			add(&imp.NewConflicts, &imp.Counts.NewConflicts, hi)
			listed = true
		}
		if resolvedConflict {
			add(&imp.ResolvedConflicts, &imp.Counts.ResolvedConflicts, hi)
			listed = true
		}
		switch {
		case resolvedToDrift || !bad(nowStatus) && bad(afterStatus) && !newConflict:
			// Drift appears (a resolved conflict's value differs, or a
			// host that was fine would differ).
			add(&imp.BecomeDrifted, &imp.Counts.BecomeDrifted, hi)
		case bad(nowStatus) && afterStatus == StatusCompliant:
			add(&imp.BecomeCompliant, &imp.Counts.BecomeCompliant, hi)
		case unknown && !listed:
			add(&imp.ToCheck, &imp.Counts.ToCheck, hi)
		case !listed:
			imp.Counts.Unchanged++
		}
		if unknown && afterStatus != StatusUnknown && (afterStatus == StatusConflict || bad(afterStatus) || afterStatus == StatusCompliant) {
			// Some resources can't be predicted: say so as well.
			if !containsHost(imp.ToCheck, id) {
				add(&imp.ToCheck, &imp.Counts.ToCheck, hi)
			}
		}
	}
	return nil
}

// valueHash identifies a host's desired values only (not where they come
// from).
func valueHash(hs HostState) string {
	keys := make([]string, 0, len(hs.Resources)+len(hs.Excluded))
	for k := range hs.Resources {
		keys = append(keys, k)
	}
	for k := range hs.Excluded {
		keys = append(keys, "!"+k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		b.WriteString(k)
		b.WriteByte(0)
		if e, ok := hs.Resources[k]; ok {
			if e.Conflict {
				for _, c := range e.Source.Competing {
					b.WriteString(c.Want + ";")
				}
			} else {
				b.Write(e.Resource.canonical())
			}
		}
		b.WriteByte('\n')
	}
	return b.String()
}

func containsHost(hs []HostImpact, id string) bool {
	for _, h := range hs {
		if h.HostID == id {
			return true
		}
	}
	return false
}
