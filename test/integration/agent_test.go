//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"drool/internal/release"
)

// The real drool-agent binary in unprivileged containers (no systemd: the
// installer starts its supervisor script, which mirrors the systemd unit).

type hostAgent struct {
	Management struct {
		Preferred  string `json:"preferred"`
		Fallback   string `json:"fallback"`
		Active     string `json:"active"`
		AgentState string `json:"agent_state"`
		Reason     string `json:"reason"`
	} `json:"management"`
	Identity *struct {
		ID           string `json:"id"`
		AgentVersion string `json:"agent_version"`
		UpdateState  string `json:"update_state"`
		UpdateDetail string `json:"update_detail"`
		Connected    bool   `json:"connected"`
	} `json:"identity"`
}

func getHostAgent(t *testing.T, id string) hostAgent {
	t.Helper()
	var a hostAgent
	ctl.must(t, "GET", "/api/v1/hosts/"+id+"/agent", nil, &a)
	return a
}

func waitAgent(t *testing.T, id string, d time.Duration, what string, ok func(hostAgent) bool) hostAgent {
	t.Helper()
	var a hostAgent
	waitFor(t, d, what, func() bool { a = getHostAgent(t, id); return ok(a) })
	return a
}

type jobHostT struct {
	State         string `json:"state"`
	ErrorClass    string `json:"error_class"`
	ErrorMessage  string `json:"error_message"`
	Transport     string `json:"transport"`
	TransportNote string `json:"transport_note"`
}

func runT(t *testing.T, hostID string, step map[string]any) (jobHostT, string) {
	t.Helper()
	id := submit(t, []string{hostID}, step)
	waitJob(t, id, 15*time.Minute)
	var jh jobHostT
	ctl.must(t, "GET", "/api/v1/jobs/"+id+"/hosts/"+hostID, nil, &jh)
	return jh, output(t, id, hostID)
}

// releaseKey is a release-signing key for the test run.
type releaseKey struct {
	line string
	priv []byte
}

func newReleaseKey(t *testing.T) releaseKey {
	pub, priv, err := release.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	return releaseKey{line: release.FormatPublicKey(pub, "integration "+runID), priv: priv}
}

func installAgent(t *testing.T, hostID, preset, keyLine string) {
	t.Helper()
	var res struct {
		OK     bool   `json:"ok"`
		Output string `json:"output"`
		Error  string `json:"error"`
	}
	body := map[string]any{"preset": preset}
	if keyLine != "" {
		body["release_key"] = keyLine
	}
	ctl.must(t, "POST", "/api/v1/hosts/"+hostID+"/agent/install", body, &res)
	if !res.OK {
		t.Fatalf("install agent: %s\n%s", res.Error, res.Output)
	}
}

func stopAgent(t *testing.T, c *container) {
	// The supervisor first (it would restart the agent), then the agent.
	c.exec(t, `pkill -f 'drool-agent-supervis[e]' || true; kill $(cat /var/lib/drool-agent/agent.pid) 2>/dev/null || true`)
}

func startSupervisor(t *testing.T, c *container) {
	mustDocker(t, "exec", "-d", c.Name, "sh", "-c", "exec sh /usr/libexec/drool-agent/drool-agent-supervise >>/var/log/drool-agent/agent.log 2>&1")
}

func TestAgentMatrix(t *testing.T) {
	for _, d := range selected() {
		t.Run(d.Name, func(t *testing.T) {
			t.Parallel()
			testAgentOnDistro(t, d)
		})
	}
}

func testAgentOnDistro(t *testing.T, d distro) {
	short := "agent-" + d.Name
	c := startHost(t, image(t, d), short, true, netOuter)
	h := addHost(t, short, c, hostOpts{})

	// M10: an SSH-only host is observed by snapshots only (ss over SSH, as
	// root through sudo): the controller's own SSH session is there,
	// attributed to sshd and classified as DROOL management.
	t.Run("network: SSH snapshot", func(t *testing.T) {
		var snap struct{ Flows int }
		ctl.must(t, "POST", "/api/v1/hosts/"+h.ID+"/network/snapshot", nil, &snap)
		var p netFlowsT
		ctl.must(t, "GET", "/api/v1/network/flows?host="+h.ID+"&direction=in&port=22", nil, &p)
		if snap.Flows < 1 || len(p.Items) == 0 || p.Items[0].Process != "sshd" || p.Items[0].Source != "snapshot" || p.Items[0].Policy.Status != "management" {
			t.Fatalf("snapshot: %+v %+v", snap, p.Items)
		}
		var hn hostNetT
		ctl.must(t, "GET", "/api/v1/hosts/"+h.ID+"/network", nil, &hn)
		if hn.Collection.Label != "Snapshot only: SSH" {
			t.Fatalf("collection: %+v", hn.Collection)
		}
	})

	t.Run("install over the trusted SSH connection", func(t *testing.T) {
		installAgent(t, h.ID, "full", "")
		a := waitAgent(t, h.ID, time.Minute, "agent connected", func(a hostAgent) bool { return a.Management.Active == "agent" })
		if a.Management.Preferred != "agent" || a.Management.Fallback != "ssh" || a.Identity == nil || a.Identity.AgentVersion != agentV1 {
			t.Fatalf("after install: %+v %+v", a.Management, a.Identity)
		}
		// The private identity key exists only on the host.
		if out := c.exec(t, "stat -c %a /var/lib/drool-agent/identity.key"); out != "600" {
			t.Errorf("identity key mode %s", out)
		}
		if out := c.exec(t, "stat -c '%U %a' /etc/drool/agent.yaml"); out != "root 600" {
			t.Errorf("ceiling file: %s", out)
		}
	})

	t.Run("operations through the agent", func(t *testing.T) {
		jh, out := runT(t, h.ID, cmd("id -un; echo agent-ran", false))
		if jh.State != "succeeded" || jh.Transport != "agent" || !strings.Contains(out, "agent-ran") || !strings.Contains(out, "root") {
			t.Fatalf("command: %+v %q", jh, out)
		}
		jh, out = runT(t, h.ID, map[string]any{"type": "package", "action": "install", "names": []string{"tree"}})
		if jh.State != "succeeded" || jh.Transport != "agent" {
			t.Fatalf("package via agent: %+v\n%s", jh, out)
		}
		// No systemd in the container: the agent reports it like SSH would.
		jh, _ = runT(t, h.ID, svc("cron", "running", nil))
		if jh.Transport != "agent" || jh.ErrorClass != "ConfigurationError" {
			t.Errorf("service via agent without systemd: %+v", jh)
		}
	})

	// M10: the agent collects continuously. An unprivileged container has
	// no CAP_NET_ADMIN, so conntrack events are out of reach: the agent
	// falls back to socket sampling and says why.
	t.Run("network: continuous socket sampling", func(t *testing.T) {
		var hn hostNetT
		waitFor(t, 3*time.Minute, "agent network report", func() bool {
			ctl.must(t, "GET", "/api/v1/hosts/"+h.ID+"/network", nil, &hn)
			return hn.Collection.Source != ""
		})
		if hn.Collection.Mode != "continuous" || hn.Collection.Source != "sockets" || !strings.HasPrefix(hn.Collection.Label, "Continuous: socket sampling") ||
			!strings.Contains(hn.Collection.Detail, "not seen") {
			t.Fatalf("collection: %+v", hn.Collection)
		}
		var p netFlowsT
		waitFor(t, 2*time.Minute, "the agent's own connection", func() bool {
			ctl.must(t, "GET", fmt.Sprintf("/api/v1/network/flows?host=%s&direction=out&port=%d", h.ID, agentPort), nil, &p)
			return len(p.Items) > 0
		})
		if f := p.Items[0]; f.Process != "drool-agent" || f.Source != "sockets" || f.Policy.Status != "management" {
			t.Fatalf("agent connection: %+v", f)
		}
	})

	t.Run("files and deployments through the agent", func(t *testing.T) {
		base := "/api/v1/hosts/" + h.ID + "/files"
		r, err := ctl.raw(context.Background(), "PUT", base+"/content?path="+url.QueryEscape("/etc/drool-agent-test.conf"), strings.NewReader("via agent\n"))
		if err != nil || r.StatusCode != 200 {
			b, _ := io.ReadAll(r.Body)
			t.Fatalf("upload via agent: %v %d %s", err, r.StatusCode, b)
		}
		r.Body.Close()
		if got := c.cat(t, "/etc/drool-agent-test.conf"); got != "via agent" {
			t.Fatalf("content %q", got)
		}
		art := putArtifact(t, "x.conf", "deployed by the agent\n")
		jh, out := runT(t, h.ID, map[string]any{"type": "file_deploy", "artifact": art, "path": "/etc/drool-agent-deploy.conf", "validate": "grep -q agent {path}"})
		if jh.State != "succeeded" || jh.Transport != "agent" {
			t.Fatalf("deploy via agent: %+v\n%s", jh, out)
		}
		// The agent can't reach its own files through file operations.
		r, _ = ctl.raw(context.Background(), "GET", base+"/content?path="+url.QueryEscape("/var/lib/drool-agent/identity.key"), nil)
		if r.StatusCode != 403 {
			t.Errorf("reading the agent's identity key through the agent: %d", r.StatusCode)
		}
		r.Body.Close()
	})

	t.Run("agent-side operation log", func(t *testing.T) {
		runT(t, h.ID, cmd("echo top-secret-value-42 >/dev/null", false))
		log := c.exec(t, "cat /var/log/drool-agent/operations.log")
		if !strings.Contains(log, `"event":"completed"`) || !strings.Contains(log, `"type":"command"`) {
			t.Errorf("operation log: %s", log)
		}
		if strings.Contains(log, "top-secret-value-42") || strings.Contains(log, "deployed by the agent") {
			t.Error("the agent's operation log contains command text or file content")
		}
	})

	t.Run("agent down before dispatch: SSH fallback", func(t *testing.T) {
		stopAgent(t, c)
		waitAgent(t, h.ID, time.Minute, "agent offline", func(a hostAgent) bool { return a.Management.Active == "ssh" })
		jh, out := runT(t, h.ID, cmd("echo via-ssh", false))
		if jh.State != "succeeded" || jh.Transport != "ssh" || !strings.Contains(jh.TransportNote, "fallback") || !strings.Contains(out, "via-ssh") {
			t.Fatalf("fallback: %+v %q", jh, out)
		}
		startSupervisor(t, c)
		waitAgent(t, h.ID, time.Minute, "agent back", func(a hostAgent) bool { return a.Management.Active == "agent" })
	})

	t.Run("local ceiling can't be bypassed", func(t *testing.T) {
		c.exec(t, `sed -i 's/^    shell: true/    shell: false/; s/^  shell: true/  shell: false/' /etc/drool/agent.yaml && grep -q 'shell: false' /etc/drool/agent.yaml`)
		stopAgent(t, c)
		startSupervisor(t, c)
		waitAgent(t, h.ID, time.Minute, "agent back", func(a hostAgent) bool { return a.Management.Active == "agent" })
		jh, _ := runT(t, h.ID, cmd("touch /tmp/should-not-exist", false))
		if jh.State != "failed" || jh.ErrorClass != "CapabilityError" || jh.Transport != "agent" {
			t.Fatalf("command with shell disabled locally: %+v", jh)
		}
		if _, err := docker("exec", c.Name, "test", "-e", "/tmp/should-not-exist"); err == nil {
			t.Fatal("the command ran anyway (over SSH?)")
		}
		c.exec(t, `sed -i 's/shell: false/shell: true/' /etc/drool/agent.yaml`)
		stopAgent(t, c)
		startSupervisor(t, c)
		waitAgent(t, h.ID, time.Minute, "agent back", func(a hostAgent) bool { return a.Management.Active == "agent" })
	})
}

// TestAgentManualEnrollment: a host DROOL has no SSH access to, enrolled
// with the real binary and a token on stdin.
func TestAgentManualEnrollment(t *testing.T) {
	d := selected()[0]
	c := startHost(t, image(t, d), "agent-manual", false, netOuter)
	var tok struct {
		Token string `json:"token"`
	}
	ctl.must(t, "POST", "/api/v1/agents/tokens", map[string]any{"preset": "monitor", "description": "manual"}, &tok)
	mustDocker(t, "cp", filepath.Join(agentDir, "drool-agent-linux-amd64"), c.Name+":/tmp/drool-agent")
	out, err := dockerStdin(c.Name, tok.Token+"\n", "/tmp/drool-agent", "install", "--preset", "monitor", "--token-file", "-")
	if err != nil {
		t.Fatalf("install: %v\n%s", err, out)
	}
	if !strings.Contains(out, "Enrolled as host") || !strings.Contains(out, "systemd is not running") {
		t.Errorf("install output: %s", out)
	}
	// The same token can't enroll a second machine.
	c2 := startHost(t, image(t, d), "agent-manual2", false, netOuter)
	mustDocker(t, "cp", filepath.Join(agentDir, "drool-agent-linux-amd64"), c2.Name+":/tmp/drool-agent")
	if out, err := dockerStdin(c2.Name, tok.Token+"\n", "/tmp/drool-agent", "install", "--preset", "monitor", "--token-file", "-"); err == nil {
		t.Fatalf("token reused: %s", out)
	}
	// The new agent-only host reports telemetry, including agent-observed
	// addresses; monitor-only means no commands.
	var hosts struct {
		Items []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"items"`
	}
	ctl.must(t, "GET", "/api/v1/hosts", nil, &hosts)
	var id string
	for _, h := range hosts.Items {
		if strings.HasPrefix(h.Name, "agent-manual") {
			id = h.ID
		}
	}
	if id == "" {
		t.Fatalf("no agent-only host created: %+v", hosts)
	}
	waitFor(t, time.Minute, "agent addresses", func() bool {
		var hd struct {
			Addresses []struct {
				Source string `json:"source"`
			} `json:"addresses"`
		}
		ctl.must(t, "GET", "/api/v1/hosts/"+id+"/facts", nil, &hd)
		for _, a := range hd.Addresses {
			if a.Source == "agent" {
				return true
			}
		}
		return false
	})
	jh, _ := runT(t, id, cmd("true", false))
	if jh.ErrorClass != "CapabilityError" {
		t.Errorf("command on a monitor-only agent: %+v", jh)
	}
}

func dockerStdin(container, stdin string, args ...string) (string, error) {
	cmd := execCommand("docker", append([]string{"exec", "-i", container}, args...)...)
	cmd.Stdin = strings.NewReader(stdin)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	return out.String(), err
}

// uploadRelease signs and uploads an agent build.
func uploadRelease(t *testing.T, key releaseKey, binFile, version string) (int, string) {
	t.Helper()
	priv, _ := release.ParsePrivateKey(key.priv)
	b, err := os.ReadFile(binFile)
	if err != nil {
		t.Fatal(err)
	}
	s, err := release.Sign(priv, release.Manifest{Version: version, OS: "linux", Arch: "amd64", ProtocolMin: 1, ProtocolMax: 1}, bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	mj, _ := json.Marshal(s)
	req, _ := newRawRequest("POST", "/api/v1/agents/releases", bytes.NewReader(b))
	req.Header.Set("X-DROOL-Release", base64.StdEncoding.EncodeToString(mj))
	r, err := ctl.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	body, _ := io.ReadAll(r.Body)
	return r.StatusCode, string(body)
}

type rolloutT struct {
	ID           string `json:"id"`
	Status       string `json:"status"`
	PausedReason string `json:"paused_reason"`
	Hosts        []struct {
		HostID string `json:"host_id"`
		State  string `json:"state"`
		Detail string `json:"detail"`
	} `json:"hosts"`
}

func getRollout(t *testing.T, id string) rolloutT {
	var r rolloutT
	ctl.must(t, "GET", "/api/v1/agents/rollouts/"+id, nil, &r)
	return r
}

func TestAgentUpdates(t *testing.T) {
	d := selected()[0]
	key := newReleaseKey(t)
	ctl.must(t, "POST", "/api/v1/agents/release-keys", map[string]any{"public_key": key.line}, nil)
	ctl.must(t, "PUT", "/api/v1/agents/updates/policy", map[string]any{"policy": "manual", "channel": "stable", "version": "",
		"canary": 1, "batch_size": 5, "max_failures": 1, "health_window_s": 45}, nil)

	ca := startHost(t, image(t, d), "upd-a", true, netOuter)
	cb := startHost(t, image(t, d), "upd-b", true, netOuter)
	a := addHost(t, "upd-a", ca, hostOpts{})
	b := addHost(t, "upd-b", cb, hostOpts{})
	installAgent(t, a.ID, "managed", key.line)
	installAgent(t, b.ID, "managed", key.line)
	for _, id := range []string{a.ID, b.ID} {
		waitAgent(t, id, time.Minute, "agent", func(x hostAgent) bool { return x.Management.Active == "agent" })
	}

	t.Run("controller refuses a binary the manifest doesn't sign", func(t *testing.T) {
		priv, _ := release.ParsePrivateKey(key.priv)
		s, _ := release.Sign(priv, release.Manifest{Version: "v9.9.9", OS: "linux", Arch: "amd64", ProtocolMin: 1, ProtocolMax: 1}, strings.NewReader("other bytes"))
		mj, _ := json.Marshal(s)
		bin, _ := os.ReadFile(filepath.Join(releaseDir, "drool-agent-"+agentV2))
		req, _ := newRawRequest("POST", "/api/v1/agents/releases", bytes.NewReader(bin))
		req.Header.Set("X-DROOL-Release", base64.StdEncoding.EncodeToString(mj))
		r, _ := ctl.client.Do(req)
		if r.StatusCode != 422 {
			t.Errorf("altered binary accepted by the controller: %d", r.StatusCode)
		}
		r.Body.Close()
	})

	t.Run("signed update, health-confirmed, offline host catches up", func(t *testing.T) {
		if st, body := uploadRelease(t, key, filepath.Join(releaseDir, "drool-agent-"+agentV2), agentV2); st != 201 {
			t.Fatalf("upload release: %d %s", st, body)
		}
		stopAgent(t, cb) // b is offline when the rollout starts
		waitAgent(t, b.ID, time.Minute, "b offline", func(x hostAgent) bool { return x.Identity != nil && !x.Identity.Connected })
		var ro rolloutT
		ctl.must(t, "POST", "/api/v1/agents/rollouts", map[string]any{"version": agentV2, "host_ids": []string{a.ID, b.ID}}, &ro)
		waitAgent(t, a.ID, 3*time.Minute, "a updated", func(x hostAgent) bool {
			return x.Identity != nil && x.Identity.AgentVersion == agentV2 && x.Identity.Connected
		})
		waitFor(t, 2*time.Minute, "a healthy in the rollout", func() bool {
			for _, h := range getRollout(t, ro.ID).Hosts {
				if h.HostID == a.ID && h.State == "healthy" {
					return true
				}
			}
			return false
		})
		if out := ca.exec(t, "readlink /usr/libexec/drool-agent/current; readlink /usr/libexec/drool-agent/previous"); out != "versions/"+agentV2+"\nversions/"+agentV1 {
			t.Errorf("version links: %q", out)
		}
		if r := getRollout(t, ro.ID); r.Status != "running" {
			t.Errorf("rollout should wait for the offline host: %+v", r)
		}
		startSupervisor(t, cb)
		waitAgent(t, b.ID, 3*time.Minute, "b caught up", func(x hostAgent) bool {
			return x.Identity != nil && x.Identity.AgentVersion == agentV2 && x.Identity.Connected
		})
		waitFor(t, 2*time.Minute, "rollout completed", func() bool { return getRollout(t, ro.ID).Status == "completed" })
	})

	t.Run("broken signed update rolls back and pauses the rollout", func(t *testing.T) {
		if st, body := uploadRelease(t, key, filepath.Join(releaseDir, "drool-agent-"+agentBroken), agentBroken); st != 201 {
			t.Fatalf("upload broken release: %d %s", st, body)
		}
		var ro rolloutT
		ctl.must(t, "POST", "/api/v1/agents/rollouts", map[string]any{"version": agentBroken, "host_ids": []string{a.ID, b.ID}}, &ro)
		waitFor(t, 5*time.Minute, "rollback reported", func() bool {
			r := getRollout(t, ro.ID)
			return r.Status == "paused"
		})
		r := getRollout(t, ro.ID)
		failed := 0
		for _, h := range r.Hosts {
			if h.State == "rolled_back" || h.State == "failed" {
				failed++
				if h.Detail == "" {
					t.Errorf("rollback detail: %q", h.Detail)
				}
			}
		}
		if failed != 1 {
			t.Fatalf("expected exactly the canary to fail, then a pause: %+v", r)
		}
		// Back on the previous version, connected; the other host untouched.
		for _, id := range []string{a.ID, b.ID} {
			waitAgent(t, id, 3*time.Minute, "agent on "+agentV2, func(x hostAgent) bool {
				return x.Identity != nil && x.Identity.AgentVersion == agentV2 && x.Identity.Connected
			})
		}
		for _, c := range []*container{ca, cb} {
			if out := c.exec(t, "readlink /usr/libexec/drool-agent/current"); out != "versions/"+agentV2 {
				t.Errorf("%s current: %s", c.Name, out)
			}
		}
		ctl.must(t, "POST", "/api/v1/agents/rollouts/"+ro.ID+"/cancel", nil, nil)
	})

	t.Run("a release signed by a key the hosts don't trust is refused by the agent", func(t *testing.T) {
		other := newReleaseKey(t)
		ctl.must(t, "POST", "/api/v1/agents/release-keys", map[string]any{"public_key": other.line}, nil)
		bin := filepath.Join(releaseDir, "drool-agent-"+agentV2)
		if st, body := uploadRelease(t, other, bin, "v1.3.0"); st != 201 {
			t.Fatalf("upload: %d %s", st, body)
		}
		var ro rolloutT
		ctl.must(t, "POST", "/api/v1/agents/rollouts", map[string]any{"version": "v1.3.0", "host_ids": []string{a.ID}}, &ro)
		waitFor(t, 2*time.Minute, "refusal", func() bool {
			r := getRollout(t, ro.ID)
			return len(r.Hosts) == 1 && r.Hosts[0].State == "failed"
		})
		if r := getRollout(t, ro.ID); !strings.Contains(r.Hosts[0].Detail, "does not trust") {
			t.Errorf("refusal detail: %+v", r.Hosts[0])
		}
		if got := getHostAgent(t, a.ID).Identity.AgentVersion; got != agentV2 {
			t.Errorf("version changed to %s", got)
		}
	})
}

var _ = fmt.Sprint

// Network observation views (M10), as the API returns them.
type netFlowsT struct {
	Items []struct {
		Direction string `json:"direction"`
		Port      int    `json:"port"`
		Process   string `json:"process"`
		Source    string `json:"source"`
		Count     int64  `json:"count"`
		Policy    struct {
			Status string `json:"status"`
		} `json:"policy"`
	} `json:"items"`
}

type hostNetT struct {
	Collection struct{ Mode, Source, Label, Detail string } `json:"collection"`
}
