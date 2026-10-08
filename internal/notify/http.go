package notify

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"drool/internal/version"
)

// Sender delivers notifications. The zero value is ready to use.
type Sender struct {
	// RootCAs replaces the system roots (tests).
	RootCAs *x509.CertPool
	// HeloName is the name DROOL gives SMTP servers (default: the host name).
	HeloName string

	once      sync.Once
	transport *http.Transport
}

const (
	dialTimeout      = 5 * time.Second
	handshakeTimeout = 5 * time.Second
	maxRedirects     = 3
)

func (s *Sender) tlsConfig(serverName string) *tls.Config {
	return &tls.Config{RootCAs: s.RootCAs, ServerName: serverName, MinVersion: tls.VersionTLS12}
}

func (s *Sender) httpTransport() *http.Transport {
	s.once.Do(func() {
		s.transport = &http.Transport{
			Proxy:                  http.ProxyFromEnvironment,
			DialContext:            (&net.Dialer{Timeout: dialTimeout, KeepAlive: 30 * time.Second}).DialContext,
			TLSClientConfig:        s.tlsConfig(""),
			TLSHandshakeTimeout:    handshakeTimeout,
			MaxIdleConns:           16,
			MaxConnsPerHost:        4,
			IdleConnTimeout:        60 * time.Second,
			MaxResponseHeaderBytes: 16 << 10,
			ForceAttemptHTTP2:      true,
		}
	})
	return s.transport
}

// Send delivers m to a destination. deliveryID identifies this delivery
// (the same across retries) so receivers can deduplicate.
func (s *Sender) Send(ctx context.Context, kind string, c Config, sec Secrets, m Message, deliveryID string) Result {
	start := time.Now()
	ctx, cancel := context.WithTimeout(ctx, c.Timeout())
	defer cancel()
	var r Result
	switch kind {
	case KindEmail:
		r = s.sendEmail(ctx, c, sec, m, deliveryID)
	case KindWebhook:
		body, _ := json.Marshal(webhookBody(m, deliveryID))
		r = s.post(ctx, c, sec, body, m, deliveryID)
	case KindDiscord:
		body, _ := json.Marshal(discordBody(m))
		r = s.post(ctx, c, sec, body, m, deliveryID)
	case KindSlack:
		body, _ := json.Marshal(slackBody(m))
		r = s.post(ctx, c, sec, body, m, deliveryID)
	default:
		r = fail(OutcomePermanent, CategoryConfig, "unknown destination kind")
	}
	r.Message = redact(r.Message, sec.values())
	r.DurationMs = time.Since(start).Milliseconds()
	return r
}

var errRedirect = errors.New("redirect")

func (s *Sender) post(ctx context.Context, c Config, sec Secrets, body []byte, m Message, deliveryID string) Result {
	u, err := checkURL(sec.URL)
	if err != nil {
		return fail(OutcomePermanent, CategoryConfig, err.Error())
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(body))
	if err != nil {
		return fail(OutcomePermanent, CategoryConfig, "the URL is not valid")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "DROOL/"+version.Version)
	req.Header.Set("X-DROOL-Event", m.EventID)
	if deliveryID != "" {
		req.Header.Set("X-DROOL-Delivery", deliveryID)
		req.Header.Set("Idempotency-Key", deliveryID)
	}
	if c.HeaderName != "" && sec.HeaderValue != "" {
		req.Header.Set(c.HeaderName, sec.HeaderValue)
	}
	var wrote bool
	req = req.WithContext(httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{
		WroteRequest: func(i httptrace.WroteRequestInfo) { wrote = i.Err == nil },
	}))
	origin := u
	client := &http.Client{Transport: s.httpTransport(), CheckRedirect: func(r *http.Request, via []*http.Request) error {
		// Follow a few redirects, only to http(s), never from https down
		// to http, and never carry the secret header to another host.
		if len(via) >= maxRedirects || r.URL.Scheme != "http" && r.URL.Scheme != "https" ||
			origin.Scheme == "https" && r.URL.Scheme == "http" {
			return errRedirect
		}
		if r.URL.Host != origin.Host && c.HeaderName != "" {
			r.Header.Del(c.HeaderName)
		}
		return nil
	}}
	resp, err := client.Do(req)
	if err != nil {
		r := classifyNetErr(err)
		r.Ambiguous = wrote && r.Category == CategoryTimeout
		return r
	}
	defer resp.Body.Close()
	snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	io.Copy(io.Discard, io.LimitReader(resp.Body, MaxResponseBody)) // let the connection be reused, but never read more
	return classifyStatus(resp, strings.TrimSpace(string(snippet)))
}

func classifyStatus(resp *http.Response, snippet string) Result {
	code := resp.StatusCode
	r := Result{StatusCode: code}
	detail := ""
	if snippet != "" {
		detail = ": " + oneLine(clip(snippet, 200))
	}
	switch {
	case code >= 200 && code < 300:
		r.Outcome, r.Category, r.Message = OutcomeDelivered, CategoryOK, fmt.Sprintf("delivered (HTTP %d)", code)
	case code == 401 || code == 403:
		r.Outcome, r.Category, r.Message = OutcomePermanent, CategoryAuth, fmt.Sprintf("the receiver refused the credentials (HTTP %d)%s", code, detail)
	case code == 408:
		r.Outcome, r.Category, r.Message = OutcomeTransient, CategoryTimeout, "the receiver timed out (HTTP 408)"
	case code == 429:
		r.Outcome, r.Category, r.Message = OutcomeTransient, CategoryRateLimited, "rate limited by the receiver (HTTP 429)"
		r.RetryAfter = retryAfter(resp.Header.Get("Retry-After"))
	case code == 425 || code >= 500:
		r.Outcome, r.Category, r.Message = OutcomeTransient, CategoryServer, fmt.Sprintf("the receiver failed (HTTP %d)%s", code, detail)
		r.RetryAfter = retryAfter(resp.Header.Get("Retry-After"))
	case code >= 300 && code < 400:
		r.Outcome, r.Category, r.Message = OutcomePermanent, CategoryConfig, fmt.Sprintf("unexpected redirect (HTTP %d); use the final URL", code)
	default:
		r.Outcome, r.Category, r.Message = OutcomePermanent, CategoryRejected, fmt.Sprintf("the receiver rejected the notification (HTTP %d)%s", code, detail)
	}
	return r
}

func retryAfter(v string) time.Duration {
	if v == "" {
		return 0
	}
	if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n >= 0 {
		return min(time.Duration(n)*time.Second, time.Hour)
	}
	if t, err := http.ParseTime(v); err == nil {
		return min(max(time.Until(t), 0), time.Hour)
	}
	return 0
}

// classifyNetErr turns a transport error into a result without the URL
// (url.Error repeats it, and webhook URLs are secrets).
func classifyNetErr(err error) Result {
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	var (
		dnsErr   *net.DNSError
		unknown  x509.UnknownAuthorityError
		hostErr  x509.HostnameError
		invalid  x509.CertificateInvalidError
		verify   *tls.CertificateVerificationError
		recHdr   tls.RecordHeaderError
		netErr   net.Error
		opErr    *net.OpError
		alertErr tls.AlertError
	)
	switch {
	case errors.Is(err, errRedirect):
		return fail(OutcomePermanent, CategoryConfig, "the receiver redirected to an unsupported location (another scheme, https to http, or too many redirects)")
	case errors.As(err, &unknown), errors.As(err, &hostErr), errors.As(err, &invalid), errors.As(err, &verify):
		return fail(OutcomePermanent, CategoryTLS, "TLS certificate error: "+tlsReason(err))
	case errors.As(err, &recHdr):
		return fail(OutcomePermanent, CategoryTLS, "TLS handshake failed: the server doesn't speak TLS on this port")
	case errors.As(err, &alertErr):
		return fail(OutcomePermanent, CategoryTLS, "TLS handshake failed: "+err.Error())
	case errors.As(err, &dnsErr):
		if dnsErr.IsNotFound {
			return fail(OutcomeTransient, CategoryNetwork, "host name not found ("+dnsErr.Name+")")
		}
		if dnsErr.IsTimeout {
			return fail(OutcomeTransient, CategoryTimeout, "DNS lookup timed out")
		}
		return fail(OutcomeTransient, CategoryNetwork, "DNS lookup failed")
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &netErr) && netErr.Timeout():
		return fail(OutcomeTransient, CategoryTimeout, "timed out")
	case errors.Is(err, context.Canceled):
		return fail(OutcomeTransient, CategoryNetwork, "interrupted")
	case errors.As(err, &opErr) && opErr.Op == "dial":
		msg := "could not connect"
		if strings.Contains(err.Error(), "refused") {
			msg = "connection refused"
		} else if strings.Contains(err.Error(), "unreachable") {
			msg = "network unreachable"
		}
		return fail(OutcomeTransient, CategoryNetwork, msg)
	}
	msg := err.Error()
	if strings.Contains(msg, "server gave HTTP response to HTTPS client") {
		return fail(OutcomePermanent, CategoryTLS, "TLS handshake failed: the server answered plain HTTP (use http://, or enable TLS on the server)")
	}
	if strings.Contains(msg, "certificate") {
		return fail(OutcomePermanent, CategoryTLS, "TLS certificate error: "+msg)
	}
	return fail(OutcomeTransient, CategoryNetwork, "connection failed: "+msg)
}

func tlsReason(err error) string {
	var (
		unknown x509.UnknownAuthorityError
		hostErr x509.HostnameError
		invalid x509.CertificateInvalidError
	)
	switch {
	case errors.As(err, &unknown):
		return "signed by an unknown authority (install a trusted certificate on the server, or add its CA to the controller's trust store)"
	case errors.As(err, &hostErr):
		return "the certificate doesn't match the host name"
	case errors.As(err, &invalid):
		if invalid.Reason == x509.Expired {
			return "the certificate has expired or is not yet valid"
		}
	}
	return err.Error()
}

// --- bodies ---

// WebhookPayload is the generic webhook's JSON body (schema
// "drool.notification.v1", documented in the README).
type WebhookPayload struct {
	Schema     string        `json:"schema"`
	EventID    string        `json:"event_id"`
	DeliveryID string        `json:"delivery_id"`
	Event      string        `json:"event"` // opened | escalated | reminder | recovered | test
	SentAt     time.Time     `json:"sent_at"`
	Test       bool          `json:"test"`
	Summary    string        `json:"summary"`
	Text       string        `json:"text"`
	Alert      *WebhookAlert `json:"alert,omitempty"`
}

// WebhookAlert describes the alert in a webhook payload.
type WebhookAlert struct {
	ID          string      `json:"id"`
	Fingerprint string      `json:"fingerprint"`
	State       string      `json:"state"` // active | acknowledged | resolved
	Severity    string      `json:"severity"`
	Host        WebhookHost `json:"host"`
	Check       string      `json:"check"`
	Category    string      `json:"category"`
	Subject     string      `json:"subject,omitempty"`
	Title       string      `json:"title"`
	Detail      string      `json:"detail,omitempty"`
	Value       *float64    `json:"value,omitempty"`
	ValueText   string      `json:"value_text,omitempty"`
	ObservedAt  *time.Time  `json:"observed_at,omitempty"`
	OpenedAt    time.Time   `json:"opened_at"`
	ResolvedAt  *time.Time  `json:"resolved_at,omitempty"`
	DurationS   *int64      `json:"duration_s,omitempty"`
	Acknowledge *WebhookAck `json:"acknowledgement,omitempty"`
	URL         string      `json:"url,omitempty"`
}

type WebhookHost struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type WebhookAck struct {
	By   string     `json:"by"`
	At   *time.Time `json:"at,omitempty"`
	Note string     `json:"note,omitempty"`
}

var eventNames = map[string]string{MsgOpening: "opened", MsgEscalation: "escalated", MsgReminder: "reminder", MsgRecovery: "recovered", MsgTest: "test"}

func webhookBody(m Message, deliveryID string) WebhookPayload {
	p := WebhookPayload{Schema: "drool.notification.v1", EventID: m.EventID, DeliveryID: deliveryID, Event: eventNames[m.Kind],
		SentAt: time.Now().UTC().Truncate(time.Second), Test: m.Kind == MsgTest, Summary: Headline(m), Text: strings.Join(Lines(m), "\n")}
	if m.Kind == MsgTest {
		return p
	}
	a := &WebhookAlert{ID: m.AlertID, Fingerprint: m.Fingerprint, State: "active", Severity: m.Severity, Host: WebhookHost{m.HostID, m.Host},
		Check: m.Check, Category: m.Category, Subject: m.Subject, Title: m.Title, Detail: m.Detail, Value: m.Value, ValueText: m.ValueText,
		ObservedAt: m.ObservedAt, OpenedAt: m.OpenedAt, ResolvedAt: m.ResolvedAt, DurationS: m.DurationS, URL: m.URL}
	if m.AckBy != "" {
		a.State = "acknowledged"
		a.Acknowledge = &WebhookAck{By: m.AckBy, At: m.AckAt, Note: m.AckNote}
	}
	if m.Kind == MsgRecovery {
		a.State = "resolved"
	}
	p.Alert = a
	return p
}

// Discord: one embed, no mentions (host and service names must never ping
// anyone).
func discordBody(m Message) map[string]any {
	color := map[string]int{"critical": 0xd32f2f, "warning": 0xf9a825}[m.Severity]
	if m.Kind == MsgRecovery {
		color = 0x2e7d32
	}
	if m.Kind == MsgTest {
		color = 0x546e7a
	}
	lines := Lines(m)
	embed := map[string]any{"title": clip(oneLine(Headline(m)), 250), "description": clip(strings.Join(lines, "\n"), 3500), "color": color}
	if m.URL != "" {
		embed["url"] = m.URL
	}
	return map[string]any{"username": "DROOL", "embeds": []any{embed}, "allowed_mentions": map[string]any{"parse": []string{}}}
}

// Slack: mrkdwn text with Slack's three escapes.
func slackBody(m Message) map[string]any {
	esc := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
	var b strings.Builder
	b.WriteString("*" + esc.Replace(oneLine(Headline(m))) + "*")
	for _, l := range Lines(m) {
		b.WriteString("\n" + esc.Replace(l))
	}
	return map[string]any{"text": clip(b.String(), 3500), "unfurl_links": false, "unfurl_media": false}
}

func hostName() string {
	if h, err := os.Hostname(); err == nil && h != "" && !strings.ContainsAny(h, " \r\n") {
		return h
	}
	return "localhost"
}
