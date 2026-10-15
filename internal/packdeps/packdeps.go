// Package packdeps answers "what depends on this Pack?" across every
// subsystem that can name one, for deleting a Pack safely (final After-M17
// batch, ARCHITECTURE-ALPHA §38). Deleting a Pack used to cascade silently
// through foreign keys (Desired State assignments, health settings,
// enrollment tokens, helper approvals) and leave dangling ids in JSON
// (firewall policies, schedule targets, notification routes). Now the
// references that change what happens to hosts block the deletion until the
// administrator changes them; the few things that are genuinely harmless
// to drop are listed before confirmation and dropped in the same
// transaction. The query runs inside the deleting transaction, so nothing
// can slip in between the check and the delete.
package packdeps

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"drool/internal/monitor/monsvc"
)

// Querier is a database or a transaction.
type Querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// Reference kinds that block a deletion.
const (
	KindFirewallPolicy    = "firewall_policy"
	KindSchedule          = "schedule"
	KindNotificationRoute = "notification_route"
	KindMaintenance       = "maintenance"
	KindDesiredAssignment = "desired_assignment"
	KindHealthOverride    = "health_override"
	KindEnrollmentToken   = "enrollment_token"
	KindNetworkRule       = "network_rule"
)

// Ref is one thing that names the Pack and must change before it can go.
type Ref struct {
	Kind   string `json:"kind"`
	ID     string `json:"id"`
	Name   string `json:"name"`
	Detail string `json:"detail"` // what to change, in words
	Path   string `json:"path"`   // the page where it changes
}

// Removal is something deleting the Pack drops with it, harmlessly.
type Removal struct {
	Kind   string `json:"kind"`
	Count  int    `json:"count"`
	Detail string `json:"detail"`
}

// Report is a Pack's dependencies.
type Report struct {
	PackID   string    `json:"pack_id"`
	PackName string    `json:"pack_name"`
	Members  int       `json:"members"`
	Blocking []Ref     `json:"blocking"`
	Removed  []Removal `json:"removed"`
}

// Deletable reports whether nothing blocks the deletion.
func (r *Report) Deletable() bool { return len(r.Blocking) == 0 }

// ErrNotFound: no such Pack.
var ErrNotFound = sql.ErrNoRows

// Query lists what depends on Pack id at now.
func Query(ctx context.Context, q Querier, id string, now time.Time) (*Report, error) {
	r := &Report{PackID: id, Blocking: []Ref{}, Removed: []Removal{}}
	if err := q.QueryRowContext(ctx, `SELECT name FROM groups WHERE id = ?`, id).Scan(&r.PackName); err != nil {
		return nil, err
	}
	if err := q.QueryRowContext(ctx, `SELECT count(*) FROM group_members WHERE group_id = ?`, id).Scan(&r.Members); err != nil {
		return nil, err
	}
	rows := func(query string, args []any, each func(*sql.Rows) error) error {
		rs, err := q.QueryContext(ctx, query, args...)
		if err != nil {
			return err
		}
		defer rs.Close()
		for rs.Next() {
			if err := each(rs); err != nil {
				return err
			}
		}
		return rs.Err()
	}
	block := func(ref Ref) { r.Blocking = append(r.Blocking, ref) }
	ms := now.UnixMilli()

	// Firewall policies: the Pack as a source or destination.
	if err := rows(`SELECT p.id, p.name,
		EXISTS (SELECT 1 FROM json_each(p.from_json) WHERE json_extract(value, '$.kind') = 'group' AND json_extract(value, '$.id') = ?1),
		EXISTS (SELECT 1 FROM json_each(p.to_json) WHERE json_extract(value, '$.kind') = 'group' AND json_extract(value, '$.id') = ?1)
		FROM fw_policies p WHERE json_valid(p.from_json) AND json_valid(p.to_json) ORDER BY p.position, p.id`, []any{id}, func(rs *sql.Rows) error {
		var pid, name string
		var from, to bool
		if err := rs.Scan(&pid, &name, &from, &to); err != nil {
			return err
		}
		if from || to {
			side := map[[2]bool]string{{true, false}: "as a source", {false, true}: "as a destination", {true, true}: "as a source and a destination"}[[2]bool{from, to}]
			block(Ref{Kind: KindFirewallPolicy, ID: pid, Name: name, Path: "/firewall?tab=designer",
				Detail: fmt.Sprintf("The firewall policy “%s” names this Pack %s. Take the Pack out of it (or delete the policy) in the Firewall's Policy tab.", name, side)})
		}
		return nil
	}); err != nil {
		return nil, err
	}

	// Schedules (runs and recurring maintenance), not archived.
	if err := rows(`SELECT s.id, s.name, s.kind FROM schedules s
		WHERE s.archived_at IS NULL AND json_valid(s.config_json)
		AND EXISTS (SELECT 1 FROM json_each(s.config_json, '$.targets.groups') WHERE value = ?)
		ORDER BY s.name, s.id`, []any{id}, func(rs *sql.Rows) error {
		var sid, name, kind string
		if err := rs.Scan(&sid, &name, &kind); err != nil {
			return err
		}
		what := "runs on"
		if kind == "maintenance" {
			what = "puts into maintenance"
		}
		block(Ref{Kind: KindSchedule, ID: sid, Name: name, Path: "/jobs/schedules/" + sid,
			Detail: fmt.Sprintf("The schedule “%s” %s this Pack. Change its targets or archive it.", name, what)})
		return nil
	}); err != nil {
		return nil, err
	}

	// Notification routes filtered by the Pack.
	if err := rows(`SELECT r.id, r.name FROM notify_routes r
		WHERE json_valid(r.group_ids_json) AND EXISTS (SELECT 1 FROM json_each(r.group_ids_json) WHERE value = ?)
		ORDER BY r.name, r.id`, []any{id}, func(rs *sql.Rows) error {
		var rid, name string
		if err := rs.Scan(&rid, &name); err != nil {
			return err
		}
		block(Ref{Kind: KindNotificationRoute, ID: rid, Name: name, Path: "/settings/notifications?advanced=1",
			Detail: fmt.Sprintf("The notification route “%s” sends alerts for this Pack's hosts. Take the Pack out of its filter (or delete the route).", name)})
		return nil
	}); err != nil {
		return nil, err
	}

	// Maintenance windows that still cover the Pack (ended ones are
	// history and keep its name).
	if err := rows(`SELECT m.id, m.reason, m.started_at FROM maintenance m JOIN maintenance_targets t ON t.maintenance_id = m.id
		WHERE t.kind = 'group' AND t.target_id = ? AND m.ended_at IS NULL AND (m.ends_at IS NULL OR m.ends_at > ?)
		ORDER BY m.started_at, m.id`, []any{id, ms}, func(rs *sql.Rows) error {
		var mid, reason string
		var started int64
		if err := rs.Scan(&mid, &reason, &started); err != nil {
			return err
		}
		name := reason
		if name == "" {
			name = "maintenance since " + time.UnixMilli(started).UTC().Format("2 Jan 15:04 MST")
		}
		block(Ref{Kind: KindMaintenance, ID: mid, Name: name, Path: "/alerts?view=maintenance",
			Detail: fmt.Sprintf("The maintenance window “%s” covers this Pack. End it first.", name)})
		return nil
	}); err != nil {
		return nil, err
	}

	// Desired State baselines assigned to the Pack.
	if err := rows(`SELECT b.id, b.name FROM ds_assignments a JOIN ds_baselines b ON b.id = a.baseline_id
		WHERE a.scope = 'group' AND a.group_id = ? AND b.archived_at IS NULL ORDER BY b.name, b.id`, []any{id}, func(rs *sql.Rows) error {
		var bid, name string
		if err := rs.Scan(&bid, &name); err != nil {
			return err
		}
		block(Ref{Kind: KindDesiredAssignment, ID: bid, Name: name, Path: "/desired/baselines/" + bid,
			Detail: fmt.Sprintf("The Desired State baseline “%s” is assigned to this Pack. Unassign it from the Pack.", name)})
		return nil
	}); err != nil {
		return nil, err
	}

	// Monitoring settings the Pack customizes (health checks, and the
	// expected state of services).
	if err := rows(`SELECT o.id, o.check_id, o.subject, o.subject_label, o.fields_json FROM mon_health_overrides o
		WHERE o.scope = 'group' AND o.group_id = ? ORDER BY o.check_id, o.subject, o.id`, []any{id}, func(rs *sql.Rows) error {
		var oid, check, subject, label, fields string
		if err := rs.Scan(&oid, &check, &subject, &label, &fields); err != nil {
			return err
		}
		name := check
		if spec, ok := monsvc.Spec(check); ok {
			name = spec.Label
		}
		if subject != "" {
			if label == "" {
				label = strings.TrimPrefix(subject, "unit:")
			}
			name += " for " + label
		}
		block(Ref{Kind: KindHealthOverride, ID: oid, Name: name, Path: "/groups/" + id,
			Detail: fmt.Sprintf("This Pack customizes the monitoring setting “%s” (%s) for its hosts. Reset it to inherited on the Pack's page.", name, fieldSummary(fields))})
		return nil
	}); err != nil {
		return nil, err
	}

	// Enrollment tokens still able to add new hosts to the Pack.
	if err := rows(`SELECT t.id, t.description, t.expires_at FROM agent_enrollment_tokens t
		WHERE t.group_id = ? AND t.used_at IS NULL AND t.revoked_at IS NULL AND t.expires_at > ? ORDER BY t.expires_at, t.id`, []any{id, ms}, func(rs *sql.Rows) error {
		var tid, desc string
		var exp int64
		if err := rs.Scan(&tid, &desc, &exp); err != nil {
			return err
		}
		name := desc
		if name == "" {
			name = "enrollment token " + tid
		}
		block(Ref{Kind: KindEnrollmentToken, ID: tid, Name: name, Path: "/agents?tab=enroll",
			Detail: fmt.Sprintf("The Shepherd enrollment token “%s” adds new hosts to this Pack until %s. Revoke it, or let it expire.", name,
				time.UnixMilli(exp).UTC().Format("2 Jan 2006 15:04 MST"))})
		return nil
	}); err != nil {
		return nil, err
	}

	// Network classifications that match the Pack (on either side).
	if tableExists(ctx, q, "net_flow_rules") {
		if err := rows(`SELECT r.id, r.label FROM net_flow_rules r WHERE r.host_group_id = ?1 OR r.remote_group_id = ?1 ORDER BY r.label, r.id`, []any{id},
			func(rs *sql.Rows) error {
				var rid, label string
				if err := rs.Scan(&rid, &label); err != nil {
					return err
				}
				block(Ref{Kind: KindNetworkRule, ID: rid, Name: label, Path: "/network?tab=classifications",
					Detail: fmt.Sprintf("The network classification “%s” matches this Pack. Change it to other hosts, or delete it.", label)})
				return nil
			}); err != nil {
			return nil, err
		}
	}

	// What goes with the Pack, harmlessly.
	if r.Members > 0 {
		r.Removed = append(r.Removed, Removal{Kind: "membership", Count: r.Members,
			Detail: fmt.Sprintf("%d host%s leave%s the Pack; the hosts stay in DROOL.", r.Members, plural(r.Members), map[bool]string{true: "s", false: ""}[r.Members == 1])})
	}
	count := func(query string, args ...any) int {
		var n int
		q.QueryRowContext(ctx, query, args...).Scan(&n)
		return n
	}
	var prio int
	if q.QueryRowContext(ctx, `SELECT priority FROM mon_group_health WHERE group_id = ?`, id).Scan(&prio) == nil {
		r.Removed = append(r.Removed, Removal{Kind: "monitoring_priority", Count: 1,
			Detail: fmt.Sprintf("Its monitoring priority (%d): it only ranked this Pack's own monitoring settings.", prio)})
	}
	if q.QueryRowContext(ctx, `SELECT priority FROM ds_group_priority WHERE group_id = ?`, id).Scan(&prio) == nil {
		r.Removed = append(r.Removed, Removal{Kind: "desired_priority", Count: 1,
			Detail: fmt.Sprintf("Its Desired State priority (%d): it only ranked this Pack's own baselines.", prio)})
	}
	var timeout sql.NullInt64
	if q.QueryRowContext(ctx, `SELECT rollback_timeout_s FROM fw_group_config WHERE group_id = ?`, id).Scan(&timeout) == nil && timeout.Valid {
		r.Removed = append(r.Removed, Removal{Kind: "firewall_timeout", Count: 1,
			Detail: fmt.Sprintf("Its firewall rollback timeout (%d s): its hosts use their own or the default again; a firewall change still rolls back by itself if DROOL can't confirm it.", timeout.Int64)})
	}
	if n := count(`SELECT count(*) FROM mon_helper_approvals WHERE scope = 'group' AND group_id = ?`, id); n > 0 {
		var pkgs string
		q.QueryRowContext(ctx, `SELECT group_concat(package, ', ') FROM (SELECT package FROM mon_helper_approvals WHERE scope = 'group' AND group_id = ? ORDER BY package)`, id).Scan(&pkgs)
		r.Removed = append(r.Removed, Removal{Kind: "helper_approvals", Count: n,
			Detail: fmt.Sprintf("Its standing approval to install monitoring helpers (%s) on its hosts: those installs ask again.", pkgs)})
	}
	if n := count(`SELECT count(*) FROM agent_enrollment_tokens WHERE group_id = ? AND NOT (used_at IS NULL AND revoked_at IS NULL AND expires_at > ?)`, id, ms); n > 0 {
		r.Removed = append(r.Removed, Removal{Kind: "token_history", Count: n,
			Detail: fmt.Sprintf("%d used, revoked or expired enrollment token%s keep%s its history, without the Pack.", n, plural(n), map[bool]string{true: "s", false: ""}[n == 1])})
	}
	return r, nil
}

// ReleaseTx detaches what is kept when the Pack goes (enrollment token
// history, which would otherwise cascade away). Call it after Query found
// nothing blocking, in the same transaction, before deleting the Pack.
func ReleaseTx(ctx context.Context, q Querier, id string) error {
	_, err := q.ExecContext(ctx, `UPDATE agent_enrollment_tokens SET group_id = NULL WHERE group_id = ?`, id)
	return err
}

func tableExists(ctx context.Context, q Querier, name string) bool {
	var n int
	q.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, name).Scan(&n)
	return n > 0
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// fieldSummary says what a stored override sets ("enabled: off, warning: 80").
func fieldSummary(fieldsJSON string) string {
	var f map[string]any
	if json.Unmarshal([]byte(fieldsJSON), &f) != nil || len(f) == 0 {
		return "no values"
	}
	keys := make([]string, 0, len(f))
	for k := range f {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		v := f[k]
		switch x := v.(type) {
		case bool:
			v = map[bool]string{true: "on", false: "off"}[x]
		}
		parts = append(parts, fmt.Sprintf("%s: %v", strings.ReplaceAll(k, "_", " "), v))
	}
	return strings.Join(parts, ", ")
}
