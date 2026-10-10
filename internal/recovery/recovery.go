// Package recovery reconciles hosts after a controller restore (DESIGN
// §10.3, ARCHITECTURE-ALPHA §33.1).
//
// drool restore gave the controller a new epoch and set aside everything
// the backup recorded as in progress (internal/backup/reconcile.go). Hosts
// still answer to the backup's epoch: Shepherds refuse every command from
// another epoch, the firewall tool refuses transactions from another
// epoch. This service compares each host with what the restored database
// says, and moves a host to the new epoch only when nothing it has would
// be lost or rolled back: identical state, or state the controller is
// ahead of. A host that is ahead (changes made after the backup), that
// disagrees otherwise, or whose identity can't be trusted waits for an
// administrator. Nothing here creates a job or repeats an operation, and
// a bounded number of hosts is worked on at a time.
package recovery

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"drool/internal/agentrpc/pb"
	"drool/internal/agentsvc"
	"drool/internal/audit"
	"drool/internal/ids"
	"drool/internal/model"
	"drool/internal/store"
	"drool/internal/store/db"
)

// Host states (host_reconciliation.state).
const (
	StatePending      = "pending"
	StateRecovered    = "recovered"
	StateReconciled   = "reconciled"
	StateAhead        = "ahead"
	StateAttention    = "attention"
	StateIncompatible = "incompatible"
	StateBlocked      = "blocked"
	StateUnreachable  = "unreachable"
)

// Per-domain verdicts.
const (
	VerdictSame            = "same"
	VerdictControllerAhead = "controller_ahead"
	VerdictHostAhead       = "host_ahead"
	VerdictConflict        = "conflict"
	VerdictForeignEpoch    = "foreign_epoch"
	VerdictAccepted        = "accepted" // the host's state, accepted by an administrator
)

// Agents is what reconciliation needs from the agent service.
type Agents interface {
	AgentView(hostID string) (agentsvc.AgentView, bool)
	Transition(ctx context.Context, hostID string, t *pb.EpochTransition) (*pb.EpochTransitionResult, error)
	PushConfigIfBehind(ctx context.Context, hostID string)
}

// Firewall is what reconciliation needs from the firewall service.
type Firewall interface {
	SSHStatus(ctx context.Context, hostID string) (map[string]string, error)
	SSHRebase(ctx context.Context, hostID, from string, rev int64, hash string) error
}

// Deps are the service's collaborators.
type Deps struct {
	Store    *store.Store
	Agents   Agents   // nil: the agent endpoint is off
	Firewall Firewall // nil: no firewall service
	Audit    *audit.Writer
	Log      *slog.Logger
	Now      func() time.Time
	// Workers bounds how many hosts are compared at once (default 8).
	Workers int
	// HostChanged tells the UI a host's state changed.
	HostChanged func(hostID string)
}

// Service reconciles hosts with the latest restore.
type Service struct {
	d Deps

	mu        sync.Mutex
	active    *restoreRec
	unsettled map[string]bool // hosts pending or unreachable in the active restore
	queue     []string
	queued    map[string]bool
	inflight  map[string]bool // being worked on: one worker per host
	again     map[string]bool // queued again while in flight: after it
	wake      chan struct{}
	running   int
	peak      int // most hosts worked on at once (tests)
	started   bool
	tick      time.Duration // how often due hosts are queued
	scale     float64       // retry delays are multiplied by this (tests)
}

type restoreRec struct {
	ID, Epoch, PreviousEpoch, Status string
	RestoredAt, BackupCreatedAt      int64
}

// New creates the service; Start begins work.
func New(d Deps) *Service {
	if d.Workers <= 0 {
		d.Workers = 8
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Log == nil {
		d.Log = slog.Default()
	}
	return &Service{d: d, unsettled: map[string]bool{}, queued: map[string]bool{}, inflight: map[string]bool{}, again: map[string]bool{},
		wake: make(chan struct{}, 1),
		tick: 15 * time.Second, scale: 1}
}

func (s *Service) nowMs() int64 { return s.d.Now().UnixMilli() }

// Start loads the latest restore and works on its hosts until ctx ends.
func (s *Service) Start(ctx context.Context) error {
	if err := s.load(ctx); err != nil {
		return err
	}
	s.mu.Lock()
	s.started = true
	s.mu.Unlock()
	for i := 0; i < s.d.Workers; i++ {
		go s.worker(ctx)
	}
	go s.ticker(ctx)
	s.enqueueDue(ctx)
	return nil
}

// load reads the latest restore and the hosts not settled yet.
func (s *Service) load(ctx context.Context) error {
	var r restoreRec
	err := s.d.Store.Reader.QueryRowContext(ctx, `SELECT id, epoch, previous_epoch, status, restored_at, backup_created_at
		FROM controller_restores ORDER BY restored_at DESC, id DESC LIMIT 1`).Scan(&r.ID, &r.Epoch, &r.PreviousEpoch, &r.Status, &r.RestoredAt, &r.BackupCreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	ep, err := agentsvc.ControllerEpoch(ctx, s.d.Store)
	if err != nil {
		return err
	}
	if ep != r.Epoch {
		// Not this controller's restore (its epoch was replaced since):
		// nothing to reconcile against.
		s.d.Log.Error("the latest restore record names another controller epoch; reconciliation is off", "restore", r.ID)
		return nil
	}
	rows, err := s.d.Store.Reader.QueryContext(ctx, `SELECT host_id FROM host_reconciliation WHERE restore_id = ? AND state IN ('pending','unreachable')`, r.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	unsettled := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return err
		}
		unsettled[id] = true
	}
	if err := rows.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	s.active, s.unsettled = &r, unsettled
	s.mu.Unlock()
	if len(unsettled) > 0 {
		s.d.Log.Info("reconciling hosts after a controller restore", "restore", r.ID, "hosts", len(unsettled))
	}
	return nil
}

func (s *Service) activeRestore() *restoreRec {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.active
}

// --- queue ---

func (s *Service) enqueue(hostID string) {
	s.mu.Lock()
	switch {
	case s.inflight[hostID]:
		s.again[hostID] = true
	case !s.queued[hostID]:
		s.queued[hostID] = true
		s.queue = append(s.queue, hostID)
	}
	s.mu.Unlock()
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *Service) next() (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.queue) == 0 {
		return "", false
	}
	id := s.queue[0]
	s.queue = s.queue[1:]
	delete(s.queued, id)
	s.inflight[id] = true
	s.running++
	s.peak = max(s.peak, s.running)
	return id, true
}

func (s *Service) worker(ctx context.Context) {
	for {
		id, ok := s.next()
		if !ok {
			select {
			case <-ctx.Done():
				return
			case <-s.wake:
				continue
			case <-time.After(5 * time.Second):
				continue
			}
		}
		func() {
			defer func() {
				s.mu.Lock()
				s.running--
				delete(s.inflight, id)
				again := s.again[id]
				delete(s.again, id)
				s.mu.Unlock()
				if r := recover(); r != nil {
					s.d.Log.Error("reconciliation panicked", "host", id, "panic", r)
				}
				if again {
					s.enqueue(id)
				}
			}()
			s.process(ctx, id)
		}()
		select {
		case s.wake <- struct{}{}: // more may be queued
		default:
		}
	}
}

func (s *Service) ticker(ctx context.Context) {
	t := time.NewTicker(s.tick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.enqueueDue(ctx)
		}
	}
}

// enqueueDue queues hosts whose next attempt is due: SSH hosts are checked
// from here; Shepherd hosts when their session is connected. At most 64 a
// round, so a large fleet is worked through steadily, never all at once.
func (s *Service) enqueueDue(ctx context.Context) {
	r := s.activeRestore()
	if r == nil {
		return
	}
	s.mu.Lock()
	backlog := len(s.queue)
	s.mu.Unlock()
	if backlog >= 64 {
		return
	}
	rows, err := s.d.Store.Reader.QueryContext(ctx, `SELECT h.host_id FROM host_reconciliation h
		WHERE h.restore_id = ? AND h.state IN ('pending','unreachable') AND h.next_attempt_at <= ?
		ORDER BY h.next_attempt_at, h.host_id LIMIT 256`, r.ID, s.nowMs())
	if err != nil {
		return
	}
	var due []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			due = append(due, id)
		}
	}
	rows.Close()
	// A Shepherd host whose Shepherd isn't connected is recorded as
	// waiting and comes back when it connects (OnSession), or in 30
	// minutes; it is never polled.
	for i, id := range due {
		if i >= 64-backlog {
			break
		}
		s.enqueue(id)
	}
}

// --- hooks (agentsvc.RecoveryHooks) ---

// OnSession: a Shepherd connected; compare it if its host isn't settled.
func (s *Service) OnSession(hostID string) {
	s.mu.Lock()
	want := s.started && s.unsettled[hostID]
	s.mu.Unlock()
	if want {
		s.enqueue(hostID)
	}
}

// MachineChanged: the host's Shepherd identity is presented by another
// machine. Before reconciliation that is never accepted silently.
func (s *Service) MachineChanged(hostID, previous, current string) {
	r := s.activeRestore()
	if r == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	reason := fmt.Sprintf("its Shepherd's identity is now presented by another machine (machine id %s, was %s): a cloned or moved system? "+
		"Check the host; if it is right, accept its state", shortID(current), shortID(previous))
	res, err := s.d.Store.Writer.ExecContext(ctx, `UPDATE host_reconciliation SET state = 'blocked', reason = ?, checked_at = ?
		WHERE restore_id = ? AND host_id = ? AND state IN ('pending','unreachable') AND decided_by = ''`, reason, s.nowMs(), r.ID, hostID)
	if err == nil {
		if n, _ := res.RowsAffected(); n > 0 {
			s.settle(hostID)
			s.d.Log.Warn("restore reconciliation blocked: a Shepherd identity moved to another machine", "host", hostID)
		}
	}
}

const revokedElsewhere = "its Shepherd reports that a controller revoked its identity"

// Revoked: the Shepherd says a controller revoked its identity; this one
// has it as active (the revocation came after the backup). It is not
// moved until an administrator decides: revoke it here too, or accept it.
func (s *Service) Revoked(hostID string, r *pb.Revocation) {
	rs := s.activeRestore()
	if rs == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	when := time.Unix(r.AtUnix, 0).UTC().Format("2 Jan 2006 15:04 MST")
	reason := fmt.Sprintf("%s on %s (%q), after the backup this controller was restored from. Revoke it here too (Shepherds), "+
		"or accept its state if that revocation was a mistake", revokedElsewhere, when, r.Reason)
	res, err := s.d.Store.Writer.ExecContext(ctx, `UPDATE host_reconciliation SET state = 'blocked', reason = ?, checked_at = ?
		WHERE restore_id = ? AND host_id = ? AND state IN ('pending','unreachable') AND decided_by = ''`, reason, s.nowMs(), rs.ID, hostID)
	if err == nil {
		if n, _ := res.RowsAffected(); n > 0 {
			s.settle(hostID)
			s.d.Log.Warn("restore reconciliation blocked: a Shepherd reports its identity was revoked after the backup", "host", hostID)
			if s.d.Audit != nil {
				s.d.Audit.Record(ctx, audit.Event{Action: "restore.reconcile", Actor: &audit.System, TargetType: "host", TargetID: hostID,
					Result: audit.Failure, Details: map[string]any{"restore_id": rs.ID, "state": StateBlocked, "reason": reason}})
			}
		}
	}
}

func shortID(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}

func (s *Service) settle(hostID string) {
	s.mu.Lock()
	delete(s.unsettled, hostID)
	s.mu.Unlock()
	if s.d.HostChanged != nil {
		s.d.HostChanged(hostID)
	}
}

// --- comparison ---

// DomainCheck is one domain's comparison.
type DomainCheck struct {
	Domain             string `json:"domain"`
	Verdict            string `json:"verdict"`
	ControllerEpoch    string `json:"controller_epoch,omitempty"`
	ControllerRevision int64  `json:"controller_revision"`
	ControllerHash     string `json:"controller_hash,omitempty"`
	HostEpoch          string `json:"host_epoch,omitempty"`
	HostRevision       int64  `json:"host_revision"`
	HostHash           string `json:"host_hash,omitempty"`
}

func normHash(h string) string { return strings.TrimPrefix(h, "sha256:") }

// Compare decides one domain: ctl is the controller's record (nil: none),
// host what the host has; prev is the backup's epoch, next the restored
// controller's. accepted: an administrator accepted the host's state.
func Compare(domain string, ctl *db.StateVersion, hostEpoch string, hostRev int64, hostHash, prev, next string, accepted bool) DomainCheck {
	c := DomainCheck{Domain: domain, HostEpoch: hostEpoch, HostRevision: hostRev, HostHash: hostHash}
	ctlRev, ctlHash := int64(0), ""
	if ctl != nil {
		c.ControllerEpoch, c.ControllerRevision, c.ControllerHash = ctl.Epoch, ctl.Revision, ctl.ContentHash
		ctlRev, ctlHash = ctl.Revision, ctl.ContentHash
	}
	switch {
	case hostRev == 0:
		c.Verdict = VerdictSame
		if ctlRev > 0 {
			c.Verdict = VerdictControllerAhead
		}
		return c
	case hostEpoch != prev && hostEpoch != next:
		c.Verdict = VerdictForeignEpoch
	case hostRev > ctlRev:
		c.Verdict = VerdictHostAhead
	case hostRev < ctlRev:
		c.Verdict = VerdictControllerAhead
		return c
	case normHash(hostHash) != normHash(ctlHash):
		c.Verdict = VerdictConflict
	default:
		c.Verdict = VerdictSame
		return c
	}
	if accepted {
		c.Verdict = VerdictAccepted
	}
	return c
}

// domainNames are the domains as the UI names them.
var domainNames = map[string]string{"agent-config": "Shepherd configuration", "files": "file deployment"}

func domainList(domains []string) string {
	out := make([]string, len(domains))
	for i, d := range domains {
		if n, ok := domainNames[d]; ok {
			d = n
		}
		out[i] = d
	}
	return strings.Join(out, ", ")
}

// verdictState is the host state for a set of domain verdicts ("" when
// the host can be moved).
func verdictState(checks []DomainCheck) (string, string) {
	var ahead, other []string
	for _, c := range checks {
		switch c.Verdict {
		case VerdictHostAhead:
			ahead = append(ahead, c.Domain)
		case VerdictConflict, VerdictForeignEpoch:
			other = append(other, c.Domain)
		}
	}
	switch {
	case len(other) > 0:
		return StateAttention, "the host's " + domainList(other) + " state disagrees with the restored controller's (same revision with other content, or another controller epoch): review it, then accept the host's state or re-enroll"
	case len(ahead) > 0:
		return StateAhead, "the host has " + domainList(ahead) + " changes made after the backup was taken: accept the host's state (nothing on the host changes; DROOL's own configuration then shows where it differs)"
	}
	return "", ""
}

// --- processing ---

type hostRec struct {
	State, Reason, DecidedBy string
	Attempts                 int64
}

func (s *Service) loadRec(ctx context.Context, restoreID, hostID string) (*hostRec, error) {
	var h hostRec
	err := s.d.Store.Reader.QueryRowContext(ctx, `SELECT state, reason, decided_by, attempts FROM host_reconciliation WHERE restore_id = ? AND host_id = ?`,
		restoreID, hostID).Scan(&h.State, &h.Reason, &h.DecidedBy, &h.Attempts)
	if err != nil {
		return nil, err
	}
	return &h, nil
}

// outcome is what one attempt decided.
type outcome struct {
	state, reason string
	checks        []DomainCheck
	retryIn       time.Duration // pending/unreachable: when to try again (0: on the next connection only)
}

func (s *Service) process(ctx context.Context, hostID string) {
	r := s.activeRestore()
	if r == nil {
		return
	}
	rec, err := s.loadRec(ctx, r.ID, hostID)
	if err != nil {
		return
	}
	if rec.State != StatePending && rec.State != StateUnreachable {
		return
	}
	h, err := s.d.Store.R.GetHost(ctx, hostID)
	if err != nil {
		return
	}
	accepted := rec.DecidedBy != ""
	var o outcome
	if ident, err := s.d.Store.R.GetLiveIdentityForHost(ctx, hostID); err == nil {
		o = s.agentHostCheck(ctx, r, h, ident, accepted)
	} else if h.SshConfigured != 0 {
		o = s.sshHostCheck(ctx, r, h, accepted)
	} else if revoked := s.revokedOnly(ctx, hostID); revoked {
		o = outcome{state: StateBlocked, reason: "its Shepherd's identity is revoked: enroll the Shepherd again (or give the host SSH access)"}
	} else {
		o = outcome{state: StateRecovered, reason: "no management path to compare"}
	}
	s.record(ctx, r, hostID, rec, o)
}

func (s *Service) revokedOnly(ctx context.Context, hostID string) bool {
	ids, err := s.d.Store.R.ListIdentitiesForHost(ctx, hostID)
	return err == nil && len(ids) > 0
}

// record stores an attempt's outcome.
func (s *Service) record(ctx context.Context, r *restoreRec, hostID string, rec *hostRec, o outcome) {
	now := s.nowMs()
	attempts := rec.Attempts + 1
	next := int64(0)
	if o.state == StatePending || o.state == StateUnreachable {
		wait := o.retryIn
		if wait == 0 {
			wait = 30 * time.Minute // Shepherd hosts: on their next connection, or then
		}
		next = now + int64(float64(wait.Milliseconds())*s.scale)
	}
	checks := o.checks
	if checks == nil {
		checks = []DomainCheck{}
	}
	js, _ := json.Marshal(checks)
	reconciledAt := sql.NullInt64{}
	if o.state == StateReconciled || o.state == StateRecovered {
		reconciledAt = sql.NullInt64{Int64: now, Valid: true}
	}
	// Only over what this attempt started from: a hook may have blocked
	// the host meanwhile (machine changed, revoked elsewhere).
	res, err := s.d.Store.Writer.ExecContext(ctx, `UPDATE host_reconciliation SET state = ?, reason = ?, domains_json = ?, checked_at = ?,
		reconciled_at = coalesce(?, reconciled_at), attempts = ?, next_attempt_at = ?
		WHERE restore_id = ? AND host_id = ? AND state = ? AND decided_by = ?`,
		o.state, o.reason, string(js), now, reconciledAt, attempts, next, r.ID, hostID, rec.State, rec.DecidedBy)
	if err != nil {
		s.d.Log.Warn("record reconciliation", "host", hostID, "err", err)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return
	}
	switch o.state {
	case StatePending, StateUnreachable:
		s.mu.Lock()
		s.unsettled[hostID] = true
		s.mu.Unlock()
	default:
		s.settle(hostID)
		res := audit.Success
		if o.state != StateReconciled && o.state != StateRecovered {
			res = audit.Failure
		}
		if s.d.Audit != nil {
			s.d.Audit.Record(ctx, audit.Event{Action: "restore.reconcile", Actor: &audit.System, TargetType: "host", TargetID: hostID, Result: res,
				Details: map[string]any{"restore_id": r.ID, "state": o.state, "reason": o.reason, "domains": checks}})
		}
		s.maybeFinish(ctx, r)
	}
	if s.d.HostChanged != nil {
		s.d.HostChanged(hostID)
	}
}

// maybeFinish closes the restore once every host is settled for good.
func (s *Service) maybeFinish(ctx context.Context, r *restoreRec) {
	var open int
	if err := s.d.Store.Reader.QueryRowContext(ctx, `SELECT count(*) FROM host_reconciliation WHERE restore_id = ? AND state NOT IN ('reconciled','recovered')`,
		r.ID).Scan(&open); err != nil || open > 0 {
		return
	}
	res, err := s.d.Store.Writer.ExecContext(ctx, `UPDATE controller_restores SET status = 'finished', finished_at = ?, finished_by = 'every host reconciled'
		WHERE id = ? AND status = 'reconciling'`, s.nowMs(), r.ID)
	if err != nil {
		return
	}
	if n, _ := res.RowsAffected(); n > 0 {
		s.mu.Lock()
		if s.active != nil && s.active.ID == r.ID {
			s.active.Status = "finished"
		}
		s.mu.Unlock()
		s.d.Log.Info("restore reconciliation finished: every host reconciled", "restore", r.ID)
		if s.d.Audit != nil {
			s.d.Audit.Record(ctx, audit.Event{Action: "restore.finish", Actor: &audit.System, TargetType: "controller",
				Details: map[string]any{"restore_id": r.ID, "by": "every host reconciled"}})
		}
	}
}

// controllerDomains is the restored controller's record of a host.
func (s *Service) controllerDomains(ctx context.Context, hostID string) (map[string]*db.StateVersion, error) {
	list, err := s.d.Store.R.ListStateVersions(ctx, hostID)
	if err != nil {
		return nil, err
	}
	out := map[string]*db.StateVersion{}
	for i := range list {
		out[list[i].Domain] = &list[i]
	}
	return out, nil
}

// fwStatus extracts the firewall tool's status from an agent's report.
func fwStatus(report []byte) map[string]string {
	var rep struct {
		Status map[string]string `json:"status"`
	}
	json.Unmarshal(report, &rep)
	return rep.Status
}

func fwInstalled(st map[string]string) bool {
	v := st["tool_version"]
	return v != "" && v != "none"
}

func fwCommitted(st map[string]string) (string, int64, string) {
	if !fwInstalled(st) {
		return "", 0, ""
	}
	rev, _ := strconv.ParseInt(st["committed_revision"], 10, 64)
	return st["committed_epoch"], rev, st["committed_hash"]
}

func (s *Service) agentHostCheck(ctx context.Context, r *restoreRec, h db.Host, ident db.AgentIdentity, accepted bool) outcome {
	if ident.Status == "quarantined" {
		return outcome{state: StateBlocked, reason: "identity conflict: two machines present this host's Shepherd identity; resolve the conflict on the host page first"}
	}
	if s.d.Agents == nil {
		return outcome{state: StatePending, reason: "the agent endpoint is off on this controller"}
	}
	v, ok := s.d.Agents.AgentView(h.ID)
	switch {
	case !ok:
		return outcome{state: StatePending, reason: "waiting for its Shepherd to connect"}
	case v.Sessions > 1 || v.IdentityStatus == "quarantined":
		return outcome{state: StateBlocked, reason: "identity conflict: more than one machine presents this host's Shepherd identity"}
	case !v.Compatible:
		return outcome{state: StateIncompatible, reason: "its Shepherd's protocol version is incompatible with this controller"}
	case !v.HasFeature("epoch-1"):
		return outcome{state: StateIncompatible, reason: fmt.Sprintf("its Shepherd (version %s) can't move to a new controller epoch; "+
			"update it (Shepherds → Updates), and it is reconciled when it connects again", orDash(v.AgentVersion))}
	}
	ctl, err := s.controllerDomains(ctx, h.ID)
	if err != nil {
		return outcome{state: StateUnreachable, reason: err.Error(), retryIn: time.Minute}
	}
	// The epoch the agent answers to: the backup's, already this one, or
	// (only after an administrator accepted it) another.
	if v.Epoch != r.PreviousEpoch && v.Epoch != r.Epoch && !accepted {
		return outcome{state: StateAttention, reason: fmt.Sprintf("its Shepherd answers to controller epoch %s, which is neither the backup's (%s) nor this controller's: "+
			"another DROOL controller may have managed it since the backup (a second restore, or a copy running elsewhere). "+
			"Make sure only this controller manages it, then accept its state", short(v.Epoch), short(r.PreviousEpoch))}
	}
	var checks []DomainCheck
	t := &pb.EpochTransition{TransitionId: ids.New(ids.Transition), FromEpoch: v.Epoch, ToEpoch: r.Epoch}
	seen := map[string]bool{}
	for _, d := range v.Domains {
		seen[d.Domain] = true
		c := Compare(d.Domain, ctl[d.Domain], d.Epoch, int64(d.Revision), d.ContentHash, r.PreviousEpoch, r.Epoch, accepted)
		checks = append(checks, c)
		t.Domains = append(t.Domains, &pb.EpochDomain{Domain: d.Domain, ExpectEpoch: d.Epoch, ExpectRevision: d.Revision, ExpectHash: d.ContentHash})
	}
	fw := fwStatus(v.FirewallJSON)
	fwEpoch, fwRev, fwHash := fwCommitted(fw)
	if fw["journal_state"] == "pending" {
		return outcome{state: StatePending, reason: "a firewall transaction is pending on the host; it settles by itself (commit or automatic rollback), then the host is compared",
			retryIn: time.Minute}
	}
	if fwRev > 0 || ctl["firewall"] != nil {
		seen["firewall"] = true
		checks = append(checks, Compare("firewall", ctl["firewall"], fwEpoch, fwRev, fwHash, r.PreviousEpoch, r.Epoch, accepted))
		if fwRev > 0 {
			t.Domains = append(t.Domains, &pb.EpochDomain{Domain: "firewall", ExpectEpoch: fwEpoch, ExpectRevision: uint64(fwRev), ExpectHash: fwHash})
		}
	}
	for name, c := range ctl {
		if !seen[name] {
			checks = append(checks, Compare(name, c, "", 0, "", r.PreviousEpoch, r.Epoch, accepted))
		}
	}
	if st, why := verdictState(checks); st != "" {
		return outcome{state: st, reason: why, checks: checks}
	}

	tctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	res, err := s.d.Agents.Transition(tctx, h.ID, t)
	cancel()
	if err != nil {
		// Unknown whether it arrived: the transition is answered the same
		// way again, so it is simply sent again on the next connection.
		return outcome{state: StatePending, reason: "the epoch transition got no answer (" + err.Error() + "); sent again when the Shepherd reconnects",
			checks: checks, retryIn: 2 * time.Minute}
	}
	if !res.Ok {
		switch res.Code {
		case "pending":
			return outcome{state: StatePending, reason: res.Error, checks: checks, retryIn: time.Minute}
		case "changed", "epoch":
			// The agent's state moved since its report: compare again
			// with what it answered, a few times.
			if s.attempts(ctx, r, h.ID) < 5 {
				return outcome{state: StatePending, reason: "the Shepherd's state changed while it was compared: " + res.Error, checks: checks, retryIn: 10 * time.Second}
			}
			return outcome{state: StateAttention, reason: "the Shepherd's state kept changing while it was compared: " + res.Error, checks: checks}
		}
		return outcome{state: StateUnreachable, reason: "the Shepherd couldn't move to the new epoch: " + res.Error, checks: checks,
			retryIn: backoff(s.attempts(ctx, r, h.ID))}
	}
	// Moved: record every domain in the new epoch.
	err = s.d.Store.InTx(ctx, func(q *db.Queries) error {
		for _, c := range checks {
			if err := moveDomain(ctx, q, h.ID, c, r.Epoch, s.nowMs()); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return outcome{state: StateUnreachable, reason: "the Shepherd moved, but recording it failed: " + err.Error(), checks: checks, retryIn: time.Minute}
	}
	s.d.Agents.PushConfigIfBehind(ctx, h.ID)
	why := ""
	if v.Epoch == r.Epoch {
		why = "its Shepherd had already moved to this controller's epoch"
	}
	return outcome{state: StateReconciled, reason: why, checks: checks}
}

func (s *Service) attempts(ctx context.Context, r *restoreRec, hostID string) int64 {
	var n int64
	s.d.Store.Reader.QueryRowContext(ctx, `SELECT attempts FROM host_reconciliation WHERE restore_id = ? AND host_id = ?`, r.ID, hostID).Scan(&n)
	return n
}

func backoff(attempts int64) time.Duration {
	d := 30 * time.Second
	for i := int64(0); i < attempts && d < 30*time.Minute; i++ {
		d *= 2
	}
	return min(d, 30*time.Minute)
}

func (s *Service) sshHostCheck(ctx context.Context, r *restoreRec, h db.Host, accepted bool) outcome {
	if s.d.Firewall == nil {
		return outcome{state: StateRecovered, reason: "no firewall service; nothing versioned to compare"}
	}
	ctl, err := s.controllerDomains(ctx, h.ID)
	if err != nil {
		return outcome{state: StateUnreachable, reason: err.Error(), retryIn: time.Minute}
	}
	st, err := s.d.Firewall.SSHStatus(ctx, h.ID)
	if err != nil {
		switch model.ClassOf(err) {
		case model.ClassHostKey:
			return outcome{state: StateBlocked, reason: "the host's SSH host key differs from the one DROOL trusts (changed after the backup?): " +
				"check it, accept the new key on the host page if it is right, then check again"}
		case model.ClassAuthentication:
			return outcome{state: StateAttention, reason: "the host refused DROOL's SSH credential (the key or the SSH certificate authority changed after the backup?): " + err.Error()}
		}
		return outcome{state: StateUnreachable, reason: "couldn't connect: " + err.Error(), retryIn: backoff(s.attempts(ctx, r, h.ID))}
	}
	if st["journal_state"] == "pending" {
		return outcome{state: StatePending, reason: "a firewall transaction is pending on the host; it settles by itself (commit or automatic rollback), then the host is compared",
			retryIn: time.Minute}
	}
	fwEpoch, fwRev, fwHash := fwCommitted(st)
	if fwRev == 0 && ctl["firewall"] == nil {
		return outcome{state: StateRecovered, reason: ""}
	}
	c := Compare("firewall", ctl["firewall"], fwEpoch, fwRev, fwHash, r.PreviousEpoch, r.Epoch, accepted)
	checks := []DomainCheck{c}
	if st, why := verdictState(checks); st != "" {
		return outcome{state: st, reason: why, checks: checks}
	}
	if fwRev > 0 {
		if err := s.d.Firewall.SSHRebase(ctx, h.ID, fwEpoch, fwRev, fwHash); err != nil {
			if model.ClassOf(err) == model.ClassValidation {
				return outcome{state: StatePending, reason: "the firewall's committed policy changed while it was compared: " + err.Error(), checks: checks, retryIn: 30 * time.Second}
			}
			return outcome{state: StateUnreachable, reason: "couldn't move the firewall to the new epoch: " + err.Error(), checks: checks,
				retryIn: backoff(s.attempts(ctx, r, h.ID))}
		}
	}
	if err := s.d.Store.InTx(ctx, func(q *db.Queries) error { return moveDomain(ctx, q, h.ID, c, r.Epoch, s.nowMs()) }); err != nil {
		return outcome{state: StateUnreachable, reason: "the firewall moved, but recording it failed: " + err.Error(), checks: checks, retryIn: time.Minute}
	}
	return outcome{state: StateReconciled, checks: checks}
}

// moveDomain records a domain in the new epoch after the host moved: the
// controller keeps its own revision and intent when it is ahead (or
// equal), and takes the host's only when an administrator accepted it.
// The host's report becomes current; agents never advance the
// controller's counters by themselves.
func moveDomain(ctx context.Context, q *db.Queries, hostID string, c DomainCheck, epoch string, now int64) error {
	rev, hash := c.ControllerRevision, c.ControllerHash
	if c.Verdict == VerdictAccepted {
		rev = max(rev, c.HostRevision)
		hash = c.HostHash
		if c.Domain == "firewall" {
			hash = "sha256:" + normHash(c.HostHash)
		}
	}
	if rev == 0 && hash == "" && c.HostRevision == 0 {
		return nil // nothing on either side
	}
	if err := q.UpsertStateVersion(ctx, db.UpsertStateVersionParams{HostID: hostID, Domain: c.Domain, Epoch: epoch, Revision: rev,
		ContentHash: hash, UpdatedAt: now}); err != nil {
		return err
	}
	return q.ReportStateVersion(ctx, db.ReportStateVersionParams{ReportedEpoch: epoch, ReportedRevision: c.HostRevision, ReportedHash: c.HostHash,
		ReportedAt: sql.NullInt64{Int64: now, Valid: true}, Status: "ok", HostID: hostID, Domain: c.Domain})
}

func short(e string) string {
	if e == "" {
		return "(none)"
	}
	if len(e) > 8 {
		return e[:8]
	}
	return e
}

func orDash(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}
