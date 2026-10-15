// Package notify delivers alert notifications (M12): email over SMTP,
// generic JSON webhooks, Discord and Slack incoming webhooks, and Telegram
// bots (final After-M17 batch). It knows
// nothing about alert state: callers hand it a Message and a destination,
// and it reports how the attempt ended, classified so the outbox can tell
// a misconfiguration (permanent) from an outage (transient).
//
// Every send is bounded: connection, DNS, TLS handshake and the whole
// exchange share a deadline, and response bodies are read only up to a
// small limit. Errors never contain the destination's secrets.
package notify

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/mail"
	"net/url"
	"slices"
	"strings"
	"time"
)

// Destination kinds.
const (
	KindEmail    = "email"
	KindWebhook  = "webhook"
	KindDiscord  = "discord"
	KindSlack    = "slack"
	KindTelegram = "telegram"
)

// Kinds lists the built-in providers.
var Kinds = []string{KindEmail, KindWebhook, KindDiscord, KindSlack, KindTelegram}

// Email security modes.
const (
	SecurityStartTLS = "starttls" // plain connection upgraded with STARTTLS (port 587); refused if the server doesn't offer it
	SecurityTLS      = "tls"      // TLS from the first byte (port 465)
	SecurityNone     = "none"     // unencrypted: only with AllowPlaintext, never with a password
)

// Limits.
const (
	DefaultTimeout  = 10 * time.Second
	MaxTimeout      = 30 * time.Second
	MaxResponseBody = 64 << 10
	maxRecipients   = 20
	maxURL          = 2048
)

// Config is a destination's non-secret settings. Which fields apply
// depends on the kind.
type Config struct {
	// Email.
	Host           string   `json:"host,omitempty"`
	Port           int      `json:"port,omitempty"`
	Security       string   `json:"security,omitempty"`
	Username       string   `json:"username,omitempty"`
	From           string   `json:"from,omitempty"`
	To             []string `json:"to,omitempty"`
	AllowPlaintext bool     `json:"allow_plaintext,omitempty"`
	// Webhook: the header that carries the optional secret value.
	HeaderName string `json:"header_name,omitempty"`
	// Webhook, Discord, Slack: where the (secret) URL points, for display.
	// Never set for Telegram: its request URL contains the bot token.
	URLDisplay string `json:"url_display,omitempty"`
	// Telegram: the chat (a number such as -1001234567890, or @channel)
	// and, in a group with topics, the topic.
	ChatID   string `json:"chat_id,omitempty"`
	ThreadID int64  `json:"thread_id,omitempty"`
	// Request timeout in seconds (all kinds; default 10, at most 30).
	TimeoutS int `json:"timeout_s,omitempty"`
}

// Secrets are a destination's secret values, sealed at rest and never
// returned by the API.
type Secrets struct {
	URL         string `json:"url,omitempty"`          // webhook, Discord, Slack
	HeaderValue string `json:"header_value,omitempty"` // webhook (e.g. "Bearer …")
	Password    string `json:"password,omitempty"`     // email
	BotToken    string `json:"bot_token,omitempty"`    // Telegram
}

// values lists the non-empty secret strings (for redaction).
func (s Secrets) values() []string {
	var out []string
	for _, v := range []string{s.URL, s.HeaderValue, s.Password} {
		if v != "" {
			out = append(out, v)
		}
	}
	out = append(out, tokenForms(s.BotToken)...)
	if u, err := url.Parse(s.URL); err == nil && s.URL != "" {
		// Parts of the URL show up separately in some errors.
		for _, p := range []string{u.Path, u.RawQuery, u.EscapedPath()} {
			if len(p) > 8 {
				out = append(out, p)
			}
		}
	}
	return out
}

// Timeout is the request timeout.
func (c Config) Timeout() time.Duration {
	if c.TimeoutS <= 0 {
		return DefaultTimeout
	}
	return min(time.Duration(c.TimeoutS)*time.Second, MaxTimeout)
}

// ConfigError is a malformed destination.
type ConfigError struct{ Msg string }

func (e *ConfigError) Error() string { return e.Msg }

func cfgErr(format string, a ...any) error { return &ConfigError{Msg: fmt.Sprintf(format, a...)} }

// Normalize validates a destination and fills defaults. It returns
// warnings the UI shows (unencrypted transport) alongside a valid config.
func Normalize(kind string, c *Config, s *Secrets) (warnings []string, err error) {
	if c.TimeoutS < 0 || c.TimeoutS > int(MaxTimeout/time.Second) {
		return nil, cfgErr("timeout must be between 1 and %d seconds", int(MaxTimeout/time.Second))
	}
	switch kind {
	case KindEmail:
		c.HeaderName, c.URLDisplay, c.ChatID, c.ThreadID = "", "", "", 0
		s.URL, s.HeaderValue, s.BotToken = "", "", ""
		c.Host = strings.TrimSpace(c.Host)
		if c.Host == "" || strings.ContainsAny(c.Host, " /\r\n") || len(c.Host) > 253 {
			return nil, cfgErr("SMTP server: enter a host name or address")
		}
		if c.Security == "" {
			c.Security = SecurityStartTLS
		}
		if !slices.Contains([]string{SecurityStartTLS, SecurityTLS, SecurityNone}, c.Security) {
			return nil, cfgErr("security must be starttls, tls or none")
		}
		if c.Port == 0 {
			c.Port = map[string]int{SecurityStartTLS: 587, SecurityTLS: 465, SecurityNone: 25}[c.Security]
		}
		if c.Port < 1 || c.Port > 65535 {
			return nil, cfgErr("port must be 1-65535")
		}
		c.Username = strings.TrimSpace(c.Username)
		if strings.ContainsAny(c.Username, "\r\n") || len(c.Username) > 256 {
			return nil, cfgErr("invalid username")
		}
		if c.Username != "" && s.Password == "" {
			return nil, cfgErr("a username needs a password")
		}
		if c.Security == SecurityNone {
			if !c.AllowPlaintext {
				return nil, cfgErr("unencrypted SMTP sends alerts in clear text; choose STARTTLS or TLS, or confirm unencrypted delivery")
			}
			if c.Username != "" || s.Password != "" {
				return nil, cfgErr("DROOL never sends an SMTP password over an unencrypted connection; use STARTTLS or TLS")
			}
			warnings = append(warnings, "Email is sent unencrypted. Use this only for a relay on a trusted network.")
		} else {
			c.AllowPlaintext = false
		}
		from, err := mail.ParseAddress(strings.TrimSpace(c.From))
		if err != nil {
			return nil, cfgErr("from: enter an email address")
		}
		c.From = from.String()
		if len(c.To) == 0 {
			return nil, cfgErr("add at least one recipient")
		}
		if len(c.To) > maxRecipients {
			return nil, cfgErr("at most %d recipients", maxRecipients)
		}
		var to []string
		for _, t := range c.To {
			a, err := mail.ParseAddress(strings.TrimSpace(t))
			if err != nil {
				return nil, cfgErr("recipient %q is not an email address", t)
			}
			if !slices.Contains(to, a.Address) {
				to = append(to, a.Address)
			}
		}
		c.To = to
	case KindTelegram:
		c.Host, c.Port, c.Security, c.Username, c.From, c.To, c.AllowPlaintext = "", 0, "", "", "", nil, false
		c.HeaderName, c.URLDisplay = "", ""
		s.URL, s.HeaderValue, s.Password = "", "", ""
		if err := normalizeTelegram(c, s); err != nil {
			return nil, err
		}
	case KindWebhook, KindDiscord, KindSlack:
		c.Host, c.Port, c.Security, c.Username, c.From, c.To, c.AllowPlaintext = "", 0, "", "", "", nil, false
		c.ChatID, c.ThreadID = "", 0
		s.Password, s.BotToken = "", ""
		u, err := checkURL(s.URL)
		if err != nil {
			return nil, err
		}
		switch kind {
		case KindWebhook:
			c.HeaderName = strings.TrimSpace(c.HeaderName)
			if s.HeaderValue != "" && c.HeaderName == "" {
				c.HeaderName = "Authorization"
			}
			if c.HeaderName != "" && !validHeaderName(c.HeaderName) {
				return nil, cfgErr("header name may contain only letters, digits and '-'")
			}
			if strings.ContainsAny(s.HeaderValue, "\r\n") || len(s.HeaderValue) > 4096 {
				return nil, cfgErr("invalid header value")
			}
			if u.Scheme == "http" {
				w := "Notifications to this URL are sent unencrypted."
				if s.HeaderValue != "" {
					w = "Notifications and the authorization header are sent unencrypted."
				}
				warnings = append(warnings, w)
			}
		case KindDiscord:
			c.HeaderName, s.HeaderValue = "", ""
			if u.Scheme != "https" || !strings.Contains(u.Path, "/api/webhooks/") {
				return nil, cfgErr("paste the Discord webhook URL (https://discord.com/api/webhooks/…) from the channel's Integrations settings")
			}
		case KindSlack:
			c.HeaderName, s.HeaderValue = "", ""
			if u.Scheme != "https" || !strings.Contains(u.Path, "/services/") {
				return nil, cfgErr("paste the Slack incoming webhook URL (https://hooks.slack.com/services/…)")
			}
		}
		c.URLDisplay = u.Scheme + "://" + u.Host + "/…"
	default:
		return nil, cfgErr("kind must be email, webhook, discord, slack or telegram")
	}
	return warnings, nil
}

func validHeaderName(h string) bool {
	if len(h) > 64 {
		return false
	}
	for _, r := range h {
		if !(r == '-' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z') {
			return false
		}
	}
	return !strings.EqualFold(h, "Content-Type") && !strings.EqualFold(h, "Host") && !strings.EqualFold(h, "Content-Length")
}

// checkURL accepts absolute http(s) URLs without embedded credentials.
// Private and loopback addresses are allowed on purpose: administrators
// send notifications to services on their own network.
func checkURL(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, cfgErr("enter the webhook URL")
	}
	if len(raw) > maxURL {
		return nil, cfgErr("the URL is too long")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, cfgErr("the URL is not valid")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, cfgErr("the URL must start with https:// or http://")
	}
	if u.Host == "" || u.Hostname() == "" {
		return nil, cfgErr("the URL has no host")
	}
	if u.User != nil {
		return nil, cfgErr("don't put credentials in the URL; use the authorization header")
	}
	if u.Fragment != "" {
		return nil, cfgErr("the URL must not have a #fragment")
	}
	if p := u.Port(); p != "" {
		if _, err := net.LookupPort("tcp", p); err != nil {
			return nil, cfgErr("the URL's port is not valid")
		}
	}
	return u, nil
}

// --- messages ---

// Notification kinds.
const (
	MsgOpening    = "opening"
	MsgEscalation = "escalation"
	MsgReminder   = "reminder"
	MsgRecovery   = "recovery"
	// MsgCleared closes an alert whose issue ended without the machine
	// recovering: its check was turned off, its threshold changed so the
	// current state no longer qualifies, or the subject disappeared. It
	// goes where a recovery would (delivery kind "recovery") but never
	// says "recovered".
	MsgCleared = "cleared"
	MsgTest    = "test"
)

// Message is one notification, independent of the provider. It is
// snapshotted when queued so retries send the same content.
type Message struct {
	Kind       string     `json:"kind"`
	EventID    string     `json:"event_id"` // the alert and what happened to it: the same for every destination
	AlertID    string     `json:"alert_id,omitempty"`
	Severity   string     `json:"severity,omitempty"` // warning | critical
	HostID     string     `json:"host_id,omitempty"`
	Host       string     `json:"host,omitempty"`
	Title      string     `json:"title"`
	Detail     string     `json:"detail,omitempty"`
	Check      string     `json:"check,omitempty"`
	Category   string     `json:"category,omitempty"`
	Subject    string     `json:"subject,omitempty"`
	Value      *float64   `json:"value,omitempty"`
	ValueText  string     `json:"value_text,omitempty"`
	ObservedAt *time.Time `json:"observed_at,omitempty"`
	OpenedAt   time.Time  `json:"opened_at"`
	ResolvedAt *time.Time `json:"resolved_at,omitempty"`
	DurationS  *int64     `json:"duration_s,omitempty"`
	// Resolution (cleared): disabled | reconfigured | gone, and why.
	Resolution     string     `json:"resolution,omitempty"`
	ResolutionNote string     `json:"resolution_note,omitempty"`
	AckBy          string     `json:"ack_by,omitempty"`
	AckNote        string     `json:"ack_note,omitempty"`
	AckAt          *time.Time `json:"ack_at,omitempty"`
	Reminder       int        `json:"reminder,omitempty"`
	URL            string     `json:"url,omitempty"`
	TimeZone       string     `json:"tz,omitempty"`
	Fingerprint    string     `json:"fingerprint,omitempty"` // host/check/subject: the same for every occurrence
}

// Result is how a send ended.
type Result struct {
	Outcome    string        `json:"outcome"`  // delivered | transient | permanent
	Category   string        `json:"category"` // see Category*
	StatusCode int           `json:"status_code,omitempty"`
	Message    string        `json:"message"` // human readable, secrets removed
	RetryAfter time.Duration `json:"-"`
	// Ambiguous: the request may have reached the receiver although DROOL
	// saw no success (a timeout after the request was written). A retry
	// may then deliver it twice.
	Ambiguous  bool  `json:"ambiguous,omitempty"`
	DurationMs int64 `json:"duration_ms"`
}

// OK reports success.
func (r Result) OK() bool { return r.Outcome == OutcomeDelivered }

// Outcomes.
const (
	OutcomeDelivered = "delivered"
	OutcomeTransient = "transient"
	OutcomePermanent = "permanent"
)

// Result categories.
const (
	CategoryOK          = "ok"
	CategoryTimeout     = "timeout"
	CategoryTLS         = "tls"
	CategoryAuth        = "auth"
	CategoryRejected    = "rejected"     // the receiver refused the request (4xx, SMTP 5xx)
	CategoryRateLimited = "rate_limited" // 429
	CategoryServer      = "server_error" // 5xx, SMTP 4xx
	CategoryNetwork     = "network"      // DNS, connection refused, reset
	CategoryConfig      = "config"       // malformed destination, unsupported redirect, no STARTTLS
)

func fail(outcome, cat, msg string) Result {
	return Result{Outcome: outcome, Category: cat, Message: msg}
}

// redact removes secret values from text.
func redact(text string, secrets []string) string {
	slices.SortFunc(secrets, func(a, b string) int { return len(b) - len(a) })
	for _, s := range secrets {
		if s != "" {
			text = strings.ReplaceAll(text, s, "[redacted]")
		}
	}
	if len(text) > 500 {
		text = text[:500] + "…"
	}
	return text
}

// Redact removes s's secrets from text (for callers that log errors).
func Redact(text string, s Secrets) string { return redact(text, s.values()) }

// MarshalSecrets / UnmarshalSecrets encode the sealed blob.
func MarshalSecrets(s Secrets) []byte { b, _ := json.Marshal(s); return b }

func UnmarshalSecrets(b []byte) (Secrets, error) {
	var s Secrets
	if len(b) == 0 {
		return s, nil
	}
	if err := json.Unmarshal(b, &s); err != nil {
		return s, errors.New("stored destination secrets are unreadable")
	}
	return s, nil
}
