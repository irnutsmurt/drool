package artifacts

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"drool/internal/store"
)

func newStore(t *testing.T) (*Store, *store.Store, string) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, store.FileName))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	s, err := Open(dir, st)
	if err != nil {
		t.Fatal(err)
	}
	return s, st, dir
}

func TestPutDedupesAndVerifies(t *testing.T) {
	s, _, dir := newStore(t)
	ctx := context.Background()
	a, err := s.Put(ctx, strings.NewReader("server_tokens off;\n"), "nginx.conf", "")
	if err != nil {
		t.Fatal(err)
	}
	if !a.IsText || a.Size != 19 || a.Name != "nginx.conf" || !ValidHash(a.SHA256) {
		t.Fatalf("artifact %+v", a)
	}
	b, err := s.Put(ctx, strings.NewReader("server_tokens off;\n"), "../../evil", "")
	if err != nil || b.SHA256 != a.SHA256 || b.Name != "nginx.conf" {
		t.Fatalf("dedupe: %+v %v", b, err)
	}
	fi, err := os.Stat(filepath.Join(dir, RelPath(a.SHA256)))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("blob: %v %v", fi, err)
	}
	bin, _ := s.Put(ctx, strings.NewReader("\x00\x01binary"), "x.bin", "")
	if bin.IsText {
		t.Fatal("binary marked as text")
	}
	empty, err := s.Put(ctx, strings.NewReader(""), "empty", "")
	if err != nil || empty.Size != 0 || empty.SHA256 != "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" {
		t.Fatalf("zero bytes: %+v %v", empty, err)
	}

	// Corruption on disk is caught while reading.
	os.WriteFile(filepath.Join(dir, RelPath(a.SHA256)), []byte("server_tokens on;;\n"), 0o600)
	rc, _, err := s.Open(ctx, a.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadAll(rc); err == nil || !strings.Contains(err.Error(), "corrupted") {
		t.Fatalf("corruption not detected: %v", err)
	}
	rc.Close()
	if _, err := s.Get(ctx, "not-a-hash"); err != ErrNotFound {
		t.Fatalf("bad hash: %v", err)
	}
}

func TestGCKeepsReferenced(t *testing.T) {
	s, st, dir := newStore(t)
	ctx := context.Background()
	kept, _ := s.Put(ctx, strings.NewReader("history\n"), "a", "")
	inJob, _ := s.Put(ctx, strings.NewReader("queued job\n"), "b", "")
	gone, _ := s.Put(ctx, strings.NewReader("forgotten\n"), "c", "")
	fresh, _ := s.Put(ctx, strings.NewReader("just uploaded\n"), "d", "")
	for _, stmt := range []string{
		`INSERT INTO users (id, username, password_hash, role, created_at, updated_at, password_changed_at) VALUES ('u','u','x','admin',0,0,0)`,
		`INSERT INTO hosts (id, name, connect_address, ssh_user, created_at, updated_at) VALUES ('h','h','x','x',0,0)`,
		`INSERT INTO file_deployments (id, host_id, path, kind, outcome, before_exists, after_sha256, after_artifact, created_at)
		 VALUES ('d1','h','/etc/x','deploy','installed',0,'` + kept.SHA256 + `','` + kept.SHA256 + `',0)`,
		`INSERT INTO jobs (id, name, spec_json, spec_version, status, created_by, created_at)
		 VALUES ('j1','j','{"steps":[{"type":"file_deploy","artifact":"` + inJob.SHA256 + `"}]}',1,'pending','u',0)`,
	} {
		if _, err := st.Writer.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Now().Add(-48 * time.Hour).UnixMilli()
	for _, a := range []string{kept.SHA256, inJob.SHA256, gone.SHA256} {
		st.Writer.Exec(`UPDATE artifacts SET last_used_at = ? WHERE sha256 = ?`, old, a)
	}
	n, err := s.GC(ctx, 24*time.Hour)
	if err != nil || n != 1 {
		t.Fatalf("gc removed %d: %v", n, err)
	}
	for sha, want := range map[string]bool{kept.SHA256: true, inJob.SHA256: true, gone.SHA256: false, fresh.SHA256: true} {
		_, err := os.Stat(filepath.Join(dir, RelPath(sha)))
		if (err == nil) != want {
			t.Errorf("%s present=%v, want %v", sha[:8], err == nil, want)
		}
	}
}
