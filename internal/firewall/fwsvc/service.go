// Package fwsvc is the controller side of DROOL's firewall (DESIGN §15–19,
// ARCHITECTURE-ALPHA §24): policy storage, per-host compilation, the
// dead-man transaction protocol over the agent or SSH, set-element
// propagation, drift detection, observations, simulation and Explain Host.
package fwsvc

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"drool/internal/agentsvc"
	"drool/internal/audit"
	"drool/internal/firewall/compile"
	"drool/internal/firewall/hostfw"
	"drool/internal/firewall/policy"
	"drool/internal/ids"
	"drool/internal/inventory"
	"drool/internal/model"
	"drool/internal/store"
	"drool/internal/store/db"
)

// Decision is the management router's view of a host right now.
type Decision struct {
	UseAgent   bool
	SSHAllowed bool
	Blocked    bool
	Reason     string
}

// Deps are the service's collaborators.
type Deps struct {
	Store  *store.Store
	Inv    *inventory.Service
	Agents *agentsvc.Service // nil: agent endpoint disabled
	Audit  *audit.Writer
	Log    *slog.Logger
	// Decide is the management router's decision (mgmt.Router.Decide).
	Decide func(ctx context.Context, h db.Host) Decision
	// ToolPaths redirects the host tooling on SSH hosts (tests).
	ToolPaths *hostfw.Paths
}

// Modes.
const (
	ModeOff     = "off"
	ModeMonitor = "monitor"
	ModeGuided  = "guided"
	ModeEnforce = "enforce"
)

// Settings keys.
const (
	settingRanges  = "firewall.internal_ranges"
	settingTimeout = "firewall.default_timeout_s"
)

// DefaultTimeoutS is the dead-man rollback timeout (DESIGN §17.4).
const DefaultTimeoutS = 120

// Service is the controller's firewall.
type Service struct {
	d     Deps
	log   *slog.Logger
	now   func() time.Time
	epoch string

	mu      sync.Mutex
	locks   map[string]*sync.Mutex // per-host firewall domain lock
	pending map[string]time.Time   // in-flight transactions: host → deadline
	kick    chan struct{}

	// testCommitDelay (DROOL_TEST_FW_DELAY_COMMIT) holds a transaction
	// between apply and verification, so tests can kill the controller,
	// the agent or the host inside the window.
	testCommitDelay time.Duration
	// testSkipCommit (DROOL_TEST_FW_SKIP_COMMIT=1) never commits: the host
	// must roll back on its own, as if the controller vanished.
	testSkipCommit bool
	// testControlFile (DROOL_TEST_FW_CONTROL) sets both per host.
	testControlFile string
}

// New creates the service.
func New(ctx context.Context, d Deps) (*Service, error) {
	if d.Log == nil {
		d.Log = slog.Default()
	}
	epoch, err := agentsvc.ControllerEpoch(ctx, d.Store)
	if err != nil {
		return nil, err
	}
	s := &Service{d: d, log: d.Log, now: time.Now, epoch: epoch, locks: map[string]*sync.Mutex{},
		pending: map[string]time.Time{}, kick: make(chan struct{}, 1)}
	if v := os.Getenv("DROOL_TEST_FW_DELAY_COMMIT"); v != "" {
		n, _ := strconv.Atoi(v)
		s.testCommitDelay = time.Duration(n) * time.Second
		s.log.Warn("TEST MODE: firewall transactions wait before verification", "seconds", n)
	}
	if os.Getenv("DROOL_TEST_FW_SKIP_COMMIT") == "1" {
		s.testSkipCommit = true
		s.log.Warn("TEST MODE: firewall transactions are never committed")
	}
	if f := os.Getenv("DROOL_TEST_FW_CONTROL"); f != "" {
		s.testControlFile = f
		s.log.Warn("TEST MODE: per-host firewall test control file", "file", f)
	}
	return s, nil
}

// testControl reads the integration tests' per-host control file: lines
// "<host name> delay=<seconds>" or "<host name> skip" (never commit).
func (s *Service) testControl(hostName string) (time.Duration, bool) {
	if s.testControlFile == "" {
		return s.testCommitDelay, s.testSkipCommit
	}
	b, _ := os.ReadFile(s.testControlFile)
	for _, l := range strings.Split(string(b), "\n") {
		f := strings.Fields(l)
		if len(f) != 2 || f[0] != hostName {
			continue
		}
		if f[1] == "skip" {
			return 0, true
		}
		if n, err := strconv.Atoi(strings.TrimPrefix(f[1], "delay=")); err == nil {
			return time.Duration(n) * time.Second, false
		}
	}
	return s.testCommitDelay, s.testSkipCommit
}

// SetTestCommitDelay holds transactions between apply and verification
// (tests; DROOL_TEST_FW_DELAY_COMMIT in integration runs).
func (s *Service) SetTestCommitDelay(d time.Duration) { s.testCommitDelay = d }

func (s *Service) toolPaths() hostfw.Paths {
	if s.d.ToolPaths != nil {
		return *s.d.ToolPaths
	}
	return hostfw.DefaultPaths
}

func (s *Service) hostLock(hostID string) *sync.Mutex {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.locks[hostID]
	if !ok {
		m = &sync.Mutex{}
		s.locks[hostID] = m
	}
	return m
}

// Kick asks the converger to run soon (addresses, groups or policies changed).
func (s *Service) Kick() {
	select {
	case s.kick <- struct{}{}:
	default:
	}
}

// --- settings ---

// InternalRanges returns internal_ranges (DESIGN §15.3).
func (s *Service) InternalRanges(ctx context.Context) []string {
	row, err := s.d.Store.R.GetSetting(ctx, settingRanges)
	if err == nil {
		var rs []string
		if json.Unmarshal([]byte(row.ValueJson), &rs) == nil && len(rs) > 0 {
			return rs
		}
	}
	return append([]string(nil), policy.DefaultInternalRanges...)
}

// DefaultTimeout returns the default rollback timeout in seconds.
func (s *Service) DefaultTimeout(ctx context.Context) int {
	row, err := s.d.Store.R.GetSetting(ctx, settingTimeout)
	if err == nil {
		var n int
		if json.Unmarshal([]byte(row.ValueJson), &n) == nil && n > 0 {
			return n
		}
	}
	return DefaultTimeoutS
}

// Settings is the global firewall configuration.
type Settings struct {
	InternalRanges  []string `json:"internal_ranges"`
	DefaultTimeoutS int      `json:"default_timeout_s"`
}

// GetSettings returns the settings.
func (s *Service) GetSettings(ctx context.Context) Settings {
	return Settings{InternalRanges: s.InternalRanges(ctx), DefaultTimeoutS: s.DefaultTimeout(ctx)}
}

// ValidationError is a user error.
func ValidationError(f string, a ...any) error {
	return &model.OpError{Class: model.ClassValidation, Message: fmt.Sprintf(f, a...)}
}

// SetSettingsTx stores the settings.
func (s *Service) SetSettingsTx(ctx context.Context, q *db.Queries, in Settings, by string) error {
	ps, err := policy.ParseRanges(in.InternalRanges)
	if err != nil {
		return ValidationError("%v", err)
	}
	if in.DefaultTimeoutS < 30 || in.DefaultTimeoutS > 3600 {
		return ValidationError("the default rollback timeout must be between 30 and 3600 seconds")
	}
	rs := make([]string, len(ps))
	for i, p := range ps {
		rs[i] = p.String()
	}
	now := s.now().UnixMilli()
	upd := sql.NullString{String: by, Valid: by != ""}
	b, _ := json.Marshal(rs)
	if err := q.UpsertSetting(ctx, db.UpsertSettingParams{Key: settingRanges, ValueJson: string(b), UpdatedBy: upd, UpdatedAt: now}); err != nil {
		return err
	}
	b, _ = json.Marshal(in.DefaultTimeoutS)
	return q.UpsertSetting(ctx, db.UpsertSettingParams{Key: settingTimeout, ValueJson: string(b), UpdatedBy: upd, UpdatedAt: now})
}

// --- services ---

// Services returns built-in and custom services.
func (s *Service) Services(ctx context.Context) ([]policy.Service, map[string]string, error) {
	rows, err := s.d.Store.R.ListFwServices(ctx)
	if err != nil {
		return nil, nil, err
	}
	out := []policy.Service{}
	idByName := map[string]string{}
	for _, b := range policy.Builtin {
		b.Builtin = true
		out = append(out, b)
	}
	for _, r := range rows {
		var es []policy.Entry
		json.Unmarshal([]byte(r.EntriesJson), &es)
		out = append(out, policy.Service{Name: r.Name, Description: r.Description, Entries: es})
		idByName[r.Name] = r.ID
	}
	return out, idByName, nil
}

// CreateServiceTx adds a custom service.
func (s *Service) CreateServiceTx(ctx context.Context, q *db.Queries, svc policy.Service) (string, error) {
	if err := policy.ValidateService(svc); err != nil {
		return "", ValidationError("%v", err)
	}
	b, _ := json.Marshal(svc.Entries)
	id := ids.New(ids.FwService)
	now := s.now().UnixMilli()
	if err := q.InsertFwService(ctx, db.InsertFwServiceParams{ID: id, Name: svc.Name, Description: svc.Description,
		EntriesJson: string(b), CreatedAt: now, UpdatedAt: now}); err != nil {
		if store.IsUniqueViolation(err) {
			return "", ValidationError("a service named %q exists", svc.Name)
		}
		return "", err
	}
	return id, nil
}

// DeleteServiceTx removes a custom service that no policy uses.
func (s *Service) DeleteServiceTx(ctx context.Context, q *db.Queries, id string) (string, error) {
	row, err := q.GetFwService(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	} else if err != nil {
		return "", err
	}
	pols, err := q.ListFwPolicies(ctx)
	if err != nil {
		return "", err
	}
	for _, p := range pols {
		var svcs []string
		json.Unmarshal([]byte(p.ServicesJson), &svcs)
		for _, n := range svcs {
			if n == row.Name {
				return "", ValidationError("policy %q uses service %q", p.Name, row.Name)
			}
		}
	}
	return row.Name, q.DeleteFwService(ctx, id)
}

// ErrNotFound is returned for unknown ids.
var ErrNotFound = errors.New("not found")

// --- policies ---

// PolicyRow is a stored policy.
type PolicyRow struct {
	policy.Policy
	Position  int64  `json:"position"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

func toPolicy(r db.FwPolicy) PolicyRow {
	p := PolicyRow{Policy: policy.Policy{ID: r.ID, Name: r.Name, Description: r.Description, Enabled: r.Enabled != 0, Action: r.Action},
		Position: r.Position, CreatedAt: time.UnixMilli(r.CreatedAt).UTC().Format(time.RFC3339), UpdatedAt: time.UnixMilli(r.UpdatedAt).UTC().Format(time.RFC3339)}
	json.Unmarshal([]byte(r.FromJson), &p.From)
	json.Unmarshal([]byte(r.ToJson), &p.To)
	json.Unmarshal([]byte(r.ServicesJson), &p.Services)
	if p.From == nil {
		p.From = []policy.Selector{}
	}
	if p.To == nil {
		p.To = []policy.Selector{}
	}
	if p.Services == nil {
		p.Services = []string{}
	}
	return p
}

// Policies lists policies in evaluation order.
func (s *Service) Policies(ctx context.Context) ([]PolicyRow, error) {
	rows, err := s.d.Store.R.ListFwPolicies(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]PolicyRow, len(rows))
	for i, r := range rows {
		out[i] = toPolicy(r)
	}
	return out, nil
}

// Policy returns one policy.
func (s *Service) Policy(ctx context.Context, id string) (PolicyRow, error) {
	r, err := s.d.Store.R.GetFwPolicy(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return PolicyRow{}, ErrNotFound
	}
	return toPolicy(r), err
}

func (s *Service) checkRefs(ctx context.Context, q *db.Queries, p policy.Policy) error {
	svcs, _, err := s.Services(ctx)
	if err != nil {
		return err
	}
	byName := map[string]policy.Service{}
	for _, sv := range svcs {
		byName[sv.Name] = sv
	}
	if err := policy.Validate(p, byName); err != nil {
		return ValidationError("%v", err)
	}
	for _, sel := range append(append([]policy.Selector{}, p.From...), p.To...) {
		switch sel.Kind {
		case policy.SelHost:
			if _, err := q.GetHost(ctx, sel.ID); err != nil {
				return ValidationError("unknown host %s", sel.ID)
			}
		case policy.SelGroup:
			if _, err := q.GetGroup(ctx, sel.ID); err != nil {
				return ValidationError("unknown group %s", sel.ID)
			}
		}
	}
	return nil
}

// SavePolicyTx creates (id "") or updates a policy. position 0 appends.
func (s *Service) SavePolicyTx(ctx context.Context, q *db.Queries, p policy.Policy, position int64, by string) (PolicyRow, error) {
	p.Name = strings.TrimSpace(p.Name)
	if err := s.checkRefs(ctx, q, p); err != nil {
		return PolicyRow{}, err
	}
	from, _ := json.Marshal(p.From)
	to, _ := json.Marshal(p.To)
	svcs, _ := json.Marshal(p.Services)
	now := s.now().UnixMilli()
	enabled := int64(0)
	if p.Enabled {
		enabled = 1
	}
	if position <= 0 {
		maxPos, err := q.MaxFwPolicyPosition(ctx)
		if err != nil {
			return PolicyRow{}, err
		}
		if p.ID != "" {
			if cur, err := q.GetFwPolicy(ctx, p.ID); err == nil {
				position = cur.Position
			}
		}
		if position <= 0 {
			position = maxPos + 10
		}
	}
	var err error
	if p.ID == "" {
		p.ID = ids.New(ids.FwPolicy)
		err = q.InsertFwPolicy(ctx, db.InsertFwPolicyParams{ID: p.ID, Name: p.Name, Description: p.Description, Enabled: enabled,
			Action: p.Action, FromJson: string(from), ToJson: string(to), ServicesJson: string(svcs), Position: position,
			CreatedBy: sql.NullString{String: by, Valid: by != ""}, CreatedAt: now, UpdatedAt: now})
	} else {
		if _, gerr := q.GetFwPolicy(ctx, p.ID); errors.Is(gerr, sql.ErrNoRows) {
			return PolicyRow{}, ErrNotFound
		}
		err = q.UpdateFwPolicy(ctx, db.UpdateFwPolicyParams{Name: p.Name, Description: p.Description, Enabled: enabled, Action: p.Action,
			FromJson: string(from), ToJson: string(to), ServicesJson: string(svcs), Position: position, UpdatedAt: now, ID: p.ID})
	}
	if err != nil {
		if store.IsUniqueViolation(err) {
			return PolicyRow{}, ValidationError("a policy named %q exists", p.Name)
		}
		return PolicyRow{}, err
	}
	r, err := q.GetFwPolicy(ctx, p.ID)
	if err != nil {
		return PolicyRow{}, err
	}
	return toPolicy(r), nil
}

// DeletePolicyTx removes a policy.
func (s *Service) DeletePolicyTx(ctx context.Context, q *db.Queries, id string) (PolicyRow, error) {
	r, err := q.GetFwPolicy(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return PolicyRow{}, ErrNotFound
	} else if err != nil {
		return PolicyRow{}, err
	}
	return toPolicy(r), q.DeleteFwPolicy(ctx, id)
}

// --- host and group configuration ---

// HostConfig is a host's firewall settings.
type HostConfig struct {
	Mode     string `json:"mode"`
	Outbound string `json:"outbound"`
	// TimeoutS is the host's own rollback timeout (0: inherit).
	TimeoutS int `json:"rollback_timeout_s"`
}

// HostConfig returns a host's settings (defaults when unset).
func (s *Service) HostConfig(ctx context.Context, hostID string) HostConfig {
	r, err := s.d.Store.R.GetFwHostConfig(ctx, hostID)
	if err != nil {
		return HostConfig{Mode: ModeOff, Outbound: compile.OutboundAllow}
	}
	return HostConfig{Mode: r.Mode, Outbound: r.Outbound, TimeoutS: int(r.RollbackTimeoutS.Int64)}
}

// SetHostConfigTx stores a host's settings.
func (s *Service) SetHostConfigTx(ctx context.Context, q *db.Queries, hostID string, c HostConfig) error {
	switch c.Mode {
	case ModeOff, ModeMonitor, ModeGuided, ModeEnforce:
	default:
		return ValidationError("mode must be off, monitor, guided or enforce")
	}
	if c.Outbound == "" {
		c.Outbound = compile.OutboundAllow
	}
	if c.Outbound != compile.OutboundAllow && c.Outbound != compile.OutboundRestrict {
		return ValidationError("outbound must be allow or restrict")
	}
	if c.TimeoutS != 0 && (c.TimeoutS < 30 || c.TimeoutS > 3600) {
		return ValidationError("the rollback timeout must be between 30 and 3600 seconds")
	}
	if _, err := q.GetHost(ctx, hostID); errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return q.UpsertFwHostConfig(ctx, db.UpsertFwHostConfigParams{HostID: hostID, Mode: c.Mode, Outbound: c.Outbound,
		RollbackTimeoutS: sql.NullInt64{Int64: int64(c.TimeoutS), Valid: c.TimeoutS > 0}, UpdatedAt: s.now().UnixMilli()})
}

// HostConfigChanged runs after a host's settings were committed: it turns
// the agent's learning on or off. (Not from inside the transaction: the
// new mode isn't visible to other connections until it commits.)
func (s *Service) HostConfigChanged(ctx context.Context, hostID string) {
	s.pushLearning(ctx, hostID)
	s.Kick()
}

// SetGroupTimeoutTx stores a group's rollback timeout (0: inherit).
func (s *Service) SetGroupTimeoutTx(ctx context.Context, q *db.Queries, groupID string, timeoutS int) error {
	if timeoutS != 0 && (timeoutS < 30 || timeoutS > 3600) {
		return ValidationError("the rollback timeout must be between 30 and 3600 seconds")
	}
	if _, err := q.GetGroup(ctx, groupID); errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return q.UpsertFwGroupConfig(ctx, db.UpsertFwGroupConfigParams{GroupID: groupID,
		RollbackTimeoutS: sql.NullInt64{Int64: int64(timeoutS), Valid: timeoutS > 0}, UpdatedAt: s.now().UnixMilli()})
}

// GroupTimeouts returns groups' rollback timeouts.
func (s *Service) GroupTimeouts(ctx context.Context) map[string]int {
	rows, _ := s.d.Store.R.ListFwGroupConfigs(ctx)
	out := map[string]int{}
	for _, r := range rows {
		if r.RollbackTimeoutS.Valid {
			out[r.GroupID] = int(r.RollbackTimeoutS.Int64)
		}
	}
	return out
}

// EffectiveTimeout is the rollback timeout for a host and where it comes
// from: the host's own, else the smallest of its groups', else the default.
func (s *Service) EffectiveTimeout(ctx context.Context, hostID string) (int, string) {
	if c := s.HostConfig(ctx, hostID); c.TimeoutS > 0 {
		return c.TimeoutS, "host"
	}
	gts := s.GroupTimeouts(ctx)
	best, from := 0, ""
	if gids, err := s.d.Store.R.ListGroupsForHost(ctx, hostID); err == nil {
		for _, gid := range gids {
			if t, ok := gts[gid]; ok && (best == 0 || t < best) {
				name := gid
				if g, err := s.d.Store.R.GetGroup(ctx, gid); err == nil {
					name = g.Name
				}
				best, from = t, "group "+name
			}
		}
	}
	if best > 0 {
		return best, from
	}
	return s.DefaultTimeout(ctx), "default"
}

// --- world ---

// usableAddr filters addresses that can't identify a host on the network.
func usableAddr(a netip.Addr) bool {
	return a.IsValid() && !a.IsLoopback() && !a.IsLinkLocalUnicast() && !a.IsUnspecified() && !a.IsMulticast()
}

// World loads everything the compiler needs.
func (s *Service) World(ctx context.Context) (*compile.World, error) {
	q := s.d.Store.R
	hosts, err := q.ListHosts(ctx)
	if err != nil {
		return nil, err
	}
	addrs, err := q.ListAllHostAddresses(ctx)
	if err != nil {
		return nil, err
	}
	byHost := map[string][]netip.Addr{}
	for _, a := range addrs {
		ip, err := netip.ParseAddr(a.Address)
		if err != nil || !usableAddr(ip) {
			continue
		}
		byHost[a.HostID] = append(byHost[a.HostID], ip.Unmap())
	}
	w := &compile.World{Hosts: map[string]compile.HostRef{}, Groups: map[string]compile.GroupRef{}}
	for _, h := range hosts {
		w.Hosts[h.ID] = compile.HostRef{ID: h.ID, Name: h.Name, Addrs: byHost[h.ID]}
	}
	groups, err := q.ListGroups(ctx)
	if err != nil {
		return nil, err
	}
	for _, g := range groups {
		w.Groups[g.ID] = compile.GroupRef{ID: g.ID, Name: g.Name}
	}
	members, err := q.ListGroupMembers(ctx)
	if err != nil {
		return nil, err
	}
	for _, m := range members {
		g := w.Groups[m.GroupID]
		g.Members = append(g.Members, m.HostID)
		w.Groups[m.GroupID] = g
	}
	svcs, _, err := s.Services(ctx)
	if err != nil {
		return nil, err
	}
	w.Services = policy.Services(nil)
	for _, sv := range svcs {
		w.Services[sv.Name] = sv
	}
	pols, err := s.Policies(ctx)
	if err != nil {
		return nil, err
	}
	for _, p := range pols {
		w.Policies = append(w.Policies, p.Policy)
	}
	w.Internal, err = policy.ParseRanges(s.InternalRanges(ctx))
	return w, err
}
