package api

import (
	"context"
	"sort"
	"strings"
	"testing"
	"time"

	"drool/internal/auth"
	"drool/internal/monitor"
	"drool/internal/monitor/agentmon"
	"drool/internal/monitor/monsvc"
	"drool/internal/monitor/services"
)

// Monitoring a service is opt-in (dogfooding correction batch): a unit with
// no expected state is inventory only, its failures shown and never a health
// issue or alert; ticking Monitor means expected running; unticking a
// service a pack or every-host setting monitors makes a host exception
// instead of revealing the inherited setting; and SSH and Shepherd reports
// are judged alike.
func TestServiceMonitoringOptIn(t *testing.T) {
	e := newAlertEnv(t)
	ssh := e.host("svc-ssh")
	ag := e.host("svc-agent")
	lab := e.host("svc-lab")
	g := e.group("lab", lab)
	ctx := context.Background()
	units := map[string]map[string]services.Unit{}
	// report sends a host's whole unit list: over SSH for ssh and lab, from
	// the Shepherd (its telemetry) for ag.
	report := func(host string, us ...services.Unit) {
		t.Helper()
		if units[host] == nil {
			units[host] = map[string]services.Unit{}
		}
		for _, u := range us {
			units[host][u.Name] = u
		}
		var list []services.Unit
		for _, u := range units[host] {
			list = append(list, u)
		}
		sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
		r := &services.Report{Supported: true, At: e.clk.now().UnixMilli(), Units: list}
		if host == ag {
			send(t, e.h.mon, host, agentmon.KindServices, r)
			return
		}
		if err := e.h.mon.IngestServices(ctx, host, r, true); err != nil {
			t.Fatal(err)
		}
	}
	tick := func(d time.Duration) {
		t.Helper()
		e.clk.add(d)
		for _, h := range []string{ssh, ag, lab} {
			send(t, e.h.mon, h, agentmon.KindState, stateAt(e.clk.now()))
		}
		e.h.mon.Tick(ctx)
	}
	running := func(n string) services.Unit {
		return services.Unit{Name: n, Load: "loaded", Active: "active", Sub: "running", FileState: "enabled"}
	}
	stopped := func(n string) services.Unit {
		return services.Unit{Name: n, Load: "loaded", Active: "inactive", Sub: "dead", FileState: "enabled"}
	}
	failed := func(n string) services.Unit {
		return services.Unit{Name: n, Load: "loaded", Active: "failed", Sub: "failed", FileState: "enabled", Result: "exit-code"}
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
		t.Fatalf("%s isn't listed on %s", unit, host)
		return monsvc.ServiceRow{}
	}
	type monitorResp struct {
		Changed  bool               `json:"changed"`
		Override *monsvc.Override   `json:"override"`
		Unit     *monsvc.ServiceRow `json:"unit"`
	}
	monitorAs := func(c *client, host, unit string, on bool) (int, monitorResp) {
		t.Helper()
		r := c.do("PUT", "/api/v1/hosts/"+host+"/monitoring/services/"+unit+"/monitor", map[string]any{"monitor": on})
		var m monitorResp
		if r.status == 200 {
			r.json(t, &m)
		}
		return r.status, m
	}
	mon := func(host, unit string, on bool) monitorResp {
		t.Helper()
		s, m := monitorAs(e.c, host, unit, on)
		if s != 200 {
			t.Fatalf("monitor %s on %s = %v: %d", unit, host, on, s)
		}
		return m
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
	active := func(host string) []string {
		var out []string
		for k := range e.issuesOf(host, "active") {
			out = append(out, k)
		}
		sort.Strings(out)
		return out
	}
	tile := func(host string) monsvc.Tile {
		t.Helper()
		var o monsvc.HostOverview
		e.c.do("GET", "/api/v1/hosts/"+host+"/monitoring", nil).json(t, &o)
		for _, x := range o.Tiles {
			if x.Key == "services" {
				return x
			}
		}
		t.Fatalf("no services tile: %+v", o.Tiles)
		return monsvc.Tile{}
	}
	fleet := func(host string) monsvc.FleetHost {
		t.Helper()
		var f monsvc.FleetSummary
		e.c.do("GET", "/api/v1/monitoring/summary", nil).json(t, &f)
		for _, r := range f.Rows {
			if r.HostID == host {
				return r
			}
		}
		t.Fatalf("%s not in the fleet view", host)
		return monsvc.FleetHost{}
	}
	for _, h := range []string{ssh, ag, lab} {
		e.states(h, 1, func() monitor.State { return stateAt(e.clk.now()) })
	}

	// Automatic: failed and stopped units nobody monitors are inventory
	// only, however long they stay that way, on both transports.
	for _, h := range []string{ssh, ag} {
		report(h, failed("rtl433.service"), failed("smartmontools.service"), stopped("varnish.service"), running("sshd.service"))
	}
	tick(10 * time.Minute)
	tick(10 * time.Minute)
	for _, h := range []string{ssh, ag} {
		if a := active(h); len(a) != 0 {
			t.Fatalf("automatic units raised issues on %s: %v", h, a)
		}
		if n := len(e.list("view=open&host_id=" + h)); n != 0 {
			t.Fatalf("automatic units raised %d alerts on %s", n, h)
		}
		r := row(h, "rtl433.service")
		if r.State != "failed" || r.Monitored || r.Expected != "" || r.Health != "unmonitored" || !strings.Contains(r.HealthNote, "not monitored") || r.Customized {
			t.Fatalf("a failed unit nobody monitors, on %s: %+v", h, r)
		}
		if r := row(h, "varnish.service"); r.Monitored || r.Health != "unmonitored" || r.HealthNote != "" {
			t.Fatalf("a stopped unit nobody monitors, on %s: %+v", h, r)
		}
		// The facts stay (2 failed), but nothing is unhealthy.
		if tl := tile(h); tl.Status != "info" || tl.Text != "None monitored" || !strings.Contains(tl.Detail, "2 failed, not monitored") {
			t.Fatalf("services tile with nothing monitored, on %s: %+v", h, tl)
		}
		if f := fleet(h); f.Failed != 2 || f.ServiceIssues != 0 || f.Worst != "ok" || contains(f.Categories, "services") || contains(f.Categories, "unhealthy") {
			t.Fatalf("fleet row with unmonitored failures, on %s: %+v", h, f)
		}
	}

	// Ticking Monitor means expected running, set on this host: a failed
	// unit is then a critical problem, the same on both transports.
	for _, h := range []string{ssh, ag} {
		m := mon(h, "rtl433.service", true)
		if !m.Changed || m.Unit == nil || !m.Unit.Monitored || m.Unit.Expected != "running" || m.Unit.ExpectedSource == nil ||
			m.Unit.ExpectedSource.Kind != "subject" || m.Unit.ExpectedSource.OverrideID == "" || m.Unit.Customized || m.Unit.Inherited != "" {
			t.Fatalf("ticked on %s: %+v", h, m)
		}
		if m := mon(h, "rtl433.service", true); m.Changed {
			t.Fatalf("ticking a monitored unit again changed something on %s", h)
		}
	}
	tick(2 * time.Minute)
	for _, h := range []string{ssh, ag} {
		i := e.issuesOf(h, "active")["service_failed|rtl433.service"]
		if i.ID == "" || i.Severity != "critical" || !strings.Contains(i.Detail, "expected to run (set on this host)") {
			t.Fatalf("monitored and failed, on %s: %+v", h, e.issuesOf(h, "active"))
		}
		if a := active(h); len(a) != 1 {
			t.Fatalf("only the monitored unit is a problem on %s: %v", h, a)
		}
		if tl := tile(h); tl.Status != "critical" || tl.Text != "0/1 monitored as expected" || !strings.Contains(tl.Detail, "1 failed, not monitored") {
			t.Fatalf("services tile with a monitored failure, on %s: %+v", h, tl)
		}
		if f := fleet(h); f.ServiceIssues != 1 || !contains(f.Categories, "services") || !contains(f.Categories, "unhealthy") {
			t.Fatalf("fleet row with a monitored failure, on %s: %+v", h, f)
		}
	}
	// Monitored and stopping: a problem too. Running again: recovered.
	for _, h := range []string{ssh, ag} {
		report(h, running("rtl433.service"), stopped("sshd.service"))
		mon(h, "sshd.service", true)
	}
	tick(3 * time.Minute)
	for _, h := range []string{ssh, ag} {
		if r := e.issuesOf(h, "resolved")["service_failed|rtl433.service"]; r.ID == "" || r.Resolution != "recovered" {
			t.Fatalf("running again is a recovery, on %s: %+v", h, r)
		}
		if i := e.issuesOf(h, "active")["service_stopped|sshd.service"]; i.ID == "" || i.Severity != "critical" {
			t.Fatalf("monitored and stopped, on %s: %+v", h, e.issuesOf(h, "active"))
		}
	}
	// Unticking takes the host's own setting away: the issue is cleared by
	// configuration, never recovered, and the alert says so.
	for _, h := range []string{ssh, ag} {
		m := mon(h, "sshd.service", false)
		if !m.Changed || m.Override != nil || m.Unit.Monitored || m.Unit.Expected != "" || m.Unit.ExpectedSource != nil {
			t.Fatalf("unticked on %s: %+v", h, m)
		}
		r := e.issuesOf(h, "resolved")["service_stopped|sshd.service"]
		if r.Resolution != "reconfigured" || !strings.Contains(r.ResolutionNote, "not a recovery") {
			t.Fatalf("unticking clears the issue as a configuration change, on %s: %+v", h, r)
		}
		var al []string
		for _, a := range e.list("view=resolved&host_id=" + h) {
			if a.Subject == "sshd.service" {
				al = append(al, a.Resolution)
			}
		}
		if len(al) != 1 || al[0] != "reconfigured" {
			t.Fatalf("the alert closed as %v on %s", al, h)
		}
	}
	// Expected stopped (the advanced setting): running is the problem.
	for _, h := range []string{ssh, ag} {
		report(h, running("telnet.service"))
		expect("host", h, "telnet.service", "stopped")
	}
	tick(3 * time.Minute)
	for _, h := range []string{ssh, ag} {
		if i := e.issuesOf(h, "active")["service_unexpected|telnet.service"]; i.ID == "" || i.Severity != "warning" {
			t.Fatalf("expected stopped, running, on %s: %+v", h, e.issuesOf(h, "active"))
		}
		if r := row(h, "telnet.service"); !r.Monitored || r.Expected != "stopped" {
			t.Fatalf("expected stopped counts as monitored, on %s: %+v", h, r)
		}
		// Unticking an expected-stopped unit the host set: its own setting goes.
		if m := mon(h, "telnet.service", false); !m.Changed || m.Override != nil || m.Unit.Monitored {
			t.Fatalf("unticked an expected-stopped unit on %s: %+v", h, m)
		}
		if r := e.issuesOf(h, "resolved")["service_unexpected|telnet.service"]; r.Resolution != "reconfigured" {
			t.Fatalf("on %s: %+v", h, r)
		}
	}
	// Parity: both transports reached the same issues, the same way.
	for _, st := range []string{"active", "resolved"} {
		a, b := e.issuesOf(ssh, st), e.issuesOf(ag, st)
		if len(a) != len(b) {
			t.Fatalf("%s issues differ between SSH and the Shepherd: %v vs %v", st, a, b)
		}
		for k, x := range a {
			y, ok := b[k]
			if !ok || x.Severity != y.Severity || x.Resolution != y.Resolution || x.Title != y.Title {
				t.Fatalf("%s %s differs between SSH and the Shepherd: %+v vs %+v", st, k, x, y)
			}
		}
	}

	// Inherited from a pack: ticked from there, provenance shown, nothing
	// "customized" about it.
	report(lab, failed("nfs-server.service"), running("smbd.service"))
	expect("group", g, "nfs-server.service", "running")
	tick(2 * time.Minute)
	if i := e.issuesOf(lab, "active")["service_failed|nfs-server.service"]; i.ID == "" || !strings.Contains(i.Detail, "from pack lab") {
		t.Fatalf("a pack's expectation: %+v", e.issuesOf(lab, "active"))
	}
	r := row(lab, "nfs-server.service")
	if !r.Monitored || r.Expected != "running" || r.ExpectedSource.Kind != "group" || r.ExpectedSource.OverrideID != "" ||
		r.Inherited != "running" || r.InheritedSource != "from pack lab" || r.Customized {
		t.Fatalf("monitored from a pack: %+v", r)
	}
	// Unticking it on one host is an exception to the pack: weakening it is
	// an administrator's, so an operator is refused and nothing changes.
	e.h.mkUser("op", auth.RoleOperator)
	op := e.h.login("op")
	if s, _ := monitorAs(op, lab, "nfs-server.service", false); s != 403 {
		t.Fatalf("an operator unticked a service a pack monitors: %d", s)
	}
	if row(lab, "nfs-server.service").Expected != "running" {
		t.Fatal("a refused untick changed something")
	}
	m := mon(lab, "nfs-server.service", false)
	if !m.Changed || m.Override == nil || m.Override.Fields[monsvc.FieldExpected] != monsvc.ExpectIgnored || m.Unit.Monitored ||
		m.Unit.Expected != "ignored" || m.Unit.Inherited != "running" || m.Unit.ExpectedSource.Kind != "subject" {
		t.Fatalf("unticked a service a pack monitors: %+v", m)
	}
	if r := e.issuesOf(lab, "resolved")["service_failed|nfs-server.service"]; r.Resolution != "reconfigured" || !strings.Contains(r.ResolutionNote, "ignored (set on this host)") {
		t.Fatalf("the exception clears the issue: %+v", r)
	}
	tick(5 * time.Minute)
	if i := e.issuesOf(lab, "active")["service_failed|nfs-server.service"]; i.ID != "" {
		t.Fatal("the host's exception didn't hold: the pack's setting came back")
	}
	// Ticking it again removes the exception (the pack's setting applies),
	// rather than adding a host setting of its own; an operator may.
	if s, m := monitorAs(op, lab, "nfs-server.service", true); s != 200 || !m.Changed || m.Override != nil || !m.Unit.Monitored ||
		m.Unit.ExpectedSource.Kind != "group" {
		t.Fatalf("ticked again: %d %+v", s, m)
	}
	tick(2 * time.Minute)
	if i := e.issuesOf(lab, "active")["service_failed|nfs-server.service"]; i.ID == "" {
		t.Fatal("monitored from the pack again, but no issue")
	}
	// Every host: the same, below a pack and a host.
	expect("global", "", "smbd.service", "stopped")
	tick(3 * time.Minute)
	if i := e.issuesOf(lab, "active")["service_unexpected|smbd.service"]; i.ID == "" || !strings.Contains(i.Detail, "set for every host") {
		t.Fatalf("every host's expectation: %+v", e.issuesOf(lab, "active"))
	}
	if r := row(lab, "smbd.service"); r.Inherited != "stopped" || r.InheritedSource != "set for every host" || !r.Monitored {
		t.Fatalf("monitored for every host: %+v", r)
	}
	if m := mon(lab, "smbd.service", false); m.Override == nil || m.Unit.Expected != "ignored" {
		t.Fatalf("unticked a service every host monitors: %+v", m)
	}
	if r := e.issuesOf(lab, "resolved")["service_unexpected|smbd.service"]; r.Resolution != "reconfigured" {
		t.Fatalf("%+v", r)
	}
	// A host's own running beats a pack's stopped: worth calling out, and
	// the row says what the host inherits.
	report(lab, running("cups.service"))
	expect("group", g, "cups.service", "stopped")
	expect("host", lab, "cups.service", "running")
	if r := row(lab, "cups.service"); r.Expected != "running" || r.Inherited != "stopped" || r.ExpectedSource.Kind != "subject" || r.Health != "ok" {
		t.Fatalf("a host's running beats a pack's stopped: %+v", r)
	}
	// Changing a unit's health check (here its duration) is what
	// "customized" means.
	e.mustPut(map[string]any{"scope": "host", "host_id": lab, "check": "service_failed", "subject": "unit:nfs-server.service", "fields": map[string]any{"duration_min": 5}})
	if r := row(lab, "nfs-server.service"); !r.Customized {
		t.Fatalf("a custom duration isn't shown as customized: %+v", r)
	}

	// The old watched_services list is the host's own expected running (the
	// whole list: what it leaves out loses that setting).
	if r := e.c.do("PUT", "/api/v1/hosts/"+ssh+"/monitoring/settings", map[string]any{"watched_services": []string{"redis", "rtl433.service"}, "probe_mounts": []string{}}); r.status != 200 {
		t.Fatalf("watched services: %d %s", r.status, r.body)
	}
	report(ssh, failed("redis.service"))
	if r := row(ssh, "redis.service"); !r.Monitored || r.Expected != "running" || r.ExpectedSource.Kind != "subject" {
		t.Fatalf("a watched service is monitored, expected running: %+v", r)
	}
	mon(ssh, "redis.service", false)
	if contains(e.h.mon.HostSettings(ctx, ssh).WatchedServices, "redis.service") {
		t.Fatal("unticked, but still in watched_services")
	}
	if !contains(e.h.mon.HostSettings(ctx, ssh).WatchedServices, "rtl433.service") {
		t.Fatalf("a ticked service is in watched_services: %+v", e.h.mon.HostSettings(ctx, ssh))
	}

	// Validation.
	if s, _ := monitorAs(e.c, ssh, "bad name", true); s != 422 {
		t.Fatalf("a bad unit name: %d", s)
	}
	if r := e.c.do("PUT", "/api/v1/hosts/"+ssh+"/monitoring/services/x.service/monitor", map[string]any{}); r.status != 422 {
		t.Fatalf("no monitor value: %d", r.status)
	}
	if r := e.c.do("PUT", "/api/v1/hosts/nope/monitoring/services/x.service/monitor", map[string]any{"monitor": true}); r.status != 404 {
		t.Fatalf("unknown host: %d", r.status)
	}
	e.h.mkUser("aud", auth.RoleAuditor)
	if s, _ := monitorAs(e.h.login("aud"), ssh, "x.service", true); s != 403 {
		t.Fatalf("an auditor ticked Monitor: %d", s)
	}
}

// Issues that only existed because the old automatic rule judged any
// failed unit a problem settle after the upgrade as cleared by
// configuration, never as recoveries, even on a host that no longer
// reports; explicit expectations stay as they were.
func TestServiceIssuesSettleAfterUpgrade(t *testing.T) {
	e := newAlertEnv(t)
	ctx := context.Background()
	h := e.host("old-svc")
	quiet := e.host("old-quiet")
	for _, x := range []string{h, quiet} {
		e.states(x, 1, func() monitor.State { return stateAt(e.clk.now()) })
		if err := e.h.mon.IngestServices(ctx, x, &services.Report{Supported: true, At: e.clk.now().UnixMilli(), Units: []services.Unit{
			{Name: "rtl433.service", Load: "loaded", Active: "failed", Sub: "failed", FileState: "enabled", Result: "exit-code"},
			{Name: "postgresql.service", Load: "loaded", Active: "failed", Sub: "failed", FileState: "enabled", Result: "exit-code"},
		}}, true); err != nil {
			t.Fatal(err)
		}
		e.mustPut(map[string]any{"scope": "host", "host_id": x, "check": "service_expected", "subject": "unit:postgresql.service", "fields": map[string]any{"expected": "running"}})
	}
	e.clk.add(2 * time.Minute)
	e.h.mon.Tick(ctx)
	// What the previous version left: an alert for a failed unit nobody
	// monitors (opened through the normal path, as it did then).
	for _, x := range []string{h, quiet} {
		e.issue(x, "service_failed", "rtl433.service", "warning", "rtl433.service failed")
		if i := e.issuesOf(x, "active")["service_failed|postgresql.service"]; i.ID == "" || i.Severity != "critical" {
			t.Fatalf("the monitored unit's issue: %+v", e.issuesOf(x, "active"))
		}
	}
	// One host stopped reporting before the upgrade: its units are unknown.
	if _, err := e.h.st.Writer.Exec(`UPDATE mon_services SET known = 0 WHERE host_id = ?`, quiet); err != nil {
		t.Fatal(err)
	}
	// The controller restarts on the new version.
	e.h.mon.Load(ctx)
	for _, x := range []string{h, quiet} {
		r := e.issuesOf(x, "resolved")["service_failed|rtl433.service"]
		if r.ID == "" || r.Resolution != "reconfigured" || !strings.Contains(r.ResolutionNote, "isn't monitored") || !strings.Contains(r.ResolutionNote, "not a recovery") {
			t.Fatalf("an old automatic failure on %s: %+v", x, r)
		}
		var a = e.only("view=resolved&host_id=" + x)
		if a.Subject != "rtl433.service" || a.Resolution != "reconfigured" {
			t.Fatalf("the alert on %s: %+v", x, a)
		}
		tl := timelineKinds(e.detail(a.ID))
		if !contains(tl, "cleared") || contains(tl, "recovered") {
			t.Fatalf("the alert's timeline on %s says %v", x, tl)
		}
		// The explicit expectation is untouched, its issue too.
		if i := e.issuesOf(x, "active")["service_failed|postgresql.service"]; i.ID == "" || i.Severity != "critical" {
			t.Fatalf("an explicit expectation's issue on %s: %+v", x, e.issuesOf(x, "active"))
		}
	}
	// The quiet host's monitored issue stays as it is while nothing is
	// known: no fake recovery from an evaluation without data.
	e.h.mon.Load(ctx)
	if i := e.issuesOf(quiet, "active")["service_failed|postgresql.service"]; i.ID == "" {
		t.Fatal("a monitored unit's issue ended while its state was unknown")
	}
}

// A pass that began before a configuration change (a tick holding the
// rules it loaded at its start) runs after the change re-judged the host:
// it judges with the configuration now, so it neither closes the issue
// monitoring a failed service just opened, nor re-opens the one ignoring
// it just closed. (Under the old rules the issue flapped: closed as "isn't
// monitored", then opened again as a new alert.)
func TestServicePassAfterAChange(t *testing.T) {
	e := newAlertEnv(t)
	h := e.host("svc-race")
	ctx := context.Background()
	e.states(h, 1, func() monitor.State { return stateAt(e.clk.now()) })
	r := &services.Report{Supported: true, At: e.clk.now().UnixMilli(), Units: []services.Unit{
		{Name: "backup.service", Load: "loaded", Active: "failed", Sub: "failed", FileState: "static", Result: "exit-code"}}}
	if err := e.h.mon.IngestServices(ctx, h, r, true); err != nil {
		t.Fatal(err)
	}
	put := func(check string, fields map[string]any) {
		t.Helper()
		e.mustPut(map[string]any{"scope": "host", "host_id": h, "check": check, "subject": "unit:backup.service", "subject_label": "backup.service", "fields": fields})
	}
	key := "service_failed|backup.service"
	before := e.h.mon.PolicyForTest(ctx)
	put("service_failed", map[string]any{"duration_min": 0})
	put("service_expected", map[string]any{"expected": "running"})
	e.h.mon.PolicyForTest(ctx) // the change re-judges the host
	opened, ok := e.issuesOf(h, "active")[key]
	if !ok {
		t.Fatalf("monitoring a failed service opened no issue: %v", e.issuesOf(h, "active"))
	}
	e.h.mon.ServicePassForTest(ctx, h, before)
	if now, ok := e.issuesOf(h, "active")[key]; !ok || now.ID != opened.ID {
		t.Fatalf("a pass with the rules from before the change closed the new issue: %+v, resolved %+v", now, e.issuesOf(h, "resolved")[key])
	}

	// Ignored: the issue closes as a configuration change and stays closed.
	monitored := e.h.mon.PolicyForTest(ctx)
	put("service_expected", map[string]any{"expected": "ignored"})
	e.h.mon.PolicyForTest(ctx)
	if res := e.issuesOf(h, "resolved")[key]; res.ID != opened.ID || res.Resolution != "reconfigured" {
		t.Fatalf("ignoring it: %+v", res)
	}
	e.h.mon.ServicePassForTest(ctx, h, monitored)
	if a, ok := e.issuesOf(h, "active")[key]; ok {
		t.Fatalf("a pass with the rules from before ignoring it re-opened the issue: %+v", a)
	}
}
