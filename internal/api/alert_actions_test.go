package api

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"drool/internal/auth"
	"drool/internal/monitor"
	"drool/internal/monitor/agentmon"
	"drool/internal/monitor/monsvc"
	"drool/internal/monitor/services"
)

type ignoreOptions struct {
	AlertID string `json:"alert_id"`
	Options []struct {
		ID      string                `json:"id"`
		Label   string                `json:"label"`
		Effect  string                `json:"effect"`
		Reset   string                `json:"reset"`
		Change  monsvc.OverrideChange `json:"change"`
		Allowed bool                  `json:"allowed"`
		Needs   string                `json:"needs"`
	} `json:"options"`
	Closes string `json:"closes"`
	Note   string `json:"note"`
}

// The Alerts page's Ignore… (dogfooding correction batch): the health
// settings that would quiet an alert's condition for good, at the narrowest
// scope, with who may apply each; applying one is an ordinary override
// write (permissions, audit), and the alert closes as cleared, never as a
// recovery.
func TestAlertIgnoreOptions(t *testing.T) {
	e := newAlertEnv(t)
	ctx := context.Background()
	h := e.host("nas01")
	lab := e.host("lab01")
	g := e.group("lab", lab)
	e.h.mkUser("op", auth.RoleOperator)
	e.h.mkUser("vera", auth.RoleViewer)
	op, vera := e.h.login("op"), e.h.login("vera")
	options := func(c *client, alertID string) ignoreOptions {
		t.Helper()
		var o ignoreOptions
		r := c.do("GET", "/api/v1/alerts/"+alertID+"/ignore-options", nil)
		if r.status != 200 {
			t.Fatalf("ignore options: %d %s", r.status, r.body)
		}
		r.json(t, &o)
		return o
	}
	byID := func(o ignoreOptions) map[string]int {
		m := map[string]int{}
		for i, x := range o.Options {
			m[x.ID] = i
		}
		return m
	}
	apply := func(c *client, o ignoreOptions, id string) int {
		t.Helper()
		i, ok := byID(o)[id]
		if !ok {
			t.Fatalf("no option %s: %+v", id, o.Options)
		}
		return c.do("PUT", "/api/v1/monitoring/health/overrides", o.Options[i].Change).status
	}
	tick := func(host string, d time.Duration) {
		t.Helper()
		e.clk.add(d)
		send(t, e.h.mon, host, agentmon.KindState, stateAt(e.clk.now()))
		e.h.mon.Tick(ctx)
	}
	unit := func(n, active, sub string) services.Unit {
		return services.Unit{Name: n, Load: "loaded", Active: active, Sub: sub, FileState: "enabled", Result: "exit-code"}
	}
	alertFor := func(host, check, subject string) string {
		t.Helper()
		for _, a := range e.list("view=open&host_id=" + host) {
			if a.Check == check && a.Subject == subject {
				return a.ID
			}
		}
		t.Fatalf("no open %s alert for %s on %s: %+v", check, subject, host, e.list("view=open&host_id="+host))
		return ""
	}
	for _, x := range []string{h, lab} {
		e.states(x, 1, func() monitor.State { return stateAt(e.clk.now()) })
	}

	// A monitored service that failed: ignore it here, stop monitoring it
	// (this host's own setting is all that monitors it), or expect it
	// stopped; never guessed.
	e.h.mon.IngestServices(ctx, h, &services.Report{Supported: true, At: e.clk.now().UnixMilli(), Units: []services.Unit{
		unit("backup.service", "failed", "failed"), unit("telnet.service", "active", "running")}}, true)
	e.mustPut(map[string]any{"scope": "host", "host_id": h, "check": "service_expected", "subject": "unit:backup.service", "fields": map[string]any{"expected": "running"}})
	e.mustPut(map[string]any{"scope": "host", "host_id": h, "check": "service_expected", "subject": "unit:telnet.service", "fields": map[string]any{"expected": "stopped"}})
	tick(h, 3*time.Minute)
	backup := alertFor(h, "service_failed", "backup.service")
	o := options(e.c, backup)
	if ids := byID(o); len(ids) != 3 || !strings.Contains(o.Closes, "cleared by configuration, not as a recovery") {
		t.Fatalf("a monitored failed service: %+v", o)
	}
	ig := o.Options[byID(o)["ignore_service"]]
	if ig.Label != "Ignore backup.service on nas01" || ig.Change.Scope != "host" || ig.Change.HostID != h || ig.Change.Check != "service_expected" ||
		ig.Change.Subject != "unit:backup.service" || ig.Change.Fields[monsvc.FieldExpected] != monsvc.ExpectIgnored || !ig.Allowed ||
		!strings.Contains(ig.Reset, "Customize… → Reset to inherited") {
		t.Fatalf("ignore the service: %+v", ig)
	}
	if es := o.Options[byID(o)["expect_stopped"]]; es.Change.Fields[monsvc.FieldExpected] != monsvc.ExpectStopped || es.Label != "Expect backup.service to be stopped on nas01" {
		t.Fatalf("expect it stopped: %+v", es)
	}
	if sm := o.Options[byID(o)["stop_monitoring"]]; len(sm.Change.Fields) != 0 || !sm.Allowed {
		t.Fatalf("stop monitoring: %+v", sm)
	}
	// Who may: ignoring stops detection (administrators); taking away the
	// host's own expectation or expecting another state is an operator's,
	// as ticking Monitor is; a viewer may do none.
	po := options(op, backup)
	if x := po.Options[byID(po)["ignore_service"]]; x.Allowed || x.Needs != "monitoring.settings" {
		t.Fatalf("operator, ignore: %+v", x)
	}
	if x := po.Options[byID(po)["expect_stopped"]]; !x.Allowed {
		t.Fatalf("operator, expect stopped: %+v", x)
	}
	for _, x := range options(vera, backup).Options {
		if x.Allowed || x.Needs != "monitoring.manage" {
			t.Fatalf("viewer: %+v", x)
		}
	}
	if s := apply(op, po, "ignore_service"); s != 403 {
		t.Fatalf("an operator ignored a service: %d", s)
	}
	if s := apply(vera, po, "expect_stopped"); s != 403 {
		t.Fatalf("a viewer changed a health setting: %d", s)
	}
	// An acknowledged alert can still be quieted for good.
	if r := op.do("POST", "/api/v1/alerts/"+backup+"/acknowledge", map[string]any{"note": "looking"}); r.status != 200 {
		t.Fatalf("ack: %d", r.status)
	}
	if o := options(e.c, backup); len(o.Options) != 3 {
		t.Fatalf("acknowledged: %+v", o)
	}
	if s := apply(e.c, o, "ignore_service"); s != 200 {
		t.Fatalf("ignore: %d", s)
	}
	closed := e.detail(backup)
	if closed.Alert.State != "resolved" || closed.Alert.Resolution != "reconfigured" || !strings.Contains(closed.Alert.ResolutionNote, "ignored (set on this host)") {
		t.Fatalf("ignored: %+v", closed.Alert)
	}
	if k := timelineKinds(closed); !slices.Contains(k, "cleared") || slices.Contains(k, "recovered") {
		t.Fatalf("timeline: %v", k)
	}
	var audits listResponse[auditJSON]
	e.c.do("GET", "/api/v1/audit?action=monitoring.health_override.update", nil).json(t, &audits)
	found := false
	for _, a := range audits.Items {
		if strings.Contains(string(a.Details), `"subject":"unit:backup.service"`) && strings.Contains(string(a.Details), `"expected":{"before":"running","after":"ignored"}`) {
			found = true
		}
	}
	if !found {
		t.Fatalf("ignoring wasn't audited: %+v", audits.Items)
	}
	// A closed alert has nothing to quiet.
	if o := options(e.c, backup); len(o.Options) != 0 || o.Note == "" {
		t.Fatalf("closed: %+v", o)
	}

	// Expected stopped, running: the intentional opposite is "expected to
	// run" (an operator's).
	tick(h, 3*time.Minute)
	telnet := alertFor(h, "service_unexpected", "telnet.service")
	o = options(op, telnet)
	if er, ok := byID(o)["expect_running"]; !ok || !o.Options[er].Allowed || o.Options[er].Change.Fields[monsvc.FieldExpected] != monsvc.ExpectRunning {
		t.Fatalf("expected stopped and running: %+v", o)
	}
	if _, ok := byID(o)["expect_stopped"]; ok {
		t.Fatal("offered the state it already expects")
	}
	if s := apply(op, o, "expect_running"); s != 200 {
		t.Fatalf("operator, expect running: %d", s)
	}
	if a := e.detail(telnet).Alert; a.Resolution != "reconfigured" {
		t.Fatalf("expected to run now: %+v", a)
	}

	// Monitored from a pack: no "stop monitoring" here (taking the host's
	// own setting away wouldn't do it); ignoring makes the exception.
	e.h.mon.IngestServices(ctx, lab, &services.Report{Supported: true, At: e.clk.now().UnixMilli(), Units: []services.Unit{unit("nfs.service", "failed", "failed")}}, true)
	e.mustPut(map[string]any{"scope": "group", "group_id": g, "check": "service_expected", "subject": "unit:nfs.service", "fields": map[string]any{"expected": "running"}})
	tick(lab, 3*time.Minute)
	nfs := alertFor(lab, "service_failed", "nfs.service")
	o = options(e.c, nfs)
	if _, ok := byID(o)["stop_monitoring"]; ok || len(o.Options) != 2 {
		t.Fatalf("monitored from a pack: %+v", o)
	}
	if s := apply(e.c, o, "ignore_service"); s != 200 {
		t.Fatalf("ignore: %d", s)
	}
	if a := e.detail(nfs).Alert; a.Resolution != "reconfigured" {
		t.Fatalf("%+v", a)
	}

	// Any other check: turned off at the narrowest scope there is. CPU has
	// no subject: this host only.
	e.cpu(h, 99, 6)
	cpu := alertFor(h, "cpu", "")
	o = options(e.c, cpu)
	if len(o.Options) != 1 || o.Options[0].ID != "disable_check" || o.Options[0].Change.Scope != "host" || o.Options[0].Change.HostID != h ||
		o.Options[0].Change.Check != "cpu" || o.Options[0].Change.Subject != "" || o.Options[0].Change.Fields[monsvc.FieldEnabled] != 0 ||
		!strings.Contains(o.Options[0].Label, "on nas01") || !strings.Contains(o.Options[0].Effect, "Other hosts and every other check keep working") {
		t.Fatalf("cpu: %+v", o)
	}
	if po := options(op, cpu); po.Options[0].Allowed || po.Options[0].Needs != "monitoring.settings" {
		t.Fatalf("operator, turn a check off: %+v", po.Options[0])
	}
	if s := apply(e.c, o, "disable_check"); s != 200 {
		t.Fatalf("turn cpu off: %d", s)
	}
	if a := e.detail(cpu).Alert; a.Resolution != "disabled" || !strings.Contains(a.ResolutionNote, "not a recovery") {
		t.Fatalf("cpu turned off: %+v", a)
	}
	// A filesystem: that filesystem only, by its identity, keeping what
	// else is set for it.
	fs := func(mount string, pct float64) monitor.FS {
		return monitor.FS{Mount: mount, Device: "/dev/x", TotalBytes: 1000, UsedBytes: int64(pct * 10), AvailBytes: 1000 - int64(pct*10), UsedPercent: pct}
	}
	e.mustPut(map[string]any{"scope": "host", "host_id": lab, "check": "filesystem", "subject": "mount:/srv", "fields": map[string]any{"critical": 99}})
	e.states(lab, 3, func() monitor.State { return stateAt(e.clk.now(), fs("/", 20), fs("/srv", 97)) })
	srv := alertFor(lab, "filesystem", "/srv")
	o = options(e.c, srv)
	if len(o.Options) != 1 || o.Options[0].Change.Subject != "mount:/srv" || o.Options[0].Change.Fields[monsvc.FieldEnabled] != 0 ||
		o.Options[0].Change.Fields[monsvc.FieldCritical] != 99 || !strings.Contains(o.Options[0].Label, "for /srv on lab01") {
		t.Fatalf("filesystem: %+v", o)
	}
	if s := apply(e.c, o, "disable_check"); s != 200 {
		t.Fatalf("turn /srv off: %d", s)
	}
	if a := e.detail(srv).Alert; a.Resolution != "disabled" {
		t.Fatalf("/srv: %+v", a)
	}
	// Only /srv: / still alerts when it fills.
	e.states(lab, 3, func() monitor.State { return stateAt(e.clk.now(), fs("/", 97), fs("/srv", 97)) })
	alertFor(lab, "filesystem", "/")

	if r := e.c.do("GET", "/api/v1/alerts/nope/ignore-options", nil); r.status != 404 {
		t.Fatalf("unknown alert: %d", r.status)
	}
}
