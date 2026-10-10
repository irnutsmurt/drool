package backup

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"drool/internal/audit"
	"drool/internal/store"
)

// TestRestoreSetsAsideInProgressWork: a restore gives the controller a new
// epoch and never resumes or repeats what the backup recorded as in
// progress (DESIGN §10.3, ARCHITECTURE-ALPHA §33.1).
func TestRestoreSetsAsideInProgressWork(t *testing.T) {
	src := controller(t)
	st, err := store.Open(filepath.Join(src.DataDir, store.FileName))
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`INSERT INTO settings (key, value_json, updated_at) VALUES ('controller_epoch', '"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"', 0)`,
		`INSERT INTO jobs (id, name, spec_json, spec_version, status, created_by, created_at, started_at) VALUES ('job_run','patch','{}',1,'running','usr_1',1,1)`,
		`INSERT INTO jobs (id, name, spec_json, spec_version, status, created_by, created_at) VALUES ('job_wait','later','{}',1,'pending','usr_1',2)`,
		`INSERT INTO jobs (id, name, spec_json, spec_version, status, created_by, created_at, finished_at) VALUES ('job_done','done','{}',1,'completed','usr_1',0,1)`,
		`INSERT INTO job_hosts (job_id, host_id, host_name, position, state) VALUES ('job_run','h1','web1',0,'running')`,
		`INSERT INTO job_hosts (job_id, host_id, host_name, position, state) VALUES ('job_wait','h1','web1',0,'queued')`,
		`INSERT INTO job_hosts (job_id, host_id, host_name, position, state, finished_at) VALUES ('job_done','h1','web1',0,'succeeded',1)`,
		`INSERT INTO job_steps (job_id, host_id, step_index, op_type, state, started_at) VALUES ('job_run','h1',0,'command','running',1)`,
		`INSERT INTO fw_transactions (id, host_id, state, epoch, revision, candidate_hash, timeout_s, candidate, created_at, updated_at)
			VALUES ('fwtx_open','h1','pending','aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',4,'abc',60,'table inet drool {}',1,1)`,
		`INSERT INTO fw_transactions (id, host_id, state, epoch, revision, candidate_hash, timeout_s, candidate, created_at, updated_at, finished_at)
			VALUES ('fwtx_done','h1','committed','aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',3,'abd',60,'table inet drool {}',0,0,0)`,
		`INSERT INTO notify_deliveries (id, dedupe_key, destination_name, destination_kind, kind, message_json, status, next_attempt_at, created_at)
			VALUES ('ndel_1','k1','ops','webhook','opening','{}','queued',0,1)`,
		`INSERT INTO notify_deliveries (id, dedupe_key, destination_name, destination_kind, kind, message_json, status, next_attempt_at, created_at, finished_at)
			VALUES ('ndel_2','k2','ops','webhook','opening','{}','delivered',0,0,1)`,
		`INSERT INTO schedules (id, kind, name, enabled, revision, config_json, next_run_at, created_at, updated_at) VALUES ('sch_on','job','nightly',1,3,'{}',5,0,0)`,
		`INSERT INTO schedules (id, kind, name, enabled, config_json, created_at, updated_at) VALUES ('sch_off','job','manual',0,'{}',0,0)`,
		`INSERT INTO schedules (id, kind, name, enabled, config_json, created_at, updated_at) VALUES ('sch_mnt','maintenance','window',1,'{}',0,0)`,
		`INSERT INTO schedule_occurrences (id, schedule_id, schedule_revision, scheduled_for, status, created_at) VALUES ('occ_q','sch_on',3,4,'queued',4)`,
		`INSERT INTO ds_baselines (id, name, revision, auto_enabled, created_at, updated_at) VALUES ('bl_auto','base',1,1,0,0)`,
		`INSERT INTO ds_baselines (id, name, revision, auto_enabled, created_at, updated_at) VALUES ('bl_man','manual',1,0,0,0)`,
		`INSERT INTO agent_rollouts (id, version, status, canary, batch_size, max_failures, health_window_s, created_at, updated_at)
			VALUES ('arol_1','1.2.3','running',1,5,1,60,0,0)`,
	} {
		if _, err := st.Writer.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	st.Close()
	archive := makeBackup(t, src, true)

	dst := fresh(t)
	res, err := Restore(context.Background(), dst, bytes.NewReader(archive), RestoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	rc := res.Reconcile
	if rc == nil || rc.PreviousEpoch != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" || len(rc.Epoch) != 32 || rc.Epoch == rc.PreviousEpoch {
		t.Fatalf("epochs: %+v", rc)
	}
	if rc.JobsInterrupted != 2 || rc.HostsInterrupted != 2 || rc.FirewallClosed != 1 || rc.DeliveriesDropped != 1 ||
		len(rc.SchedulesHeld) != 1 || rc.OccurrencesDrop != 1 || len(rc.AutoPaused) != 1 || rc.RolloutsPaused != 1 || rc.Hosts != 1 {
		t.Fatalf("summary: %+v", rc)
	}

	st, err = store.Open(filepath.Join(dst.DataDir, store.FileName))
	if err != nil {
		t.Fatal(err)
	}
	one := func(q string, args ...any) string {
		t.Helper()
		var s string
		if err := st.Reader.QueryRow(q, args...).Scan(&s); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		return s
	}
	if ep := one(`SELECT value_json FROM settings WHERE key = 'controller_epoch'`); ep != `"`+rc.Epoch+`"` {
		t.Errorf("epoch setting %s", ep)
	}
	// Jobs: never resumed, never re-run; the outcome is unknown and says why.
	if s := one(`SELECT group_concat(id || ':' || status) FROM (SELECT id, status FROM jobs ORDER BY id)`); s != "job_done:completed,job_run:interrupted,job_wait:interrupted" {
		t.Errorf("jobs: %s", s)
	}
	if s := one(`SELECT error_message FROM job_hosts WHERE job_id = 'job_run'`); !strings.Contains(s, "restored from that backup") || !strings.Contains(s, "not run again automatically") {
		t.Errorf("job host message: %s", s)
	}
	if s := one(`SELECT state FROM job_steps WHERE job_id = 'job_run'`); s != "interrupted" {
		t.Errorf("step: %s", s)
	}
	if s := one(`SELECT state FROM job_hosts WHERE job_id = 'job_done'`); s != "succeeded" {
		t.Errorf("finished job host touched: %s", s)
	}
	// The open firewall transaction is closed as unknown, the settled one untouched.
	if s := one(`SELECT state || ':' || closed_reason FROM fw_transactions WHERE id = 'fwtx_open'`); s != "unknown:restore" {
		t.Errorf("open transaction: %s", s)
	}
	if s := one(`SELECT state || ':' || closed_reason FROM fw_transactions WHERE id = 'fwtx_done'`); s != "committed:" {
		t.Errorf("committed transaction: %s", s)
	}
	if s := one(`SELECT status || ':' || ambiguous FROM notify_deliveries WHERE id = 'ndel_1'`); s != "cancelled:1" {
		t.Errorf("queued delivery: %s", s)
	}
	if s := one(`SELECT status FROM notify_deliveries WHERE id = 'ndel_2'`); s != "delivered" {
		t.Errorf("delivered delivery: %s", s)
	}
	// Job schedules are held with a note and a marker for the gap;
	// maintenance windows and disabled schedules are left alone.
	if s := one(`SELECT enabled || ':' || attention FROM schedules WHERE id = 'sch_on'`); !strings.HasPrefix(s, "0:Held after a controller restore") {
		t.Errorf("held schedule: %s", s)
	}
	if s := one(`SELECT enabled || ':' || attention FROM schedules WHERE id = 'sch_mnt'`); s != "1:" {
		t.Errorf("maintenance schedule: %s", s)
	}
	if s := one(`SELECT enabled || ':' || attention FROM schedules WHERE id = 'sch_off'`); s != "0:" {
		t.Errorf("disabled schedule: %s", s)
	}
	if s := one(`SELECT status || ':' || schedule_revision FROM schedule_occurrences WHERE schedule_id = 'sch_on' AND status = 'missed'`); s != "missed:3" {
		t.Errorf("gap marker: %s", s)
	}
	if s := one(`SELECT status FROM schedule_occurrences WHERE id = 'occ_q'`); s != "skipped" {
		t.Errorf("queued run: %s", s)
	}
	if s := one(`SELECT attention FROM ds_baselines WHERE id = 'bl_auto'`); !strings.HasPrefix(s, "Paused after a controller restore") {
		t.Errorf("automatic restore: %s", s)
	}
	if s := one(`SELECT attention FROM ds_baselines WHERE id = 'bl_man'`); s != "" {
		t.Errorf("manual baseline: %s", s)
	}
	if s := one(`SELECT status || ':' || paused_reason FROM agent_rollouts WHERE id = 'arol_1'`); !strings.HasPrefix(s, "paused:paused after a controller restore") {
		t.Errorf("rollout: %s", s)
	}
	// The restore and every host's reconciliation are recorded.
	if s := one(`SELECT status || ':' || epoch || ':' || previous_epoch FROM controller_restores WHERE id = ?`, rc.RestoreID); s != "reconciling:"+rc.Epoch+":"+rc.PreviousEpoch {
		t.Errorf("restore record: %s", s)
	}
	if s := one(`SELECT state FROM host_reconciliation WHERE restore_id = ? AND host_id = 'h1'`, rc.RestoreID); s != "pending" {
		t.Errorf("host reconciliation: %s", s)
	}
	var details map[string]any
	json.Unmarshal([]byte(one(`SELECT details_json FROM audit_events WHERE action = 'backup.restore'`)), &details)
	if details["restore_id"] != rc.RestoreID || details["epoch"] != rc.Epoch || details["jobs_interrupted"] != float64(2) {
		t.Errorf("audit details: %v", details)
	}
	// The audit history still verifies, the restore recorded in its chain.
	if rep, err := audit.Verify(context.Background(), st.Reader, audit.VerifyOptions{}); err != nil || !rep.OK || rep.Records == 0 {
		t.Errorf("audit chain after the restore: %+v %v", rep, err)
	}

	st.Close()

	// A backup taken during that reconciliation, restored again: the
	// earlier reconciliation is superseded, and the epoch moves again.
	archive2 := makeBackup(t, dst, true)
	again := fresh(t)
	res2, err := Restore(context.Background(), again, bytes.NewReader(archive2), RestoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res2.Reconcile.PreviousEpoch != rc.Epoch || res2.Reconcile.Epoch == rc.Epoch {
		t.Fatalf("second restore epochs: %+v", res2.Reconcile)
	}
	st2, err := store.Open(filepath.Join(again.DataDir, store.FileName))
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	var states string
	st2.Reader.QueryRow(`SELECT group_concat(id || ':' || status, ',') FROM (SELECT id, status FROM controller_restores ORDER BY restored_at)`).Scan(&states)
	if states != rc.RestoreID+":finished,"+res2.Reconcile.RestoreID+":reconciling" {
		t.Fatalf("restores: %s", states)
	}
}
