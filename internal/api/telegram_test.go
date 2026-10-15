package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"drool/internal/alerts"
)

// fakeTelegram stands in for the Telegram Bot API in every API test.
var fakeTelegram = func() *fakeTG {
	f := &fakeTG{status: 200, reply: `{"ok":true,"result":{"message_id":1}}`}
	f.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		defer f.mu.Unlock()
		f.paths, f.bodies = append(f.paths, r.URL.Path), append(f.bodies, body)
		w.WriteHeader(f.status)
		fmt.Fprint(w, strings.ReplaceAll(f.reply, "{path}", r.URL.Path))
	}))
	return f
}()

type fakeTG struct {
	srv    *httptest.Server
	mu     sync.Mutex
	status int
	reply  string // {path} is replaced by the request's path
	paths  []string
	bodies []map[string]any
}

func (f *fakeTG) answer(status int, reply string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.status, f.reply = status, reply
}

func (f *fakeTG) reset() {
	f.answer(200, `{"ok":true,"result":{"message_id":1}}`)
	f.mu.Lock()
	f.paths, f.bodies = nil, nil
	f.mu.Unlock()
}

func (f *fakeTG) sent() ([]string, []map[string]any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.paths...), append([]map[string]any(nil), f.bodies...)
}

// TestTelegramDestination (final After-M17 batch): a Telegram destination
// from setup to delivery. The bot token is write-only and sealed; it never
// comes back from the API, never reaches the audit log, delivery history or
// test results, even when Telegram's answer repeats the request; the chat
// and topic are plain settings.
func TestTelegramDestination(t *testing.T) {
	e := newAlertEnv(t)
	fakeTelegram.reset()
	t.Cleanup(fakeTelegram.reset)
	const token = "7012345678:example-only-not-a-real-bot-token-111111"
	secretPart := strings.SplitN(token, ":", 2)[1]
	leak := func(where, text string) {
		t.Helper()
		if strings.Contains(text, secretPart) || strings.Contains(text, "telegram.org") {
			t.Fatalf("%s leaks the bot token: %s", where, text)
		}
	}

	// A malformed token is refused without being repeated.
	r := e.c.do("POST", "/api/v1/notifications/destinations", map[string]any{"name": "tg", "kind": "telegram",
		"config": map[string]any{"chat_id": "-1001234567890"}, "secrets": map[string]any{"bot_token": "7012345678:" + secretPart[:12]}})
	if r.status != 422 || strings.Contains(string(r.body), secretPart[:12]) {
		t.Fatalf("malformed token: %d %s", r.status, r.body)
	}
	var d alerts.Destination
	r = e.c.do("POST", "/api/v1/notifications/destinations", map[string]any{"name": "Ops chat", "kind": "telegram",
		"config": map[string]any{"chat_id": "-1001234567890", "thread_id": 7}, "secrets": map[string]any{"bot_token": token}})
	r.json(t, &d)
	if r.status != 201 || !d.SecretsSet.BotToken || d.Config.ChatID != "-1001234567890" || d.Config.ThreadID != 7 || d.Config.URLDisplay != "" {
		t.Fatalf("create: %d %s", r.status, r.body)
	}
	leak("create", string(r.body))
	// Renaming keeps the token.
	r = e.c.do("PATCH", "/api/v1/notifications/destinations/"+d.ID, map[string]any{"name": "Ops"})
	if r.status != 200 {
		t.Fatalf("rename: %d %s", r.status, r.body)
	}
	leak("update", string(r.body))
	for _, path := range []string{"/api/v1/notifications/destinations", "/api/v1/notifications/destinations/" + d.ID} {
		leak(path, string(e.c.do("GET", path, nil).body))
	}

	// The test notification goes to the chat's topic, with the token only
	// in the request path.
	var tr alerts.TestResult
	e.c.do("POST", "/api/v1/notifications/destinations/"+d.ID+"/test", nil).json(t, &tr)
	paths, bodies := fakeTelegram.sent()
	if !tr.OK() || len(paths) != 1 || paths[0] != "/bot"+token+"/sendMessage" || bodies[0]["chat_id"] != -1001234567890.0 ||
		bodies[0]["message_thread_id"] != 7.0 || !strings.Contains(bodies[0]["text"].(string), "TEST: DROOL notification test") {
		t.Fatalf("test: %+v %v %v", tr, paths, bodies)
	}
	// Unsaved (the setup form's Send test).
	r = e.c.do("POST", "/api/v1/notifications/test", map[string]any{"kind": "telegram", "config": map[string]any{"chat_id": "@drool_alerts"},
		"secrets": map[string]any{"bot_token": token}})
	r.json(t, &tr)
	if !tr.OK() {
		t.Fatalf("unsaved test: %s", r.body)
	}
	leak("unsaved test", string(r.body))

	// Telegram refuses, repeating the request: the answer is scrubbed in
	// the test result and in what DROOL keeps of it.
	fakeTelegram.answer(401, `{"ok":false,"error_code":401,"description":"Unauthorized: {path}"}`)
	r = e.c.do("POST", "/api/v1/notifications/destinations/"+d.ID+"/test", nil)
	r.json(t, &tr)
	if tr.OK() || tr.Category != "auth" || !strings.Contains(tr.Message, "[redacted]") {
		t.Fatalf("refused test: %s", r.body)
	}
	leak("refused test", string(r.body))
	leak("last test", string(e.c.do("GET", "/api/v1/notifications/destinations/"+d.ID, nil).body))

	// Alert deliveries: delivered; rate limited (retried after Telegram's
	// retry_after); then a permanent failure, scrubbed in the history.
	fakeTelegram.answer(200, `{"ok":true}`)
	h := e.host("nas1")
	e.issue(h, "service_failed", "smbd.service", "critical", "smbd.service failed")
	e.deliver()
	if paths, _ := fakeTelegram.sent(); len(paths) != 4 {
		t.Fatalf("delivery: %v", paths)
	}
	fakeTelegram.answer(429, `{"ok":false,"error_code":429,"description":"Too Many Requests: retry after 40","parameters":{"retry_after":40}}`)
	e.issue(h, "service_failed", "nmbd.service", "critical", "nmbd.service failed")
	e.deliver()
	var q struct{ Items []alerts.Delivery }
	e.c.do("GET", "/api/v1/notifications/deliveries?status=queued", nil).json(t, &q)
	if len(q.Items) != 1 || q.Items[0].NextAttemptAt == nil || *q.Items[0].NextAttemptAt < e.clk.now().Add(40*time.Second).UnixMilli() {
		t.Fatalf("429: %+v", q.Items)
	}
	fakeTelegram.answer(400, `{"ok":false,"error_code":400,"description":"Bad Request: chat not found ({path})"}`)
	e.clk.add(time.Minute)
	e.deliver()
	r = e.c.do("GET", "/api/v1/notifications/deliveries", nil)
	if !strings.Contains(string(r.body), "check the chat ID") {
		t.Fatalf("history: %s", r.body)
	}
	leak("delivery history", string(r.body))
	leak("destinations", string(e.c.do("GET", "/api/v1/notifications/destinations", nil).body))
	leak("events", string(e.c.do("GET", "/api/v1/events", nil).body))

	// The worker's log: a delivery failing with Telegram repeating the
	// request, processed by a service that logs everything.
	var logs bytes.Buffer
	logged := alerts.New(alerts.Deps{Store: e.h.st, Box: e.h.box, Audit: e.h.srv.d.Audit, Sender: testSender,
		Log:    slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
		Config: alerts.Config{DisableWorkers: true, BackoffBase: time.Second, BackoffMax: 4 * time.Second, RatePerMinute: 6000}})
	logged.SetClock(e.clk.now)
	fakeTelegram.answer(401, `{"ok":false,"error_code":401,"description":"Unauthorized {path}"}`)
	e.issue(h, "service_failed", "winbindd.service", "critical", "winbindd.service failed")
	logged.RecoverLeases(context.Background(), true)
	before, _ := fakeTelegram.sent()
	if n := logged.ProcessOutbox(context.Background(), 4); n == 0 {
		t.Fatal("the logging service delivered nothing")
	}
	if after, _ := fakeTelegram.sent(); len(after) <= len(before) {
		t.Fatal("nothing reached the fake Bot API")
	}
	leak("log", logs.String())

	// Sealed at rest: the stored secret is ciphertext.
	var sealed []byte
	e.h.st.Reader.QueryRow(`SELECT secret_ciphertext FROM notify_destinations WHERE id = ?`, d.ID).Scan(&sealed)
	if len(sealed) == 0 || bytes.Contains(sealed, []byte(secretPart)) {
		t.Fatal("the bot token isn't sealed at rest")
	}

	// Nowhere in the database but the sealed secret.
	var audit, deliveries, dests string
	e.h.st.Reader.QueryRow(`SELECT ifnull(group_concat(details_json, ' '), '') FROM audit_events`).Scan(&audit)
	e.h.st.Reader.QueryRow(`SELECT ifnull(group_concat(ifnull(last_error, ''), ' '), '') FROM notify_deliveries`).Scan(&deliveries)
	e.h.st.Reader.QueryRow(`SELECT group_concat(config_json || ifnull(last_error, '') || ifnull(last_test_result, ''), ' ') FROM notify_destinations`).Scan(&dests)
	leak("audit log", audit)
	leak("deliveries table", deliveries)
	leak("destinations table", dests)
	if !strings.Contains(audit, "notification.destination") && !strings.Contains(audit, "Ops") {
		t.Fatalf("no audit of the destination: %s", audit)
	}
}
