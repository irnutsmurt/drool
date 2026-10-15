package api

import (
	"context"
	"strings"
	"testing"
	"time"

	"drool/internal/auth"
	"drool/internal/monitor"
	"drool/internal/monitor/agentmon"
	"drool/internal/monitor/monsvc"
	"drool/internal/monitor/services"
)

// The expected state of services (final After-M17 batch), through the real
// health evaluation and alerting: running + stopped is a problem, stopped +
// stopped is what was wanted, stopped + running is unexpected, ignored
// never alerts while its state stays visible; set on a host, for a pack's
// hosts or for every host, with the same inheritance and provenance as the
// other health settings; and an issue that ends because the expectation
// changed is cleared, never "recovered".
func TestExpectedServiceState(t *testing.T) {
	e := newAlertEnv(t)
	h := e.host("svc-a")
	lab := e.host("svc-lab")
	g := e.group("lab", lab)
	ctx := context.Background()
	units := map[string]services.Unit{}
	report := func(host string, us ...services.Unit) {
		t.Helper()
		for _, u := range us {
			units[host+"|"+u.Name] = u
		}
		var list []services.Unit
		for k, u := range units {
			if strings.HasPrefix(k, host+"|") {
				list = append(list, u)
			}
		}
		e.h.mon.IngestServices(ctx, host, &services.Report{Supported: true, At: e.clk.now().UnixMilli(), Units: list}, true)
	}
	tick := func(host string, d time.Duration) {
		t.Helper()
		e.clk.add(d)
		send(t, e.h.mon, host, agentmon.KindState, stateAt(e.clk.now()))
		e.h.mon.Tick(ctx)
	}
	running := func(n, file string) services.Unit {
		return services.Unit{Name: n, Load: "loaded", Active: "active", Sub: "running", FileState: file}
	}
	stopped := func(n, file string) services.Unit {
		return services.Unit{Name: n, Load: "loaded", Active: "inactive", Sub: "dead", FileState: file}
	}
	failed := func(n string) services.Unit {
		return services.Unit{Name: n, Load: "loaded", Active: "failed", Sub: "failed", FileState: "enabled", Result: "exit-code"}
	}
	expect := func(scope, id, unit, value string) {
		t.Helper()
		body := map[string]any{"scope": scope, "check": "service_expected", "subject": "unit:" + unit, "subject_label": unit,
			"fields": map[string]any{"expected": value}}
		switch scope {
		case "host":
			body["host_id"] = id
		case "group":
			body["group_id"] = id
		}
		if value == "" {
			body["fields"] = map[string]any{}
		}
		e.mustPut(body)
	}
	row := func(host, unit string) monsvc.ServiceRow {
		t.Helper()
		var v monsvc.ServicesView
		e.c.do("GET", "/api/v1/hosts/"+host+"/monitoring/services?all=true", nil).json(t, &v)
		for _, u := range v.Units {
			if u.Name == unit {
				return u
			}
		}
		t.Fatalf("%s isn't listed on %s: %+v", unit, host, v.Units)
		return monsvc.ServiceRow{}
	}
	e.states(h, 1, func() monitor.State { return stateAt(e.clk.now()) })
	e.states(lab, 1, func() monitor.State { return stateAt(e.clk.now()) })

	// Expected running, stopped: a problem after the duration, with an alert.
	report(h, stopped("nginx.service", "enabled"))
	expect("host", h, "nginx.service", "running")
	if r := row(h, "nginx.service"); r.Expected != "running" || r.ExpectedSource == nil || r.ExpectedSource.Label != "set on this host" ||
		r.ExpectedSource.OverrideID == "" || r.Health != "problem" || !strings.Contains(r.HealthNote, "Not running, though expected to run") || !r.Watched {
		t.Fatalf("running + stopped: %+v", r)
	}
	tick(h, 3*time.Minute)
	is := e.issuesOf(h, "active")["service_stopped|nginx.service"]
	if is.ID == "" || is.Severity != "critical" || !strings.Contains(is.Detail, "Expected to run (set on this host)") {
		t.Fatalf("no issue for an expected-running service that stopped: %+v", e.issuesOf(h, "active"))
	}
	if len(e.list("view=active&host_id="+h)) != 1 {
		t.Fatal("no alert")
	}
	// It runs again: recovered.
	report(h, running("nginx.service", "enabled"))
	if r := e.issuesOf(h, "resolved")["service_stopped|nginx.service"]; r.ID == "" || r.Resolution != "" && r.Resolution != "recovered" {
		t.Fatalf("running again isn't a recovery: %+v", r)
	}

	// Expected stopped, stopped (or failed): what was wanted, no issue.
	report(h, stopped("backup.service", "enabled"), failed("migrate.service"))
	expect("host", h, "backup.service", "stopped")
	expect("host", h, "migrate.service", "stopped")
	tick(h, 5*time.Minute)
	act := e.issuesOf(h, "active")
	if act["service_failed|migrate.service"].ID != "" || act["service_stopped|backup.service"].ID != "" {
		t.Fatalf("an expected-stopped service raised an issue: %+v", act)
	}
	if r := row(h, "backup.service"); r.Health != "expected" || r.HealthNote != "Not alerting: expected to be stopped (set on this host)." {
		t.Fatalf("stopped + stopped: %+v", r)
	}
	if r := row(h, "migrate.service"); r.State != "failed" || r.Health != "expected" {
		t.Fatalf("expected stopped, failed: %+v", r)
	}

	// Expected stopped, running: unexpected, after the duration.
	report(h, running("telnet.service", "disabled"))
	expect("host", h, "telnet.service", "stopped")
	tick(h, time.Minute)
	if e.issuesOf(h, "active")["service_unexpected|telnet.service"].ID != "" {
		t.Fatal("unexpected before the 2-minute duration")
	}
	tick(h, 2*time.Minute)
	un := e.issuesOf(h, "active")["service_unexpected|telnet.service"]
	if un.ID == "" || un.Severity != "warning" || !strings.Contains(un.Detail, "Expected to be stopped (set on this host)") {
		t.Fatalf("stopped + running: %+v", e.issuesOf(h, "active"))
	}
	// Changing the expectation clears it: a configuration change, not a recovery.
	expect("host", h, "telnet.service", "")
	if r := e.issuesOf(h, "resolved")["service_unexpected|telnet.service"]; r.Resolution != "reconfigured" || !strings.Contains(r.ResolutionNote, "not a recovery") {
		t.Fatalf("expectation removed: %+v", r)
	}

	// Ignored: the state is still collected and shown, never an issue.
	report(h, failed("ModemManager.service"))
	tick(h, 3*time.Minute)
	if e.issuesOf(h, "active")["service_failed|ModemManager.service"].ID == "" {
		t.Fatal("automatic: a failed unit is a problem")
	}
	expect("global", "", "ModemManager.service", "ignored")
	if r := e.issuesOf(h, "resolved")["service_failed|ModemManager.service"]; r.Resolution != "reconfigured" || !strings.Contains(r.ResolutionNote, "ignored (set for every host)") {
		t.Fatalf("ignored: %+v", r)
	}
	report(h, failed("ModemManager.service"))
	tick(h, 5*time.Minute)
	if e.issuesOf(h, "active")["service_failed|ModemManager.service"].ID != "" {
		t.Fatal("an ignored service raised an issue again")
	}
	if r := row(h, "ModemManager.service"); r.State != "failed" || r.Health != "ignored" || r.Expected != "ignored" ||
		r.ExpectedSource.Kind != "global" || !strings.Contains(r.HealthNote, "Ignored (set for every host)") {
		t.Fatalf("ignored, still visible: %+v", r)
	}

	// From a pack, and a host setting beats it.
	report(lab, stopped("nfs-server.service", "enabled"))
	expect("group", g, "nfs-server.service", "running")
	if r := row(lab, "nfs-server.service"); r.Expected != "running" || r.ExpectedSource.Kind != "group" || r.ExpectedSource.Label != "from pack lab" ||
		r.ExpectedSource.OverrideID != "" {
		t.Fatalf("from a pack: %+v", r)
	}
	tick(lab, 3*time.Minute)
	if e.issuesOf(lab, "active")["service_stopped|nfs-server.service"].ID == "" {
		t.Fatal("the pack's expectation isn't applied")
	}
	if r := row(h, "nginx.service"); r.ExpectedSource.Kind == "group" {
		t.Fatal("a pack's setting reached a host outside it")
	}
	expect("host", lab, "nfs-server.service", "stopped")
	if r := e.issuesOf(lab, "resolved")["service_stopped|nfs-server.service"]; r.Resolution != "reconfigured" {
		t.Fatalf("host beats pack: %+v", r)
	}
	if r := row(lab, "nfs-server.service"); r.ExpectedSource.Label != "set on this host" || r.Health != "expected" {
		t.Fatalf("host beats pack: %+v", r)
	}
	// Expected running from a pack, on a host without the unit: not installed.
	expect("group", g, "smbd.service", "running")
	tick(lab, time.Minute)
	if i := e.issuesOf(lab, "active")["service_stopped|smbd.service"]; i.ID == "" || !strings.Contains(i.Title, "is not installed") {
		t.Fatalf("not installed: %+v", e.issuesOf(lab, "active"))
	}

	// Who may: an operator sets running or stopped on a host (as watching
	// was), but ignoring turns checks off, and packs and every host are
	// administrators' (monitoring.settings).
	e.h.mkUser("op", auth.RoleOperator)
	op := e.h.login("op")
	put := func(c *client, body map[string]any) int { return e.put(c, body).status }
	if s := put(op, map[string]any{"scope": "host", "host_id": h, "check": "service_expected", "subject": "unit:cron.service", "fields": map[string]any{"expected": "stopped"}}); s != 200 {
		t.Fatalf("operator, host, stopped: %d", s)
	}
	if s := put(op, map[string]any{"scope": "host", "host_id": h, "check": "service_expected", "subject": "unit:cron.service", "fields": map[string]any{"expected": "ignored"}}); s != 403 {
		t.Fatalf("operator, host, ignored: %d", s)
	}
	if s := put(op, map[string]any{"scope": "group", "group_id": g, "check": "service_expected", "subject": "unit:cron.service", "fields": map[string]any{"expected": "running"}}); s != 403 {
		t.Fatalf("operator, pack: %d", s)
	}
	// Shapes.
	for _, bad := range []map[string]any{
		{"scope": "host", "host_id": h, "check": "service_expected", "fields": map[string]any{"expected": "running"}},
		{"scope": "host", "host_id": h, "check": "service_expected", "subject": "unit:x.service", "fields": map[string]any{"expected": "sometimes"}},
		{"scope": "group", "group_id": g, "check": "filesystem", "subject": "mount:/srv", "fields": map[string]any{"warning": 80}},
	} {
		if s := put(e.c, bad); s != 422 && s != 400 {
			t.Fatalf("%v: %d", bad, s)
		}
	}

	// The watch list of older clients reads and writes "expected running"
	// on the host.
	var ms struct {
		Settings monsvc.HostSettings `json:"settings"`
	}
	e.c.do("GET", "/api/v1/hosts/"+h+"/monitoring", nil).json(t, &ms)
	if strings.Join(ms.Settings.WatchedServices, ",") != "nginx.service" {
		t.Fatalf("watched_services: %v", ms.Settings.WatchedServices)
	}
	set := ms.Settings
	set.WatchedServices = []string{"sshd"}
	if r := e.c.do("PUT", "/api/v1/hosts/"+h+"/monitoring/settings", set); r.status != 200 {
		t.Fatalf("put settings: %d %s", r.status, r.body)
	}
	report(h, running("sshd.service", "enabled"))
	if r := row(h, "sshd.service"); r.Expected != "running" || r.ExpectedSource.Label != "set on this host" {
		t.Fatalf("watched → expected running: %+v", r)
	}
	if r := row(h, "nginx.service"); r.Expected != "" {
		t.Fatalf("taken off the watch list → automatic: %+v", r)
	}
	assertAuditLike(t, e.h, "monitoring.health_override.create", `"check":"service_expected"`)
}
