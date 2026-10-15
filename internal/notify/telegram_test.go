package notify

import (
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// A made-up token in the real format; tests never reach Telegram.
const testBotToken = "7012345678:example-only-not-a-real-bot-token-000000"

func telegramServer(t *testing.T, h http.HandlerFunc) (*httptest.Server, *Sender) {
	srv, s := tlsServer(t, h)
	s.TelegramAPI = srv.URL
	return srv, s
}

// noToken fails if any form of the token is in text.
func noToken(t *testing.T, what, text string) {
	t.Helper()
	for _, f := range append(tokenForms(testBotToken), "telegram.org/bot") {
		if strings.Contains(text, f) {
			t.Fatalf("%s contains the bot token (%q): %s", what, f, text)
		}
	}
}

func noTokenResult(t *testing.T, r Result) {
	t.Helper()
	b, _ := json.Marshal(r)
	noToken(t, "message", r.Message)
	noToken(t, "result", string(b))
}

func TestTelegram(t *testing.T) {
	cfg := Config{ChatID: "-1001234567890", ThreadID: 42, TimeoutS: 2}
	sec := Secrets{BotToken: testBotToken}

	t.Run("configuration: the token is a secret, and no URL is shown", func(t *testing.T) {
		c := Config{ChatID: " -1001234567890 ", URLDisplay: "https://api.telegram.org/bot" + testBotToken + "/", HeaderName: "X", Host: "h", To: []string{"a@b"}}
		s := Secrets{BotToken: " " + testBotToken + "\n", URL: "https://example.com/x", Password: "p"}
		if _, err := Normalize(KindTelegram, &c, &s); err != nil {
			t.Fatal(err)
		}
		if c.ChatID != "-1001234567890" || c.URLDisplay != "" || c.HeaderName != "" || c.Host != "" || c.To != nil || s.URL != "" || s.Password != "" || s.BotToken != testBotToken {
			t.Fatalf("normalized: %+v %+v", c, s)
		}
		b, _ := json.Marshal(c)
		noToken(t, "configuration", string(b))
		if strings.Contains(string(b), "telegram.org") {
			t.Fatalf("a URL in the configuration: %s", b)
		}
		for _, ok := range []string{"@drool_alerts", "123456789", "-100987"} {
			c := Config{ChatID: ok}
			if _, err := Normalize(KindTelegram, &c, &Secrets{BotToken: testBotToken}); err != nil {
				t.Errorf("chat %q refused: %v", ok, err)
			}
		}
		for _, bad := range []string{"", "123", "abc:" + strings.Repeat("x", 35), "123456:short", "7012345678:example only not a real bot token 01",
			"7012345678:example-only/not-a-real-bot-token-0000", "https://api.telegram.org/bot" + testBotToken + "/"} {
			c := Config{ChatID: "-100123"}
			_, err := Normalize(KindTelegram, &c, &Secrets{BotToken: bad})
			if err == nil {
				t.Errorf("token %q accepted", bad)
				continue
			}
			if len(bad) > 8 && strings.Contains(err.Error(), strings.TrimSpace(bad)) {
				t.Errorf("the error repeats what was pasted: %v", err)
			}
		}
		for _, bad := range []string{"", "abc", "-100abc", "@ab", "1234567890123456789012", "@1channel", "-100 123"} {
			c := Config{ChatID: bad}
			if _, err := Normalize(KindTelegram, &c, &Secrets{BotToken: testBotToken}); err == nil {
				t.Errorf("chat %q accepted", bad)
			}
		}
		c = Config{ChatID: "-100123", ThreadID: -1}
		if _, err := Normalize(KindTelegram, &c, &Secrets{BotToken: testBotToken}); err == nil {
			t.Error("negative topic accepted")
		}
		// Other kinds drop Telegram's settings.
		c = Config{ChatID: "-100123", ThreadID: 4}
		s = Secrets{URL: "https://hooks.slack.com/services/T/B/x", BotToken: testBotToken}
		if _, err := Normalize(KindSlack, &c, &s); err != nil || c.ChatID != "" || c.ThreadID != 0 || s.BotToken != "" {
			t.Fatalf("slack kept Telegram settings: %v %+v %+v", err, c, s)
		}
	})

	t.Run("delivered: sendMessage to the chat and topic, conservative HTML, nobody mentioned", func(t *testing.T) {
		var rec recorder
		srv, s := telegramServer(t, func(w http.ResponseWriter, r *http.Request) {
			rec.add(r)
			fmt.Fprint(w, `{"ok":true,"result":{"message_id":7}}`)
		})
		m := testMsg()
		m.Host, m.Title = "@alice <b>&", "disk <full> & failing"
		r := s.Send(t.Context(), KindTelegram, cfg, sec, m, "d")
		if !r.OK() || r.StatusCode != 200 || r.Message != "delivered (Telegram)" {
			t.Fatalf("%+v", r)
		}
		req := rec.reqs[0]
		if req.Method != "POST" || req.URL.Path != "/bot"+testBotToken+"/sendMessage" || req.Header.Get("Content-Type") != "application/json" {
			t.Fatalf("request: %s %s %v", req.Method, req.URL.Path, req.Header)
		}
		var p struct {
			ChatID      json.Number `json:"chat_id"`
			Thread      int64       `json:"message_thread_id"`
			Text        string      `json:"text"`
			ParseMode   string      `json:"parse_mode"`
			LinkPreview struct {
				Disabled bool `json:"is_disabled"`
			} `json:"link_preview_options"`
		}
		d := json.NewDecoder(strings.NewReader(rec.body[0]))
		d.UseNumber()
		if err := d.Decode(&p); err != nil {
			t.Fatal(err)
		}
		if p.ChatID.String() != "-1001234567890" || p.Thread != 42 || p.ParseMode != "HTML" || !p.LinkPreview.Disabled {
			t.Fatalf("payload: %s", rec.body[0])
		}
		if !strings.HasPrefix(p.Text, "<b>CRITICAL: @\u2060alice &lt;b&gt;&amp;</b>\n") || !strings.Contains(p.Text, "disk &lt;full&gt; &amp; failing") ||
			!strings.Contains(p.Text, "First detected") || !strings.Contains(p.Text, "Open in DROOL: https://drool.lan/alerts/alr_1") {
			t.Fatalf("text: %q", p.Text)
		}
		_ = srv
		// A public channel by name; no topic.
		c := Config{ChatID: "@drool_alerts"}
		if r := s.Send(t.Context(), KindTelegram, c, sec, testMsg(), "d"); !r.OK() {
			t.Fatalf("%+v", r)
		}
		var p2 map[string]any
		json.Unmarshal([]byte(rec.body[1]), &p2)
		if p2["chat_id"] != "@drool_alerts" || p2["message_thread_id"] != nil {
			t.Fatalf("channel payload: %s", rec.body[1])
		}
	})

	t.Run("long text is cut before escaping: no broken entity, within Telegram's limit", func(t *testing.T) {
		var rec recorder
		_, s := telegramServer(t, func(w http.ResponseWriter, r *http.Request) { rec.add(r); fmt.Fprint(w, `{"ok":true}`) })
		m := testMsg()
		m.Title = strings.Repeat("<&>", 3000)
		if r := s.Send(t.Context(), KindTelegram, cfg, sec, m, "d"); !r.OK() {
			t.Fatalf("%+v", r)
		}
		var p struct{ Text string }
		json.Unmarshal([]byte(rec.body[0]), &p)
		body := strings.ReplaceAll(strings.ReplaceAll(p.Text, "<b>", ""), "</b>", "")
		if strings.ContainsAny(body, "<>") {
			t.Fatalf("an unescaped bracket: %.200q", body)
		}
		for i := 0; i < len(body); i++ {
			if body[i] == '&' && !strings.HasPrefix(body[i:], "&amp;") && !strings.HasPrefix(body[i:], "&lt;") && !strings.HasPrefix(body[i:], "&gt;") {
				t.Fatalf("a broken entity at %d: %.40q", i, body[i:])
			}
		}
		if n := len([]rune(html.UnescapeString(body))); n > 4096 {
			t.Fatalf("%d characters", n)
		}
	})

	type tc struct {
		name     string
		status   int
		header   map[string]string
		reply    string
		outcome  string
		category string
		retry    time.Duration
		says     string
	}
	for _, c := range []tc{
		{"rate limited, retry_after", 429, nil, `{"ok":false,"error_code":429,"description":"Too Many Requests: retry after 17","parameters":{"retry_after":17}}`,
			OutcomeTransient, CategoryRateLimited, 17 * time.Second, "retrying after 17s"},
		{"rate limited, Retry-After header", 429, map[string]string{"Retry-After": "30"}, `{"ok":false}`, OutcomeTransient, CategoryRateLimited, 30 * time.Second, "HTTP 429"},
		{"bad token", 401, nil, `{"ok":false,"error_code":401,"description":"Unauthorized"}`, OutcomePermanent, CategoryAuth, 0, "@BotFather"},
		{"unknown token", 404, nil, `{"ok":false,"error_code":404,"description":"Not Found"}`, OutcomePermanent, CategoryAuth, 0, "refused the bot token"},
		{"chat not found", 400, nil, `{"ok":false,"error_code":400,"description":"Bad Request: chat not found"}`, OutcomePermanent, CategoryRejected, 0, "check the chat ID"},
		{"topic not found", 400, nil, `{"ok":false,"error_code":400,"description":"Bad Request: message thread not found"}`, OutcomePermanent, CategoryRejected, 0, "topic"},
		{"group became a supergroup", 400, nil, `{"ok":false,"error_code":400,"description":"Bad Request: group chat was upgraded to a supergroup chat","parameters":{"migrate_to_chat_id":-1009876543210}}`,
			OutcomePermanent, CategoryConfig, 0, "-1009876543210"},
		{"not a member", 403, nil, `{"ok":false,"error_code":403,"description":"Forbidden: bot is not a member of the channel chat"}`, OutcomePermanent, CategoryRejected, 0, "add it to the group"},
		{"bad entities", 400, nil, `{"ok":false,"error_code":400,"description":"Bad Request: can't parse entities"}`, OutcomePermanent, CategoryRejected, 0, "can't parse entities"},
		{"server error", 500, nil, `{"ok":false,"error_code":500,"description":"Internal Server Error"}`, OutcomeTransient, CategoryServer, 0, "Telegram failed"},
		{"bad gateway, html", 502, map[string]string{"Retry-After": "5"}, `<html>bad gateway</html>`, OutcomeTransient, CategoryServer, 5 * time.Second, "HTTP 502"},
		{"200 but not ok", 200, nil, `{"ok":false}`, OutcomePermanent, CategoryRejected, 0, "rejected"},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, s := telegramServer(t, func(w http.ResponseWriter, r *http.Request) {
				for k, v := range c.header {
					w.Header().Set(k, v)
				}
				w.WriteHeader(c.status)
				fmt.Fprint(w, c.reply)
			})
			r := s.Send(t.Context(), KindTelegram, cfg, sec, testMsg(), "d")
			if r.Outcome != c.outcome || r.Category != c.category || r.StatusCode != c.status || r.RetryAfter != c.retry || !strings.Contains(r.Message, c.says) {
				t.Fatalf("%+v", r)
			}
			noTokenResult(t, r)
		})
	}

	// The request URL carries the token, so every way a request can fail is
	// checked for every form of it.
	t.Run("the bot token never appears in results or errors", func(t *testing.T) {
		// Connection refused.
		ln, _ := net.Listen("tcp", "127.0.0.1:0")
		addr := ln.Addr().String()
		ln.Close()
		r := (&Sender{TelegramAPI: "https://" + addr}).Send(t.Context(), KindTelegram, cfg, sec, testMsg(), "d")
		if r.Category != CategoryNetwork || r.Outcome != OutcomeTransient {
			t.Fatalf("refused: %+v", r)
		}
		noTokenResult(t, r)

		// An untrusted certificate.
		bad := httptest.NewTLSServer(http.NotFoundHandler())
		defer bad.Close()
		r = (&Sender{TelegramAPI: bad.URL}).Send(t.Context(), KindTelegram, cfg, sec, testMsg(), "d")
		if r.Category != CategoryTLS {
			t.Fatalf("tls: %+v", r)
		}
		noTokenResult(t, r)

		// Plain HTTP where TLS was expected.
		plain := httptest.NewServer(http.NotFoundHandler())
		defer plain.Close()
		r = (&Sender{TelegramAPI: strings.Replace(plain.URL, "http://", "https://", 1)}).Send(t.Context(), KindTelegram, cfg, sec, testMsg(), "d")
		if r.Category != CategoryTLS {
			t.Fatalf("plain http: %+v", r)
		}
		noTokenResult(t, r)

		// A timeout after the request was written: ambiguous.
		_, slow := telegramServer(t, func(w http.ResponseWriter, r *http.Request) { time.Sleep(1500 * time.Millisecond) })
		c := cfg
		c.TimeoutS = 1
		r = slow.Send(t.Context(), KindTelegram, c, sec, testMsg(), "d")
		if r.Category != CategoryTimeout || !r.Ambiguous {
			t.Fatalf("timeout: %+v", r)
		}
		noTokenResult(t, r)

		// A receiver that echoes the request back in its error.
		_, echo := telegramServer(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(400)
			b, _ := json.Marshal(map[string]any{"ok": false, "description": "Bad Request: " + r.URL.String() + " " + r.URL.EscapedPath() + " token " +
				strings.SplitN(testBotToken, ":", 2)[1] + " " + url.QueryEscape(testBotToken)})
			w.Write(b)
		})
		r = echo.Send(t.Context(), KindTelegram, cfg, sec, testMsg(), "d")
		if r.Outcome != OutcomePermanent || !strings.Contains(r.Message, "[redacted]") {
			t.Fatalf("echo: %+v", r)
		}
		noTokenResult(t, r)

		// A redirect is never followed (the token is in the path).
		var rec recorder
		_, redir := telegramServer(t, func(w http.ResponseWriter, r *http.Request) {
			rec.add(r)
			http.Redirect(w, r, "/bot"+testBotToken+"/elsewhere", http.StatusFound)
		})
		r = redir.Send(t.Context(), KindTelegram, cfg, sec, testMsg(), "d")
		if r.Category != CategoryConfig || rec.count() != 1 {
			t.Fatalf("redirect: %+v (%d requests)", r, rec.count())
		}
		noTokenResult(t, r)

		// Transport errors quote the URL; the cause is kept, the URL is not.
		u := "https://api.telegram.org/bot" + testBotToken + "/sendMessage"
		r = classifyNetErr(&url.Error{Op: "Post", URL: u, Err: errors.New("something odd")})
		noTokenResult(t, r)
		// And whatever slips through is scrubbed before it leaves Send.
		msg := redact("Post "+u+": "+url.QueryEscape(u)+" "+url.PathEscape(testBotToken), sec.values())
		noToken(t, "scrubbed", msg)
		if !strings.Contains(msg, "https://api.telegram.org/[redacted]/sendMessage") {
			t.Fatalf("scrubbed: %q", msg)
		}

		// A malformed stored token is refused before any request.
		r = (&Sender{TelegramAPI: "https://" + addr}).Send(t.Context(), KindTelegram, cfg, Secrets{BotToken: "123:" + strings.Repeat("y", 10)}, testMsg(), "d")
		if r.Category != CategoryConfig {
			t.Fatalf("malformed: %+v", r)
		}
	})
}
