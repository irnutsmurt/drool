package agentsvc

import (
	"strings"
	"testing"
	"time"

	"drool/internal/store/db"
)

func TestJudge(t *testing.T) {
	now := time.Now()
	all := []string{"firewall-1", "netmon-1", "monitor-1", "query-1", "epoch-1"}
	ok := db.AgentIdentity{Status: "active", Compatible: 1, AgentVersion: "v1.2.0", CertNotAfter: now.Add(24 * time.Hour).UnixMilli()}
	grace := 180 * 24 * time.Hour
	for _, c := range []struct {
		name     string
		id       func(db.AgentIdentity) db.AgentIdentity
		features []string
		needed   map[string]string
		update   string
		status   string
		contains string
	}{
		{"all good", nil, all, nil, "", CompatOK, "compatible"},
		{"update available", nil, all, nil, "v1.3.0", CompatUpdateAvailable, "v1.3.0"},
		{"missing an optional feature", nil, all[:4], nil, "", CompatReduced, "moving to a restored controller's epoch"},
		// What an M15 Shepherd announces: everything it can do, so only
		// the restore transition is missing (it answers queries too).
		{"an M15 Shepherd", nil, all[:3], nil, "", CompatReduced, "works, without moving to a restored controller's epoch (version"},
		{"missing a needed feature", nil, all[1:], map[string]string{"firewall-1": "its firewall is managed"}, "", CompatUpdateRequired, "firewall management (its firewall is managed)"},
		{"from before features", nil, nil, nil, "", CompatReduced, "before these were announced"},
		{"protocol", func(i db.AgentIdentity) db.AgentIdentity {
			i.Compatible, i.CompatReason = 0, "agent speaks 3-4"
			return i
		}, all, nil, "", CompatIncompatible, "3-4"},
		{"revoked", func(i db.AgentIdentity) db.AgentIdentity { i.Status = "revoked"; return i }, all, nil, "", CompatTrustFailure, "revoked"},
		{"conflict", func(i db.AgentIdentity) db.AgentIdentity { i.Status = "quarantined"; return i }, all, nil, "", CompatTrustFailure, "two machines"},
		{"expired, still renewable", func(i db.AgentIdentity) db.AgentIdentity {
			i.CertNotAfter = now.Add(-30 * 24 * time.Hour).UnixMilli()
			return i
		}, all, nil, "", CompatOK, ""},
		{"expired past the grace", func(i db.AgentIdentity) db.AgentIdentity {
			i.CertNotAfter = now.Add(-200 * 24 * time.Hour).UnixMilli()
			return i
		}, all, nil, "", CompatTrustFailure, "enroll the Shepherd again"},
	} {
		id := ok
		if c.id != nil {
			id = c.id(ok)
		}
		got := Judge(id, c.features, c.needed, c.update, grace, now)
		if got.Status != c.status || !strings.Contains(got.Summary, c.contains) || got.Missing == nil {
			t.Errorf("%s: %+v", c.name, got)
		}
	}
}
