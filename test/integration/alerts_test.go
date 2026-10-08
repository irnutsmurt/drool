//go:build integration

package integration

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestAlertsControllerCrash kills the real controller process in the
// middle of delivering a notification (M12) and starts it again:
//
//   - the receiver hasn't taken the notification yet: after the restart
//     it is delivered, once;
//   - the receiver took it but the answer never came back: after the
//     restart it is delivered again with the same delivery id (at least
//     once), the delivery is marked ambiguous, and nothing about the alert
//     changes.
//
// The hosts are monitoring fixtures (crash-01, crash-02: a full root
// filesystem) and the receiver is local: nothing outside this machine is
// involved.
func TestAlertsControllerCrash(t *testing.T) {
	const (
		hangBefore = iota // hold the request before reading it
		normal
		hangAfter // read and record it, then never answer
	)
	var mode atomic.Int32
	var mu sync.Mutex
	type got struct{ Delivery, Event, Host string }
	var recv []got
	var arrived atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		arrived.Add(1)
		m := mode.Load()
		// Read the body first: the server notices a closed connection
		// (the controller killed) only after that.
		b, _ := io.ReadAll(r.Body)
		if m == hangBefore {
			select {
			case <-r.Context().Done():
			case <-time.After(3 * time.Minute):
			}
			return
		}
		var p struct {
			DeliveryID string `json:"delivery_id"`
			Event      string `json:"event"`
			Alert      struct {
				Host struct{ Name string } `json:"host"`
			} `json:"alert"`
		}
		json.Unmarshal(b, &p)
		mu.Lock()
		recv = append(recv, got{p.DeliveryID, p.Event, p.Alert.Host.Name})
		mu.Unlock()
		if m == hangAfter {
			select {
			case <-r.Context().Done():
			case <-time.After(3 * time.Minute):
			}
			return
		}
		w.WriteHeader(204)
	}))
	defer srv.Close()
	of := func(host string) []got {
		mu.Lock()
		defer mu.Unlock()
		var out []got
		for _, g := range recv {
			if g.Host == host {
				out = append(out, g)
			}
		}
		return out
	}
	var dest struct{ ID string }
	ctl.must(t, "POST", "/api/v1/notifications/destinations", map[string]any{"name": "crash-test-hook", "kind": "webhook",
		"config": map[string]any{"timeout_s": 30}, "secrets": map[string]any{"url": srv.URL + "/hook"}}, &dest)
	defer ctl.do("DELETE", "/api/v1/notifications/destinations/"+dest.ID, nil, nil)

	type alertList struct {
		Items []struct {
			ID, State string
		} `json:"items"`
	}
	type delivery struct {
		Items []struct {
			ID, Status string
			Attempts   int
			Ambiguous  bool
			AttemptLog []struct{ Outcome string } `json:"attempt_log"`
		} `json:"items"`
	}
	restart := func() {
		t.Helper()
		ctl.crash()
		mode.Store(normal) // the receiver behaves from now on
		if err := ctl.start(); err != nil {
			t.Fatal(err)
		}
		if err := ctl.login(); err != nil {
			t.Fatal(err)
		}
	}
	scenario := func(host string, first int32) (string, alertList) {
		t.Helper()
		mode.Store(first)
		before := arrived.Load()
		var h struct{ ID string }
		ctl.must(t, "POST", "/api/v1/hosts", map[string]any{"name": host, "address": "192.0.2.40", "port": 22, "user": "deploy", "become_method": "none"}, &h)
		// Other tests share this controller: leave no hosts (or their alerts) behind.
		t.Cleanup(func() { ctl.do("DELETE", "/api/v1/hosts/"+h.ID, nil, nil) })
		var a alertList
		waitFor(t, 3*time.Minute, host+": alert opened", func() bool {
			ctl.must(t, "GET", "/api/v1/alerts?view=active&host_id="+h.ID, nil, &a)
			return len(a.Items) == 1
		})
		waitFor(t, time.Minute, host+": delivery in progress", func() bool { return arrived.Load() > before })
		restart() // SIGKILL in the middle of the delivery
		return h.ID, a
	}

	// 1. Killed before the receiver took the notification.
	h1, a1 := scenario("crash-01", hangBefore)
	waitFor(t, 2*time.Minute, "crash-01: delivered after the restart", func() bool { return len(of("crash-01")) == 1 })
	var d delivery
	ctl.must(t, "GET", "/api/v1/notifications/deliveries?alert_id="+a1.Items[0].ID, nil, &d)
	if len(d.Items) != 1 || d.Items[0].Status != "delivered" || d.Items[0].Attempts != 2 || d.Items[0].AttemptLog[0].Outcome != "interrupted" {
		t.Fatalf("crash-01 delivery: %+v", d.Items)
	}
	time.Sleep(5 * time.Second)
	if n := len(of("crash-01")); n != 1 {
		t.Fatalf("crash-01: %d notifications, want exactly 1", n)
	}

	// 2. Killed after the receiver took it: sent again, same delivery id.
	h2, a2 := scenario("crash-02", hangAfter)
	waitFor(t, 2*time.Minute, "crash-02: delivered again after the restart", func() bool { return len(of("crash-02")) == 2 })
	g := of("crash-02")
	if g[0].Delivery == "" || g[0].Delivery != g[1].Delivery || g[0].Event != "opened" {
		t.Fatalf("crash-02: the duplicate must carry the same delivery id: %+v", g)
	}
	ctl.must(t, "GET", "/api/v1/notifications/deliveries?alert_id="+a2.Items[0].ID, nil, &d)
	if len(d.Items) != 1 || d.Items[0].Status != "delivered" || !d.Items[0].Ambiguous {
		t.Fatalf("crash-02 delivery: %+v", d.Items)
	}
	// The alerts are untouched: one each, still active, same ids.
	for _, c := range []struct {
		host, id string
	}{{h1, a1.Items[0].ID}, {h2, a2.Items[0].ID}} {
		var a alertList
		ctl.must(t, "GET", "/api/v1/alerts?view=all&host_id="+c.host, nil, &a)
		if len(a.Items) != 1 || a.Items[0].ID != c.id || a.Items[0].State != "active" {
			t.Fatalf("alerts after the crashes: %+v", a.Items)
		}
	}
	t.Logf("crash before acceptance: delivered once after restart; crash after acceptance: delivered twice with delivery id %s (marked ambiguous)", g[0].Delivery)
}
