package store

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func openTemp(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), FileName))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestMigrateIdempotent(t *testing.T) {
	st := openTemp(t)
	ctx := context.Background()
	applied, err := st.Migrate(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(applied) == 0 {
		t.Fatal("expected migrations to apply on a fresh database")
	}
	again, err := st.Migrate(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != 0 {
		t.Fatalf("second migrate applied %v", again)
	}
	v, err := st.SchemaVersion(ctx)
	if err != nil || v != LatestVersion() {
		t.Fatalf("schema version %d (err %v), want %d", v, err, LatestVersion())
	}
}

func TestRefusesNewerSchema(t *testing.T) {
	st := openTemp(t)
	ctx := context.Background()
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Writer.Exec(`INSERT INTO schema_migrations(version, name, applied_at) VALUES (9999, 'future', 0)`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Migrate(ctx); !errors.Is(err, ErrNewerSchema) {
		t.Fatalf("err = %v, want ErrNewerSchema", err)
	}
}

func TestAuditAppendOnly(t *testing.T) {
	st := openTemp(t)
	ctx := context.Background()
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Writer.Exec(`INSERT INTO audit_events (id, ts, actor_type, action, result) VALUES ('aud_1', 1, 'system', 'x', 'success')`); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`UPDATE audit_events SET action = 'y' WHERE id = 'aud_1'`,
		`DELETE FROM audit_events WHERE id = 'aud_1'`,
	} {
		_, err := st.Writer.Exec(q)
		if err == nil || !strings.Contains(err.Error(), "append-only") {
			t.Fatalf("%s: err = %v, want append-only error", q, err)
		}
	}
}

func TestReaderIsReadOnly(t *testing.T) {
	st := openTemp(t)
	if _, err := st.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Reader.Exec(`INSERT INTO settings(key, value_json, updated_at) VALUES ('k', '1', 0)`); err == nil {
		t.Fatal("reader pool accepted a write")
	}
}

func TestForeignKeysEnforced(t *testing.T) {
	st := openTemp(t)
	if _, err := st.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, err := st.Writer.Exec(`INSERT INTO sessions (token_hash, user_id, csrf_token, created_at, last_seen_at, expires_at)
		VALUES (x'00', 'usr_missing', 'c', 0, 0, 0)`)
	if err == nil {
		t.Fatal("expected foreign key violation")
	}
}
