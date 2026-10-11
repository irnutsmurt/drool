//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"
)

type deployment struct {
	ID         string `json:"id"`
	JobID      string `json:"job_id"`
	HostID     string `json:"host_id"`
	Path       string `json:"path"`
	Kind       string `json:"kind"`
	RollbackOf string `json:"rollback_of"`
	Outcome    string `json:"outcome"`
	Before     struct {
		Exists bool   `json:"exists"`
		SHA256 string `json:"sha256"`
		Meta   struct {
			Owner, Group, Mode string
		} `json:"meta"`
	} `json:"before"`
	CanRollback bool   `json:"can_rollback"`
	Diff        string `json:"diff"`
}

func putArtifact(t *testing.T, name, content string) string {
	t.Helper()
	r, err := ctl.raw(context.Background(), "POST", "/api/v1/artifacts?name="+q(name), strings.NewReader(content))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	b, _ := io.ReadAll(r.Body)
	if r.StatusCode != 201 {
		t.Fatalf("artifact upload: %d %s", r.StatusCode, b)
	}
	var a struct {
		SHA256 string `json:"sha256"`
	}
	json.Unmarshal(b, &a)
	if a.SHA256 != hexSum([]byte(content)) {
		t.Fatalf("artifact hash %s", b)
	}
	return a.SHA256
}

// deployJob submits a file deployment to the targets and waits for it.
func deployJob(t *testing.T, targets map[string]any, step map[string]any) string {
	t.Helper()
	step["type"] = "file_deploy"
	var j struct {
		ID string `json:"id"`
	}
	ctl.must(t, "POST", "/api/v1/jobs", map[string]any{"steps": []any{step}, "targets": targets}, &j)
	waitJob(t, j.ID, 10*time.Minute)
	return j.ID
}

func deployments(t *testing.T, query string) []deployment {
	t.Helper()
	var l struct {
		Items []deployment `json:"items"`
	}
	ctl.must(t, "GET", "/api/v1/file-deployments?"+query, nil, &l)
	return l.Items
}

func rollback(t *testing.T, id string) string {
	t.Helper()
	var j struct {
		ID string `json:"id"`
	}
	ctl.must(t, "POST", "/api/v1/file-deployments/"+id+"/rollback", nil, &j)
	waitJob(t, j.ID, 10*time.Minute)
	return j.ID
}

// TestFileDeployment: a bulk deployment across distros by tag and group,
// with preview, no-op, validation, rollback and a failing service action.
func TestFileDeployment(t *testing.T) {
	freshController(t) // a controller of its own: nothing from other tests (M17)
	ds := selected()
	ca := startHost(t, image(t, ds[0]), "dep-a", true, netOuter)
	cb := startHost(t, image(t, ds[len(ds)-1]), "dep-b", true, netOuter)
	a := addHost(t, "dep-a", ca, hostOpts{})
	b := addHost(t, "dep-b", cb, hostOpts{})
	ctl.must(t, "PATCH", "/api/v1/hosts/"+a.ID, map[string]any{"tags": []string{"deploytest"}}, nil)
	ctl.must(t, "PATCH", "/api/v1/hosts/"+b.ID, map[string]any{"tags": []string{"deploytest"}}, nil)
	var g struct {
		ID string `json:"id"`
	}
	ctl.must(t, "POST", "/api/v1/groups", map[string]any{"name": "deploy-fleet-" + runID, "host_ids": []string{a.ID, b.ID}}, &g)

	const path = "/etc/drool-app/app.conf"
	ca.exec(t, "mkdir -p /etc/drool-app && printf 'key = 0\\n' > "+path+" && chmod 640 "+path)
	// b has no /etc/drool-app yet: its deployment fails.
	v1 := putArtifact(t, "app.conf", "key = 1\n")
	step := func(art string) map[string]any {
		return map[string]any{"artifact": art, "path": path, "elevated": true}
	}
	byTag := map[string]any{"tags": []string{"deploytest"}}
	byGroup := map[string]any{"groups": []string{g.ID}}

	t.Run("preview per host", func(t *testing.T) {
		var pv struct {
			Hosts []struct {
				HostID  string                    `json:"host_id"`
				Error   *struct{ Message string } `json:"error"`
				Preview *struct {
					State string `json:"state"`
					Diff  string `json:"diff"`
				} `json:"preview"`
			} `json:"hosts"`
		}
		ps := step(v1)
		ps["type"] = "file_deploy"
		ctl.must(t, "POST", "/api/v1/file-deployments/preview", map[string]any{"targets": byTag, "step": ps}, &pv)
		if len(pv.Hosts) != 2 {
			t.Fatalf("preview: %+v", pv)
		}
		for _, h := range pv.Hosts {
			switch h.HostID {
			case a.ID:
				if h.Preview == nil || h.Preview.State != "replace" || !strings.Contains(h.Preview.Diff, "-key = 0\n+key = 1") {
					t.Errorf("preview a: %+v %+v", h.Preview, h.Error)
				}
			case b.ID:
				if h.Preview != nil || h.Error == nil || !strings.Contains(h.Error.Message, "/etc/drool-app does not exist") {
					t.Errorf("preview b (no directory) should be inaccessible: %+v %+v", h.Preview, h.Error)
				}
			}
		}
		if cb.cat(t, path) != "<missing>" || ca.cat(t, path) != "key = 0" {
			t.Fatal("preview changed something")
		}
	})

	var firstA, createB string
	t.Run("mixed success and failure by tag", func(t *testing.T) {
		job := deployJob(t, byTag, step(v1))
		ja, jb := getJobHost(t, job, a.ID), getJobHost(t, job, b.ID)
		if ja.State != "succeeded" || ja.changed() == nil || !*ja.changed() {
			t.Errorf("a: %v", ja)
		}
		if jb.State != "failed" || jb.ErrorClass != "ExecutionError" || !strings.Contains(jb.ErrorMessage, "does not exist") {
			t.Errorf("b should fail without the directory: %v", jb)
		}
		if ca.cat(t, path) != "key = 1" {
			t.Error("a content")
		}
		if out := ca.exec(t, "stat -c '%U:%G %a' "+path); out != "root:root 640" {
			t.Errorf("a metadata not kept: %s", out)
		}
		ds := deployments(t, "host="+a.ID)
		if len(ds) != 1 || ds[0].Outcome != "installed" || ds[0].Before.SHA256 != hexSum([]byte("key = 0\n")) {
			t.Fatalf("history a: %+v", ds)
		}
		firstA = ds[0].ID
	})

	t.Run("by group: no-op on a, create on b", func(t *testing.T) {
		cb.exec(t, "mkdir -p /etc/drool-app")
		job := deployJob(t, byGroup, step(v1))
		ja, jb := getJobHost(t, job, a.ID), getJobHost(t, job, b.ID)
		if ja.State != "succeeded" || ja.changed() == nil || *ja.changed() {
			t.Errorf("identical content should be a no-op: %v %v", ja, ja.Steps)
		}
		if jb.State != "succeeded" || cb.cat(t, path) != "key = 1" {
			t.Errorf("b create: %v", jb)
		}
		if out := cb.exec(t, "stat -c '%U:%G %a' "+path); out != "root:root 644" {
			t.Errorf("b default metadata: %s", out)
		}
		ds := deployments(t, "host="+b.ID)
		for _, d := range ds {
			if d.Outcome == "installed" {
				createB = d.ID
			}
		}
		if createB == "" {
			t.Fatalf("history b: %+v", ds)
		}
	})

	t.Run("validation failure leaves the live file", func(t *testing.T) {
		bad := putArtifact(t, "app.conf", "broken\n")
		s := step(bad)
		s["validate"] = "grep -q '^key = ' {path}"
		job := deployJob(t, map[string]any{"hosts": []string{a.ID}}, s)
		ja := getJobHost(t, job, a.ID)
		if ja.State != "failed" || !strings.Contains(ja.ErrorMessage, "live file was not changed") {
			t.Errorf("validation failure: %v", ja)
		}
		if ca.cat(t, path) != "key = 1" {
			t.Fatal("live file changed despite failed validation")
		}
		if ca.temps(t, "/etc/drool-app") != "" {
			t.Error("staged file left behind")
		}
	})

	var v3dep string
	t.Run("validation success, then roll back twice", func(t *testing.T) {
		v3 := putArtifact(t, "app.conf", "key = 3\n")
		s := step(v3)
		s["validate"] = "grep -q '^key = ' {path}"
		s["mode"] = "0600"
		job := deployJob(t, map[string]any{"hosts": []string{a.ID}}, s)
		if ja := getJobHost(t, job, a.ID); ja.State != "succeeded" || ca.cat(t, path) != "key = 3" {
			t.Fatalf("validated deploy: %v", ja)
		}
		ds := deployments(t, "job="+job)
		if len(ds) != 1 {
			t.Fatalf("%+v", ds)
		}
		v3dep = ds[0].ID
		var d deployment
		ctl.must(t, "GET", "/api/v1/file-deployments/"+v3dep, nil, &d)
		if !d.CanRollback || !strings.Contains(d.Diff, "-key = 1\n+key = 3") {
			t.Errorf("deployment detail: %+v", d)
		}

		rb := rollback(t, v3dep)
		if j := getJobHost(t, rb, a.ID); j.State != "succeeded" {
			t.Fatalf("rollback: %v", j)
		}
		if ca.cat(t, path) != "key = 1" {
			t.Fatal("rollback content")
		}
		if out := ca.exec(t, "stat -c '%U:%G %a' "+path); out != "root:root 640" {
			t.Errorf("rollback metadata: %s", out)
		}
		// Rolling back the newer deployment again is refused at run time:
		// the file no longer holds what it installed.
		rb = rollback(t, v3dep)
		if j := getJobHost(t, rb, a.ID); j.State != "failed" {
			t.Errorf("second rollback of the same deployment: %v", j)
		}
		// Back to the original content from before DROOL touched it.
		rb = rollback(t, firstA)
		if j := getJobHost(t, rb, a.ID); j.State != "succeeded" || ca.cat(t, path) != "key = 0" {
			t.Fatalf("rollback to original: %v", j)
		}
		// A file DROOL created is removed by its rollback.
		rb = rollback(t, createB)
		if j := getJobHost(t, rb, b.ID); j.State != "succeeded" || cb.cat(t, path) != "<missing>" {
			t.Fatalf("rollback of a created file: %v", j)
		}
	})

	t.Run("service failure after a successful install is reported separately", func(t *testing.T) {
		v5 := putArtifact(t, "app.conf", "key = 5\n")
		s := step(v5)
		s["service"] = map[string]any{"name": "drool-no-such-unit", "action": "reload"}
		job := deployJob(t, map[string]any{"hosts": []string{a.ID}}, s)
		ja := getJobHost(t, job, a.ID)
		if ja.State != "failed" || !strings.Contains(ja.ErrorMessage, "file was installed") {
			t.Errorf("service failure: %v", ja)
		}
		if ca.cat(t, path) != "key = 5" {
			t.Error("file should be installed despite the service failure")
		}
		ds := deployments(t, "job="+job)
		if len(ds) != 1 || ds[0].Outcome != "installed" {
			t.Errorf("history: %+v", ds)
		}
	})

	t.Run("audit has hashes, not content", func(t *testing.T) {
		var au struct {
			Items []struct {
				Action string `json:"action"`
			} `json:"items"`
		}
		r, _ := ctl.raw(context.Background(), "GET", "/api/v1/audit?action=file.&limit=500", nil)
		raw, _ := io.ReadAll(r.Body)
		r.Body.Close()
		json.Unmarshal(raw, &au)
		n := map[string]int{}
		for _, it := range au.Items {
			n[it.Action]++
		}
		if n["file.deploy"] < 4 || n["file.rollback"] < 3 || n["file.preview"] < 1 {
			t.Errorf("audit counts: %v", n)
		}
		for _, s := range []string{"key = ", "broken"} {
			if strings.Contains(string(raw), s) {
				t.Errorf("audit contains file content %q", s)
			}
		}
		if !strings.Contains(string(raw), v1) {
			t.Error("audit lacks artifact hash")
		}
	})
}
