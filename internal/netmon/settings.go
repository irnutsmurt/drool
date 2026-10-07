package netmon

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"drool/internal/store/db"
)

// Collection modes: which agent hosts sample connections continuously.
// SSH-only hosts are never continuous; they get snapshots on request.
const (
	CollectAll      = "all"      // every agent host with the monitoring capability
	CollectFirewall = "firewall" // only hosts whose firewall mode isn't off (M8 behaviour)
	CollectOff      = "off"
)

// Settings is the network monitoring configuration (audited when changed).
type Settings struct {
	Collection string `json:"collection"`
	// RetentionDays: observations not seen for this long are pruned.
	RetentionDays int `json:"retention_days"`
	// MaxFlowsPerHost bounds each host's rows; the least recently seen
	// go first.
	MaxFlowsPerHost int `json:"max_flows_per_host"`
	// Enrichment: look public addresses up in the local GeoIP/ASN
	// databases, if any are installed. Never online.
	Enrichment bool `json:"enrichment"`
}

// Defaults.
const (
	DefaultRetentionDays   = 30
	DefaultMaxFlowsPerHost = 5000
)

const settingKey = "network.monitoring"

// DefaultSettings are used until an administrator saves settings.
func DefaultSettings() Settings {
	return Settings{Collection: CollectAll, RetentionDays: DefaultRetentionDays, MaxFlowsPerHost: DefaultMaxFlowsPerHost, Enrichment: true}
}

// LoadSettings reads the settings (defaults for anything unset).
func LoadSettings(ctx context.Context, q *db.Queries) Settings {
	s := DefaultSettings()
	if row, err := q.GetSetting(ctx, settingKey); err == nil {
		json.Unmarshal([]byte(row.ValueJson), &s)
	}
	if s.Validate() != nil {
		return DefaultSettings()
	}
	return s
}

// Validate checks the settings.
func (s Settings) Validate() error {
	switch s.Collection {
	case CollectAll, CollectFirewall, CollectOff:
	default:
		return fmt.Errorf("collection must be all, firewall or off")
	}
	if s.RetentionDays < 1 || s.RetentionDays > 365 {
		return fmt.Errorf("retention must be between 1 and 365 days")
	}
	if s.MaxFlowsPerHost < 100 || s.MaxFlowsPerHost > 100000 {
		return fmt.Errorf("the per-host limit must be between 100 and 100000 connections")
	}
	return nil
}

// SaveSettingsTx stores the settings.
func SaveSettingsTx(ctx context.Context, q *db.Queries, s Settings, by string, now int64) error {
	if err := s.Validate(); err != nil {
		return err
	}
	b, _ := json.Marshal(s)
	return q.UpsertSetting(ctx, db.UpsertSettingParams{Key: settingKey, ValueJson: string(b),
		UpdatedBy: sql.NullString{String: by, Valid: by != ""}, UpdatedAt: now})
}

// Collects reports whether an agent host samples continuously under s,
// given its firewall mode.
func (s Settings) Collects(firewallMode string) bool {
	switch s.Collection {
	case CollectAll:
		return true
	case CollectFirewall:
		return firewallMode != "" && firewallMode != "off"
	}
	return false
}
