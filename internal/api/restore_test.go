package api

import (
	"context"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"drool/internal/agent"
	"drool/internal/backup"
	"drool/internal/recovery"
	"drool/internal/store"
)

// TestRestoreReconciliation restores a controller from a backup while its
// Shepherds keep running (DESIGN §10.3, ARCHITECTURE-ALPHA §33.1): the real
// agent endpoint, real agents, the backup's database prepared exactly as
// drool restore prepares it, and the restored controller on the same
// endpoint.
func TestRestoreReconciliation(t *testing.T) {
	e := newAgentEnv(t)
	ctx := context.Background()
	enroll := func(name string) (string, agent.Paths) {
		t.Helper()
		tok := e.token(t, map[string]any{"preset": "full", "description": name})
		p := agentPaths(t, fullCaps(""))
		id, err := agent.Enroll(ctx, p, tok.Token, "v1.0.0")
		if err != nil {
			t.Fatal(err)
		}
		return id.HostID, p
	}
	applied := func(p agent.Paths, rev uint64) func() bool {
		return func() bool {
			st, err := agent.OpenStore(p.State)
			if err != nil {
				return false
			}
			d, _ := st.Domains()
			return d["agent-config"].Revision == rev
		}
	}
	// Agent-only hosts: nothing can fall back to SSH.
	hostSame, pSame := enroll("same")
	hostAhead, pAhead := enroll("ahead")
	hostRevoked, pRevoked := enroll("revoked")
	startAgent(t, pSame, "v1.0.0")
	startAgent(t, pAhead, "v1.0.0")
	agRevoked := startAgent(t, pRevoked, "v1.0.0")
	eventually(t, 10*time.Second, "agents connected", func() bool {
		return e.connected(hostSame) && e.connected(hostAhead) && e.connected(hostRevoked)
	})
	narrowed := fullCaps("")
	narrowed.Reboot = false
	for _, h := range []string{hostSame, hostAhead} {
		if r := e.c.do("PUT", "/api/v1/hosts/"+h+"/agent/caps", map[string]any{"caps": narrowed}); r.status != 204 {
			t.Fatalf("caps: %d %s", r.status, r.body)
		}
	}
	eventually(t, 10*time.Second, "configuration applied", func() bool { return applied(pSame, 1)() && applied(pAhead, 1)() })
	oldEpoch := e.h.agents.Epoch()

	// The backup.
	backupPath := filepath.Join(t.TempDir(), store.FileName)
	if _, err := e.h.st.Writer.ExecContext(ctx, `VACUUM INTO ?`, backupPath); err != nil {
		t.Fatal(err)
	}
	backupAt := time.Now()

	// After the backup: one host's configuration changes again, and a new
	// Shepherd enrolls.
	narrower := narrowed
	narrower.Packages.Enabled = false
	if r := e.c.do("PUT", "/api/v1/hosts/"+hostAhead+"/agent/caps", map[string]any{"caps": narrower}); r.status != 204 {
		t.Fatalf("caps: %d %s", r.status, r.body)
	}
	eventually(t, 10*time.Second, "second configuration applied", applied(pAhead, 2))
	_, pLate := enroll("late")
	late := startAgent(t, pLate, "v1.0.0")
	lateID, _ := agent.OpenStore(pLate.State)
	lateIdent, _ := lateID.Identity()
	// ... and one Shepherd is revoked (the restored database won't know).
	revokedStore, _ := agent.OpenStore(pRevoked.State)
	revokedIdent, _ := revokedStore.Identity()
	if r := e.c.do("POST", "/api/v1/agents/"+revokedIdent.IdentityID+"/revoke", map[string]any{"reason": "decommissioned", "leave_unmanaged": true}); r.status != 204 {
		t.Fatalf("revoke: %d %s", r.status, r.body)
	}
	eventually(t, 10*time.Second, "the Shepherd remembers its revocation", func() bool { return revokedStore.Revocation() != nil })
	agRevoked.stop()

	// The controller goes away; drool restore prepares the backup.
	e.h.agents.Stop()
	rst, err := store.Open(backupPath)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := rst.Writer.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	sum, err := backup.PrepareRestored(ctx, tx, &backup.Manifest{CreatedAt: backupAt}, "cli:test", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	rst.Close()
	if sum.PreviousEpoch != oldEpoch || sum.Epoch == oldEpoch {
		t.Fatalf("epochs: %+v (old %s)", sum, oldEpoch)
	}

	// The restored controller, on the same agent endpoint.
	h2 := newHarnessWith(t, harnessOpts{dbPath: backupPath, box: e.h.box})
	var ln net.Listener
	eventually(t, 5*time.Second, "agent port free again", func() bool {
		ln, err = net.Listen("tcp", e.addr)
		return err == nil
	})
	h2.agents.Serve(ln)
	t.Cleanup(h2.agents.Stop)
	h2.agents.SetAdvertise([]string{e.addr})
	if h2.agents.Epoch() != sum.Epoch {
		t.Fatalf("restored controller epoch %s, want %s", h2.agents.Epoch(), sum.Epoch)
	}
	c2 := h2.login("admin")
	e2 := &agentEnv{h: h2, c: c2, addr: e.addr}
	startAgent(t, pRevoked, "v1.0.0") // back without its revocation back-off
	recState := func(host string) recovery.HostRecord {
		var rec recovery.HostRecord
		c2.do("GET", "/api/v1/hosts/"+host+"/reconciliation", nil).json(t, &rec)
		return rec
	}

	// Identical state: compared and moved by itself.
	eventually(t, 30*time.Second, "unchanged host reconciled", func() bool { return recState(hostSame).State == recovery.StateReconciled })
	if st, _ := agent.OpenStore(pSame.State); st != nil {
		id, _ := st.Identity()
		d, _ := st.Domains()
		if id.ControllerEpoch != sum.Epoch || d["agent-config"].Epoch != sum.Epoch || d["agent-config"].Revision != 1 {
			t.Fatalf("agent after the transition: epoch %s, domains %+v", id.ControllerEpoch, d)
		}
	}
	if jh, _ := e2.runJob(t, hostSame, map[string]any{"type": "command", "command": "echo restored"}); jh.State != "succeeded" || jh.Transport != "agent" {
		t.Fatalf("job on the reconciled host: %+v", jh)
	}

	// A host with changes made after the backup waits for a decision; DROOL
	// sends it nothing until then, and says why.
	eventually(t, 30*time.Second, "changed host flagged", func() bool { return recState(hostAhead).State == recovery.StateAhead })
	rec := recState(hostAhead)
	var cfgCheck *recovery.DomainCheck
	for i := range rec.Domains {
		if rec.Domains[i].Domain == "agent-config" {
			cfgCheck = &rec.Domains[i]
		}
	}
	if cfgCheck == nil || cfgCheck.Verdict != recovery.VerdictHostAhead || cfgCheck.HostRevision != 2 || cfgCheck.ControllerRevision != 1 {
		t.Fatalf("ahead host's domains: %+v", rec.Domains)
	}
	jh, _ := e2.runJob(t, hostAhead, map[string]any{"type": "command", "command": "echo must-not-run"})
	if jh.State != "failed" || jh.ErrorClass != "ConfigurationError" || !strings.Contains(jh.ErrorMessage, "epoch of the backup") {
		t.Fatalf("job on an unreconciled host: %+v", jh)
	}
	if st, _ := agent.OpenStore(pAhead.State); st != nil {
		if d, _ := st.Domains(); d["agent-config"].Revision != 2 || d["agent-config"].Epoch != oldEpoch {
			t.Fatalf("the host's newer state was touched: %+v", d)
		}
	}

	// Accepting the host's state moves it without changing it.
	if r := c2.do("POST", "/api/v1/hosts/"+hostAhead+"/reconciliation/accept", nil); r.status != 200 {
		t.Fatalf("accept: %d %s", r.status, r.body)
	}
	eventually(t, 30*time.Second, "accepted host reconciled", func() bool { return recState(hostAhead).State == recovery.StateReconciled })
	if st, _ := agent.OpenStore(pAhead.State); st != nil {
		if d, _ := st.Domains(); d["agent-config"].Revision != 2 || d["agent-config"].Epoch != sum.Epoch {
			t.Fatalf("accepted host's state: %+v", d)
		}
	}
	ha := e2.hostAgent(t, hostAhead)
	for _, sv := range ha.StateVersions {
		if sv.Domain == "agent-config" && (sv.Revision != 2 || sv.Status != "ok") {
			t.Fatalf("controller's record after accepting: %+v", ha.StateVersions)
		}
	}
	if jh, _ := e2.runJob(t, hostAhead, map[string]any{"type": "command", "command": "true"}); jh.State != "succeeded" {
		t.Fatalf("job after accepting: %+v", jh)
	}

	// The Shepherd revoked after the backup says so, and is blocked: it
	// stays in the backup's epoch and refuses this controller's commands.
	eventually(t, 30*time.Second, "revoked Shepherd blocked", func() bool {
		r := recState(hostRevoked)
		return r.State == recovery.StateBlocked && strings.Contains(r.Reason, "revoked its identity") && strings.Contains(r.Reason, "decommissioned")
	})
	if jh, _ := e2.runJob(t, hostRevoked, map[string]any{"type": "command", "command": "echo must-not-run"}); jh.State != "failed" {
		t.Fatalf("job on a Shepherd revoked after the backup: %+v", jh)
	}
	if r := c2.do("POST", "/api/v1/agents/"+revokedIdent.IdentityID+"/revoke", map[string]any{"reason": "revoked again after the restore", "leave_unmanaged": true}); r.status != 204 {
		t.Fatalf("revoke again: %d %s", r.status, r.body)
	}

	// The Shepherd enrolled after the backup is refused and listed.
	var unknown struct {
		Items []struct {
			IdentityID string `json:"identity_id"`
			Hostname   string `json:"hostname"`
		} `json:"items"`
	}
	eventually(t, 30*time.Second, "unknown Shepherd listed", func() bool {
		c2.do("GET", "/api/v1/agents/unknown", nil).json(t, &unknown)
		return len(unknown.Items) == 1 && unknown.Items[0].IdentityID == lateIdent.IdentityID && unknown.Items[0].Hostname != ""
	})
	late.stop()

	// A blocked host keeps the reconciliation open until it is finished by
	// hand.
	var status recovery.Status
	c2.do("GET", "/api/v1/system/restore", nil).json(t, &status)
	if status.Restore == nil || status.Restore.Status != "reconciling" || status.Counts[recovery.StateReconciled] != 2 || status.Counts[recovery.StateBlocked] != 1 {
		t.Fatalf("restore status: %+v %+v", status.Restore, status.Counts)
	}
	if r := c2.do("POST", "/api/v1/system/restore/finish", nil); r.status != 200 {
		t.Fatalf("finish: %d %s", r.status, r.body)
	}
	c2.do("GET", "/api/v1/system/restore", nil).json(t, &status)
	if status.Restore.Status != "finished" || status.Restore.FinishedBy != "admin" {
		t.Fatalf("after finishing: %+v", status.Restore)
	}
	var au struct {
		Items []struct {
			Action string `json:"action"`
		} `json:"items"`
	}
	c2.do("GET", "/api/v1/audit?action=restore.&limit=50", nil).json(t, &au)
	seen := map[string]int{}
	for _, it := range au.Items {
		seen[it.Action]++
	}
	if seen["restore.reconcile"] < 4 || seen["restore.accept"] != 1 || seen["restore.finish"] != 1 {
		t.Fatalf("audit: %v", seen)
	}
}
