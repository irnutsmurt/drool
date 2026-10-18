package agentsvc

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"drool/internal/agentrpc/pb"
	"drool/internal/audit"
	"drool/internal/ids"
	"drool/internal/release"
	"drool/internal/store/db"
	"drool/internal/version"
)

// Shepherd updates (DESIGN §28, ARCHITECTURE-ALPHA §23.7 and §42). A
// rollout offers one version to Shepherds wave by wave: a canary first,
// then batches, pausing at the failure limit. Each Shepherd verifies the
// signed release itself before installing it, and rolls back by itself if
// the new version doesn't become healthy. The automatic policy keeps every
// Shepherd on the newest release available to this controller in its
// channel: the official release of DROOL's own version (official.go), or a
// custom release an operator signed and uploaded.

// UpdatePolicy is the agent update setting (settings key agent_updates).
type UpdatePolicy struct {
	Policy        string `json:"policy"`  // manual | automatic
	Channel       string `json:"channel"` // automatic: newest release in this channel
	Version       string `json:"version"` // automatic: this exact version instead
	Canary        int    `json:"canary"`
	BatchSize     int    `json:"batch_size"`
	MaxFailures   int    `json:"max_failures"`
	HealthWindowS int    `json:"health_window_s"`
}

// DefaultUpdatePolicy: automatic, stable releases, a canary of one,
// batches of five, pause at the first failure, two minutes to become
// healthy. A policy someone saved is kept as it is.
func DefaultUpdatePolicy() UpdatePolicy {
	return UpdatePolicy{Policy: "automatic", Channel: version.Stable, Canary: 1, BatchSize: 5, MaxFailures: 1, HealthWindowS: 120}
}

// Channels the update policy offers (an operator's own releases may name
// others; the API takes those too).
var Channels = []string{version.Stable, version.Prerelease}

var channelRE = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)

func (p *UpdatePolicy) normalize() error {
	switch p.Policy {
	case "manual", "automatic":
	default:
		return fmt.Errorf("policy must be manual or automatic")
	}
	if p.Channel == "" {
		p.Channel = version.Stable
	}
	if !channelRE.MatchString(p.Channel) {
		return fmt.Errorf("the channel must be stable, prerelease or another lower-case name")
	}
	p.Version = strings.TrimSpace(p.Version)
	if p.Version != "" && !release.ValidVersion(p.Version) {
		return fmt.Errorf("%q isn't a version (like v1.2.3)", p.Version)
	}
	if p.Canary < 1 || p.BatchSize < 1 || p.MaxFailures < 1 || p.HealthWindowS < 30 || p.HealthWindowS > 3600 {
		return fmt.Errorf("canary, batch size and max failures must be at least 1; the health window 30–3600 s")
	}
	return nil
}

// ChannelIncludes reports whether a policy channel takes a release of
// channel rel: its own, and prerelease takes stable releases too (a release
// candidate's successor is the release).
func ChannelIncludes(policy, rel string) bool {
	return rel == policy || policy == version.Prerelease && rel == version.Stable
}

// GetUpdatePolicy reads the update setting.
func (s *Service) GetUpdatePolicy(ctx context.Context) UpdatePolicy {
	p := DefaultUpdatePolicy()
	if row, err := s.st.R.GetSetting(ctx, "agent_updates"); err == nil {
		json.Unmarshal([]byte(row.ValueJson), &p)
	}
	return p
}

// SetUpdatePolicy stores the update setting.
func (s *Service) SetUpdatePolicy(ctx context.Context, q *db.Queries, p UpdatePolicy, userID string) error {
	if err := p.normalize(); err != nil {
		return err
	}
	b, _ := json.Marshal(p)
	return q.UpsertSetting(ctx, db.UpsertSettingParams{Key: "agent_updates", ValueJson: string(b),
		UpdatedBy: sql.NullString{String: userID, Valid: userID != ""}, UpdatedAt: s.now().UnixMilli()})
}

// --- release keys and releases ---

// KnownKeys are the release keys registered on the controller. They are
// for display and early checks only: agents trust their own local list.
func (s *Service) KnownKeys(ctx context.Context) ([]release.PublicKey, error) {
	rows, err := s.st.R.ListReleaseKeys(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]release.PublicKey, 0, len(rows))
	for _, r := range rows {
		out = append(out, release.PublicKey{Key: r.PublicKey, Name: r.Name})
	}
	return out, nil
}

// uploadKeys are the keys an uploaded release may be signed with: the
// registered ones and the project's official keys.
func (s *Service) uploadKeys(ctx context.Context) ([]release.PublicKey, error) {
	keys, err := s.KnownKeys(ctx)
	if err != nil {
		return nil, err
	}
	return append(keys, release.OfficialKeys()...), nil
}

// ReleaseUploadError is a rejected release upload.
type ReleaseUploadError struct{ Msg string }

func (e *ReleaseUploadError) Error() string { return e.Msg }

// AddRelease stores a signed release: the signature must verify against a
// registered release key or the project's official keys, and the binary
// must match the manifest. bin may be nil for a release of a Shepherd
// binary this controller ships (an official release's description
// uploaded by hand, without internet access). The controller checks this
// for early feedback; every agent checks it again.
func (s *Service) AddRelease(ctx context.Context, signed *release.Signed, bin io.Reader, userID string) (*db.AgentRelease, error) {
	keys, err := s.uploadKeys(ctx)
	if err != nil {
		return nil, err
	}
	m, err := release.Verify(signed, keys)
	if err != nil {
		return nil, &ReleaseUploadError{Msg: "release rejected: " + err.Error() + " (register your release-signing key under Shepherds → Updates → Advanced settings first)"}
	}
	if _, err := s.st.R.GetReleaseFor(ctx, db.GetReleaseForParams{Version: m.Version, Os: m.OS, Arch: m.Arch}); err == nil {
		return nil, &ReleaseUploadError{Msg: fmt.Sprintf("release %s for %s/%s already exists", m.Version, m.OS, m.Arch)}
	}
	if bin == nil {
		b := s.bundledBinaryFor(m.SHA256)
		if b == nil {
			return nil, &ReleaseUploadError{Msg: fmt.Sprintf("this manifest describes a binary DROOL doesn't have (sha256 %.16s): upload the Shepherd program with it", m.SHA256)}
		}
		f, err := openBinary(b)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		bin = f
	}
	return s.storeRelease(ctx, signed, m, bin, userID)
}

// FetchRelease implements pb.AgentServer: stream a release binary to an
// authenticated agent, resuming at an offset.
func (s *Service) FetchRelease(req *pb.FetchReleaseRequest, stream pb.Agent_FetchReleaseServer) error {
	if _, _, err := s.authAgent(stream.Context(), false); err != nil {
		return err
	}
	rel, err := s.st.R.GetRelease(stream.Context(), req.ReleaseId)
	if err != nil {
		return status.Error(codes.NotFound, "unknown release")
	}
	rc, _, err := s.arts.Open(stream.Context(), rel.Sha256)
	if err != nil {
		return status.Error(codes.Internal, "release binary unavailable")
	}
	defer rc.Close()
	if req.Offset > 0 {
		if _, err := io.CopyN(io.Discard, rc, req.Offset); err != nil {
			return status.Error(codes.OutOfRange, "offset beyond the binary")
		}
	}
	buf := make([]byte, 256<<10)
	for {
		n, err := rc.Read(buf)
		if n > 0 {
			if serr := stream.Send(&pb.Chunk{Data: append([]byte(nil), buf[:n]...)}); serr != nil {
				return serr
			}
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return status.Error(codes.DataLoss, "reading the release failed")
		}
	}
}

// --- who can take which release ---

// ReleaseTrust is what a Shepherd's hello says about its update trust
// (agent_identities.release_trust_json). Shepherds before 1.0 say nothing.
type ReleaseTrust struct {
	KeyIDs         []string `json:"key_ids"`
	Official       bool     `json:"official"`        // trusts the project's keys, and has them
	OfficialDenied bool     `json:"official_denied"` // agent.yaml updates.official: deny
	UpdatesDenied  bool     `json:"updates_denied"`  // agent.yaml updates.policy: deny
}

func trustFromHello(rt *pb.ReleaseTrust) *ReleaseTrust {
	if rt == nil {
		return nil
	}
	return &ReleaseTrust{KeyIDs: nonNil(rt.KeyIds), Official: rt.Official, OfficialDenied: rt.OfficialDenied, UpdatesDenied: rt.UpdatesDenied}
}

// IdentityTrust is the release trust a Shepherd last reported (nil: it
// doesn't say).
func IdentityTrust(id db.AgentIdentity) *ReleaseTrust {
	if id.ReleaseTrustJson == "" {
		return nil
	}
	var t ReleaseTrust
	if json.Unmarshal([]byte(id.ReleaseTrustJson), &t) != nil {
		return nil
	}
	return &t
}

// failedHere: version failed its health check or was rolled back on this
// Shepherd, as it last reported.
func failedHere(id db.AgentIdentity, v string) bool {
	return (id.UpdateState == "rolled_back" || id.UpdateState == "failed") && id.UpdateVersion == v
}

// Blocker says why a Shepherd can't take a release ("" when it can). rel
// is the release for its platform (nil: none). trust nil means the
// Shepherd doesn't say what it trusts (built before 1.0): it may refuse,
// which only skips it.
func Blocker(id db.AgentIdentity, trust *ReleaseTrust, v string, rel *db.AgentRelease, retry bool) string {
	if rel == nil {
		return fmt.Sprintf("there is no %s release for %s/%s", v, id.Os, id.Arch)
	}
	if t := trust; t != nil {
		if t.UpdatesDenied {
			return "updates are turned off on this host (updates.policy: deny in /etc/drool/agent.yaml)"
		}
		if !slices.Contains(t.KeyIDs, rel.KeyID) {
			switch {
			case release.IsOfficialKey(rel.KeyID) && t.OfficialDenied:
				return "this host trusts only its own release keys (updates.official: deny in /etc/drool/agent.yaml)"
			case release.IsOfficialKey(rel.KeyID):
				return "this Shepherd doesn't have the DROOL release key: update it once with a custom release, or reinstall it"
			}
			return fmt.Sprintf("this host doesn't trust the key that signed %s (%s)", v, rel.KeyID)
		}
	}
	if m, err := manifestOf(*rel); err == nil {
		if m.MinFrom != "" && release.CompareVersions(id.AgentVersion, m.MinFrom) < 0 {
			return fmt.Sprintf("%s updates only from %s or newer; this Shepherd runs %s", v, m.MinFrom, id.AgentVersion)
		}
		if p := uint32(id.Protocol); p != 0 && (p < m.ProtocolMin || p > m.ProtocolMax) {
			return fmt.Sprintf("%s speaks protocol %d–%d, this Shepherd's connection uses %d", v, m.ProtocolMin, m.ProtocolMax, p)
		}
	}
	if !retry && failedHere(id, v) {
		return fmt.Sprintf("%s failed on this host before (%s); try again once the cause is fixed", v, id.UpdateDetail)
	}
	return ""
}

// platformReleases maps "os/arch" to a version's release.
func (s *Service) platformReleases(ctx context.Context, v string) map[string]*db.AgentRelease {
	rels, _ := s.st.R.ListReleases(ctx)
	out := map[string]*db.AgentRelease{}
	for i := range rels {
		if rels[i].Version == v {
			out[rels[i].Os+"/"+rels[i].Arch] = &rels[i]
		}
	}
	return out
}

// --- rollouts ---

// RolloutOptions say how a rollout runs, besides its version.
type RolloutOptions struct {
	HostIDs   []string // nil: every Shepherd
	Policy    UpdatePolicy
	Automatic bool
	// Retry: a person asked to try the version again where it failed.
	Retry  bool
	UserID string
}

// ErrNothingToUpdate: every chosen Shepherd already runs the version.
var ErrNothingToUpdate = errors.New("every chosen Shepherd already runs this version or a newer one")

// StartRollout creates a rollout of version to the Shepherds behind it
// (all of them when o.HostIDs is nil). The canary wave comes first, then
// batches; connected Shepherds whose trust is known lead, so the canary is
// a host that can take the release. Shepherds that can't take it are
// listed as skipped with the reason (a person's rollout) or left out (an
// automatic one, which the status page explains).
func (s *Service) StartRollout(ctx context.Context, v string, o RolloutOptions) (string, error) {
	s.rollMu.Lock()
	defer s.rollMu.Unlock()
	return s.startRollout(ctx, v, o)
}

// startRollout is StartRollout with the rollout lock held: which hosts are
// already in a rollout can't change between the check and the insert.
func (s *Service) startRollout(ctx context.Context, v string, o RolloutOptions) (string, error) {
	p := o.Policy
	if err := p.normalize(); err != nil {
		return "", err
	}
	idents, err := s.st.R.ListLiveIdentities(ctx)
	if err != nil {
		return "", err
	}
	busy := s.hostsInActiveRollouts(ctx, "")
	names := s.hostNames(ctx)
	rels := s.platformReleases(ctx, v)
	want := map[string]bool{}
	for _, h := range o.HostIDs {
		want[h] = true
	}
	type target struct {
		id     db.AgentIdentity
		rank   int // 0 connected, trust known; 1 connected; 2 offline
		reason string
	}
	var targets, skipped []target
	var conflicts []string
	for _, id := range idents {
		if id.Status != "active" || (len(o.HostIDs) > 0 && !want[id.HostID]) {
			continue
		}
		if release.CompareVersions(id.AgentVersion, v) >= 0 {
			continue
		}
		if r, ok := busy[id.HostID]; ok {
			if o.Automatic {
				continue
			}
			conflicts = append(conflicts, fmt.Sprintf("%s is in the update to %s (%s)", names[id.HostID], r.Version, r.Status))
			continue
		}
		trust := IdentityTrust(id)
		if why := Blocker(id, trust, v, rels[id.Os+"/"+id.Arch], o.Retry); why != "" {
			if !o.Automatic {
				skipped = append(skipped, target{id: id, reason: why})
			}
			continue
		}
		t := target{id: id, rank: 2}
		if _, live := s.SessionFor(id.HostID); live {
			t.rank = 1
			if trust != nil {
				t.rank = 0
			}
		}
		targets = append(targets, t)
	}
	if len(conflicts) > 0 {
		return "", fmt.Errorf("%s: resume or cancel that update first", strings.Join(conflicts, "; "))
	}
	if len(targets) == 0 {
		if len(skipped) > 0 {
			reasons := []string{}
			for _, t := range skipped {
				reasons = append(reasons, names[t.id.HostID]+": "+t.reason)
			}
			return "", fmt.Errorf("no Shepherd can take %s: %s", v, strings.Join(reasons, "; "))
		}
		return "", ErrNothingToUpdate
	}
	sort.SliceStable(targets, func(i, j int) bool {
		if targets[i].rank != targets[j].rank {
			return targets[i].rank < targets[j].rank
		}
		return names[targets[i].id.HostID] < names[targets[j].id.HostID]
	})
	id := ids.New(ids.AgentRollout)
	now := s.now().UnixMilli()
	err = s.st.InTx(ctx, func(q *db.Queries) error {
		if err := q.InsertRollout(ctx, db.InsertRolloutParams{ID: id, Version: v, Canary: int64(p.Canary), BatchSize: int64(p.BatchSize),
			MaxFailures: int64(p.MaxFailures), HealthWindowS: int64(p.HealthWindowS), Automatic: boolInt(o.Automatic),
			CreatedBy: sql.NullString{String: o.UserID, Valid: o.UserID != ""}, CreatedAt: now, UpdatedAt: now}); err != nil {
			return err
		}
		if o.Retry {
			if err := q.SetRolloutRetry(ctx, db.SetRolloutRetryParams{Retry: 1, ID: id}); err != nil {
				return err
			}
		}
		for i, t := range targets {
			wave := 0
			if i >= p.Canary {
				wave = 1 + (i-p.Canary)/p.BatchSize
			}
			if err := q.InsertRolloutHost(ctx, db.InsertRolloutHostParams{RolloutID: id, HostID: t.id.HostID, Wave: int64(wave),
				FromVersion: t.id.AgentVersion, UpdatedAt: now}); err != nil {
				return err
			}
		}
		for _, t := range skipped {
			if err := q.InsertSkippedRolloutHost(ctx, db.InsertSkippedRolloutHostParams{RolloutID: id, HostID: t.id.HostID, Wave: 0,
				FromVersion: t.id.AgentVersion, Detail: t.reason, UpdatedAt: now}); err != nil {
				return err
			}
		}
		_, err := s.audit.RecordTx(ctx, q, audit.Event{Action: "agent.rollout.start", TargetType: "agent_rollout", TargetID: id,
			Details: map[string]any{"version": v, "hosts": len(targets), "skipped": len(skipped), "canary": p.Canary, "batch_size": p.BatchSize,
				"max_failures": p.MaxFailures, "automatic": o.Automatic, "retry": o.Retry}})
		return err
	})
	if err != nil {
		return "", err
	}
	go s.advanceRollouts(context.Background())
	return id, nil
}

// hostsInActiveRollouts maps each host in a running or paused update
// rollout (other than except) to that rollout.
func (s *Service) hostsInActiveRollouts(ctx context.Context, except string) map[string]db.AgentRollout {
	out := map[string]db.AgentRollout{}
	rolls, _ := s.st.R.UnfinishedRollouts(ctx)
	for _, r := range rolls {
		if r.Kind != "update" || r.ID == except {
			continue
		}
		hosts, _ := s.st.R.ListRolloutHosts(ctx, r.ID)
		for _, h := range hosts {
			if !terminal[h.State] {
				out[h.HostID] = r
			}
		}
	}
	return out
}

func (s *Service) hostNames(ctx context.Context) map[string]string {
	out := map[string]string{}
	hs, _ := s.st.R.ListHosts(ctx)
	for _, h := range hs {
		out[h.ID] = h.Name
	}
	return out
}

// SetRolloutStatus pauses, resumes or cancels a rollout.
func (s *Service) SetRolloutStatus(ctx context.Context, id, st, reason string) error {
	return s.st.W.SetRolloutStatus(ctx, db.SetRolloutStatusParams{Status: st, PausedReason: reason, UpdatedAt: s.now().UnixMilli(), ID: id})
}

// ChangeRolloutStatus is a person's pause, resume or cancel, under the
// rollout lock so that a wave pass running meanwhile can't overwrite it.
// Resuming accepts the failures so far: only hosts that fail after it count
// toward the rollout's failure limit, so it continues (with a new canary
// when the canary was what failed) instead of pausing again at once.
func (s *Service) ChangeRolloutStatus(ctx context.Context, id, st, reason string) error {
	s.rollMu.Lock()
	defer s.rollMu.Unlock()
	now := s.now().UnixMilli()
	if st == "running" {
		if err := s.st.W.ResumeRollout(ctx, db.ResumeRolloutParams{ResumedAt: now, UpdatedAt: now, ID: id}); err != nil {
			return err
		}
		go s.advanceRollouts(context.Background())
		return nil
	}
	return s.st.W.SetRolloutStatus(ctx, db.SetRolloutStatusParams{Status: st, PausedReason: reason, UpdatedAt: now, ID: id})
}

var terminal = map[string]bool{"healthy": true, "rolled_back": true, "failed": true, "skipped": true}

// supersededPrefix starts the reason of an automatic rollout that a newer
// automatic one replaced (a person's cancel says who).
const supersededPrefix = "superseded by the update to "

// RunRollouts advances rollouts and the automatic policy until ctx ends.
func (s *Service) RunRollouts(ctx context.Context) {
	t := time.NewTicker(10 * time.Second)
	defer t.Stop()
	for {
		s.RunUpdatePass(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// RunUpdatePass is one round of RunRollouts: the automatic policy, then
// the rollouts' waves.
func (s *Service) RunUpdatePass(ctx context.Context) {
	s.autoPolicy(ctx)
	s.advanceRollouts(ctx)
}

// AutoTarget is the version the automatic policy keeps Shepherds on: the
// exact version it names, or the newest release in its channel that this
// controller can speak to ("" when there is none).
func (s *Service) AutoTarget(ctx context.Context, p UpdatePolicy) string {
	rels, err := s.st.R.ListReleases(ctx)
	if err != nil {
		return ""
	}
	target := ""
	own := version.Get().Version
	for _, r := range rels {
		if !s.speaks(r) {
			continue
		}
		// Shepherds follow DROOL: an official release newer than this DROOL
		// (uploaded by hand) waits for DROOL's own update. A custom release
		// is the operator's to roll out.
		if release.IsOfficialKey(r.KeyID) && release.CompareVersions(r.Version, own) > 0 {
			continue
		}
		if p.Version != "" {
			if r.Version == p.Version {
				target = r.Version
			}
			continue
		}
		if ChannelIncludes(p.Channel, r.Channel) && (target == "" || release.CompareVersions(r.Version, target) > 0) {
			target = r.Version
		}
	}
	return target
}

// speaks reports whether this controller and the release share a protocol.
func (s *Service) speaks(r db.AgentRelease) bool {
	lo, hi := max(uint32(r.ProtocolMin), s.ControllerRange.Min), min(uint32(r.ProtocolMax), s.ControllerRange.Max)
	return lo <= hi
}

// autoPolicy keeps Shepherds on the automatic target. It never fights a
// person: a rollout to the target that someone paused or cancelled stops
// it for that version, and Shepherds in someone's running rollout are left
// to it. Shepherds that join later, or come back after a newer target
// appeared, get a rollout of their own; an older automatic rollout still
// running is superseded by the newer one.
func (s *Service) autoPolicy(ctx context.Context) {
	s.rollMu.Lock()
	defer s.rollMu.Unlock()
	p := s.GetUpdatePolicy(ctx)
	if p.Policy != "automatic" {
		return
	}
	target := s.AutoTarget(ctx, p)
	if target == "" {
		return
	}
	atTarget, _ := s.st.R.ListUpdateRolloutsForVersion(ctx, target)
	if stoppedBy(atTarget) != "" {
		return // a person decides what happens to this version next
	}
	unfinished, _ := s.st.R.UnfinishedRollouts(ctx)
	for _, r := range unfinished {
		if r.Kind == "update" && r.Automatic != 0 && release.CompareVersions(r.Version, target) < 0 {
			s.SetRolloutStatus(ctx, r.ID, "cancelled", supersededPrefix+target)
			s.audit.Record(ctx, audit.Event{Action: "agent.rollout.supersede", TargetType: "agent_rollout", TargetID: r.ID, Actor: &audit.System,
				Details: map[string]any{"version": r.Version, "by": target}})
		}
	}
	busy := s.hostsInActiveRollouts(ctx, "")
	idents, _ := s.st.R.ListLiveIdentities(ctx)
	rels := s.platformReleases(ctx, target)
	var hosts []string
	for _, id := range idents {
		if id.Status != "active" || release.CompareVersions(id.AgentVersion, target) >= 0 {
			continue
		}
		if _, ok := busy[id.HostID]; ok {
			continue
		}
		if s.updateInProgress(id) {
			continue // finishing another update (one a newer target superseded): offered when it's done
		}
		if Blocker(id, IdentityTrust(id), target, rels[id.Os+"/"+id.Arch], false) != "" {
			continue // the status page says why
		}
		if s.triedAutomatically(atTarget, id.HostID) {
			continue
		}
		hosts = append(hosts, id.HostID)
	}
	if len(hosts) == 0 {
		return
	}
	if _, err := s.startRollout(ctx, target, RolloutOptions{HostIDs: hosts, Policy: p, Automatic: true}); err != nil && !errors.Is(err, ErrNothingToUpdate) {
		s.log.Warn("automatic Shepherd update rollout", "version", target, "err", err)
	}
}

// stoppedBy is why automatic updates leave a version alone: a rollout to it
// that a person paused or cancelled ("" when none). rolls are that version's.
func stoppedBy(rolls []db.AgentRollout) string {
	for _, r := range rolls {
		if r.Kind != "update" {
			continue
		}
		if r.Status == "paused" || r.Status == "cancelled" && !strings.HasPrefix(r.PausedReason, supersededPrefix) {
			if r.PausedReason == "" {
				return r.Status
			}
			return r.PausedReason
		}
	}
	return ""
}

// busyDetail is what a Shepherd says when it refuses an offer because it
// is still finishing another update: a transient refusal.
const busyDetail = "another update is still in progress"

// triedAutomatically: an automatic rollout to this version (rolls are its
// rollouts) already finished with this host — skipped, refused or failed —
// so it isn't offered again by itself. A host that was only busy finishing
// another update is offered again.
func (s *Service) triedAutomatically(rolls []db.AgentRollout, hostID string) bool {
	for _, r := range rolls {
		if r.Kind != "update" || r.Automatic == 0 {
			continue
		}
		hosts, _ := s.st.R.ListRolloutHosts(context.Background(), r.ID)
		for _, h := range hosts {
			if h.HostID == hostID && terminal[h.State] && h.State != "healthy" && !(h.State == "skipped" && strings.Contains(h.Detail, busyDetail)) {
				return true
			}
		}
	}
	return false
}

// updateInProgress: the Shepherd was last offered an update, or reported
// one under way, recently enough that it may still be at it.
func (s *Service) updateInProgress(id db.AgentIdentity) bool {
	switch id.UpdateState {
	case "offered", "staged", "updating":
		return id.UpdateAt.Valid && s.now().Sub(time.UnixMilli(id.UpdateAt.Int64)) < 30*time.Minute
	}
	return false
}

// advanceRollouts offers updates wave by wave. A wave starts when every
// earlier wave is finished or waiting for offline hosts, which catch up
// when they reconnect. The canary wave needs a host that actually takes
// the update: when its hosts are offline or skipped, the next connected
// host of a later wave becomes the canary. Failures beyond the limit pause
// the rollout, so a bad release stops at the canary instead of reaching
// the fleet.
func (s *Service) advanceRollouts(ctx context.Context) {
	s.rollMu.Lock()
	defer s.rollMu.Unlock()
	rolls, err := s.st.R.ActiveRollouts(ctx)
	if err != nil {
		return
	}
	for _, r := range rolls {
		hosts, err := s.st.R.ListRolloutHosts(ctx, r.ID)
		if err != nil {
			continue
		}
		failures, open := 0, 0
		now := s.now()
		connected := func(hostID string) bool { _, ok := s.SessionFor(hostID); return ok }
		for i, h := range hosts {
			// Resuming accepted the failures before it.
			if (h.State == "rolled_back" || h.State == "failed") && h.UpdatedAt > r.ResumedAt {
				failures++
			}
			if (h.State == "offered" || h.State == "staged" || h.State == "updating") &&
				now.Sub(time.UnixMilli(h.UpdatedAt)) > time.Duration(r.HealthWindowS)*time.Second+10*time.Minute &&
				!s.converting(r.ID, h.HostID) {
				if r.Kind == "update" && h.State != "updating" && !connected(h.HostID) {
					// Cut off with its session before the update helper
					// started: nothing changed there; it is offered again
					// when it reconnects.
					s.setHostState(ctx, r.ID, h.HostID, "pending", interruptedDetail)
					hosts[i].State = "pending"
					continue
				}
				// A host that accepted an offer but never reported back.
				s.setHostState(ctx, r.ID, h.HostID, "failed", "no report from the agent")
				hosts[i].State = "failed"
				failures++
			}
		}
		if int64(failures) != r.Failures {
			s.st.W.SetRolloutFailures(ctx, db.SetRolloutFailuresParams{Failures: int64(failures), UpdatedAt: now.UnixMilli(), ID: r.ID})
		}
		if int64(failures) >= r.MaxFailures {
			s.SetRolloutStatus(ctx, r.ID, "paused", fmt.Sprintf("paused after %d failed host(s); fix the problem, then resume or cancel", failures))
			s.audit.Record(ctx, audit.Event{Action: "agent.rollout.pause", TargetType: "agent_rollout", TargetID: r.ID, Actor: &audit.System,
				Details: map[string]any{"failures": failures}})
			continue
		}
		if r.Kind == "update" {
			hosts = s.promoteCanary(ctx, r, hosts, connected)
		}
		// Current wave: the lowest wave with hosts that are not finished
		// and not merely waiting for an offline agent.
		wave := int64(-1)
		for _, h := range hosts {
			if terminal[h.State] {
				continue
			}
			open++
			if h.State == "pending" && r.Kind == "update" && !connected(h.HostID) {
				continue // offline: catches up later, doesn't block the wave
			}
			if wave < 0 || h.Wave < wave {
				wave = h.Wave
			}
		}
		if open == 0 {
			s.SetRolloutStatus(ctx, r.ID, "completed", "")
			s.audit.Record(ctx, audit.Event{Action: "agent.rollout.complete", TargetType: "agent_rollout", TargetID: r.ID, Actor: &audit.System})
			continue
		}
		for _, h := range hosts {
			if h.State == "pending" && h.Wave <= wave {
				if r.Kind == "install" {
					s.convertHost(ctx, r, h.HostID)
				} else {
					s.offer(ctx, r, h.HostID)
				}
			}
		}
	}
}

// promoteCanary makes sure the canary wave has hosts that can take the
// update: healthy ones, or ones it is in progress on or can be offered to
// now. Short of r.Canary such hosts, the next connected pending hosts of
// later waves join the canary wave. It returns the hosts as they now are.
func (s *Service) promoteCanary(ctx context.Context, r db.AgentRollout, hosts []db.ListRolloutHostsRow, connected func(string) bool) []db.ListRolloutHostsRow {
	have := 0
	for _, h := range hosts {
		if h.Wave != 0 {
			continue
		}
		if h.State == "healthy" || !terminal[h.State] && (h.State != "pending" || connected(h.HostID)) {
			have++
		}
	}
	for i := range hosts {
		if int64(have) >= r.Canary {
			break
		}
		h := &hosts[i]
		if h.Wave > 0 && h.State == "pending" && connected(h.HostID) {
			if err := s.st.W.SetRolloutHostWave(ctx, db.SetRolloutHostWaveParams{Wave: 0, UpdatedAt: s.now().UnixMilli(), RolloutID: r.ID, HostID: h.HostID}); err == nil {
				s.log.Info("a new canary for the Shepherd rollout", "rollout", r.ID, "host", h.HostID, "was_wave", h.Wave)
				h.Wave = 0
				have++
			}
		}
	}
	return hosts
}

func (s *Service) setHostState(ctx context.Context, rolloutID, hostID, st, detail string) {
	s.st.W.SetRolloutHostState(ctx, db.SetRolloutHostStateParams{State: st, Detail: detail, UpdatedAt: s.now().UnixMilli(),
		RolloutID: rolloutID, HostID: hostID})
	s.hostChanged(hostID)
}

// offer sends the release for the host's platform to its agent, unless the
// host can't take it, which skips it with the reason.
func (s *Service) offer(ctx context.Context, r db.AgentRollout, hostID string) {
	se, ok := s.SessionFor(hostID)
	if !ok {
		return
	}
	if fh := s.firewallHooks(); fh != nil && fh.UpdateBlocked(hostID) != "" {
		return // a firewall transaction is pending: the update waits (DESIGN §17.3)
	}
	info := se.info()
	if release.CompareVersions(info.AgentVersion, r.Version) >= 0 {
		s.setHostState(ctx, r.ID, hostID, "healthy", "already running "+info.AgentVersion)
		return
	}
	var relp *db.AgentRelease
	if rel, err := s.st.R.GetReleaseFor(ctx, db.GetReleaseForParams{Version: r.Version, Os: se.hello.Info.Os, Arch: se.hello.Info.Arch}); err == nil {
		relp = &rel
	}
	id := se.ident
	id.AgentVersion, id.Protocol = info.AgentVersion, int64(info.Protocol)
	if fresh, err := s.st.R.GetAgentIdentity(ctx, se.ident.ID); err == nil {
		id.UpdateState, id.UpdateVersion, id.UpdateDetail = fresh.UpdateState, fresh.UpdateVersion, fresh.UpdateDetail
	}
	if why := Blocker(id, trustFromHello(se.hello.ReleaseTrust), r.Version, relp, r.Retry != 0); why != "" {
		s.setHostState(ctx, r.ID, hostID, "skipped", why)
		return
	}
	rel := *relp
	s.setHostState(ctx, r.ID, hostID, "offered", "")
	s.st.W.SetIdentityUpdate(ctx, db.SetIdentityUpdateParams{UpdateState: "offered", UpdateVersion: r.Version, UpdateDetail: "",
		UpdateAt: sql.NullInt64{Int64: s.now().UnixMilli(), Valid: true}, ID: se.ident.ID})
	se.send(&pb.ControllerMessage{Msg: &pb.ControllerMessage_UpdateOffer{UpdateOffer: &pb.UpdateOffer{ReleaseId: rel.ID,
		ManifestPayload: rel.ManifestPayload, KeyId: rel.KeyID, Signature: rel.Signature, HealthWindowSeconds: uint32(r.HealthWindowS),
		Retry: r.Retry != 0}}})
}

// offerPendingUpdate: an agent that reconnects catches up with any running
// rollout it is part of.
func (s *Service) offerPendingUpdate(ctx context.Context, se *Session) {
	time.Sleep(2 * time.Second) // let the hello settle
	rolls, err := s.st.R.ActiveRollouts(ctx)
	if err != nil {
		return
	}
	for _, r := range rolls {
		hosts, _ := s.st.R.ListRolloutHosts(ctx, r.ID)
		for _, h := range hosts {
			if h.HostID == se.ident.HostID && h.State == "pending" {
				s.advanceRollouts(ctx)
				return
			}
		}
	}
}

// refusalOf is why an agent refused an offer before changing anything
// ("" when it didn't refuse). 1.0 agents say so; older ones are known by
// the words they have always used.
func refusalOf(u *pb.UpdateReport) string {
	if u.Refusal != "" {
		return u.Refusal
	}
	if u.State != "failed" || u.Version != "" {
		return "" // older agents refuse before they know the version
	}
	for code, words := range map[string]string{"untrusted_key": "does not trust", "updates_denied": "updates.policy: deny",
		"failed_before": "already failed its health check", "downgrade": "downgrades are refused",
		"upgrade_path": "needs at least version", "busy": "another update is still in progress"} {
		if strings.Contains(u.Detail, words) {
			return code
		}
	}
	return ""
}

// interruptedDetail: an offered update that didn't reach the update helper
// before the Shepherd's session ended. Nothing changed on the host.
const interruptedDetail = "interrupted before it was applied (the Shepherd's connection ended); offered again"

// onHelloUpdate takes what a (re)connecting Shepherd says about updates. An
// offered attempt (offered or staged) whose version the hello says nothing
// about — no update under way and no result for it — ended with the session
// that carried it before its helper started: nothing changed on the host,
// so it goes back to pending and is offered again. A hello that does report
// on that version (under way, or a result) is taken as the attempt's own:
// a failure is never dropped, so a rolled-back canary still pauses the
// rollout. (A Shepherd sets an earlier result aside when it accepts a Try
// again, so that one isn't reported for the new attempt.)
func (s *Service) onHelloUpdate(ctx context.Context, ident db.AgentIdentity, u *pb.UpdateReport) {
	s.rollMu.Lock()
	rolls, _ := s.st.R.UnfinishedRollouts(ctx)
	for _, r := range rolls {
		if r.Kind != "update" {
			continue
		}
		hosts, _ := s.st.R.ListRolloutHosts(ctx, r.ID)
		for _, h := range hosts {
			if h.HostID != ident.HostID || (h.State != "offered" && h.State != "staged") {
				continue
			}
			if u != nil && u.Version == r.Version {
				continue // the report below answers for it
			}
			s.setHostState(ctx, r.ID, h.HostID, "pending", interruptedDetail)
		}
	}
	s.rollMu.Unlock()
	if u != nil {
		s.onUpdateReport(ctx, ident, u)
	}
}

// onUpdateReport records an agent's update progress.
func (s *Service) onUpdateReport(ctx context.Context, ident db.AgentIdentity, u *pb.UpdateReport) {
	if u.State == "" {
		return
	}
	s.rollMu.Lock()
	defer s.rollMu.Unlock()
	refused := refusalOf(u)
	if u.Version == "" && u.ReleaseId != "" {
		// Refused before the agent trusted the manifest: the controller
		// knows which version it offered.
		if rel, err := s.st.R.GetRelease(ctx, u.ReleaseId); err == nil {
			u.Version = rel.Version
		}
	}
	idState := u.State
	if refused != "" {
		idState = "refused" // not a failed update: nothing changed on the host
	}
	s.st.W.SetIdentityUpdate(ctx, db.SetIdentityUpdateParams{UpdateState: idState, UpdateVersion: u.Version, UpdateDetail: u.Detail,
		UpdateAt: sql.NullInt64{Int64: s.now().UnixMilli(), Valid: true}, ID: ident.ID})
	rolls, _ := s.st.R.UnfinishedRollouts(ctx)
	for _, r := range rolls {
		if r.Kind != "update" {
			continue
		}
		if u.Version != "" && u.Version != r.Version {
			continue // about another rollout's version
		}
		hosts, _ := s.st.R.ListRolloutHosts(ctx, r.ID)
		for _, h := range hosts {
			if h.HostID != ident.HostID || terminal[h.State] || h.State == "pending" {
				continue
			}
			st := u.State
			if st == "healthy" && u.Version != r.Version {
				continue
			}
			if refused != "" {
				st = "skipped"
			}
			s.setHostState(ctx, r.ID, h.HostID, st, u.Detail)
			if st == "healthy" || st == "rolled_back" || st == "failed" || st == "skipped" {
				res := audit.Success
				if st != "healthy" {
					res = audit.Failure
				}
				s.audit.Record(ctx, audit.Event{Action: "agent.update", TargetType: "host", TargetID: h.HostID, Result: res, Actor: agentActor(ident),
					Details: map[string]any{"rollout_id": r.ID, "from": h.FromVersion, "to": r.Version, "state": st, "refusal": refused, "detail": u.Detail}})
				go s.advanceRollouts(context.WithoutCancel(ctx))
			}
		}
	}
}

// --- helpers for the API ---

// ParseSignedRelease parses the release manifest file written by
// `drool release sign`.
func ParseSignedRelease(b []byte) (*release.Signed, error) {
	var s release.Signed
	dec := json.NewDecoder(bytes.NewReader(b))
	if err := dec.Decode(&s); err != nil || len(s.Payload) == 0 {
		return nil, errors.New("not a DROOL release manifest (the .release.json file from `drool release sign`)")
	}
	return &s, nil
}
