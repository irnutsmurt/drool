package localfs

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"drool/internal/agentcaps"
	"drool/internal/files"
	"drool/internal/hostexec"
)

func code(err error) string {
	var fe *files.Error
	if errors.As(err, &fe) {
		return fe.Code
	}
	if err == nil {
		return ""
	}
	return "other: " + err.Error()
}

func sum(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

// tree builds:
//
//	allowed/        (the only allowed path)
//	allowed/a.txt
//	allowed/sub/
//	allowed/escape -> ../outside   (symlink leading out)
//	allowed/inner  -> sub          (symlink staying inside)
//	outside/secret.txt
func tree(t *testing.T) (string, *FS) {
	t.Helper()
	dir := t.TempDir()
	os.Chmod(dir, 0o755)
	allowed := filepath.Join(dir, "allowed")
	os.MkdirAll(filepath.Join(allowed, "sub"), 0o755)
	os.MkdirAll(filepath.Join(dir, "outside"), 0o755)
	os.WriteFile(filepath.Join(allowed, "a.txt"), []byte("hello\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "outside", "secret.txt"), []byte("secret\n"), 0o600)
	os.Symlink("../outside", filepath.Join(allowed, "escape"))
	os.Symlink("sub", filepath.Join(allowed, "inner"))
	caps := agentcaps.Caps{Files: agentcaps.Files{Enabled: true, AllowedPaths: []string{allowed}}}
	caps.Normalize()
	return dir, New(caps, hostexec.Local{})
}

func TestAllowedPathsCannotBeEscaped(t *testing.T) {
	dir, f := tree(t)
	ctx := context.Background()
	allowed := filepath.Join(dir, "allowed")

	if _, err := f.List(ctx, allowed); err != nil {
		t.Fatalf("list allowed: %v", err)
	}
	// Textually outside.
	if _, err := f.Stat(ctx, filepath.Join(dir, "outside", "secret.txt")); code(err) != "capability" {
		t.Errorf("outside path: %v", err)
	}
	// Through a symlink that leads out: refused by the kernel (RESOLVE_BENEATH).
	if _, _, err := f.Open(ctx, filepath.Join(allowed, "escape", "secret.txt")); code(err) != "capability" {
		t.Errorf("read through escaping symlink: %v", err)
	}
	if _, err := f.List(ctx, filepath.Join(allowed, "escape")); code(err) != "capability" {
		t.Errorf("list through escaping symlink: %v", err)
	}
	// A symlink staying inside is fine for reading...
	if _, err := f.List(ctx, filepath.Join(allowed, "inner")); err != nil {
		t.Errorf("list through inner symlink: %v", err)
	}
	// ...but never for writing.
	if _, err := f.Stage(ctx, filepath.Join(allowed, "inner", "x"), strings.NewReader("x"), files.StageOptions{}); code(err) != files.CodeSymlink {
		t.Errorf("write through inner symlink: %v", err)
	}
	if _, err := f.Stage(ctx, filepath.Join(allowed, "escape", "x"), strings.NewReader("x"), files.StageOptions{}); err == nil {
		t.Error("write through escaping symlink allowed")
	}
	// The final component is never followed for changes.
	if err := f.Chmod(ctx, filepath.Join(allowed, "escape"), 0o777); code(err) != files.CodeSymlink {
		t.Errorf("chmod of a symlink: %v", err)
	}
	if err := f.Remove(ctx, filepath.Join(allowed, "escape"), false); err != nil {
		t.Errorf("deleting the link itself: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "outside", "secret.txt")); err != nil {
		t.Error("deleting a symlink removed its target")
	}
	if c := f.Caps(); c.RootEquivalent || len(c.AllowedPaths) != 1 || c.Transport != "agent" {
		t.Errorf("caps %+v", c)
	}
}

func TestAgentOwnFilesUnreachable(t *testing.T) {
	f := New(agentcaps.Caps{Files: agentcaps.Files{Enabled: true}}, hostexec.Local{})
	for _, p := range []string{"/etc/drool/agent.yaml", "/var/lib/drool-agent/identity.key"} {
		if _, err := f.Stat(context.Background(), p); code(err) != "capability" {
			t.Errorf("%s: %v", p, err)
		}
	}
	if !f.Caps().RootEquivalent && os.Getuid() == 0 {
		t.Error("unrestricted root files not root-equivalent")
	}
}

func TestStageInstallAndPreconditions(t *testing.T) {
	dir, f := tree(t)
	ctx := context.Background()
	p := filepath.Join(dir, "allowed", "a.txt")

	before, _ := f.Hash(ctx, p)
	st, err := f.Stage(ctx, p, strings.NewReader("new\n"), files.StageOptions{SHA256: sum([]byte("new\n"))})
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p); string(b) != "hello\n" {
		t.Fatal("staging changed the live file")
	}
	var out bytes.Buffer
	if vr, err := st.Validate(ctx, "grep -q new {path}", &out); err == nil {
		t.Errorf("validation without shell capability ran: %+v", vr)
	}
	// Someone else changes the file between read and install.
	os.WriteFile(p, []byte("changed\n"), 0o644)
	if _, err := st.Install(ctx, files.Precondition{ExpectSHA256: before}); code(err) != files.CodeConflict {
		t.Fatalf("precondition: %v", err)
	}
	st.Discard(ctx)
	if m, _ := filepath.Glob(filepath.Join(dir, "allowed", ".drool-tmp-*")); len(m) != 0 {
		t.Errorf("staged files left: %v", m)
	}

	// Wrong checksum is refused before anything lands.
	if _, err := f.Stage(ctx, p, strings.NewReader("x"), files.StageOptions{SHA256: sum([]byte("y"))}); code(err) != files.CodeIntegrity {
		t.Errorf("integrity: %v", err)
	}
	// Zero bytes, keep metadata.
	os.Chmod(p, 0o640)
	st, err = f.Stage(ctx, p, bytes.NewReader(nil), files.StageOptions{})
	if err != nil {
		t.Fatal(err)
	}
	inst, err := st.Install(ctx, files.Precondition{})
	st.Discard(ctx)
	if err != nil || inst.Info.Size != 0 || inst.Info.Mode != 0o640 {
		t.Fatalf("install: %+v %v", inst, err)
	}
	// Must-not-exist.
	st, _ = f.Stage(ctx, p, strings.NewReader("x"), files.StageOptions{})
	if _, err := st.Install(ctx, files.Precondition{MustNotExist: true}); code(err) != files.CodeExists {
		t.Errorf("must not exist: %v", err)
	}
	st.Discard(ctx)
	// setuid is refused with restricted file access.
	m := uint32(0o4755)
	if _, err := f.Stage(ctx, p, strings.NewReader("x"), files.StageOptions{Mode: &m}); code(err) != "capability" {
		t.Errorf("setuid: %v", err)
	}
}

func TestValidationWithAllowedTemplate(t *testing.T) {
	dir, _ := tree(t)
	allowed := filepath.Join(dir, "allowed")
	caps := agentcaps.Caps{Files: agentcaps.Files{Enabled: true, AllowedPaths: []string{allowed},
		AllowedValidationCommands: []string{"grep -q new {path}"}}}
	f := New(caps, hostexec.Local{})
	ctx := context.Background()
	st, err := f.Stage(ctx, filepath.Join(allowed, "b.txt"), strings.NewReader("new\n"), files.StageOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Discard(ctx)
	vr, err := st.Validate(ctx, "grep -q new {path}", io.Discard)
	if err != nil || !vr.OK {
		t.Fatalf("listed validation: %+v %v", vr, err)
	}
}

func TestDirectoryOperations(t *testing.T) {
	dir, f := tree(t)
	ctx := context.Background()
	a := filepath.Join(dir, "allowed")
	if err := f.Mkdir(ctx, filepath.Join(a, "new"), 0o750); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(filepath.Join(a, "new")); fi.Mode().Perm() != 0o750 {
		t.Errorf("mkdir mode %v", fi.Mode())
	}
	if err := f.Mkdir(ctx, filepath.Join(a, "new"), 0o750); code(err) != files.CodeExists {
		t.Errorf("mkdir twice: %v", err)
	}
	if err := f.Rename(ctx, filepath.Join(a, "a.txt"), filepath.Join(a, "new", "a.txt")); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(a, "b.txt"), []byte("b"), 0o644)
	if err := f.Rename(ctx, filepath.Join(a, "b.txt"), filepath.Join(a, "new", "a.txt")); code(err) != files.CodeExists {
		t.Errorf("rename over existing: %v", err)
	}
	if err := f.Remove(ctx, filepath.Join(a, "new"), false); code(err) != files.CodeNotEmpty {
		t.Errorf("remove non-empty: %v", err)
	}
	os.MkdirAll(filepath.Join(a, "new", "deep", "er"), 0o755)
	os.Symlink(filepath.Join(dir, "outside"), filepath.Join(a, "new", "deep", "link"))
	if err := f.Remove(ctx, filepath.Join(a, "new"), true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "outside", "secret.txt")); err != nil {
		t.Error("recursive delete followed a symlink")
	}
	// Writes into a directory other users can modify are refused.
	os.MkdirAll(filepath.Join(a, "shared"), 0o777)
	os.Chmod(filepath.Join(a, "shared"), 0o777)
	if _, err := f.Stage(ctx, filepath.Join(a, "shared", "x"), strings.NewReader("x"), files.StageOptions{}); code(err) != files.CodeUnsafeDir {
		t.Errorf("unsafe dir: %v", err)
	}
}
