//go:build integration

package integration

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// composeRender is what `docker compose config` makes of compose.yaml.
type composeRender struct {
	Image string
	Ports []string // published:target
	Env   map[string]string
}

func renderCompose(t *testing.T, dir string, env ...string) composeRender {
	t.Helper()
	cmd := exec.Command("docker", "compose", "-p", "drool-it-"+runID+"-render", "config", "--format", "json")
	cmd.Dir = dir
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "DROOL_") && !strings.HasPrefix(kv, "COMPOSE_") {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	cmd.Env = append(cmd.Env, env...)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("docker compose config: %v\n%s", err, out)
	}
	var cfg struct {
		Services map[string]struct {
			Image string `json:"image"`
			Ports []struct {
				Published string `json:"published"`
				Target    int    `json:"target"`
			} `json:"ports"`
			Environment map[string]string `json:"environment"`
		} `json:"services"`
	}
	if err := json.Unmarshal(out, &cfg); err != nil {
		t.Fatal(err)
	}
	s := cfg.Services["drool"]
	r := composeRender{Image: s.Image, Env: s.Environment}
	for _, p := range s.Ports {
		r.Ports = append(r.Ports, fmt.Sprintf("%s:%d", p.Published, p.Target))
	}
	return r
}

// TestComposeFilePorts: the shipped compose.yaml as Docker Compose reads
// it. Without anything set: the GitHub development image, 8080:8080 and
// 9443:9443, and DROOL told 9443. One value, DROOL_AGENT_PUBLIC_PORT (from
// the environment, from .env, or edited in the file the way a Synology
// user edits the Project YAML), publishes 2043:9443 and tells DROOL 2043.
// The container port, DROOL's listener, stays 9443.
func TestComposeFilePorts(t *testing.T) {
	src, err := os.ReadFile(filepath.Join(root, "compose.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "compose.yaml"), src, 0o644)
	check := func(what string, r composeRender, ports, shepherdPort string) {
		t.Helper()
		expect(t, r.Image == "ghcr.io/irnutsmurt/drool:main", "%s: image %s", what, r.Image)
		expect(t, strings.Join(r.Ports, " ") == ports, "%s: ports %v, want %s", what, r.Ports, ports)
		expect(t, r.Env["DROOL_AGENT_PUBLIC_PORT"] == shepherdPort && r.Env["DROOL_SERVER_PUBLIC_PORT"] == "8080" &&
			r.Env["DROOL_AGENT_PORT"] == "", "%s: environment %v", what, r.Env)
	}
	check("defaults", renderCompose(t, dir), "8080:8080 9443:9443", "9443")
	check("DROOL_AGENT_PUBLIC_PORT=2043", renderCompose(t, dir, "DROOL_AGENT_PUBLIC_PORT=2043"), "8080:8080 2043:9443", "2043")
	os.WriteFile(filepath.Join(dir, ".env"), []byte("DROOL_AGENT_PUBLIC_PORT=2043\n"), 0o644)
	check(".env", renderCompose(t, dir), "8080:8080 2043:9443", "2043")
	os.Remove(filepath.Join(dir, ".env"))
	// The one line a GUI user changes, written as the Synology guide shows it.
	line := regexp.MustCompile(`(?m)^(\s*shepherds: &shepherd-port \$\{DROOL_AGENT_PUBLIC_PORT:-)9443(\})$`)
	expect(t, line.Match(src), "compose.yaml has no Shepherd port line to edit")
	os.WriteFile(filepath.Join(dir, "compose.yaml"), line.ReplaceAll(src, []byte("${1}2043${2}")), 0o644)
	check("the file edited", renderCompose(t, dir), "8080:8080 2043:9443", "2043")
	web := regexp.MustCompile(`(?m)^(\s*web: &web-port )8080$`)
	expect(t, web.Match(src), "compose.yaml has no web port line to edit")
	os.WriteFile(filepath.Join(dir, "compose.yaml"), web.ReplaceAll(src, []byte("${1}8443")), 0o644)
	r := renderCompose(t, dir)
	expect(t, strings.Join(r.Ports, " ") == "8443:8080 9443:9443" && r.Env["DROOL_SERVER_PUBLIC_PORT"] == "8443", "web port edited: %+v", r)
}

// TestShepherdThroughPublishedPort is the reported Synology case end to
// end: this tree's container image run from the shipped compose.yaml, its
// Shepherd port published as another host port (as Synology's
// Container Manager does with 2043:9443) through DROOL_AGENT_PUBLIC_PORT,
// a host added over SSH, and a Shepherd installed on it. DROOL keeps
// listening on 9443 inside the container; the endpoint it shows, puts in
// the token and hands the host is the published one, and the Shepherd
// connects through it. Both ports are published on the test network's
// gateway only (never on the workstation's network), which is also the
// address the "browser" uses, like the NAS's address in the report.
func TestShepherdThroughPublishedPort(t *testing.T) {
	ctlImage, composeFile := imageUnderTest(t)
	p := newComposeProject(t, ctlImage, composeFile, "pubport")
	pub := 2043 // the reported case, when that port is free here
	if ln, err := net.Listen("tcp", net.JoinHostPort(agentGateway, "2043")); err == nil {
		ln.Close()
	} else {
		pub = freePort()
	}
	p.env = append(p.env, fmt.Sprintf("DROOL_AGENT_PUBLIC_PORT=%d", pub))
	os.WriteFile(filepath.Join(p.dir, "compose.override.yaml"), []byte(fmt.Sprintf(`services:
  drool:
    ports: !override
      - target: 8080
        published: "%d"
        host_ip: %s
      - target: 9443
        published: "${DROOL_AGENT_PUBLIC_PORT}"
        host_ip: %s
`, p.webPort, agentGateway, agentGateway)), 0o644)
	p.must("up", "-d")
	p.waitHealthy()
	want := fmt.Sprintf("%s:%d", agentGateway, pub)
	bound := strings.TrimSpace(mustDocker(t, "port", p.container(), "9443/tcp"))
	expect(t, bound == want, "the container's 9443 is published as %q, want %s", bound, want)
	logs := p.must("logs", "--no-log-prefix", "drool")
	expect(t, strings.Contains(logs, fmt.Sprintf(`"shepherds_connect_to":"<the host name the web UI is opened with>:%d"`, pub)) &&
		strings.Contains(logs, `"listen":"0.0.0.0:9443"`), "the startup log doesn't say where Shepherds connect:\n%s", logs)

	c := p.controller(p.setupCode())
	c.host = agentGateway
	if err := c.claim(); err != nil {
		t.Fatal(err)
	}
	var pki struct {
		EndpointPort string   `json:"endpoint_port"`
		Advertised   []string `json:"advertised_endpoints"`
		From         string   `json:"advertised_from"`
	}
	c.must(t, "GET", "/api/v1/agents/pki", nil, &pki)
	expect(t, pki.EndpointPort == "9443" && strings.Join(pki.Advertised, ",") == want && pki.From == "web_address",
		"the controller advertises %+v, want %s (listening on 9443)", pki, want)

	// A host on the test network, reached by the controller over SSH.
	mustDocker(t, "network", "connect", netOuter, p.container())
	hc := startHost(t, image(t, selected()[0]), "pubport-host", false, netOuter)
	key := c.controllerKey(t)
	hc.exec(t, fmt.Sprintf("echo %q >> /home/deploy/.ssh/authorized_keys", key))
	var h host
	c.must(t, "POST", "/api/v1/hosts", map[string]any{"name": "pubport-host", "address": hc.Name, "port": 22, "user": "deploy", "become_method": "sudo"}, &h)
	c.must(t, "POST", "/api/v1/hosts/"+h.ID+"/hostkeys/scan", nil, nil)
	c.must(t, "POST", "/api/v1/hosts/"+h.ID+"/hostkeys/approve", map[string]string{"fingerprint": hc.fingerprint(t)}, nil)
	var pr probeResult
	c.must(t, "POST", "/api/v1/hosts/"+h.ID+"/probe", nil, &pr)
	st, detail, _ := pr.step("privilege escalation")
	expect(t, st == "ok", "the host over SSH: %s %s", st, detail)

	// The browser shows that endpoint before anything is installed.
	if !runE2E(t, c, "shepherd-endpoint", "DROOL_E2E_SHEPHERD_ENDPOINT="+want, "DROOL_E2E_SHEPHERD_HOST="+h.ID) {
		t.Log("no browser here: the endpoint shown in the UI wasn't checked")
	}

	// Installing over SSH uses it, and the Shepherd connects through it.
	var res struct {
		OK     bool   `json:"ok"`
		Output string `json:"output"`
		Error  string `json:"error"`
	}
	c.must(t, "POST", "/api/v1/hosts/"+h.ID+"/agent/install", map[string]any{"preset": "managed"}, &res)
	expect(t, res.OK && strings.Contains(res.Output, "(agent endpoint "+want+")") && strings.Contains(res.Output, "The agent is connected"),
		"install: %s\n%s", res.Error, res.Output)
	t.Logf("9443 published as %s; installation over SSH:\n%s", bound, res.Output)
	var ha struct {
		Identity *struct {
			Connected bool `json:"connected"`
		} `json:"identity"`
	}
	waitFor(t, time.Minute, "the Shepherd connected", func() bool {
		c.must(t, "GET", "/api/v1/hosts/"+h.ID+"/agent", nil, &ha)
		return ha.Identity != nil && ha.Identity.Connected
	})
	known := hc.exec(t, "cat /var/lib/drool-agent/endpoints.json")
	expect(t, strings.Contains(known, `"`+want+`"`), "the host's Shepherd doesn't know %s:\n%s", want, known)
}
