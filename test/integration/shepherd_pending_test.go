//go:build integration

package integration

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"drool/internal/agent"
	"drool/internal/agentcaps"
)

// TestShepherdNotReportedYet: a Shepherd that has enrolled but not said
// hello yet has no capabilities on record (the moment between `drool-agent
// install` enrolling and its service connecting, which the host page's
// polling can catch). The host page and the Shepherds list say so instead
// of failing (dogfooding, 2026-10-14: "Cannot read properties of undefined
// (reading 'enabled')" right after a successful installation).
func TestShepherdNotReportedYet(t *testing.T) {
	c := freshController(t)
	var tok struct {
		Token string `json:"token"`
	}
	c.must(t, "POST", "/api/v1/agents/tokens", map[string]any{"preset": "managed", "description": "enrolled, not connected"}, &tok)
	p := agent.Under(t.TempDir())
	os.MkdirAll(filepath.Dir(p.Config), 0o755)
	caps, _ := agentcaps.Preset(agentcaps.PresetManaged)
	os.WriteFile(p.Config, agentcaps.Render(caps, agentcaps.PresetManaged), 0o600)
	id, err := agent.Enroll(context.Background(), p, tok.Token, agentV1)
	if err != nil {
		t.Fatal(err)
	}
	// Enrolled, and the service never started: nothing reported yet.
	var ha struct {
		Identity *struct {
			Connected bool           `json:"connected"`
			Effective map[string]any `json:"effective"`
		} `json:"identity"`
	}
	c.must(t, "GET", "/api/v1/hosts/"+id.HostID+"/agent", nil, &ha)
	expect(t, ha.Identity != nil && !ha.Identity.Connected && len(ha.Identity.Effective) == 0,
		"want an enrolled Shepherd that hasn't reported its capabilities: %+v", ha.Identity)
	if !runE2E(t, c, "shepherd-pending", "DROOL_E2E_PENDING_HOST="+id.HostID) {
		t.Skip("no browser here")
	}
}
