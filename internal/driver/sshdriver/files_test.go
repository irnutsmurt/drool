package sshdriver

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"testing"

	"drool/internal/files"
	"drool/internal/sshx/sshtest"
)

// fsModes runs a test against both implementations: SFTP as the login user,
// and the privileged command path (simulated by logging in as "root"; the
// commands run locally as the test user).
func fsModes(t *testing.T, test func(t *testing.T, fsys *FileSystem, dir string)) {
	for _, mode := range []string{"sftp", "elevated"} {
		t.Run(mode, func(t *testing.T) {
			x, tg, _ := setup(t, sshtest.ExecLocal)
			b := Become{Method: "sudo"}
			if mode == "elevated" {
				tg.User = "root"
				b = Become{Method: "none"}
			}
			fsys, err := OpenFS(context.Background(), x.Pool, tg, b, mode == "elevated")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { fsys.Close() })
			dir, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			test(t, fsys, dir)
		})
	}
}

func me(t *testing.T) string {
	u, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	return u.Username
}

func myGroup(t *testing.T) string {
	u, _ := user.Current()
	g, err := user.LookupGroupId(u.Gid)
	if err != nil {
		t.Fatal(err)
	}
	return g.Name
}

func code(err error) string {
	var fe *files.Error
	if errors.As(err, &fe) {
		return fe.Code
	}
	return ""
}

func sum(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

// write stages and installs content with the test user as owner (the
// elevated path defaults new files to root, which a test can't chown to).
func write(t *testing.T, fsys *FileSystem, p string, content []byte, pre files.Precondition) (*files.Installed, error) {
	t.Helper()
	ctx := context.Background()
	st, err := fsys.Stage(ctx, p, bytes.NewReader(content), files.StageOptions{Owner: me(t), Group: myGroup(t)})
	if err != nil {
		return nil, err
	}
	defer st.Discard(ctx)
	return st.Install(ctx, pre)
}

func noTemps(t *testing.T, dir string) {
	t.Helper()
	m, _ := filepath.Glob(filepath.Join(dir, ".drool-tmp-*"))
	if len(m) > 0 {
		t.Fatalf("temporary files left behind: %v", m)
	}
}

func TestFSListStatAndRead(t *testing.T) {
	fsModes(t, func(t *testing.T, fsys *FileSystem, dir string) {
		ctx := context.Background()
		os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello\n"), 0o640)
		os.Mkdir(filepath.Join(dir, "sub"), 0o750)
		os.Symlink("a.txt", filepath.Join(dir, "link"))
		list, err := fsys.List(ctx, dir)
		if err != nil {
			t.Fatal(err)
		}
		got := map[string]files.Info{}
		for _, i := range list {
			got[i.Name] = i
		}
		if a := got["a.txt"]; a.Kind != files.KindFile || a.Size != 6 || a.Mode != 0o640 || a.Owner != me(t) || a.Path != filepath.Join(dir, "a.txt") {
			t.Errorf("a.txt: %+v", a)
		}
		if s := got["sub"]; s.Kind != files.KindDir || s.Mode != 0o750 {
			t.Errorf("sub: %+v", s)
		}
		if l := got["link"]; l.Kind != files.KindSymlink || l.Target != "a.txt" {
			t.Errorf("link: %+v", l)
		}
		if _, err := fsys.List(ctx, filepath.Join(dir, "a.txt")); code(err) != files.CodeNotDir {
			t.Errorf("listing a file: %v", err)
		}
		if _, err := fsys.Stat(ctx, filepath.Join(dir, "missing")); code(err) != files.CodeNotFound {
			t.Errorf("stat missing: %v", err)
		}
		rc, info, err := fsys.Open(ctx, filepath.Join(dir, "a.txt"))
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(rc)
		rc.Close()
		if string(b) != "hello\n" || info.Size != 6 {
			t.Errorf("read %q %+v", b, info)
		}
		if _, _, err := fsys.Open(ctx, filepath.Join(dir, "link")); code(err) != files.CodeSymlink {
			t.Errorf("open symlink: %v", err)
		}
		if h, err := fsys.Hash(ctx, filepath.Join(dir, "a.txt")); err != nil || h != sum([]byte("hello\n")) {
			t.Errorf("hash %s %v", h, err)
		}
	})
}

func TestFSWriteAtomicAndChecked(t *testing.T) {
	fsModes(t, func(t *testing.T, fsys *FileSystem, dir string) {
		ctx := context.Background()
		p := filepath.Join(dir, "conf")

		// New file, then zero bytes, then a replacement that keeps the mode.
		if inst, err := write(t, fsys, p, []byte("v1\n"), files.Precondition{MustNotExist: true}); err != nil || inst.SHA256 != sum([]byte("v1\n")) {
			t.Fatalf("create: %+v %v", inst, err)
		}
		os.Chmod(p, 0o600)
		if _, err := write(t, fsys, p, []byte{}, files.Precondition{ExpectSHA256: sum([]byte("v1\n"))}); err != nil {
			t.Fatalf("zero bytes: %v", err)
		}
		if b, _ := os.ReadFile(p); len(b) != 0 {
			t.Fatalf("zero-byte write left %q", b)
		}
		inst, err := write(t, fsys, p, []byte("v2\n"), files.Precondition{})
		if err != nil || inst.Info.Mode != 0o600 {
			t.Fatalf("replace kept mode? %+v %v", inst, err)
		}

		// Somebody changes the file after it was read: refused, file intact.
		os.WriteFile(p, []byte("changed by someone\n"), 0o600)
		if _, err := write(t, fsys, p, []byte("v3\n"), files.Precondition{ExpectSHA256: sum([]byte("v2\n"))}); code(err) != files.CodeConflict {
			t.Fatalf("conflict not detected: %v", err)
		}
		if _, err := write(t, fsys, p, []byte("v3\n"), files.Precondition{MustNotExist: true}); code(err) != files.CodeExists {
			t.Fatalf("must-not-exist: %v", err)
		}
		if b, _ := os.ReadFile(p); string(b) != "changed by someone\n" {
			t.Fatalf("file overwritten despite the conflict: %q", b)
		}

		// Wrong checksum: nothing installed.
		_, err = fsys.Stage(ctx, p, strings.NewReader("evil"), files.StageOptions{SHA256: sum([]byte("good")), Owner: me(t), Group: myGroup(t)})
		if code(err) != files.CodeIntegrity {
			t.Fatalf("checksum mismatch: %v", err)
		}

		// The upload breaks off: destination unchanged, no temp file left.
		_, err = fsys.Stage(ctx, p, io.MultiReader(strings.NewReader("partial"), errReader{}), files.StageOptions{Owner: me(t), Group: myGroup(t)})
		if err == nil {
			t.Fatal("broken upload staged")
		}
		if b, _ := os.ReadFile(p); string(b) != "changed by someone\n" {
			t.Fatalf("destination changed by a broken upload: %q", b)
		}
		noTemps(t, dir)

		// Cancelled while uploading.
		cctx, cancel := context.WithCancel(ctx)
		cancel()
		if _, err := fsys.Stage(cctx, p, strings.NewReader("never"), files.StageOptions{Owner: me(t), Group: myGroup(t)}); err == nil {
			t.Fatal("cancelled upload staged")
		}
		noTemps(t, dir)

		// Large content streams through and hashes correctly.
		big := make([]byte, 24<<20)
		rand.Read(big)
		inst, err = write(t, fsys, filepath.Join(dir, "big.bin"), big, files.Precondition{})
		if err != nil || inst.SHA256 != sum(big) || inst.Info.Size != int64(len(big)) {
			t.Fatalf("large file: %v", err)
		}
		noTemps(t, dir)
	})
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("connection to the browser lost") }

func TestFSRefusesSymlinks(t *testing.T) {
	fsModes(t, func(t *testing.T, fsys *FileSystem, dir string) {
		ctx := context.Background()
		target := filepath.Join(dir, "real")
		os.WriteFile(target, []byte("keep\n"), 0o644)
		os.Symlink(target, filepath.Join(dir, "link"))
		os.Mkdir(filepath.Join(dir, "realdir"), 0o755)
		os.Symlink(filepath.Join(dir, "realdir"), filepath.Join(dir, "dirlink"))

		if _, err := write(t, fsys, filepath.Join(dir, "link"), []byte("x"), files.Precondition{}); code(err) != files.CodeSymlink {
			t.Errorf("write through symlink: %v", err)
		}
		if _, err := write(t, fsys, filepath.Join(dir, "dirlink", "f"), []byte("x"), files.Precondition{}); code(err) != files.CodeSymlink {
			t.Errorf("write through symlinked directory: %v", err)
		}
		if err := fsys.Chmod(ctx, filepath.Join(dir, "link"), 0o777); code(err) != files.CodeSymlink {
			t.Errorf("chmod through symlink: %v", err)
		}
		if fi, _ := os.Stat(target); fi.Mode().Perm() != 0o644 {
			t.Errorf("target changed: %v", fi.Mode())
		}
		// Deleting a symlink removes the link, never the target.
		if err := fsys.Remove(ctx, filepath.Join(dir, "link"), false); err != nil {
			t.Fatal(err)
		}
		if b, _ := os.ReadFile(target); string(b) != "keep\n" {
			t.Errorf("target damaged: %q", b)
		}
		if _, err := os.Lstat(filepath.Join(dir, "link")); !os.IsNotExist(err) {
			t.Error("link not removed")
		}
		// Recursive delete through a symlinked directory is refused.
		if err := fsys.Remove(ctx, filepath.Join(dir, "dirlink", "x"), true); code(err) != files.CodeSymlink && code(err) != files.CodeNotFound {
			t.Errorf("remove through symlinked dir: %v", err)
		}
	})
}

func TestFSDirectoryOperations(t *testing.T) {
	fsModes(t, func(t *testing.T, fsys *FileSystem, dir string) {
		ctx := context.Background()
		d := filepath.Join(dir, "new")
		if err := fsys.Mkdir(ctx, d, 0o750); err != nil {
			t.Fatal(err)
		}
		if fi, _ := os.Stat(d); !fi.IsDir() || fi.Mode().Perm() != 0o750 {
			t.Fatalf("mkdir: %v", fi.Mode())
		}
		if err := fsys.Mkdir(ctx, d, 0o750); code(err) != files.CodeExists {
			t.Errorf("mkdir twice: %v", err)
		}
		os.WriteFile(filepath.Join(d, "f"), []byte("x"), 0o644)
		os.WriteFile(filepath.Join(dir, "other"), []byte("y"), 0o644)

		// Rename and move never replace an existing entry.
		if err := fsys.Rename(ctx, filepath.Join(d, "f"), filepath.Join(dir, "other")); code(err) != files.CodeExists {
			t.Errorf("rename onto existing: %v", err)
		}
		if err := fsys.Rename(ctx, filepath.Join(d, "f"), filepath.Join(dir, "moved")); err != nil {
			t.Fatal(err)
		}
		if b, _ := os.ReadFile(filepath.Join(dir, "moved")); string(b) != "x" {
			t.Errorf("move: %q", b)
		}
		if err := fsys.Rename(ctx, d, filepath.Join(d, "inside")); code(err) != files.CodeInvalid {
			t.Errorf("move into itself: %v", err)
		}

		if err := fsys.Chmod(ctx, filepath.Join(dir, "moved"), 0o600); err != nil {
			t.Fatal(err)
		}
		if fi, _ := os.Stat(filepath.Join(dir, "moved")); fi.Mode().Perm() != 0o600 {
			t.Errorf("chmod: %v", fi.Mode())
		}
		if err := fsys.Chown(ctx, filepath.Join(dir, "moved"), me(t), ""); err != nil {
			t.Errorf("chown to self: %v", err)
		}
		if err := fsys.Chown(ctx, filepath.Join(dir, "moved"), "bad name;rm", ""); code(err) != files.CodeInvalid {
			t.Errorf("bad owner name accepted: %v", err)
		}

		os.WriteFile(filepath.Join(d, "g"), []byte("x"), 0o644)
		if err := fsys.Remove(ctx, d, false); code(err) != files.CodeNotEmpty {
			t.Errorf("remove non-empty: %v", err)
		}
		if err := fsys.Remove(ctx, d, true); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(d); !os.IsNotExist(err) {
			t.Error("directory still there")
		}
		if err := fsys.Remove(ctx, "/etc", true); code(err) != files.CodeInvalid {
			t.Errorf("recursive delete of a top-level directory: %v", err)
		}
	})
}

func TestFSValidateLeavesLiveFileAlone(t *testing.T) {
	fsModes(t, func(t *testing.T, fsys *FileSystem, dir string) {
		ctx := context.Background()
		p := filepath.Join(dir, "app.conf")
		os.WriteFile(p, []byte("good=1\n"), 0o644)
		st, err := fsys.Stage(ctx, p, strings.NewReader("broken\n"), files.StageOptions{Owner: me(t), Group: myGroup(t)})
		if err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		res, err := st.Validate(ctx, `grep -q '=' {path} || { echo "syntax error in $(basename {path})"; exit 1; }`, &out)
		if err != nil || res.OK || res.ExitCode != 1 || !strings.Contains(out.String(), "syntax error in .drool-tmp-") {
			t.Fatalf("validation: %+v %v %q", res, err, out.String())
		}
		st.Discard(ctx)
		if b, _ := os.ReadFile(p); string(b) != "good=1\n" {
			t.Fatalf("live file changed by a failed validation: %q", b)
		}
		noTemps(t, dir)
	})
}

// Root writes into a directory that other users can modify are refused.
func TestElevatedRefusesUnsafeDirectory(t *testing.T) {
	x, tg, _ := setup(t, sshtest.ExecLocal)
	tg.User = "root"
	fsys, err := OpenFS(context.Background(), x.Pool, tg, Become{Method: "none"}, true)
	if err != nil {
		t.Fatal(err)
	}
	defer fsys.Close()
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	os.Chmod(dir, 0o777)
	_, err = write(t, fsys, filepath.Join(dir, "x"), []byte("x"), files.Precondition{})
	if code(err) != files.CodeUnsafeDir {
		t.Fatalf("got %v", err)
	}
	os.WriteFile(filepath.Join(dir, "y"), []byte("y"), 0o644)
	if err := fsys.Chmod(context.Background(), filepath.Join(dir, "y"), 0o600); code(err) != files.CodeUnsafeDir {
		t.Fatalf("chmod: %v", err)
	}
}

func TestElevatedNeedsRootOrSudo(t *testing.T) {
	x, tg, _ := setup(t, sshtest.ExecLocal)
	if _, err := OpenFS(context.Background(), x.Pool, tg, Become{Method: "none"}, true); err == nil {
		t.Fatal("elevated session without sudo")
	}
}

func TestFSUploadAfterBrokenUpload(t *testing.T) {
	x, tg, _ := setup(t, sshtest.ExecLocal)
	fsys, err := OpenFS(context.Background(), x.Pool, tg, Become{Method: "sudo"}, false)
	if err != nil {
		t.Fatal(err)
	}
	defer fsys.Close()
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	_, err = fsys.Stage(context.Background(), filepath.Join(dir, "f"), io.MultiReader(strings.NewReader("partial"), errReader{}), files.StageOptions{})
	t.Logf("broken: %v", err)
	if _, err := write(t, fsys, filepath.Join(dir, "g"), []byte("after"), files.Precondition{}); err != nil {
		t.Fatalf("the session is unusable after a broken upload: %v", err)
	}
}
