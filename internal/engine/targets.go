package engine

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"sort"

	"drool/internal/model"
	"drool/internal/store/db"
)

// TargetHost is one resolved target.
type TargetHost struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Status  string `json:"status"`
	Warning string `json:"warning,omitempty"`
}

// ValidationError is a user-correctable problem with a job spec.
type ValidationError struct{ Msg string }

func (e *ValidationError) Error() string { return e.Msg }

func invalid(format string, args ...any) error {
	return &ValidationError{Msg: fmt.Sprintf(format, args...)}
}

var statusWarnings = map[string]string{
	"pending_hostkey":  "host key not approved yet; this host will fail",
	"hostkey_mismatch": "host key changed; this host will fail until the key is reviewed",
	"auth_failed":      "last connection test failed authentication",
	"unreachable":      "last connection test could not reach the host",
	"unsupported":      "last connection test found an unsupported system",
	"untested":         "connection not tested yet",
}

// ResolveTargets expands a target spec to hosts, sorted by name. Jump-only
// hosts matched through groups or tags are left out; naming one explicitly
// is an error.
func ResolveTargets(ctx context.Context, q *db.Queries, t model.TargetSpec) ([]TargetHost, []string, error) {
	hosts, err := q.ListHosts(ctx)
	if err != nil {
		return nil, nil, err
	}
	byID := map[string]db.Host{}
	for _, h := range hosts {
		byID[h.ID] = h
	}
	selected := map[string]bool{}
	var warnings []string
	for _, id := range t.Hosts {
		h, ok := byID[id]
		if !ok {
			return nil, nil, invalid("host %s not found", id)
		}
		if h.JumpOnly != 0 {
			return nil, nil, invalid("%s is a jump host only and cannot be targeted", h.Name)
		}
		selected[id] = true
	}
	skippedJump := 0
	addIndirect := func(id string) {
		if h, ok := byID[id]; ok {
			if h.JumpOnly != 0 {
				skippedJump++
				return
			}
			selected[id] = true
		}
	}
	if len(t.Groups) > 0 {
		for _, g := range t.Groups {
			if _, err := q.GetGroup(ctx, g); errors.Is(err, sql.ErrNoRows) {
				return nil, nil, invalid("group %s not found", g)
			} else if err != nil {
				return nil, nil, err
			}
			members, err := q.ListMembersOfGroup(ctx, g)
			if err != nil {
				return nil, nil, err
			}
			for _, m := range members {
				addIndirect(m)
			}
		}
	}
	if len(t.Tags) > 0 {
		tags, err := q.ListHostTags(ctx)
		if err != nil {
			return nil, nil, err
		}
		for _, ht := range tags {
			if slices.Contains(t.Tags, ht.Tag) {
				addIndirect(ht.HostID)
			}
		}
	}
	for _, id := range t.ExcludeHosts {
		delete(selected, id)
	}
	if skippedJump > 0 {
		warnings = append(warnings, fmt.Sprintf("%d jump-only host(s) matched and were left out", skippedJump))
	}
	out := make([]TargetHost, 0, len(selected))
	for id := range selected {
		h := byID[id]
		out = append(out, TargetHost{ID: id, Name: h.Name, Status: h.Status, Warning: statusWarnings[h.Status]})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, warnings, nil
}
