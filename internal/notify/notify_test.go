package notify

import (
	"bufio"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// --- a local fake SMTP server (never a real one) ---

type fakeSMTP struct {
	t        *testing.T
	ln       net.Listener
	tlsCfg   *tls.Config
	implicit bool // TLS from the first byte
	starttls bool // offer STARTTLS
	user     string
	pass     string
	hang     bool // accept and never answer
	rcptCode int  // non-zero: answer RCPT with this
	dataCode int  // non-zero: answer the end of DATA with this

	mu     sync.Mutex
	msgs   []string
	authed []string
	tlsOn  []bool
}

func newCert(t *testing.T) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "fake smtp"}, NotBefore: time.Now().Add(-time.Hour),
		NotAfter: time.Now().Add(time.Hour), IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, DNSNames: []string{"localhost"},
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, IsCA: true, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, pool
}

func startSMTP(t *testing.T, f *fakeSMTP) (host string, port int) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f.t, f.ln = t, ln
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go f.serve(c)
		}
	}()
	a := ln.Addr().(*net.TCPAddr)
	return "127.0.0.1", a.Port
}

func (f *fakeSMTP) serve(c net.Conn) {
	defer c.Close()
	if f.hang {
		io.Copy(io.Discard, c)
		return
	}
	isTLS := false
	if f.implicit {
		tc := tls.Server(c, f.tlsCfg)
		if tc.Handshake() != nil {
			return
		}
		c, isTLS = tc, true
	}
	r := bufio.NewReader(c)
	w := func(s string) { fmt.Fprint(c, s+"\r\n") }
	w("220 fake ESMTP")
	user := ""
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		cmd := strings.ToUpper(strings.TrimSpace(line))
		switch {
		case strings.HasPrefix(cmd, "EHLO"), strings.HasPrefix(cmd, "HELO"):
			ext := []string{"250-fake"}
			if f.starttls && !isTLS {
				ext = append(ext, "250-STARTTLS")
			}
			if f.user != "" {
				ext = append(ext, "250-AUTH PLAIN")
			}
			ext = append(ext, "250 8BITMIME")
			w(strings.Join(ext, "\r\n"))
		case cmd == "STARTTLS":
			w("220 go ahead")
			tc := tls.Server(c, f.tlsCfg)
			if tc.Handshake() != nil {
				return
			}
			c, isTLS = tc, true
			r = bufio.NewReader(c)
		case strings.HasPrefix(cmd, "AUTH PLAIN"):
			b, _ := base64.StdEncoding.DecodeString(strings.TrimSpace(line[len("AUTH PLAIN"):]))
			p := strings.Split(string(b), "\x00")
			if len(p) == 3 && p[1] == f.user && p[2] == f.pass {
				user = p[1]
				w("235 ok")
			} else {
				w("535 5.7.8 Authentication credentials invalid")
			}
		case strings.HasPrefix(cmd, "MAIL"):
			w("250 ok")
		case strings.HasPrefix(cmd, "RCPT"):
			if f.rcptCode != 0 {
				w(fmt.Sprintf("%d no such user", f.rcptCode))
				continue
			}
			w("250 ok")
		case cmd == "DATA":
			w("354 go")
			var msg strings.Builder
			for {
				l, err := r.ReadString('\n')
				if err != nil {
					return
				}
				if l == ".\r\n" {
					break
				}
				msg.WriteString(l)
			}
			if f.dataCode != 0 {
				w(fmt.Sprintf("%d try later", f.dataCode))
				continue
			}
			f.mu.Lock()
			f.msgs, f.authed, f.tlsOn = append(f.msgs, msg.String()), append(f.authed, user), append(f.tlsOn, isTLS)
			f.mu.Unlock()
			w("250 queued")
		case cmd == "QUIT":
			w("221 bye")
			return
		default:
			w("502 unknown")
		}
	}
}

func testMsg() Message {
	obs := time.Date(2026, 10, 8, 2, 14, 0, 0, time.UTC)
	v := 3.0
	return Message{Kind: MsgOpening, EventID: "alr_1:opening", AlertID: "alr_1", Severity: "critical", HostID: "host_1", Host: "storage01",
		Title: "nvme0n1 (Samsung 990 PRO): new media errors", Detail: "media errors 2 → 3", Check: "media_errors", Category: "storage",
		Value: &v, ValueText: "3", ObservedAt: &obs, OpenedAt: obs, URL: "https://drool.lan/alerts/alr_1", TimeZone: "UTC"}
}

func TestEmail(t *testing.T) {
	cert, pool := newCert(t)
	tlsCfg := &tls.Config{Certificates: []tls.Certificate{cert}}
	s := &Sender{RootCAs: pool, HeloName: "drool.test"}
	email := func(host string, port int, sec, user string) Config {
		c := Config{Host: host, Port: port, Security: sec, Username: user, From: "DROOL <drool@example.lan>", To: []string{"ops@example.lan", "rob@example.lan"}, TimeoutS: 3}
		if _, err := Normalize(KindEmail, &c, &Secrets{Password: map[bool]string{true: "pw", false: ""}[user != ""]}); err != nil {
			t.Fatal(err)
		}
		return c
	}

	t.Run("STARTTLS with authentication", func(t *testing.T) {
		f := &fakeSMTP{tlsCfg: tlsCfg, starttls: true, user: "drool", pass: "s3cret"}
		h, p := startSMTP(t, f)
		r := s.Send(t.Context(), KindEmail, email(h, p, SecurityStartTLS, "drool"), Secrets{Password: "s3cret"}, testMsg(), "ndel_1")
		if !r.OK() {
			t.Fatalf("%+v", r)
		}
		if len(f.msgs) != 1 || f.authed[0] != "drool" || !f.tlsOn[0] {
			t.Fatalf("message, auth and TLS: %v %v %v", len(f.msgs), f.authed, f.tlsOn)
		}
		m := f.msgs[0]
		for _, want := range []string{"Subject: =?utf-8?q?CRITICAL:_storage01", "Message-ID: <ndel_1@drool.test>", "X-DROOL-Event: alr_1:opening",
			"Auto-Submitted: auto-generated", "First detected: 2026-10-08 02:14 UTC", "Current value:", "Open in DROOL: https://drool.lan/alerts/alr_1"} {
			if !strings.Contains(m, want) {
				t.Errorf("message lacks %q:\n%s", want, m)
			}
		}
	})
	t.Run("implicit TLS", func(t *testing.T) {
		f := &fakeSMTP{tlsCfg: tlsCfg, implicit: true}
		h, p := startSMTP(t, f)
		if r := s.Send(t.Context(), KindEmail, email(h, p, SecurityTLS, ""), Secrets{}, testMsg(), "ndel_2"); !r.OK() || !f.tlsOn[0] {
			t.Fatalf("%+v", r)
		}
	})
	t.Run("STARTTLS required, never downgraded", func(t *testing.T) {
		f := &fakeSMTP{tlsCfg: tlsCfg}
		h, p := startSMTP(t, f)
		r := s.Send(t.Context(), KindEmail, email(h, p, SecurityStartTLS, ""), Secrets{}, testMsg(), "")
		if r.OK() || r.Category != CategoryConfig || r.Outcome != OutcomePermanent || len(f.msgs) != 0 {
			t.Fatalf("%+v", r)
		}
	})
	t.Run("certificate error", func(t *testing.T) {
		f := &fakeSMTP{tlsCfg: tlsCfg, starttls: true}
		h, p := startSMTP(t, f)
		r := (&Sender{}).Send(t.Context(), KindEmail, email(h, p, SecurityStartTLS, ""), Secrets{}, testMsg(), "")
		if r.Category != CategoryTLS || r.Outcome != OutcomePermanent || !strings.Contains(r.Message, "unknown authority") {
			t.Fatalf("%+v", r)
		}
	})
	t.Run("authentication failure", func(t *testing.T) {
		f := &fakeSMTP{tlsCfg: tlsCfg, starttls: true, user: "drool", pass: "right"}
		h, p := startSMTP(t, f)
		r := s.Send(t.Context(), KindEmail, email(h, p, SecurityStartTLS, "drool"), Secrets{Password: "wrong-password-xyz"}, testMsg(), "")
		if r.Category != CategoryAuth || r.Outcome != OutcomePermanent || r.StatusCode != 535 {
			t.Fatalf("%+v", r)
		}
		if strings.Contains(r.Message, "wrong-password-xyz") {
			t.Fatal("password in the error")
		}
	})
	t.Run("timeout", func(t *testing.T) {
		f := &fakeSMTP{hang: true}
		h, p := startSMTP(t, f)
		c := email(h, p, SecurityStartTLS, "")
		c.TimeoutS = 1
		start := time.Now()
		r := s.Send(t.Context(), KindEmail, c, Secrets{}, testMsg(), "")
		if r.Category != CategoryTimeout || r.Outcome != OutcomeTransient || time.Since(start) > 3*time.Second {
			t.Fatalf("%+v after %v", r, time.Since(start))
		}
	})
	t.Run("server rejection and deferral", func(t *testing.T) {
		f := &fakeSMTP{tlsCfg: tlsCfg, starttls: true, rcptCode: 550}
		h, p := startSMTP(t, f)
		if r := s.Send(t.Context(), KindEmail, email(h, p, SecurityStartTLS, ""), Secrets{}, testMsg(), ""); r.Category != CategoryRejected || r.Outcome != OutcomePermanent {
			t.Fatalf("550: %+v", r)
		}
		f2 := &fakeSMTP{tlsCfg: tlsCfg, starttls: true, dataCode: 451}
		h, p = startSMTP(t, f2)
		if r := s.Send(t.Context(), KindEmail, email(h, p, SecurityStartTLS, ""), Secrets{}, testMsg(), ""); r.Category != CategoryServer || r.Outcome != OutcomeTransient {
			t.Fatalf("451: %+v", r)
		}
	})
	t.Run("connection refused", func(t *testing.T) {
		ln, _ := net.Listen("tcp", "127.0.0.1:0")
		port := ln.Addr().(*net.TCPAddr).Port
		ln.Close()
		r := s.Send(t.Context(), KindEmail, email("127.0.0.1", port, SecurityStartTLS, ""), Secrets{}, testMsg(), "")
		if r.Category != CategoryNetwork || r.Outcome != OutcomeTransient || !strings.Contains(r.Message, "refused") {
			t.Fatalf("%+v", r)
		}
	})
	t.Run("plaintext refused unless confirmed, and never with a password", func(t *testing.T) {
		c := Config{Host: "relay.lan", Security: SecurityNone, From: "a@b.c", To: []string{"x@y.z"}}
		if _, err := Normalize(KindEmail, &c, &Secrets{}); err == nil {
			t.Fatal("unencrypted SMTP accepted without confirmation")
		}
		c.AllowPlaintext = true
		w, err := Normalize(KindEmail, &c, &Secrets{})
		if err != nil || len(w) == 0 || c.Port != 25 {
			t.Fatalf("confirmed plaintext: %v %v %d", w, err, c.Port)
		}
		c.Username = "u"
		if _, err := Normalize(KindEmail, &c, &Secrets{Password: "p"}); err == nil || !strings.Contains(err.Error(), "never sends an SMTP password") {
			t.Fatalf("password over plaintext: %v", err)
		}
		f := &fakeSMTP{}
		h, p := startSMTP(t, f)
		pc := Config{Host: h, Port: p, Security: SecurityNone, AllowPlaintext: true, From: "a@b.c", To: []string{"x@y.z"}}
		if r := s.Send(t.Context(), KindEmail, pc, Secrets{}, testMsg(), ""); !r.OK() || f.tlsOn[0] {
			t.Fatalf("plaintext relay: %+v", r)
		}
	})
	t.Run("validation", func(t *testing.T) {
		for _, c := range []Config{{Host: "", From: "a@b.c", To: []string{"x@y.z"}}, {Host: "h", From: "nope", To: []string{"x@y.z"}},
			{Host: "h", From: "a@b.c"}, {Host: "h", From: "a@b.c", To: []string{"bad"}}, {Host: "h", Port: 70000, From: "a@b.c", To: []string{"x@y.z"}}} {
			if _, err := Normalize(KindEmail, &c, &Secrets{}); err == nil {
				t.Errorf("accepted %+v", c)
			}
		}
		c := Config{Host: "h", From: "a@b.c", To: []string{"x@y.z"}, Username: "u"}
		if _, err := Normalize(KindEmail, &c, &Secrets{}); err == nil {
			t.Error("username without password accepted")
		}
	})
	t.Run("header injection", func(t *testing.T) {
		m := testMsg()
		m.Host = "evil\r\nBcc: victim@example.com"
		b := string(buildEmail(Config{From: "a@b.c", To: []string{"x@y.z"}}, m, "id", "h"))
		hdr, _, _ := strings.Cut(b, "\r\n\r\n")
		if strings.Contains(hdr, "\r\nBcc:") {
			t.Fatalf("header injected:\n%s", hdr)
		}
	})
}

// --- HTTP providers ---

type recorder struct {
	mu   sync.Mutex
	reqs []*http.Request
	body []string
}

func (r *recorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.reqs)
}

func (r *recorder) add(req *http.Request) string {
	b, _ := io.ReadAll(req.Body)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reqs, r.body = append(r.reqs, req), append(r.body, string(b))
	return string(b)
}

func tlsServer(t *testing.T, h http.HandlerFunc) (*httptest.Server, *Sender) {
	srv := httptest.NewTLSServer(h)
	t.Cleanup(srv.Close)
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	return srv, &Sender{RootCAs: pool}
}

func TestWebhook(t *testing.T) {
	cfg := func(headerName string) Config {
		c := Config{HeaderName: headerName, TimeoutS: 2}
		return c
	}
	t.Run("success, payload schema and idempotency headers", func(t *testing.T) {
		var rec recorder
		srv, s := tlsServer(t, func(w http.ResponseWriter, r *http.Request) { rec.add(r); w.WriteHeader(204) })
		sec := Secrets{URL: srv.URL + "/hook/tok3n-in-path?key=abcdefghijk", HeaderValue: "Bearer sekrit-token"}
		c := cfg("")
		if _, err := Normalize(KindWebhook, &c, &sec); err != nil || c.HeaderName != "Authorization" || c.URLDisplay != srv.URL+"/…" {
			t.Fatalf("%v %+v", err, c)
		}
		r := s.Send(t.Context(), KindWebhook, c, sec, testMsg(), "ndel_42")
		if !r.OK() || r.StatusCode != 204 {
			t.Fatalf("%+v", r)
		}
		req := rec.reqs[0]
		if req.Header.Get("Authorization") != "Bearer sekrit-token" || req.Header.Get("Idempotency-Key") != "ndel_42" ||
			req.Header.Get("X-DROOL-Delivery") != "ndel_42" || req.Header.Get("X-DROOL-Event") != "alr_1:opening" || req.Method != "POST" {
			t.Fatalf("headers: %v", req.Header)
		}
		var p WebhookPayload
		if err := json.Unmarshal([]byte(rec.body[0]), &p); err != nil {
			t.Fatal(err)
		}
		if p.Schema != "drool.notification.v1" || p.Event != "opened" || p.DeliveryID != "ndel_42" || p.EventID != "alr_1:opening" ||
			p.Alert == nil || p.Alert.Severity != "critical" || p.Alert.Host.Name != "storage01" || p.Alert.State != "active" || p.Test {
			t.Fatalf("payload: %s", rec.body[0])
		}
	})
	type tc struct {
		name     string
		status   int
		header   map[string]string
		outcome  string
		category string
	}
	for _, c := range []tc{
		{"200", 200, nil, OutcomeDelivered, CategoryOK},
		{"400", 400, nil, OutcomePermanent, CategoryRejected},
		{"401", 401, nil, OutcomePermanent, CategoryAuth},
		{"403", 403, nil, OutcomePermanent, CategoryAuth},
		{"404", 404, nil, OutcomePermanent, CategoryRejected},
		{"429", 429, map[string]string{"Retry-After": "120"}, OutcomeTransient, CategoryRateLimited},
		{"500", 500, nil, OutcomeTransient, CategoryServer},
		{"503", 503, nil, OutcomeTransient, CategoryServer},
		{"304", 304, nil, OutcomePermanent, CategoryConfig},
	} {
		t.Run("status "+c.name, func(t *testing.T) {
			srv, s := tlsServer(t, func(w http.ResponseWriter, r *http.Request) {
				for k, v := range c.header {
					w.Header().Set(k, v)
				}
				w.WriteHeader(c.status)
				fmt.Fprint(w, "receiver says no")
			})
			r := s.Send(t.Context(), KindWebhook, cfg(""), Secrets{URL: srv.URL}, testMsg(), "d")
			if r.Outcome != c.outcome || r.Category != c.category || r.StatusCode != c.status {
				t.Fatalf("%+v", r)
			}
			if c.status == 429 && r.RetryAfter != 2*time.Minute {
				t.Fatalf("Retry-After: %v", r.RetryAfter)
			}
		})
	}
	t.Run("timeout after the request was sent is ambiguous", func(t *testing.T) {
		var rec recorder
		srv, s := tlsServer(t, func(w http.ResponseWriter, r *http.Request) { rec.add(r); time.Sleep(3 * time.Second) })
		c := cfg("")
		c.TimeoutS = 1
		start := time.Now()
		r := s.Send(t.Context(), KindWebhook, c, Secrets{URL: srv.URL}, testMsg(), "d")
		if r.Category != CategoryTimeout || r.Outcome != OutcomeTransient || !r.Ambiguous || time.Since(start) > 2500*time.Millisecond {
			t.Fatalf("%+v after %v", r, time.Since(start))
		}
		if rec.count() != 1 {
			t.Fatal("the receiver should have got the request")
		}
	})
	t.Run("connection refused is not ambiguous", func(t *testing.T) {
		ln, _ := net.Listen("tcp", "127.0.0.1:0")
		addr := ln.Addr().String()
		ln.Close()
		r := (&Sender{}).Send(t.Context(), KindWebhook, cfg(""), Secrets{URL: "http://" + addr + "/x"}, testMsg(), "d")
		if r.Category != CategoryNetwork || r.Outcome != OutcomeTransient || r.Ambiguous || !strings.Contains(r.Message, "refused") {
			t.Fatalf("%+v", r)
		}
	})
	t.Run("response size is bounded", func(t *testing.T) {
		srv, s := tlsServer(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(200)
			chunk := strings.Repeat("x", 64<<10)
			for i := 0; i < 1024; i++ { // 64 MiB if anyone read it all
				if _, err := io.WriteString(w, chunk); err != nil {
					return
				}
			}
		})
		start := time.Now()
		r := s.Send(t.Context(), KindWebhook, cfg(""), Secrets{URL: srv.URL}, testMsg(), "d")
		if !r.OK() || time.Since(start) > 2*time.Second {
			t.Fatalf("%+v after %v", r, time.Since(start))
		}
	})
	t.Run("certificate error", func(t *testing.T) {
		srv, _ := tlsServer(t, func(w http.ResponseWriter, r *http.Request) {})
		r := (&Sender{}).Send(t.Context(), KindWebhook, cfg(""), Secrets{URL: srv.URL}, testMsg(), "d")
		if r.Category != CategoryTLS || r.Outcome != OutcomePermanent {
			t.Fatalf("%+v", r)
		}
	})
	t.Run("redirects", func(t *testing.T) {
		var rec recorder
		target, s := tlsServer(t, func(w http.ResponseWriter, r *http.Request) { rec.add(r); w.WriteHeader(200) })
		// Same-scheme redirect to another host (port): followed, but without the secret header.
		mux := http.NewServeMux()
		mux.HandleFunc("/same", func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, target.URL+"/final", http.StatusTemporaryRedirect)
		})
		mux.HandleFunc("/ftp", func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "ftp://example.com/x", http.StatusTemporaryRedirect)
		})
		mux.HandleFunc("/down", func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "http://127.0.0.1:1/x", http.StatusTemporaryRedirect)
		})
		mux.HandleFunc("/loop", func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "/loop", http.StatusTemporaryRedirect)
		})
		front := httptest.NewUnstartedServer(mux)
		front.TLS = &tls.Config{Certificates: target.TLS.Certificates}
		front.StartTLS()
		t.Cleanup(front.Close)
		c := cfg("X-Token")
		r := s.Send(t.Context(), KindWebhook, c, Secrets{URL: front.URL + "/same", HeaderValue: "tok"}, testMsg(), "d")
		if !r.OK() || len(rec.reqs) != 1 || rec.reqs[0].Header.Get("X-Token") != "" || rec.body[0] == "" {
			t.Fatalf("same-scheme redirect: %+v %v", r, rec.reqs)
		}
		for _, p := range []string{"/ftp", "/down", "/loop"} {
			r := s.Send(t.Context(), KindWebhook, c, Secrets{URL: front.URL + p}, testMsg(), "d")
			if r.Outcome != OutcomePermanent || r.Category != CategoryConfig {
				t.Fatalf("%s: %+v", p, r)
			}
		}
	})
	t.Run("private and loopback addresses are allowed", func(t *testing.T) {
		for _, u := range []string{"http://192.168.1.10:8123/api/webhook/x", "http://10.0.0.5/hook", "http://localhost:9000/x", "https://[fd00::1]/x"} {
			c := cfg("")
			if _, err := Normalize(KindWebhook, &c, &Secrets{URL: u}); err != nil {
				t.Errorf("%s refused: %v", u, err)
			}
		}
	})
	t.Run("invalid URLs", func(t *testing.T) {
		for _, u := range []string{"", "ftp://x/y", "file:///etc/passwd", "https://user:pw@host/x", "https:///x", "not a url", "https://h/x#frag", "gopher://h"} {
			c := cfg("")
			if _, err := Normalize(KindWebhook, &c, &Secrets{URL: u}); err == nil {
				t.Errorf("%q accepted", u)
			}
		}
		c := cfg("Bad Header")
		if _, err := Normalize(KindWebhook, &c, &Secrets{URL: "https://h/x", HeaderValue: "v"}); err == nil {
			t.Error("bad header name accepted")
		}
		c = cfg("X")
		if _, err := Normalize(KindWebhook, &c, &Secrets{URL: "https://h/x", HeaderValue: "a\r\nInjected: 1"}); err == nil {
			t.Error("header value with CRLF accepted")
		}
	})
	t.Run("secrets never appear in errors", func(t *testing.T) {
		ln, _ := net.Listen("tcp", "127.0.0.1:0")
		addr := ln.Addr().String()
		ln.Close()
		sec := Secrets{URL: "http://" + addr + "/api/hooks/SUPERSECRETTOKEN123?k=QUERYSECRET456", HeaderValue: "Bearer HEADERSECRET789"}
		r := (&Sender{}).Send(t.Context(), KindWebhook, Config{HeaderName: "Authorization"}, sec, testMsg(), "d")
		for _, s := range []string{"SUPERSECRETTOKEN123", "QUERYSECRET456", "HEADERSECRET789"} {
			if strings.Contains(r.Message, s) {
				t.Fatalf("secret %s in %q", s, r.Message)
			}
		}
		srv, s := tlsServer(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(400)
			fmt.Fprint(w, "bad token "+r.Header.Get("Authorization")+" at "+r.URL.Path)
		})
		sec.URL = srv.URL + "/api/hooks/SUPERSECRETTOKEN123"
		r = s.Send(t.Context(), KindWebhook, Config{HeaderName: "Authorization"}, sec, testMsg(), "d")
		if strings.Contains(r.Message, "HEADERSECRET789") || strings.Contains(r.Message, "SUPERSECRETTOKEN123") || !strings.Contains(r.Message, "[redacted]") {
			t.Fatalf("echoed secrets: %q", r.Message)
		}
	})
}

func TestChatPayloads(t *testing.T) {
	t.Run("discord", func(t *testing.T) {
		var rec recorder
		srv, s := tlsServer(t, func(w http.ResponseWriter, r *http.Request) { rec.add(r); w.WriteHeader(204) })
		sec := Secrets{URL: srv.URL + "/api/webhooks/123/abc"}
		c := Config{}
		if _, err := Normalize(KindDiscord, &c, &sec); err != nil {
			t.Fatal(err)
		}
		m := testMsg()
		m.Host = "@everyone"
		if r := s.Send(t.Context(), KindDiscord, c, sec, m, "d"); !r.OK() {
			t.Fatalf("%+v", r)
		}
		var p struct {
			Embeds []struct {
				Title, Description, URL string
				Color                   int
			}
			AllowedMentions struct{ Parse []string } `json:"allowed_mentions"`
		}
		json.Unmarshal([]byte(rec.body[0]), &p)
		if len(p.Embeds) != 1 || p.Embeds[0].Title != "CRITICAL: @everyone" || p.Embeds[0].Color != 0xd32f2f || p.AllowedMentions.Parse == nil ||
			len(p.AllowedMentions.Parse) != 0 || !strings.Contains(p.Embeds[0].Description, "First detected") || p.Embeds[0].URL == "" {
			t.Fatalf("discord payload: %s", rec.body[0])
		}
		rec2 := recorder{}
		srv2, s2 := tlsServer(t, func(w http.ResponseWriter, r *http.Request) {
			rec2.add(r)
			w.WriteHeader(404)
			fmt.Fprint(w, `{"message": "Unknown Webhook", "code": 10015}`)
		})
		r := s2.Send(t.Context(), KindDiscord, c, Secrets{URL: srv2.URL + "/api/webhooks/1/x"}, testMsg(), "d")
		if r.Outcome != OutcomePermanent || !strings.Contains(r.Message, "Unknown Webhook") {
			t.Fatalf("deleted webhook: %+v", r)
		}
		if _, err := Normalize(KindDiscord, &Config{}, &Secrets{URL: "https://example.com/not-a-webhook"}); err == nil {
			t.Error("non-webhook Discord URL accepted")
		}
	})
	t.Run("slack", func(t *testing.T) {
		var rec recorder
		srv, s := tlsServer(t, func(w http.ResponseWriter, r *http.Request) { rec.add(r); fmt.Fprint(w, "ok") })
		sec := Secrets{URL: srv.URL + "/services/T0/B0/xyz"}
		c := Config{}
		if _, err := Normalize(KindSlack, &c, &sec); err != nil {
			t.Fatal(err)
		}
		m := testMsg()
		m.Kind, m.Title = MsgRecovery, "jellyfin.service failed <script>"
		d := int64(462)
		m.DurationS = &d
		if r := s.Send(t.Context(), KindSlack, c, sec, m, "d"); !r.OK() {
			t.Fatalf("%+v", r)
		}
		var p struct{ Text string }
		json.Unmarshal([]byte(rec.body[0]), &p)
		if !strings.HasPrefix(p.Text, "*RECOVERED: storage01*") || !strings.Contains(p.Text, "Duration: 7m 42s") || !strings.Contains(p.Text, "&lt;script&gt;") {
			t.Fatalf("slack payload: %q", p.Text)
		}
		srv2, s2 := tlsServer(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(403); fmt.Fprint(w, "invalid_token") })
		r := s2.Send(t.Context(), KindSlack, c, Secrets{URL: srv2.URL + "/services/x"}, testMsg(), "d")
		if r.Category != CategoryAuth || r.Outcome != OutcomePermanent || !strings.Contains(r.Message, "invalid_token") {
			t.Fatalf("revoked Slack webhook: %+v", r)
		}
	})
	t.Run("test notification", func(t *testing.T) {
		m := Message{Kind: MsgTest, EventID: "test:1", OpenedAt: time.Now()}
		if Headline(m) != "TEST: DROOL notification test" || !strings.Contains(Text(m), "No alert was raised") {
			t.Fatalf("%q %q", Headline(m), Text(m))
		}
		var p WebhookPayload
		b, _ := json.Marshal(webhookBody(m, ""))
		json.Unmarshal(b, &p)
		if !p.Test || p.Alert != nil || p.Event != "test" {
			t.Fatalf("test payload: %s", b)
		}
	})
}

func TestFormat(t *testing.T) {
	for d, want := range map[time.Duration]string{42 * time.Second: "42s", 7*time.Minute + 42*time.Second: "7m 42s", 2*time.Hour + 5*time.Minute: "2h 5m", 76 * time.Hour: "3d 4h"} {
		if got := Duration(d); got != want {
			t.Errorf("%v: %q", d, got)
		}
	}
	m := testMsg()
	m.AckBy, m.AckNote = "rob", "Replacing failed disk this evening"
	txt := Text(m)
	for _, want := range []string{"nvme0n1 (Samsung 990 PRO): new media errors", "First detected: 2026-10-08 02:14 UTC", "Acknowledged by rob: Replacing failed disk"} {
		if !strings.Contains(txt, want) {
			t.Errorf("%q lacks %q", txt, want)
		}
	}
}

// A configuration-only close (M12.1) is never worded as a recovery, on any
// destination.
func TestClearedIsNotARecovery(t *testing.T) {
	d := int64(3600)
	for why, head := range map[string]string{"disabled": "CLEARED BY CONFIGURATION: storage01", "reconfigured": "CLEARED BY CONFIGURATION: storage01",
		"gone": "CLOSED, NO LONGER REPORTED: storage01"} {
		m := testMsg()
		m.Kind, m.Resolution, m.ResolutionNote, m.DurationS = MsgCleared, why, "High CPU utilization was turned off (Custom for this host).", &d
		if Headline(m) != head {
			t.Errorf("%s: %q", why, Headline(m))
		}
		txt := Text(m)
		if strings.Contains(strings.ToLower(txt+Headline(m)), "recovered") || !strings.Contains(txt, "This is not a recovery") ||
			!strings.Contains(txt, "Why: High CPU utilization was turned off") || !strings.Contains(txt, "Was open for: 1h 0m") {
			t.Errorf("%s: %q", why, txt)
		}
		var p WebhookPayload
		b, _ := json.Marshal(webhookBody(m, "ndel_1"))
		json.Unmarshal(b, &p)
		if p.Event != "cleared" || p.Alert.State != "resolved" || p.Alert.Resolution != why || p.Alert.ResolutionNote == "" {
			t.Errorf("%s webhook: %s", why, b)
		}
		if c := discordBody(m)["embeds"].([]any)[0].(map[string]any)["color"]; c != 0x546e7a {
			t.Errorf("%s: a cleared Discord embed isn't green: %v", why, c)
		}
		if s := slackBody(m)["text"].(string); !strings.HasPrefix(s, "*"+head+"*") {
			t.Errorf("%s slack: %q", why, s)
		}
		subj, body := Headline(m), Text(m)
		if strings.Contains(subj, "RECOVERED") || strings.Contains(body, "Recovered:") {
			t.Errorf("%s email: %q %q", why, subj, body)
		}
	}
	r := testMsg()
	r.Kind = MsgRecovery
	var p WebhookPayload
	b, _ := json.Marshal(webhookBody(r, "ndel_2"))
	json.Unmarshal(b, &p)
	if p.Event != "recovered" || p.Alert.Resolution != "recovered" {
		t.Errorf("a real recovery: %s", b)
	}
}
