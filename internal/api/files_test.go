package api

import (
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"drool/internal/auth"
	"drool/internal/sshx/sshtest"
)

// raw sends a non-JSON body (uploads) or reads a non-JSON response.
func (c *client) raw(method, path string, body io.Reader) resp {
	c.h.t.Helper()
	req, _ := http.NewRequest(method, c.h.ts.URL+path, body)
	req.Header.Set("Content-Type", "application/octet-stream")
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: c.cookie})
	req.Header.Set(csrfHeader, c.csrf)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		c.h.t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return resp{status: res.StatusCode, header: res.Header, body: b}
}

func qp(p string) string { return url.QueryEscape(p) }

func TestFileManagerAPI(t *testing.T) {
	h := newHarness(t)
	h.mkUser("admin", auth.RoleAdmin)
	c := h.login("admin")
	id, srv := readyHostServer(t, h, c, "files")
	// This host's SSH user may not use sudo, whatever the machine running
	// the test allows (a CI runner's user has passwordless sudo).
	srv.SetExec(func(cmd string, stdin []byte) (string, string, int) {
		if strings.HasPrefix(cmd, "sudo ") {
			return "", "sudo: a password is required\n", 1
		}
		return sshtest.ExecLocal(cmd, stdin)
	})
	dir := t.TempDir()
	f := filepath.Join(dir, "notes.txt")
	os.WriteFile(f, []byte("secret-content-1\n"), 0o644)
	base := "/api/v1/hosts/" + id + "/files"

	var l struct {
		Path    string `json:"path"`
		Entries []struct {
			Name string `json:"name"`
		} `json:"entries"`
	}
	if r := c.do("GET", base+"?path="+qp(dir), nil); r.status != 200 {
		t.Fatalf("list: %d %s", r.status, r.body)
	} else if r.json(t, &l); len(l.Entries) != 1 || l.Entries[0].Name != "notes.txt" {
		t.Fatalf("listing: %+v", l)
	}
	if r := c.do("GET", base+"?path=relative/path", nil); r.status != 422 {
		t.Errorf("relative path: %d %s", r.status, r.body)
	}

	// Text editing with a concurrent change.
	var tf struct {
		Content string `json:"content"`
		SHA256  string `json:"sha256"`
	}
	c.do("GET", base+"/text?path="+qp(f), nil).json(t, &tf)
	os.WriteFile(f, []byte("changed elsewhere\n"), 0o644)
	r := c.do("PUT", base+"/text", map[string]any{"path": f, "content": "secret-content-2\n", "expect_sha256": tf.SHA256})
	var p Problem
	r.json(t, &p)
	if r.status != 409 || p.Code != "file_changed" || !strings.HasPrefix(p.Detail, "This file changed on the host after you opened it. Reload it or explicitly overwrite the newer version.") {
		t.Fatalf("conflict: %d %+v", r.status, p)
	}
	if b, _ := os.ReadFile(f); string(b) != "changed elsewhere\n" {
		t.Fatal("overwrote the newer version")
	}
	if r := c.do("PUT", base+"/text", map[string]any{"path": f, "content": "secret-content-2\n", "expect_sha256": tf.SHA256, "force": true}); r.status != 200 {
		t.Fatalf("forced save: %d %s", r.status, r.body)
	}

	// Streamed upload and download, and refusal to replace without overwrite.
	up := filepath.Join(dir, "up.bin")
	if r := c.raw("PUT", base+"/content?path="+qp(up), strings.NewReader("secret-content-3")); r.status != 200 {
		t.Fatalf("upload: %d %s", r.status, r.body)
	}
	if r := c.raw("PUT", base+"/content?path="+qp(up), strings.NewReader("x")); r.status != 409 {
		t.Errorf("upload over an existing file without overwrite: %d", r.status)
	}
	if r := c.raw("GET", base+"/content?path="+qp(up), nil); r.status != 200 || string(r.body) != "secret-content-3" ||
		!strings.Contains(r.header.Get("Content-Disposition"), "up.bin") {
		t.Errorf("download: %d %q %v", r.status, r.body, r.header)
	}

	// Elevation is never implied: this host has no sudo.
	if r := c.do("GET", base+"?path="+qp(dir)+"&elevated=true", nil); r.status < 400 {
		t.Errorf("elevated listing without sudo: %d", r.status)
	}

	a := c.do("GET", "/api/v1/audit?action=file.&limit=100", nil)
	for _, want := range []string{`"file.write"`, `"file.upload"`, `"after_sha256"`} {
		if !strings.Contains(string(a.body), want) {
			t.Errorf("audit lacks %s", want)
		}
	}
	if strings.Contains(string(a.body), "secret-content") {
		t.Error("audit contains file content")
	}
}

func TestFileDeploymentAPI(t *testing.T) {
	h := newHarness(t)
	h.mkUser("admin", auth.RoleAdmin)
	c := h.login("admin")
	id := readyHost(t, h, c, "deploy")
	dest := filepath.Join(t.TempDir(), "app.conf")

	var art struct {
		SHA256 string `json:"sha256"`
		IsText bool   `json:"is_text"`
	}
	r := c.raw("POST", "/api/v1/artifacts?name=app.conf", strings.NewReader("key = 1\n"))
	if r.status != 201 {
		t.Fatalf("artifact: %d %s", r.status, r.body)
	}
	r.json(t, &art)
	step := map[string]any{"type": "file_deploy", "artifact": art.SHA256, "path": dest}

	// The UI sends the step exactly as it would appear in a job.
	var pv struct {
		Hosts []struct {
			Preview *struct {
				State string `json:"state"`
				Diff  string `json:"diff"`
			} `json:"preview"`
		} `json:"hosts"`
	}
	r = c.do("POST", "/api/v1/file-deployments/preview", map[string]any{"targets": map[string]any{"hosts": []string{id}}, "step": step})
	if r.status != 200 {
		t.Fatalf("preview: %d %s", r.status, r.body)
	}
	r.json(t, &pv)
	if len(pv.Hosts) != 1 || pv.Hosts[0].Preview == nil || pv.Hosts[0].Preview.State != "create" || !strings.Contains(pv.Hosts[0].Preview.Diff, "+key = 1") {
		t.Fatalf("preview: %s", r.body)
	}
	if _, err := os.Stat(dest); err == nil {
		t.Fatal("preview created the file")
	}

	// Rollback fields can't be set by clients.
	bad := map[string]any{"type": "file_deploy", "artifact": art.SHA256, "path": dest, "rollback_of": "fdep_x"}
	if r := c.do("POST", "/api/v1/jobs", map[string]any{"steps": []any{bad}, "targets": map[string]any{"hosts": []string{id}}}); r.status != 422 {
		t.Errorf("client-set rollback_of: %d %s", r.status, r.body)
	}

	var j jobJSON
	c.do("POST", "/api/v1/jobs", map[string]any{"steps": []any{step}, "targets": map[string]any{"hosts": []string{id}}}).json(t, &j)
	if j = waitJob(t, c, j.ID); j.Counts["succeeded"] != 1 {
		t.Fatalf("deploy job: %+v", j)
	}
	if b, _ := os.ReadFile(dest); string(b) != "key = 1\n" {
		t.Fatalf("deployed content %q", b)
	}
	var list struct {
		Items []struct {
			ID      string `json:"id"`
			Outcome string `json:"outcome"`
		} `json:"items"`
	}
	c.do("GET", "/api/v1/file-deployments?host="+id, nil).json(t, &list)
	if len(list.Items) != 1 || list.Items[0].Outcome != "installed" {
		t.Fatalf("history: %+v", list)
	}

	// Roll back: the file didn't exist before, so it is removed.
	c.do("POST", "/api/v1/file-deployments/"+list.Items[0].ID+"/rollback", nil).json(t, &j)
	if j = waitJob(t, c, j.ID); j.Counts["succeeded"] != 1 {
		t.Fatalf("rollback job: %+v", j)
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Fatal("rollback did not remove the created file")
	}
}
