// Package hostrefs takes a removed host out of the configuration that names
// it by id (dogfooding correction batch, ARCHITECTURE-ALPHA §40.7).
// Foreign keys clean up what belongs to a host; two kinds of configuration
// name hosts inside JSON instead, and kept a removed host's id:
//
//   - a schedule's targets, exclusions and canary hosts: every later save
//     of the schedule, and every run, failed with "host … not found";
//   - a firewall policy's sources and destinations: every later save of
//     the policy failed with "unknown host", and the YAML view couldn't
//     name it.
//
// Removing a host now takes it out of both in the removing transaction. A
// schedule left without targets is disabled and says why; a firewall
// policy left without a source or destination is deleted (it could never
// match again, and a policy needs both). Nothing widens: a host taken out of
// a list only narrows it. Notification routes keep a removed host on
// purpose (dropping it could widen a route to every host), so they aren't
// touched. Repair finds what earlier removals left behind.
package hostrefs

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"drool/internal/audit"
	"drool/internal/firewall/policy"
	"drool/internal/sched"
	"drool/internal/store/db"
)

// Querier is a database or a transaction.
type Querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// Kinds of configuration that name hosts.
const (
	KindSchedule       = "schedule"
	KindFirewallPolicy = "firewall_policy"
)

// Change is one thing that names the host, and what removing the host does
// to it.
type Change struct {
	Kind   string `json:"kind"`
	ID     string `json:"id"`
	Name   string `json:"name"`
	Detail string `json:"detail"` // in words
	Path   string `json:"path"`   // where it is in the UI
	// Disabled: a schedule left without targets. Deleted: a firewall
	// policy left without a source or a destination.
	Disabled bool `json:"disabled,omitempty"`
	Deleted  bool `json:"deleted,omitempty"`
}

// Actor is who removes the host (a schedule's new revision says so).
type Actor struct{ UserID, Name string }

// Plan lists what removing host id (named name) would change. Nothing is
// written.
func Plan(ctx context.Context, q Querier, id, name string) ([]Change, error) {
	return walk(ctx, q, id, name, nil, time.Time{})
}

// ReleaseTx takes host id out of every schedule and firewall policy that
// names it, in the caller's transaction (the one removing the host), and
// returns what changed.
func ReleaseTx(ctx context.Context, q Querier, id, name string, by Actor, now time.Time) ([]Change, error) {
	return walk(ctx, q, id, name, &by, now)
}

func walk(ctx context.Context, q Querier, id, name string, by *Actor, now time.Time) ([]Change, error) {
	if name == "" {
		name = "a removed host"
	}
	sc, err := schedules(ctx, q, id, name, by, now)
	if err != nil {
		return nil, err
	}
	fw, err := firewallPolicies(ctx, q, id, name, by != nil, now)
	if err != nil {
		return nil, err
	}
	return append(sc, fw...), nil
}

// schedules takes the host out of schedules that aren't archived: their
// targets, exclusions and canary hosts, in a new revision.
func schedules(ctx context.Context, q Querier, id, name string, by *Actor, now time.Time) ([]Change, error) {
	type row struct {
		id, name, kind, cfg, owner, attention string
		enabled                               bool
		revision                              int64
	}
	var list []row
	rs, err := q.QueryContext(ctx, `SELECT id, name, kind, enabled, revision, config_json, COALESCE(owner_id, ''), attention FROM schedules
		WHERE archived_at IS NULL AND json_valid(config_json) AND (
			EXISTS (SELECT 1 FROM json_each(config_json, '$.targets.hosts') WHERE value = ?1) OR
			EXISTS (SELECT 1 FROM json_each(config_json, '$.targets.exclude_hosts') WHERE value = ?1) OR
			EXISTS (SELECT 1 FROM json_each(config_json, '$.strategy.canary_hosts') WHERE value = ?1))
		ORDER BY name, id`, id)
	if err != nil {
		return nil, err
	}
	for rs.Next() {
		var r row
		if err := rs.Scan(&r.id, &r.name, &r.kind, &r.enabled, &r.revision, &r.cfg, &r.owner, &r.attention); err != nil {
			rs.Close()
			return nil, err
		}
		list = append(list, r)
	}
	rs.Close()
	if err := rs.Err(); err != nil {
		return nil, err
	}
	var out []Change
	for _, r := range list {
		var c sched.Config
		if err := json.Unmarshal([]byte(r.cfg), &c); err != nil {
			return nil, fmt.Errorf("schedule %s: %w", r.id, err)
		}
		var parts []string
		var target, excluded, canary bool
		c.Targets.Hosts, target = without(c.Targets.Hosts, id)
		c.Targets.ExcludeHosts, excluded = without(c.Targets.ExcludeHosts, id)
		c.Strategy.CanaryHosts, canary = without(c.Strategy.CanaryHosts, id)
		if target {
			parts = append(parts, name+" is taken out of its targets.")
		}
		if excluded {
			parts = append(parts, name+" is taken out of its exclusions.")
		}
		if canary {
			if len(c.Strategy.CanaryHosts) == 0 {
				// Keep the safety net: a canary all the same (as a run
				// does when its chosen canaries aren't among the targets).
				c.Strategy.CanaryCount = 1
				parts = append(parts, name+" was its canary host: the first host by name is the canary instead.")
			} else {
				parts = append(parts, name+" is taken out of its canary hosts.")
			}
		}
		ch := Change{Kind: KindSchedule, ID: r.id, Name: r.name, Path: "/jobs/schedules/" + r.id}
		attention := r.attention
		if strings.Contains(attention, id) {
			attention = "" // "The last run could not start: host <id> not found" is answered now
		}
		enabled := r.enabled
		if c.Targets.Empty() {
			ch.Disabled = true
			what := "runs on"
			if r.kind == sched.ScheduleMaintenance {
				what = "puts into maintenance"
			}
			parts = append(parts, fmt.Sprintf("It has nothing left that it %s, so it's disabled until its targets are chosen again.", what))
			enabled = false
			attention = "Disabled: its only target, " + name + ", was removed from DROOL. Choose its targets, then enable it."
		}
		ch.Detail = strings.Join(parts, " ")
		out = append(out, ch)
		if by == nil {
			continue
		}
		cfg, err := json.Marshal(c)
		if err != nil {
			return nil, err
		}
		ms := now.UnixMilli()
		var attAt any
		if attention != "" {
			attAt = ms
		}
		rev := r.revision + 1
		if _, err := q.ExecContext(ctx, `UPDATE schedules SET revision = ?, config_json = ?, enabled = ?, attention = ?,
			attention_at = CASE WHEN ? = '' THEN NULL WHEN attention = ? THEN attention_at ELSE ? END, updated_by = ?, updated_at = ? WHERE id = ?`,
			rev, string(cfg), b2i(enabled), attention, attention, attention, attAt, nullStr(by.UserID), ms, r.id); err != nil {
			return nil, err
		}
		if _, err := q.ExecContext(ctx, `INSERT INTO schedule_revisions (schedule_id, revision, config_json, owner_id, changed_by_name, changed_at)
			VALUES (?, ?, ?, ?, ?, ?)`, r.id, rev, string(cfg), nullStr(r.owner), by.Name, ms); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// firewallPolicies takes the host out of firewall policies' sources and
// destinations. Compiling already ignored a removed host (it matched no
// address and no host), so nothing allowed or blocked changes.
func firewallPolicies(ctx context.Context, q Querier, id, name string, write bool, now time.Time) ([]Change, error) {
	type row struct{ id, name, from, to string }
	var list []row
	rs, err := q.QueryContext(ctx, `SELECT id, name, from_json, to_json FROM fw_policies
		WHERE json_valid(from_json) AND json_valid(to_json) AND (
			EXISTS (SELECT 1 FROM json_each(from_json) WHERE json_extract(value, '$.kind') = 'host' AND json_extract(value, '$.id') = ?1) OR
			EXISTS (SELECT 1 FROM json_each(to_json) WHERE json_extract(value, '$.kind') = 'host' AND json_extract(value, '$.id') = ?1))
		ORDER BY position, id`, id)
	if err != nil {
		return nil, err
	}
	for rs.Next() {
		var r row
		if err := rs.Scan(&r.id, &r.name, &r.from, &r.to); err != nil {
			rs.Close()
			return nil, err
		}
		list = append(list, r)
	}
	rs.Close()
	if err := rs.Err(); err != nil {
		return nil, err
	}
	var out []Change
	for _, r := range list {
		var from, to []policy.Selector
		if err := json.Unmarshal([]byte(r.from), &from); err != nil {
			return nil, fmt.Errorf("firewall policy %s: %w", r.id, err)
		}
		if err := json.Unmarshal([]byte(r.to), &to); err != nil {
			return nil, fmt.Errorf("firewall policy %s: %w", r.id, err)
		}
		isHost := func(s policy.Selector) bool { return s.Kind == policy.SelHost && s.ID == id }
		inFrom, inTo := slices.ContainsFunc(from, isHost), slices.ContainsFunc(to, isHost)
		from, to = slices.DeleteFunc(from, isHost), slices.DeleteFunc(to, isHost)
		side := map[[2]bool]string{{true, false}: "sources", {false, true}: "destinations", {true, true}: "sources and destinations"}[[2]bool{inFrom, inTo}]
		ch := Change{Kind: KindFirewallPolicy, ID: r.id, Name: r.name, Path: "/firewall?tab=designer"}
		if len(from) == 0 || len(to) == 0 {
			ch.Deleted = true
			only := "source"
			if len(to) == 0 {
				only = "destination"
			}
			ch.Detail = fmt.Sprintf("Its only %s was %s, so it's deleted: it could never match again. Nothing allowed or blocked changes: a removed host already matched nothing.", only, name)
		} else {
			ch.Detail = fmt.Sprintf("%s is taken out of its %s. Nothing allowed or blocked changes: a removed host already matched nothing.", name, side)
		}
		out = append(out, ch)
		if !write {
			continue
		}
		if ch.Deleted {
			if _, err := q.ExecContext(ctx, `DELETE FROM fw_policies WHERE id = ?`, r.id); err != nil {
				return nil, err
			}
			continue
		}
		fj, _ := json.Marshal(from)
		tj, _ := json.Marshal(to)
		if _, err := q.ExecContext(ctx, `UPDATE fw_policies SET from_json = ?, to_json = ?, updated_at = ? WHERE id = ?`,
			string(fj), string(tj), now.UnixMilli(), r.id); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// Removed is a host that configuration still names although it no longer
// exists (removed before DROOL took removed hosts out by itself).
type Removed struct {
	ID   string
	Name string // from the audit log's host.delete; "" when unknown
}

// RepairTx takes every host that no longer exists out of the schedules and
// firewall policies that still name it (left by removals before DROOL did
// that itself), as DROOL's own audited changes. It runs at start; emit the
// returned records after the commit.
func RepairTx(ctx context.Context, tx *sql.Tx, w *audit.Writer, now time.Time) ([]Removed, [][]Change, []func(), error) {
	gone, err := Vanished(ctx, tx)
	if err != nil {
		return nil, nil, nil, err
	}
	all := make([][]Change, len(gone))
	var emits []func()
	for i, h := range gone {
		changes, err := ReleaseTx(ctx, tx, h.ID, h.Name, Actor{Name: "DROOL"}, now)
		if err != nil {
			return nil, nil, nil, err
		}
		e, err := RecordTx(ctx, w, db.New(tx), &audit.System, h.Name, changes)
		if err != nil {
			return nil, nil, nil, err
		}
		all[i], emits = changes, append(emits, e...)
	}
	return gone, all, emits, nil
}

// Vanished lists the hosts that schedules and firewall policies still name
// but that no longer exist.
func Vanished(ctx context.Context, q Querier) ([]Removed, error) {
	rs, err := q.QueryContext(ctx, `SELECT DISTINCT h FROM (
			SELECT j.value AS h FROM schedules s, json_each(s.config_json, '$.targets.hosts') j WHERE s.archived_at IS NULL AND json_valid(s.config_json)
			UNION SELECT j.value FROM schedules s, json_each(s.config_json, '$.targets.exclude_hosts') j WHERE s.archived_at IS NULL AND json_valid(s.config_json)
			UNION SELECT j.value FROM schedules s, json_each(s.config_json, '$.strategy.canary_hosts') j WHERE s.archived_at IS NULL AND json_valid(s.config_json)
			UNION SELECT json_extract(j.value, '$.id') FROM fw_policies p, json_each(p.from_json) j
				WHERE json_valid(p.from_json) AND json_extract(j.value, '$.kind') = 'host'
			UNION SELECT json_extract(j.value, '$.id') FROM fw_policies p, json_each(p.to_json) j
				WHERE json_valid(p.to_json) AND json_extract(j.value, '$.kind') = 'host')
		WHERE typeof(h) = 'text' AND h <> '' AND h NOT IN (SELECT id FROM hosts) ORDER BY h`)
	if err != nil {
		return nil, err
	}
	var out []Removed
	for rs.Next() {
		var r Removed
		if err := rs.Scan(&r.ID); err != nil {
			rs.Close()
			return nil, err
		}
		out = append(out, r)
	}
	rs.Close()
	if err := rs.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		var n sql.NullString
		q.QueryRowContext(ctx, `SELECT json_extract(details_json, '$.name') FROM audit_events WHERE action = 'host.delete' AND target_id = ?
			ORDER BY ts DESC LIMIT 1`, out[i].ID).Scan(&n)
		out[i].Name = n.String
	}
	return out, nil
}

// RecordTx audits each schedule or firewall policy a removed host was
// taken out of, as its own change: by the user removing the host (actor
// nil: from the request), or by DROOL (actor set) when Repair found it.
// Emit the returned records after the commit.
func RecordTx(ctx context.Context, w *audit.Writer, q *db.Queries, actor *audit.Actor, hostName string, changes []Change) ([]func(), error) {
	if hostName == "" {
		hostName = "that no longer exists"
	}
	var emits []func()
	for _, c := range changes {
		ev := audit.Event{Actor: actor, TargetID: c.ID, Details: map[string]any{"name": c.Name, "change": c.Detail,
			"reason": "the host " + hostName + " was removed from DROOL"}}
		switch {
		case c.Kind == KindSchedule:
			ev.Action, ev.TargetType = "schedule.update", "schedule"
			if c.Disabled {
				ev.Details["enabled"] = false
			}
		case c.Deleted:
			ev.Action, ev.TargetType = "firewall.policy.delete", "firewall_policy"
		default:
			ev.Action, ev.TargetType = "firewall.policy.update", "firewall_policy"
		}
		emit, err := w.RecordTx(ctx, q, ev)
		if err != nil {
			return nil, err
		}
		emits = append(emits, emit)
	}
	return emits, nil
}

// Summaries names what a removed host was taken out of (for the
// host.delete audit record).
func Summaries(changes []Change) []map[string]any {
	out := make([]map[string]any, 0, len(changes))
	for _, c := range changes {
		out = append(out, map[string]any{"kind": c.Kind, "id": c.ID, "name": c.Name, "change": c.Detail})
	}
	return out
}

func without(list []string, id string) ([]string, bool) {
	if !slices.Contains(list, id) {
		return list, false
	}
	return slices.DeleteFunc(slices.Clone(list), func(s string) bool { return s == id }), true
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}
