//go:build integration

package integration

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

type fileEntry struct {
	Name   string `json:"name"`
	Path   string `json:"path"`
	Kind   string `json:"kind"`
	Size   int64  `json:"size"`
	Mode   int    `json:"mode"`
	Owner  string `json:"owner"`
	Group  string `json:"group"`
	Target string `json:"target"`
}

type listing struct {
	Path    string      `json:"path"`
	Entries []fileEntry `json:"entries"`
	Caps    struct {
		Transport      string `json:"transport"`
		RootEquivalent bool   `json:"root_equivalent"`
	} `json:"caps"`
}

func hexSum(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

func q(p string) string { return url.QueryEscape(p) }

// filesAPI wraps the file manager endpoints for one host.
type filesAPI struct {
	t  *testing.T
	id string
}

func (f filesAPI) list(dir string, elevated bool) (listing, int, problem) {
	var l listing
	var p problem
	st, err := ctl.do("GET", fmt.Sprintf("/api/v1/hosts/%s/files?path=%s&elevated=%v", f.id, q(dir), elevated), nil, &l)
	if err != nil {
		f.t.Fatal(err)
	}
	if st != 200 {
		ctl.do("GET", fmt.Sprintf("/api/v1/hosts/%s/files?path=%s&elevated=%v", f.id, q(dir), elevated), nil, &p)
	}
	return l, st, p
}

func (f filesAPI) upload(ctx context.Context, p string, body io.Reader, overwrite, elevated bool) (int, string) {
	r, err := ctl.raw(ctx, "PUT", fmt.Sprintf("/api/v1/hosts/%s/files/content?path=%s&overwrite=%v&elevated=%v", f.id, q(p), overwrite, elevated), body)
	if err != nil {
		return 0, err.Error()
	}
	defer r.Body.Close()
	b, _ := io.ReadAll(r.Body)
	return r.StatusCode, string(b)
}

func (f filesAPI) download(p string, elevated bool) ([]byte, int) {
	r, err := ctl.raw(context.Background(), "GET", fmt.Sprintf("/api/v1/hosts/%s/files/content?path=%s&elevated=%v", f.id, q(p), elevated), nil)
	if err != nil {
		f.t.Fatal(err)
	}
	defer r.Body.Close()
	b, _ := io.ReadAll(r.Body)
	return b, r.StatusCode
}

func (f filesAPI) change(action string, body map[string]any) (int, string) {
	var raw json.RawMessage
	st, err := ctl.do("POST", "/api/v1/hosts/"+f.id+"/files/"+action, body, &raw)
	if err != nil {
		f.t.Fatal(err)
	}
	if st >= 300 {
		var p problem
		ctl.do("POST", "/api/v1/hosts/"+f.id+"/files/"+action, body, &p)
		return st, p.Code + ": " + p.Detail
	}
	return st, string(raw)
}

func (c *container) cat(t *testing.T, p string) string {
	t.Helper()
	out, err := docker("exec", c.Name, "cat", p)
	if err != nil {
		return "<missing>"
	}
	return out
}

func (c *container) temps(t *testing.T, dir string) string {
	out, _ := docker("exec", c.Name, "sh", "-c", "ls -A "+dir+" | grep '^.drool-tmp-' || true")
	return out
}

// TestFileManager covers the interactive file manager against a real host:
// SFTP as the login user, elevated access through sudo, and the failure
// paths that must never leave a damaged file behind.
func TestFileManager(t *testing.T) {
	for _, d := range selected() {
		t.Run(d.Name, func(t *testing.T) { testFileManager(t, d) })
	}
}

func testFileManager(t *testing.T, d distro) {
	short := "files-" + d.Name
	c := startHost(t, image(t, d), short, true, netOuter)
	h := addHost(t, short, c, hostOpts{})
	f := filesAPI{t, h.ID}
	ctx := context.Background()
	c.exec(t, "mkdir -p /home/deploy/work && printf 'hello\\n' > /home/deploy/work/a.txt && ln -s a.txt /home/deploy/work/link && chown -R deploy:deploy /home/deploy/work")

	t.Run("browse as the login user", func(t *testing.T) {
		l, st, p := f.list("", false)
		if st != 200 || l.Path != "/home/deploy" || l.Caps.Transport != "sftp" || l.Caps.RootEquivalent {
			t.Fatalf("home: %d %+v %v", st, l, p)
		}
		l, _, _ = f.list("/home/deploy/work", false)
		got := map[string]fileEntry{}
		for _, e := range l.Entries {
			got[e.Name] = e
		}
		if a := got["a.txt"]; a.Owner != "deploy" || a.Size != 6 || a.Kind != "file" {
			t.Errorf("a.txt %+v", a)
		}
		if got["link"].Kind != "symlink" || got["link"].Target != "a.txt" {
			t.Errorf("link %+v", got["link"])
		}
		if _, st, p := f.list("/root", false); st != 403 || p.Code != "permission_denied" {
			t.Errorf("permission denied: %d %+v", st, p)
		}
	})

	t.Run("upload, download, zero bytes, overwrite", func(t *testing.T) {
		if st, body := f.upload(ctx, "/home/deploy/work/new.txt", strings.NewReader("new\n"), false, false); st != 200 {
			t.Fatalf("upload: %d %s", st, body)
		}
		if st, _ := f.upload(ctx, "/home/deploy/work/new.txt", strings.NewReader("again\n"), false, false); st != 409 {
			t.Errorf("upload without overwrite replaced a file: %d", st)
		}
		if st, body := f.upload(ctx, "/home/deploy/work/new.txt", strings.NewReader("again\n"), true, false); st != 200 || c.cat(t, "/home/deploy/work/new.txt") != "again" {
			t.Errorf("overwrite: %d %s", st, body)
		}
		if st, body := f.upload(ctx, "/home/deploy/work/empty", bytes.NewReader(nil), false, false); st != 200 {
			t.Fatalf("zero bytes: %d %s", st, body)
		}
		if b, st := f.download("/home/deploy/work/empty", false); st != 200 || len(b) != 0 {
			t.Errorf("zero-byte download: %d %q", st, b)
		}
		if b, st := f.download("/home/deploy/work/a.txt", false); st != 200 || string(b) != "hello\n" {
			t.Errorf("download: %d %q", st, b)
		}
		if _, st := f.download("/home/deploy/work/link", false); st != 409 {
			t.Errorf("download through a symlink: %d", st)
		}
		if st, _ := f.upload(ctx, "/etc/drool-test", strings.NewReader("x"), false, false); st != 403 {
			t.Errorf("writing /etc as the login user: %d", st)
		}
	})

	t.Run("large file streams in both directions", func(t *testing.T) {
		const size = 256 << 20
		before := controllerRSS(t)
		src := &randReader{left: size}
		st, body := f.upload(ctx, "/home/deploy/work/big.bin", src, false, false)
		if st != 200 || !strings.Contains(body, src.sum()) {
			t.Fatalf("large upload: %d %s", st, body)
		}
		r, err := ctl.raw(ctx, "GET", fmt.Sprintf("/api/v1/hosts/%s/files/content?path=%s", h.ID, q("/home/deploy/work/big.bin")), nil)
		if err != nil {
			t.Fatal(err)
		}
		hh := sha256.New()
		n, _ := io.Copy(hh, r.Body)
		r.Body.Close()
		if n != size || hex.EncodeToString(hh.Sum(nil)) != src.sum() {
			t.Fatalf("large download: %d bytes", n)
		}
		if grew := controllerRSS(t) - before; grew > 128<<20 {
			t.Errorf("controller memory grew by %d MiB moving a %d MiB file: not streaming", grew>>20, size>>20)
		}
		c.exec(t, "rm -f /home/deploy/work/big.bin")
	})

	t.Run("cancelled upload leaves the destination alone", func(t *testing.T) {
		c.exec(t, "printf 'original\\n' > /home/deploy/work/keep.txt && chown deploy: /home/deploy/work/keep.txt")
		cctx, cancel := context.WithCancel(ctx)
		pr, pw := io.Pipe()
		go func() {
			pw.Write(bytes.Repeat([]byte("x"), 1<<20))
			time.Sleep(500 * time.Millisecond)
			cancel() // the browser goes away mid-upload
			pw.CloseWithError(context.Canceled)
		}()
		f.upload(cctx, "/home/deploy/work/keep.txt", pr, true, false)
		waitFor(t, 10*time.Second, "temporary file cleanup", func() bool { return c.temps(t, "/home/deploy/work") == "" })
		if got := c.cat(t, "/home/deploy/work/keep.txt"); got != "original" {
			t.Fatalf("destination changed by a cancelled upload: %q", got)
		}
	})

	t.Run("directories, rename, move, delete, chmod", func(t *testing.T) {
		chk := func(action string, body map[string]any, want int) string {
			t.Helper()
			st, out := f.change(action, body)
			if st != want {
				t.Fatalf("%s %v: %d %s", action, body, st, out)
			}
			return out
		}
		chk("mkdir", map[string]any{"path": "/home/deploy/work/dir", "mode": "0750"}, 200)
		chk("mkdir", map[string]any{"path": "/home/deploy/work/dir"}, 409)
		chk("rename", map[string]any{"path": "/home/deploy/work/new.txt", "to": "/home/deploy/work/renamed.txt"}, 200)
		chk("rename", map[string]any{"path": "/home/deploy/work/renamed.txt", "to": "/home/deploy/work/dir/moved.txt"}, 200)
		chk("rename", map[string]any{"path": "/home/deploy/work/a.txt", "to": "/home/deploy/work/dir/moved.txt"}, 409)
		chk("chmod", map[string]any{"path": "/home/deploy/work/dir/moved.txt", "mode": "0600"}, 200)
		if out := c.exec(t, "stat -c %a /home/deploy/work/dir/moved.txt"); out != "600" {
			t.Errorf("chmod: %s", out)
		}
		chk("chmod", map[string]any{"path": "/home/deploy/work/link", "mode": "0777"}, 409)
		chk("delete", map[string]any{"path": "/home/deploy/work/dir"}, 409) // not empty
		chk("delete", map[string]any{"path": "/home/deploy/work/link"}, 204)
		if c.cat(t, "/home/deploy/work/a.txt") != "hello" {
			t.Error("deleting a symlink damaged its target")
		}
		chk("delete", map[string]any{"path": "/home/deploy/work/dir", "recursive": true}, 204)
		chk("delete", map[string]any{"path": "/home", "recursive": true}, 422)
	})

	t.Run("text editing refuses to overwrite a newer version", func(t *testing.T) {
		var tf struct {
			Content string `json:"content"`
			SHA256  string `json:"sha256"`
		}
		ctl.must(t, "GET", fmt.Sprintf("/api/v1/hosts/%s/files/text?path=%s", h.ID, q("/home/deploy/work/a.txt")), nil, &tf)
		c.exec(t, "printf 'changed meanwhile\\n' > /home/deploy/work/a.txt")
		var p problem
		st, _ := ctl.do("PUT", "/api/v1/hosts/"+h.ID+"/files/text", map[string]any{"path": "/home/deploy/work/a.txt", "content": "mine\n", "expect_sha256": tf.SHA256}, &p)
		if st != 409 || p.Code != "file_changed" || !strings.Contains(p.Detail, "Reload it or explicitly overwrite") {
			t.Fatalf("conflict: %d %+v", st, p)
		}
		if c.cat(t, "/home/deploy/work/a.txt") != "changed meanwhile" {
			t.Fatal("overwritten despite the conflict")
		}
		st, _ = ctl.do("PUT", "/api/v1/hosts/"+h.ID+"/files/text", map[string]any{"path": "/home/deploy/work/a.txt", "content": "mine\n", "expect_sha256": tf.SHA256, "force": true}, nil)
		if st != 200 || c.cat(t, "/home/deploy/work/a.txt") != "mine" {
			t.Fatalf("explicit overwrite: %d", st)
		}
		c.exec(t, "head -c 2097152 /dev/zero | tr '\\0' x > /home/deploy/work/big.txt")
		st, _ = ctl.do("GET", fmt.Sprintf("/api/v1/hosts/%s/files/text?path=%s", h.ID, q("/home/deploy/work/big.txt")), nil, &p)
		if st != 422 || p.Code != "not_text" {
			t.Errorf("large file as text: %d %+v", st, p)
		}
	})

	t.Run("elevated writes with passwordless sudo", func(t *testing.T) {
		l, st, p := f.list("/etc", true)
		if st != 200 || !l.Caps.RootEquivalent || l.Caps.Transport != "ssh-sudo" {
			t.Fatalf("elevated list: %d %+v %+v", st, l.Caps, p)
		}
		if st, body := f.upload(ctx, "/etc/drool-elevated.conf", strings.NewReader("as root\n"), false, true); st != 200 {
			t.Fatalf("elevated upload: %d %s", st, body)
		}
		if out := c.exec(t, "stat -c '%U:%G %a' /etc/drool-elevated.conf"); out != "root:root 644" {
			t.Errorf("new root file: %s", out)
		}
		c.exec(t, "chmod 640 /etc/drool-elevated.conf && chgrp adm /etc/drool-elevated.conf 2>/dev/null || chgrp wheel /etc/drool-elevated.conf")
		grp := c.exec(t, "stat -c %G /etc/drool-elevated.conf")
		if st, body := f.upload(ctx, "/etc/drool-elevated.conf", strings.NewReader("replaced\n"), true, true); st != 200 {
			t.Fatalf("elevated replace: %d %s", st, body)
		}
		if out := c.exec(t, "stat -c '%U:%G %a' /etc/drool-elevated.conf"); out != "root:"+grp+" 640" {
			t.Errorf("replace kept metadata? %s", out)
		}
		if b, st := f.download("/etc/shadow", true); st != 200 || !strings.Contains(string(b), "root:") {
			t.Errorf("elevated read: %d", st)
		}
		// Root writes into a directory other users can modify are refused.
		c.exec(t, "mkdir -p /srv/shared && chmod 0777 /srv/shared")
		if st, body := f.upload(ctx, "/srv/shared/x", strings.NewReader("x"), false, true); st != 422 || !strings.Contains(body, "unsafe_directory") {
			t.Errorf("unsafe directory: %d %s", st, body)
		}
		// A symlinked directory is never written through.
		c.exec(t, "ln -sfn /etc /tmp/etc-link")
		if st, body := f.upload(ctx, "/tmp/etc-link/evil", strings.NewReader("x"), false, true); st != 409 || !strings.Contains(body, "symlink") {
			t.Errorf("write through a symlinked directory: %d %s", st, body)
		}
	})

	t.Run("elevated writes with password sudo, and a wrong password", func(t *testing.T) {
		good := createSecret(t, short+"-sudo", "sudo_password", sudoPassword)
		bad := createSecret(t, short+"-badsudo", "sudo_password", "wrong-password")
		hp := addHost(t, short+"-pw", c, hostOpts{User: "pwuser", BecomeCredID: good})
		fp := filesAPI{t, hp.ID}
		if st, body := fp.upload(ctx, "/etc/drool-pw.conf", strings.NewReader("pw\n"), false, true); st != 200 {
			t.Fatalf("password sudo upload: %d %s", st, body)
		}
		if c.cat(t, "/etc/drool-pw.conf") != "pw" {
			t.Fatal("content")
		}
		if b, st := fp.download("/etc/drool-pw.conf", true); st != 200 || string(b) != "pw\n" {
			t.Fatalf("password sudo download: %d %q", st, b)
		}
		hb := addHost(t, short+"-badpw", c, hostOpts{User: "pwuser", BecomeCredID: bad})
		fb := filesAPI{t, hb.ID}
		st, body := fb.upload(ctx, "/etc/drool-bad.conf", strings.NewReader("x"), false, true)
		if st == 200 || !strings.Contains(body, "sudo") || strings.Contains(body, "wrong-password") {
			t.Fatalf("wrong sudo password: %d %s", st, body)
		}
		if c.cat(t, "/etc/drool-bad.conf") != "<missing>" {
			t.Fatal("file written with a wrong password")
		}
	})

	t.Run("audit records metadata, never content", func(t *testing.T) {
		var a struct {
			Items []struct {
				Action  string         `json:"action"`
				Details map[string]any `json:"details"`
			} `json:"items"`
		}
		ctl.must(t, "GET", "/api/v1/audit?action=file.&target="+h.ID+"&limit=500", nil, &a)
		actions := map[string]bool{}
		raw, _ := json.Marshal(a.Items)
		for _, it := range a.Items {
			actions[it.Action] = true
		}
		for _, want := range []string{"file.upload", "file.write", "file.mkdir", "file.rename", "file.delete", "file.chmod", "file.read"} {
			if !actions[want] {
				t.Errorf("no %s audit event", want)
			}
		}
		for _, secret := range []string{"as root", "replaced", "changed meanwhile", "mine\\n", "drool-it-sudo-pw"} {
			if strings.Contains(string(raw), secret) {
				t.Errorf("audit log contains file content or a password: %q", secret)
			}
		}
		if !strings.Contains(string(raw), "after_sha256") {
			t.Error("audit lacks hashes")
		}
	})

	t.Run("connection loss during an upload leaves the destination alone", func(t *testing.T) {
		c.exec(t, "printf 'survives\\n' > /home/deploy/work/target && chown deploy: /home/deploy/work/target")
		pr, pw := io.Pipe()
		go func() {
			pw.Write(bytes.Repeat([]byte("y"), 4<<20))
			mustDocker(t, "kill", c.Name) // the host disappears mid-upload
			pw.Write(bytes.Repeat([]byte("y"), 4<<20))
			pw.Close()
		}()
		st, _ := f.upload(ctx, "/home/deploy/work/target", pr, true, false)
		mustDocker(t, "start", c.Name)
		if st == 200 {
			t.Fatal("upload reported success after the host died")
		}
		if got := c.cat(t, "/home/deploy/work/target"); got != "survives" {
			t.Fatalf("destination changed by an interrupted upload: %q", got)
		}
	})

	// Last: replaces the container.
	t.Run("host key mismatch blocks file access", func(t *testing.T) {
		c.recreate(t)
		probe(t, h.ID)
		if _, st, p := f.list("/", false); st == 200 || !strings.Contains(p.Detail+p.Code, "key") {
			t.Fatalf("file access with a changed host key: %d %+v", st, p)
		}
	})
}

type randReader struct {
	left int64
	h    interface {
		io.Writer
		Sum([]byte) []byte
	}
}

func (r *randReader) Read(p []byte) (int, error) {
	if r.h == nil {
		r.h = sha256.New()
	}
	if r.left <= 0 {
		return 0, io.EOF
	}
	if int64(len(p)) > r.left {
		p = p[:r.left]
	}
	n, _ := rand.Read(p)
	r.h.Write(p[:n])
	r.left -= int64(n)
	return n, nil
}

func (r *randReader) sum() string { return hex.EncodeToString(r.h.Sum(nil)) }

// controllerRSS is the controller's resident memory.
func controllerRSS(t *testing.T) int64 {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", ctl.cmd.Process.Pid))
	if err != nil {
		t.Skip("no /proc")
	}
	for _, l := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(l, "VmRSS:"); ok {
			kb, _ := strconv.ParseInt(strings.Fields(v)[0], 10, 64)
			return kb << 10
		}
	}
	return 0
}

// TestFilesThroughJumpHost: the same session model over the jump path.
func TestFilesThroughJumpHost(t *testing.T) {
	ds := selected()
	jump := startHost(t, image(t, ds[0]), "fjump", true, netOuter, netInner)
	target := startHost(t, image(t, ds[len(ds)-1]), "fbehind", false, netInner)
	j := addHost(t, "fjump", jump, hostOpts{JumpOnly: true})
	h := addHost(t, "fbehind", target, hostOpts{Via: j.ID})
	f := filesAPI{t, h.ID}
	if st, body := f.upload(context.Background(), "/home/deploy/via-jump.txt", strings.NewReader("through the jump\n"), false, false); st != 200 {
		t.Fatalf("upload via jump: %d %s", st, body)
	}
	if b, st := f.download("/home/deploy/via-jump.txt", false); st != 200 || string(b) != "through the jump\n" {
		t.Fatalf("download via jump: %d %q", st, b)
	}
	if st, body := f.upload(context.Background(), "/etc/via-jump.conf", strings.NewReader("root via jump\n"), false, true); st != 200 {
		t.Fatalf("elevated upload via jump: %d %s", st, body)
	}
	if target.cat(t, "/etc/via-jump.conf") != "root via jump" {
		t.Fatal("content")
	}
}
