package desired

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"drool/internal/audit"
	"drool/internal/ids"
	"drool/internal/model"
	"drool/internal/store/db"
)

// ErrNotFound: no such baseline (or host, or setting).
var ErrNotFound = errors.New("not found")

// ErrNameTaken: another active baseline has that name.
var ErrNameTaken = errors.New("a baseline with this name already exists")

// Assignment is where a baseline applies.
type Assignment struct {
	Scope   string `json:"scope"` // global | group | host
	GroupID string `json:"group_id,omitempty"`
	HostID  string `json:"host_id,omitempty"`
	Name    string `json:"name,omitempty"` // the group's or host's name (display)
}

func (a Assignment) key() string { return a.Scope + ":" + a.GroupID + a.HostID }

// BaselineInput creates or changes a baseline.
type BaselineInput struct {
	Name        string       `json:"name"`
	Description string       `json:"description"`
	Resources   []Resource   `json:"resources"`
	Assignments []Assignment `json:"assignments"`
}

// Person names a user.
type Person struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// AutoView is a baseline's automatic restore.
type AutoView struct {
	Enabled  bool            `json:"enabled"`
	Strategy *model.Strategy `json:"strategy,omitempty"`
	Owner    *Person         `json:"owner,omitempty"`
	// Attention: why automatic restore is paused ("" when it isn't).
	Attention   string `json:"attention,omitempty"`
	AttentionAt *int64 `json:"attention_at,omitempty"`
}

// RevisionInfo summarizes one revision.
type RevisionInfo struct {
	Revision  int    `json:"revision"`
	CreatedBy string `json:"created_by,omitempty"`
	CreatedAt int64  `json:"created_at"`
	Resources int    `json:"resources"`
	Hash      string `json:"hash"`
}

// BaselineCounts are the compliance of what a baseline decides.
type BaselineCounts struct {
	Hosts       int `json:"hosts"` // hosts it applies to
	Resources   int `json:"resources"`
	Compliant   int `json:"compliant"`
	Drift       int `json:"drift"`
	Conflict    int `json:"conflict"`
	Unknown     int `json:"unknown"`
	Reconciling int `json:"reconciling"`
	Failed      int `json:"failed"`
	AutoPaused  int `json:"auto_paused"`
}

// BaselineView is a baseline as shown.
type BaselineView struct {
	ID          string         `json:"id"`
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Revision    int            `json:"revision"`
	Resources   []Resource     `json:"resources"`
	Assignments []Assignment   `json:"assignments"`
	Auto        AutoView       `json:"auto"`
	Archived    bool           `json:"archived"`
	Counts      BaselineCounts `json:"counts"`
	Revisions   []RevisionInfo `json:"revisions,omitempty"`
	CreatedAt   int64          `json:"created_at"`
	UpdatedAt   int64          `json:"updated_at"`
	UpdatedBy   string         `json:"updated_by,omitempty"`
}

// normalizeInput validates a baseline's input.
func (s *Service) normalizeInput(ctx context.Context, q queryer, in *BaselineInput) error {
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || len([]rune(in.Name)) > 100 {
		return &ValidationError{Msg: "a baseline needs a name of at most 100 characters"}
	}
	in.Description = strings.TrimSpace(in.Description)
	if len(in.Description) > 2000 {
		return &ValidationError{Msg: "the description is too long"}
	}
	rs, err := NormalizeResources(in.Resources)
	if err != nil {
		return &ValidationError{Msg: err.Error()}
	}
	in.Resources = rs
	for _, r := range rs {
		if r.Kind != KindFile {
			continue
		}
		if s.d.Artifacts == nil {
			return &ValidationError{Msg: "artifact storage is not configured, so files can't be managed"}
		}
		if _, err := s.d.Artifacts.Get(ctx, r.Artifact); err != nil {
			return &ValidationError{Msg: fmt.Sprintf("%s: the content (sha256 %s) is not stored on the controller; upload the file first", r.Path, short(r.Artifact))}
		}
	}
	seen := map[string]bool{}
	var out []Assignment
	for _, a := range in.Assignments {
		a.Name = ""
		switch a.Scope {
		case "global":
			a.GroupID, a.HostID = "", ""
		case "group":
			a.HostID = ""
			if err := q.QueryRowContext(ctx, `SELECT name FROM groups WHERE id = ?`, a.GroupID).Scan(&a.Name); err != nil {
				return &ValidationError{Msg: "group " + a.GroupID + " not found"}
			}
		case "host":
			a.GroupID = ""
			var jump int
			if err := q.QueryRowContext(ctx, `SELECT name, jump_only FROM hosts WHERE id = ?`, a.HostID).Scan(&a.Name, &jump); err != nil {
				return &ValidationError{Msg: "host " + a.HostID + " not found"}
			}
			if jump != 0 {
				return &ValidationError{Msg: a.Name + " is a jump host only and isn't managed"}
			}
		default:
			return &ValidationError{Msg: `each assignment's scope must be "global", "group" or "host"`}
		}
		if seen[a.key()] {
			continue
		}
		seen[a.key()] = true
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return assignOrder(out[i]) < assignOrder(out[j]) })
	in.Assignments = out
	return nil
}

func assignOrder(a Assignment) string {
	return map[string]string{"global": "0", "group": "1", "host": "2"}[a.Scope] + strings.ToLower(a.Name) + a.GroupID + a.HostID
}

func (s *Service) auditTx(ctx context.Context, tx *sql.Tx, action, ttype, tid string, details map[string]any) error {
	if s.d.Audit == nil {
		return nil
	}
	emit, err := s.d.Audit.RecordTx(ctx, db.New(tx), audit.Event{Action: action, TargetType: ttype, TargetID: tid, Details: details})
	if err != nil {
		return err
	}
	pendingEmits.add(tx, emit)
	return nil
}

// CreateBaseline saves a new baseline as revision 1.
func (s *Service) CreateBaseline(ctx context.Context, in BaselineInput, by Actor) (*BaselineView, error) {
	id := ids.New(ids.Baseline)
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		if err := s.normalizeInput(ctx, tx, &in); err != nil {
			return err
		}
		var n int
		tx.QueryRowContext(ctx, `SELECT count(*) FROM ds_baselines WHERE name = ? COLLATE NOCASE AND archived_at IS NULL`, in.Name).Scan(&n)
		if n > 0 {
			return ErrNameTaken
		}
		now := s.nowMs()
		if _, err := tx.ExecContext(ctx, `INSERT INTO ds_baselines (id, name, description, revision, created_by, created_at, updated_by, updated_at)
			VALUES (?, ?, ?, 1, ?, ?, ?, ?)`, id, in.Name, in.Description, nullStr(by.UserID), now, nullStr(by.UserID), now); err != nil {
			return err
		}
		if err := insertRevision(ctx, tx, id, 1, in, by, now); err != nil {
			return err
		}
		if err := replaceAssignments(ctx, tx, id, in.Assignments); err != nil {
			return err
		}
		return s.auditTx(ctx, tx, "desired.baseline.create", "baseline", id, map[string]any{"name": in.Name, "revision": 1,
			"resources": resourceKeys(in.Resources), "assignments": assignmentText(in.Assignments)})
	})
	if err != nil {
		return nil, err
	}
	s.afterChange(ctx)
	return s.Baseline(ctx, id)
}

func insertRevision(ctx context.Context, tx *sql.Tx, id string, rev int, in BaselineInput, by Actor, now int64) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO ds_baseline_revisions (baseline_id, revision, name, description, resources_json, assignments_json,
		content_hash, created_by, created_by_name, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, rev, in.Name, in.Description, mustJSON(nonNilResources(in.Resources)), mustJSON(nonNilAssignments(in.Assignments)),
		HashResources(in.Resources), nullStr(by.UserID), by.Name, now)
	return err
}

func replaceAssignments(ctx context.Context, tx *sql.Tx, id string, as []Assignment) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM ds_assignments WHERE baseline_id = ?`, id); err != nil {
		return err
	}
	for _, a := range as {
		if _, err := tx.ExecContext(ctx, `INSERT INTO ds_assignments (baseline_id, scope, group_id, host_id) VALUES (?, ?, ?, ?)`,
			id, a.Scope, nullStr(a.GroupID), nullStr(a.HostID)); err != nil {
			return err
		}
	}
	return nil
}

func nonNilResources(rs []Resource) []Resource {
	if rs == nil {
		return []Resource{}
	}
	return rs
}

func nonNilAssignments(as []Assignment) []Assignment {
	if as == nil {
		return []Assignment{}
	}
	return as
}

func resourceKeys(rs []Resource) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = r.Key()
	}
	return out
}

func assignmentText(as []Assignment) []string {
	out := make([]string, len(as))
	for i, a := range as {
		switch a.Scope {
		case "global":
			out[i] = "everywhere"
		case "group":
			out[i] = "pack " + a.Name
		default:
			out[i] = "host " + a.Name
		}
	}
	return out
}

// UpdateBaseline saves a changed baseline as a new revision (only when
// something changed). Earlier revisions are never modified.
func (s *Service) UpdateBaseline(ctx context.Context, id string, in BaselineInput, by Actor) (*BaselineView, bool, error) {
	changed := false
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		cur, err := currentRevision(ctx, tx, id)
		if err != nil {
			return err
		}
		if cur.archived {
			return &ValidationError{Msg: "the baseline is archived"}
		}
		if err := s.normalizeInput(ctx, tx, &in); err != nil {
			return err
		}
		var n int
		tx.QueryRowContext(ctx, `SELECT count(*) FROM ds_baselines WHERE name = ? COLLATE NOCASE AND archived_at IS NULL AND id <> ?`, in.Name, id).Scan(&n)
		if n > 0 {
			return ErrNameTaken
		}
		if cur.Name == in.Name && cur.Description == in.Description && HashResources(cur.Resources) == HashResources(in.Resources) &&
			mustJSON(stripNames(cur.Assignments)) == mustJSON(stripNames(in.Assignments)) {
			return nil
		}
		changed = true
		rev := cur.Revision + 1
		now := s.nowMs()
		if _, err := tx.ExecContext(ctx, `UPDATE ds_baselines SET name = ?, description = ?, revision = ?, updated_by = ?, updated_at = ? WHERE id = ?`,
			in.Name, in.Description, rev, nullStr(by.UserID), now, id); err != nil {
			return err
		}
		if err := insertRevision(ctx, tx, id, rev, in, by, now); err != nil {
			return err
		}
		if err := replaceAssignments(ctx, tx, id, in.Assignments); err != nil {
			return err
		}
		ch := diffResources(cur.Resources, in.Resources)
		return s.auditTx(ctx, tx, "desired.baseline.update", "baseline", id, map[string]any{"name": in.Name, "revision": rev,
			"previous_revision": cur.Revision, "added": ch.added, "removed": ch.removed, "changed": ch.changed,
			"assignments": assignmentText(in.Assignments), "previous_assignments": assignmentText(cur.Assignments)})
	})
	if err != nil {
		return nil, false, err
	}
	if changed {
		s.afterChange(ctx)
	}
	v, err := s.Baseline(ctx, id)
	return v, changed, err
}

func stripNames(as []Assignment) []Assignment {
	out := make([]Assignment, len(as))
	for i, a := range as {
		a.Name = ""
		out[i] = a
	}
	sort.Slice(out, func(i, j int) bool { return out[i].key() < out[j].key() })
	return out
}

type resourceChanges struct {
	added, removed, changed []string
}

func diffResources(old, new []Resource) resourceChanges {
	om := map[string]Resource{}
	for _, r := range old {
		om[r.Key()] = r
	}
	var c resourceChanges
	nm := map[string]bool{}
	for _, r := range new {
		nm[r.Key()] = true
		o, ok := om[r.Key()]
		switch {
		case !ok:
			c.added = append(c.added, r.Key())
		case !o.Same(r):
			c.changed = append(c.changed, r.Key())
		}
	}
	for _, r := range old {
		if !nm[r.Key()] {
			c.removed = append(c.removed, r.Key())
		}
	}
	return c
}

// ArchiveBaseline stops applying a baseline; its revisions and history
// are kept.
func (s *Service) ArchiveBaseline(ctx context.Context, id string) error {
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		var name string
		var archived sql.NullInt64
		if err := tx.QueryRowContext(ctx, `SELECT name, archived_at FROM ds_baselines WHERE id = ?`, id).Scan(&name, &archived); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		if archived.Valid {
			return nil
		}
		if _, err := tx.ExecContext(ctx, `UPDATE ds_baselines SET archived_at = ?, auto_enabled = 0, updated_at = ? WHERE id = ?`, s.nowMs(), s.nowMs(), id); err != nil {
			return err
		}
		return s.auditTx(ctx, tx, "desired.baseline.archive", "baseline", id, map[string]any{"name": name})
	})
	if err == nil {
		s.afterChange(ctx)
	}
	return err
}

type curRev struct {
	BaselineInput
	Revision int
	archived bool
}

func currentRevision(ctx context.Context, q queryer, id string) (*curRev, error) {
	var c curRev
	var rj, aj string
	var archived sql.NullInt64
	err := q.QueryRowContext(ctx, `SELECT b.revision, b.archived_at, r.name, r.description, r.resources_json, r.assignments_json FROM ds_baselines b
		JOIN ds_baseline_revisions r ON r.baseline_id = b.id AND r.revision = b.revision WHERE b.id = ?`, id).
		Scan(&c.Revision, &archived, &c.Name, &c.Description, &rj, &aj)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	c.archived = archived.Valid
	if err := json.Unmarshal([]byte(rj), &c.Resources); err != nil {
		return nil, err
	}
	// Current assignments (groups and hosts may have been deleted since).
	rows, err := q.QueryContext(ctx, `SELECT a.scope, COALESCE(a.group_id, ''), COALESCE(a.host_id, ''), COALESCE(g.name, h.name, '')
		FROM ds_assignments a LEFT JOIN groups g ON g.id = a.group_id LEFT JOIN hosts h ON h.id = a.host_id WHERE a.baseline_id = ?`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var a Assignment
		if err := rows.Scan(&a.Scope, &a.GroupID, &a.HostID, &a.Name); err != nil {
			return nil, err
		}
		c.Assignments = append(c.Assignments, a)
	}
	sort.Slice(c.Assignments, func(i, j int) bool { return assignOrder(c.Assignments[i]) < assignOrder(c.Assignments[j]) })
	return &c, rows.Err()
}

// Baseline returns a baseline with its compliance and revisions.
func (s *Service) Baseline(ctx context.Context, id string) (*BaselineView, error) {
	vs, err := s.baselines(ctx, id, true)
	if err != nil {
		return nil, err
	}
	if len(vs) == 0 {
		return nil, ErrNotFound
	}
	v := vs[0]
	rows, err := s.d.Store.Reader.QueryContext(ctx, `SELECT revision, created_by_name, created_at, resources_json, content_hash
		FROM ds_baseline_revisions WHERE baseline_id = ? ORDER BY revision DESC LIMIT 200`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var ri RevisionInfo
		var rj string
		if err := rows.Scan(&ri.Revision, &ri.CreatedBy, &ri.CreatedAt, &rj, &ri.Hash); err != nil {
			return nil, err
		}
		var rs []Resource
		_ = json.Unmarshal([]byte(rj), &rs)
		ri.Resources = len(rs)
		v.Revisions = append(v.Revisions, ri)
	}
	return v, rows.Err()
}

// Baselines lists baselines (archived ones only when asked).
func (s *Service) Baselines(ctx context.Context, archived bool) ([]*BaselineView, error) {
	vs, err := s.baselines(ctx, "", archived)
	if err != nil {
		return nil, err
	}
	if !archived {
		out := vs[:0]
		for _, v := range vs {
			if !v.Archived {
				out = append(out, v)
			}
		}
		vs = out
	}
	return vs, nil
}

func (s *Service) baselines(ctx context.Context, id string, archived bool) ([]*BaselineView, error) {
	where := "1 = 1"
	args := []any{}
	if id != "" {
		where, args = "b.id = ?", append(args, id)
	} else if !archived {
		where = "b.archived_at IS NULL"
	}
	rows, err := s.d.Store.Reader.QueryContext(ctx, `SELECT b.id, b.name, b.description, b.revision, b.auto_enabled, b.auto_strategy_json,
		COALESCE(b.owner_id, ''), COALESCE(o.username, ''), b.attention, b.attention_at, b.archived_at, b.created_at, b.updated_at,
		COALESCE(u.username, ''), r.resources_json
		FROM ds_baselines b JOIN ds_baseline_revisions r ON r.baseline_id = b.id AND r.revision = b.revision
		LEFT JOIN users o ON o.id = b.owner_id LEFT JOIN users u ON u.id = b.updated_by
		WHERE `+where+` ORDER BY b.archived_at IS NOT NULL, b.name COLLATE NOCASE LIMIT 500`, args...)
	if err != nil {
		return nil, err
	}
	var out []*BaselineView
	for rows.Next() {
		v := &BaselineView{}
		var auto int
		var sj, ownerID, ownerName, rj string
		var attAt, archivedAt sql.NullInt64
		if err := rows.Scan(&v.ID, &v.Name, &v.Description, &v.Revision, &auto, &sj, &ownerID, &ownerName, &v.Auto.Attention, &attAt,
			&archivedAt, &v.CreatedAt, &v.UpdatedAt, &v.UpdatedBy, &rj); err != nil {
			rows.Close()
			return nil, err
		}
		v.Auto.Enabled, v.Archived = auto == 1, archivedAt.Valid
		if ownerID != "" {
			v.Auto.Owner = &Person{ID: ownerID, Name: ownerName}
		}
		if attAt.Valid {
			v.Auto.AttentionAt = &attAt.Int64
		}
		if sj != "" && sj != "{}" {
			var st model.Strategy
			if json.Unmarshal([]byte(sj), &st) == nil {
				v.Auto.Strategy = &st
			}
		}
		_ = json.Unmarshal([]byte(rj), &v.Resources)
		v.Resources = nonNilResources(v.Resources)
		out = append(out, v)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	eff := s.eff
	s.mu.Unlock()
	for _, v := range out {
		c, err := currentRevision(ctx, s.d.Store.Reader, v.ID)
		if err == nil {
			v.Assignments = nonNilAssignments(c.Assignments)
		} else {
			v.Assignments = []Assignment{}
		}
		// Hosts it applies to, and the status of what it decides.
		hosts := map[string]bool{}
		for hid, hs := range eff {
			for _, e := range hs.Resources {
				if hasBaseline(e.Source.Baselines, v.ID) || hasCompeting(e.Source.Competing, v.ID) {
					hosts[hid] = true
				}
			}
		}
		v.Counts.Hosts = len(hosts)
	}
	if err := s.baselineCounts(ctx, out); err != nil {
		return nil, err
	}
	return out, nil
}

func hasCompeting(ds []Declaration, id string) bool {
	for _, d := range ds {
		if d.Baseline != nil && d.Baseline.ID == id {
			return true
		}
	}
	return false
}

// baselineCounts fills the status counts of the resources each baseline
// decides (a conflict counts for every baseline in it).
func (s *Service) baselineCounts(ctx context.Context, vs []*BaselineView) error {
	if len(vs) == 0 {
		return nil
	}
	by := map[string]*BaselineView{}
	for _, v := range vs {
		by[v.ID] = v
	}
	rows, err := s.d.Store.Reader.QueryContext(ctx, `SELECT status, source_json, auto_paused FROM ds_resource_state`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var st, src, paused string
		if err := rows.Scan(&st, &src, &paused); err != nil {
			return err
		}
		var so Source
		if json.Unmarshal([]byte(src), &so) != nil {
			continue
		}
		seen := map[string]bool{}
		for _, b := range so.Baselines {
			v := by[b.ID]
			if v == nil || seen[b.ID] {
				continue
			}
			seen[b.ID] = true
			v.Counts.Resources++
			switch st {
			case StatusCompliant:
				v.Counts.Compliant++
			case StatusDrift:
				v.Counts.Drift++
			case StatusConflict:
				v.Counts.Conflict++
			case StatusUnknown:
				v.Counts.Unknown++
			case StatusReconciling:
				v.Counts.Reconciling++
			case StatusFailed:
				v.Counts.Failed++
			}
			if paused != "" {
				v.Counts.AutoPaused++
			}
		}
	}
	return rows.Err()
}

// Revision returns one revision of a baseline.
func (s *Service) Revision(ctx context.Context, id string, rev int) (*BaselineInput, *RevisionInfo, error) {
	var in BaselineInput
	var ri RevisionInfo
	var rj, aj string
	err := s.d.Store.Reader.QueryRowContext(ctx, `SELECT revision, name, description, resources_json, assignments_json, created_by_name, created_at, content_hash
		FROM ds_baseline_revisions WHERE baseline_id = ? AND revision = ?`, id, rev).Scan(&ri.Revision, &in.Name, &in.Description, &rj, &aj,
		&ri.CreatedBy, &ri.CreatedAt, &ri.Hash)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, ErrNotFound
	}
	if err != nil {
		return nil, nil, err
	}
	_ = json.Unmarshal([]byte(rj), &in.Resources)
	_ = json.Unmarshal([]byte(aj), &in.Assignments)
	in.Resources, in.Assignments = nonNilResources(in.Resources), nonNilAssignments(in.Assignments)
	ri.Resources = len(in.Resources)
	return &in, &ri, nil
}

// --- group priorities ---

// GroupPriority is a group's desired-state priority with what it assigns.
type GroupPriority struct {
	GroupID   string   `json:"group_id"`
	Name      string   `json:"name"`
	Priority  int      `json:"priority"`
	Hosts     int      `json:"hosts"`
	Baselines []string `json:"baselines"` // names of baselines assigned to it
}

// GroupPriorities lists every group.
func (s *Service) GroupPriorities(ctx context.Context) ([]GroupPriority, error) {
	rows, err := s.d.Store.Reader.QueryContext(ctx, `SELECT g.id, g.name, COALESCE(p.priority, 0),
		(SELECT count(*) FROM group_members m WHERE m.group_id = g.id),
		COALESCE((SELECT group_concat(b.name, '|') FROM ds_assignments a JOIN ds_baselines b ON b.id = a.baseline_id
			WHERE a.group_id = g.id AND b.archived_at IS NULL), '')
		FROM groups g LEFT JOIN ds_group_priority p ON p.group_id = g.id ORDER BY g.name COLLATE NOCASE`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []GroupPriority{}
	for rows.Next() {
		var g GroupPriority
		var names string
		if err := rows.Scan(&g.GroupID, &g.Name, &g.Priority, &g.Hosts, &names); err != nil {
			return nil, err
		}
		g.Baselines = []string{}
		if names != "" {
			g.Baselines = strings.Split(names, "|")
			sort.Strings(g.Baselines)
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// SetGroupPriority changes a group's desired-state priority.
func (s *Service) SetGroupPriority(ctx context.Context, groupID string, priority int) error {
	if priority < -1000 || priority > 1000 {
		return &ValidationError{Msg: "the priority must be between -1000 and 1000"}
	}
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		var name string
		if err := tx.QueryRowContext(ctx, `SELECT name FROM groups WHERE id = ?`, groupID).Scan(&name); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		var prev int
		tx.QueryRowContext(ctx, `SELECT priority FROM ds_group_priority WHERE group_id = ?`, groupID).Scan(&prev)
		if prev == priority {
			return nil
		}
		by := audit.ActorFrom(ctx)
		if _, err := tx.ExecContext(ctx, `INSERT INTO ds_group_priority (group_id, priority, updated_by, updated_at) VALUES (?, ?, ?, ?)
			ON CONFLICT(group_id) DO UPDATE SET priority = excluded.priority, updated_by = excluded.updated_by, updated_at = excluded.updated_at`,
			groupID, priority, nullStr(userID(by)), s.nowMs()); err != nil {
			return err
		}
		return s.auditTx(ctx, tx, "desired.group_priority", "group", groupID, map[string]any{"group": name, "priority": priority, "previous": prev})
	})
	if err == nil {
		s.afterChange(ctx)
	}
	return err
}

func userID(a audit.Actor) string {
	if a.Type == audit.ActorUser || a.Type == audit.ActorToken {
		return a.ID
	}
	return ""
}

// afterChange applies a configuration change at once: the new snapshot
// is loaded and every affected host evaluated (and its packages and files
// checked soon, spread over a minute).
func (s *Service) afterChange(ctx context.Context) {
	if err := s.reload(ctx, true); err != nil {
		s.log.Error("desired state: reload after change", "err", err)
	}
	s.poke()
}

// inTx runs fn in a write transaction and mirrors its audit events to the
// log stream after it commits.
func (s *Service) inTx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	var txp *sql.Tx
	err := s.d.Store.InRawTx(ctx, func(tx *sql.Tx) error {
		txp = tx
		return fn(tx)
	})
	emits := pendingEmits.take(txp)
	if err == nil {
		for _, e := range emits {
			e()
		}
	}
	return err
}
