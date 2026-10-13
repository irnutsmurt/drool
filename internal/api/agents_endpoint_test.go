package api

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"drool/internal/agent"
	"drool/internal/agentcaps"
	"drool/internal/agentrpc"
	"drool/internal/auth"
	"drool/internal/sshx/sshtest"
)

// Where Shepherds are told to connect when the controller runs in a
// container whose Shepherd port is published under another host port
// (Docker's 2043:9443): agent.public_port, not the port it listens on.
// A TCP forwarder plays Docker's port publishing.

// publishedPort is a listener standing in for a port Docker publishes:
// what connects to it reaches target. The port is known before the
// controller starts (it goes into its configuration).
type publishedPort struct {
	ln   net.Listener
	port int
}

func newPublishedPort(t *testing.T) *publishedPort {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	return &publishedPort{ln: ln, port: ln.Addr().(*net.TCPAddr).Port}
}

func (p *publishedPort) forwardTo(target string) {
	go func() {
		for {
			c, err := p.ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				u, err := net.Dial("tcp", target)
				if err != nil {
					return
				}
				defer u.Close()
				go io.Copy(u, c)
				io.Copy(c, u)
			}()
		}
	}()
}

// closedPort is a port nothing listens on: connecting is refused.
func closedPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	return port
}

// shepherdHost is an in-process SSH host that DROOL installs a Shepherd on.
// `drool-agent install` enrolls a real agent with the token DROOL hands
// over, at the endpoints the token names, and starts it: exactly where a
// Shepherd would connect. If enrolling fails, it fails like the real one.
type shepherdHost struct {
	t     *testing.T
	srv   *sshtest.Server
	key   ssh.Signer
	paths agent.Paths

	mu        sync.Mutex
	endpoints []string // in the token the host was given
	stopped   bool     // the rollback stopped and disabled the service
}

func newShepherdHost(t *testing.T) *shepherdHost {
	caps, _ := agentcaps.Preset(agentcaps.PresetManaged)
	sh := &shepherdHost{t: t, key: sshtest.NewEd25519Signer(t), paths: agentPaths(t, caps)}
	sh.srv = sshtest.Start(t, sh.key)
	sh.srv.SetExec(sh.exec)
	return sh
}

const installDir = "/tmp/drool-agent-install.test"

func (sh *shepherdHost) exec(cmd string, stdin []byte) (string, string, int) {
	root, asRoot := strings.CutPrefix(cmd, "sudo -n -- ")
	switch {
	case cmd == "uname -s; uname -m":
		return "Linux\nx86_64\n", "", 0
	case strings.HasPrefix(cmd, `d=$(mktemp -d`):
		return fmt.Sprintf("%x  %s/drool-agent\n%s\n", sha256.Sum256(stdin), installDir, installDir), "", 0
	case asRoot && strings.HasPrefix(root, "'"+installDir+"/drool-agent' install --token-file -"):
		token := strings.TrimSpace(string(stdin))
		b, err := agentrpc.ParseBundle(token)
		if err != nil {
			return "", "drool-agent: " + err.Error() + "\n", 1
		}
		sh.mu.Lock()
		sh.endpoints = b.Endpoints
		sh.mu.Unlock()
		if _, err := agent.Enroll(context.Background(), sh.paths, token, "v1.0.0"); err != nil {
			return "", "drool-agent: " + err.Error() + "\n", 1
		}
		startAgent(sh.t, sh.paths, "v1.0.0")
		return "Enrolled.\n", "", 0
	case asRoot && strings.HasPrefix(root, "systemctl disable --now drool-agent.service"):
		sh.mu.Lock()
		sh.stopped = true
		sh.mu.Unlock()
		return "", "", 0
	case strings.HasPrefix(cmd, "rm -rf -- '"+installDir):
		return "", "", 0
	}
	return linuxExec(cmd, stdin)
}

// add adds the host to the controller over SSH and trusts its key.
func (sh *shepherdHost) add(t *testing.T, c *client, name string) string {
	t.Helper()
	var ck controllerKeyResponse
	c.do("GET", "/api/v1/secrets/controller-ssh-key", nil).json(t, &ck)
	authorizeCred(t, sh.srv, ck.PublicKey)
	addr, port := sh.srv.Host()
	var host hostJSON
	c.do("POST", "/api/v1/hosts", map[string]any{"name": name, "address": addr, "port": port, "user": "deploy"}).json(t, &host)
	c.do("POST", "/api/v1/hosts/"+host.ID+"/hostkeys/scan", nil)
	if r := c.do("POST", "/api/v1/hosts/"+host.ID+"/hostkeys/approve", map[string]string{"fingerprint": ssh.FingerprintSHA256(sh.key.PublicKey())}); r.status != 200 {
		t.Fatalf("approve: %s", r.body)
	}
	return host.ID
}

// fakeAgentBinaries gives the controller something to upload.
func fakeAgentBinaries(t *testing.T, h *harness) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "drool-agent-linux-amd64"), []byte("#!/bin/sh\n# a stand-in Shepherd binary\n"), 0o755)
	h.agents.SetBinariesDir(dir)
}

type installResult struct {
	OK     bool   `json:"ok"`
	Output string `json:"output"`
	Error  string `json:"error"`
}

type advertisedJSON struct {
	EndpointPort        string   `json:"endpoint_port"`
	AdvertisedEndpoints []string `json:"advertised_endpoints"`
	AdvertisedFrom      string   `json:"advertised_from"`
	AdvertisedPort      int      `json:"advertised_port"`
}

func advertised(t *testing.T, c *client) advertisedJSON {
	t.Helper()
	var a advertisedJSON
	c.do("GET", "/api/v1/agents/pki", nil).json(t, &a)
	return a
}

// The Synology case: the container listens on 9443, Docker publishes it
// as another port, agent.public_port says which. The endpoint shown, the
// enrollment token and the installation over SSH all use the published
// port, and the Shepherd connects through it.
func TestShepherdInstallUsesThePublishedPort(t *testing.T) {
	pub := newPublishedPort(t)
	h := newHarnessWith(t, harnessOpts{env: []string{"DROOL_AGENT_PUBLIC_PORT=" + strconv.Itoa(pub.port)}})
	h.mkUser("admin", auth.RoleAdmin)
	c := h.login("admin")
	ln, err := net.Listen("tcp", "127.0.0.1:0") // the endpoint inside the container
	if err != nil {
		t.Fatal(err)
	}
	h.agents.Serve(ln)
	t.Cleanup(h.agents.Stop)
	pub.forwardTo(ln.Addr().String())
	fakeAgentBinaries(t, h)
	want := "127.0.0.1:" + strconv.Itoa(pub.port) // this test's web address, with the published port

	// The controller still listens on its own port; Shepherds are told the
	// published one.
	if a := advertised(t, c); a.EndpointPort != "9443" || strings.Join(a.AdvertisedEndpoints, ",") != want || a.AdvertisedFrom != "web_address" ||
		a.AdvertisedPort != pub.port {
		t.Fatalf("advertised: %+v, want %s", a, want)
	}
	tok := createdToken(t, c)
	if strings.Join(tok.Endpoints, ",") != want {
		t.Fatalf("token endpoints %v, want %s", tok.Endpoints, want)
	}
	if b, err := agentrpc.ParseBundle(tok.Token); err != nil || strings.Join(b.Endpoints, ",") != want {
		t.Fatalf("the token itself names %v (%v), want %s", b.Endpoints, err, want)
	}

	sh := newShepherdHost(t)
	id := sh.add(t, c, "nas-shepherd")
	var res installResult
	c.do("POST", "/api/v1/hosts/"+id+"/agent/install", map[string]any{"preset": "managed"}).json(t, &res)
	if !res.OK || !strings.Contains(res.Output, "agent endpoint "+want) || !strings.Contains(res.Output, "The agent is connected") {
		t.Fatalf("install: %+v", res)
	}
	sh.mu.Lock()
	got := strings.Join(sh.endpoints, ",")
	sh.mu.Unlock()
	if got != want {
		t.Fatalf("the host was handed %s, want %s", got, want)
	}
	var ha struct {
		Identity *struct {
			Connected bool `json:"connected"`
		} `json:"identity"`
	}
	c.do("GET", "/api/v1/hosts/"+id+"/agent", nil).json(t, &ha)
	if ha.Identity == nil || !ha.Identity.Connected {
		t.Fatalf("the Shepherd isn't connected: %+v", ha)
	}
}

type createdTokenResp struct {
	ID        string   `json:"id"`
	Token     string   `json:"token"`
	Endpoints []string `json:"endpoints"`
}

func createdToken(t *testing.T, c *client) createdTokenResp {
	t.Helper()
	r := c.do("POST", "/api/v1/agents/tokens", map[string]any{"description": "endpoint test"})
	if r.status != 201 {
		t.Fatalf("token: %d %s", r.status, r.body)
	}
	var tr createdTokenResp
	r.json(t, &tr)
	return tr
}

// A published port nothing answers on (the reported case: the controller
// said 9443, the NAS published 2043): the installation fails as before,
// and is rolled back exactly as before. The service is stopped, the
// identity and the token are revoked, and the host stays managed over SSH.
func TestShepherdInstallRollsBackWhenTheEndpointRefuses(t *testing.T) {
	refused := closedPort(t)
	h := newHarnessWith(t, harnessOpts{env: []string{"DROOL_AGENT_PUBLIC_PORT=" + strconv.Itoa(refused)}})
	h.mkUser("admin", auth.RoleAdmin)
	c := h.login("admin")
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	h.agents.Serve(ln)
	t.Cleanup(h.agents.Stop)
	fakeAgentBinaries(t, h)
	want := "127.0.0.1:" + strconv.Itoa(refused)

	sh := newShepherdHost(t)
	id := sh.add(t, c, "nas-shepherd")
	var res installResult
	c.do("POST", "/api/v1/hosts/"+id+"/agent/install", map[string]any{"preset": "managed"}).json(t, &res)
	if res.OK || !strings.Contains(res.Output, "agent endpoint "+want) || !strings.Contains(res.Output, "connection refused") ||
		!strings.Contains(res.Output, "Rolled back: the agent service was stopped; the host remains managed over SSH.") {
		t.Fatalf("install: %+v", res)
	}
	sh.mu.Lock()
	stopped := sh.stopped
	sh.mu.Unlock()
	if !stopped {
		t.Fatal("the rollback didn't stop the service")
	}
	var ha struct {
		Management struct {
			Active string `json:"active"`
		} `json:"management"`
		Identity *struct{} `json:"identity"`
	}
	c.do("GET", "/api/v1/hosts/"+id+"/agent", nil).json(t, &ha)
	if ha.Identity != nil || ha.Management.Active != "ssh" {
		t.Fatalf("after the rollback: %+v", ha)
	}
	var toks struct {
		Items []struct {
			Description string `json:"description"`
			State       string `json:"state"`
		} `json:"items"`
	}
	c.do("GET", "/api/v1/agents/tokens", nil).json(t, &toks)
	for _, tk := range toks.Items {
		if tk.Description == "installed over SSH" && tk.State != "revoked" {
			t.Fatalf("the installation's token is %s", tk.State)
		}
	}
	if n := countAudit(t, h, "agent.install"); n != 1 {
		t.Fatalf("%d agent.install audit records", n)
	}
}

// Explicit agent.advertise wins over the web address; an entry without a
// port gets the public port, one with a port is used as it is. Shown,
// tokens and installation all agree.
func TestAdvertisedEndpointPrecedence(t *testing.T) {
	h := newHarnessWith(t, harnessOpts{env: []string{"DROOL_AGENT_PUBLIC_PORT=2043"}})
	h.mkUser("admin", auth.RoleAdmin)
	c := h.login("admin")
	for _, tc := range []struct {
		advertise []string
		want      string
		from      string
	}{
		{nil, "127.0.0.1:2043", "web_address"},
		{[]string{"nas.example.net"}, "nas.example.net:2043", "advertise"},
		{[]string{"drool.example.net:7000", "10.10.0.10"}, "drool.example.net:7000,10.10.0.10:2043", "advertise"},
		{[]string{"fd00::10"}, "[fd00::10]:2043", "advertise"},
	} {
		h.agents.SetAdvertise(tc.advertise)
		a := advertised(t, c)
		if strings.Join(a.AdvertisedEndpoints, ",") != tc.want || a.AdvertisedFrom != tc.from || a.EndpointPort != "9443" || a.AdvertisedPort != 2043 {
			t.Errorf("advertise %v: %+v, want %s (%s)", tc.advertise, a, tc.want, tc.from)
		}
		if tok := createdToken(t, c); strings.Join(tok.Endpoints, ",") != tc.want {
			t.Errorf("advertise %v: token endpoints %v, want %s", tc.advertise, tok.Endpoints, tc.want)
		}
	}
}

// Converting many SSH hosts at once (Monitoring → Convert to Shepherd)
// gives each host the endpoint that installing it from its page would:
// the address the conversion was started from, with the published port.
// The conversion runs in the background, after the request that started
// it, so the rollout keeps that address.
func TestBulkConversionUsesThePublishedPort(t *testing.T) {
	pub := newPublishedPort(t)
	h := newHarnessWith(t, harnessOpts{env: []string{"DROOL_AGENT_PUBLIC_PORT=" + strconv.Itoa(pub.port)}})
	h.mkUser("admin", auth.RoleAdmin)
	c := h.login("admin")
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	h.agents.Serve(ln)
	t.Cleanup(h.agents.Stop)
	pub.forwardTo(ln.Addr().String())
	fakeAgentBinaries(t, h)
	want := "127.0.0.1:" + strconv.Itoa(pub.port)

	sh := newShepherdHost(t)
	id := sh.add(t, c, "nas-convert")
	h.st.W.UpsertHostFacts(context.Background(), factsWithPkg(id, "apt"))
	h.st.Writer.Exec(`UPDATE hosts SET status = 'ready', become_method = 'sudo' WHERE id = ?`, id)
	var start struct {
		RolloutID string `json:"rollout_id"`
	}
	if r := c.do("POST", "/api/v1/agents/conversions", map[string]any{"host_ids": []string{id}, "preset": "managed"}); r.status != 201 {
		t.Fatalf("start: %d %s", r.status, r.body)
	} else {
		r.json(t, &start)
	}
	var state, detail string
	eventually(t, time.Minute, "the conversion finished", func() bool {
		h.st.Reader.QueryRow(`SELECT state, detail FROM agent_rollout_hosts WHERE rollout_id = ? AND host_id = ?`, start.RolloutID, id).Scan(&state, &detail)
		return state == "healthy" || state == "failed"
	})
	sh.mu.Lock()
	got := strings.Join(sh.endpoints, ",")
	sh.mu.Unlock()
	if state != "healthy" || got != want {
		t.Fatalf("converted %s (%s), the host was handed %s; want healthy and %s", state, detail, got, want)
	}
}

// A new controller address (Shepherds → Controller address) without a port
// gets the port Shepherds are told now: the public one, not the listener's.
func TestControllerAddressGetsThePublicPort(t *testing.T) {
	h := newHarnessWith(t, harnessOpts{env: []string{"DROOL_AGENT_PUBLIC_PORT=2043"}})
	h.mkUser("admin", auth.RoleAdmin)
	c := h.login("admin")
	for in, want := range map[string]string{"nas.example.net": "nas.example.net:2043", "nas.example.net:7000": "nas.example.net:7000"} {
		if r := c.do("POST", "/api/v1/agents/endpoints", map[string]any{"endpoint": in}); r.status != 201 {
			t.Fatalf("%s: %d %s", in, r.status, r.body)
		}
		var l struct {
			Items []struct {
				Endpoint string `json:"endpoint"`
			} `json:"items"`
		}
		c.do("GET", "/api/v1/agents/endpoints", nil).json(t, &l)
		found := false
		for _, m := range l.Items {
			found = found || m.Endpoint == want
		}
		if !found {
			t.Fatalf("%s: proposed as %+v, want %s", in, l.Items, want)
		}
	}
	if r := c.do("POST", "/api/v1/agents/endpoints", map[string]any{"endpoint": "https://nas.example.net"}); r.status != 422 ||
		!strings.Contains(string(r.body), "another port than 2043") {
		t.Fatalf("a URL: %d %s", r.status, r.body)
	}
}
