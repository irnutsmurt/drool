package api

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"drool/internal/alerts"
	"drool/internal/auth"
	"drool/internal/monitor"
	"drool/internal/notify"
)

// testSender trusts httptest's TLS certificate (every httptest TLS server
// uses the same one). Tests never reach real notification services.
var testSender = func() *notify.Sender {
	srv := httptest.NewTLSServer(http.NotFoundHandler())
	defer srv.Close()
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	return &notify.Sender{RootCAs: pool, HeloName: "drool.test"}
}()

type alertEnv struct {
	t   *testing.T
	h   *harness
	c   *client
	clk *monClock
	a   *alerts.Service
}

func newAlertEnv(t *testing.T) *alertEnv {
	h := newHarness(t)
	h.mkUser("admin", auth.RoleAdmin)
	clk := &monClock{t: time.Now().Add(-6 * time.Hour).Truncate(time.Minute)}
	h.mon.SetClock(clk.now)
	h.alerts.SetClock(clk.now)
	return &alertEnv{t: t, h: h, c: h.login("admin"), clk: clk, a: h.alerts}
}

func (e *alertEnv) host(name string) string {
	e.t.Helper()
	id := "host_" + strings.ReplaceAll(name, "-", "")
	if _, err := e.h.st.Writer.Exec(`INSERT INTO hosts (id, name, connect_address, ssh_user, status, created_at, updated_at) VALUES (?, ?, '10.0.0.1', 'x', 'ready', 0, 0)`,
		id, name); err != nil {
		e.t.Fatal(err)
	}
	return id
}

func (e *alertEnv) group(name string, hosts ...string) string {
	e.t.Helper()
	var g struct{ ID string }
	r := e.c.do("POST", "/api/v1/groups", map[string]any{"name": name})
	r.json(e.t, &g)
	if g.ID == "" {
		e.t.Fatalf("group: %s", r.body)
	}
	for _, h := range hosts {
		if _, err := e.h.st.Writer.Exec(`INSERT INTO group_members (group_id, host_id) VALUES (?, ?)`, g.ID, h); err != nil {
			e.t.Fatal(err)
		}
	}
	return g.ID
}

// cpu sends minutes of CPU load through M11's real health evaluation.
func (e *alertEnv) cpu(hostID string, pct float64, minutes int) {
	e.t.Helper()
	for range minutes {
		send(e.t, e.h.mon, hostID, "metrics", monitor.Batch{Points: []monitor.Point{pointAt(e.clk.now(), map[string]float64{"cpu": pct, "mem": 20})}})
		e.clk.add(time.Minute)
	}
}

func (e *alertEnv) issue(hostID, check, subject, sev, title string) {
	e.h.mon.OpenIssueForTest(context.Background(), hostID, check, subject, sev, title, nil, time.Time{})
}

func (e *alertEnv) recover(hostID, check, subject string) {
	e.h.mon.ResolveIssueForTest(context.Background(), hostID, check, subject)
}

func (e *alertEnv) deliver() int { return e.a.ProcessOutbox(context.Background(), 4) }

func (e *alertEnv) tick(d time.Duration) {
	e.clk.add(d)
	e.a.Tick(context.Background())
}

func (e *alertEnv) list(query string) []alerts.Alert {
	e.t.Helper()
	var p struct{ Items []alerts.Alert }
	r := e.c.do("GET", "/api/v1/alerts?"+query, nil)
	if r.status != 200 {
		e.t.Fatalf("list alerts: %d %s", r.status, r.body)
	}
	r.json(e.t, &p)
	return p.Items
}

func (e *alertEnv) detail(id string) alerts.Detail {
	e.t.Helper()
	var d alerts.Detail
	r := e.c.do("GET", "/api/v1/alerts/"+id, nil)
	if r.status != 200 {
		e.t.Fatalf("alert: %d %s", r.status, r.body)
	}
	r.json(e.t, &d)
	return d
}

func (e *alertEnv) only(query string) alerts.Alert {
	e.t.Helper()
	as := e.list(query)
	if len(as) != 1 {
		e.t.Fatalf("want one alert for %q, got %d: %+v", query, len(as), as)
	}
	return as[0]
}

// receiver is a fake webhook/Discord/Slack endpoint.
type receiver struct {
	srv    *httptest.Server
	mu     sync.Mutex
	got    []notify.WebhookPayload
	raw    []string
	hdr    []http.Header
	status atomic.Int32 // 0: 204
	hang   atomic.Bool
	hits   atomic.Int32
}

func newReceiver(t *testing.T) *receiver {
	r := &receiver{}
	r.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.hits.Add(1)
		b, _ := io.ReadAll(req.Body)
		if r.hang.Load() {
			select {
			case <-req.Context().Done():
			case <-time.After(20 * time.Second):
			}
		}
		var p notify.WebhookPayload
		json.Unmarshal(b, &p)
		r.mu.Lock()
		r.got, r.raw, r.hdr = append(r.got, p), append(r.raw, string(b)), append(r.hdr, req.Header.Clone())
		r.mu.Unlock()
		code := int(r.status.Load())
		if code == 0 {
			code = 204
		}
		w.WriteHeader(code)
	}))
	t.Cleanup(r.srv.Close)
	return r
}

func (r *receiver) events() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, p := range r.got {
		out = append(out, p.Event)
	}
	return out
}

func (r *receiver) last() notify.WebhookPayload {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.got[len(r.got)-1]
}

func (e *alertEnv) dest(name, kind, url, notifyFor string) string {
	e.t.Helper()
	var d alerts.Destination
	r := e.c.do("POST", "/api/v1/notifications/destinations", map[string]any{"name": name, "kind": kind, "config": map[string]any{"timeout_s": 2},
		"secrets": map[string]any{"url": url}, "notify_for": notifyFor})
	if r.status != 201 {
		e.t.Fatalf("destination: %d %s", r.status, r.body)
	}
	r.json(e.t, &d)
	return d.ID
}

func timelineKinds(d alerts.Detail) []string {
	var out []string
	for _, x := range d.Timeline {
		out = append(out, x.Kind)
	}
	return out
}

func TestAlertLifecycle(t *testing.T) {
	e := newAlertEnv(t)
	rcv := newReceiver(t)
	e.dest("Discord-ish webhook", "webhook", rcv.srv.URL+"/hook", "recommended")
	h := e.host("media01")

	e.cpu(h, 99, 6)
	a := e.only("view=active")
	if a.Severity != "warning" || a.Check != "cpu" || a.Category != "cpu" || a.HostName != "media01" || a.State != "active" ||
		a.ObservedAt == nil || *a.ObservedAt >= a.OpenedAt || a.Notification.Status != "pending" || a.ValueText != "99%" {
		t.Fatalf("opened alert: %+v", a)
	}
	if n := e.deliver(); n != 1 {
		t.Fatalf("deliveries: %d", n)
	}
	if ev := rcv.events(); !slices.Equal(ev, []string{"opened"}) {
		t.Fatalf("receiver: %v", ev)
	}
	p := rcv.last()
	if p.Alert.ID != a.ID || p.DeliveryID == "" || p.EventID != a.ID+":opening" || p.Alert.Fingerprint != h+"/cpu/" {
		t.Fatalf("payload: %+v", p)
	}

	// Alert storm prevention: the condition keeps being sampled.
	e.cpu(h, 99, 50)
	if as := e.list("view=open"); len(as) != 1 || as[0].ID != a.ID {
		t.Fatalf("50 more samples: %+v", as)
	}
	if e.deliver() != 0 || len(rcv.events()) != 1 {
		t.Fatalf("repeated samples notified again: %v", rcv.events())
	}

	// Acknowledgement: operators can, viewers can't; it isn't resolution.
	e.h.mkUser("rob", auth.RoleOperator)
	e.h.mkUser("vera", auth.RoleViewer)
	if r := e.h.login("vera").do("POST", "/api/v1/alerts/"+a.ID+"/acknowledge", map[string]any{"note": "x"}); r.status != 403 {
		t.Fatalf("viewer acknowledged: %d", r.status)
	}
	op := e.h.login("rob")
	r := op.do("POST", "/api/v1/alerts/"+a.ID+"/acknowledge", map[string]any{"note": "Replacing failed disk this evening"})
	if r.status != 200 {
		t.Fatalf("ack: %d %s", r.status, r.body)
	}
	if r := op.do("POST", "/api/v1/alerts/"+a.ID+"/acknowledge", nil); r.status != 409 {
		t.Fatalf("second ack: %d", r.status)
	}
	acked := e.only("view=acknowledged")
	if acked.Acknowledgment == nil || acked.Acknowledgment.By != "rob" || acked.Acknowledgment.Note != "Replacing failed disk this evening" {
		t.Fatalf("ack: %+v", acked)
	}
	if len(e.list("view=active")) != 0 {
		t.Fatal("acknowledged alert still listed as unacknowledged")
	}
	if is := e.h.mon.Settings(context.Background()); !is.Enabled {
		t.Fatal("monitoring disabled?")
	}
	var issues struct {
		Issues []struct{ Check, State string }
	}
	e.c.do("GET", "/api/v1/monitoring/issues?host_id="+h, nil).json(t, &issues)
	if len(issues.Issues) != 1 || issues.Issues[0].State != "active" {
		t.Fatalf("acknowledging changed the health issue: %+v", issues)
	}
	var audits listResponse[auditJSON]
	e.c.do("GET", "/api/v1/audit?action=alert.acknowledge", nil).json(t, &audits)
	if len(audits.Items) != 1 {
		t.Fatalf("acknowledgement not audited: %+v", audits)
	}

	// Recovery comes only from the source condition.
	e.cpu(h, 20, 4)
	res := e.only("view=resolved")
	if res.ID != a.ID || res.ResolvedAt == nil || res.Acknowledgment == nil || res.DurationMs <= 0 {
		t.Fatalf("resolved: %+v", res)
	}
	if e.deliver() != 1 {
		t.Fatal("recovery not delivered")
	}
	rec := rcv.last()
	if rec.Event != "recovered" || rec.Alert.State != "resolved" || rec.Alert.DurationS == nil || rec.Alert.Acknowledge == nil ||
		rec.Alert.Acknowledge.By != "rob" || !strings.Contains(rec.Text, "Duration: ") {
		t.Fatalf("recovery payload: %s", rcv.raw[len(rcv.raw)-1])
	}
	d := e.detail(a.ID)
	kinds := timelineKinds(d)
	for _, want := range []string{"observed", "issue", "opened", "notify", "delivered", "acknowledged", "recovered", "resolved"} {
		if !slices.Contains(kinds, want) {
			t.Errorf("timeline lacks %s: %v", want, kinds)
		}
	}
	for i := 1; i < len(d.Timeline); i++ {
		if d.Timeline[i].TS < d.Timeline[i-1].TS {
			t.Fatalf("timeline out of order: %+v", d.Timeline)
		}
	}
	if d.Issue == nil || d.Issue.State != "resolved" || len(d.Deliveries) != 2 {
		t.Fatalf("detail: issue %+v deliveries %d", d.Issue, len(d.Deliveries))
	}

	// The same condition again is a new occurrence, linked to the first.
	e.cpu(h, 99, 6)
	b := e.only("view=active")
	if b.ID == a.ID {
		t.Fatal("a new activation reused the old alert")
	}
	if occ := e.detail(b.ID).Occurrences; len(occ) != 1 || occ[0].ID != a.ID {
		t.Fatalf("occurrences: %+v", occ)
	}
	var evs struct{ Events []alerts.Event }
	e.c.do("GET", "/api/v1/events?limit=50", nil).json(t, &evs)
	var ek []string
	for _, ev := range evs.Events {
		ek = append(ek, ev.Kind)
	}
	for _, want := range []string{"alert.opened", "alert.acknowledged", "alert.recovered"} {
		if !slices.Contains(ek, want) {
			t.Errorf("event timeline lacks %s: %v", want, ek)
		}
	}
	if slices.Contains(ek, "notifications.failing") {
		t.Error("healthy delivery raised a failing event")
	}
}

func TestAlertIssueTypesSeverityAndEscalation(t *testing.T) {
	e := newAlertEnv(t)
	rcv := newReceiver(t)
	e.dest("hook", "webhook", rcv.srv.URL, "recommended")
	h := e.host("nas01")
	cases := []struct{ check, subject, sev, title, category string }{
		{"stale", "", "warning", "No monitoring data since 02:00 UTC", "stale"},
		{"memory", "", "warning", "Memory over 90% in use for 5 minutes", "memory"},
		{"filesystem", "/srv", "critical", "/srv is over 95% full", "disk"},
		{"inodes", "/", "warning", "/ is running out of inodes", "disk"},
		{"filesystem_readonly", "/data", "critical", "/data became read-only", "storage"},
		{"device_readonly", "mmcblk0", "critical", "mmcblk0 became read-only", "storage"},
		{"service_failed", "jellyfin.service", "critical", "jellyfin.service failed", "services"},
		{"service_stopped", "nginx.service", "critical", "nginx.service is not running", "services"},
		{"interface_down", "eth1", "warning", "Network interface eth1 is down", "network"},
		{"storage_health", "nvme0n1", "critical", "nvme0n1 (Samsung 990 PRO): SMART reports the drive is failing", "storage"},
		{"media_errors", "nvme0n1", "warning", "nvme0n1 (Samsung 990 PRO): new media errors", "storage"},
		{"kernel_storage", "sda", "warning", "sda: kernel reports IO errors", "storage"},
		{"probe", "/", "critical", "Write/read probe of / failed (write)", "storage"},
		{"temperature", "coretemp/Package id 0", "warning", "CPU (Package id 0) is above its high temperature", "temperature"},
	}
	for _, c := range cases {
		e.issue(h, c.check, c.subject, c.sev, c.title)
	}
	// Informational issues stay in monitoring and never become alerts.
	e.issue(h, "fidelity", "", "info", "Monitoring fidelity reduced to save resources")
	open := e.list("view=open&limit=200")
	if len(open) != len(cases) {
		t.Fatalf("want %d alerts, got %d", len(cases), len(open))
	}
	byCheck := map[string]alerts.Alert{}
	for _, a := range open {
		byCheck[a.Check] = a
	}
	for _, c := range cases {
		a := byCheck[c.check]
		if a.Severity != c.sev || a.Category != c.category || a.Title != c.title || a.Subject != c.subject {
			t.Errorf("%s: %+v", c.check, a)
		}
	}
	if _, ok := byCheck["fidelity"]; ok {
		t.Fatal("an informational issue became an alert")
	}
	// Critical first in the open views.
	if open[0].Severity != "critical" || open[len(open)-1].Severity != "warning" {
		t.Fatal("open alerts not sorted critical first")
	}
	if got := e.deliver(); got != len(cases) {
		t.Fatalf("deliveries: %d", got)
	}
	for _, c := range cases {
		e.recover(h, c.check, c.subject)
	}
	if len(e.list("view=open")) != 0 || e.deliver() != len(cases) {
		t.Fatal("recoveries")
	}

	// Escalation notifies once per level, however much the severity flaps.
	e.issue(h, "memory", "", "warning", "Memory over 90% in use for 5 minutes")
	e.deliver()
	before := len(rcv.events())
	for i := 0; i < 20; i++ {
		e.issue(h, "memory", "", "critical", "Memory over 97% in use: the host is close to running out")
		e.issue(h, "memory", "", "warning", "Memory over 90% in use for 5 minutes")
	}
	e.deliver()
	if ev := rcv.events()[before:]; !slices.Equal(ev, []string{"escalated"}) {
		t.Fatalf("flapping severity: %v", ev)
	}
	a := e.only("view=active")
	if a.PeakSeverity != "critical" || a.Severity != "warning" {
		t.Fatalf("peak: %+v", a)
	}
	if n := len(e.detail(a.ID).Timeline); n > 12 {
		t.Fatalf("flapping grew the timeline to %d entries", n)
	}
	e.recover(h, "memory", "")
	e.deliver()
	if last := rcv.last(); last.Event != "recovered" {
		t.Fatalf("recovery after escalation: %+v", last)
	}
	if n := rcv.events(); slices.Index(n, "recovered") == -1 {
		t.Fatal(n)
	}
}

func TestAlertMaintenance(t *testing.T) {
	e := newAlertEnv(t)
	rcv := newReceiver(t)
	e.dest("hook", "webhook", rcv.srv.URL, "recommended")
	h1, h2, h3 := e.host("web1"), e.host("web2"), e.host("web3")
	g := e.group("media", h2, h3)
	e.h.mkUser("op", auth.RoleOperator)
	e.h.mkUser("viewer", auth.RoleViewer)
	op := e.h.login("op")
	start := func(c *client, body map[string]any) alerts.Maintenance {
		t.Helper()
		var m alerts.Maintenance
		r := c.do("POST", "/api/v1/maintenance", body)
		if r.status != 201 {
			t.Fatalf("start maintenance: %d %s", r.status, r.body)
		}
		r.json(t, &m)
		return m
	}
	end := func(id string) {
		t.Helper()
		if r := op.do("POST", "/api/v1/maintenance/"+id+"/end", nil); r.status != 200 {
			t.Fatalf("end: %d %s", r.status, r.body)
		}
	}
	if r := e.h.login("viewer").do("POST", "/api/v1/maintenance", map[string]any{"host_ids": []string{h1}, "indefinite": true}); r.status != 403 {
		t.Fatalf("viewer started maintenance: %d", r.status)
	}
	for _, bad := range []map[string]any{{"indefinite": true}, {"host_ids": []string{h1}}, {"host_ids": []string{h1}, "indefinite": true, "duration_minutes": 5},
		{"host_ids": []string{"host_nope"}, "indefinite": true}, {"host_ids": []string{h1}, "until": e.clk.now().Add(-time.Hour).UnixMilli()}} {
		if r := op.do("POST", "/api/v1/maintenance", bad); r.status != 422 {
			t.Errorf("%v: %d %s", bad, r.status, r.body)
		}
	}

	// 1. An issue before maintenance: notified; then suppressed; its recovery during maintenance is not sent.
	e.issue(h1, "service_failed", "a.service", "critical", "a.service failed")
	e.deliver()
	m1 := start(op, map[string]any{"host_ids": []string{h1}, "duration_minutes": 120, "reason": "Kernel upgrade"})
	if !m1.Active || m1.EndsAt == nil || m1.CreatedBy != "op" || m1.Reason != "Kernel upgrade" || m1.HostCount != 1 {
		t.Fatalf("maintenance: %+v", m1)
	}
	a := e.only("view=active&host_id=" + h1)
	if !a.Suppressed || a.SuppressedBy == nil || a.SuppressedBy.Reason != "Kernel upgrade" {
		t.Fatalf("suppression: %+v", a)
	}
	// Health evaluation continues: the issue is still visible.
	var issues struct{ Issues []struct{ Check string } }
	e.c.do("GET", "/api/v1/monitoring/issues?host_id="+h1, nil).json(t, &issues)
	if len(issues.Issues) != 1 {
		t.Fatal("maintenance hid the health issue")
	}
	e.recover(h1, "service_failed", "a.service")
	if e.deliver() != 0 || len(rcv.events()) != 1 {
		t.Fatalf("recovery during maintenance was sent: %v", rcv.events())
	}

	// 2. Starts and recovers during maintenance: kept, marked, never sent, not even later.
	e.issue(h1, "service_failed", "b.service", "warning", "b.service failed")
	b := e.only("view=active&host_id=" + h1)
	if !b.Suppressed || !b.EverSuppressed || b.Notification.Status != "suppressed" {
		t.Fatalf("suppressed from the start: %+v", b)
	}
	e.recover(h1, "service_failed", "b.service")
	end(m1.ID)
	e.tick(10 * time.Minute)
	if e.deliver() != 0 || len(rcv.events()) != 1 {
		t.Fatalf("a delayed notification after maintenance: %v", rcv.events())
	}
	hist := e.list("view=resolved&suppressed=true&host_id=" + h1)
	if len(hist) != 2 {
		t.Fatalf("suppressed history: %+v", hist)
	}

	// 3. Starts during maintenance, still active when it ends: one opening after the grace period.
	m2 := start(op, map[string]any{"host_ids": []string{h1}, "indefinite": true, "reason": "Replacing storage"})
	e.issue(h1, "storage_health", "sda", "critical", "sda: failing")
	e.deliver()
	end(m2.ID)
	c := e.only("view=active&host_id=" + h1)
	if c.Suppressed || c.NotifyAfter == nil {
		t.Fatalf("after maintenance: %+v", c)
	}
	e.tick(time.Minute)
	if e.deliver() != 0 {
		t.Fatal("notified before the grace period")
	}
	e.tick(2 * time.Minute)
	if e.deliver() != 1 || rcv.last().Alert.ID != c.ID || rcv.last().Event != "opened" {
		t.Fatalf("one opening after maintenance: %v", rcv.events())
	}
	e.tick(5 * time.Minute)
	if e.deliver() != 0 {
		t.Fatal("more than one opening")
	}
	e.recover(h1, "storage_health", "sda")
	e.deliver()

	// 4. Expiry.
	m3 := start(op, map[string]any{"host_ids": []string{h1}, "duration_minutes": 30})
	e.issue(h1, "interface_down", "eth0", "warning", "Network interface eth0 is down")
	e.tick(31 * time.Minute)
	var mm alerts.Maintenance
	e.c.do("GET", "/api/v1/maintenance/"+m3.ID, nil).json(t, &mm)
	if mm.Active || mm.EndedAt == nil {
		t.Fatalf("expired maintenance: %+v", mm)
	}
	e.tick(3 * time.Minute)
	if e.deliver() != 1 || rcv.last().Alert.Check != "interface_down" {
		t.Fatalf("after expiry: %v", rcv.events())
	}
	e.recover(h1, "interface_down", "eth0")
	e.deliver()

	// 5. Group maintenance and overlapping host + group maintenance.
	mg := start(op, map[string]any{"group_ids": []string{g}, "indefinite": true, "reason": "Media rebuild"})
	mh := start(op, map[string]any{"host_ids": []string{h2}, "indefinite": true})
	e.issue(h2, "service_failed", "jellyfin.service", "critical", "jellyfin.service failed")
	e.issue(h3, "service_failed", "plex.service", "critical", "plex.service failed")
	if e.deliver() != 0 {
		t.Fatal("group maintenance didn't suppress")
	}
	end(mh.ID)
	e.tick(5 * time.Minute)
	if a := e.only("view=active&host_id=" + h2); !a.Suppressed || a.SuppressedBy.ID != mg.ID || e.deliver() != 0 {
		t.Fatalf("still covered by the group: %+v", a)
	}
	end(mg.ID)
	e.tick(3 * time.Minute)
	if n := e.deliver(); n != 2 {
		t.Fatalf("after both ended: %d", n)
	}
	var sum alerts.Summary
	e.c.do("GET", "/api/v1/alerts/summary", nil).json(t, &sum)
	if len(sum.Maintenance) != 0 || sum.Critical != 2 {
		t.Fatalf("summary: %+v", sum)
	}
	var ml struct{ Maintenance []alerts.Maintenance }
	e.c.do("GET", "/api/v1/maintenance?state=all", nil).json(t, &ml)
	if len(ml.Maintenance) != 5 {
		t.Fatalf("maintenance history: %d", len(ml.Maintenance))
	}
	var audits listResponse[auditJSON]
	e.c.do("GET", "/api/v1/audit?action=maintenance.", nil).json(t, &audits)
	if len(audits.Items) < 8 {
		t.Fatalf("maintenance not audited: %d", len(audits.Items))
	}
}

func TestAlertRouting(t *testing.T) {
	e := newAlertEnv(t)
	ra, rb, rc := newReceiver(t), newReceiver(t), newReceiver(t)
	A := e.dest("A email-ish", "webhook", ra.srv.URL, "recommended")
	B := e.dest("B discord-ish", "webhook", rb.srv.URL, "custom")
	C := e.dest("C slack-ish", "webhook", rc.srv.URL, "custom")
	h1, nas := e.host("app1"), e.host("nas1")
	g := e.group("NAS", nas)
	route := func(body map[string]any) string {
		t.Helper()
		var r alerts.Route
		res := e.c.do("POST", "/api/v1/notifications/routes", body)
		if res.status != 201 {
			t.Fatalf("route: %d %s", res.status, res.body)
		}
		res.json(t, &r)
		return r.ID
	}
	route(map[string]any{"name": "All Critical", "severities": []string{"critical"}, "destination_ids": []string{B, C}})
	route(map[string]any{"name": "Storage on NAS", "group_ids": []string{g}, "categories": []string{"storage"}, "destination_ids": []string{C}})
	route(map[string]any{"name": "Everything to A again", "destination_ids": []string{A}})
	svcRoute := route(map[string]any{"name": "app1 services", "host_ids": []string{h1}, "checks": []string{"service_failed"}, "severities": []string{"warning"},
		"destination_ids": []string{B}, "recovery": false})
	count := func() [3]int { return [3]int{len(ra.events()), len(rb.events()), len(rc.events())} }
	step := func(name string, want [3]int, f func()) {
		t.Helper()
		before := count()
		f()
		e.deliver()
		after := count()
		got := [3]int{after[0] - before[0], after[1] - before[1], after[2] - before[2]}
		if got != want {
			t.Fatalf("%s: deliveries A/B/C = %v, want %v", name, got, want)
		}
	}
	step("warning cpu on app1 (default + duplicate route → A once)", [3]int{1, 0, 0}, func() { e.issue(h1, "cpu", "", "warning", "CPU") })
	step("critical memory (severity filter)", [3]int{1, 1, 1}, func() { e.issue(h1, "memory", "", "critical", "Memory") })
	step("storage warning on NAS (group + category filter)", [3]int{1, 0, 1}, func() { e.issue(nas, "media_errors", "sda", "warning", "sda: new media errors") })
	step("service warning on app1 (host + check filter)", [3]int{1, 1, 0}, func() { e.issue(h1, "service_failed", "x.service", "warning", "x.service failed") })
	step("service warning on NAS (no host match)", [3]int{1, 0, 0}, func() { e.issue(nas, "service_failed", "y.service", "warning", "y.service failed") })
	// Recoveries go to destinations that got the opening, if their route wants them.
	step("recovery of app1 service (B's route has recovery off)", [3]int{1, 0, 0}, func() { e.recover(h1, "service_failed", "x.service") })
	step("recovery of memory", [3]int{1, 1, 1}, func() { e.recover(h1, "memory", "") })

	var pv alerts.RoutePreview
	e.c.do("POST", "/api/v1/notifications/routes/preview", map[string]any{"host_id": nas, "check": "storage_health", "severity": "critical"}).json(t, &pv)
	var names []string
	for _, d := range pv.Destinations {
		names = append(names, d.Name)
	}
	if !slices.Equal(names, []string{"A email-ish", "B discord-ish", "C slack-ish"}) {
		t.Fatalf("preview: %v", names)
	}
	// Disabled routes and destinations are skipped.
	e.c.do("PATCH", "/api/v1/notifications/routes/"+svcRoute, map[string]any{"enabled": false})
	e.c.do("PATCH", "/api/v1/notifications/destinations/"+C, map[string]any{"enabled": false})
	step("disabled route and destination", [3]int{1, 1, 0}, func() { e.issue(h1, "probe", "/", "critical", "probe failed") })
	if r := e.c.do("DELETE", "/api/v1/notifications/routes/"+alerts.DefaultRouteID, nil); r.status != 409 {
		t.Fatalf("deleting the default route: %d", r.status)
	}
	for _, bad := range []map[string]any{{"name": ""}, {"name": "x", "severities": []string{"info"}}, {"name": "x", "categories": []string{"nope"}},
		{"name": "x", "reminder_minutes": 5}, {"name": "x", "destination_ids": []string{"ndst_nope"}}} {
		if r := e.c.do("POST", "/api/v1/notifications/routes", bad); r.status != 422 {
			t.Errorf("%v: %d", bad, r.status)
		}
	}

	// A route keeps a deleted host in its filter (matching nothing, never
	// widening to every host) and can still be edited.
	gone := e.host("gone1")
	gr := route(map[string]any{"name": "Only gone1", "host_ids": []string{gone}, "destination_ids": []string{B}})
	e.h.st.Writer.Exec(`DELETE FROM hosts WHERE id = ?`, gone)
	if r := e.c.do("PATCH", "/api/v1/notifications/routes/"+gr, map[string]any{"name": "Only gone1 (renamed)"}); r.status != 200 {
		t.Fatalf("editing a route whose host was deleted: %d %s", r.status, r.body)
	}
	step("a route for a deleted host matches nothing", [3]int{1, 0, 0}, func() { e.issue(h1, "inodes", "/", "warning", "inodes") })

	// Reminders: off by default; a route can ask for them; acknowledgement stops them.
	e.tick(3 * time.Hour)
	if e.deliver() != 0 {
		t.Fatal("reminder without a reminder route")
	}
	rem := route(map[string]any{"name": "Remind", "severities": []string{"critical"}, "destination_ids": []string{A}, "reminder_minutes": 60})
	_ = rem
	e.issue(h1, "storage_health", "nvme0n1", "critical", "nvme0n1: failing")
	e.deliver()
	a := e.only("view=active&check=storage_health")
	e.tick(61 * time.Minute)
	if e.deliver() != 1 || ra.last().Event != "reminder" || ra.last().EventID != a.ID+":reminder:1" {
		t.Fatalf("reminder: %v", ra.events())
	}
	e.c.do("POST", "/api/v1/alerts/"+a.ID+"/acknowledge", nil)
	e.tick(2 * time.Hour)
	if e.deliver() != 0 {
		t.Fatal("reminder after acknowledgement")
	}
}

func TestAlertDeliveryFailures(t *testing.T) {
	e := newAlertEnv(t)
	rcv := newReceiver(t)
	D := e.dest("flaky", "webhook", rcv.srv.URL, "recommended")
	h := e.host("srv1")

	// Transient failures retry with backoff and are not new alerts.
	rcv.status.Store(503)
	e.issue(h, "cpu", "", "warning", "CPU")
	e.deliver()
	e.tick(500 * time.Millisecond)
	if e.deliver() != 0 {
		t.Fatal("retried before the backoff")
	}
	e.tick(2 * time.Second)
	e.deliver()
	rcv.status.Store(0)
	e.tick(5 * time.Second)
	e.deliver()
	a := e.only("view=active")
	d := e.detail(a.ID)
	if len(d.Deliveries) != 1 || d.Deliveries[0].Status != "delivered" || d.Deliveries[0].Attempts != 3 || len(d.Deliveries[0].AttemptLog) != 3 ||
		d.Deliveries[0].AttemptLog[0].Outcome != "transient" || d.Deliveries[0].AttemptLog[0].StatusCode == nil {
		t.Fatalf("retries: %+v", d.Deliveries)
	}
	if len(e.list("view=all")) != 1 {
		t.Fatal("a delivery failure created another alert")
	}
	tl := timelineKinds(d)
	if !slices.Contains(tl, "delivered") || slices.Contains(tl, "delivery_failed") {
		t.Fatalf("timeline: %v", tl)
	}

	// Permanent failure (401): no retries, destination failing, one event, recovery not sent.
	rcv.status.Store(401)
	e.issue(h, "memory", "", "warning", "Memory")
	e.deliver()
	e.tick(time.Minute)
	if e.deliver() != 0 {
		t.Fatal("retried a permanent failure")
	}
	var dest alerts.Destination
	e.c.do("GET", "/api/v1/notifications/destinations/"+D, nil).json(t, &dest)
	if dest.Health.Status != "failing" || dest.Health.LastError == "" || dest.Health.LastSuccessAt == nil {
		t.Fatalf("destination health: %+v", dest.Health)
	}
	sent := rcv.hits.Load()
	e.recover(h, "memory", "")
	e.deliver()
	if rcv.hits.Load() != sent {
		t.Fatal("recovery sent for an opening that was never delivered")
	}
	m := e.only("view=resolved")
	if m.Notification.Status != "failed" {
		t.Fatalf("notification status: %+v", m.Notification)
	}
	// Alert state is independent of the destination: monitoring and the alert list work.
	if r := e.c.do("GET", "/api/v1/alerts/summary", nil); r.status != 200 {
		t.Fatal(r.status)
	}

	// Transient failures up to the attempt limit: failed, never forever.
	rcv.status.Store(500)
	e.issue(h, "probe", "/", "critical", "probe")
	for range 12 {
		e.deliver()
		e.tick(5 * time.Second)
	}
	var ds struct{ Items []alerts.Delivery }
	e.c.do("GET", "/api/v1/notifications/deliveries?status=failed", nil).json(t, &ds)
	found := false
	for _, x := range ds.Items {
		if x.Kind == "opening" && x.Attempts == 8 && x.LastCategory == "server_error" {
			found = true
		}
	}
	if !found {
		t.Fatalf("attempt limit: %+v", ds.Items)
	}
	var evs struct{ Events []alerts.Event }
	e.c.do("GET", "/api/v1/events?kind=notifications.", nil).json(t, &evs)
	if len(evs.Events) != 1 || evs.Events[0].Kind != "notifications.failing" {
		t.Fatalf("one failing event, not one per attempt: %+v", evs.Events)
	}
	// It works again: one recovered event.
	rcv.status.Store(0)
	e.issue(h, "stale", "", "warning", "stale")
	e.deliver()
	e.c.do("GET", "/api/v1/events?kind=notifications.", nil).json(t, &evs)
	if len(evs.Events) != 2 || evs.Events[0].Kind != "notifications.recovered" {
		t.Fatalf("recovered event: %+v", evs.Events)
	}

	// 429 honours Retry-After.
	rcv.status.Store(429)
	e.issue(h, "interface_down", "eth0", "warning", "eth0 down")
	e.deliver()
	var q struct{ Items []alerts.Delivery }
	e.c.do("GET", "/api/v1/notifications/deliveries?status=queued", nil).json(t, &q)
	if len(q.Items) != 1 || q.Items[0].NextAttemptAt == nil || *q.Items[0].NextAttemptAt < e.clk.now().UnixMilli()+500 {
		t.Fatalf("429: %+v", q.Items)
	}
	rcv.status.Store(0)
}

func TestAlertOutboxDurability(t *testing.T) {
	e := newAlertEnv(t)
	rcv := newReceiver(t)
	e.dest("hook", "webhook", rcv.srv.URL, "recommended")
	h := e.host("db1")
	ctx := context.Background()
	restart := func() *alerts.Service {
		s := alerts.New(alerts.Deps{Store: e.h.st, Box: e.h.box, Audit: e.h.srv.d.Audit, Log: e.h.srv.d.Logs.App, Sender: testSender,
			Config: alerts.Config{DisableWorkers: true, BackoffBase: time.Second, BackoffMax: 4 * time.Second, RatePerMinute: 6000}})
		s.SetClock(e.clk.now)
		return s
	}

	// The controller "crashes" right after the issue: the alert and its
	// delivery are committed, nothing was sent.
	e.issue(h, "service_failed", "postgresql.service", "critical", "postgresql.service failed")
	if len(rcv.events()) != 0 {
		t.Fatal("sent inside the transaction?")
	}
	var n int
	e.h.st.Reader.QueryRow(`SELECT count(*) FROM notify_deliveries WHERE status = 'queued'`).Scan(&n)
	if n != 1 {
		t.Fatalf("queued deliveries: %d", n)
	}
	// A new controller process.
	s2 := restart()
	s2.RecoverLeases(ctx, true)
	if s2.ProcessOutbox(ctx, 2) != 1 || len(rcv.events()) != 1 {
		t.Fatalf("after restart: %v", rcv.events())
	}

	// Crash during delivery after the receiver accepted it: the lease is
	// recovered, the attempt is marked interrupted and ambiguous, and it is
	// sent again (the receiver can deduplicate on delivery_id).
	rcv.hang.Store(true)
	e.issue(h, "service_failed", "redis.service", "critical", "redis.service failed")
	cctx, crash := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { e.a.ProcessOutbox(cctx, 1); close(done) }()
	deadline := time.Now().Add(5 * time.Second)
	for rcv.hits.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	crash()
	<-done
	rcv.hang.Store(false)
	var status string
	e.h.st.Reader.QueryRow(`SELECT status FROM notify_deliveries WHERE kind = 'opening' ORDER BY rowid DESC LIMIT 1`).Scan(&status)
	if status != "sending" {
		t.Fatalf("a crashed delivery should still be leased: %s", status)
	}
	s3 := restart()
	s3.RecoverLeases(ctx, true)
	if s3.ProcessOutbox(ctx, 2) != 1 {
		t.Fatal("not re-delivered after the crash")
	}
	got := rcv.events()
	ids := map[string]int{}
	rcv.mu.Lock()
	for _, p := range rcv.got {
		ids[p.DeliveryID]++
	}
	rcv.mu.Unlock()
	dup := 0
	for _, c := range ids {
		if c > 1 {
			dup++
		}
	}
	if len(got) != 3 || dup != 1 {
		t.Fatalf("expected one duplicate (at-least-once): %v %v", got, ids)
	}
	var del struct{ Items []alerts.Delivery }
	e.c.do("GET", "/api/v1/notifications/deliveries?limit=1", nil).json(t, &del)
	if !del.Items[0].Ambiguous || del.Items[0].AttemptLog[0].Outcome != "interrupted" || del.Items[0].Status != "delivered" {
		t.Fatalf("ambiguity recorded: %+v", del.Items[0])
	}
	if len(e.list("view=open")) != 2 {
		t.Fatal("a crash created or lost alerts")
	}

	// Restart during retry: the scheduled retry survives.
	rcv.status.Store(502)
	e.issue(h, "cpu", "", "warning", "CPU")
	e.deliver()
	s4 := restart()
	rcv.status.Store(0)
	e.clk.add(5 * time.Second)
	s4.RecoverLeases(ctx, true)
	if s4.ProcessOutbox(ctx, 2) != 1 {
		t.Fatal("retry lost across restart")
	}

	// Duplicate worker pickup: many workers (and a second service on the
	// same database) never deliver one row twice.
	for i := range 30 {
		e.issue(h, "service_failed", fmt.Sprintf("s%02d.service", i), "warning", "failed")
	}
	before := len(rcv.events())
	var wg sync.WaitGroup
	for _, s := range []*alerts.Service{e.a, restart()} {
		wg.Add(1)
		go func() { defer wg.Done(); s.ProcessOutbox(ctx, 8) }()
	}
	wg.Wait()
	if after := len(rcv.events()) - before; after != 30 {
		t.Fatalf("30 deliveries with 16 workers: %d", after)
	}
	ids = map[string]int{}
	rcv.mu.Lock()
	for _, p := range rcv.got[before:] {
		ids[p.DeliveryID]++
	}
	rcv.mu.Unlock()
	for id, c := range ids {
		if c != 1 {
			t.Fatalf("delivery %s sent %d times", id, c)
		}
	}

	// Idempotent state: reconciling again changes nothing.
	var alertsBefore, delBefore int
	e.h.st.Reader.QueryRow(`SELECT count(*) FROM alerts`).Scan(&alertsBefore)
	e.h.st.Reader.QueryRow(`SELECT count(*) FROM notify_deliveries`).Scan(&delBefore)
	for range 3 {
		e.a.Reconcile(ctx)
		e.tick(time.Minute)
	}
	var alertsAfter, delAfter int
	e.h.st.Reader.QueryRow(`SELECT count(*) FROM alerts`).Scan(&alertsAfter)
	e.h.st.Reader.QueryRow(`SELECT count(*) FROM notify_deliveries`).Scan(&delAfter)
	if alertsAfter != alertsBefore || delAfter != delBefore {
		t.Fatalf("reconcile changed state: %d→%d alerts, %d→%d deliveries", alertsBefore, alertsAfter, delBefore, delAfter)
	}

	// Alerting can't stop monitoring from recording an issue: a failure in
	// the alert hook rolls back only alerting, and reconciliation repairs it.
	e.h.st.Writer.Exec(`CREATE TRIGGER boom BEFORE INSERT ON alerts BEGIN SELECT RAISE(ABORT, 'boom'); END`)
	e.issue(h, "temperature", "cpu", "critical", "CPU hot")
	var active int
	e.h.st.Reader.QueryRow(`SELECT count(*) FROM mon_issues WHERE check_id = 'temperature' AND state = 'active'`).Scan(&active)
	if active != 1 || len(e.list("view=open&check=temperature")) != 0 {
		t.Fatalf("issue recorded %d, alerts %d", active, len(e.list("view=open&check=temperature")))
	}
	e.h.st.Writer.Exec(`DROP TRIGGER boom`)
	e.tick(time.Second)
	a := e.only("view=open&check=temperature")
	if d := e.detail(a.ID); !strings.Contains(d.Timeline[len(d.Timeline)-2].Text+d.Timeline[len(d.Timeline)-1].Text, "already active") {
		t.Fatalf("reconciled alert: %+v", d.Timeline)
	}
}

func TestAlertEndpointIsolation(t *testing.T) {
	e := newAlertEnv(t)
	rcv := newReceiver(t)
	rcv.hang.Store(true)
	e.dest("hanging", "webhook", rcv.srv.URL, "recommended")
	h := e.host("busy1")
	for i := range 8 {
		e.issue(h, "service_failed", fmt.Sprintf("u%d.service", i), "warning", "failed")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.a.ProcessOutbox(ctx, 4)
	deadline := time.Now().Add(5 * time.Second)
	for rcv.hits.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	// While deliveries hang, ingestion, health evaluation and the API stay fast.
	start := time.Now()
	e.cpu(h, 99, 6)
	if len(e.list("view=active&check=cpu")) != 1 {
		t.Fatal("health evaluation stalled")
	}
	if r := e.c.do("GET", "/api/v1/monitoring/summary", nil); r.status != 200 {
		t.Fatal(r.status)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("monitoring blocked by a hanging destination: %v", d)
	}
	if hits := rcv.hits.Load(); hits > 4 {
		t.Fatalf("more concurrent requests than the per-destination bound: %d", hits)
	}
}

func TestAlertPermissionsAndSecrets(t *testing.T) {
	e := newAlertEnv(t)
	rcv := newReceiver(t)
	for _, u := range []struct{ n, r string }{{"op", auth.RoleOperator}, {"aud", auth.RoleAuditor}, {"view", auth.RoleViewer}} {
		e.h.mkUser(u.n, u.r)
	}
	h := e.host("h1")
	secretURL := rcv.srv.URL + "/api/webhooks/123/SUPERSECRETWEBHOOKTOKEN"
	var d alerts.Destination
	r := e.c.do("POST", "/api/v1/notifications/destinations", map[string]any{"name": "Discord", "kind": "discord",
		"config": map[string]any{}, "secrets": map[string]any{"url": secretURL}})
	r.json(t, &d)
	if r.status != 201 || strings.Contains(string(r.body), "SUPERSECRET") || !d.SecretsSet.URL || d.NotifyFor != "recommended" {
		t.Fatalf("create: %d %s", r.status, r.body)
	}
	var w alerts.Destination
	r = e.c.do("POST", "/api/v1/notifications/destinations", map[string]any{"name": "hook", "kind": "webhook",
		"config": map[string]any{"header_name": "X-Api-Key"}, "secrets": map[string]any{"url": rcv.srv.URL + "/x?token=QUERYSECRET", "header_value": "HEADERSECRET"}})
	r.json(t, &w)
	if strings.Contains(string(r.body), "QUERYSECRET") || strings.Contains(string(r.body), "HEADERSECRET") || !w.SecretsSet.HeaderValue {
		t.Fatalf("webhook create: %s", r.body)
	}
	e.issue(h, "cpu", "", "warning", "CPU")
	e.deliver()
	if len(rcv.events()) != 2 {
		t.Fatalf("both destinations: %v", rcv.events())
	}
	rcv.mu.Lock()
	gotKey := ""
	for _, hd := range rcv.hdr {
		if v := hd.Get("X-Api-Key"); v != "" {
			gotKey = v
		}
	}
	rcv.mu.Unlock()
	if gotKey != "HEADERSECRET" {
		t.Fatal("header secret not sent")
	}
	a := e.only("view=active")

	type call struct {
		method, path string
		body         any
		want         map[string]int // role → status
	}
	ok := func(admin, op, aud, view int) map[string]int {
		return map[string]int{"admin": admin, "op": op, "aud": aud, "view": view}
	}
	calls := []call{
		{"GET", "/api/v1/alerts", nil, ok(200, 200, 200, 200)},
		{"GET", "/api/v1/alerts/summary", nil, ok(200, 200, 200, 200)},
		{"GET", "/api/v1/alerts/" + a.ID, nil, ok(200, 200, 200, 200)},
		{"GET", "/api/v1/events", nil, ok(200, 200, 200, 200)},
		{"GET", "/api/v1/maintenance", nil, ok(200, 200, 200, 200)},
		{"POST", "/api/v1/maintenance", map[string]any{}, ok(422, 422, 403, 403)},
		{"GET", "/api/v1/notifications/destinations", nil, ok(200, 200, 200, 403)},
		{"GET", "/api/v1/notifications/destinations/" + d.ID, nil, ok(200, 200, 200, 403)},
		{"POST", "/api/v1/notifications/destinations", map[string]any{}, ok(422, 403, 403, 403)},
		{"PATCH", "/api/v1/notifications/destinations/" + d.ID, map[string]any{"name": ""}, ok(422, 403, 403, 403)},
		{"DELETE", "/api/v1/notifications/destinations/ndst_nope", nil, ok(404, 403, 403, 403)},
		{"POST", "/api/v1/notifications/destinations/" + d.ID + "/test", nil, ok(200, 200, 403, 403)},
		{"POST", "/api/v1/notifications/test", map[string]any{"kind": "webhook"}, ok(200, 403, 403, 403)},
		{"GET", "/api/v1/notifications/routes", nil, ok(200, 200, 200, 403)},
		{"POST", "/api/v1/notifications/routes", map[string]any{}, ok(422, 403, 403, 403)},
		{"GET", "/api/v1/notifications/deliveries", nil, ok(200, 200, 200, 403)},
		{"GET", "/api/v1/notifications/settings", nil, ok(200, 200, 200, 403)},
		{"PUT", "/api/v1/notifications/settings", map[string]any{"alert_retention_days": 1}, ok(422, 403, 403, 403)},
		{"POST", "/api/v1/alerts/alr_nope/acknowledge", nil, ok(404, 404, 403, 403)},
	}
	clients := map[string]*client{"admin": e.c, "op": e.h.login("op"), "aud": e.h.login("aud"), "view": e.h.login("view")}
	for _, c := range calls {
		for role, want := range c.want {
			r := clients[role].do(c.method, c.path, c.body)
			if r.status != want {
				t.Errorf("%s %s as %s: %d, want %d (%s)", c.method, c.path, role, r.status, want, r.body)
			}
			for _, s := range []string{"SUPERSECRET", "QUERYSECRET", "HEADERSECRET"} {
				if strings.Contains(string(r.body), s) {
					t.Errorf("%s %s as %s leaked %s", c.method, c.path, role, s)
				}
			}
		}
	}
	// Viewers see alerts but not delivery error detail or destinations' errors.
	var vd alerts.Detail
	clients["view"].do("GET", "/api/v1/alerts/"+a.ID, nil).json(t, &vd)
	for _, x := range vd.Deliveries {
		if x.LastError != "" || len(x.AttemptLog) > 0 {
			t.Fatalf("viewer saw delivery detail: %+v", x)
		}
	}
	// The audit log never holds the secrets.
	var audits listResponse[auditJSON]
	r = e.c.do("GET", "/api/v1/audit?action=notifications.", nil)
	r.json(t, &audits)
	if len(audits.Items) < 3 || strings.Contains(string(r.body), "SUPERSECRET") || strings.Contains(string(r.body), "HEADERSECRET") || strings.Contains(string(r.body), "QUERYSECRET") {
		t.Fatalf("audit: %d items, leak=%v", len(audits.Items), strings.Contains(string(r.body), "SECRET\""))
	}
	if !strings.Contains(string(r.body), "secrets_changed") {
		t.Fatal("audit doesn't name changed secrets")
	}
	// Updating without secrets keeps them; the database holds ciphertext only.
	if r := e.c.do("PATCH", "/api/v1/notifications/destinations/"+d.ID, map[string]any{"name": "Discord ops"}); r.status != 200 {
		t.Fatal(r.body)
	}
	var tr notify.Result
	e.c.do("POST", "/api/v1/notifications/destinations/"+d.ID+"/test", nil).json(t, &tr)
	if !tr.OK() {
		t.Fatalf("test after rename: %+v", tr)
	}
	var raw string
	e.h.st.Reader.QueryRow(`SELECT config_json || hex(secret_ciphertext) FROM notify_destinations WHERE id = ?`, d.ID).Scan(&raw)
	if strings.Contains(raw, "SUPERSECRET") || strings.Contains(strings.ToUpper(raw), fmt.Sprintf("%X", "SUPERSECRET")) {
		t.Fatal("secret stored in plaintext")
	}
	// A test notification creates no alert.
	if len(e.list("view=all")) != 1 {
		t.Fatal("a test created an alert")
	}
	// CSRF: a session request without the token is refused.
	csrf := *clients["op"]
	csrf.csrf = ""
	if r := csrf.do("POST", "/api/v1/alerts/"+a.ID+"/acknowledge", nil); r.status != 403 || r.problemCode(t) != "csrf" {
		t.Fatalf("csrf: %d %s", r.status, r.body)
	}
}

func TestAlertDestinationTestResults(t *testing.T) {
	e := newAlertEnv(t)
	rcv := newReceiver(t)
	test := func(body map[string]any) notify.Result {
		t.Helper()
		var r notify.Result
		res := e.c.do("POST", "/api/v1/notifications/test", body)
		if res.status != 200 {
			t.Fatalf("test: %d %s", res.status, res.body)
		}
		res.json(t, &r)
		return r
	}
	if r := test(map[string]any{"kind": "slack", "config": map[string]any{}, "secrets": map[string]any{"url": rcv.srv.URL + "/services/T/B/x"}}); !r.OK() {
		t.Fatalf("success: %+v", r)
	}
	if p := rcv.last(); p.Event != "" {
		// Slack bodies aren't webhook payloads; just make sure it arrived.
		_ = p
	}
	rcv.status.Store(403)
	if r := test(map[string]any{"kind": "slack", "config": map[string]any{}, "secrets": map[string]any{"url": rcv.srv.URL + "/services/T/B/x"}}); r.Category != "auth" {
		t.Fatalf("auth: %+v", r)
	}
	rcv.status.Store(0)
	if r := test(map[string]any{"kind": "webhook", "config": map[string]any{}, "secrets": map[string]any{"url": "ftp://x"}}); r.Category != "config" {
		t.Fatalf("malformed: %+v", r)
	}
	plain := httptest.NewServer(http.NotFoundHandler())
	defer plain.Close()
	if r := test(map[string]any{"kind": "discord", "config": map[string]any{}, "secrets": map[string]any{"url": strings.Replace(plain.URL, "http:", "https:", 1) + "/api/webhooks/1/x"}}); r.Category != "tls" {
		t.Fatalf("tls: %+v", r)
	}
	rcv.hang.Store(true)
	if r := test(map[string]any{"kind": "webhook", "config": map[string]any{"timeout_s": 1}, "secrets": map[string]any{"url": rcv.srv.URL}}); r.Category != "timeout" {
		t.Fatalf("timeout: %+v", r)
	}
	rcv.hang.Store(false)
	if len(e.list("view=all")) != 0 {
		t.Fatal("tests created alerts")
	}
	var audits listResponse[auditJSON]
	e.c.do("GET", "/api/v1/audit?action=notifications.destination.test", nil).json(t, &audits)
	if len(audits.Items) != 5 {
		t.Fatalf("tests audited: %d", len(audits.Items))
	}
	// Settings: links in notifications.
	var sv struct{ Settings alerts.Settings }
	e.c.do("PUT", "/api/v1/notifications/settings", map[string]any{"link_base_url": "https://drool.lan:8443/", "alert_retention_days": 180, "delivery_retention_days": 30}).json(t, &sv)
	if sv.Settings.LinkBaseURL != "https://drool.lan:8443" {
		t.Fatalf("settings: %+v", sv)
	}
	e.dest("hook", "webhook", rcv.srv.URL, "recommended")
	h := e.host("x1")
	e.issue(h, "cpu", "", "warning", "CPU")
	e.deliver()
	if u := rcv.last().Alert.URL; !strings.HasPrefix(u, "https://drool.lan:8443/alerts/alr_") {
		t.Fatalf("link: %q", u)
	}
}
