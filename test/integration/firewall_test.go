//go:build integration

package integration

import (
	"strings"
	"testing"
	"time"
)

type fwObsT struct {
	Direction  string `json:"direction"`
	Protocol   string `json:"protocol"`
	Port       int    `json:"port"`
	Peer       string `json:"peer"`
	Source     string `json:"source"`
	Count      int64  `json:"count"`
	Classified bool   `json:"classified"`
}

func fwObservations(t *testing.T, hostID string) []fwObsT {
	var r struct {
		Items []fwObsT `json:"items"`
	}
	ctl.must(t, "GET", "/api/v1/firewall/hosts/"+hostID+"/observations", nil, &r)
	return r.Items
}

// TestFirewallContainers: containers have no systemd, so enforcement is
// refused with a reason; inspection, snapshots, learning and simulation
// still work (DESIGN §16.5: only enforcement is gated).
func TestFirewallContainers(t *testing.T) {
	d := selected()[0]
	c := startHost(t, image(t, d), "fw-ct", true, netOuter)
	h := addHost(t, "fw-ct", c, hostOpts{})

	t.Run("refused without systemd; nothing installed", func(t *testing.T) {
		sup, why := fwDetect(t, h.ID)
		if sup || !strings.Contains(why, "systemd is not running") {
			t.Fatalf("detect: %v %s", sup, why)
		}
		ctl.must(t, "PUT", "/api/v1/firewall/hosts/"+h.ID, map[string]any{"mode": "enforce"}, nil)
		jh, _ := fwApply(t, h.ID, nil)
		if jh.State != "failed" || jh.ErrorClass != "ConfigurationError" || !strings.Contains(jh.ErrorMessage, "systemd is not running") {
			t.Fatalf("apply: %v", jh)
		}
		if out := c.exec(t, "ls /usr/libexec/drool-firewall /var/lib/drool 2>&1 || true"); !strings.Contains(out, "No such file") {
			t.Errorf("tooling installed on a refused host: %s", out)
		}
	})

	t.Run("SSH host: connection snapshot, not learning", func(t *testing.T) {
		ctl.must(t, "PUT", "/api/v1/firewall/hosts/"+h.ID, map[string]any{"mode": "monitor"}, nil)
		var plan struct {
			Learning string `json:"learning"`
		}
		ctl.must(t, "GET", "/api/v1/firewall/hosts/"+h.ID, nil, &plan)
		if plan.Learning != "snapshot" {
			t.Errorf("learning label for an SSH host: %q", plan.Learning)
		}
		var snap struct {
			Flows int `json:"flows"`
		}
		ctl.must(t, "POST", "/api/v1/firewall/hosts/"+h.ID+"/snapshot", nil, &snap)
		found := false
		for _, o := range fwObservations(t, h.ID) {
			if o.Direction == "in" && o.Port == 22 && o.Source == "snapshot" {
				found = true
			}
		}
		if snap.Flows == 0 || !found {
			t.Fatalf("snapshot: %d flows, %+v", snap.Flows, fwObservations(t, h.ID))
		}
	})

	t.Run("agent host: continuous learning and Guided suggestions", func(t *testing.T) {
		c2 := startHost(t, image(t, d), "fw-agent", true, netOuter)
		h2 := addHost(t, "fw-agent", c2, hostOpts{})
		installAgent(t, h2.ID, "full", "")
		waitAgent(t, h2.ID, time.Minute, "agent", func(a hostAgent) bool { return a.Management.Active == "agent" })
		ctl.must(t, "PUT", "/api/v1/firewall/hosts/"+h2.ID, map[string]any{"mode": "guided"}, nil)
		// Something to observe: a listener and a client inside the container.
		c2.exec(t, `ip=$(hostname -i | cut -d' ' -f1); for i in 1 2 3; do bash -c "exec 3<>/dev/tcp/$ip/22; sleep 300" >/dev/null 2>&1 & done; true`)
		waitFor(t, 4*time.Minute, "agent-learned flows", func() bool {
			for _, o := range fwObservations(t, h2.ID) {
				if (o.Source == "sockets" || o.Source == "conntrack") && o.Port == 22 && o.Direction == "in" && o.Count >= 3 {
					return true
				}
			}
			return false
		})
		var sg struct {
			Items []struct {
				Title string `json:"title"`
			} `json:"items"`
		}
		ctl.must(t, "GET", "/api/v1/firewall/hosts/"+h2.ID+"/suggestions", nil, &sg)
		ok := false
		for _, s := range sg.Items {
			ok = ok || strings.HasSuffix(s.Title, " ssh")
		}
		if !ok {
			t.Errorf("no suggestion for the observed service: %+v", sg.Items)
		}
		// Agent hosts without systemd are refused too, through the agent.
		ctl.must(t, "PUT", "/api/v1/firewall/hosts/"+h2.ID, map[string]any{"mode": "enforce"}, nil)
		jh, _ := fwApply(t, h2.ID, nil)
		if jh.State != "failed" || jh.Transport != "agent" || !strings.Contains(jh.ErrorMessage, "systemd") {
			t.Fatalf("agent host without systemd: %v", jh)
		}
	})
}
