package fwsvc

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"drool/internal/audit"
	"drool/internal/firewall/compile"
	"drool/internal/firewall/hostfw"
	"drool/internal/firewall/nftables"
	"drool/internal/model"
	"drool/internal/netmon"
	"drool/internal/store/db"
)

// --- agent hooks (agentsvc.FirewallHooks) ---

// OnSession runs when an agent connects: record its endpoints and tooling
// status, resolve open transactions (a rollback is reported on the next
// connection), and turn learning on or off.
func (s *Service) OnSession(hostID string, firewallJSON []byte, features []string, connectedAt time.Time) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	var rep fwReport
	if json.Unmarshal(firewallJSON, &rep) == nil {
		s.recordAgentEndpoints(ctx, hostID, rep)
		if len(rep.Status) > 0 {
			s.recordStatus(ctx, hostID, rep.Status)
		}
	}
	s.pushLearning(ctx, hostID)
	if h, err := s.d.Store.R.GetHost(ctx, hostID); err == nil {
		// Never while a transaction of ours is running on this host: it
		// waits for exactly this session itself.
		s.mu.Lock()
		_, busy := s.pending[hostID]
		s.mu.Unlock()
		if !busy {
			s.resolveFromStatus(ctx, h, rep.Status)
		}
	}
	s.Kick()
}

// OnTelemetry ingests agent firewall status and flow observations.
func (s *Service) OnTelemetry(hostID, kind string, payload []byte) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	switch kind {
	case "firewall":
		var rep fwReport
		if json.Unmarshal(payload, &rep) == nil {
			s.recordAgentEndpoints(ctx, hostID, rep)
			s.recordStatus(ctx, hostID, rep.Status)
		}
	case "flows": // M8 agents
		s.ingestFlows(ctx, hostID, payload)
	case "netflows": // M10 agents
		s.ingestReport(ctx, hostID, payload)
	}
}

// UpdateBlocked keeps agent updates away from pending transactions.
func (s *Service) UpdateBlocked(hostID string) string {
	return s.PendingReason(context.Background(), hostID)
}

func (s *Service) pushLearning(ctx context.Context, hostID string) {
	if s.d.Agents == nil {
		return
	}
	c := s.HostConfig(ctx, hostID)
	s.d.Agents.SendFirewallConfig(hostID, netmon.LoadSettings(ctx, s.d.Store.R).Collects(c.Mode), 30)
}

// PushCollection tells every connected agent whether to collect
// connections (after the network settings changed).
func (s *Service) PushCollection(ctx context.Context) {
	if s.d.Agents == nil {
		return
	}
	hosts, err := s.d.Store.R.ListHosts(ctx)
	if err != nil {
		return
	}
	for _, h := range hosts {
		if _, ok := s.d.Agents.SessionFor(h.ID); ok {
			s.pushLearning(ctx, h.ID)
		}
	}
}

// recordStatus stores the tooling status and derives drift.
func (s *Service) recordStatus(ctx context.Context, hostID string, st map[string]string) {
	if st["tool_version"] == "" || st["tool_version"] == "none" {
		return
	}
	s.state(ctx, hostID)
	prev, _ := s.d.Store.R.GetFwHostState(ctx, hostID)
	if staleStatus(st, prev.StatusJson) {
		return
	}
	drift := ""
	expected, live := st["expected_live"], st["live_hash"]
	switch {
	case st["journal_state"] == "pending":
		// A transaction is open on the host: its candidate is live and not
		// committed yet. That's not drift; the transaction settles it.
		drift = prev.Drift
	case expected != "" && expected != "none" && live == "none":
		drift = "missing"
	case expected != "" && live != expected:
		drift = "drift"
	case expected == "" && live != "" && live != "none":
		drift = "drift"
	}
	b, _ := json.Marshal(st)
	now := s.now().UnixMilli()
	s.d.Store.W.SetFwHostStatus(ctx, db.SetFwHostStatusParams{StatusJson: string(b), StatusAt: sql.NullInt64{Int64: now, Valid: true},
		Drift: drift, UpdatedAt: now, HostID: hostID})
	if drift != "" && prev.Drift == "" {
		s.log.Warn("firewall drift detected", "host", hostID, "kind", drift)
		s.audit(ctx, "firewall.drift", hostID, audit.Failure, map[string]any{"kind": drift})
	}
	// A restored controller: the host is ahead (DESIGN §10.3).
	crev, _ := strconv.ParseInt(st["committed_revision"], 10, 64)
	if crev > 0 {
		cur, err := s.d.Store.R.GetStateVersion(ctx, db.GetStateVersionParams{HostID: hostID, Domain: "firewall"})
		switch {
		case st["committed_epoch"] != "" && st["committed_epoch"] != s.epoch:
			s.markState(ctx, hostID, "epoch_mismatch", st["committed_epoch"], crev, st["committed_hash"])
		case err != nil || crev > cur.Revision:
			s.markState(ctx, hostID, "host_ahead", s.epoch, crev, st["committed_hash"])
		}
	}
}

// staleStatus reports whether status st was collected before the recorded
// one (prevJSON). Reports can arrive out of order: the hello of the session
// a transaction verifies over carries a status collected before the commit,
// and can be handled after the commit's own report. By the host's clock
// (seconds) within two minutes, then, in the same second, by the committed
// revision and whether a transaction was still open. A clock set back
// further, or a host restored to an older revision, reports as new.
func staleStatus(st map[string]string, prevJSON string) bool {
	var prev map[string]string
	if prevJSON == "" || json.Unmarshal([]byte(prevJSON), &prev) != nil {
		return false
	}
	num := func(m map[string]string, k string) int64 {
		n, _ := strconv.ParseInt(m[k], 10, 64)
		return n
	}
	at, prevAt := num(st, "now"), num(prev, "now")
	if at == 0 || prevAt == 0 {
		return false
	}
	switch {
	case prevAt > at:
		return prevAt-at <= 120
	case prevAt < at:
		return false
	}
	rev, prevRev := num(st, "committed_revision"), num(prev, "committed_revision")
	return rev < prevRev || (rev == prevRev && st["journal_state"] == "pending" && prev["journal_state"] != "pending")
}

// resolveHost settles transactions whose outcome DROOL doesn't know yet.
func (s *Service) resolveHost(ctx context.Context, h db.Host) {
	tx := s.openTx(ctx, h.ID)
	if tx == nil {
		return
	}
	st, err := s.statusFresh(ctx, h, time.Time{})
	if err != nil {
		return
	}
	s.resolveFromStatus(ctx, h, st)
}

func (s *Service) resolveFromStatus(ctx context.Context, h db.Host, st map[string]string) {
	tx := s.openTx(ctx, h.ID)
	if tx == nil || len(st) == 0 {
		return
	}
	t := &txRec{s: s, row: *tx}
	json.Unmarshal([]byte(tx.StepsJson), &t.steps)
	deadlinePassed := tx.Deadline.Valid && s.now().After(time.UnixMilli(tx.Deadline.Int64))
	if st["transaction_id"] != tx.ID {
		// The host never journaled this transaction: nothing was applied.
		if tx.State == TxPreparing || deadlinePassed {
			t.finish(TxFailed, "the host never received this transaction; nothing changed", model.ClassConnection)
		}
		return
	}
	switch st["journal_state"] {
	case "committed":
		hp := &compile.HostPolicy{}
		json.Unmarshal([]byte(tx.PolicyJson), hp)
		s.committed(ctx, t, h, hp, nftables.Rendered{FullHash: tx.CandidateHash, RulesHash: tx.RulesHash, SetsHash: tx.SetsHash},
			&model.FirewallApplyOp{Remove: tx.Kind == "remove"}, 0)
	case "aborted":
		t.finish(TxFailed, "the host aborted before applying: "+st["reason"], model.ClassConfiguration)
	case "rolled_back":
		t.finish(TxRolledBack, "reported by the host: "+st["reason"], model.ClassConnection)
		s.audit(ctx, "firewall.transaction.rollback", h.ID, audit.Failure, map[string]any{"transaction_id": tx.ID, "reason": st["reason"]})
	}
}

// --- converger ---

// Run converges hosts until ctx ends: open transactions, set-element
// propagation (DESIGN §5.3.1) and drift checks for SSH hosts.
func (s *Service) Run(ctx context.Context) {
	tick := time.NewTicker(15 * time.Second)
	defer tick.Stop()
	var lastDrift time.Time
	for {
		s.converge(ctx, s.now().Sub(lastDrift) > 5*time.Minute)
		if s.now().Sub(lastDrift) > 5*time.Minute {
			lastDrift = s.now()
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		case <-s.kick:
		}
	}
}

func (s *Service) converge(ctx context.Context, checkDrift bool) {
	states, err := s.d.Store.R.ListFwHostStates(ctx)
	if err != nil {
		return
	}
	var w *compile.World
	for _, st := range states {
		if ctx.Err() != nil {
			return
		}
		h, err := s.d.Store.R.GetHost(ctx, st.HostID)
		if err != nil {
			continue
		}
		if tx := s.openTx(ctx, h.ID); tx != nil {
			s.mu.Lock()
			_, busy := s.pending[h.ID]
			s.mu.Unlock()
			if !busy && tx.Deadline.Valid && s.now().After(time.UnixMilli(tx.Deadline.Int64)) {
				s.resolveHost(ctx, h)
			}
			continue
		}
		if st.Enforced == 0 {
			continue
		}
		if w == nil {
			if w, err = s.World(ctx); err != nil {
				return
			}
		}
		s.convergeSets(ctx, w, h, st)
		if checkDrift && h.ManagementMethod != "agent" {
			s.checkDrift(ctx, h)
		}
	}
}

func (s *Service) checkDrift(ctx context.Context, h db.Host) {
	c, _, err := s.openConn(ctx, h)
	if err != nil {
		return
	}
	defer c.Close()
	res, err := c.Run(ctx, hostfw.Action{Kind: hostfw.Status})
	if err == nil {
		s.recordStatus(ctx, h.ID, res.Values)
	}
}

// convergeSets pushes set-element changes (addresses joined or left a
// group) to an enforced host. Rules don't change, so no dead-man
// transaction is needed (DESIGN §15.4) — except where an emergency
// override removed management protection: there any change goes through
// a full transaction.
func (s *Service) convergeSets(ctx context.Context, w *compile.World, h db.Host, st db.FwHostState) {
	if st.OverrideMgmt != 0 || s.HostConfig(ctx, h.ID).Mode != ModeEnforce {
		return
	}
	hp, _ := s.compileHost(ctx, w, h, false)
	rend := nftables.Render(hp)
	if rend.RulesHash != st.AppliedRulesHash {
		return // policy changed: an administrator applies it (preview, transaction)
	}
	if rend.SetsHash == st.AppliedSetsHash {
		return
	}
	// Conservative near management paths: if the membership or address
	// change makes a deny now overlap a management path, hold it for review
	// instead of pushing it unattended. (Management accepts still come
	// first, so the host stays reachable; but the change deserves a look.)
	if len(hp.Conflicts) > 0 {
		msg := "held: this address or membership change makes a policy conflict with DROOL's management path (" + hp.Conflicts[0].Message +
			"); review it in the Firewall Designer and apply it explicitly"
		if st.SetsError != msg {
			s.setsError(ctx, h.ID, msg)
			s.log.Warn("firewall set update held near the management path", "host", h.Name, "conflict", hp.Conflicts[0].Message)
		}
		return
	}
	lk := s.hostLock(h.ID)
	if !lk.TryLock() {
		return
	}
	defer lk.Unlock()
	gen := st.SetsGeneration
	if gen <= st.SetsGenerationApplied {
		gen = st.SetsGenerationApplied + 1
		s.d.Store.W.SetFwHostSetsDesired(ctx, db.SetFwHostSetsDesiredParams{SetsGeneration: gen, UpdatedAt: s.now().UnixMilli(), HostID: h.ID})
	}
	var old *compile.HostPolicy
	if st.AppliedPolicyJson != "" {
		old = &compile.HostPolicy{}
		json.Unmarshal([]byte(st.AppliedPolicyJson), old)
	}
	delta := nftables.SetDelta(old, hp)
	if delta == "" {
		delta = nftables.SetDelta(nil, hp)
	}
	c, _, err := s.openConn(ctx, h)
	if err != nil {
		s.setsError(ctx, h.ID, "no management path: "+model.AsOpError(err).Message)
		return
	}
	defer c.Close()
	res, err := c.Run(ctx, hostfw.Action{Kind: hostfw.Sets, RulesHash: rend.RulesHash, SetsGeneration: gen, Hash: rend.FullHash,
		Delta: delta, Full: rend.Full})
	if err != nil {
		s.setsError(ctx, h.ID, model.AsOpError(err).Message)
		return
	}
	if res.ErrCode != "" {
		s.setsError(ctx, h.ID, res.ErrCode+": "+res.ErrMsg)
		return
	}
	pj, _ := json.Marshal(hp)
	now := s.now().UnixMilli()
	s.d.Store.W.SetFwHostSetsApplied(ctx, db.SetFwHostSetsAppliedParams{SetsGenerationApplied: gen, AppliedSetsHash: rend.SetsHash,
		AppliedFullHash: rend.FullHash, AppliedPolicyJson: string(pj), SetsAt: sql.NullInt64{Int64: now, Valid: true}, UpdatedAt: now, HostID: h.ID})
	s.log.Info("firewall address sets updated", "host", h.Name, "generation", gen, "transport", c.Transport())
	s.audit(ctx, "firewall.sets.update", h.ID, audit.Success, map[string]any{"generation": gen, "transport": c.Transport()})
}

func (s *Service) setsError(ctx context.Context, hostID, msg string) {
	now := s.now().UnixMilli()
	s.d.Store.W.SetFwHostSetsError(ctx, db.SetFwHostSetsErrorParams{SetsError: msg, SetsAt: sql.NullInt64{Int64: now, Valid: true}, UpdatedAt: now, HostID: hostID})
}

// Propagation summarises set convergence across enforced hosts
// ("Policy propagation incomplete: 17/40 peers current").
type Propagation struct {
	Enforced int      `json:"enforced"`
	Current  int      `json:"current"`
	Stale    []string `json:"stale"` // host names
	Changed  []string `json:"rules_changed"`
}

// Propagation computes the summary.
func (s *Service) Propagation(ctx context.Context) (Propagation, error) {
	p := Propagation{Stale: []string{}, Changed: []string{}}
	states, err := s.d.Store.R.ListFwHostStates(ctx)
	if err != nil {
		return p, err
	}
	w, err := s.World(ctx)
	if err != nil {
		return p, err
	}
	for _, st := range states {
		if st.Enforced == 0 {
			continue
		}
		h, err := s.d.Store.R.GetHost(ctx, st.HostID)
		if err != nil {
			continue
		}
		p.Enforced++
		hp, _ := s.compileHost(ctx, w, h, false)
		rend := nftables.Render(hp)
		switch {
		case rend.RulesHash != st.AppliedRulesHash:
			p.Changed = append(p.Changed, h.Name)
		case rend.SetsHash != st.AppliedSetsHash:
			p.Stale = append(p.Stale, h.Name)
		default:
			p.Current++
		}
	}
	return p, nil
}

// Detect inspects a host's firewall environment (read-only; works on
// every host, supported or not).
func (s *Service) Detect(ctx context.Context, hostID string) (*nftables.Env, nftables.Eligibility, error) {
	h, err := s.d.Store.R.GetHost(ctx, hostID)
	if err != nil {
		return nil, nftables.Eligibility{}, ErrNotFound
	}
	c, _, err := s.openConn(ctx, h)
	if err != nil {
		return nil, nftables.Eligibility{}, err
	}
	defer c.Close()
	det, err := c.Run(ctx, hostfw.Action{Kind: hostfw.Detect})
	if err != nil {
		return nil, nftables.Eligibility{}, err
	}
	env, el := s.storeEnv(ctx, hostID, det)
	if st, err := c.Run(ctx, hostfw.Action{Kind: hostfw.Status}); err == nil {
		s.recordStatus(ctx, hostID, st.Values)
	}
	if sc, ok := c.(*sshConn); ok {
		s.recordSSHPath(ctx, sc.r, hostID)
	}
	s.audit(ctx, "firewall.detect", hostID, audit.Success, map[string]any{"supported": el.Supported})
	return env, el, nil
}

// AdoptHostState accepts the host's committed firewall revision as the
// controller's (minimal reconciliation, as for M7's domains).
func (s *Service) AdoptHostState(ctx context.Context, hostID string) error {
	cur, err := s.d.Store.R.GetStateVersion(ctx, db.GetStateVersionParams{HostID: hostID, Domain: "firewall"})
	if err != nil {
		return ErrNotFound
	}
	if cur.Status == "epoch_mismatch" {
		return fmt.Errorf("the host's firewall state belongs to another controller epoch; remove DROOL's policy on the host or re-enroll it")
	}
	rev := max(cur.Revision, cur.ReportedRevision)
	if err := s.d.Store.W.UpsertStateVersion(ctx, db.UpsertStateVersionParams{HostID: hostID, Domain: "firewall", Epoch: s.epoch,
		Revision: rev, ContentHash: cur.ReportedHash, UpdatedAt: s.now().UnixMilli()}); err != nil {
		return err
	}
	s.markState(ctx, hostID, "ok", s.epoch, cur.ReportedRevision, cur.ReportedHash)
	return nil
}
