package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"drool/internal/alerts"
	"drool/internal/auth"
)

// TestAlertScale measures alerting's database growth and query cost, and
// delivery under load. Run with DROOL_SCALE_TEST=1.
//
//   - Real path: alert lifecycles (open, deliver to two destinations,
//     resolve, deliver recoveries) give the bytes one alert costs.
//   - Retention: history for 5, 50 and 200 hosts at three alerts per host
//     per day for the default 180 days (cloned rows), then the list,
//     history, summary and detail queries and a prune.
//   - Burst: 200 hosts fail at once; bounded workers drain the outbox.
//   - Flapping: one condition opening and recovering 500 times.
//   - Outage: every destination refuses connections while issues keep
//     arriving.
func TestAlertScale(t *testing.T) {
	if os.Getenv("DROOL_SCALE_TEST") == "" {
		t.Skip("set DROOL_SCALE_TEST=1")
	}
	ctx := context.Background()
	size := func(e *alertEnv) float64 {
		e.h.st.Writer.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`)
		var pages, free, psize int64
		e.h.st.Reader.QueryRow(`PRAGMA page_count`).Scan(&pages)
		e.h.st.Reader.QueryRow(`PRAGMA freelist_count`).Scan(&free)
		e.h.st.Reader.QueryRow(`PRAGMA page_size`).Scan(&psize)
		return float64((pages - free) * psize) // in use (deleted rows' pages are reused)
	}
	var inflight, peak atomic.Int32
	slow := atomic.Bool{}
	recv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := inflight.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		if slow.Load() {
			time.Sleep(20 * time.Millisecond)
		}
		inflight.Add(-1)
		w.WriteHeader(204)
	}))
	defer recv.Close()

	// --- real path: bytes per alert ---
	e := newAlertEnv(t)
	e.dest("chat", "webhook", recv.URL+"/a", "recommended")
	e.dest("mail-ish", "webhook", recv.URL+"/b", "recommended")
	var hosts []string
	for i := range 200 {
		hosts = append(hosts, e.host(fmt.Sprintf("node-%03d", i)))
	}
	before := size(e)
	const sample = 600
	t0 := time.Now()
	for i := range sample {
		h := hosts[i%len(hosts)]
		sub := fmt.Sprintf("svc%d.service", i)
		e.issue(h, "service_failed", sub, "warning", sub+" failed")
		e.deliver()
		e.clk.add(7 * time.Minute)
		e.recover(h, "service_failed", sub)
		e.deliver()
	}
	per := time.Since(t0) / sample
	perAlert := (size(e) - before) / sample
	var deliveries, events, timeline int
	e.h.st.Reader.QueryRow(`SELECT count(*) FROM notify_deliveries`).Scan(&deliveries)
	e.h.st.Reader.QueryRow(`SELECT count(*) FROM events`).Scan(&events)
	e.h.st.Reader.QueryRow(`SELECT count(*) FROM alert_events`).Scan(&timeline)
	t.Logf("real path: %d alert lifecycles with 2 destinations: %.0f bytes per alert (incl. %d deliveries, %d timeline rows, %d events); %v per lifecycle (open+2 deliveries+resolve+2 recoveries)",
		sample, perAlert, deliveries/sample, timeline/sample, events/sample, per.Round(time.Microsecond))

	// --- retention-sized history: 3 alerts/host/day for 180 days ---
	for _, n := range []int{5, 50, 200} {
		target := n * 3 * 180
		e.h.st.Writer.Exec(`DELETE FROM alerts WHERE id LIKE 'alr_clone%'`)
		fill := time.Now()
		// Clone the sample's rows (alerts, their timeline, deliveries,
		// attempts) onto the first n hosts, spread over 180 days.
		cloneAlerts := `WITH h AS (SELECT id AS hid, row_number() OVER (ORDER BY id) - 1 AS hn FROM hosts),
			s AS (SELECT *, row_number() OVER (ORDER BY id) AS rn FROM alerts WHERE id NOT LIKE 'alr_clone%' LIMIT ?3)
			INSERT INTO alerts (id, host_id, host_name, issue_id, check_id, subject, category, severity, peak_severity, state, title, detail,
			value, observed_at, issue_started_at, opened_at, acknowledged_at, ack_by, ack_by_name, ack_note, resolved_at, suppressed, suppressed_by, ever_suppressed,
			notify_after, next_reminder_at, reminders_sent, updated_at)
			SELECT 'alr_clone' || printf('%08d', ?1 + rn), hid, host_name, issue_id, check_id, subject,
			category, severity, peak_severity, state, title, detail, value, observed_at - ((?1 + rn) * 28800000 / ?2), issue_started_at - ((?1 + rn) * 28800000 / ?2),
			opened_at - ((?1 + rn) * 28800000 / ?2), acknowledged_at, NULL, ack_by_name, ack_note, resolved_at - ((?1 + rn) * 28800000 / ?2), 0, NULL, 0, NULL, NULL, 0, updated_at
			FROM s JOIN h ON h.hn = ((?1 + s.rn) % ?2)`
		cloneTimeline := `INSERT INTO alert_events (alert_id, ts, kind, text, actor) SELECT 'alr_clone' || printf('%08d', ?1 + rn), ts - ((?1 + rn) * 28800000 / ?2), kind, text, actor
			FROM (SELECT id AS aid, row_number() OVER (ORDER BY id) AS rn FROM alerts WHERE id NOT LIKE 'alr_clone%' LIMIT ?3) s JOIN alert_events ev ON ev.alert_id = s.aid`
		cloneDeliveries := `INSERT INTO notify_deliveries (id, dedupe_key, alert_id, destination_id, destination_name, destination_kind, kind, recovery_wanted, message_json,
			status, attempts, next_attempt_at, created_at, finished_at)
			SELECT 'ndel_clone' || printf('%08d', ?1 + rn) || '_' || d.id, 'clone' || printf('%08d', ?1 + rn) || d.dedupe_key, 'alr_clone' || printf('%08d', ?1 + rn),
			d.destination_id, d.destination_name, d.destination_kind, d.kind, d.recovery_wanted, d.message_json, d.status, d.attempts, d.next_attempt_at,
			d.created_at - ((?1 + rn) * 28800000 / ?2), d.finished_at - ((?1 + rn) * 28800000 / ?2)
			FROM (SELECT id AS aid, row_number() OVER (ORDER BY id) AS rn FROM alerts WHERE id NOT LIKE 'alr_clone%' LIMIT ?3) s JOIN notify_deliveries d ON d.alert_id = s.aid`
		for made := 0; made < target; {
			batch := min(target-made, sample)
			for _, q := range []string{cloneAlerts, cloneTimeline, cloneDeliveries} {
				if _, err := e.h.st.Writer.Exec(q, made, n, batch); err != nil {
					t.Fatal(err)
				}
			}
			made += batch
		}
		// Steady state: delivery details are kept 30 days, alerts 180.
		e.a.Prune(ctx)
		var total, kept int
		e.h.st.Reader.QueryRow(`SELECT count(*) FROM alerts`).Scan(&total)
		e.h.st.Reader.QueryRow(`SELECT count(*) FROM notify_deliveries`).Scan(&kept)
		db := size(e)
		measure := func(name string, f func()) time.Duration {
			start := time.Now()
			for range 5 {
				f()
			}
			d := time.Since(start) / 5
			_ = name
			return d
		}
		hist := measure("history", func() { e.a.List(ctx, alerts.Filter{View: "resolved", Limit: 50}) })
		histHost := measure("history by host", func() { e.a.List(ctx, alerts.Filter{View: "resolved", HostID: hosts[0], Limit: 50}) })
		deep := measure("history page 20", func() {
			cur := ""
			for range 20 {
				p, _ := e.a.List(ctx, alerts.Filter{View: "all", Limit: 50, Cursor: cur})
				cur = p.NextCursor
			}
		}) / 20
		active := measure("active", func() { e.a.List(ctx, alerts.Filter{View: "active"}) })
		sum := measure("summary", func() { e.a.GetSummary(ctx) })
		det := measure("detail", func() { e.a.GetDetail(ctx, "alr_clone00000001", true) })
		t.Logf("%3d hosts: %6d alerts (180 days × 3/host/day), %d deliveries (30 days) in %v; database %.1f MB (%.2f MB per host); history page %v, by host %v, deep page %v, active %v, summary %v, detail %v",
			n, total, kept, time.Since(fill).Round(time.Millisecond), db/1e6, db/1e6/float64(n), hist.Round(time.Microsecond), histHost.Round(time.Microsecond),
			deep.Round(time.Microsecond), active.Round(time.Microsecond), sum.Round(time.Microsecond), det.Round(time.Microsecond))
	}
	// A prune past retention (everything older than 180 days goes).
	e.clk.add(181 * 24 * time.Hour)
	var longest time.Duration
	probe, stopProbe := context.WithCancel(ctx)
	probed := make(chan struct{})
	go func() { // how long does a writer wait while the prune runs?
		defer close(probed)
		for probe.Err() == nil {
			s := time.Now()
			e.h.st.Writer.Exec(`UPDATE settings SET updated_at = updated_at WHERE key = 'none'`)
			longest = max(longest, time.Since(s))
			time.Sleep(5 * time.Millisecond)
		}
	}()
	pr := e.a.Prune(ctx)
	stopProbe()
	<-probed
	t.Logf("prune after 181 days: %d alerts, %d deliveries, %d events removed in %d ms; longest wait for another writer meanwhile %v",
		pr.Alerts, pr.Deliveries, pr.Events, pr.Millis, longest.Round(time.Millisecond))
	if longest > time.Second {
		t.Fatalf("prune blocked other writers for %v", longest)
	}

	// --- burst: 200 hosts fail at once ---
	b := newAlertEnv(t)
	b.dest("chat", "webhook", recv.URL+"/a", "recommended")
	b.dest("mail-ish", "webhook", recv.URL+"/b", "recommended")
	var bh []string
	for i := range 200 {
		bh = append(bh, b.host(fmt.Sprintf("b-%03d", i)))
	}
	start := time.Now()
	for _, h := range bh {
		b.issue(h, "stale", "", "warning", "No monitoring data")
	}
	open := time.Since(start)
	peak.Store(0)
	slow.Store(true)
	svc := alerts.New(alerts.Deps{Store: b.h.st, Box: b.h.box, Log: b.h.srv.d.Logs.App, Sender: testSender, Config: alerts.Config{RatePerMinute: 100000}})
	svc.SetClock(b.clk.now)
	runCtx, stop := context.WithCancel(ctx)
	go svc.Run(runCtx)
	deadline := time.Now().Add(2 * time.Minute)
	var left int
	for time.Now().Before(deadline) {
		b.h.st.Reader.QueryRow(`SELECT count(*) FROM notify_deliveries WHERE status IN ('queued','sending')`).Scan(&left)
		if left == 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	stop()
	slow.Store(false)
	var delivered int
	b.h.st.Reader.QueryRow(`SELECT count(*) FROM notify_deliveries WHERE status = 'delivered'`).Scan(&delivered)
	t.Logf("burst: 200 alerts opened in %v; %d notifications delivered by 4 workers in %v (receiver peak concurrency %d, at most 2 per destination × 2 destinations); %d left",
		open.Round(time.Millisecond), delivered, time.Since(start).Round(time.Millisecond), peak.Load(), left)
	if peak.Load() > 4 || left != 0 || delivered != 400 {
		t.Fatalf("burst: peak %d, left %d, delivered %d", peak.Load(), left, delivered)
	}
	// The default rate limit for a chat destination: 30 a minute after a burst of 16.
	r := newAlertEnv(t)
	r.dest("discord", "discord", recv.URL+"/api/webhooks/1/x", "recommended")
	rs := alerts.New(alerts.Deps{Store: r.h.st, Box: r.h.box, Log: r.h.srv.d.Logs.App, Sender: testSender, Config: alerts.Config{DisableWorkers: true}})
	rs.SetClock(r.clk.now)
	rh := r.host("rate")
	for i := range 40 {
		r.issue(rh, "service_failed", fmt.Sprintf("r%d.service", i), "warning", "failed")
	}
	first := rs.ProcessOutbox(ctx, 4)
	r.clk.add(time.Minute)
	second := rs.ProcessOutbox(ctx, 4)
	t.Logf("rate limit (Discord/Slack default 30/min): %d sent at once, %d more a minute later, the rest wait in the queue", first, second)
	if first > 17 || second > 31 || first+second >= 40 {
		t.Fatalf("rate limit not applied: %d, %d", first, second)
	}

	// --- flapping: M12 adds one alert per activation, nothing more ---
	f := newAlertEnv(t)
	f.dest("chat", "webhook", recv.URL+"/a", "recommended")
	fh := f.host("flappy")
	fb := size(f)
	for range 500 {
		f.issue(fh, "interface_down", "eth0", "warning", "Network interface eth0 is down")
		f.deliver()
		f.clk.add(time.Minute)
		f.recover(fh, "interface_down", "eth0")
		f.deliver()
		f.clk.add(time.Minute)
	}
	var fa, fd int
	f.h.st.Reader.QueryRow(`SELECT count(*) FROM alerts`).Scan(&fa)
	f.h.st.Reader.QueryRow(`SELECT count(*) FROM notify_deliveries`).Scan(&fd)
	t.Logf("flapping: 500 activations → %d alerts, %d notifications (1 opening + 1 recovery each), %.0f KB (%.1f KB per activation)",
		fa, fd, (size(f)-fb)/1e3, (size(f)-fb)/1e3/500)

	// --- outage: destinations refuse connections; monitoring is unaffected ---
	o := newAlertEnv(t)
	dead := httptest.NewServer(http.NotFoundHandler())
	deadURL := dead.URL
	dead.Close()
	o.dest("down", "webhook", deadURL, "recommended")
	oh := o.host("busy")
	var wg sync.WaitGroup
	octx, ostop := context.WithCancel(ctx)
	wg.Add(1)
	go func() {
		defer wg.Done()
		for octx.Err() == nil {
			o.a.ProcessOutbox(octx, 4)
			o.tick(10 * time.Second)
		}
	}()
	ingest := time.Now()
	o.h.mkUser("x", auth.RoleViewer)
	for i := range 100 {
		o.issue(oh, "service_failed", fmt.Sprintf("o%d.service", i), "warning", "failed")
	}
	o.cpu(oh, 99, 6)
	took := time.Since(ingest)
	ostop()
	wg.Wait()
	var failed, queued int
	o.h.st.Reader.QueryRow(`SELECT count(*) FROM notify_deliveries WHERE status = 'failed'`).Scan(&failed)
	o.h.st.Reader.QueryRow(`SELECT count(*) FROM notify_deliveries WHERE status = 'queued'`).Scan(&queued)
	t.Logf("outage: 101 alerts and 6 metric minutes recorded in %v while every delivery failed (%d failed after 8 attempts, %d still retrying)", took.Round(time.Millisecond), failed, queued)
}
