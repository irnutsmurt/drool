package audit

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"drool/internal/store"
)

// The remote audit sink (ARCHITECTURE-ALPHA §33.5) sends a copy of every
// audit record, with its chain fields, to one HTTPS endpoint outside the
// controller. The queue is the audit table itself: cursor_seq says how far
// the receiver has acknowledged, so nothing extra is stored, a restart
// resumes where it stopped, and a failing receiver never blocks anything
// else (it only grows the backlog). Delivery is at least once; receivers
// deduplicate by seq and hash.

// ExportFormat names the body format.
const ExportFormat = "drool-audit-export-v1"

// Sealer encrypts the sink's header value (secrets.Box).
type Sealer interface {
	Seal(id, kind string, plaintext []byte) (ciphertext, nonce []byte, keyVersion int, err error)
	Open(id, kind string, ciphertext, nonce []byte, keyVersion int) ([]byte, error)
}

const sinkSecretKind = "audit-sink-header"

// SinkDeps are the sink's dependencies.
type SinkDeps struct {
	Store      *store.Store
	Box        Sealer
	Log        *slog.Logger
	Controller string        // this controller's name in exports
	Epoch      func() string // controller epoch (restores start a new one)
	Now        func() time.Time
	// Transport overrides the HTTP transport (tests); TLS settings are
	// still applied when it is an *http.Transport.
	Transport http.RoundTripper
	// Batch is the most records per request (default 500).
	Batch int
}

// Sink delivers the audit history to the configured receiver.
type Sink struct {
	d    SinkDeps
	kick chan struct{}
	mu   sync.Mutex // one delivery at a time (Run and Test)
}

// NewSink creates the sink worker.
func NewSink(d SinkDeps) *Sink {
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Log == nil {
		d.Log = slog.New(slog.DiscardHandler)
	}
	if d.Batch <= 0 {
		d.Batch = 500
	}
	return &Sink{d: d, kick: make(chan struct{}, 1)}
}

// SetEpoch tells exports the controller epoch (a restored controller has a
// new one, which tells receivers about the fork in the history).
func (s *Sink) SetEpoch(fn func() string) {
	s.mu.Lock()
	s.d.Epoch = fn
	s.mu.Unlock()
}

// Kick wakes the worker (the writer calls it after each record).
func (s *Sink) Kick() {
	select {
	case s.kick <- struct{}{}:
	default:
	}
}

// SinkConfig is what an administrator sets.
type SinkConfig struct {
	Enabled    bool
	URL        string
	CAPEM      string
	HeaderName string
	// HeaderValue: nil keeps the stored value, "" removes it.
	HeaderValue *string
	// StartFrom applies when the sink is first configured or the URL
	// changes: "all" sends the whole history, "now" only what comes next.
	StartFrom string
}

// SinkStatus is the sink's state for the UI.
type SinkStatus struct {
	Configured    bool       `json:"configured"`
	Enabled       bool       `json:"enabled"`
	URL           string     `json:"url,omitempty"`
	URLDisplay    string     `json:"url_display,omitempty"`
	HeaderName    string     `json:"header_name,omitempty"`
	HeaderSet     bool       `json:"header_set"`
	CustomCA      bool       `json:"custom_ca"`
	CursorSeq     int64      `json:"cursor_seq"`
	HeadSeq       int64      `json:"head_seq"`
	Backlog       int64      `json:"backlog"`
	OldestUnsent  *time.Time `json:"oldest_unsent,omitempty"`
	LastAttemptAt *time.Time `json:"last_attempt_at,omitempty"`
	LastSuccessAt *time.Time `json:"last_success_at,omitempty"`
	NextAttemptAt *time.Time `json:"next_attempt_at,omitempty"`
	LastError     string     `json:"last_error,omitempty"`
	Failures      int64      `json:"failures"`
	// State: off, ok, backlogged (behind by more than a few minutes),
	// failing (the last attempt failed).
	State string `json:"state"`
}

type sinkRow struct {
	enabled                 bool
	url, caPEM, headerName  string
	ct, nonce               []byte
	keyVersion              sql.NullInt64
	cursor                  int64
	lastAttempt, lastOK     sql.NullInt64
	lastError               string
	failures, nextAttemptAt int64
}

func (s *Sink) load(ctx context.Context) (*sinkRow, error) {
	var r sinkRow
	var enabled int64
	err := s.d.Store.Reader.QueryRowContext(ctx, `SELECT enabled, url, ca_pem, header_name, secret_ciphertext, secret_nonce, secret_key_version,
		cursor_seq, last_attempt_at, last_success_at, last_error, failures, next_attempt_at FROM audit_sink WHERE id = 'default'`).Scan(
		&enabled, &r.url, &r.caPEM, &r.headerName, &r.ct, &r.nonce, &r.keyVersion, &r.cursor, &r.lastAttempt, &r.lastOK, &r.lastError,
		&r.failures, &r.nextAttemptAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	r.enabled = enabled != 0
	return &r, err
}

// ValidationError is a configuration the sink refuses.
type ValidationError struct{ Msg string }

func (e *ValidationError) Error() string { return e.Msg }

// CheckURL validates a receiver URL: https only, no credentials in it
// (they go in the header, which is encrypted at rest).
func CheckURL(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return nil, &ValidationError{"the receiver URL is not valid"}
	}
	if u.Scheme != "https" {
		return nil, &ValidationError{"the receiver URL must use https (audit records are sensitive)"}
	}
	if u.User != nil {
		return nil, &ValidationError{"don't put credentials in the URL; use the header instead (it is stored encrypted)"}
	}
	return u, nil
}

func displayURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Scheme + "://" + u.Host + u.Path
}

func (s *Sink) client(r *sinkRow) (*http.Client, error) {
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	if strings.TrimSpace(r.caPEM) != "" && !pool.AppendCertsFromPEM([]byte(r.caPEM)) {
		return nil, &ValidationError{"the CA certificate is not valid PEM"}
	}
	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: pool}
	rt := s.d.Transport
	if rt == nil {
		rt = &http.Transport{TLSClientConfig: tlsCfg, Proxy: http.ProxyFromEnvironment, TLSHandshakeTimeout: 10 * time.Second,
			ResponseHeaderTimeout: 30 * time.Second, MaxIdleConns: 2, IdleConnTimeout: time.Minute}
	} else if t, ok := rt.(*http.Transport); ok {
		t = t.Clone()
		t.TLSClientConfig = tlsCfg
		rt = t
	}
	return &http.Client{Transport: rt, Timeout: 60 * time.Second,
		// A redirect would carry the header to another place: never follow.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, nil
}

// Configure stores the sink's settings. Secrets are sealed; the caller
// audits the change (never the header value).
func (s *Sink) Configure(ctx context.Context, c SinkConfig, userID string) error {
	cur, err := s.load(ctx)
	if err != nil {
		return err
	}
	if c.Enabled || c.URL != "" {
		if _, err := CheckURL(c.URL); err != nil {
			return err
		}
	}
	if strings.TrimSpace(c.CAPEM) != "" {
		if !x509.NewCertPool().AppendCertsFromPEM([]byte(c.CAPEM)) {
			return &ValidationError{"the CA certificate is not valid PEM"}
		}
	}
	if c.HeaderName != "" && !validHeaderName(c.HeaderName) {
		return &ValidationError{"the header name is not valid"}
	}
	now := s.d.Now().UnixMilli()
	var head sql.NullInt64
	s.d.Store.Reader.QueryRowContext(ctx, `SELECT max(seq) FROM audit_events`).Scan(&head)
	cursor := int64(0)
	if c.StartFrom == "now" {
		cursor = head.Int64
	}
	var ct, nonce []byte
	var ver sql.NullInt64
	if cur != nil {
		ct, nonce, ver = cur.ct, cur.nonce, cur.keyVersion
		if cur.url == strings.TrimSpace(c.URL) {
			cursor = cur.cursor // same receiver: carry on where it is
		}
	}
	if c.HeaderValue != nil {
		ct, nonce, ver = nil, nil, sql.NullInt64{}
		if *c.HeaderValue != "" {
			c2, n2, v2, err := s.d.Box.Seal("default", sinkSecretKind, []byte(*c.HeaderValue))
			if err != nil {
				return err
			}
			ct, nonce, ver = c2, n2, sql.NullInt64{Int64: int64(v2), Valid: true}
		}
	}
	_, err = s.d.Store.Writer.ExecContext(ctx, `INSERT INTO audit_sink (id, enabled, url, ca_pem, header_name, secret_ciphertext, secret_nonce,
		secret_key_version, cursor_seq, next_attempt_at, created_at, updated_at, updated_by) VALUES ('default', ?, ?, ?, ?, ?, ?, ?, ?, 0, ?, ?, ?)
		ON CONFLICT (id) DO UPDATE SET enabled = excluded.enabled, url = excluded.url, ca_pem = excluded.ca_pem,
		header_name = excluded.header_name, secret_ciphertext = excluded.secret_ciphertext, secret_nonce = excluded.secret_nonce,
		secret_key_version = excluded.secret_key_version, cursor_seq = excluded.cursor_seq, next_attempt_at = 0, failures = 0,
		last_error = '', updated_at = excluded.updated_at, updated_by = excluded.updated_by`,
		b2i(c.Enabled), strings.TrimSpace(c.URL), c.CAPEM, c.HeaderName, ct, nonce, ver, cursor, now, now, nullStr(userID))
	if err == nil {
		s.Kick()
	}
	return err
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

func nullStr(s string) sql.NullString { return sql.NullString{String: s, Valid: s != ""} }

func validHeaderName(h string) bool {
	if h == "" || len(h) > 100 {
		return false
	}
	for _, c := range h {
		if !(c == '-' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z') {
			return false
		}
	}
	return !strings.EqualFold(h, "Host") && !strings.EqualFold(h, "Content-Length") && !strings.EqualFold(h, "Content-Type")
}

// Status reports the sink's configuration (never the header value) and
// delivery state.
func (s *Sink) Status(ctx context.Context) (*SinkStatus, error) {
	r, err := s.load(ctx)
	if err != nil {
		return nil, err
	}
	st := &SinkStatus{State: "off"}
	var head sql.NullInt64
	if err := s.d.Store.Reader.QueryRowContext(ctx, `SELECT max(seq) FROM audit_events`).Scan(&head); err != nil {
		return nil, err
	}
	st.HeadSeq = head.Int64
	if r == nil {
		return st, nil
	}
	st.Configured, st.Enabled, st.URL, st.URLDisplay = true, r.enabled, r.url, displayURL(r.url)
	st.HeaderName, st.HeaderSet, st.CustomCA = r.headerName, r.ct != nil, strings.TrimSpace(r.caPEM) != ""
	st.CursorSeq, st.Failures, st.LastError = r.cursor, r.failures, r.lastError
	st.Backlog = max(0, st.HeadSeq-r.cursor)
	ms := func(v int64) *time.Time { t := time.UnixMilli(v).UTC(); return &t }
	if r.lastAttempt.Valid {
		st.LastAttemptAt = ms(r.lastAttempt.Int64)
	}
	if r.lastOK.Valid {
		st.LastSuccessAt = ms(r.lastOK.Int64)
	}
	if r.nextAttemptAt > 0 && r.failures > 0 {
		st.NextAttemptAt = ms(r.nextAttemptAt)
	}
	if st.Backlog > 0 {
		var ts int64
		if s.d.Store.Reader.QueryRowContext(ctx, `SELECT ts FROM audit_events WHERE seq = ?`, r.cursor+1).Scan(&ts) == nil {
			st.OldestUnsent = ms(ts)
		}
	}
	switch {
	case !r.enabled:
		st.State = "off"
	case r.failures > 0:
		st.State = "failing"
	case st.OldestUnsent != nil && s.d.Now().Sub(*st.OldestUnsent) > 10*time.Minute:
		st.State = "backlogged"
	default:
		st.State = "ok"
	}
	return st, nil
}

// exportRecord is one record in an export.
type exportRecord struct {
	Seq         int64           `json:"seq"`
	ID          string          `json:"id"`
	Ts          int64           `json:"ts"`
	Time        string          `json:"time"`
	ActorType   string          `json:"actor_type"`
	ActorID     string          `json:"actor_id"`
	ActorName   string          `json:"actor_name"`
	Action      string          `json:"action"`
	TargetType  string          `json:"target_type"`
	TargetID    string          `json:"target_id"`
	Result      string          `json:"result"`
	SourceIP    string          `json:"source_ip"`
	RequestID   string          `json:"request_id"`
	Details     json.RawMessage `json:"details"`
	DetailsJSON string          `json:"details_json"` // exactly what the hash covers
	PrevHash    string          `json:"prev_hash"`
	Hash        string          `json:"hash"`
}

type exportBody struct {
	Format          string         `json:"format"`
	ChainFormat     string         `json:"chain_format"`
	ChainID         string         `json:"chain_id"`
	Controller      string         `json:"controller"`
	ControllerEpoch string         `json:"controller_epoch"`
	SentAt          string         `json:"sent_at"`
	Test            bool           `json:"test,omitempty"`
	Records         []exportRecord `json:"records"`
}

func (s *Sink) chainID(ctx context.Context) string {
	var js string
	if s.d.Store.Reader.QueryRowContext(ctx, `SELECT details_json FROM audit_events WHERE seq = 1`).Scan(&js) != nil {
		return ""
	}
	var cs chainStart
	json.Unmarshal([]byte(js), &cs)
	return cs.ChainID
}

func (s *Sink) batch(ctx context.Context, after int64) ([]exportRecord, error) {
	rows, err := s.d.Store.Reader.QueryContext(ctx, `SELECT `+columns+` FROM audit_events WHERE seq > ? ORDER BY seq LIMIT ?`, after, s.d.Batch)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []exportRecord
	size := 0
	for rows.Next() {
		l, _, err := scanLink(rows)
		if err != nil {
			return nil, err
		}
		details := json.RawMessage(l.Details)
		if !json.Valid(details) {
			details = json.RawMessage("null")
		}
		out = append(out, exportRecord{Seq: l.Seq, ID: l.ID, Ts: l.Ts, Time: time.UnixMilli(l.Ts).UTC().Format(time.RFC3339Nano),
			ActorType: l.ActorType, ActorID: l.ActorID, ActorName: l.ActorName, Action: l.Action, TargetType: l.TargetType,
			TargetID: l.TargetID, Result: l.Result, SourceIP: l.SourceIP, RequestID: l.RequestID, Details: details,
			DetailsJSON: l.Details, PrevHash: hex.EncodeToString(l.PrevHash), Hash: hex.EncodeToString(l.Hash)})
		size += 2*len(l.Details) + 400
		if size > 2<<20 {
			break // keep requests around a few MB
		}
	}
	return out, rows.Err()
}

func (s *Sink) post(ctx context.Context, r *sinkRow, body exportBody) error {
	client, err := s.client(r)
	if err != nil {
		return err
	}
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.url, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "DROOL-audit-export/1")
	var secret string
	if r.headerName != "" && r.ct != nil {
		v, err := s.d.Box.Open("default", sinkSecretKind, r.ct, r.nonce, int(r.keyVersion.Int64))
		if err != nil {
			return errors.New("the stored header value can't be decrypted (was the master key replaced?)")
		}
		secret = string(v)
		req.Header.Set(r.headerName, secret)
	}
	resp, err := client.Do(req)
	if err != nil {
		return errors.New(redact(err.Error(), secret))
	}
	defer resp.Body.Close()
	msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("the receiver answered %s: %s", resp.Status, redact(strings.TrimSpace(string(msg)), secret))
	}
	return nil
}

func redact(s, secret string) string {
	if secret != "" {
		s = strings.ReplaceAll(s, secret, "«redacted»")
	}
	return s
}

// Test sends an empty export (test: true) with c, or the stored settings
// when c is nil. Nothing is marked delivered.
func (s *Sink) Test(ctx context.Context, c *SinkConfig) error {
	r, err := s.load(ctx)
	if err != nil {
		return err
	}
	if c != nil {
		if _, err := CheckURL(c.URL); err != nil {
			return err
		}
		t := sinkRow{url: strings.TrimSpace(c.URL), caPEM: c.CAPEM, headerName: c.HeaderName}
		if r != nil && c.HeaderValue == nil && r.url == t.url {
			t.ct, t.nonce, t.keyVersion = r.ct, r.nonce, r.keyVersion
		}
		if c.HeaderValue != nil && *c.HeaderValue != "" {
			ct, nonce, v, err := s.d.Box.Seal("default", sinkSecretKind, []byte(*c.HeaderValue))
			if err != nil {
				return err
			}
			t.ct, t.nonce, t.keyVersion = ct, nonce, sql.NullInt64{Int64: int64(v), Valid: true}
		}
		r = &t
	}
	if r == nil {
		return &ValidationError{"no receiver is configured"}
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return s.post(ctx, r, s.body(ctx, true, []exportRecord{}))
}

func (s *Sink) body(ctx context.Context, test bool, recs []exportRecord) exportBody {
	ep := ""
	if s.d.Epoch != nil {
		ep = s.d.Epoch()
	}
	return exportBody{Format: ExportFormat, ChainFormat: ChainFormat, ChainID: s.chainID(ctx), Controller: s.d.Controller,
		ControllerEpoch: ep, SentAt: s.d.Now().UTC().Format(time.RFC3339Nano), Test: test, Records: recs}
}

// DeliverOnce sends the next batch if one is due. It reports whether there
// is more to send right away.
func (s *Sink) DeliverOnce(ctx context.Context) (more bool, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.load(ctx)
	if err != nil || r == nil || !r.enabled {
		return false, err
	}
	now := s.d.Now()
	if r.failures > 0 && now.UnixMilli() < r.nextAttemptAt {
		return false, nil
	}
	recs, err := s.batch(ctx, r.cursor)
	if err != nil || len(recs) == 0 {
		return false, err
	}
	pctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	perr := s.post(pctx, r, s.body(ctx, false, recs))
	cancel()
	nowMs := s.d.Now().UnixMilli()
	if perr != nil {
		failures := r.failures + 1
		// 5 s, 10 s, 20 s, ... up to 10 minutes, with jitter; never gives up.
		backoff := min(10*time.Minute, 5*time.Second<<min(failures-1, 8))
		backoff += time.Duration(rand.Int64N(int64(backoff) / 5))
		_, err := s.d.Store.Writer.ExecContext(ctx, `UPDATE audit_sink SET last_attempt_at = ?, last_error = ?, failures = ?, next_attempt_at = ?
			WHERE id = 'default' AND url = ?`, nowMs, perr.Error(), failures, nowMs+backoff.Milliseconds(), r.url)
		if failures == 1 || failures%10 == 0 {
			s.d.Log.Warn("remote audit sink delivery failed", "url", displayURL(r.url), "failures", failures, "err", perr)
		}
		return false, err
	}
	last := recs[len(recs)-1].Seq
	// Only move forward, and only for the receiver the batch was sent to.
	_, err = s.d.Store.Writer.ExecContext(ctx, `UPDATE audit_sink SET cursor_seq = max(cursor_seq, ?), last_attempt_at = ?, last_success_at = ?,
		last_error = '', failures = 0, next_attempt_at = 0 WHERE id = 'default' AND url = ?`, last, nowMs, nowMs, r.url)
	if r.failures > 0 && err == nil {
		s.d.Log.Info("remote audit sink delivering again", "url", displayURL(r.url))
	}
	return err == nil && len(recs) == s.d.Batch, err
}

// Run delivers until ctx ends.
func (s *Sink) Run(ctx context.Context) {
	t := time.NewTicker(15 * time.Second)
	defer t.Stop()
	for {
		for {
			more, err := s.DeliverOnce(ctx)
			if err != nil && ctx.Err() == nil {
				s.d.Log.Error("remote audit sink", "err", err)
			}
			if !more || ctx.Err() != nil {
				break
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-s.kick:
			// Let a burst of records collect into one request.
			select {
			case <-ctx.Done():
				return
			case <-time.After(2 * time.Second):
			}
		}
	}
}
