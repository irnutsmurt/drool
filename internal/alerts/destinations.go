package alerts

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"drool/internal/ids"
	"drool/internal/notify"
	"drool/internal/store"
)

// Destination is a notification destination as the API shows it: never
// its secrets, only whether each is set.
type Destination struct {
	ID         string            `json:"id"`
	Name       string            `json:"name"`
	Kind       string            `json:"kind"`
	Enabled    bool              `json:"enabled"`
	Config     notify.Config     `json:"config"`
	SecretsSet SecretsSet        `json:"secrets_set"`
	NotifyFor  string            `json:"notify_for"` // recommended (in the default route) | custom
	Routes     []string          `json:"routes"`     // names of routes using it
	Health     DestinationHealth `json:"health"`
	LastTest   *TestSummary      `json:"last_test,omitempty"`
	Warnings   []string          `json:"warnings"`
	CreatedAt  int64             `json:"created_at"`
	UpdatedAt  int64             `json:"updated_at"`
}

// SecretsSet says which secrets are stored.
type SecretsSet struct {
	URL         bool `json:"url"`
	HeaderValue bool `json:"header_value"`
	Password    bool `json:"password"`
	BotToken    bool `json:"bot_token"`
}

// DestinationHealth is the small delivery summary.
type DestinationHealth struct {
	Status              string `json:"status"` // ok | failing | unused | disabled
	LastSuccessAt       *int64 `json:"last_success_at,omitempty"`
	LastFailureAt       *int64 `json:"last_failure_at,omitempty"`
	LastError           string `json:"last_error,omitempty"`
	ConsecutiveFailures int    `json:"consecutive_failures"`
	FailingSince        *int64 `json:"failing_since,omitempty"`
	Queued              int    `json:"queued"`
}

// TestSummary is the last test notification.
type TestSummary struct {
	At     int64  `json:"at"`
	OK     bool   `json:"ok"`
	Result string `json:"result"`
}

// DestinationInput creates or changes a destination. Secrets: nil keeps
// the stored value, "" clears an optional one.
type DestinationInput struct {
	Name      *string        `json:"name"`
	Kind      string         `json:"kind"` // create only
	Enabled   *bool          `json:"enabled"`
	Config    *notify.Config `json:"config"`
	Secrets   *SecretsInput  `json:"secrets"`
	NotifyFor *string        `json:"notify_for"` // recommended | custom
}

// SecretsInput carries new secret values.
type SecretsInput struct {
	URL         *string `json:"url"`
	HeaderValue *string `json:"header_value"`
	Password    *string `json:"password"`
	BotToken    *string `json:"bot_token"`
}

// Changed names the secrets an input replaces (for the audit log; never
// the values).
func (in *SecretsInput) Changed() []string {
	var out []string
	if in == nil {
		return out
	}
	if in.URL != nil {
		out = append(out, "url")
	}
	if in.HeaderValue != nil {
		out = append(out, "header_value")
	}
	if in.Password != nil {
		out = append(out, "password")
	}
	if in.BotToken != nil {
		out = append(out, "bot_token")
	}
	return out
}

func (in *SecretsInput) apply(s *notify.Secrets) {
	if in == nil {
		return
	}
	if in.URL != nil {
		s.URL = strings.TrimSpace(*in.URL)
	}
	if in.HeaderValue != nil {
		s.HeaderValue = *in.HeaderValue
	}
	if in.Password != nil {
		s.Password = *in.Password
	}
	if in.BotToken != nil {
		s.BotToken = strings.TrimSpace(*in.BotToken)
	}
}

func secretAAD(kind string) string { return "notify-" + kind }

const maxDestinations = 50

func (s *Service) seal(id, kind string, sec notify.Secrets) (ct, nonce []byte, ver any, err error) {
	if sec == (notify.Secrets{}) {
		return nil, nil, nil, nil
	}
	c, n, v, err := s.d.Box.Seal(id, secretAAD(kind), notify.MarshalSecrets(sec))
	return c, n, v, err
}

// CreateDestination validates and stores a destination. With notify_for
// "recommended" (the default) it joins the default route.
func (s *Service) CreateDestination(ctx context.Context, in DestinationInput, by Actor, onTx func(tx *sql.Tx, d *Destination) error) (*Destination, error) {
	if in.Name == nil || in.Config == nil {
		return nil, &ValidationError{"name, kind and config are required"}
	}
	name := strings.TrimSpace(*in.Name)
	if name == "" || len(name) > 64 {
		return nil, &ValidationError{"the name must be 1-64 characters"}
	}
	cfg := *in.Config
	var sec notify.Secrets
	in.Secrets.apply(&sec)
	warnings, err := notify.Normalize(in.Kind, &cfg, &sec)
	if err != nil {
		return nil, &ValidationError{err.Error()}
	}
	notifyFor := "recommended"
	if in.NotifyFor != nil {
		notifyFor = *in.NotifyFor
	}
	if notifyFor != "recommended" && notifyFor != "custom" {
		return nil, &ValidationError{"notify_for must be recommended or custom"}
	}
	id := ids.New(ids.NotifyDest)
	ct, nonce, ver, err := s.seal(id, in.Kind, sec)
	if err != nil {
		return nil, err
	}
	enabled := in.Enabled == nil || *in.Enabled
	now := s.nowMs()
	err = s.inTx(ctx, func(tx *sql.Tx) error {
		var n int
		tx.QueryRowContext(ctx, `SELECT count(*) FROM notify_destinations`).Scan(&n)
		if n >= maxDestinations {
			return &ValidationError{"at most 50 destinations"}
		}
		var uid any
		if by.UserID != "" {
			uid = by.UserID
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO notify_destinations (id, name, kind, enabled, config_json, secret_ciphertext, secret_nonce,
			secret_key_version, created_by, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, name, in.Kind, b2i(enabled), marshal(cfg), ct, nonce, ver, uid, now, now); err != nil {
			if store.IsUniqueViolation(err) {
				return ErrNameTaken
			}
			return err
		}
		if notifyFor == "recommended" {
			if _, err := tx.ExecContext(ctx, `INSERT INTO notify_route_destinations (route_id, destination_id) VALUES (?, ?)`, DefaultRouteID, id); err != nil {
				return err
			}
		}
		d, err := s.destination(ctx, tx, id)
		if err != nil {
			return err
		}
		d.Warnings = warnings
		return onTx(tx, d)
	})
	if err != nil {
		return nil, err
	}
	d, err := s.Destination(ctx, id)
	if d != nil {
		d.Warnings = warnings
	}
	return d, err
}

// UpdateDestination changes a destination; onTx gets it before and after.
func (s *Service) UpdateDestination(ctx context.Context, id string, in DestinationInput, onTx func(tx *sql.Tx, before, after *Destination) error) (*Destination, error) {
	var warnings []string
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		before, err := s.destination(ctx, tx, id)
		if err != nil {
			return err
		}
		if in.Kind != "" && in.Kind != before.Kind {
			return &ValidationError{"a destination's kind can't change; create a new destination"}
		}
		name := before.Name
		if in.Name != nil {
			name = strings.TrimSpace(*in.Name)
			if name == "" || len(name) > 64 {
				return &ValidationError{"the name must be 1-64 characters"}
			}
		}
		cfg := before.Config
		if in.Config != nil {
			cfg = *in.Config
		}
		sec, err := s.secrets(ctx, tx, id, before.Kind)
		if err != nil && in.Secrets == nil {
			return &ValidationError{"the stored secrets can't be decrypted; enter them again"}
		}
		in.Secrets.apply(&sec)
		if warnings, err = notify.Normalize(before.Kind, &cfg, &sec); err != nil {
			return &ValidationError{err.Error()}
		}
		ct, nonce, ver, err := s.seal(id, before.Kind, sec)
		if err != nil {
			return err
		}
		enabled := before.Enabled
		if in.Enabled != nil {
			enabled = *in.Enabled
		}
		if _, err := tx.ExecContext(ctx, `UPDATE notify_destinations SET name = ?, enabled = ?, config_json = ?, secret_ciphertext = ?, secret_nonce = ?,
			secret_key_version = ?, updated_at = ? WHERE id = ?`, name, b2i(enabled), marshal(cfg), ct, nonce, ver, s.nowMs(), id); err != nil {
			if store.IsUniqueViolation(err) {
				return ErrNameTaken
			}
			return err
		}
		if in.Config != nil || in.Secrets != nil || (in.Enabled != nil && *in.Enabled) {
			// New settings deserve a fresh start: no "failing" left over.
			if _, err := tx.ExecContext(ctx, `UPDATE notify_destinations SET consecutive_failures = 0, failing_since = NULL WHERE id = ?`, id); err != nil {
				return err
			}
		}
		if in.NotifyFor != nil {
			switch *in.NotifyFor {
			case "recommended":
				_, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO notify_route_destinations (route_id, destination_id) VALUES (?, ?)`, DefaultRouteID, id)
			case "custom":
				_, err = tx.ExecContext(ctx, `DELETE FROM notify_route_destinations WHERE route_id = ? AND destination_id = ?`, DefaultRouteID, id)
			default:
				return &ValidationError{"notify_for must be recommended or custom"}
			}
			if err != nil {
				return err
			}
		}
		after, err := s.destination(ctx, tx, id)
		if err != nil {
			return err
		}
		return onTx(tx, before, after)
	})
	if err != nil {
		return nil, err
	}
	d, err := s.Destination(ctx, id)
	if d != nil {
		d.Warnings = warnings
	}
	return d, err
}

// DeleteDestination removes a destination. Its unsent notifications are
// cancelled; alerts are unaffected.
func (s *Service) DeleteDestination(ctx context.Context, id string, onTx func(tx *sql.Tx, d *Destination) error) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		d, err := s.destination(ctx, tx, id)
		if err != nil {
			return err
		}
		now := s.nowMs()
		if _, err := tx.ExecContext(ctx, `UPDATE notify_deliveries SET status = 'cancelled', finished_at = ?, last_error = 'the destination was deleted'
			WHERE destination_id = ? AND status = 'queued'`, now, id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM notify_destinations WHERE id = ?`, id); err != nil {
			return err
		}
		return onTx(tx, d)
	})
}

func (s *Service) secrets(ctx context.Context, qq q, id, kind string) (notify.Secrets, error) {
	var ct, nonce []byte
	var ver sql.NullInt64
	if err := qq.QueryRowContext(ctx, `SELECT secret_ciphertext, secret_nonce, secret_key_version FROM notify_destinations WHERE id = ?`, id).Scan(&ct, &nonce, &ver); err != nil {
		return notify.Secrets{}, err
	}
	if len(ct) == 0 {
		return notify.Secrets{}, nil
	}
	pt, err := s.d.Box.Open(id, secretAAD(kind), ct, nonce, int(ver.Int64))
	if err != nil {
		return notify.Secrets{}, ErrSecretsMismatch
	}
	return notify.UnmarshalSecrets(pt)
}

const destCols = `d.id, d.name, d.kind, d.enabled, d.config_json, d.secret_ciphertext IS NOT NULL, d.created_at, d.updated_at, d.last_success_at,
	d.last_failure_at, d.last_error, d.consecutive_failures, d.failing_since, d.last_test_at, d.last_test_ok, d.last_test_result,
	(SELECT count(*) FROM notify_deliveries x WHERE x.destination_id = d.id AND x.status IN ('queued','sending'))`

func (s *Service) scanDest(r scanner) (*Destination, error) {
	var d Destination
	var en, hasSecret, fails, queued int
	var cfg, lastErr, testResult string
	var success, failure, failing, testAt, testOK sql.NullInt64
	if err := r.Scan(&d.ID, &d.Name, &d.Kind, &en, &cfg, &hasSecret, &d.CreatedAt, &d.UpdatedAt, &success, &failure, &lastErr, &fails, &failing,
		&testAt, &testOK, &testResult, &queued); err != nil {
		return nil, err
	}
	d.Enabled = en == 1
	json.Unmarshal([]byte(cfg), &d.Config)
	ptr := func(v sql.NullInt64) *int64 {
		if v.Valid {
			return &v.Int64
		}
		return nil
	}
	d.Health = DestinationHealth{LastSuccessAt: ptr(success), LastFailureAt: ptr(failure), LastError: lastErr, ConsecutiveFailures: fails,
		FailingSince: ptr(failing), Queued: queued}
	switch {
	case !d.Enabled:
		d.Health.Status = "disabled"
	case failing.Valid:
		d.Health.Status = "failing"
	case success.Valid:
		d.Health.Status = "ok"
	default:
		d.Health.Status = "unused"
	}
	if testAt.Valid {
		d.LastTest = &TestSummary{At: testAt.Int64, OK: testOK.Int64 == 1, Result: testResult}
	}
	if hasSecret == 1 {
		switch d.Kind {
		case notify.KindEmail:
			d.SecretsSet.Password = d.Config.Username != ""
		case notify.KindWebhook:
			d.SecretsSet.URL, d.SecretsSet.HeaderValue = true, d.Config.HeaderName != ""
		case notify.KindTelegram:
			d.SecretsSet.BotToken = true
		default:
			d.SecretsSet.URL = true
		}
	}
	d.Routes, d.Warnings = []string{}, []string{}
	if d.Kind == notify.KindEmail && d.Config.Security == notify.SecurityNone {
		d.Warnings = append(d.Warnings, "Email is sent unencrypted.")
	}
	if strings.HasPrefix(d.Config.URLDisplay, "http://") {
		d.Warnings = append(d.Warnings, "Notifications are sent unencrypted (http).")
	}
	return &d, nil
}

func (s *Service) destinations(ctx context.Context, qq q, where string, args ...any) ([]*Destination, error) {
	rows, err := qq.QueryContext(ctx, `SELECT `+destCols+` FROM notify_destinations d WHERE `+where+` ORDER BY d.name COLLATE NOCASE`, args...)
	if err != nil {
		return nil, err
	}
	out := []*Destination{}
	for rows.Next() {
		d, err := s.scanDest(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		d.NotifyFor = "custom"
		out = append(out, d)
	}
	rows.Close()
	if len(out) == 0 {
		return out, nil
	}
	rrows, err := qq.QueryContext(ctx, `SELECT rd.destination_id, r.name, r.is_default FROM notify_route_destinations rd JOIN notify_routes r ON r.id = rd.route_id
		ORDER BY r.is_default DESC, r.name`)
	if err != nil {
		return nil, err
	}
	defer rrows.Close()
	for rrows.Next() {
		var did, name string
		var def int
		if err := rrows.Scan(&did, &name, &def); err != nil {
			return nil, err
		}
		for _, d := range out {
			if d.ID == did {
				d.Routes = append(d.Routes, name)
				if def == 1 {
					d.NotifyFor = "recommended"
				}
			}
		}
	}
	return out, rrows.Err()
}

func (s *Service) destination(ctx context.Context, qq q, id string) (*Destination, error) {
	ds, err := s.destinations(ctx, qq, `d.id = ?`, id)
	if err != nil {
		return nil, err
	}
	if len(ds) == 0 {
		return nil, ErrNotFound
	}
	return ds[0], nil
}

// Destinations lists every destination.
func (s *Service) Destinations(ctx context.Context) ([]*Destination, error) {
	return s.destinations(ctx, s.d.Store.Reader, `1=1`)
}

// Destination loads one destination.
func (s *Service) Destination(ctx context.Context, id string) (*Destination, error) {
	return s.destination(ctx, s.d.Store.Reader, id)
}

// TestResult is a test notification's outcome.
type TestResult struct {
	notify.Result
	Warnings []string `json:"warnings"`
}

// Test sends a test notification to a stored destination (even a
// disabled one). It creates no alert and leaves the delivery health
// alone; the last test is kept separately.
func (s *Service) Test(ctx context.Context, id string) (*TestResult, *Destination, error) {
	d, err := s.Destination(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	sec, err := s.secrets(ctx, s.d.Store.Reader, id, d.Kind)
	if err != nil {
		return &TestResult{Result: notify.Result{Outcome: notify.OutcomePermanent, Category: notify.CategoryConfig,
			Message: "the stored secrets can't be decrypted; enter them again"}, Warnings: []string{}}, d, nil
	}
	r := s.sender.Send(ctx, d.Kind, d.Config, sec, s.testMessage(ctx), "")
	text := r.Message
	s.d.Store.Writer.ExecContext(ctx, `UPDATE notify_destinations SET last_test_at = ?, last_test_ok = ?, last_test_result = ? WHERE id = ?`,
		s.nowMs(), b2i(r.OK()), text, id)
	return &TestResult{Result: r, Warnings: d.Warnings}, d, nil
}

// TestUnsaved sends a test notification to a destination that isn't
// stored yet (the setup form's "Send test").
func (s *Service) TestUnsaved(ctx context.Context, kind string, cfg notify.Config, in *SecretsInput) *TestResult {
	var sec notify.Secrets
	in.apply(&sec)
	warnings, err := notify.Normalize(kind, &cfg, &sec)
	if err != nil {
		return &TestResult{Result: notify.Result{Outcome: notify.OutcomePermanent, Category: notify.CategoryConfig, Message: err.Error()}, Warnings: []string{}}
	}
	r := s.sender.Send(ctx, kind, cfg, sec, s.testMessage(ctx), "")
	if warnings == nil {
		warnings = []string{}
	}
	return &TestResult{Result: r, Warnings: warnings}
}

func (s *Service) testMessage(ctx context.Context) notify.Message {
	set := s.settings(ctx, s.d.Store.Reader)
	m := notify.Message{Kind: notify.MsgTest, EventID: "test:" + ids.New(ids.NotifyDelivery), Title: "DROOL notification test",
		OpenedAt: time.Now().UTC(), TimeZone: set.TimeZone}
	if set.LinkBaseURL != "" {
		m.URL = strings.TrimRight(set.LinkBaseURL, "/") + "/settings/notifications"
	}
	return m
}

// KindsList is exported for the API (validation of the kind parameter).
func KindsList() []string { return slices.Clone(notify.Kinds) }

// IsValidation reports a request error.
func IsValidation(err error) bool {
	var v *ValidationError
	return errors.As(err, &v)
}
