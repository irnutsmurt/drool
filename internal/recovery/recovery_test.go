package recovery

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"drool/internal/agentrpc/pb"
	"drool/internal/agentsvc"
	"drool/internal/audit"
	"drool/internal/model"
	"drool/internal/store"
	"drool/internal/store/db"
)

const (
	prevEpoch = "11111111111111111111111111111111"
	newEpoch  = "22222222222222222222222222222222"
)

func TestCompare(t *testing.T) {
	ctl := &db.StateVersion{Domain: "agent-config", Epoch: prevEpoch, Revision: 3, ContentHash: "sha256:aa"}
	for _, c := range []struct {
		name     string
		ctl      *db.StateVersion
		epoch    string
		rev      int64
		hash     string
		accepted bool
		verdict  string
	}{
		{"identical", ctl, prevEpoch, 3, "sha256:aa", false, VerdictSame},
		{"identical, prefix differs", ctl, prevEpoch, 3, "aa", false, VerdictSame},
		{"already moved", ctl, newEpoch, 3, "sha256:aa", false, VerdictSame},
		{"controller ahead", ctl, prevEpoch, 2, "sha256:bb", false, VerdictControllerAhead},
		{"nothing on the host", ctl, "", 0, "", false, VerdictControllerAhead},
		{"host ahead", ctl, prevEpoch, 4, "sha256:cc", false, VerdictHostAhead},
		{"host ahead, accepted", ctl, prevEpoch, 4, "sha256:cc", true, VerdictAccepted},
		{"same revision, other content", ctl, prevEpoch, 3, "sha256:dd", false, VerdictConflict},
		{"another epoch", ctl, "33333333333333333333333333333333", 3, "sha256:aa", false, VerdictForeignEpoch},
		{"host only", nil, prevEpoch, 1, "sha256:ee", false, VerdictHostAhead},
		{"neither", nil, "", 0, "", false, VerdictSame},
	} {
		got := Compare("agent-config", c.ctl, c.epoch, c.rev, c.hash, prevEpoch, newEpoch, c.accepted)
		if got.Verdict != c.verdict {
			t.Errorf("%s: %s, want %s", c.name, got.Verdict, c.verdict)
		}
	}
	if st, _ := verdictState([]DomainCheck{{Verdict: VerdictSame}, {Verdict: VerdictHostAhead}}); st != StateAhead {
		t.Errorf("host ahead → %s", st)
	}
	if st, _ := verdictState([]DomainCheck{{Verdict: VerdictHostAhead}, {Verdict: VerdictConflict}}); st != StateAttention {
		t.Errorf("conflict → %s", st)
	}
	if st, _ := verdictState([]DomainCheck{{Verdict: VerdictControllerAhead}, {Verdict: VerdictAccepted}}); st != "" {
		t.Errorf("movable → %s", st)
	}
}

// fakeAgents is a fleet of connected Shepherds.
type fakeAgents struct {
	mu          sync.Mutex
	views       map[string]agentsvc.AgentView
	transitions map[string]int
	running     atomic.Int32
	peak        atomic.Int32
	delay       time.Duration
	answer      func(hostID string, t *pb.EpochTransition) *pb.EpochTransitionResult
}

func (f *fakeAgents) AgentView(hostID string) (agentsvc.AgentView, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.views[hostID]
	return v, ok
}

func (f *fakeAgents) Transition(ctx context.Context, hostID string, t *pb.EpochTransition) (*pb.EpochTransitionResult, error) {
	n := f.running.Add(1)
	defer f.running.Add(-1)
	for {
		p := f.peak.Load()
		if n <= p || f.peak.CompareAndSwap(p, n) {
			break
		}
	}
	time.Sleep(f.delay)
	f.mu.Lock()
	f.transitions[hostID]++
	f.mu.Unlock()
	if f.answer != nil {
		return f.answer(hostID, t), nil
	}
	return &pb.EpochTransitionResult{TransitionId: t.TransitionId, Ok: true, ControllerEpoch: t.ToEpoch}, nil
}

func (f *fakeAgents) PushConfigIfBehind(context.Context, string) {}

type fakeFirewall struct {
	mu      sync.Mutex
	status  map[string]map[string]string
	errs    map[string]error
	rebased map[string]string
}

func (f *fakeFirewall) SSHStatus(ctx context.Context, hostID string) (map[string]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.errs[hostID]; err != nil {
		return nil, err
	}
	return f.status[hostID], nil
}

func (f *fakeFirewall) SSHRebase(ctx context.Context, hostID, from string, rev int64, hash string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rebased[hostID] = fmt.Sprintf("%s/%d/%s", from, rev, hash)
	return nil
}

type env struct {
	st  *store.Store
	svc *Service
	ag  *fakeAgents
	fw  *fakeFirewall
}

// newEnv creates a restored controller's database: a restore record and
// the given hosts pending.
func newEnv(t *testing.T, workers int) *env {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), store.FileName))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ctx := context.Background()
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	mustExec(t, st, `INSERT INTO settings (key, value_json, updated_at) VALUES ('controller_epoch', '"`+newEpoch+`"', 0)`)
	mustExec(t, st, `INSERT INTO controller_restores (id, restored_at, backup_created_at, previous_epoch, epoch, status) VALUES ('rst_1', 1, 0, ?, ?, 'reconciling')`, prevEpoch, newEpoch)
	ag := &fakeAgents{views: map[string]agentsvc.AgentView{}, transitions: map[string]int{}}
	fw := &fakeFirewall{status: map[string]map[string]string{}, errs: map[string]error{}, rebased: map[string]string{}}
	svc := New(Deps{Store: st, Agents: ag, Firewall: fw, Workers: workers})
	svc.tick, svc.scale = 100*time.Millisecond, 0.01
	return &env{st: st, svc: svc, ag: ag, fw: fw}
}

func mustExec(t *testing.T, st *store.Store, q string, args ...any) {
	t.Helper()
	if _, err := st.Writer.Exec(q, args...); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}

// agentHost adds a Shepherd host whose agent-config domain is at rev on
// both sides (the agent's view at hostRev).
func (e *env) agentHost(t *testing.T, id string, ctlRev, hostRev uint64) {
	t.Helper()
	mustExec(t, e.st, `INSERT INTO hosts (id, name, connect_address, ssh_user, management_method, ssh_configured, status, created_at, updated_at)
		VALUES (?, ?, '', '', 'agent', 0, 'ready', 0, 0)`, id, id)
	mustExec(t, e.st, `INSERT INTO agent_identities (id, host_id, public_key, fingerprint, status, enrolled_at, epoch_reported) VALUES (?, ?, ?, 'fp', 'active', 0, ?)`,
		"aid_"+id, id, []byte("key-"+id), prevEpoch)
	mustExec(t, e.st, `INSERT INTO state_versions (host_id, domain, epoch, revision, content_hash, updated_at, status) VALUES (?, 'agent-config', ?, ?, ?, 0, 'epoch_mismatch')`,
		id, prevEpoch, ctlRev, fmt.Sprintf("sha256:%d", ctlRev))
	mustExec(t, e.st, `INSERT INTO host_reconciliation (restore_id, host_id, state) VALUES ('rst_1', ?, 'pending')`, id)
	e.ag.mu.Lock()
	e.ag.views[id] = agentsvc.AgentView{IdentityID: "aid_" + id, IdentityStatus: "active", Sessions: 1, Compatible: true,
		Features: []string{"epoch-1"}, Epoch: prevEpoch, AgentVersion: "1.0",
		Domains: []*pb.DomainState{{Domain: "agent-config", Epoch: prevEpoch, Revision: hostRev, ContentHash: fmt.Sprintf("sha256:%d", hostRev)}}}
	e.ag.mu.Unlock()
}

func (e *env) state(t *testing.T, id string) (string, string) {
	t.Helper()
	var st, reason string
	if err := e.st.Reader.QueryRow(`SELECT state, reason FROM host_reconciliation WHERE host_id = ?`, id).Scan(&st, &reason); err != nil {
		t.Fatal(err)
	}
	return st, reason
}

func waitFor(t *testing.T, d time.Duration, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestBoundedReconnect: 200 Shepherds reconnecting at once after a restore
// are compared a bounded number at a time, each moved exactly once, and
// nothing else is started (no job, no repeat).
func TestBoundedReconnect(t *testing.T) {
	e := newEnv(t, 8)
	const n = 200
	for i := 0; i < n; i++ {
		e.agentHost(t, fmt.Sprintf("h%03d", i), 2, 2)
	}
	e.ag.delay = 5 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := e.svc.Start(ctx); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			e.svc.OnSession(fmt.Sprintf("h%03d", i))
			e.svc.OnSession(fmt.Sprintf("h%03d", i)) // reconnect storms send the same host twice
		}(i)
	}
	wg.Wait()
	waitFor(t, 60*time.Second, "every host reconciled", func() bool {
		var left int
		e.st.Reader.QueryRow(`SELECT count(*) FROM host_reconciliation WHERE state != 'reconciled'`).Scan(&left)
		return left == 0
	})
	if p := e.ag.peak.Load(); p > 8 || p < 2 {
		t.Errorf("transitions in flight at once: %d (bound 8)", p)
	}
	e.svc.mu.Lock()
	peak := e.svc.peak
	e.svc.mu.Unlock()
	if peak > 8 {
		t.Errorf("hosts worked on at once: %d", peak)
	}
	for id, c := range e.ag.transitions {
		if c != 1 {
			t.Errorf("%s moved %d times", id, c)
		}
	}
	if len(e.ag.transitions) != n {
		t.Errorf("%d hosts moved, want %d", len(e.ag.transitions), n)
	}
	var jobs, bad int
	e.st.Reader.QueryRow(`SELECT count(*) FROM jobs`).Scan(&jobs)
	e.st.Reader.QueryRow(`SELECT count(*) FROM state_versions WHERE epoch != ? OR status != 'ok'`, newEpoch).Scan(&bad)
	if jobs != 0 || bad != 0 {
		t.Errorf("jobs created: %d; state versions not moved: %d", jobs, bad)
	}
	var status string
	e.st.Reader.QueryRow(`SELECT status FROM controller_restores WHERE id = 'rst_1'`).Scan(&status)
	if status != "finished" {
		t.Errorf("restore status %s", status)
	}
}

// TestHostDecisions: what is moved by itself, what waits, and why.
func TestHostDecisions(t *testing.T) {
	e := newEnv(t, 2)
	e.agentHost(t, "same", 2, 2)
	e.agentHost(t, "behind", 3, 2) // the controller is ahead: moved, DROOL's newer configuration follows
	e.agentHost(t, "ahead", 2, 3)
	e.agentHost(t, "old", 2, 2)
	e.ag.views["old"] = func() agentsvc.AgentView { v := e.ag.views["old"]; v.Features = nil; v.AgentVersion = "0.9"; return v }()
	e.agentHost(t, "clone", 2, 2)
	e.agentHost(t, "foreign", 2, 2)
	e.ag.views["foreign"] = func() agentsvc.AgentView {
		v := e.ag.views["foreign"]
		v.Epoch = "99999999999999999999999999999999"
		return v
	}()
	e.agentHost(t, "offline", 2, 2)
	delete(e.ag.views, "offline")

	// SSH hosts: an identical firewall (rebased), none at all (recovered),
	// a changed host key (blocked), unreachable (retried).
	ssh := func(id string) {
		mustExec(t, e.st, `INSERT INTO hosts (id, name, connect_address, ssh_user, status, created_at, updated_at) VALUES (?, ?, '10.0.0.1', 'root', 'ready', 0, 0)`, id, id)
		mustExec(t, e.st, `INSERT INTO host_reconciliation (restore_id, host_id, state) VALUES ('rst_1', ?, 'pending')`, id)
	}
	ssh("fw")
	mustExec(t, e.st, `INSERT INTO state_versions (host_id, domain, epoch, revision, content_hash, updated_at, status) VALUES ('fw', 'firewall', ?, 5, 'sha256:abc', 0, 'epoch_mismatch')`, prevEpoch)
	e.fw.status["fw"] = map[string]string{"tool_version": "2", "committed_revision": "5", "committed_epoch": prevEpoch, "committed_hash": "abc", "journal_state": "committed"}
	ssh("plain")
	e.fw.status["plain"] = map[string]string{"tool_version": "none"}
	ssh("rekeyed")
	e.fw.errs["rekeyed"] = &model.OpError{Class: model.ClassHostKey, Message: "host key mismatch"}
	ssh("down")
	e.fw.errs["down"] = &model.OpError{Class: model.ClassConnection, Message: "connection refused"}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := e.svc.Start(ctx); err != nil {
		t.Fatal(err)
	}
	// The identity conflict is noticed when the second machine connects.
	e.svc.MachineChanged("clone", "machine-a", "machine-b")
	for id := range e.ag.views {
		e.svc.OnSession(id)
	}
	want := map[string]string{"same": StateReconciled, "behind": StateReconciled, "ahead": StateAhead, "old": StateIncompatible,
		"clone": StateBlocked, "foreign": StateAttention, "offline": StatePending, "fw": StateReconciled, "plain": StateRecovered,
		"rekeyed": StateBlocked, "down": StateUnreachable}
	waitFor(t, 20*time.Second, "every host decided", func() bool {
		for id, w := range want {
			if st, _ := e.state(t, id); st != w {
				return false
			}
		}
		return true
	})
	if e.ag.transitions["ahead"]+e.ag.transitions["old"]+e.ag.transitions["clone"]+e.ag.transitions["foreign"] != 0 {
		t.Errorf("hosts that wait were moved: %v", e.ag.transitions)
	}
	if e.fw.rebased["fw"] != prevEpoch+"/5/abc" {
		t.Errorf("firewall rebase: %q", e.fw.rebased["fw"])
	}
	var rev int64
	var hash, epoch, status string
	e.st.Reader.QueryRow(`SELECT revision, content_hash, epoch, status FROM state_versions WHERE host_id = 'behind' AND domain = 'agent-config'`).Scan(&rev, &hash, &epoch, &status)
	if rev != 3 || hash != "sha256:3" || epoch != newEpoch || status != "ok" {
		t.Errorf("controller ahead: kept %d %s %s %s", rev, hash, epoch, status)
	}
	if _, why := e.state(t, "old"); !contains(why, "version 0.9") {
		t.Errorf("incompatible reason: %s", why)
	}

	// Accepting: the ahead host moves with its own state; a blocked clone
	// can be accepted; a changed host key can't (it has its own flow).
	if err := e.svc.Accept(ctx, "ahead", actor()); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.Accept(ctx, "clone", actor()); err != nil {
		t.Fatal(err)
	}
	var se *ErrState
	if err := e.svc.Accept(ctx, "rekeyed", actor()); !errors.As(err, &se) {
		t.Errorf("accepting a changed host key: %v", err)
	}
	if err := e.svc.Accept(ctx, "same", actor()); !errors.As(err, &se) {
		t.Errorf("accepting a reconciled host: %v", err)
	}
	waitFor(t, 20*time.Second, "accepted hosts moved", func() bool {
		a, _ := e.state(t, "ahead")
		c, _ := e.state(t, "clone")
		return a == StateReconciled && c == StateReconciled
	})
	e.st.Reader.QueryRow(`SELECT revision, content_hash FROM state_versions WHERE host_id = 'ahead' AND domain = 'agent-config'`).Scan(&rev, &hash)
	if rev != 3 || hash != "sha256:3" {
		t.Errorf("accepted host: controller has %d %s, want the host's 3", rev, hash)
	}

	// A host still not settled keeps the restore open until finished by hand.
	ok, _ := e.svc.Reconciling(ctx)
	if !ok {
		t.Error("reconciliation closed while hosts are unsettled")
	}
	if err := e.svc.Finish(ctx, actor()); err != nil {
		t.Fatal(err)
	}
	if ok, _ := e.svc.Reconciling(ctx); ok {
		t.Error("still reconciling after Finish")
	}
	// Its Shepherd is still compared when it connects later.
	e.agentHost2(t, "offline")
	e.svc.OnSession("offline")
	waitFor(t, 20*time.Second, "late host moved", func() bool { s, _ := e.state(t, "offline"); return s == StateReconciled })
}

func (e *env) agentHost2(t *testing.T, id string) {
	e.ag.mu.Lock()
	e.ag.views[id] = agentsvc.AgentView{IdentityID: "aid_" + id, IdentityStatus: "active", Sessions: 1, Compatible: true,
		Features: []string{"epoch-1"}, Epoch: prevEpoch,
		Domains: []*pb.DomainState{{Domain: "agent-config", Epoch: prevEpoch, Revision: 2, ContentHash: "sha256:2"}}}
	e.ag.mu.Unlock()
}

// TestTransitionAnswers: a refusal is never read as success; a changed
// agent is compared again, a pending firewall transaction waits.
func TestTransitionAnswers(t *testing.T) {
	e := newEnv(t, 2)
	e.agentHost(t, "moving", 2, 2)
	e.agentHost(t, "pending", 2, 2)
	var calls atomic.Int32
	e.ag.answer = func(hostID string, tr *pb.EpochTransition) *pb.EpochTransitionResult {
		switch hostID {
		case "pending":
			return &pb.EpochTransitionResult{TransitionId: tr.TransitionId, Code: "pending", Error: "a firewall transaction is pending"}
		}
		if calls.Add(1) == 1 {
			return &pb.EpochTransitionResult{TransitionId: tr.TransitionId, Code: "changed", Error: "agent-config changed"}
		}
		return &pb.EpochTransitionResult{TransitionId: tr.TransitionId, Ok: true, ControllerEpoch: tr.ToEpoch}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := e.svc.Start(ctx); err != nil {
		t.Fatal(err)
	}
	e.svc.OnSession("moving")
	e.svc.OnSession("pending")
	waitFor(t, 20*time.Second, "pending recorded", func() bool { s, why := e.state(t, "pending"); return s == StatePending && contains(why, "pending") })
	var epoch string
	e.st.Reader.QueryRow(`SELECT epoch FROM state_versions WHERE host_id = 'pending'`).Scan(&epoch)
	if epoch != prevEpoch {
		t.Errorf("a refused transition moved the controller's record: %s", epoch)
	}
	// "changed": compared again after a short wait, then moved.
	waitFor(t, 30*time.Second, "changed host moved on the second try", func() bool { s, _ := e.state(t, "moving"); return s == StateReconciled })
	if calls.Load() < 2 {
		t.Errorf("transitions sent: %d", calls.Load())
	}
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }

func actor() audit.Actor { return audit.Actor{Type: audit.ActorUser, ID: "usr_1", Name: "admin"} }
