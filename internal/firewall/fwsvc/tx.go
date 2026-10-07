package fwsvc

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"drool/internal/agentsvc"
	"drool/internal/audit"
	"drool/internal/driver"
	"drool/internal/firewall/compile"
	"drool/internal/firewall/hostfw"
	"drool/internal/firewall/nftables"
	"drool/internal/hostexec"
	"drool/internal/ids"
	"drool/internal/model"
	"drool/internal/store/db"
)

// Transaction states.
const (
	TxPreparing  = "preparing"
	TxPending    = "pending"
	TxCommitted  = "committed"
	TxRolledBack = "rolled_back"
	TxFailed     = "failed" // refused or failed before anything changed on the host
	TxUnknown    = "unknown"
)

// TxStep is one line of a transaction's timeline.
type TxStep struct {
	At   string `json:"at"`
	Text string `json:"text"`
}

// TxRow is a transaction for the API.
type TxRow struct {
	ID              string   `json:"id"`
	HostID          string   `json:"host_id"`
	JobID           string   `json:"job_id,omitempty"`
	Kind            string   `json:"kind"`
	State           string   `json:"state"`
	Transport       string   `json:"transport"`
	Epoch           string   `json:"epoch"`
	Revision        int64    `json:"revision"`
	PreviousHash    string   `json:"previous_hash,omitempty"`
	CandidateHash   string   `json:"candidate_hash"`
	RulesHash       string   `json:"rules_hash,omitempty"`
	TimeoutS        int64    `json:"timeout_s"`
	Deadline        string   `json:"deadline,omitempty"`
	MeasuredMs      int64    `json:"measured_ms"`
	OverrideMgmt    bool     `json:"override_mgmt"`
	OverrideTimeout bool     `json:"override_timeout"`
	AllowDrift      bool     `json:"allow_drift"`
	Detail          string   `json:"detail"`
	ErrorClass      string   `json:"error_class,omitempty"`
	Steps           []TxStep `json:"steps"`
	CreatedAt       string   `json:"created_at"`
	UpdatedAt       string   `json:"updated_at"`
	FinishedAt      string   `json:"finished_at,omitempty"`
	Candidate       string   `json:"candidate,omitempty"`
}

func ms(v int64) string { return time.UnixMilli(v).UTC().Format(time.RFC3339) }

func toTxRow(t db.FwTransaction) TxRow {
	r := TxRow{ID: t.ID, HostID: t.HostID, JobID: t.JobID.String, Kind: t.Kind, State: t.State, Transport: t.Transport, Epoch: t.Epoch,
		Revision: t.Revision, PreviousHash: t.PreviousHash, CandidateHash: t.CandidateHash, RulesHash: t.RulesHash, TimeoutS: t.TimeoutS,
		MeasuredMs: t.MeasuredMs, OverrideMgmt: t.OverrideMgmt != 0, OverrideTimeout: t.OverrideTimeout != 0, AllowDrift: t.AllowDrift != 0,
		Detail: t.Detail, ErrorClass: t.ErrorClass, CreatedAt: ms(t.CreatedAt), UpdatedAt: ms(t.UpdatedAt), Candidate: t.Candidate}
	if t.Deadline.Valid {
		r.Deadline = ms(t.Deadline.Int64)
	}
	if t.FinishedAt.Valid {
		r.FinishedAt = ms(t.FinishedAt.Int64)
	}
	json.Unmarshal([]byte(t.StepsJson), &r.Steps)
	if r.Steps == nil {
		r.Steps = []TxStep{}
	}
	return r
}

// Transactions lists transactions, newest first.
func (s *Service) Transactions(ctx context.Context, hostID string, limit int) ([]TxRow, error) {
	rows, err := s.d.Store.R.ListFwTransactions(ctx, db.ListFwTransactionsParams{HostID: sql.NullString{String: hostID, Valid: hostID != ""}, Lim: int64(limit)})
	if err != nil {
		return nil, err
	}
	out := make([]TxRow, len(rows))
	for i, r := range rows {
		out[i] = toTxRow(db.FwTransaction{ID: r.ID, HostID: r.HostID, JobID: r.JobID, Kind: r.Kind, State: r.State, Transport: r.Transport,
			Epoch: r.Epoch, Revision: r.Revision, PreviousHash: r.PreviousHash, CandidateHash: r.CandidateHash, RulesHash: r.RulesHash,
			SetsHash: r.SetsHash, TimeoutS: r.TimeoutS, Deadline: r.Deadline, MeasuredMs: r.MeasuredMs, OverrideMgmt: r.OverrideMgmt,
			OverrideTimeout: r.OverrideTimeout, AllowDrift: r.AllowDrift, Detail: r.Detail, ErrorClass: r.ErrorClass, StepsJson: r.StepsJson,
			CreatedBy: r.CreatedBy, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt, FinishedAt: r.FinishedAt})
	}
	return out, nil
}

// Transaction returns one transaction with its candidate policy.
func (s *Service) Transaction(ctx context.Context, id string) (TxRow, error) {
	t, err := s.d.Store.R.GetFwTransaction(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return TxRow{}, ErrNotFound
	}
	return toTxRow(t), err
}

func (s *Service) openTx(ctx context.Context, hostID string) *db.FwTransaction {
	rows, err := s.d.Store.R.ListOpenFwTransactions(ctx)
	if err != nil {
		return nil
	}
	for i := len(rows) - 1; i >= 0; i-- {
		if rows[i].HostID == hostID {
			return &rows[i]
		}
	}
	return nil
}

// PendingReason says why reboots, network restarts and agent updates must
// wait (DESIGN §17.3), or "" when nothing is pending.
func (s *Service) PendingReason(ctx context.Context, hostID string) string {
	s.mu.Lock()
	dl, inflight := s.pending[hostID]
	s.mu.Unlock()
	if inflight {
		return fmt.Sprintf("a firewall transaction is in progress on this host (its rollback deadline is %s)", dl.UTC().Format(time.RFC3339))
	}
	if tx := s.openTx(ctx, hostID); tx != nil {
		// After the deadline the host has rolled back on its own (timer or
		// boot loader); an unresolved record then no longer blocks.
		if !tx.Deadline.Valid || s.now().Before(time.UnixMilli(tx.Deadline.Int64).Add(2*time.Minute)) {
			return fmt.Sprintf("firewall transaction %s is %s on this host", tx.ID, tx.State)
		}
	}
	return ""
}

// txRec records a transaction's progress in the database and the job output.
type txRec struct {
	s     *Service
	row   db.FwTransaction
	steps []TxStep
	out   driver.OutputSink
}

func (t *txRec) step(f string, a ...any) {
	msg := fmt.Sprintf(f, a...)
	t.steps = append(t.steps, TxStep{At: t.s.now().UTC().Format(time.RFC3339), Text: msg})
	if t.out != nil {
		fmt.Fprintf(t.out.Stdout(), "[firewall] %s\n", msg)
	}
	t.save()
}

func (t *txRec) save() {
	b, _ := json.Marshal(t.steps)
	t.row.StepsJson = string(b)
	t.row.UpdatedAt = t.s.now().UnixMilli()
	t.s.d.Store.W.UpdateFwTransaction(context.Background(), db.UpdateFwTransactionParams{State: t.row.State, Transport: t.row.Transport,
		PreviousHash: t.row.PreviousHash, Deadline: t.row.Deadline, Detail: t.row.Detail, ErrorClass: t.row.ErrorClass,
		StepsJson: t.row.StepsJson, UpdatedAt: t.row.UpdatedAt, FinishedAt: t.row.FinishedAt, ID: t.row.ID})
}

func (t *txRec) finish(state, detail string, cls model.ErrorClass) {
	t.row.State, t.row.Detail, t.row.ErrorClass = state, detail, string(cls)
	if state != TxPending && state != TxPreparing {
		t.row.FinishedAt = sql.NullInt64{Int64: t.s.now().UnixMilli(), Valid: true}
	}
	t.step("%s: %s", strings.ReplaceAll(state, "_", " "), detail)
}

// fwReport mirrors agent.FirewallReport (the agent's hello and telemetry).
type fwReport struct {
	Status    map[string]string `json:"status"`
	Endpoints []struct {
		Endpoint string   `json:"endpoint"`
		Port     int      `json:"port"`
		Addrs    []string `json:"addrs"`
	} `json:"endpoints"`
}

// recordAgentEndpoints stores the agent's resolved controller endpoints:
// its management path on hosts that restrict outbound traffic.
func (s *Service) recordAgentEndpoints(ctx context.Context, hostID string, rep fwReport) {
	if len(rep.Endpoints) == 0 {
		return
	}
	now := s.now().UnixMilli()
	s.d.Store.W.DeleteFwMgmtObservations(ctx, db.DeleteFwMgmtObservationsParams{HostID: hostID, Kind: "agent_out"})
	for _, e := range rep.Endpoints {
		for _, a := range e.Addrs {
			if ip, err := netip.ParseAddr(a); err == nil && e.Port > 0 {
				s.d.Store.W.UpsertFwMgmtObservation(ctx, db.UpsertFwMgmtObservationParams{HostID: hostID, Kind: "agent_out",
					Address: ip.Unmap().String(), Port: int64(e.Port), ObservedAt: now})
			}
		}
	}
}

// measureSSH opens a brand-new SSH connection through the whole jump chain,
// records the management source address and sshd port as the host sees
// them (SSH_CONNECTION), and returns how long that took.
func (s *Service) measureSSH(ctx context.Context, hostID string) (time.Duration, error) {
	t0 := s.now()
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	c, err := s.sshConn(ctx, hostID, true)
	if err != nil {
		return 0, err
	}
	defer c.Close()
	err = s.recordSSHPath(ctx, c.r, hostID)
	return s.now().Sub(t0), err
}

// recordSSHPath records how the host sees DROOL's SSH connection
// (SSH_CONNECTION: source address and sshd port, right behind the last
// hop). Transactions re-measure it over a fresh connection; Check and
// snapshots record it too, so Explain and observations show the management
// path before the first transaction.
func (s *Service) recordSSHPath(ctx context.Context, run hostexec.Runner, hostID string) error {
	r, err := run.Run(ctx, `printf 'conn=%s\n' "$SSH_CONNECTION"`, hostexec.RunOptions{MaxOutput: 4096})
	if err != nil {
		return err
	}
	line := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(string(r.Stdout)), "conn="))
	f := strings.Fields(line)
	if len(f) != 4 {
		return fmt.Errorf("the host did not report SSH_CONNECTION (%q)", line)
	}
	src, err := netip.ParseAddr(f[0])
	port, perr := strconv.Atoi(f[3])
	if err != nil || perr != nil {
		return fmt.Errorf("unreadable SSH_CONNECTION %q", line)
	}
	now := s.now().UnixMilli()
	s.d.Store.W.DeleteFwMgmtObservations(ctx, db.DeleteFwMgmtObservationsParams{HostID: hostID, Kind: "ssh_in"})
	s.d.Store.W.UpsertFwMgmtObservation(ctx, db.UpsertFwMgmtObservationParams{HostID: hostID, Kind: "ssh_in",
		Address: src.Unmap().String(), Port: int64(port), ObservedAt: now})
	return nil
}

// measureAgent makes the agent connect again over a new mTLS connection and
// returns how long that took; the new hello refreshes its endpoints.
func (s *Service) measureAgent(ctx context.Context, hostID string) (time.Duration, error) {
	t0 := s.now()
	s.d.Agents.RequestReconnect(hostID)
	fs, err := s.waitSession(ctx, hostID, t0, t0.Add(90*time.Second))
	if err != nil {
		return 0, err
	}
	var rep fwReport
	json.Unmarshal(fs.FirewallJSON, &rep)
	s.recordAgentEndpoints(ctx, hostID, rep)
	return fs.ConnectedAt.Sub(t0), nil
}

func (s *Service) waitSession(ctx context.Context, hostID string, after, until time.Time) (agentsvc.FirewallSession, error) {
	for {
		if fs, ok := s.d.Agents.HostFirewallSession(hostID); ok && fs.ConnectedAt.After(after) {
			return fs, nil
		}
		if s.now().After(until) {
			return agentsvc.FirewallSession{}, &model.OpError{Class: model.ClassConnection,
				Message: "the agent did not connect again over a new connection in time"}
		}
		select {
		case <-ctx.Done():
			return agentsvc.FirewallSession{}, ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
}

func (s *Service) reserveRevision(ctx context.Context, hostID, hash string) (int64, error) {
	var rev int64
	err := s.d.Store.InTx(ctx, func(q *db.Queries) error {
		cur, err := q.GetStateVersion(ctx, db.GetStateVersionParams{HostID: hostID, Domain: "firewall"})
		if err == nil {
			if cur.Status != "ok" {
				return &agentsvc.ReconcileError{Domain: "firewall", Status: cur.Status, HostRevision: cur.ReportedRevision, ControllerRevision: cur.Revision}
			}
			rev = cur.Revision
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		rev++
		return q.UpsertStateVersion(ctx, db.UpsertStateVersionParams{HostID: hostID, Domain: "firewall", Epoch: s.epoch,
			Revision: rev, ContentHash: "sha256:" + hash, UpdatedAt: s.now().UnixMilli()})
	})
	return rev, err
}

// checkHostState compares the host tooling's status with the controller:
// pending transactions, foreign epochs, newer revisions (a restored
// controller) and drift.
func (s *Service) checkHostState(ctx context.Context, hostID string, st map[string]string, allowDrift bool) error {
	if st["tool_version"] == "" || st["tool_version"] == "none" {
		return nil
	}
	if st["journal_state"] == "pending" {
		dl, _ := strconv.ParseInt(st["deadline_unix"], 10, 64)
		if time.Unix(dl, 0).After(s.now()) {
			return &model.OpError{Class: model.ClassLock, Message: fmt.Sprintf("firewall transaction %s is pending on the host until %s",
				st["transaction_id"], time.Unix(dl, 0).UTC().Format(time.RFC3339))}
		}
	}
	crev, _ := strconv.ParseInt(st["committed_revision"], 10, 64)
	if cep := st["committed_epoch"]; crev > 0 && cep != "" && cep != s.epoch {
		s.markState(ctx, hostID, "epoch_mismatch", cep, crev, st["committed_hash"])
		return &agentsvc.ReconcileError{Domain: "firewall", Status: "epoch_mismatch"}
	}
	cur, err := s.d.Store.R.GetStateVersion(ctx, db.GetStateVersionParams{HostID: hostID, Domain: "firewall"})
	ctrl := int64(0)
	if err == nil {
		ctrl = cur.Revision
	}
	if crev > ctrl {
		s.markState(ctx, hostID, "host_ahead", s.epoch, crev, st["committed_hash"])
		return &agentsvc.ReconcileError{Domain: "firewall", Status: "host_ahead", HostRevision: crev, ControllerRevision: ctrl}
	}
	expected, live := st["expected_live"], st["live_hash"]
	drift := (expected != "" && live != expected) || (expected == "" && live != "" && live != "none")
	if drift && !allowDrift {
		return &model.OpError{Class: model.ClassValidation,
			Message: "DROOL's nftables rules on this host were changed outside DROOL (drift)",
			Hint:    "Review the host, then apply again with \"replace drifted rules\"; a rollback then restores DROOL's last committed policy."}
	}
	return nil
}

func (s *Service) markState(ctx context.Context, hostID, status, epoch string, rev int64, hash string) {
	now := s.now().UnixMilli()
	_, err := s.d.Store.R.GetStateVersion(ctx, db.GetStateVersionParams{HostID: hostID, Domain: "firewall"})
	if errors.Is(err, sql.ErrNoRows) {
		s.d.Store.W.InsertReportedOnlyState(ctx, db.InsertReportedOnlyStateParams{HostID: hostID, Domain: "firewall", Epoch: s.epoch,
			UpdatedAt: now, ReportedEpoch: epoch, ReportedRevision: rev, ReportedHash: hash, ReportedAt: sql.NullInt64{Int64: now, Valid: true}, Status: status})
		return
	}
	s.d.Store.W.ReportStateVersion(ctx, db.ReportStateVersionParams{ReportedEpoch: epoch, ReportedRevision: rev, ReportedHash: hash,
		ReportedAt: sql.NullInt64{Int64: now, Valid: true}, Status: status, HostID: hostID, Domain: "firewall"})
}

func fail(cls model.ErrorClass, hint, f string, a ...any) error {
	return &model.OpError{Class: cls, Message: fmt.Sprintf(f, a...), Hint: hint}
}

// Apply runs one firewall transaction on a host: the job step
// firewall_apply (DESIGN §17).
func (s *Service) Apply(ctx context.Context, hostID string, op *model.FirewallApplyOp, out driver.OutputSink, started func()) (*driver.OpResult, error) {
	lk := s.hostLock(hostID)
	lk.Lock()
	defer lk.Unlock()
	h, err := s.d.Store.R.GetHost(ctx, hostID)
	if err != nil {
		return nil, err
	}
	say := func(f string, a ...any) { fmt.Fprintf(out.Stdout(), "[firewall] "+f+"\n", a...) }

	// Resolve anything left open first (an earlier transaction whose result
	// was lost); refuse while one is still pending.
	s.resolveHost(ctx, h)
	if tx := s.openTx(ctx, hostID); tx != nil {
		return nil, fail(model.ClassLock, "Wait for it to commit or roll back (at most its rollback timeout).",
			"firewall transaction %s is still %s on this host", tx.ID, tx.State)
	}
	cfg := s.HostConfig(ctx, hostID)
	if !op.Remove && cfg.Mode != ModeEnforce {
		return nil, fail(model.ClassValidation, "Set the host's firewall mode to Enforce first.",
			"the host's firewall mode is %q: DROOL enforces policies only in Enforce mode", cfg.Mode)
	}

	c, note, err := s.openConn(ctx, h)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	// Failures before anything was applied still say which path was used.
	tres := func(c conn) *driver.OpResult {
		return &driver.OpResult{Data: map[string]any{"transport": c.Transport()}}
	}
	if note != "" {
		fmt.Fprintf(out.Stderr(), "[drool] %s\n", note)
	}
	if c.Transport() == "agent" && !op.Remove {
		// The host owner's ceiling decides; never fall back to SSH (M7).
		if info, _, ok := s.d.Agents.HostSession(hostID); ok {
			if err := info.Effective.AllowFirewall(); err != nil {
				return tres(c), &model.OpError{Class: model.ClassCapability, Message: "the agent's capability ceiling does not allow firewall changes on this host",
					Hint: "Only root on the host can change /etc/drool/agent.yaml; DROOL never applies firewall policy over SSH instead."}
			}
		}
	}
	say("checking the host's firewall environment over %s", c.Transport())
	det, err := c.Run(ctx, hostfw.Action{Kind: hostfw.Detect})
	if err != nil {
		return tres(c), err
	}
	env, el := s.storeEnv(ctx, hostID, det)
	switch {
	case op.Remove && env != nil:
		// Removing DROOL's own table is allowed next to other firewalls.
		r := env.RemovalBlockers()
		if det.ErrCode != "" || det.Exit != 0 {
			r = append(r, "detection failed: "+strings.TrimSpace(det.ErrMsg+" "+det.Stderr))
		}
		if len(r) > 0 {
			return tres(c), fail(model.ClassConfiguration, "", "DROOL's policy can't be removed safely on %s: %s", h.Name, strings.Join(r, "; "))
		}
		if !el.Supported {
			say("enforcement is refused on this host (%s); removing DROOL's own table is still allowed", strings.Join(el.Reasons, "; "))
		}
	case !el.Supported:
		return tres(c), fail(model.ClassConfiguration, "Monitoring, simulation and Explain still work on this host; only enforcement is refused.",
			"firewall enforcement is not available on %s: %s", h.Name, strings.Join(el.Reasons, "; "))
	}
	st, err := c.Run(ctx, hostfw.Action{Kind: hostfw.Status})
	if err != nil {
		return tres(c), err
	}
	if err := s.checkHostState(ctx, hostID, st.Values, op.AllowDrift); err != nil {
		return tres(c), err
	}

	// Measure a fresh management path (DESIGN §17.4); this also records
	// how the host sees its management connections.
	var measured time.Duration
	if c.Transport() == "agent" {
		say("measuring a fresh mTLS connection (the agent reconnects)")
		measured, err = s.measureAgent(ctx, hostID)
		if err != nil {
			return tres(c), &driver.NotStartedError{Err: err}
		}
		if s.sshPathConfigured(h) {
			if _, serr := s.measureSSH(ctx, hostID); serr != nil {
				say("warning: the SSH fallback path could not be measured (%v); it is protected only as last observed", serr)
			}
		}
	} else {
		say("measuring a fresh SSH connection through the full path")
		measured, err = s.measureSSH(ctx, hostID)
		if err != nil {
			return tres(c), &driver.NotStartedError{Err: err}
		}
	}
	say("fresh connection established in %s", measured.Round(time.Millisecond))

	w, err := s.World(ctx)
	if err != nil {
		return tres(c), err
	}
	hp, warns := s.compileHost(ctx, w, h, op.OverrideMgmt)
	for _, wmsg := range warns {
		say("warning: %s", wmsg)
	}
	rend := nftables.Render(hp)
	candidate := rend.Full
	kind := "apply"
	if op.Remove {
		candidate, kind = nftables.Empty, "remove"
		rend = nftables.Rendered{Full: candidate, FullHash: hostfw.Hash(candidate), RulesHash: "removed"}
	} else {
		if len(hp.Conflicts) > 0 {
			var msgs []string
			for _, cf := range hp.Conflicts {
				msgs = append(msgs, cf.Message)
			}
			return tres(c), fail(model.ClassValidation, "Change the policy, or use the audited emergency override (the dead-man rollback still applies).",
				"refused: %s", strings.Join(msgs, "; "))
		}
		if op.ExpectRulesHash != "" && op.ExpectRulesHash != rend.RulesHash {
			return tres(c), fail(model.ClassValidation, "Preview the policy again before applying it.",
				"the policy changed since it was previewed")
		}
		for _, cf := range hp.Overrode {
			say("EMERGENCY OVERRIDE: %s", cf.Message)
		}
	}
	timeout, from := s.EffectiveTimeout(ctx, hostID)
	if timeout < 10 {
		return tres(c), fail(model.ClassValidation, "", "the rollback timeout (%ds) must be at least 10 seconds", timeout)
	}
	if minT := 3 * measured; time.Duration(timeout)*time.Second < minT && !op.OverrideTimeout {
		return tres(c), fail(model.ClassValidation, "Raise the host's or group's rollback timeout, or override (firewall.override).",
			"the rollback timeout (%ds, %s) is shorter than 3x the measured connection time (%s): the host could roll back a working policy",
			timeout, from, minT.Round(time.Second))
	}
	rev, err := s.reserveRevision(ctx, hostID, rend.FullHash)
	if err != nil {
		return tres(c), &model.OpError{Class: model.ClassValidation, Message: err.Error(),
			Hint: "Adopt the host's firewall state on the host page (reconciliation), then apply again."}
	}
	stRow, _ := s.state(ctx, hostID)
	setsGen := max(stRow.SetsGeneration, stRow.SetsGenerationApplied) + 1
	policyJSON, _ := json.Marshal(hp)

	info := driver.StepInfoFrom(ctx)
	now := s.now()
	t := &txRec{s: s, out: out, row: db.FwTransaction{ID: ids.New(ids.FwTx), HostID: hostID,
		JobID: sql.NullString{String: info.JobID, Valid: info.JobID != ""}, Kind: kind, State: TxPreparing, Transport: c.Transport(),
		Epoch: s.epoch, Revision: rev, CandidateHash: rend.FullHash, RulesHash: rend.RulesHash, SetsHash: rend.SetsHash,
		TimeoutS: int64(timeout), MeasuredMs: measured.Milliseconds(), OverrideMgmt: b2i(op.OverrideMgmt && len(hp.Overrode) > 0),
		OverrideTimeout: b2i(op.OverrideTimeout), AllowDrift: b2i(op.AllowDrift), Candidate: candidate, PolicyJson: string(policyJSON),
		StepsJson: "[]", CreatedBy: sql.NullString{String: info.UserID, Valid: info.UserID != ""}, CreatedAt: now.UnixMilli(), UpdatedAt: now.UnixMilli()}}
	r := t.row
	if err := s.d.Store.W.InsertFwTransaction(ctx, db.InsertFwTransactionParams{ID: r.ID, HostID: r.HostID, JobID: r.JobID, Kind: r.Kind,
		State: r.State, Transport: r.Transport, Epoch: r.Epoch, Revision: r.Revision, CandidateHash: r.CandidateHash, RulesHash: r.RulesHash,
		SetsHash: r.SetsHash, TimeoutS: r.TimeoutS, MeasuredMs: r.MeasuredMs, OverrideMgmt: r.OverrideMgmt, OverrideTimeout: r.OverrideTimeout,
		AllowDrift: r.AllowDrift, Candidate: r.Candidate, PolicyJson: r.PolicyJson, StepsJson: "[]", CreatedBy: r.CreatedBy,
		CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt}); err != nil {
		return nil, err
	}
	s.audit(ctx, "firewall.transaction.start", hostID, audit.Success, map[string]any{"transaction_id": r.ID, "revision": rev,
		"transport": c.Transport(), "timeout_s": timeout, "override_mgmt": op.OverrideMgmt && len(hp.Overrode) > 0,
		"override_timeout": op.OverrideTimeout, "allow_drift": op.AllowDrift, "kind": kind})
	t.step("transaction %s: revision %d, rollback timeout %ds (%s), fresh path measured %s", r.ID, rev, timeout, from, measured.Round(time.Millisecond))

	applyStart := s.now()
	deadline := applyStart.Add(time.Duration(timeout) * time.Second)
	s.mu.Lock()
	s.pending[hostID] = deadline
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.pending, hostID)
		s.mu.Unlock()
	}()
	t.row.Deadline = sql.NullInt64{Int64: deadline.UnixMilli(), Valid: true}
	if started != nil {
		started()
	}
	t.step("applying the candidate: the host journals it, arms its rollback timer, then applies it atomically")
	res, applyErr := c.Run(ctx, hostfw.Action{Kind: hostfw.Apply, TxID: r.ID, Epoch: s.epoch, Revision: rev, Hash: rend.FullHash,
		TimeoutS: timeout, Expires: s.now().Add(2 * time.Minute), AllowDrift: op.AllowDrift, SetsGeneration: setsGen,
		RulesHash: rend.RulesHash, Candidate: candidate})
	applyEnd := s.now()
	switch {
	case applyErr != nil && driver.IsNotStarted(applyErr):
		t.finish(TxFailed, "not applied: "+model.AsOpError(applyErr).Message, model.ClassOf(applyErr))
		return s.txResult(t), applyErr
	case applyErr != nil:
		// The result was lost: the host may or may not have applied it.
		// Verification below decides; the host rolls back on its own.
		t.step("the apply result was lost (%v); verifying over a new connection", applyErr)
	case res.ErrCode != "":
		e := res.Error()
		state := TxFailed
		if res.ErrCode == "apply" {
			state = TxRolledBack
		}
		t.finish(state, res.ErrMsg, model.ClassOf(e))
		return s.txResult(t), e
	case res.Get("state") != "pending":
		e := res.Error()
		if e == nil {
			e = fail(model.ClassExecution, "", "unexpected result from the host: %v", res.Values)
		}
		t.finish(TxFailed, model.AsOpError(e).Message, model.ClassOf(e))
		return s.txResult(t), e
	default:
		t.row.PreviousHash = res.Get("previous_hash")
		t.row.State = TxPending
		t.step("candidate applied; pending until %s unless committed over a new connection", deadline.UTC().Format(time.RFC3339))
	}
	t.row.State = TxPending
	t.save()

	testDelay, testSkip := s.testControl(h.Name)
	if testDelay > 0 {
		t.step("TEST MODE: waiting %s before verification", testDelay)
		select {
		case <-ctx.Done():
		case <-time.After(testDelay):
		}
	}
	margin := min(15*time.Second, time.Duration(timeout)*time.Second/4)
	verifyBy := deadline.Add(-margin)
	var verr error
	if testSkip {
		verr = errors.New("TEST MODE: commit skipped")
	} else if c.Transport() == "agent" {
		verr = s.verifyAgent(ctx, t, hostID, applyEnd, verifyBy, rev, rend.FullHash)
	} else {
		verr = s.verifySSH(ctx, t, hostID, verifyBy, rev, rend.FullHash)
	}
	if verr == nil {
		s.committed(ctx, t, h, hp, rend, op, setsGen)
		zero, changed := 0, true
		return &driver.OpResult{ExitCode: &zero, Changed: &changed, Data: map[string]any{"firewall": toTxRow(t.row)}}, nil
	}
	t.step("not committed: %v", verr)
	t.step("waiting for the host to restore its previous policy (deadline %s)", deadline.UTC().Format(time.RFC3339))
	return s.awaitRollback(ctx, t, h, deadline, verr)
}

func b2i(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

func (s *Service) txResult(t *txRec) *driver.OpResult {
	zero := 0
	if t.row.State != TxCommitted {
		zero = 1
	}
	return &driver.OpResult{ExitCode: &zero, Data: map[string]any{"firewall": toTxRow(t.row)}}
}

func verifyStatus(st map[string]string, tx string, rev int64, hash string) error {
	switch {
	case st["transaction_id"] != tx:
		return fmt.Errorf("the host reports transaction %q, not %s", st["transaction_id"], tx)
	case st["journal_state"] != "pending":
		return fmt.Errorf("the host reports transaction %s as %q (%s)", tx, st["journal_state"], st["reason"])
	case st["candidate_hash"] != hash:
		return fmt.Errorf("the host's pending candidate has hash %s, not %s", st["candidate_hash"], hash)
	case st["tx_revision"] != strconv.FormatInt(rev, 10):
		return fmt.Errorf("the host's pending revision is %s, not %d", st["tx_revision"], rev)
	}
	return nil
}

// verifyAgent: the agent drops its session after applying; only a session
// that connected after the apply proves the candidate kept the management
// path (DESIGN §17.1). The commit travels over that new session.
func (s *Service) verifyAgent(ctx context.Context, t *txRec, hostID string, applyEnd, verifyBy time.Time, rev int64, hash string) error {
	s.d.Agents.RequestReconnect(hostID)
	t.step("waiting for the agent to connect again over a brand-new mTLS connection")
	fs, err := s.waitSession(ctx, hostID, applyEnd, verifyBy)
	if err != nil {
		return err
	}
	var rep fwReport
	json.Unmarshal(fs.FirewallJSON, &rep)
	if err := verifyStatus(rep.Status, t.row.ID, rev, hash); err != nil {
		return err
	}
	t.step("new connection at %s verified transaction %s, revision %d, hash %s", fs.ConnectedAt.UTC().Format(time.RFC3339), t.row.ID, rev, hash[:16])
	cctx, cancel := context.WithDeadline(agentsvc.WithSessionAfter(ctx, applyEnd), verifyBy.Add(10*time.Second))
	defer cancel()
	res, err := (&agentConn{s: s, hostID: hostID}).Run(cctx, hostfw.Action{Kind: hostfw.Commit, TxID: t.row.ID, Hash: hash})
	if err != nil {
		return err
	}
	if res.ErrCode != "" || res.Get("state") != "committed" {
		return fmt.Errorf("the host refused the commit: %s %s", res.ErrCode, res.ErrMsg)
	}
	return nil
}

// verifySSH opens a brand-new SSH connection through the full jump chain
// (never the pool, DESIGN §17.2); only that connection may commit.
func (s *Service) verifySSH(ctx context.Context, t *txRec, hostID string, verifyBy time.Time, rev int64, hash string) error {
	t.step("opening a brand-new SSH connection through the full path (no pooled or multiplexed connection)")
	cctx, cancel := context.WithDeadline(ctx, verifyBy)
	defer cancel()
	var c *sshConn
	var err, lastErr error
	for {
		c, err = s.sshConn(cctx, hostID, true)
		if err == nil || cctx.Err() != nil {
			break
		}
		lastErr = err
		t.step("new connection failed: %v; retrying", model.AsOpError(err).Message)
		select {
		case <-cctx.Done():
		case <-time.After(3 * time.Second):
		}
	}
	if err != nil {
		if lastErr == nil {
			lastErr = err
		}
		return fmt.Errorf("no new SSH connection before the deadline (%v)", model.AsOpError(lastErr).Message)
	}
	defer c.Close()
	st, err := c.Run(cctx, hostfw.Action{Kind: hostfw.Status})
	if err != nil {
		return err
	}
	if err := verifyStatus(st.Values, t.row.ID, rev, hash); err != nil {
		return err
	}
	t.step("new connection verified transaction %s, revision %d, hash %s", t.row.ID, rev, hash[:16])
	res, err := c.Run(cctx, hostfw.Action{Kind: hostfw.Commit, TxID: t.row.ID, Hash: hash})
	if err != nil {
		return err
	}
	if res.ErrCode != "" || res.Get("state") != "committed" {
		return fmt.Errorf("the host refused the commit: %s %s", res.ErrCode, res.ErrMsg)
	}
	return nil
}

func (s *Service) committed(ctx context.Context, t *txRec, h db.Host, hp *compile.HostPolicy, rend nftables.Rendered, op *model.FirewallApplyOp, setsGen int64) {
	enforced := int64(1)
	policyJSON, _ := json.Marshal(hp)
	pj := string(policyJSON)
	if op.Remove {
		enforced, pj = 0, ""
	}
	s.d.Store.W.SetFwHostApplied(ctx, db.SetFwHostAppliedParams{Enforced: enforced, AppliedRevision: t.row.Revision, AppliedRulesHash: rend.RulesHash,
		AppliedFullHash: rend.FullHash, AppliedSetsHash: rend.SetsHash, AppliedPolicyJson: pj, OverrideMgmt: t.row.OverrideMgmt,
		SetsGeneration: setsGen, SetsGenerationApplied: setsGen, UpdatedAt: s.now().UnixMilli(), HostID: h.ID})
	s.markState(ctx, h.ID, "ok", s.epoch, t.row.Revision, "sha256:"+rend.FullHash)
	t.finish(TxCommitted, fmt.Sprintf("revision %d committed over a new %s connection", t.row.Revision, t.row.Transport), "")
	s.audit(ctx, "firewall.transaction.commit", h.ID, audit.Success, map[string]any{"transaction_id": t.row.ID, "revision": t.row.Revision,
		"hash": rend.FullHash, "transport": t.row.Transport})
}

// awaitRollback waits past the deadline and confirms the host restored its
// previous policy. It never reports success: a transaction that wasn't
// committed is rolled back (confirmed) or unknown.
func (s *Service) awaitRollback(ctx context.Context, t *txRec, h db.Host, deadline time.Time, cause error) (*driver.OpResult, error) {
	wait := time.Until(deadline.Add(5 * time.Second))
	select {
	case <-ctx.Done():
	case <-time.After(wait):
	}
	confirmUntil := s.now().Add(90 * time.Second)
	for ctx.Err() == nil && s.now().Before(confirmUntil) {
		st, err := s.statusFresh(ctx, h, time.Time{})
		if err == nil && st["transaction_id"] == t.row.ID {
			switch st["journal_state"] {
			case "rolled_back":
				t.finish(TxRolledBack, "the host restored its previous policy: "+st["reason"], model.ClassConnection)
				s.audit(ctx, "firewall.transaction.rollback", h.ID, audit.Failure, map[string]any{"transaction_id": t.row.ID, "reason": st["reason"]})
				return s.txResult(t), &model.OpError{Class: model.ClassConnection,
					Message: "the new policy was not confirmed over a new connection and was rolled back: " + cause.Error(),
					Hint:    "The host kept its previous policy. Check the management path in the preview and the transaction timeline."}
			case "committed":
				// The commit reached the host but its reply was lost.
				hp := &compile.HostPolicy{}
				json.Unmarshal([]byte(t.row.PolicyJson), hp)
				rend := nftables.Rendered{FullHash: t.row.CandidateHash, RulesHash: t.row.RulesHash, SetsHash: t.row.SetsHash}
				s.committed(ctx, t, h, hp, rend, &model.FirewallApplyOp{Remove: t.row.Kind == "remove"}, 0)
				zero, changed := 0, true
				return &driver.OpResult{ExitCode: &zero, Changed: &changed, Data: map[string]any{"firewall": toTxRow(t.row)}}, nil
			}
		}
		select {
		case <-ctx.Done():
		case <-time.After(5 * time.Second):
		}
	}
	t.finish(TxUnknown, fmt.Sprintf("not committed (%v); the host should have restored its previous policy at %s, but DROOL could not confirm it yet",
		cause, deadline.UTC().Format(time.RFC3339)), model.ClassConnection)
	s.audit(ctx, "firewall.transaction.unknown", h.ID, audit.Failure, map[string]any{"transaction_id": t.row.ID})
	return s.txResult(t), &model.OpError{Class: model.ClassConnection,
		Message: "firewall transaction outcome unknown: it was not committed, and the host could not be reached to confirm the rollback",
		Hint:    "DROOL checks again when the host is reachable. The host restores its previous policy on its own (timer or boot)."}
}

// statusFresh asks the host for its tooling status over a path that
// connected after since (agent session) or a new SSH connection.
func (s *Service) statusFresh(ctx context.Context, h db.Host, since time.Time) (map[string]string, error) {
	d := s.d.Decide(ctx, h)
	if d.UseAgent && s.d.Agents != nil {
		if fs, ok := s.d.Agents.HostFirewallSession(h.ID); ok && fs.ConnectedAt.After(since) {
			cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			res, err := (&agentConn{s: s, hostID: h.ID}).Run(cctx, hostfw.Action{Kind: hostfw.Status})
			if err == nil {
				return res.Values, nil
			}
		}
	}
	if s.sshPathConfigured(h) || h.ManagementMethod == "ssh" {
		cctx, cancel := context.WithTimeout(ctx, 45*time.Second)
		defer cancel()
		c, err := s.sshConn(cctx, h.ID, true)
		if err != nil {
			return nil, err
		}
		defer c.Close()
		res, err := c.Run(cctx, hostfw.Action{Kind: hostfw.Status})
		if err != nil {
			return nil, err
		}
		return res.Values, nil
	}
	return nil, errors.New("no path to the host")
}

func (s *Service) audit(ctx context.Context, action, hostID string, res audit.Result, details map[string]any) {
	if s.d.Audit == nil {
		return
	}
	e := audit.Event{Action: action, TargetType: "host", TargetID: hostID, Result: res, Details: details}
	if audit.ActorFrom(ctx).Type == audit.ActorAnonymous {
		e.Actor = &audit.System
	}
	s.d.Audit.Record(context.WithoutCancel(ctx), e)
}

// storeEnv records detection results and returns the eligibility.
func (s *Service) storeEnv(ctx context.Context, hostID string, det *hostfw.Result) (*nftables.Env, nftables.Eligibility) {
	env, _ := nftables.ParseDetect(det.Values, det.Multi, det.Sections)
	el := env.Eligibility()
	if det.ErrCode != "" || det.Exit != 0 {
		el.Supported = false
		el.Reasons = append(el.Reasons, "detection failed: "+strings.TrimSpace(det.ErrMsg+" "+det.Stderr))
	}
	eb, _ := json.Marshal(env)
	lb, _ := json.Marshal(el)
	s.state(ctx, hostID)
	now := s.now().UnixMilli()
	s.d.Store.W.SetFwHostEnv(ctx, db.SetFwHostEnvParams{EnvJson: string(eb), EligibilityJson: string(lb),
		EnvCheckedAt: sql.NullInt64{Int64: now, Valid: true}, UpdatedAt: now, HostID: hostID})
	return env, el
}
