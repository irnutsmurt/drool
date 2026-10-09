package desired

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
)

// queryer is the read side of *sql.DB and *sql.Tx.
type queryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// execer is the write side of *sql.DB and *sql.Tx.
type execer interface {
	queryer
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

func generation(ctx context.Context, q queryer) (int64, error) {
	var g int64
	err := q.QueryRowContext(ctx, `SELECT gen FROM ds_gen WHERE id = 1`).Scan(&g)
	return g, err
}

// LoadSnapshot reads the fleet's desired-state configuration.
func LoadSnapshot(ctx context.Context, q queryer) (*Snapshot, error) {
	gen, err := generation(ctx, q)
	if err != nil {
		return nil, err
	}
	s := &Snapshot{Gen: gen, Baselines: map[string]*Baseline{}, ByGroup: map[string][]string{}, ByHost: map[string][]string{},
		Overrides: map[string]map[string]Override{}, Groups: map[string]Group{}, Members: map[string][]string{}, Hosts: map[string]string{}}

	rows, err := q.QueryContext(ctx, `SELECT b.id, b.name, b.revision, b.auto_enabled, b.attention, r.resources_json
		FROM ds_baselines b JOIN ds_baseline_revisions r ON r.baseline_id = b.id AND r.revision = b.revision
		WHERE b.archived_at IS NULL`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var b Baseline
		var auto int
		var attention, rj string
		if err := rows.Scan(&b.ID, &b.Name, &b.Revision, &auto, &attention, &rj); err != nil {
			rows.Close()
			return nil, err
		}
		var rs []Resource
		if err := json.Unmarshal([]byte(rj), &rs); err != nil {
			rows.Close()
			return nil, fmt.Errorf("baseline %s revision %d: %w", b.ID, b.Revision, err)
		}
		b.Resources = make(map[string]Resource, len(rs))
		for _, r := range rs {
			b.Resources[r.Key()] = r
		}
		b.Auto = auto == 1 && attention == ""
		s.Baselines[b.ID] = &b
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	rows, err = q.QueryContext(ctx, `SELECT a.baseline_id, a.scope, COALESCE(a.group_id, ''), COALESCE(a.host_id, '')
		FROM ds_assignments a JOIN ds_baselines b ON b.id = a.baseline_id WHERE b.archived_at IS NULL
		ORDER BY a.baseline_id`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id, scope, gid, hid string
		if err := rows.Scan(&id, &scope, &gid, &hid); err != nil {
			rows.Close()
			return nil, err
		}
		switch scope {
		case "global":
			s.Global = append(s.Global, id)
		case "group":
			s.ByGroup[gid] = append(s.ByGroup[gid], id)
		case "host":
			s.ByHost[hid] = append(s.ByHost[hid], id)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	rows, err = q.QueryContext(ctx, `SELECT g.id, g.name, COALESCE(p.priority, 0) FROM groups g LEFT JOIN ds_group_priority p ON p.group_id = g.id`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id string
		var g Group
		if err := rows.Scan(&id, &g.Name, &g.Priority); err != nil {
			rows.Close()
			return nil, err
		}
		s.Groups[id] = g
	}
	rows.Close()

	rows, err = q.QueryContext(ctx, `SELECT m.group_id, m.host_id FROM group_members m ORDER BY m.host_id, m.group_id`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var gid, hid string
		if err := rows.Scan(&gid, &hid); err != nil {
			rows.Close()
			return nil, err
		}
		s.Members[hid] = append(s.Members[hid], gid)
	}
	rows.Close()

	// Jump-only hosts are never managed (they can't be targeted by jobs).
	rows, err = q.QueryContext(ctx, `SELECT id, name FROM hosts WHERE jump_only = 0`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id, name string
		if err := rows.Scan(&id, &name); err != nil {
			rows.Close()
			return nil, err
		}
		s.Hosts[id] = name
	}
	rows.Close()

	rows, err = q.QueryContext(ctx, `SELECT o.host_id, o.key, o.resource_json, o.excluded, o.origin, o.note, o.version,
		COALESCE(u.username, ''), o.updated_at FROM ds_host_overrides o LEFT JOIN users u ON u.id = o.updated_by`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var hid, key, rj string
		var excluded int
		var o Override
		if err := rows.Scan(&hid, &key, &rj, &excluded, &o.Info.Origin, &o.Info.Note, &o.Info.Version, &o.Info.UpdatedBy, &o.Info.UpdatedAt); err != nil {
			rows.Close()
			return nil, err
		}
		o.Info.Excluded = excluded == 1
		if !o.Info.Excluded {
			if err := json.Unmarshal([]byte(rj), &o.Resource); err != nil || o.Resource.Key() != key {
				continue // never trust a row that doesn't describe its own key
			}
		}
		if s.Overrides[hid] == nil {
			s.Overrides[hid] = map[string]Override{}
		}
		s.Overrides[hid][key] = o
	}
	rows.Close()
	return s, rows.Err()
}
