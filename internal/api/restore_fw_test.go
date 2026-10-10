package api

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"drool/internal/backup"
	"drool/internal/firewall/hostfw"
	"drool/internal/recovery"
	"drool/internal/store"
)

// committedOn reads the firewall tool's committed state on a (fake) host.
func committedOn(t *testing.T, p hostfw.Paths) (epoch string, rev int64) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(filepath.Dir(p.ToolDir), "state", "committed.json"))
	if err != nil {
		t.Fatal(err)
	}
	var c struct {
		Epoch    string `json:"controller_epoch"`
		Revision int64  `json:"revision"`
	}
	if err := json.Unmarshal(b, &c); err != nil {
		t.Fatalf("committed.json: %v\n%s", err, b)
	}
	return c.Epoch, c.Revision
}

// TestRestoreReconciliationFirewall: committed firewall policies on
// Shepherd hosts and on an SSH host are moved to the restored
// controller's epoch without a rule changing; a host whose firewall was
// changed after the backup is never rolled back to the backup's policy.
// The tooling is the real drool-fw against fake nftables (firewall_test.go).
func TestRestoreReconciliationFirewall(t *testing.T) {
	e := newFwEnv(t)
	ctx := context.Background()
	hostA, _, _, fpA := e.agentFwHost(t, "agent1", fullCaps(""))
	hostB, _, _, fpB := e.agentFwHost(t, "agent2", fullCaps(""))
	hostS, _ := e.sshFwHost(t, "ssh1")
	targets := []any{}
	for _, h := range []string{hostA, hostB, hostS} {
		e.setHost(t, h, map[string]any{"mode": "enforce", "rollback_timeout_s": 30})
		targets = append(targets, map[string]any{"kind": "host", "id": h})
	}
	e.policy(t, map[string]any{"name": "web-in", "action": "allow", "from": []any{map[string]any{"kind": "internet"}},
		"to": targets, "services": []string{"https"}})
	for _, h := range []string{hostA, hostB, hostS} {
		if jh, tx := e.apply(t, h, nil); jh.State != "succeeded" || tx.State != "committed" || tx.Revision != 1 {
			t.Fatalf("apply on %s: %+v %+v\n%s", h, jh, tx, tx.timeline())
		}
	}
	oldEpoch := e.h.agents.Epoch()

	backupPath := filepath.Join(t.TempDir(), store.FileName)
	if _, err := e.h.st.Writer.ExecContext(ctx, `VACUUM INTO ?`, backupPath); err != nil {
		t.Fatal(err)
	}
	backupAt := time.Now()
	// After the backup, one host's firewall changes again.
	e.policy(t, map[string]any{"name": "ssh-in", "action": "allow", "from": []any{map[string]any{"kind": "internet"}},
		"to": []any{map[string]any{"kind": "host", "id": hostB}}, "services": []string{"ssh"}})
	if jh, tx := e.apply(t, hostB, nil); jh.State != "succeeded" || tx.Revision != 2 {
		t.Fatalf("second apply: %+v %+v", jh, tx)
	}

	e.h.agents.Stop()
	rst, err := store.Open(backupPath)
	if err != nil {
		t.Fatal(err)
	}
	tx, _ := rst.Writer.BeginTx(ctx, nil)
	sum, err := backup.PrepareRestored(ctx, tx, &backup.Manifest{CreatedAt: backupAt}, "cli:test", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	tx.Commit()
	rst.Close()

	h2 := newHarnessWith(t, harnessOpts{dbPath: backupPath, box: e.h.box, fwSSH: &e.h.fwSSH, fwFake: e.h.fwFake})
	var ln net.Listener
	eventually(t, 5*time.Second, "agent port free again", func() bool {
		ln, err = net.Listen("tcp", e.addr)
		return err == nil
	})
	h2.agents.Serve(ln)
	t.Cleanup(h2.agents.Stop)
	h2.agents.SetAdvertise([]string{e.addr})
	c2 := h2.login("admin")
	e2 := &fwEnv{&agentEnv{h: h2, c: c2, addr: e.addr}}
	recState := func(host string) recovery.HostRecord {
		var rec recovery.HostRecord
		c2.do("GET", "/api/v1/hosts/"+host+"/reconciliation", nil).json(t, &rec)
		return rec
	}

	eventually(t, 60*time.Second, "unchanged hosts reconciled", func() bool {
		return recState(hostA).State == recovery.StateReconciled && recState(hostS).State == recovery.StateReconciled
	})
	for name, p := range map[string]hostfw.Paths{"agent": fpA, "ssh": h2.fwSSH} {
		if ep, rev := committedOn(t, p); ep != sum.Epoch || rev != 1 {
			t.Errorf("%s host's committed policy: epoch %s revision %d, want %s/1", name, ep, rev, sum.Epoch)
		}
	}
	// The changed host keeps its newer policy and waits for a decision.
	eventually(t, 60*time.Second, "changed host flagged", func() bool { return recState(hostB).State == recovery.StateAhead })
	if ep, rev := committedOn(t, fpB); ep != oldEpoch || rev != 2 {
		t.Fatalf("the changed host's firewall was touched: %s/%d", ep, rev)
	}
	if jh, _ := e2.apply(t, hostB, nil); jh.State != "failed" {
		t.Fatalf("firewall change on an unreconciled host: %+v", jh)
	}
	if ep, rev := committedOn(t, fpB); ep != oldEpoch || rev != 2 {
		t.Fatalf("a refused apply changed the host: %s/%d", ep, rev)
	}

	// Reconciled hosts take new transactions in the new epoch.
	for _, h := range []string{hostA, hostS} {
		if jh, tx := e2.apply(t, h, nil); jh.State != "succeeded" || tx.State != "committed" || tx.Revision != 2 {
			t.Fatalf("apply after reconciliation on %s: %+v %+v\n%s", h, jh, tx, tx.timeline())
		}
	}
	if ep, rev := committedOn(t, fpA); ep != sum.Epoch || rev != 2 {
		t.Errorf("agent host after a new transaction: %s/%d", ep, rev)
	}

	// Accepting the changed host's state: moved as it is, and DROOL's next
	// transaction supersedes it.
	if r := c2.do("POST", "/api/v1/hosts/"+hostB+"/reconciliation/accept", nil); r.status != 200 {
		t.Fatalf("accept: %d %s", r.status, r.body)
	}
	eventually(t, 60*time.Second, "accepted host reconciled", func() bool { return recState(hostB).State == recovery.StateReconciled })
	if ep, rev := committedOn(t, fpB); ep != sum.Epoch || rev != 2 {
		t.Fatalf("accepted host's committed policy: %s/%d", ep, rev)
	}
	if jh, tx := e2.apply(t, hostB, nil); jh.State != "succeeded" || tx.Revision != 3 {
		t.Fatalf("apply after accepting: %+v %+v\n%s", jh, tx, tx.timeline())
	}
}
