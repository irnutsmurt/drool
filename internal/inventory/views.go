package inventory

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"strings"
	"time"

	"drool/internal/ids"
	"drool/internal/store"
	"drool/internal/store/db"
)

// HostView is a host with its relations, for API responses.
type HostView struct {
	db.Host
	ViaHostID string
	Tags      []string
	GroupIDs  []string
	PinnedKey *db.SshHostKey
	Facts     *db.HostFact
	Health    *db.HostStatus
}

// HostFilter narrows ListHosts. Empty fields match everything.
type HostFilter struct {
	Query   string // substring of name, address or description
	Tag     string
	GroupID string
	Status  string
}

// ListHosts returns hosts with relations, loading each relation table once.
func (s *Service) ListHosts(ctx context.Context, f HostFilter) ([]HostView, error) {
	q := s.st.R
	hosts, err := q.ListHosts(ctx)
	if err != nil {
		return nil, err
	}
	tags, err := q.ListHostTags(ctx)
	if err != nil {
		return nil, err
	}
	members, err := q.ListGroupMembers(ctx)
	if err != nil {
		return nil, err
	}
	hops, err := q.ListHostHops(ctx)
	if err != nil {
		return nil, err
	}
	pinned, err := q.ListPinnedHostKeys(ctx)
	if err != nil {
		return nil, err
	}
	facts, err := q.ListHostFacts(ctx)
	if err != nil {
		return nil, err
	}
	health, err := q.ListHostStatus(ctx)
	if err != nil {
		return nil, err
	}
	byID := map[string]*HostView{}
	views := make([]HostView, len(hosts))
	for i, h := range hosts {
		views[i] = HostView{Host: h, Tags: []string{}, GroupIDs: []string{}}
		byID[h.ID] = &views[i]
	}
	for _, t := range tags {
		if v := byID[t.HostID]; v != nil {
			v.Tags = append(v.Tags, t.Tag)
		}
	}
	for _, m := range members {
		if v := byID[m.HostID]; v != nil {
			v.GroupIDs = append(v.GroupIDs, m.GroupID)
		}
	}
	for _, h := range hops {
		if v := byID[h.HostID]; v != nil && h.HopIndex == 0 {
			v.ViaHostID = h.ViaHostID
		}
	}
	for i := range pinned {
		if v := byID[pinned[i].HostID]; v != nil {
			v.PinnedKey = &pinned[i]
		}
	}
	for i := range facts {
		if v := byID[facts[i].HostID]; v != nil {
			v.Facts = &facts[i]
		}
	}
	for i := range health {
		if v := byID[health[i].HostID]; v != nil {
			v.Health = &health[i]
		}
	}
	out := views[:0]
	query := strings.ToLower(strings.TrimSpace(f.Query))
	for _, v := range views {
		if query != "" && !strings.Contains(strings.ToLower(v.Name+" "+v.ConnectAddress+" "+v.Description), query) {
			continue
		}
		if f.Tag != "" && !slices.Contains(v.Tags, f.Tag) {
			continue
		}
		if f.GroupID != "" && !slices.Contains(v.GroupIDs, f.GroupID) {
			continue
		}
		if f.Status != "" && v.Status != f.Status {
			continue
		}
		out = append(out, v)
	}
	return out, nil
}

// GetHost returns one host with relations.
func (s *Service) GetHost(ctx context.Context, id string) (*HostView, error) {
	return s.hostView(ctx, s.st.R, id)
}

func (s *Service) hostView(ctx context.Context, q *db.Queries, id string) (*HostView, error) {
	h, err := q.GetHost(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrHostNotFound
	} else if err != nil {
		return nil, err
	}
	v := &HostView{Host: h}
	if v.Tags, err = q.ListTagsForHost(ctx, id); err != nil {
		return nil, err
	}
	if v.GroupIDs, err = q.ListGroupsForHost(ctx, id); err != nil {
		return nil, err
	}
	if via, err := q.GetHostHop(ctx, id); err == nil {
		v.ViaHostID = via
	} else if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if k, err := q.GetPinnedHostKey(ctx, id); err == nil {
		v.PinnedKey = &k
	} else if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if f, err := q.GetHostFacts(ctx, id); err == nil {
		v.Facts = &f
	} else if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if h, err := q.GetHostStatus(ctx, id); err == nil {
		v.Health = &h
	} else if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	return v, nil
}

// HostViewTx reads a host with relations inside a transaction.
func (s *Service) HostViewTx(ctx context.Context, q *db.Queries, id string) (*HostView, error) {
	return s.hostView(ctx, q, id)
}

// InputFrom returns the editable fields of a host (for PATCH merging).
func InputFrom(v *HostView) HostInput {
	return HostInput{
		Name: v.Name, Description: v.Description, Address: v.ConnectAddress, Port: int(v.SshPort),
		User: v.SshUser, CredentialID: v.SshCredentialID.String, BecomeMethod: v.BecomeMethod,
		BecomeCredentialID: v.BecomeCredentialID.String, NeedsPTY: v.SshNeedsPty != 0,
		JumpOnly: v.JumpOnly != 0, ViaHostID: v.ViaHostID, Tags: slices.Clone(v.Tags),
	}
}

// ---- Groups ----

// GroupView is a group with its member host ids.
type GroupView struct {
	db.Group
	HostIDs []string
}

var groupNameRE = hostNameRE

// ListGroups returns all groups with members.
func (s *Service) ListGroups(ctx context.Context) ([]GroupView, error) {
	q := s.st.R
	groups, err := q.ListGroups(ctx)
	if err != nil {
		return nil, err
	}
	members, err := q.ListGroupMembers(ctx)
	if err != nil {
		return nil, err
	}
	byID := map[string][]string{}
	for _, m := range members {
		byID[m.GroupID] = append(byID[m.GroupID], m.HostID)
	}
	out := make([]GroupView, len(groups))
	for i, g := range groups {
		out[i] = GroupView{Group: g, HostIDs: byID[g.ID]}
		if out[i].HostIDs == nil {
			out[i].HostIDs = []string{}
		}
	}
	return out, nil
}

// GroupTx returns one group with members.
func (s *Service) GroupTx(ctx context.Context, q *db.Queries, id string) (*GroupView, error) {
	g, err := q.GetGroup(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrGroupNotFound
	} else if err != nil {
		return nil, err
	}
	m, err := q.ListMembersOfGroup(ctx, id)
	if err != nil {
		return nil, err
	}
	if m == nil {
		m = []string{}
	}
	return &GroupView{Group: g, HostIDs: m}, nil
}

// GetGroup returns one group with members.
func (s *Service) GetGroup(ctx context.Context, id string) (*GroupView, error) {
	return s.GroupTx(ctx, s.st.R, id)
}

func validateGroup(name string) (string, error) {
	name = strings.TrimSpace(name)
	if !groupNameRE.MatchString(name) {
		return "", invalid("group name must start with a letter or digit and contain only letters, digits, '.', '_' or '-'")
	}
	return name, nil
}

// CreateGroupTx adds a group.
func (s *Service) CreateGroupTx(ctx context.Context, q *db.Queries, name, description string, hostIDs []string) (*GroupView, error) {
	name, err := validateGroup(name)
	if err != nil {
		return nil, err
	}
	now := time.Now().UnixMilli()
	id := ids.New(ids.Group)
	err = q.InsertGroup(ctx, db.InsertGroupParams{ID: id, Name: name, Description: strings.TrimSpace(description), CreatedAt: now, UpdatedAt: now})
	if store.IsUniqueViolation(err) {
		return nil, ErrGroupTaken
	} else if err != nil {
		return nil, err
	}
	if hostIDs != nil {
		if err := s.SetMembersTx(ctx, q, id, hostIDs); err != nil {
			return nil, err
		}
	}
	return s.GroupTx(ctx, q, id)
}

// UpdateGroupTx renames or re-describes a group.
func (s *Service) UpdateGroupTx(ctx context.Context, q *db.Queries, id string, name, description *string) (*GroupView, error) {
	g, err := q.GetGroup(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrGroupNotFound
	} else if err != nil {
		return nil, err
	}
	if name != nil {
		if g.Name, err = validateGroup(*name); err != nil {
			return nil, err
		}
	}
	if description != nil {
		g.Description = strings.TrimSpace(*description)
	}
	err = q.UpdateGroup(ctx, db.UpdateGroupParams{Name: g.Name, Description: g.Description, UpdatedAt: time.Now().UnixMilli(), ID: id})
	if store.IsUniqueViolation(err) {
		return nil, ErrGroupTaken
	} else if err != nil {
		return nil, err
	}
	return s.GroupTx(ctx, q, id)
}

// SetMembersTx replaces a group's members.
func (s *Service) SetMembersTx(ctx context.Context, q *db.Queries, id string, hostIDs []string) error {
	if _, err := q.GetGroup(ctx, id); errors.Is(err, sql.ErrNoRows) {
		return ErrGroupNotFound
	} else if err != nil {
		return err
	}
	if err := q.DeleteGroupMembers(ctx, id); err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, h := range hostIDs {
		if seen[h] {
			continue
		}
		seen[h] = true
		if _, err := q.GetHost(ctx, h); errors.Is(err, sql.ErrNoRows) {
			return invalid("host %s not found", h)
		} else if err != nil {
			return err
		}
		if err := q.InsertGroupMember(ctx, db.InsertGroupMemberParams{GroupID: id, HostID: h}); err != nil {
			return err
		}
	}
	return nil
}

// DeleteGroupTx removes a group (hosts are unaffected).
func (s *Service) DeleteGroupTx(ctx context.Context, q *db.Queries, id string) (db.Group, error) {
	g, err := q.GetGroup(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return db.Group{}, ErrGroupNotFound
	} else if err != nil {
		return db.Group{}, err
	}
	return g, q.DeleteGroup(ctx, id)
}
