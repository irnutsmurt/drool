package alerts

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/url"
	"strings"
	"time"
)

const settingKey = "alerts"

// Settings are alerting's fleet-wide choices.
type Settings struct {
	// LinkBaseURL is DROOL's address as recipients reach it, for "Open in
	// DROOL" links (empty: no links). The UI offers the address it is
	// served from.
	LinkBaseURL string `json:"link_base_url"`
	// TimeZone for times in notifications (IANA name; empty: the
	// controller's).
	TimeZone string `json:"time_zone"`
	// Retention. Alerts are small; deliveries keep per-attempt detail.
	AlertRetentionDays    int `json:"alert_retention_days"`
	DeliveryRetentionDays int `json:"delivery_retention_days"`
}

// DefaultSettings: alert history for six months, delivery detail for 30
// days (see ARCHITECTURE-ALPHA §28 for the measurements).
func DefaultSettings() Settings {
	return Settings{AlertRetentionDays: 180, DeliveryRetentionDays: 30}
}

// Validate checks the settings.
func (s *Settings) Validate() error {
	s.LinkBaseURL = strings.TrimRight(strings.TrimSpace(s.LinkBaseURL), "/")
	if s.LinkBaseURL != "" {
		u, err := url.Parse(s.LinkBaseURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return &ValidationError{"the link address must be like https://drool.example.lan:8443"}
		}
	}
	if s.TimeZone != "" {
		if _, err := time.LoadLocation(s.TimeZone); err != nil {
			return &ValidationError{"unknown time zone " + s.TimeZone}
		}
	}
	if s.AlertRetentionDays < 30 || s.AlertRetentionDays > 730 {
		return &ValidationError{"keep alert history 30-730 days"}
	}
	if s.DeliveryRetentionDays < 7 || s.DeliveryRetentionDays > 365 || s.DeliveryRetentionDays > s.AlertRetentionDays {
		return &ValidationError{"keep delivery details 7-365 days, and no longer than alerts"}
	}
	return nil
}

func (s *Service) settings(ctx context.Context, qq q) Settings {
	set := DefaultSettings()
	var v string
	if qq.QueryRowContext(ctx, `SELECT value_json FROM settings WHERE key = ?`, settingKey).Scan(&v) == nil {
		json.Unmarshal([]byte(v), &set)
	}
	if set.Validate() != nil {
		return DefaultSettings()
	}
	return set
}

// Settings returns the settings.
func (s *Service) Settings(ctx context.Context) Settings { return s.settings(ctx, s.d.Store.Reader) }

// SaveSettings stores the settings; onTx runs in the transaction.
func (s *Service) SaveSettings(ctx context.Context, set Settings, by string, onTx func(tx *sql.Tx) error) error {
	if err := set.Validate(); err != nil {
		return err
	}
	return s.inTx(ctx, func(tx *sql.Tx) error {
		var uid any
		if by != "" {
			uid = by
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO settings (key, value_json, updated_by, updated_at) VALUES (?, ?, ?, ?)
			ON CONFLICT (key) DO UPDATE SET value_json = excluded.value_json, updated_by = excluded.updated_by, updated_at = excluded.updated_at`,
			settingKey, marshal(set), uid, s.nowMs()); err != nil {
			return err
		}
		return onTx(tx)
	})
}

// SetLinkBaseIfUnset records DROOL's address the first time a destination
// is created from the UI, so links work without a separate step.
func (s *Service) SetLinkBaseIfUnset(ctx context.Context, base string) {
	set := s.Settings(ctx)
	if set.LinkBaseURL != "" || base == "" {
		return
	}
	set.LinkBaseURL = base
	if set.Validate() != nil {
		return
	}
	s.SaveSettings(ctx, set, "", func(*sql.Tx) error { return nil })
}
