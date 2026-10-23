package store

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

// Historical upgrades (M16, ARCHITECTURE-ALPHA §33.9): a database created by
// any earlier schema version, holding data written in that version's shape,
// upgrades to the current schema without losing a row, and the result
// passes SQLite's integrity and foreign-key checks.

// fixture rows, by the schema version whose shape they use (until: the
// first version whose shape no longer accepts them, 0: none).
var fixture = []struct {
	version int
	table   string
	sql     string
	until   int
}{
	{1, "users", `INSERT INTO users (id, username, password_hash, role, created_at, updated_at, password_changed_at) VALUES ('usr_1','admin','x','admin',1,1,1)`, 0},
	{1, "sessions", `INSERT INTO sessions (token_hash, user_id, csrf_token, created_at, last_seen_at, expires_at) VALUES (x'01','usr_1','c',1,1,9999999999999)`, 0},
	{1, "api_tokens", `INSERT INTO api_tokens (id, user_id, name, token_hash, created_at) VALUES ('tok_1','usr_1','ci','h1',1)`, 0},
	{1, "audit_events", `INSERT INTO audit_events (id, ts, actor_type, action, result) VALUES ('aud_00000000000000000000000001', 1, 'system', 'system.start', 'success')`, 0},
	{1, "settings", `INSERT INTO settings (key, value_json, updated_at) VALUES ('fixture', '"x"', 1)`, 0},
	{2, "setup_codes", `INSERT INTO setup_codes (id, kind, code_hash, created_at) VALUES ('setup_1','generated','h',1)`, 0},
	{3, "secrets", `INSERT INTO secrets (id, name, kind, ciphertext, nonce, key_version, created_at, updated_at) VALUES ('sec_1','deploy','ssh_private_key',x'00',x'00',1,1,1)`, 0},
	{3, "hosts", `INSERT INTO hosts (id, name, connect_address, ssh_user, ssh_credential_id, status, created_at, updated_at) VALUES ('h1','web1','10.0.0.5','deploy','sec_1','ready',1,1)`, 0},
	{3, "hosts", `INSERT INTO hosts (id, name, connect_address, ssh_user, created_at, updated_at) VALUES ('h2','db1','10.0.0.6','deploy',1,1)`, 0},
	{3, "ssh_host_keys", `INSERT INTO ssh_host_keys (id, host_id, key_type, public_key, fingerprint_sha256, status, first_seen_at, last_seen_at) VALUES ('hk_1','h1','ssh-ed25519','AAAA','SHA256:x','pinned',1,1)`, 0},
	{3, "host_tags", `INSERT INTO host_tags (host_id, tag) VALUES ('h1','role:web')`, 0},
	{3, "groups", `INSERT INTO groups (id, name, created_at, updated_at) VALUES ('g1','web',1,1)`, 0},
	{3, "group_members", `INSERT INTO group_members (group_id, host_id) VALUES ('g1','h1')`, 0},
	{4, "jobs", `INSERT INTO jobs (id, name, spec_json, spec_version, status, created_by, created_at, finished_at) VALUES ('job_1','patch','{}',1,'completed','usr_1',1,2)`, 0},
	{4, "job_hosts", `INSERT INTO job_hosts (job_id, host_id, host_name, position, state) VALUES ('job_1','h1','web1',0,'succeeded')`, 0},
	{4, "job_steps", `INSERT INTO job_steps (job_id, host_id, step_index, op_type, state, started_at) VALUES ('job_1','h1',0,'command','succeeded',1)`, 0},
	{4, "job_output", `INSERT INTO job_output (job_id, host_id, step_index, seq, stream, ts, chunk) VALUES ('job_1','h1',0,1,1,1,x'6f6b0a')`, 0},
	{5, "host_facts", `INSERT INTO host_facts (host_id, collected_at) VALUES ('h1',1)`, 0},
	{6, "artifacts", `INSERT INTO artifacts (sha256, size, created_at, last_used_at) VALUES ('` + strings.Repeat("ab", 32) + `',3,1,1)`, 0},
	{6, "file_deployments", `INSERT INTO file_deployments (id, host_id, path, kind, outcome, before_exists, created_at) VALUES ('fdep_1','h1','/etc/app.conf','deploy','installed',0,1)`, 0},
	{7, "agent_identities", `INSERT INTO agent_identities (id, host_id, public_key, fingerprint, status, enrolled_at) VALUES ('aid_1','h2',x'01','fp','active',1)`, 0},
	{7, "state_versions", `INSERT INTO state_versions (host_id, domain, epoch, revision, updated_at) VALUES ('h2','agent-config','e1',2,1)`, 0},
	{7, "agent_rollouts", `INSERT INTO agent_rollouts (id, version, status, canary, batch_size, max_failures, health_window_s, created_at, updated_at) VALUES ('arol_1','1.0','completed',1,5,1,60,1,1)`, 0},
	{8, "fw_policies", `INSERT INTO fw_policies (id, name, action, from_json, to_json, services_json, position, created_at, updated_at) VALUES ('fwp_1','web-in','allow','[]','[]','[]',0,1,1)`, 0},
	{8, "fw_transactions", `INSERT INTO fw_transactions (id, host_id, state, epoch, revision, candidate_hash, timeout_s, candidate, created_at, updated_at) VALUES ('fwtx_1','h1','committed','e1',1,'abc',60,'table inet drool {}',1,1)`, 0},
	{8, "fw_observations", `INSERT INTO fw_observations (host_id, direction, protocol, port, peer, source, count, first_seen, last_seen) VALUES ('h1','in','tcp',443,'10.0.0.9','agent',3,1,2)`, 10},
	{10, "fw_observations", `INSERT INTO fw_observations (host_id, direction, protocol, port, peer, source, count, first_seen, last_seen) VALUES ('h1','in','tcp',443,'10.0.0.9','conntrack',3,1,2)`, 0},
	{9, "fw_networks", `INSERT INTO fw_networks (id, name, cidrs_json, created_at, updated_at) VALUES ('fwn_1','lan','["10.0.0.0/24"]',1,1)`, 0},
	{12, "alerts", `INSERT INTO alerts (id, host_id, host_name, issue_id, check_id, category, severity, peak_severity, state, title, issue_started_at, opened_at, updated_at) VALUES ('alr_1','h1','web1','iss_1','disk','disk','warning','warning','resolved','Disk',1,1,2)`, 0},
	{12, "maintenance", `INSERT INTO maintenance (id, started_at) VALUES ('mnt_1',1)`, 0},
	{12, "notify_destinations", `INSERT INTO notify_destinations (id, name, kind, config_json, created_at, updated_at) VALUES ('ndst_1','ops','webhook','{}',1,1)`, 0},
	{12, "notify_deliveries", `INSERT INTO notify_deliveries (id, dedupe_key, destination_name, destination_kind, kind, message_json, status, next_attempt_at, created_at) VALUES ('ndel_1','k','ops','webhook','opening','{}','delivered',1,1)`, 0},
	{12, "notify_deliveries", `INSERT INTO notify_deliveries (id, dedupe_key, destination_id, destination_name, destination_kind, kind, message_json, status, next_attempt_at, created_at) VALUES ('ndel_2','k2','ndst_1','ops','webhook','opening','{}','queued',1,1)`, 0},
	{12, "notify_route_destinations", `INSERT INTO notify_route_destinations (route_id, destination_id) VALUES ('nrt_default','ndst_1')`, 0},
	{14, "workflows", `INSERT INTO workflows (id, name, latest_revision, created_at, updated_at) VALUES ('wf_1','patch',1,1,1)`, 0},
	{14, "schedules", `INSERT INTO schedules (id, kind, name, config_json, created_at, updated_at) VALUES ('sch_1','job','nightly','{}',1,1)`, 0},
	{14, "schedule_occurrences", `INSERT INTO schedule_occurrences (id, schedule_id, schedule_revision, scheduled_for, status, created_at) VALUES ('occ_1','sch_1',1,1,'dispatched',1)`, 0},
	{15, "ds_baselines", `INSERT INTO ds_baselines (id, name, revision, created_at, updated_at) VALUES ('bl_1','base',1,1,1)`, 0},
	{15, "ds_reconciles", `INSERT INTO ds_reconciles (id, kind, job_id, status, items_json, created_at) VALUES ('rec_1','manual','job_1','done','[]',1)`, 0},
	{18, "ssh_user_cas", `INSERT INTO ssh_user_cas (id, name, public_key, fingerprint, state, created_at) VALUES ('sca_1','ca','ssh-ed25519 AAAA','SHA256:y','retired',1)`, 0},
	{19, "controller_restores", `INSERT INTO controller_restores (id, restored_at, backup_created_at, epoch, status) VALUES ('rst_1',1,0,'e2','finished')`, 0},
}

func TestUpgradeFromEverySchema(t *testing.T) {
	latest := LatestVersion()
	ctx := context.Background()
	for from := 1; from < latest; from++ {
		t.Run(fmt.Sprintf("from %d", from), func(t *testing.T) {
			st := openTemp(t)
			if _, err := st.MigrateTo(ctx, from); err != nil {
				t.Fatal(err)
			}
			if v, _ := st.SchemaVersion(ctx); v != from {
				t.Fatalf("at schema %d, want %d", v, from)
			}
			want := map[string]int{}
			for _, f := range fixture {
				if f.version > from || (f.until > 0 && from >= f.until) {
					continue
				}
				if _, err := st.Writer.Exec(f.sql); err != nil {
					t.Fatalf("schema %d fixture (%s, written for %d): %v", from, f.table, f.version, err)
				}
				want[f.table]++
			}
			// The fixture's update rollout (schema 7 on) is an installation
			// that updated its Shepherds by hand: 0026 writes its policy
			// down as manual.
			if from >= 7 && from < 26 {
				want["settings"]++
			}
			applied, err := st.Migrate(ctx)
			if err != nil {
				t.Fatalf("upgrade from %d: %v", from, err)
			}
			if len(applied) != latest-from {
				t.Errorf("applied %d migrations, want %d", len(applied), latest-from)
			}
			for table, n := range want {
				var got int
				if err := st.Reader.QueryRow(`SELECT count(*) FROM ` + table).Scan(&got); err != nil || got != n {
					t.Errorf("%s after the upgrade: %d rows (err %v), want %d", table, got, err, n)
				}
			}
			// Rebuilt tables keep what points at them (0025 rebuilt
			// notify_destinations).
			if from >= 12 {
				var linked int
				st.Reader.QueryRow(`SELECT count(*) FROM notify_deliveries WHERE id = 'ndel_2' AND destination_id = 'ndst_1'`).Scan(&linked)
				if linked != 1 {
					t.Errorf("a delivery lost its destination in the upgrade")
				}
			}
			var ok string
			st.Reader.QueryRow(`PRAGMA integrity_check`).Scan(&ok)
			if ok != "ok" {
				t.Errorf("integrity check: %s", ok)
			}
			rows, err := st.Reader.Query(`PRAGMA foreign_key_check`)
			if err != nil {
				t.Fatal(err)
			}
			for rows.Next() {
				var table, parent string
				var rowid, fk any
				rows.Scan(&table, &rowid, &parent, &fk)
				t.Errorf("foreign key violation after the upgrade: %s row %v → %s", table, rowid, parent)
			}
			rows.Close()
			// Every migration records the DROOL version that applied it.
			var unrecorded int
			st.Reader.QueryRow(`SELECT count(*) FROM schema_migrations WHERE version > ? AND drool_version = ''`, from).Scan(&unrecorded)
			if unrecorded != 0 {
				t.Errorf("%d migrations without the version that applied them", unrecorded)
			}
		})
	}
}

// withMigrations adds test migrations after the real ones.
func withMigrations(t *testing.T, extra map[string]string) {
	t.Helper()
	m := fstest.MapFS{}
	entries, err := fs.ReadDir(migrationFS, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		b, _ := fs.ReadFile(migrationFS, "migrations/"+e.Name())
		m["migrations/"+e.Name()] = &fstest.MapFile{Data: b}
	}
	for name, body := range extra {
		m["migrations/"+name] = &fstest.MapFile{Data: []byte(body)}
	}
	migrationSource = m
	t.Cleanup(func() { migrationSource = migrationFS })
}

// TestFailingMigrationChangesNothing: a migration that fails part-way
// leaves the database exactly at the previous version.
func TestFailingMigrationChangesNothing(t *testing.T) {
	st := openTemp(t)
	ctx := context.Background()
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	before := LatestVersion()
	withMigrations(t, map[string]string{
		fmt.Sprintf("%04d_broken.sql", before+1): `CREATE TABLE half_done (x INTEGER);
INSERT INTO half_done VALUES (1);
UPDATE settings SET value_json = 'changed';
INSERT INTO no_such_table VALUES (1);`,
	})
	st.Writer.Exec(`INSERT INTO settings (key, value_json, updated_at) VALUES ('k', '"orig"', 1)`)
	if _, err := st.Migrate(ctx); err == nil || !strings.Contains(err.Error(), "_broken.sql") {
		t.Fatalf("broken migration: %v", err)
	}
	if v, _ := st.SchemaVersion(ctx); v != before {
		t.Fatalf("schema version %d after a failed migration, want %d", v, before)
	}
	var n int
	st.Reader.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name = 'half_done'`).Scan(&n)
	var val string
	st.Reader.QueryRow(`SELECT value_json FROM settings WHERE key = 'k'`).Scan(&val)
	if n != 0 || val != `"orig"` {
		t.Fatalf("a failed migration left changes: table %d, setting %s", n, val)
	}
}

// TestInterruptedMigration kills a process in the middle of a migration
// (a power cut, an OOM kill, a container stop timing out): the database
// stays at the previous version, intact, and upgrades normally later.
func TestInterruptedMigration(t *testing.T) {
	if os.Getenv("DROOL_TEST_MIGRATE_CHILD") != "" {
		return // the child half runs in TestInterruptedMigrationChild
	}
	dir := t.TempDir()
	path := filepath.Join(dir, FileName)
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	st.Close()
	before := LatestVersion()

	cmd := exec.Command(os.Args[0], "-test.run=^TestInterruptedMigrationChild$", "-test.v")
	cmd.Env = append(os.Environ(), "DROOL_TEST_MIGRATE_CHILD="+path)
	out, _ := os.Create(filepath.Join(dir, "child.log"))
	cmd.Stdout, cmd.Stderr = out, out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	// Wait until the child is inside the slow migration's transaction.
	deadline := time.Now().Add(30 * time.Second)
	for {
		b, _ := os.ReadFile(filepath.Join(dir, "child.log"))
		if strings.Contains(string(b), "MIGRATING") {
			break
		}
		if time.Now().After(deadline) {
			cmd.Process.Kill()
			t.Fatalf("the child never started migrating:\n%s", b)
		}
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(500 * time.Millisecond)
	cmd.Process.Kill()
	cmd.Wait()
	out.Close()

	st, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	if v, _ := st.SchemaVersion(ctx); v != before {
		t.Fatalf("schema version %d after the kill, want %d", v, before)
	}
	var ok string
	st.Reader.QueryRow(`PRAGMA integrity_check`).Scan(&ok)
	var n int
	st.Reader.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name = 'started'`).Scan(&n)
	if ok != "ok" || n != 0 {
		t.Fatalf("after the kill: integrity %s, partial table %d", ok, n)
	}
	// The next start (without the test migration) is an ordinary one.
	if applied, err := st.Migrate(ctx); err != nil || len(applied) != 0 {
		t.Fatalf("migrate after the kill: %v %v", applied, err)
	}
}

func TestInterruptedMigrationChild(t *testing.T) {
	path := os.Getenv("DROOL_TEST_MIGRATE_CHILD")
	if path == "" {
		t.Skip("child half of TestInterruptedMigration")
	}
	// The migration writes, then computes for minutes (nothing big on disk).
	withMigrations(t, map[string]string{
		fmt.Sprintf("%04d_slow.sql", LatestVersion()+1): `CREATE TABLE started (x INTEGER);
INSERT INTO started VALUES (1);
CREATE TABLE slow AS WITH RECURSIVE c(x) AS (SELECT 1 UNION ALL SELECT x + 1 FROM c WHERE x < 4000000000) SELECT count(*) AS n FROM c;`,
	})
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Println("MIGRATING")
	if _, err := st.Migrate(context.Background()); err != nil && !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	t.Fatal("the slow migration finished before it was killed")
}

// Automatic Shepherd updates became the default in 1.0 (0026): a saved
// policy stays as it is, an installation that rolled out releases of its
// own without saving one keeps updating by hand, and one that never did
// gets the new default (no row).
func TestShepherdUpdatePolicyMigration(t *testing.T) {
	ctx := context.Background()
	policy := func(st *Store) string {
		var v string
		if err := st.Reader.QueryRow(`SELECT value_json FROM settings WHERE key = 'agent_updates'`).Scan(&v); err != nil {
			return ""
		}
		return v
	}
	for _, c := range []struct {
		name  string
		setup []string
		want  string // "" no row
	}{
		{"never used updates", nil, ""},
		{"rolled out its own releases", []string{
			`INSERT INTO agent_rollouts (id, version, status, canary, batch_size, max_failures, health_window_s, created_at, updated_at) VALUES ('arol_1','v1.1.0','completed',1,5,1,60,1,1)`},
			`"policy":"manual"`},
		{"uploaded a release", []string{
			`INSERT INTO artifacts (sha256, size, created_at, last_used_at) VALUES ('` + strings.Repeat("cd", 32) + `',3,1,1)`,
			`INSERT INTO agent_releases (id, version, channel, os, arch, protocol_min, protocol_max, sha256, size, key_id, manifest_payload, signature, created_at) VALUES ('arel_1','v1.1.0','stable','linux','amd64',1,1,'` + strings.Repeat("cd", 32) + `',3,'k',x'00',x'00',1)`},
			`"policy":"manual"`},
		{"saved automatic", []string{
			`INSERT INTO agent_rollouts (id, version, status, canary, batch_size, max_failures, health_window_s, created_at, updated_at) VALUES ('arol_1','v1.1.0','completed',1,5,1,60,1,1)`,
			`INSERT INTO settings (key, value_json, updated_at) VALUES ('agent_updates', '{"policy":"automatic","channel":"beta"}', 1)`},
			`"channel":"beta"`},
		{"install rollouts only", []string{
			`INSERT INTO agent_rollouts (id, version, status, canary, batch_size, max_failures, health_window_s, kind, created_at, updated_at) VALUES ('arol_1','v1.0.0','completed',1,5,1,60,'install',1,1)`},
			""},
	} {
		t.Run(c.name, func(t *testing.T) {
			st := openTemp(t)
			if _, err := st.MigrateTo(ctx, 25); err != nil {
				t.Fatal(err)
			}
			for _, q := range c.setup {
				if _, err := st.Writer.Exec(q); err != nil {
					t.Fatalf("%s: %v", q, err)
				}
			}
			if _, err := st.Migrate(ctx); err != nil {
				t.Fatal(err)
			}
			got := policy(st)
			if c.want == "" && got != "" || c.want != "" && !strings.Contains(got, c.want) {
				t.Errorf("policy after 0026: %q, want %q", got, c.want)
			}
		})
	}
}

// 0028: creating or renaming a Pack counts as a change to monitoring's
// health configuration (mon_health_gen), on a new database and on one
// upgraded from 27 with Packs already in it. Other changes to a Pack (its
// description) don't count, and the Packs that were there stay as they were.
func TestPackHealthGenMigration(t *testing.T) {
	ctx := context.Background()
	gen := func(st *Store) int {
		t.Helper()
		var g int
		if err := st.Reader.QueryRow(`SELECT gen FROM mon_health_gen WHERE id = 1`).Scan(&g); err != nil {
			t.Fatal(err)
		}
		return g
	}
	exec := func(st *Store, q string) {
		t.Helper()
		if _, err := st.Writer.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	check := func(t *testing.T, st *Store, existing string) {
		for _, name := range []string{"mon_health_gen_grp_ins", "mon_health_gen_grp_upd"} {
			var n int
			st.Reader.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type = 'trigger' AND name = ?`, name).Scan(&n)
			if n != 1 {
				t.Fatalf("trigger %s: %d", name, n)
			}
		}
		g := gen(st)
		exec(st, `INSERT INTO groups (id, name, created_at, updated_at) VALUES ('g_new','fresh',1,1)`)
		if gen(st) != g+1 {
			t.Fatalf("creating a Pack: gen %d → %d", g, gen(st))
		}
		exec(st, `UPDATE groups SET name = 'renamed' WHERE id = '`+existing+`'`)
		if gen(st) != g+2 {
			t.Fatalf("renaming a Pack: gen %d → %d", g+1, gen(st))
		}
		exec(st, `UPDATE groups SET description = 'notes' WHERE id = '`+existing+`'`)
		if gen(st) != g+2 {
			t.Fatalf("a description counted: gen %d", gen(st))
		}
	}
	t.Run("new database", func(t *testing.T) {
		st := openTemp(t)
		if _, err := st.Migrate(ctx); err != nil {
			t.Fatal(err)
		}
		exec(st, `INSERT INTO groups (id, name, created_at, updated_at) VALUES ('g1','web',1,1)`)
		check(t, st, "g1")
	})
	t.Run("upgraded from 27", func(t *testing.T) {
		st := openTemp(t)
		if _, err := st.MigrateTo(ctx, 27); err != nil {
			t.Fatal(err)
		}
		exec(st, `INSERT INTO hosts (id, name, connect_address, ssh_user, created_at, updated_at) VALUES ('h1','web1','10.0.0.5','deploy',1,1)`)
		exec(st, `INSERT INTO groups (id, name, created_at, updated_at) VALUES ('g1','web',1,1)`)
		exec(st, `INSERT INTO group_members (group_id, host_id) VALUES ('g1','h1')`)
		before := gen(st)
		applied, err := st.Migrate(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(applied) != LatestVersion()-27 {
			t.Fatalf("applied %v", applied)
		}
		if gen(st) != before {
			t.Fatalf("the upgrade itself changed gen: %d → %d", before, gen(st))
		}
		var name string
		var members int
		st.Reader.QueryRow(`SELECT name FROM groups WHERE id = 'g1'`).Scan(&name)
		st.Reader.QueryRow(`SELECT count(*) FROM group_members WHERE group_id = 'g1'`).Scan(&members)
		if name != "web" || members != 1 {
			t.Fatalf("the Pack after the upgrade: %q, %d members", name, members)
		}
		check(t, st, "g1")
	})
}
