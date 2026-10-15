//go:build integration

package integration

import (
	"strings"
	"testing"
	"time"
)

// TestShepherdUninstallOnHost (final After-M17 batch): a real Shepherd,
// installed over SSH into an unprivileged container (no systemd: it runs
// under its supervisor script), is removed again from the UI's endpoint.
// The real removal script stops the supervisor and the agent and deletes its
// programs, configuration, identity and log; DROOL revokes the identity and
// manages the host over SSH, which keeps working; a second removal finds
// nothing left.
func TestShepherdUninstallOnHost(t *testing.T) {
	freshController(t)
	d := selected()[0]
	c := startHost(t, image(t, d), "uninst", true, netOuter)
	h := addHost(t, "uninst", c, hostOpts{})
	installAgent(t, h.ID, "managed", "")
	waitAgent(t, h.ID, time.Minute, "Shepherd connected", func(a hostAgent) bool { return a.Identity != nil && a.Identity.Connected })
	expect(t, strings.Contains(c.exec(t, "ls /var/lib/drool-agent"), "identity.key"), "no identity on the host before")
	agentProcs := func() string {
		return strings.TrimSpace(c.exec(t, `for d in /proc/[0-9]*; do e=$(readlink "$d/exe" 2>/dev/null); case "$e" in /usr/libexec/drool-agent/*) echo "${d#/proc/}";; esac; done`))
	}
	expect(t, agentProcs() != "", "no Shepherd process before")

	var plan struct {
		CanUninstall bool   `json:"can_uninstall"`
		Systemd      string `json:"systemd"`
		Processes    int    `json:"processes"`
		Items        []struct {
			Path    string `json:"path"`
			Present bool   `json:"present"`
		} `json:"items"`
	}
	ctl.must(t, "POST", "/api/v1/hosts/"+h.ID+"/agent/uninstall/plan", nil, &plan)
	present := map[string]bool{}
	for _, it := range plan.Items {
		present[it.Path] = it.Present
	}
	expect(t, plan.CanUninstall && plan.Systemd == "none" && plan.Processes >= 2, "plan: %+v", plan)
	expect(t, present["/usr/libexec/drool-agent"] && present["/var/lib/drool-agent"] && present["/etc/drool/agent.yaml"] &&
		!present["/etc/systemd/system/drool-agent.service"], "plan items: %+v", plan.Items)

	var res struct {
		Outcome         string   `json:"outcome"`
		Removed         []string `json:"removed"`
		Remaining       []string `json:"remaining"`
		IdentityRevoked bool     `json:"identity_revoked"`
		Management      string   `json:"management"`
		Message         string   `json:"message"`
		Output          string   `json:"output"`
	}
	// From the host's page in a browser, when there is one; otherwise the
	// same endpoint directly.
	if !runE2E(t, ctl, "shepherd-uninstall", "DROOL_E2E_HOST="+h.ID) {
		t.Log("no browser here: uninstalling through the API")
		ctl.must(t, "POST", "/api/v1/hosts/"+h.ID+"/agent/uninstall", nil, &res)
		expect(t, res.Outcome == "uninstalled" && res.IdentityRevoked && res.Management == "ssh" && len(res.Remaining) == 0,
			"uninstall: %+v", res)
	}
	// On the host: nothing of it left, nothing running.
	expect(t, agentProcs() == "", "Shepherd processes left: %s", agentProcs())
	left := c.exec(t, `for p in /usr/libexec/drool-agent /var/lib/drool-agent /var/log/drool-agent /etc/drool/agent.yaml /etc/drool/release-keys.d /etc/drool; do [ -e "$p" ] && echo "$p"; done; true`)
	expect(t, strings.TrimSpace(left) == "", "left on the host: %s", left)
	// In DROOL: revoked, managed over SSH, which works.
	a := waitAgent(t, h.ID, 30*time.Second, "managed over SSH", func(a hostAgent) bool { return a.Management.Preferred == "ssh" })
	expect(t, a.Management.Active == "ssh" && (a.Identity == nil || !a.Identity.Connected), "after: %+v", a)
	jh, out := runT(t, h.ID, cmd("echo still-managed", false))
	expect(t, jh.State == "succeeded" && jh.Transport == "ssh" && strings.Contains(out, "still-managed"), "over SSH: %+v %s", jh, out)
	// Again: nothing left.
	ctl.must(t, "POST", "/api/v1/hosts/"+h.ID+"/agent/uninstall", nil, &res)
	expect(t, res.Outcome == "nothing", "again: %+v", res)
}
