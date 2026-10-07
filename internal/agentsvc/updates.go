package agentsvc

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"drool/internal/agentrpc/pb"
	"drool/internal/audit"
	"drool/internal/ids"
	"drool/internal/release"
	"drool/internal/store/db"
)

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

// DefaultUpdatePolicy: manual, canary of one, batches of five, pause at
// the first failure, two minutes to become healthy.
func DefaultUpdatePolicy() UpdatePolicy {
	return UpdatePolicy{Policy: "manual", Channel: "stable", Canary: 1, BatchSize: 5, MaxFailures: 1, HealthWindowS: 120}
}

func (p *UpdatePolicy) normalize() error {
	switch p.Policy {
	case "manual", "automatic":
	default:
		return fmt.Errorf("policy must be manual or automatic")
	}
	if p.Channel == "" {
		p.Channel = "stable"
	}
	if p.Canary < 1 || p.BatchSize < 1 || p.MaxFailures < 1 || p.HealthWindowS < 30 || p.HealthWindowS > 3600 {
		return fmt.Errorf("canary, batch size and max failures must be at least 1; the health window 30–3600 s")
	}
	return nil
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

// ReleaseUploadError is a rejected release upload.
type ReleaseUploadError struct{ Msg string }

func (e *ReleaseUploadError) Error() string { return e.Msg }

// AddRelease stores a signed release: the signature must verify against a
// registered release key and the binary must match the manifest. The
// controller checks this for early feedback; every agent checks it again.
func (s *Service) AddRelease(ctx context.Context, signed *release.Signed, bin io.Reader, userID string) (*db.AgentRelease, error) {
	keys, err := s.KnownKeys(ctx)
	if err != nil {
		return nil, err
	}
	m, err := release.Verify(signed, keys)
	if err != nil {
		return nil, &ReleaseUploadError{Msg: "release rejected: " + err.Error() + " (register the release public key under Agents → Updates first)"}
	}
	if _, err := s.st.R.GetReleaseFor(ctx, db.GetReleaseForParams{Version: m.Version, Os: m.OS, Arch: m.Arch}); err == nil {
		return nil, &ReleaseUploadError{Msg: fmt.Sprintf("release %s for %s/%s already exists", m.Version, m.OS, m.Arch)}
	}
	a, err := s.arts.Put(ctx, bin, fmt.Sprintf("drool-agent-%s-%s-%s", m.Version, m.OS, m.Arch), userID)
	if err != nil {
		return nil, err
	}
	if a.SHA256 != m.SHA256 || a.Size != m.Size {
		return nil, &ReleaseUploadError{Msg: fmt.Sprintf("the uploaded binary (sha256 %.16s, %d bytes) is not the one the manifest signs (%.16s, %d bytes)", a.SHA256, a.Size, m.SHA256, m.Size)}
	}
	r := db.InsertReleaseParams{ID: ids.New(ids.AgentRelease), Version: m.Version, Channel: m.Channel, Os: m.OS, Arch: m.Arch,
		ProtocolMin: int64(m.ProtocolMin), ProtocolMax: int64(m.ProtocolMax), Sha256: m.SHA256, Size: m.Size, KeyID: signed.KeyID,
		ManifestPayload: signed.Payload, Signature: signed.Signature, UploadedBy: sql.NullString{String: userID, Valid: userID != ""},
		CreatedAt: s.now().UnixMilli()}
	if err := s.st.W.InsertRelease(ctx, r); err != nil {
		return nil, err
	}
	rel, err := s.st.R.GetRelease(ctx, r.ID)
	return &rel, err
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

// --- rollouts ---

// StartRollout creates a rollout to version over hosts (all agent hosts
// when nil): a canary wave, then batches.
func (s *Service) StartRollout(ctx context.Context, version string, hostIDs []string, p UpdatePolicy, automatic bool, userID string) (string, error) {
	if err := p.normalize(); err != nil {
		return "", err
	}
	idents, err := s.st.R.ListLiveIdentities(ctx)
	if err != nil {
		return "", err
	}
	want := map[string]bool{}
	for _, h := range hostIDs {
		want[h] = true
	}
	var targets []db.AgentIdentity
	for _, id := range idents {
		if id.Status == "active" && (len(hostIDs) == 0 || want[id.HostID]) {
			targets = append(targets, id)
		}
	}
	if len(targets) == 0 {
		return "", errors.New("no enrolled agents to update")
	}
	sort.Slice(targets, func(i, j int) bool { return targets[i].HostID < targets[j].HostID })
	id := ids.New(ids.AgentRollout)
	now := s.now().UnixMilli()
	err = s.st.InTx(ctx, func(q *db.Queries) error {
		if err := q.InsertRollout(ctx, db.InsertRolloutParams{ID: id, Version: version, Canary: int64(p.Canary), BatchSize: int64(p.BatchSize),
			MaxFailures: int64(p.MaxFailures), HealthWindowS: int64(p.HealthWindowS), Automatic: boolInt(automatic),
			CreatedBy: sql.NullString{String: userID, Valid: userID != ""}, CreatedAt: now, UpdatedAt: now}); err != nil {
			return err
		}
		for i, t := range targets {
			wave := 0
			if i >= p.Canary {
				wave = 1 + (i-p.Canary)/p.BatchSize
			}
			if err := q.InsertRolloutHost(ctx, db.InsertRolloutHostParams{RolloutID: id, HostID: t.HostID, Wave: int64(wave),
				FromVersion: t.AgentVersion, UpdatedAt: now}); err != nil {
				return err
			}
		}
		_, err := s.audit.RecordTx(ctx, q, audit.Event{Action: "agent.rollout.start", TargetType: "agent_rollout", TargetID: id,
			Details: map[string]any{"version": version, "hosts": len(targets), "canary": p.Canary, "batch_size": p.BatchSize,
				"max_failures": p.MaxFailures, "automatic": automatic}})
		return err
	})
	if err != nil {
		return "", err
	}
	go s.advanceRollouts(context.Background())
	return id, nil
}

// SetRolloutStatus pauses, resumes or cancels a rollout.
func (s *Service) SetRolloutStatus(ctx context.Context, id, st, reason string) error {
	return s.st.W.SetRolloutStatus(ctx, db.SetRolloutStatusParams{Status: st, PausedReason: reason, UpdatedAt: s.now().UnixMilli(), ID: id})
}

var terminal = map[string]bool{"healthy": true, "rolled_back": true, "failed": true, "skipped": true}

// RunRollouts advances rollouts and the automatic policy until ctx ends.
func (s *Service) RunRollouts(ctx context.Context) {
	t := time.NewTicker(10 * time.Second)
	defer t.Stop()
	for {
		s.autoPolicy(ctx)
		s.advanceRollouts(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// autoPolicy starts a rollout when the automatic policy names a release
// newer than some agents run and no rollout to it exists yet.
func (s *Service) autoPolicy(ctx context.Context) {
	p := s.GetUpdatePolicy(ctx)
	if p.Policy != "automatic" {
		return
	}
	rels, err := s.st.R.ListReleases(ctx)
	if err != nil || len(rels) == 0 {
		return
	}
	target := ""
	for _, r := range rels {
		if p.Version != "" {
			if r.Version == p.Version {
				target = r.Version
			}
			continue
		}
		if r.Channel == p.Channel && (target == "" || release.CompareVersions(r.Version, target) > 0) {
			target = r.Version
		}
	}
	if target == "" {
		return
	}
	rolls, _ := s.st.R.ListRollouts(ctx)
	for _, r := range rolls {
		if r.Version == target && (r.Status == "running" || r.Status == "paused" || r.Automatic != 0) {
			return // already rolling out (or paused/finished: a person decides what next)
		}
	}
	idents, _ := s.st.R.ListLiveIdentities(ctx)
	behind := false
	for _, id := range idents {
		if id.Status == "active" && release.CompareVersions(id.AgentVersion, target) < 0 {
			behind = true
		}
	}
	if !behind {
		return
	}
	if _, err := s.StartRollout(ctx, target, nil, p, true, ""); err != nil {
		s.log.Warn("automatic agent update rollout", "version", target, "err", err)
	}
}

// advanceRollouts offers updates wave by wave. A wave starts when every
// earlier wave is finished or waiting for offline hosts, which catch up
// when they reconnect. Failures beyond the limit pause the rollout, so a
// bad release stops at the canary instead of reaching the fleet.
func (s *Service) advanceRollouts(ctx context.Context) {
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
		for _, h := range hosts {
			if h.State == "rolled_back" || h.State == "failed" {
				failures++
			}
			// A host that accepted an offer but never reported back.
			if (h.State == "offered" || h.State == "staged" || h.State == "updating") &&
				now.Sub(time.UnixMilli(h.UpdatedAt)) > time.Duration(r.HealthWindowS)*time.Second+10*time.Minute {
				s.setHostState(ctx, r.ID, h.HostID, "failed", "no report from the agent")
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
		// Current wave: the lowest wave with hosts that are not finished
		// and not merely waiting for an offline agent.
		wave := int64(-1)
		for _, h := range hosts {
			if terminal[h.State] {
				continue
			}
			open++
			if h.State == "pending" {
				if _, ok := s.SessionFor(h.HostID); !ok {
					continue // offline: catches up later, doesn't block the wave
				}
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
				s.offer(ctx, r, h.HostID)
			}
		}
	}
}

func (s *Service) setHostState(ctx context.Context, rolloutID, hostID, st, detail string) {
	s.st.W.SetRolloutHostState(ctx, db.SetRolloutHostStateParams{State: st, Detail: detail, UpdatedAt: s.now().UnixMilli(),
		RolloutID: rolloutID, HostID: hostID})
	s.hostChanged(hostID)
}

// offer sends the release for the host's platform to its agent.
func (s *Service) offer(ctx context.Context, r db.AgentRollout, hostID string) {
	se, ok := s.SessionFor(hostID)
	if !ok {
		return
	}
	if fh := s.firewallHooks(); fh != nil && fh.UpdateBlocked(hostID) != "" {
		return // a firewall transaction is pending: the update waits (DESIGN §17.3)
	}
	info := se.info()
	if release.CompareVersions(info.AgentVersion, r.Version) == 0 {
		s.setHostState(ctx, r.ID, hostID, "healthy", "already running "+r.Version)
		return
	}
	rel, err := s.st.R.GetReleaseFor(ctx, db.GetReleaseForParams{Version: r.Version, Os: se.hello.Info.Os, Arch: se.hello.Info.Arch})
	if err != nil {
		s.setHostState(ctx, r.ID, hostID, "skipped", fmt.Sprintf("no %s release for %s/%s", r.Version, se.hello.Info.Os, se.hello.Info.Arch))
		return
	}
	s.setHostState(ctx, r.ID, hostID, "offered", "")
	s.st.W.SetIdentityUpdate(ctx, db.SetIdentityUpdateParams{UpdateState: "offered", UpdateVersion: r.Version, UpdateDetail: "",
		UpdateAt: sql.NullInt64{Int64: s.now().UnixMilli(), Valid: true}, ID: se.ident.ID})
	se.send(&pb.ControllerMessage{Msg: &pb.ControllerMessage_UpdateOffer{UpdateOffer: &pb.UpdateOffer{ReleaseId: rel.ID,
		ManifestPayload: rel.ManifestPayload, KeyId: rel.KeyID, Signature: rel.Signature, HealthWindowSeconds: uint32(r.HealthWindowS)}}})
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

// onUpdateReport records an agent's update progress.
func (s *Service) onUpdateReport(ctx context.Context, ident db.AgentIdentity, u *pb.UpdateReport) {
	if u.State == "" {
		return
	}
	if u.Version == "" && u.ReleaseId != "" {
		// Refused before the agent trusted the manifest: the controller
		// knows which version it offered.
		if rel, err := s.st.R.GetRelease(ctx, u.ReleaseId); err == nil {
			u.Version = rel.Version
		}
	}
	s.st.W.SetIdentityUpdate(ctx, db.SetIdentityUpdateParams{UpdateState: u.State, UpdateVersion: u.Version, UpdateDetail: u.Detail,
		UpdateAt: sql.NullInt64{Int64: s.now().UnixMilli(), Valid: true}, ID: ident.ID})
	rolls, _ := s.st.R.ListRollouts(ctx)
	for _, r := range rolls {
		if r.Status != "running" && r.Status != "paused" {
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
			s.setHostState(ctx, r.ID, h.HostID, st, u.Detail)
			if st == "healthy" || st == "rolled_back" || st == "failed" {
				res := audit.Success
				if st != "healthy" {
					res = audit.Failure
				}
				s.audit.Record(ctx, audit.Event{Action: "agent.update", TargetType: "host", TargetID: h.HostID, Result: res, Actor: agentActor(ident),
					Details: map[string]any{"rollout_id": r.ID, "from": h.FromVersion, "to": r.Version, "state": st, "detail": u.Detail}})
				go s.advanceRollouts(context.WithoutCancel(ctx))
			}
		}
	}
}

// --- endpoint migration (DESIGN §6.5) ---

// ProposeEndpoint starts a migration: every connected agent stores and
// tests the candidate; nothing changes on an agent until it confirmed it.
func (s *Service) ProposeEndpoint(ctx context.Context, endpoint, userID string) (string, error) {
	id := ids.New(ids.AgentEndpoint)
	if err := s.st.W.InsertEndpointMigration(ctx, db.InsertEndpointMigrationParams{ID: id, Endpoint: endpoint,
		CreatedBy: sql.NullString{String: userID, Valid: userID != ""}, CreatedAt: s.now().UnixMilli()}); err != nil {
		return "", err
	}
	s.mu.Lock()
	sessions := make([]*Session, 0, len(s.sessions))
	for _, se := range s.sessions {
		sessions = append(sessions, se)
	}
	s.mu.Unlock()
	for _, se := range sessions {
		se.send(&pb.ControllerMessage{Msg: &pb.ControllerMessage_EndpointCandidate{EndpointCandidate: &pb.EndpointCandidate{Id: id, Endpoint: endpoint}}})
	}
	return id, nil
}

func (s *Service) onEndpointTest(ctx context.Context, se *Session, r *pb.EndpointTestResult) {
	s.st.W.UpsertEndpointTest(ctx, db.UpsertEndpointTestParams{MigrationID: r.Id, IdentityID: se.ident.ID, Ok: boolInt(r.Ok), Error: r.Error, TestedAt: s.now().UnixMilli()})
	m, err := s.st.R.GetEndpointMigration(ctx, r.Id)
	if err == nil && m.Status == "committed" && r.Ok {
		s.commitFor(ctx, se, m)
	}
}

func (s *Service) commitFor(ctx context.Context, se *Session, m db.AgentEndpointMigration) {
	se.send(&pb.ControllerMessage{Msg: &pb.ControllerMessage_EndpointCommit{EndpointCommit: &pb.EndpointCommit{Id: m.ID, Endpoint: m.Endpoint}}})
	s.st.W.MarkEndpointTestCommitted(ctx, db.MarkEndpointTestCommittedParams{MigrationID: m.ID, IdentityID: se.ident.ID})
}

// CommitEndpoint makes the candidate primary on every agent that confirmed
// it (now, or when it confirms later). Agents that couldn't reach it keep
// their current endpoint: the change can't strand them.
func (s *Service) CommitEndpoint(ctx context.Context, id string) (int, error) {
	m, err := s.st.R.GetEndpointMigration(ctx, id)
	if err != nil {
		return 0, err
	}
	if m.Status == "cancelled" {
		return 0, errors.New("this migration was cancelled")
	}
	if err := s.st.W.SetEndpointMigrationStatus(ctx, db.SetEndpointMigrationStatusParams{Status: "committed",
		CommittedAt: sql.NullInt64{Int64: s.now().UnixMilli(), Valid: true}, ID: id}); err != nil {
		return 0, err
	}
	m.Status = "committed"
	tests, _ := s.st.R.ListEndpointTests(ctx, id)
	n := 0
	for _, t := range tests {
		if t.Ok == 0 {
			continue
		}
		for _, se := range s.liveSessions(t.IdentityID) {
			s.commitFor(ctx, se, m)
			n++
		}
	}
	return n, nil
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
