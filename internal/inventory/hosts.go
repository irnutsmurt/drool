// Package inventory manages hosts, groups and tags, and resolves the
// connection details needed to reach a host.
package inventory

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"regexp"
	"slices"
	"strings"
	"time"

	"drool/internal/ids"
	"drool/internal/secrets"
	"drool/internal/sshx"
	"drool/internal/store"
	"drool/internal/store/db"
)

// Host statuses (hosts.status).
const (
	StatusPendingHostKey = "pending_hostkey"
	StatusUntested       = "untested"
	StatusReady          = "ready"
	StatusUnreachable    = "unreachable"
	StatusHostKeyChanged = "hostkey_mismatch"
	StatusAuthFailed     = "auth_failed"
	StatusUnsupported    = "unsupported"
)

var (
	ErrHostNotFound  = errors.New("host not found")
	ErrGroupNotFound = errors.New("group not found")
	ErrHostNameTaken = errors.New("a host with this name already exists")
	ErrGroupTaken    = errors.New("a group with this name already exists")
)

// ValidationError is a user-correctable input problem.
type ValidationError struct{ Msg string }

func (e *ValidationError) Error() string { return e.Msg }

func invalid(format string, args ...any) error {
	return &ValidationError{Msg: fmt.Sprintf(format, args...)}
}

// ConflictError is a state conflict (e.g. deleting a host others depend on).
type ConflictError struct{ Msg string }

func (e *ConflictError) Error() string { return e.Msg }

// Service manages inventory.
type Service struct {
	st      *store.Store
	secrets *secrets.Service
	dialer  *sshx.Dialer
	pool    *sshx.Pool
	now     func() time.Time
}

func NewService(st *store.Store, sec *secrets.Service, dialer *sshx.Dialer, pool *sshx.Pool) *Service {
	return &Service{st: st, secrets: sec, dialer: dialer, pool: pool, now: time.Now}
}

// HostInput is the editable part of a host.
type HostInput struct {
	Name               string
	Description        string
	Address            string
	Port               int
	User               string
	CredentialID       string
	BecomeMethod       string
	BecomeCredentialID string // "" = passwordless sudo
	NeedsPTY           bool
	JumpOnly           bool
	ViaHostID          string // "" = direct
	Tags               []string
	GroupIDs           []string
}

var (
	hostNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,62}$`)
	sshUserRE  = regexp.MustCompile(`^[A-Za-z0-9._][A-Za-z0-9._-]{0,31}$`)
	tagRE      = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}(:[A-Za-z0-9_.:/-]{1,128})?$`)
	addressRE  = regexp.MustCompile(`^[A-Za-z0-9._:%-]{1,253}$`)
)

const maxTags = 32

func normalizeTags(tags []string) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	for _, t := range tags {
		t = strings.TrimSpace(t)
		if t == "" || seen[t] {
			continue
		}
		if !tagRE.MatchString(t) {
			return nil, invalid("invalid tag %q: use key or key:value with letters, digits, '.', '_', '-'", t)
		}
		seen[t] = true
		out = append(out, t)
	}
	if len(out) > maxTags {
		return nil, invalid("at most %d tags per host", maxTags)
	}
	slices.Sort(out)
	return out, nil
}

// validate checks input against the database. hostID is "" for new hosts.
func (s *Service) validate(ctx context.Context, q *db.Queries, hostID string, in *HostInput) error {
	in.Name = strings.TrimSpace(in.Name)
	in.Address = strings.TrimSpace(in.Address)
	in.User = strings.TrimSpace(in.User)
	in.Description = strings.TrimSpace(in.Description)
	if !hostNameRE.MatchString(in.Name) {
		return invalid("name must start with a letter or digit and contain only letters, digits, '.', '_' or '-' (max 63)")
	}
	if !addressRE.MatchString(in.Address) {
		return invalid("address must be a hostname or IP address")
	}
	if in.Port == 0 {
		in.Port = 22
	}
	if in.Port < 1 || in.Port > 65535 {
		return invalid("port must be between 1 and 65535")
	}
	if !sshUserRE.MatchString(in.User) {
		return invalid("SSH user is not a valid Unix user name")
	}
	if in.BecomeMethod == "" {
		in.BecomeMethod = "sudo"
		if in.User == "root" {
			in.BecomeMethod = "none"
		}
	}
	if in.BecomeMethod != "sudo" && in.BecomeMethod != "none" {
		return invalid("become_method must be sudo or none")
	}
	if in.CredentialID == "" {
		return invalid("a credential is required")
	}
	cred, err := q.GetSecret(ctx, in.CredentialID)
	if errors.Is(err, sql.ErrNoRows) {
		return invalid("credential not found")
	} else if err != nil {
		return err
	}
	if cred.Kind != secrets.KindSSHKey && cred.Kind != secrets.KindPassword {
		return invalid("the SSH credential must be an SSH key or a password")
	}
	if in.BecomeCredentialID != "" {
		if in.BecomeMethod != "sudo" {
			return invalid("a sudo password only applies when become_method is sudo")
		}
		bc, err := q.GetSecret(ctx, in.BecomeCredentialID)
		if errors.Is(err, sql.ErrNoRows) {
			return invalid("sudo credential not found")
		} else if err != nil {
			return err
		}
		if bc.Kind != secrets.KindSudoPassword && bc.Kind != secrets.KindPassword {
			return invalid("the sudo credential must be a sudo password or password")
		}
	}
	if in.ViaHostID != "" {
		if in.ViaHostID == hostID {
			return invalid("a host cannot be its own jump host")
		}
		via, err := q.GetHost(ctx, in.ViaHostID)
		if errors.Is(err, sql.ErrNoRows) {
			return invalid("jump host not found")
		} else if err != nil {
			return err
		}
		// Alpha: one hop. The jump host itself must be reached directly.
		if _, err := q.GetHostHop(ctx, via.ID); err == nil {
			return invalid("jump host %s itself uses a jump host; only one hop is supported", via.Name)
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if hostID != "" {
			deps, err := q.ListHostsViaHop(ctx, hostID)
			if err != nil {
				return err
			}
			if len(deps) > 0 {
				return invalid("this host is a jump host for others, so it cannot use a jump host itself")
			}
		}
	}
	tags, err := normalizeTags(in.Tags)
	if err != nil {
		return err
	}
	in.Tags = tags
	for _, g := range in.GroupIDs {
		if _, err := q.GetGroup(ctx, g); errors.Is(err, sql.ErrNoRows) {
			return invalid("group %s not found", g)
		} else if err != nil {
			return err
		}
	}
	return nil
}

func nullStr(s string) sql.NullString { return sql.NullString{String: s, Valid: s != ""} }

func boolInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

// writeRelations replaces a host's hop, tags, groups and configured address.
func (s *Service) writeRelations(ctx context.Context, q *db.Queries, id string, in HostInput, now int64) error {
	if err := q.DeleteHostHops(ctx, id); err != nil {
		return err
	}
	if in.ViaHostID != "" {
		if err := q.InsertHostHop(ctx, db.InsertHostHopParams{HostID: id, HopIndex: 0, ViaHostID: in.ViaHostID}); err != nil {
			return err
		}
	}
	if err := q.DeleteHostTags(ctx, id); err != nil {
		return err
	}
	for _, t := range in.Tags {
		if err := q.InsertHostTag(ctx, db.InsertHostTagParams{HostID: id, Tag: t}); err != nil {
			return err
		}
	}
	if in.GroupIDs != nil {
		if err := q.DeleteHostGroupMemberships(ctx, id); err != nil {
			return err
		}
		for _, g := range in.GroupIDs {
			if err := q.InsertGroupMember(ctx, db.InsertGroupMemberParams{GroupID: g, HostID: id}); err != nil && !store.IsUniqueViolation(err) {
				return err
			}
		}
	}
	if err := q.DeleteConfiguredAddresses(ctx, id); err != nil {
		return err
	}
	if ip := net.ParseIP(in.Address); ip != nil {
		fam := int64(6)
		if ip.To4() != nil {
			fam = 4
		}
		if err := q.UpsertHostAddress(ctx, db.UpsertHostAddressParams{
			HostID: id, Address: ip.String(), Family: fam, Source: "configured", FirstSeenAt: now, LastSeenAt: now,
		}); err != nil {
			return err
		}
	}
	return nil
}

// CreateHostTx adds a host. It starts in pending_hostkey until a host key is
// approved.
func (s *Service) CreateHostTx(ctx context.Context, q *db.Queries, in HostInput) (db.Host, error) {
	if err := s.validate(ctx, q, "", &in); err != nil {
		return db.Host{}, err
	}
	now := s.now().UnixMilli()
	h := db.Host{
		ID: ids.New(ids.Host), Name: in.Name, Description: in.Description, ConnectAddress: in.Address,
		ManagementMethod: "ssh", SshPort: int64(in.Port), SshUser: in.User,
		SshCredentialID: nullStr(in.CredentialID), BecomeMethod: in.BecomeMethod,
		BecomeCredentialID: nullStr(in.BecomeCredentialID), SshNeedsPty: boolInt(in.NeedsPTY),
		JumpOnly: boolInt(in.JumpOnly), Status: StatusPendingHostKey, CreatedAt: now, UpdatedAt: now,
	}
	err := q.InsertHost(ctx, db.InsertHostParams{
		ID: h.ID, Name: h.Name, Description: h.Description, ConnectAddress: h.ConnectAddress,
		SshPort: h.SshPort, SshUser: h.SshUser, SshCredentialID: h.SshCredentialID,
		BecomeMethod: h.BecomeMethod, BecomeCredentialID: h.BecomeCredentialID,
		SshNeedsPty: h.SshNeedsPty, JumpOnly: h.JumpOnly, CreatedAt: now, UpdatedAt: now,
	})
	if store.IsUniqueViolation(err) {
		return db.Host{}, ErrHostNameTaken
	}
	if err != nil {
		return db.Host{}, err
	}
	if in.GroupIDs == nil {
		in.GroupIDs = []string{}
	}
	return h, s.writeRelations(ctx, q, h.ID, in, now)
}

// UpdateHostTx replaces a host's editable fields. A changed address or port
// requires re-approving the host key, since it may be a different machine.
func (s *Service) UpdateHostTx(ctx context.Context, q *db.Queries, id string, in HostInput) (db.Host, error) {
	cur, err := q.GetHost(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return db.Host{}, ErrHostNotFound
	} else if err != nil {
		return db.Host{}, err
	}
	if err := s.validate(ctx, q, id, &in); err != nil {
		return db.Host{}, err
	}
	now := s.now().UnixMilli()
	curVia, err := q.GetHostHop(ctx, id)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return db.Host{}, err
	}
	// Only settings that affect how DROOL connects invalidate the last
	// connection test; names, tags, groups and descriptions don't.
	connChanged := cur.SshUser != in.User || cur.SshCredentialID.String != in.CredentialID ||
		cur.BecomeMethod != in.BecomeMethod || cur.BecomeCredentialID.String != in.BecomeCredentialID ||
		cur.SshNeedsPty != boolInt(in.NeedsPTY) || cur.JumpOnly != boolInt(in.JumpOnly) || curVia != in.ViaHostID
	err = q.UpdateHost(ctx, db.UpdateHostParams{
		Name: in.Name, Description: in.Description, ConnectAddress: in.Address, SshPort: int64(in.Port),
		SshUser: in.User, SshCredentialID: nullStr(in.CredentialID), BecomeMethod: in.BecomeMethod,
		BecomeCredentialID: nullStr(in.BecomeCredentialID), SshNeedsPty: boolInt(in.NeedsPTY),
		JumpOnly: boolInt(in.JumpOnly), UpdatedAt: now, ID: id,
	})
	if store.IsUniqueViolation(err) {
		return db.Host{}, ErrHostNameTaken
	}
	if err != nil {
		return db.Host{}, err
	}
	if err := s.writeRelations(ctx, q, id, in, now); err != nil {
		return db.Host{}, err
	}
	status, detail := cur.Status, cur.StatusDetail
	if cur.ConnectAddress != in.Address || cur.SshPort != int64(in.Port) {
		// The endpoint changed: the pinned key no longer proves identity.
		if err := s.retirePinned(ctx, q, id, now); err != nil {
			return db.Host{}, err
		}
		status, detail = StatusPendingHostKey, "address changed; approve the host key again"
	} else if connChanged && status != StatusPendingHostKey && status != StatusHostKeyChanged {
		status, detail = StatusUntested, "settings changed; run a connection test"
	}
	if err := q.UpdateHostStatus(ctx, db.UpdateHostStatusParams{
		Status: status, StatusDetail: detail, LastProbeAt: cur.LastProbeAt, UpdatedAt: now, ID: id,
	}); err != nil {
		return db.Host{}, err
	}
	s.pool.Evict(id)
	return q.GetHost(ctx, id)
}

// DeleteHostTx removes a host that no other host uses as a jump host.
func (s *Service) DeleteHostTx(ctx context.Context, q *db.Queries, id string) (db.Host, error) {
	h, err := q.GetHost(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return db.Host{}, ErrHostNotFound
	} else if err != nil {
		return db.Host{}, err
	}
	deps, err := q.ListHostsViaHop(ctx, id)
	if err != nil {
		return db.Host{}, err
	}
	if len(deps) > 0 {
		names := make([]string, len(deps))
		for i, d := range deps {
			names[i] = d.Name
		}
		return db.Host{}, &ConflictError{Msg: "host is the jump host for: " + strings.Join(names, ", ")}
	}
	if err := q.DeleteHost(ctx, id); err != nil {
		return db.Host{}, err
	}
	s.pool.Evict(id)
	return h, nil
}

func nullInt(v int64) sql.NullInt64 { return sql.NullInt64{Int64: v, Valid: true} }
