package desired

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"drool/internal/model"
)

func TestPackagesOneAggregatedJob(t *testing.T) {
	e := newEnv(t, "a1", "a2", "a3", "a4")
	// a1: curl missing, telnet present; a2: only telnet present; a3: only
	// curl missing; a4: compliant.
	for h, pk := range map[string]map[string]string{"a1": {"telnet": "0.17"}, "a2": {"curl": "8.0", "telnet": "0.17"}, "a3": {}, "a4": {"curl": "8.0"}} {
		fh := e.f.host("h_" + h)
		e.f.do(func() {
			for n, v := range pk {
				fh.packages[n] = v
			}
		})
	}
	e.baseline("Server Defaults", []Resource{pkgR("curl", StateInstalled), pkgR("telnet", StateAbsent)}, everywhere)
	e.step()
	e.checkAll()
	want := map[string][2]string{"a1": {StatusDrift, StatusDrift}, "a2": {StatusCompliant, StatusDrift}, "a3": {StatusDrift, StatusCompliant}, "a4": {StatusCompliant, StatusCompliant}}
	for h, w := range want {
		if c, tn := e.status(h, "package:curl"), e.status(h, "package:telnet"); c != w[0] || tn != w[1] {
			t.Fatalf("%s: curl %s telnet %s", h, c, tn)
		}
	}
	r := e.resource("a2", "package:telnet")
	if DiffText(r.Diffs) != "installed: absent → installed (0.17)" {
		t.Fatal(DiffText(r.Diffs))
	}
	p, err := e.s.PreviewRestore(e.ctx, Selection{}, RestoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Jobs) != 1 || len(p.Items) != 4 || p.Hosts != 3 {
		t.Fatalf("plan: %d jobs, %d items, %d hosts", len(p.Jobs), len(p.Items), p.Hosts)
	}
	j := p.Jobs[0]
	// One step per correction, installs before removals; each host runs
	// only its own.
	if strings.Join(j.StepText, " | ") != "install curl | remove telnet" {
		t.Fatalf("steps %v", j.StepText)
	}
	masks := map[string]string{}
	for _, h := range j.Hosts {
		masks[h.HostName] = fmt.Sprint(h.Steps)
	}
	if masks["a1"] != "[0 1]" || masks["a2"] != "[1]" || masks["a3"] != "[0]" {
		t.Fatalf("masks %v", masks)
	}
	// Three hosts: the recommended rollout (one canary, then batches).
	if !j.Waves[0].Canary || j.Waves[0].Hosts[0] != "a1" || j.Strategy.FailureThreshold == nil || *j.Strategy.FailureThreshold != 2 {
		t.Fatalf("rollout %+v", j)
	}
	res, err := e.s.Restore(e.ctx, Selection{}, RestoreOptions{}, p.Hash, operator)
	if err != nil {
		t.Fatal(err)
	}
	e.waitJobs()
	if got := strings.Join(e.exec.ops("h_a2"), ","); got != "remove telnet" {
		t.Fatalf("a2 ran %q", got)
	}
	if got := strings.Join(e.exec.ops("h_a1"), ","); got != "install curl,remove telnet" {
		t.Fatalf("a1 ran %q", got)
	}
	if len(e.exec.ops("h_a4")) != 0 {
		t.Fatal("a compliant host ran something")
	}
	for h := range want {
		for _, k := range []string{"package:curl", "package:telnet"} {
			if st := e.status(h, k); st != StatusCompliant {
				t.Fatalf("%s %s after: %s", h, k, st)
			}
		}
	}
	var hosts string
	e.st.Reader.QueryRow(`SELECT group_concat(host_name || ':' || COALESCE(steps, 'all'), ' ') FROM (SELECT * FROM job_hosts WHERE job_id = ? ORDER BY position)`, res.Jobs[0]).Scan(&hosts)
	if hosts != "a1:[0,1] a2:[1] a3:[0]" {
		t.Fatalf("job hosts %q", hosts)
	}
	// Re-running a correction job is refused (it would run every step on
	// every host): see the API test; here, nothing is left to restore.
	if _, err := e.s.Restore(e.ctx, Selection{}, RestoreOptions{}, "", operator); !errors.Is(err, ErrNothingToRestore) {
		t.Fatal(err)
	}
}

func TestPackageRemovalSafeguards(t *testing.T) {
	e := newEnv(t, "a1")
	fh := e.f.host("h_a1")
	e.f.do(func() {
		fh.packages["telnet"], fh.packages["telnet-extras"] = "0.17", "1"
		fh.dependents["telnet"] = []string{"telnet-extras"}
	})
	e.baseline("No telnet", []Resource{pkgR("telnet", StateAbsent)}, everywhere)
	e.step()
	e.checkAll()
	p, err := e.s.PreviewRestore(e.ctx, Selection{}, RestoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Removals) != 1 || strings.Join(p.Removals[0].Also, ",") != "telnet-extras" || len(p.Warnings) == 0 ||
		!strings.Contains(p.Warnings[0].Message, "would also remove telnet-extras") {
		t.Fatalf("preview %+v", p)
	}
	if _, err := e.s.Restore(e.ctx, Selection{}, RestoreOptions{}, p.Hash, operator); err != nil {
		t.Fatal(err)
	}
	e.waitJobs()
	r := e.resource("a1", "package:telnet")
	if r.Status != StatusFailed || !strings.Contains(r.Note, "would also remove 1 package(s): telnet-extras") {
		t.Fatalf("%s %q", r.Status, r.Note)
	}
	if fh.packages["telnet-extras"] == "" {
		t.Fatal("a dependent package was removed without permission")
	}
	// Explicitly allowed: the removal goes through.
	p, _ = e.s.PreviewRestore(e.ctx, Selection{}, RestoreOptions{AllowRemovals: true})
	if _, err := e.s.Restore(e.ctx, Selection{}, RestoreOptions{AllowRemovals: true}, p.Hash, operator); err != nil {
		t.Fatal(err)
	}
	e.waitJobs()
	if st := e.status("a1", "package:telnet"); st != StatusCompliant {
		t.Fatal(st)
	}
}

func TestUnsupportedPackageManager(t *testing.T) {
	e := newEnv(t, "arch")
	e.f.host("h_arch").manager = "pacman"
	e.baseline("Base", []Resource{pkgR("curl", StateInstalled)}, everywhere)
	e.step()
	e.checkAll()
	r := e.resource("arch", "package:curl")
	if r.Status != StatusUnknown || !strings.Contains(r.Note, `unsupported package manager "pacman"`) {
		t.Fatalf("%s %q", r.Status, r.Note)
	}
	p, _ := e.s.PreviewRestore(e.ctx, Selection{}, RestoreOptions{})
	if len(p.Items) != 0 || len(p.Excluded) != 1 {
		t.Fatalf("plan %+v", p)
	}
}

func TestManagedFiles(t *testing.T) {
	e := newEnv(t, "w1", "w2", "w3", "w4")
	good := "server {\n  listen 80;\n}\n"
	sha := e.upload("site.conf", good)
	for _, h := range []string{"w1", "w2", "w3"} {
		fh := e.f.host("h_" + h)
		e.f.do(func() {
			fh.files["/etc/nginx/site.conf"] = &fakeFile{kind: "file", content: []byte(good), owner: "root", group: "root", mode: 0o644}
		})
	}
	w4 := e.f.host("h_w4")
	e.f.do(func() {
		e.f.hosts["h_w2"].files["/etc/nginx/site.conf"].mode = 0o666                          // permissions drift
		e.f.hosts["h_w3"].files["/etc/nginx/site.conf"].content = []byte("server { evil }\n") // content drift
		// w4: missing; and a symlink elsewhere
		w4.files["/etc/nginx/other.conf"] = &fakeFile{kind: "symlink", target: "/tmp/x"}
	})
	res := []Resource{{Kind: KindFile, Path: "/etc/nginx/site.conf", Artifact: sha, Mode: "0644", Owner: "root", Elevated: true, Validate: "nginx -t -c {path}"}}
	e.baseline("Web", res, everywhere)
	e.step()
	e.checkAll()
	const key = "file:/etc/nginx/site.conf"
	if st := e.status("w1", key); st != StatusCompliant {
		t.Fatal("w1", st)
	}
	if d := DiffText(e.resource("w2", key).Diffs); d != "mode: 0644 → 0666" {
		t.Fatal("w2", d)
	}
	if d := DiffText(e.resource("w3", key).Diffs); !strings.HasPrefix(d, "content: sha256 ") {
		t.Fatal("w3", d)
	}
	if d := DiffText(e.resource("w4", key).Diffs); d != "exists: present → missing" {
		t.Fatal("w4", d)
	}
	// Review difference: hashes and metadata only without content access;
	// a text diff with it.
	rv, err := e.s.ReviewFile(e.ctx, "h_w3", key, false)
	if err != nil || rv.State != "replace" || rv.Diff != "" || !strings.Contains(rv.DiffNote, "files.read") {
		t.Fatalf("%+v %v", rv, err)
	}
	rv, err = e.s.ReviewFile(e.ctx, "h_w3", key, true)
	if err != nil || !strings.Contains(rv.Diff, "-server { evil }") || !strings.Contains(rv.Diff, "+  listen 80;") {
		t.Fatalf("%+v %v", rv, err)
	}
	// Review never changes the host.
	if string(e.f.hosts["h_w3"].files["/etc/nginx/site.conf"].content) != "server { evil }\n" {
		t.Fatal("review changed the file")
	}
	// Validation fails on w3 only: its live file stays untouched; the others are restored.
	e.f.host("h_w3").validateOK = func(string, []byte) bool { return false }
	p, err := e.s.PreviewRestore(e.ctx, Selection{}, RestoreOptions{Strategy: &model.Strategy{Concurrency: 5}})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Items) != 3 || len(p.Needs) != 2 || p.Needs[0] != "files.deploy" || p.Needs[1] != "files.elevate" {
		t.Fatalf("plan %d items needs %v", len(p.Items), p.Needs)
	}
	if _, err := e.s.Restore(e.ctx, Selection{}, RestoreOptions{Strategy: &model.Strategy{Concurrency: 5}}, p.Hash, operator); err != nil {
		t.Fatal(err)
	}
	e.waitJobs()
	if st := e.status("w2", key); st != StatusCompliant {
		t.Fatal("w2 after", st)
	}
	if st := e.status("w4", key); st != StatusCompliant {
		t.Fatal("w4 after", st)
	}
	r := e.resource("w3", key)
	if r.Status != StatusFailed || !strings.Contains(r.Note, "validation failed") {
		t.Fatalf("w3 after: %s %q", r.Status, r.Note)
	}
	if string(e.f.hosts["h_w3"].files["/etc/nginx/site.conf"].content) != "server { evil }\n" {
		t.Fatal("a failed validation changed the live file")
	}
	// Accept current state on w3: the host's content becomes its own
	// desired content (captured in the artifact store); the baseline is
	// unchanged and w1 still uses the baseline's content.
	acc, err := e.s.Accept(e.ctx, "h_w3", []string{key}, "w3 runs a custom vhost", admin)
	if err != nil {
		t.Fatal(err)
	}
	a := acc.Accepted[0]
	if a.Resource == nil || a.Resource.Artifact != shaOf([]byte("server { evil }\n")) || a.Resource.Validate != "nginx -t -c {path}" || a.Resource.Mode != "0644" {
		t.Fatalf("accepted %+v", a)
	}
	if _, err := e.arts.Get(e.ctx, a.Resource.Artifact); err != nil {
		t.Fatal("the accepted content isn't stored:", err)
	}
	r = e.resource("w3", key)
	if r.Status != StatusCompliant || r.Source.Level != LevelHost || r.Source.Label != "Custom for this host" {
		t.Fatalf("w3 after accept: %+v", r)
	}
	h := e.history("w3", key)
	if h[0].Kind != "accepted" || h[0].Resolution != "configuration" || h[0].Actor != "admin" || !strings.Contains(h[0].Detail, "not a restore") {
		t.Fatalf("history %+v", h[0])
	}
	bv, _ := e.s.Baselines(e.ctx, false)
	if bv[0].Revision != 1 || bv[0].Resources[0].Artifact != sha {
		t.Fatal("accepting changed the baseline")
	}
	if r := e.resource("w1", key); r.Desired.Artifact != sha {
		t.Fatal("accepting on one host changed another")
	}
}

func TestFileSymlinkAndPermissions(t *testing.T) {
	e := newEnv(t, "w1")
	sha := e.upload("x", "x\n")
	h := e.f.host("h_w1")
	e.f.do(func() {
		h.files["/etc/app/link.conf"] = &fakeFile{kind: "symlink", target: "/srv/real.conf"}
		h.files["/etc/app/secret.conf"] = &fakeFile{kind: "file", content: []byte("x\n"), owner: "root", group: "root", mode: 0o600, unreadableUnlessRoot: true}
	})
	e.baseline("App", []Resource{{Kind: KindFile, Path: "/etc/app/link.conf", Artifact: sha}, {Kind: KindFile, Path: "/etc/app/secret.conf", Artifact: sha}}, everywhere)
	e.step()
	e.checkAll()
	r := e.resource("w1", "file:/etc/app/link.conf")
	if r.Status != StatusDrift || r.Restorable || !strings.Contains(r.Blocker, "never replaces a symlink") || r.Acceptable {
		t.Fatalf("symlink %+v", r)
	}
	if _, err := e.s.Accept(e.ctx, "h_w1", []string{"file:/etc/app/link.conf"}, "", admin); err == nil {
		t.Fatal("a symlink was accepted")
	}
	p, _ := e.s.PreviewRestore(e.ctx, Selection{}, RestoreOptions{})
	if len(p.Items) != 0 || len(p.Excluded) != 2 {
		t.Fatalf("plan %+v", p)
	}
	// Not readable without root: unknown, with the reason (never compliant).
	r = e.resource("w1", "file:/etc/app/secret.conf")
	if r.Status != StatusUnknown || !strings.Contains(r.Note, "permission denied") {
		t.Fatalf("secret %+v", r)
	}
}

func TestConflictBlocksRestoreUntilResolved(t *testing.T) {
	e := newEnv(t, "lab")
	e.setUnit("lab", "nginx.service", "inactive", "disabled")
	e.reportAll()
	e.group("g_web", "Web Servers", "lab")
	e.group("g_lab", "Maintenance Lab", "lab")
	e.baseline("Web", []Resource{svc("nginx", StateRunning, nil)}, inGroup("g_web"))
	e.baseline("Lab", []Resource{svc("nginx", StateStopped, nil)}, inGroup("g_lab"))
	e.step()
	const key = "service:nginx.service"
	r := e.resource("lab", key)
	if r.Status != StatusConflict || r.Restorable || len(r.Source.Competing) != 2 {
		t.Fatalf("%+v", r)
	}
	if k := kinds(e.history("lab", key)); k != "conflict_detected" {
		t.Fatal(k)
	}
	p, _ := e.s.PreviewRestore(e.ctx, Selection{}, RestoreOptions{})
	if len(p.Items) != 0 || len(p.Excluded) != 1 || !strings.Contains(p.Excluded[0].Reason, "Conflict between groups Maintenance Lab and Web Servers") {
		t.Fatalf("plan %+v", p)
	}
	// The impact of a priority change is previewed, then saved.
	imp, err := e.s.PreviewPriority(e.ctx, "g_web", 10)
	if err != nil || imp.Counts.ResolvedConflicts != 1 || imp.ResolvedConflicts[0].After != StatusDrift {
		t.Fatalf("%+v %v", imp, err)
	}
	if err := e.s.SetGroupPriority(e.ctx, "g_web", 10); err != nil {
		t.Fatal(err)
	}
	e.step()
	r = e.resource("lab", key)
	if r.Status != StatusDrift || r.Source.Groups[0].Name != "Web Servers" {
		t.Fatalf("after priority: %+v", r)
	}
	if k := kinds(e.history("lab", key)); k != "conflict_detected,conflict_resolved" {
		t.Fatal(k)
	}
}

func TestAcceptResolvesConflictForOneHost(t *testing.T) {
	e := newEnv(t, "lab", "web")
	e.setUnit("lab", "nginx.service", "inactive", "disabled")
	e.setUnit("web", "nginx.service", "inactive", "disabled")
	e.reportAll()
	e.group("g_web", "Web Servers", "lab", "web")
	e.group("g_lab", "Maintenance Lab", "lab", "web")
	e.baseline("Web", []Resource{svc("nginx", StateRunning, nil)}, inGroup("g_web"))
	e.baseline("Lab", []Resource{svc("nginx", StateStopped, nil)}, inGroup("g_lab"))
	e.step()
	if _, err := e.s.Accept(e.ctx, "h_lab", []string{"service:nginx.service"}, "lab keeps nginx off", admin); err != nil {
		t.Fatal(err)
	}
	if r := e.resource("lab", "service:nginx.service"); r.Status != StatusCompliant || r.Desired.State != StateStopped {
		t.Fatalf("%+v", r)
	}
	if st := e.status("web", "service:nginx.service"); st != StatusConflict {
		t.Fatal("the other host's conflict was resolved too:", st)
	}
	// Reset to inherited: the conflict is back.
	if err := e.s.ResetOverride(e.ctx, "h_lab", "service:nginx.service"); err != nil {
		t.Fatal(err)
	}
	e.step()
	if st := e.status("lab", "service:nginx.service"); st != StatusConflict {
		t.Fatal(st)
	}
}

func TestAcceptCurrentIsHostSpecific(t *testing.T) {
	e := newEnv(t, "lab-box", "web-1")
	e.setUnit("lab-box", "nginx.service", "inactive", "enabled")
	e.setUnit("web-1", "nginx.service", "active", "enabled")
	e.reportAll()
	b := e.baseline("Server Defaults", []Resource{svc("nginx", StateRunning, on())}, everywhere)
	e.step()
	if _, err := e.s.Accept(e.ctx, "h_web-1", []string{"service:nginx.service"}, "", admin); err == nil {
		t.Fatal("accepting a compliant resource")
	}
	acc, err := e.s.Accept(e.ctx, "h_lab-box", []string{"service:nginx.service"}, "", admin)
	if err != nil {
		t.Fatal(err)
	}
	// Exactly the managed fields, as observed: stopped, still enabled.
	if got := acc.Accepted[0].Resource.Want(); got != "stopped, enabled at boot" {
		t.Fatal(got)
	}
	v, err := e.s.Baseline(e.ctx, b.ID)
	if err != nil || v.Revision != 1 || v.Resources[0].State != StateRunning {
		t.Fatalf("the baseline changed: %+v", v)
	}
	if st := e.status("web-1", "service:nginx.service"); st != StatusCompliant {
		t.Fatal(st)
	}
	r := e.resource("lab-box", "service:nginx.service")
	if r.Status != StatusCompliant || r.Source.Override == nil || r.Source.Override.Origin != "accept" || len(r.Source.Shadowed) != 1 {
		t.Fatalf("%+v", r)
	}
	if k := kinds(e.history("lab-box", "service:nginx.service")); k != "drift_detected,accepted(configuration)" {
		t.Fatal(k)
	}
	var n int
	e.st.Reader.QueryRow(`SELECT count(*) FROM audit_events WHERE action = 'desired.accept'`).Scan(&n)
	if n != 1 {
		t.Fatal("audit", n)
	}
	// A baseline edit that makes the host compliant is "resolved by
	// configuration", never "restored".
	e.setUnit("web-1", "nginx.service", "inactive", "enabled")
	e.reportAll()
	e.step()
	if _, _, err := e.s.UpdateBaseline(e.ctx, b.ID, BaselineInput{Name: "Server Defaults", Resources: []Resource{svc("nginx", "", on())},
		Assignments: []Assignment{everywhere}}, admin); err != nil {
		t.Fatal(err)
	}
	e.step()
	h := e.history("web-1", "service:nginx.service")
	if h[0].Kind != "resolved_by_configuration" || h[0].Resolution != "configuration" || h[0].Baseline.Revision != 2 {
		t.Fatalf("%+v", h[0])
	}
}

func TestRevisionsAndImpactPreview(t *testing.T) {
	e := newEnv(t, "a1", "a2", "a3")
	for _, h := range []string{"a1", "a2", "a3"} {
		e.setUnit(h, "caddy.service", "active", "enabled")
	}
	e.setUnit("a3", "caddy.service", "inactive", "enabled")
	e.f.do(func() { e.f.hosts["h_a2"].packages["telnet"] = "1" })
	e.reportAll()
	b := e.baseline("Base", []Resource{svc("caddy", StateRunning, nil), pkgR("telnet", StateAbsent)}, everywhere)
	e.step()
	e.checkAll()
	in := BaselineInput{Name: "Base", Resources: []Resource{svc("caddy", StateStopped, nil), pkgR("curl", StateInstalled)}, Assignments: []Assignment{everywhere}}
	imp, err := e.s.PreviewBaseline(e.ctx, b.ID, in)
	if err != nil {
		t.Fatal(err)
	}
	if len(imp.Added) != 1 || imp.Added[0].Key != "package:curl" || len(imp.Removed) != 1 || imp.Removed[0].Key != "package:telnet" ||
		len(imp.Modified) != 1 || imp.Modified[0].From != "running" || imp.Modified[0].To != "stopped" {
		t.Fatalf("resource changes %+v %+v %+v", imp.Added, imp.Removed, imp.Modified)
	}
	if imp.HostsAffected != 3 || !imp.Changed {
		t.Fatalf("affected %d", imp.HostsAffected)
	}
	// a1 (compliant) would drift (caddy runs); a2 already differs; a3
	// would comply on caddy; curl hasn't been checked anywhere, so every
	// host also has something to check after saving.
	if imp.Counts.BecomeDrifted != 1 || imp.BecomeDrifted[0].HostName != "a1" || imp.Counts.ToCheck != 3 {
		t.Fatalf("counts %+v", imp.Counts)
	}
	if d := strings.Join(imp.BecomeDrifted[0].Detail, "; "); !strings.Contains(d, "caddy.service: stopped — differs (state: stopped → running)") ||
		!strings.Contains(d, "package curl: installed — checked after saving") || !strings.Contains(d, "package telnet: no longer managed") {
		t.Fatal(d)
	}
	// Nothing saved by the preview.
	if v, _ := e.s.Baseline(e.ctx, b.ID); v.Revision != 1 {
		t.Fatal("preview saved")
	}
	v, changed, err := e.s.UpdateBaseline(e.ctx, b.ID, in, admin)
	if err != nil || !changed || v.Revision != 2 || len(v.Revisions) != 2 {
		t.Fatalf("%+v %v %v", v, changed, err)
	}
	// Saving the same again creates no revision.
	if _, changed, _ := e.s.UpdateBaseline(e.ctx, b.ID, in, admin); changed {
		t.Fatal("an unchanged save made a revision")
	}
	old, _, err := e.s.Revision(e.ctx, b.ID, 1)
	if err != nil || len(old.Resources) != 2 || old.Resources[1].Key() != "service:caddy.service" || old.Resources[1].State != StateRunning {
		t.Fatal("revision 1 was rewritten", err)
	}
	e.step()
	e.checkAll()
	// telnet isn't managed any more: a2's drift ended by configuration.
	h := e.history("a2", "package:telnet")
	if h[0].Kind != "no_longer_managed" || h[0].Resolution != "configuration" {
		t.Fatalf("%s", kinds(h))
	}
	var aud string
	e.st.Reader.QueryRow(`SELECT details_json FROM audit_events WHERE action = 'desired.baseline.update'`).Scan(&aud)
	var d map[string]any
	json.Unmarshal([]byte(aud), &d)
	if d["revision"] != float64(2) || fmt.Sprint(d["added"]) != "[package:curl]" {
		t.Fatalf("audit %s", aud)
	}
}

func TestJobWarnings(t *testing.T) {
	e := newEnv(t, "a1", "a2")
	e.setUnit("a1", "jellyfin.service", "active", "enabled")
	e.reportAll()
	e.baseline("Media", []Resource{svc("jellyfin", StateRunning, on())}, onHost("a1"))
	e.baseline("Server Defaults", []Resource{pkgR("telnet", StateAbsent)}, everywhere)
	e.step()
	ws := e.s.JobWarnings(e.ctx, []string{"h_a1", "h_a2"}, []model.Operation{
		{Type: model.OpService, Service: &model.ServiceOp{Name: "jellyfin", State: "stopped"}},
		{Type: model.OpPackage, Package: &model.PackageOp{Action: "install", Names: []string{"telnet"}}},
		{Type: model.OpService, Service: &model.ServiceOp{Name: "jellyfin", State: "restarted"}},
	})
	if len(ws) != 2 {
		t.Fatalf("%+v", ws)
	}
	if ws[0].Message != "On a1: jellyfin.service is managed to keep running (Baseline Media, assigned to this host). Stopping it will create desired-state drift." {
		t.Fatal(ws[0].Message)
	}
	if ws[1].Message != "On a1, a2: telnet is prohibited by baseline “Server Defaults”. Installing it will create drift." || ws[1].Auto {
		t.Fatal(ws[1].Message)
	}
}
