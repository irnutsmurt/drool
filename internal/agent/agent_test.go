package agent

import (
	"crypto/ed25519"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"drool/internal/agentcaps"
	"drool/internal/agentpki"
	"drool/internal/agentrpc"
	"drool/internal/model"
)

func testAgent(t *testing.T, caps agentcaps.Caps) *Agent {
	t.Helper()
	p := Under(t.TempDir())
	os.MkdirAll(filepath.Dir(p.Config), 0o755)
	os.WriteFile(p.Config, agentcaps.Render(caps, ""), 0o600)
	a, err := New(Options{Paths: p, Version: "v1.0.0"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	// A minimal enrollment record (no network needed for admission).
	root, _ := agentpki.NewRoot("t", time.Now())
	inter, _ := agentpki.NewIntermediate(root, "1", time.Now())
	sm, _ := agentpki.SignManifest(root, agentpki.Manifest{Version: 1, TrustedIntermediates: []string{agentpki.Fingerprint(inter.Cert.Raw)}})
	key, _ := a.store.IdentityKey(true)
	c, _ := agentpki.IssueAgent(inter, "aid_1", key.Public().(ed25519.PublicKey), time.Hour, time.Now())

	if err := a.store.SaveEnrollment(Identity{IdentityID: "aid_1", HostID: "host_1", ControllerEpoch: "epoch-A"}, root.Cert,
		[][]byte{c.Raw, inter.Cert.Raw}, sm, Endpoints{Primary: "x:1"}); err != nil {
		t.Fatal(err)
	}
	return a
}

func cmdEnv(id string) *agentrpc.OpEnvelope {
	op := model.Operation{Type: model.OpCommand, Command: &model.CommandOp{Command: "echo secret-password-123"}}
	return &agentrpc.OpEnvelope{OpID: id, IssuedAt: time.Now(), ExpiresAt: time.Now().Add(time.Minute), Epoch: "epoch-A",
		Operation: &op, Purpose: agentrpc.PurposeOp, TimeoutS: 30}
}

func TestAdmission(t *testing.T) {
	full, _ := agentcaps.Preset(agentcaps.PresetFull)
	a := testAgent(t, full)

	if r := a.admit(cmdEnv("op_1")); r != nil {
		t.Fatalf("valid op rejected: %+v", r)
	}
	// Duplicate / replay.
	if r := a.admit(cmdEnv("op_1")); r == nil || r.Reason != agentrpc.RejectDuplicate {
		t.Errorf("duplicate: %+v", r)
	}
	// Expired.
	e := cmdEnv("op_2")
	e.ExpiresAt = time.Now().Add(-time.Second)
	if r := a.admit(e); r == nil || r.Reason != agentrpc.RejectExpired {
		t.Errorf("expired: %+v", r)
	}
	// From another controller epoch.
	e = cmdEnv("op_3")
	e.Epoch = "epoch-B"
	if r := a.admit(e); r == nil || r.Reason != agentrpc.RejectEpoch {
		t.Errorf("foreign epoch: %+v", r)
	}
	// Stale revision: the agent applied files revision 5.
	a.store.ApplyDomain(agentrpc.StateVersion{Domain: "files", Epoch: "epoch-A", Revision: 5})
	for _, rev := range []uint64{4, 5} {
		e = cmdEnv("op_rev" + string(rune('0'+rev)))
		e.State = &agentrpc.StateVersion{Domain: "files", Epoch: "epoch-A", Revision: rev}
		if r := a.admit(e); r == nil || r.Reason != agentrpc.RejectStale {
			t.Errorf("revision %d: %+v", rev, r)
		}
	}
	e = cmdEnv("op_rev6")
	e.State = &agentrpc.StateVersion{Domain: "files", Epoch: "epoch-A", Revision: 6}
	if r := a.admit(e); r != nil {
		t.Errorf("newer revision rejected: %+v", r)
	}
	e = cmdEnv("op_epochstate")
	e.State = &agentrpc.StateVersion{Domain: "files", Epoch: "epoch-B", Revision: 99}
	if r := a.admit(e); r == nil || r.Reason != agentrpc.RejectEpoch {
		t.Errorf("state from another epoch: %+v", r)
	}
	// Issued far in the future (clock problems).
	e = cmdEnv("op_future")
	e.IssuedAt = time.Now().Add(time.Hour)
	if r := a.admit(e); r == nil || r.Reason != agentrpc.RejectInvalid {
		t.Errorf("future issue time: %+v", r)
	}
	// The duplicate check survives a restart (durable ledger).
	led, err := OpenLedger(a.paths.State)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := led.Get("op_1"); !ok {
		t.Error("ledger lost the operation id across a restart")
	}
	// An accepted op that never finished is marked interrupted on restart.
	if e, _ := led.Get("op_1"); e.State != OpInterrupted {
		t.Errorf("unfinished op after restart: %s", e.State)
	}
}

func TestAdmissionCapabilities(t *testing.T) {
	mon, _ := agentcaps.Preset(agentcaps.PresetMonitor)
	a := testAgent(t, mon)
	r := a.admit(cmdEnv("op_x"))
	if r == nil || r.Reason != agentrpc.RejectCapability || r.Capability != "shell" {
		t.Fatalf("command on a monitor-only agent: %+v", r)
	}
	// A refused operation is not recorded: it never ran.
	if _, ok := a.ledger.Get("op_x"); ok {
		t.Error("a refused operation was recorded as accepted")
	}
	op := model.Operation{Type: model.OpService, Service: &model.ServiceOp{Name: "nginx", State: "restarted"}}
	e := cmdEnv("op_svc")
	e.Operation = &op
	if r := a.admit(e); r == nil || r.Reason != agentrpc.RejectCapability {
		t.Errorf("service on a monitor-only agent: %+v", r)
	}
	e = &agentrpc.OpEnvelope{OpID: "op_files", IssuedAt: time.Now(), ExpiresAt: time.Now().Add(time.Minute), Epoch: "epoch-A", Purpose: agentrpc.PurposeFiles}
	if r := a.admit(e); r == nil || r.Reason != agentrpc.RejectCapability {
		t.Errorf("file session on a monitor-only agent: %+v", r)
	}
}

// TestStateQueryAdmission: the read-only package query (desired state,
// M14) is allowed by monitoring (or packages), refused with neither, and
// validates its names like a package operation.
func TestStateQueryAdmission(t *testing.T) {
	query := func(id string, names ...string) *agentrpc.OpEnvelope {
		return &agentrpc.OpEnvelope{OpID: id, IssuedAt: time.Now(), ExpiresAt: time.Now().Add(time.Minute), Epoch: "epoch-A",
			Purpose: agentrpc.PurposeQuery, Query: &agentrpc.StateQuery{Packages: names}, TimeoutS: 30}
	}
	mon, _ := agentcaps.Preset(agentcaps.PresetMonitor)
	a := testAgent(t, mon)
	if r := a.admit(query("q_1", "curl", "telnet")); r != nil {
		t.Fatalf("query on a monitor-only agent: %+v", r)
	}
	if r := a.admit(query("q_2", "bad name")); r == nil || r.Reason != agentrpc.RejectInvalid {
		t.Errorf("invalid name: %+v", r)
	}
	if r := a.admit(query("q_3")); r == nil || r.Reason != agentrpc.RejectInvalid {
		t.Errorf("empty query: %+v", r)
	}
	none := testAgent(t, agentcaps.Caps{})
	if r := none.admit(query("q_4", "curl")); r == nil || r.Reason != agentrpc.RejectCapability || r.Capability != "monitoring" {
		t.Errorf("query with neither monitoring nor packages: %+v", r)
	}
	pk := testAgent(t, agentcaps.Caps{Packages: agentcaps.Packages{Enabled: true}})
	if r := pk.admit(query("q_5", "curl")); r != nil {
		t.Errorf("query with packages only: %+v", r)
	}
	if got := opType(query("q_6", "curl")); got != "query" {
		t.Errorf("op type %q", got)
	}
}

func TestAbandonMakesUnknownSafe(t *testing.T) {
	full, _ := agentcaps.Preset(agentcaps.PresetFull)
	a := testAgent(t, full)
	// The controller lost the channel before any reply and asks: the agent
	// never saw the id, records it as abandoned...
	if _, seen := a.ledger.Abandon("op_late", time.Now().Add(time.Minute)); seen {
		t.Fatal("unknown id reported as seen")
	}
	// ...so a delayed copy arriving afterwards can't start.
	if r := a.admit(cmdEnv("op_late")); r == nil || r.Reason != agentrpc.RejectDuplicate {
		t.Fatalf("late copy after abandon: %+v", r)
	}
}

func TestOpLogHasNoSecrets(t *testing.T) {
	full, _ := agentcaps.Preset(agentcaps.PresetFull)
	a := testAgent(t, full)
	e := cmdEnv("op_log")
	a.oplog.Write(OpEvent{Event: "accepted", OpID: e.OpID, Type: opType(e), Summary: summary(e), OpHash: opHash(e)})
	a.oplog.Close()
	b, _ := os.ReadFile(filepath.Join(a.paths.Log, "operations.log"))
	if !strings.Contains(string(b), "op_log") {
		t.Fatalf("log: %s", b)
	}
	if strings.Contains(string(b), "secret-password-123") {
		t.Fatal("the operation log contains command text")
	}
	if fi, _ := os.Stat(filepath.Join(a.paths.Log, "operations.log")); fi.Mode().Perm() != 0o600 {
		t.Errorf("log mode %v", fi.Mode())
	}
}

func TestCeilingFileMustBeValid(t *testing.T) {
	p := Under(t.TempDir())
	if _, err := New(Options{Paths: p}); err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Errorf("missing ceiling: %v", err)
	}
	os.MkdirAll(filepath.Dir(p.Config), 0o755)
	os.WriteFile(p.Config, []byte("capabilities:\n  shel: true\n"), 0o600)
	if _, err := New(Options{Paths: p}); err == nil {
		t.Error("ceiling with a typo accepted")
	}
}
