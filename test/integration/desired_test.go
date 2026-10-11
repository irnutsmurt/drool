//go:build integration

package integration

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// TestDesiredState runs desired state (M14) against real hosts over SSH:
// an APT and a DNF container. Packages are checked with the read-only
// query and corrected with ordinary package jobs (including the removal
// safeguard); a managed file is checked by hash on the host, reviewed,
// restored through the file deployment (as root) and accepted; a
// controller killed in the middle of a correction never re-runs it.
// (The containers have no systemd: services are covered by the VM test.)
func TestDesiredState(t *testing.T) {
	freshController(t) // a controller of its own: nothing from other tests (M17)
	var picks []distro
	seen := map[string]bool{}
	for _, d := range selected() {
		if !seen[d.PkgManager] {
			seen[d.PkgManager] = true
			picks = append(picks, d)
		}
	}
	for _, d := range picks {
		t.Run(d.Name, func(t *testing.T) { desiredOn(t, d) })
	}
}

type dsResource struct {
	Key     string `json:"key"`
	Status  string `json:"status"`
	Note    string `json:"note"`
	Current string `json:"current"`
	Source  struct {
		Level string `json:"level"`
		Label string `json:"label"`
	} `json:"source"`
	Diffs []struct{ Field, Desired, Current string } `json:"diffs"`
}

type dsHost struct {
	Status    string       `json:"status"`
	Resources []dsResource `json:"resources"`
}

func dsGet(t *testing.T, hostID string) map[string]dsResource {
	t.Helper()
	var h dsHost
	ctl.must(t, "GET", "/api/v1/desired/hosts/"+hostID, nil, &h)
	out := map[string]dsResource{}
	for _, r := range h.Resources {
		out[r.Key] = r
	}
	return out
}

// dsWait refreshes the host until a resource has a status.
func dsWait(t *testing.T, hostID, key, status string, d time.Duration) dsResource {
	t.Helper()
	var r dsResource
	ctl.must(t, "POST", "/api/v1/desired/hosts/"+hostID+"/refresh", nil, nil)
	last := time.Now()
	waitFor(t, d, key+" "+status, func() bool {
		r = dsGet(t, hostID)[key]
		if r.Status == status {
			return true
		}
		if time.Since(last) > 10*time.Second && r.Status != "reconciling" {
			ctl.must(t, "POST", "/api/v1/desired/hosts/"+hostID+"/refresh", nil, nil)
			last = time.Now()
		}
		return false
	})
	return r
}

type dsPlan struct {
	Plan struct {
		Hash     string                           `json:"hash"`
		Items    []struct{ Key, HostName string } `json:"items"`
		Excluded []struct{ Key, Reason string }   `json:"excluded"`
		Removals []struct {
			Package string   `json:"package"`
			Also    []string `json:"also"`
		} `json:"removals"`
		Jobs []struct {
			StepText []string `json:"step_text"`
		} `json:"jobs"`
	} `json:"plan"`
}

func dsRestore(t *testing.T, sel map[string]any) (dsPlan, string) {
	t.Helper()
	var p dsPlan
	ctl.must(t, "POST", "/api/v1/desired/restore/preview", sel, &p)
	if len(p.Plan.Items) == 0 {
		t.Fatalf("nothing to restore: %+v", p.Plan.Excluded)
	}
	body := map[string]any{}
	for k, v := range sel {
		body[k] = v
	}
	body["plan_hash"] = p.Plan.Hash
	var res struct {
		Jobs []string `json:"jobs"`
	}
	ctl.must(t, "POST", "/api/v1/desired/restore", body, &res)
	if len(res.Jobs) != 1 {
		t.Fatalf("jobs %v", res.Jobs)
	}
	return p, res.Jobs[0]
}

func desiredOn(t *testing.T, d distro) {
	img := image(t, d)
	c := startHost(t, img, "ds-"+d.Name, true, netOuter)
	h := addHost(t, "ds-"+d.Name, c, hostOpts{})
	// The packages of the removal pair, installed with an ordinary job.
	var j struct{ ID string }
	ctl.must(t, "POST", "/api/v1/jobs", map[string]any{"steps": []map[string]any{{"type": "package", "action": "install", "names": []string{d.Pkg}}},
		"targets": map[string]any{"hosts": []string{h.ID}}}, &j)
	if st := waitJob(t, j.ID, 10*time.Minute); st != "completed" {
		t.Fatalf("install %s: %s", d.Pkg, st)
	}
	c.exec(t, "mkdir -p /etc/drool-it")
	content := "listen = 8080\nworkers = 4\n"
	sha := putArtifact(t, "app.conf", content)
	file := "/etc/drool-it/app.conf"
	var b struct{ ID string }
	ctl.must(t, "POST", "/api/v1/desired/baselines", map[string]any{
		"name": "IT " + d.Name, "assignments": []map[string]any{{"scope": "host", "host_id": h.ID}},
		"resources": []map[string]any{
			{"kind": "package", "name": d.Dep, "state": "absent"},
			{"kind": "file", "path": file, "artifact": sha, "owner": "root", "group": "root", "mode": "0640", "elevated": true},
		},
	}, &b)
	t.Cleanup(func() { ctl.do("DELETE", "/api/v1/desired/baselines/"+b.ID, nil, nil) })

	// Drift: the prohibited package is installed, the file is missing.
	dep := dsWait(t, h.ID, "package:"+d.Dep, "drift", 3*time.Minute)
	if !strings.HasPrefix(dep.Current, "installed (") {
		t.Fatalf("%s: %+v", d.Dep, dep)
	}
	if f := dsWait(t, h.ID, "file:"+file, "drift", time.Minute); f.Current != "missing" {
		t.Fatalf("file: %+v", f)
	}

	t.Run("restore keeps the removal safeguard", func(t *testing.T) {
		p, job := dsRestore(t, map[string]any{"host_ids": []string{h.ID}})
		if len(p.Plan.Removals) != 1 || !strings.Contains(strings.Join(p.Plan.Removals[0].Also, ","), d.Pkg) {
			t.Fatalf("removal preview %+v", p.Plan.Removals)
		}
		if got := strings.Join(p.Plan.Jobs[0].StepText, " | "); !strings.HasPrefix(got, "Deploy "+file) || !strings.HasSuffix(got, "remove "+d.Dep) {
			t.Fatalf("steps %q", got)
		}
		waitJob(t, job, 10*time.Minute)
		// The file is restored (proven by a fresh check); the removal was
		// refused on the host because it would remove more.
		dsWait(t, h.ID, "file:"+file, "compliant", 3*time.Minute)
		r := dsWait(t, h.ID, "package:"+d.Dep, "failed", 3*time.Minute)
		if !strings.Contains(r.Note, "would also remove") {
			t.Fatalf("%+v", r)
		}
		if out := c.exec(t, "stat -c '%U:%G %a' "+file+"; cat "+file); !strings.HasPrefix(out, "root:root 640") || !strings.Contains(out, "workers = 4") {
			t.Fatalf("deployed file: %q", out)
		}
		// Explicitly allowed: the removal goes through.
		_, job = dsRestore(t, map[string]any{"host_ids": []string{h.ID}, "allow_removals": true})
		waitJob(t, job, 10*time.Minute)
		dsWait(t, h.ID, "package:"+d.Dep, "compliant", 3*time.Minute)
	})

	t.Run("content drift: review, then accept for this host", func(t *testing.T) {
		c.exec(t, "echo 'debug = true' >> "+file)
		r := dsWait(t, h.ID, "file:"+file, "drift", 2*time.Minute)
		if len(r.Diffs) != 1 || r.Diffs[0].Field != "content" {
			t.Fatalf("%+v", r)
		}
		var rv struct {
			File struct {
				State string `json:"state"`
				Diff  string `json:"diff"`
			} `json:"file"`
		}
		ctl.must(t, "GET", "/api/v1/desired/hosts/"+h.ID+"/review?key=file:"+file, nil, &rv)
		if rv.File.State != "replace" || !strings.Contains(rv.File.Diff, "+debug = true") && !strings.Contains(rv.File.Diff, "-debug = true") {
			t.Fatalf("review %+v", rv)
		}
		ctl.must(t, "POST", "/api/v1/desired/hosts/"+h.ID+"/accept", map[string]any{"keys": []string{"file:" + file}, "note": "debug on this host"}, nil)
		r = dsGet(t, h.ID)["file:"+file]
		if r.Status != "compliant" || r.Source.Level != "host" {
			t.Fatalf("after accept %+v", r)
		}
		var bv struct {
			Revision  int `json:"revision"`
			Resources []struct {
				Artifact string `json:"artifact"`
			} `json:"resources"`
		}
		ctl.must(t, "GET", "/api/v1/desired/baselines/"+b.ID, nil, &bv)
		if bv.Revision != 1 {
			t.Fatal("accepting changed the baseline")
		}
	})

	t.Run("a controller restart never re-runs a correction", func(t *testing.T) {
		// Want the package again (a slow install), then kill the controller
		// while the job runs.
		var in map[string]any
		ctl.must(t, "GET", "/api/v1/desired/baselines/"+b.ID, nil, &in)
		ctl.must(t, "PUT", "/api/v1/desired/baselines/"+b.ID, map[string]any{"name": in["name"], "assignments": in["assignments"],
			"resources": []map[string]any{{"kind": "package", "name": d.Pkg, "state": "installed"}}}, nil)
		dsWait(t, h.ID, "package:"+d.Pkg, "drift", 3*time.Minute)
		_, job := dsRestore(t, map[string]any{"host_ids": []string{h.ID}})
		waitFor(t, time.Minute, "job running", func() bool {
			var hs struct {
				Items []struct{ State string } `json:"items"`
			}
			ctl.must(t, "GET", "/api/v1/jobs/"+job+"/hosts", nil, &hs)
			return len(hs.Items) == 1 && hs.Items[0].State == "running"
		})
		ctl.crash()
		if err := ctl.start(); err != nil {
			t.Fatal(err)
		}
		if err := ctl.login(); err != nil {
			t.Fatal(err)
		}
		var jj struct{ Status string }
		ctl.must(t, "GET", "/api/v1/jobs/"+job, nil, &jj)
		if jj.Status != "interrupted" {
			t.Fatalf("job after restart: %s", jj.Status)
		}
		// Settled from a fresh check, not left "restoring", never re-run.
		var r dsResource
		waitFor(t, 3*time.Minute, "settled", func() bool {
			r = dsGet(t, h.ID)["package:"+d.Pkg]
			return r.Status != "reconciling"
		})
		var hist struct {
			Items []struct{ Kind, Detail, JobID string } `json:"items"`
		}
		ctl.must(t, "GET", fmt.Sprintf("/api/v1/desired/history?host=%s&key=package:%s", h.ID, d.Pkg), nil, &hist)
		var jobs struct {
			Items []struct{ ID string } `json:"items"`
		}
		ctl.must(t, "GET", "/api/v1/jobs?host="+h.ID, nil, &jobs)
		corrections := 0
		for _, x := range jobs.Items {
			var jd struct {
				Origin *struct {
					ReconcileID string `json:"reconcile_id"`
				} `json:"origin"`
			}
			ctl.must(t, "GET", "/api/v1/jobs/"+x.ID, nil, &jd)
			if jd.Origin != nil && jd.Origin.ReconcileID != "" {
				corrections++
			}
		}
		if corrections != 3 { // two restores above and this one: nothing re-ran
			t.Fatalf("%d corrective jobs", corrections)
		}
		last := hist.Items[0]
		t.Logf("%s after the restart: %s; history: %s — %s", d.Pkg, r.Status, last.Kind, last.Detail)
		if r.Status == "failed" && !strings.Contains(r.Note, "not re-run automatically") {
			t.Fatalf("%+v", r)
		}
		if r.Status == "compliant" && last.Kind == "restored" {
			t.Fatal("an interrupted correction was claimed as restored")
		}
	})
}
