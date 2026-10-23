//go:build integration

package integration

import (
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"drool/internal/firewall/observe"
	"drool/internal/netmon/geoip/mmdbtest"
)

// netflowsFile is where the controller reads synthetic observations
// (DROOL_TEST_NETFLOWS_FILE): no isolated test network has hundreds of
// public addresses in dozens of networks.
var netflowsFile string

// TestBrowser runs the web UI's browser tests (web/e2e: Firewall Designer,
// network monitoring, monitoring, alerts, health, scheduling and
// workflows, desired state, navigation and the narrow-screen shell,
// accessibility (host states, titles, keyboard, narrow layouts, touch),
// Light/Dark/System theme, contrast in both themes) against a real
// controller and container hosts: jump-a,
// server-a..c behind it, web-1 direct. It needs Chromium (CHROME, default
// /usr/bin/chromium) and the web dev dependencies (npm ci in web/).
func TestBrowser(t *testing.T) {
	chrome := os.Getenv("CHROME")
	if chrome == "" {
		chrome = "/usr/bin/chromium"
	}
	if _, err := os.Stat(chrome); err != nil {
		t.Skip("no Chromium at " + chrome + " (set CHROME)")
	}
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node is not installed")
	}
	if _, err := os.Stat(filepath.Join(root, "web", "node_modules", "puppeteer-core")); err != nil {
		t.Skip("run npm ci in web/ first")
	}
	freshController(t) // a controller of its own: nothing from other tests (M17)
	img := image(t, allDistros[0])
	jumpC := startHost(t, img, "jump-a", true, netOuter)
	jump := addHost(t, "jump-a", jumpC, hostOpts{})
	hosts := []host{jump}
	addrs := map[string]string{}
	for _, name := range []string{"server-a", "server-b", "server-c"} {
		c := startHost(t, img, name, false, netOuter)
		hosts = append(hosts, addHost(t, name, c, hostOpts{Via: jump.ID}))
		addrs[name] = containerIP(t, c.Name)
	}
	web := startHost(t, img, "web-1", true, netOuter)
	hosts = append(hosts, addHost(t, "web-1", web, hostOpts{}))
	addrs["web-1"] = containerIP(t, web.Name)
	// Check: records how each host sees DROOL's management connection.
	for _, h := range hosts {
		ctl.must(t, "POST", "/api/v1/firewall/hosts/"+h.ID+"/detect", nil, nil)
	}
	// Network monitoring: local GeoIP/ASN test databases and observations.
	writeTestGeoIP(t, filepath.Join(ctl.dataDir, "geoip"))
	ctl.must(t, "POST", "/api/v1/network/enrichment/reload", nil, nil)
	writeTestFlows(t, addrs)
	// Monitoring (M11): hosts whose data comes from the fixture
	// (testdata/monitor.json): an SD-card Pi, a NAS with a failing disk,
	// a GPU box, an eMMC edge device with a USB SSD, a stale VM. Their
	// addresses are unreachable, so SSH polling never overwrites them.
	// A host the browser adds with DROOL's own SSH account (addhost): its
	// setup account signs in with a password only, and sudo asks for it.
	acct := startHost(t, img, "acct-browser", true, netOuter)
	acct.exec(t, ": > /home/pwuser/.ssh/authorized_keys")
	for i, n := range []string{"nas-01", "pi-sd", "gpu-01", "edge-usb", "old-vm"} {
		ctl.must(t, "POST", "/api/v1/hosts", map[string]any{"name": n, "address": fmt.Sprintf("192.0.2.%d", 11+i), "port": 22, "user": "deploy", "become_method": "sudo"}, nil)
	}

	shots := t.TempDir()
	if d := os.Getenv("DROOL_E2E_SHOTS"); d != "" {
		shots = d
	}
	files := []string{"addhost", "designer", "network", "monitoring", "alerts", "health", "automation", "desired", "navigation", "workspace", "accessibility", "packs", "reauth-dialog", "security", "shepherd-endpoint", "shepherd-updates", "shepherd-update-states", "firewall-nftables", "firewall-workflow", "operations", "phase1-ui", "phase2-ui", "monitoring-rules", "theme", "contrast"}
	if only := os.Getenv("DROOL_E2E_FILES"); only != "" {
		files = strings.Split(only, ",") // e.g. "automation,contrast" while iterating
	}
	args := []string{"--test", "--test-concurrency=1"}
	for _, f := range files {
		args = append(args, "e2e/"+strings.TrimSpace(f)+".test.mjs")
	}
	cmd := exec.Command("node", args...)
	cmd.Dir = filepath.Join(root, "web")
	cmd.Env = append(os.Environ(), "DROOL_E2E_URL="+ctl.base(), "DROOL_E2E_USER="+adminUser, "DROOL_E2E_PASSWORD="+adminPassword,
		"CHROME="+chrome, "DROOL_E2E_SHOTS="+shots,
		"DROOL_E2E_ACCOUNT_HOST="+fmt.Sprintf("%d %s", acct.Port, acct.fingerprint(t)), "DROOL_E2E_ACCOUNT_PASSWORD="+sudoPassword)
	start := time.Now()
	out, err := cmd.CombinedOutput()
	t.Logf("browser tests (%s):\n%s", time.Since(start).Round(time.Second), out)
	if err != nil {
		t.Fatalf("browser tests failed: %v (screenshots in %s)", err, shots)
	}
	// Country and network owner without their databases (dogfooding
	// correction batch): the ASN database removed, then the country one;
	// the browser checks what Network Activity says each time. Last: these
	// change what every other test sees.
	if slices.Contains(files, "network") {
		geo := filepath.Join(ctl.dataDir, "geoip")
		for _, phase := range []struct{ remove, state string }{{"test-asn.mmdb", "country"}, {"test-country.mmdb", "none"}} {
			if err := os.Remove(filepath.Join(geo, phase.remove)); err != nil {
				t.Fatal(err)
			}
			ctl.must(t, "POST", "/api/v1/network/enrichment/reload", nil, nil)
			cmd := exec.Command("node", "--test", "e2e/network-enrichment.test.mjs")
			cmd.Dir = filepath.Join(root, "web")
			cmd.Env = append(os.Environ(), "DROOL_E2E_URL="+ctl.base(), "DROOL_E2E_USER="+adminUser, "DROOL_E2E_PASSWORD="+adminPassword,
				"CHROME="+chrome, "DROOL_E2E_SHOTS="+shots, "DROOL_E2E_ENRICH="+phase.state)
			out, err := cmd.CombinedOutput()
			t.Logf("enrichment without %s:\n%s", phase.remove, out)
			if err != nil {
				t.Fatalf("enrichment browser test (%s): %v", phase.state, err)
			}
		}
		writeTestGeoIP(t, geo)
		ctl.must(t, "POST", "/api/v1/network/enrichment/reload", nil, nil)
	}
	// The addhost test removed its host with "Clean up DROOL access": on
	// the container, looked at independently, DROOL's account is gone and
	// the setup account stays (dogfooding correction batch).
	if slices.Contains(files, "addhost") {
		waitFor(t, 90*time.Second, "DROOL's account gone from acct-browser", func() bool { return accountLeft(t, acct) == "" })
		expect(t, strings.Contains(acct.exec(t, "getent passwd pwuser"), "pwuser:"), "the setup account was removed")
	}
}

func containerIP(t *testing.T, c string) string {
	t.Helper()
	out, err := docker("inspect", "-f", "{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}", c)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(out)
}

// testNetworks are the fixture's network owners: real-looking public
// prefixes, so the browser sees realistic grouping (the data is ours).
var testNetworks = []struct {
	prefix, cc, country string
	asn                 uint32
	org                 string
	n, port             int
	proc                string
}{
	{"142.250.74.", "US", "United States", 15169, "Google LLC", 14, 443, "curl"},
	{"104.16.132.", "US", "United States", 13335, "Cloudflare, Inc.", 18, 443, "apt"},
	{"52.95.110.", "IE", "Ireland", 16509, "Amazon.com, Inc.", 22, 443, "aws"},
	{"20.42.65.", "US", "United States", 8075, "Microsoft Corporation", 16, 443, "dotnet"},
	{"95.216.1.", "FI", "Finland", 24940, "Hetzner Online GmbH", 12, 8812, ""},
	{"51.75.62.", "FR", "France", 16276, "OVH SAS", 10, 443, "java"},
	{"159.89.212.", "DE", "Germany", 14061, "DigitalOcean, LLC", 8, 6443, ""},
	{"151.101.2.", "US", "United States", 54113, "Fastly, Inc.", 9, 443, "pip"},
	{"23.215.0.", "NL", "Netherlands", 20940, "Akamai International B.V.", 11, 80, "apt"},
	{"140.82.112.", "US", "United States", 36459, "GitHub, Inc.", 5, 22, "git"},
	{"77.88.8.", "RU", "Russia", 13238, "YANDEX LLC", 3, 53, "systemd-resolve"},
	{"47.88.53.", "SG", "Singapore", 45102, "Alibaba US Technology Co., Ltd.", 7, 443, ""},
	{"34.120.5.", "US", "United States", 396982, "Google LLC", 14, 443, "node"},
	{"91.189.91.", "GB", "United Kingdom", 41231, "Canonical Group Limited", 6, 80, "apt"},
	{"185.10.20.", "NL", "Netherlands", 64512, "Example Hosting B.V.", 1, 8812, ""},
	{"45.33.32.", "US", "United States", 63949, "Akamai Connected Cloud", 4, 443, ""},
	{"194.71.107.", "SE", "Sweden", 1257, "Tele2 Sverige AB", 3, 443, ""},
	{"8.8.8.", "US", "United States", 15169, "Google LLC", 2, 53, "systemd-resolve"},
	{"1.1.1.", "AU", "Australia", 13335, "Cloudflare, Inc.", 2, 53, "systemd-resolve"},
}

func writeTestGeoIP(t *testing.T, dir string) {
	t.Helper()
	var countries, asns []mmdbtest.Record
	for _, n := range testNetworks {
		p := n.prefix + "0/24"
		countries = append(countries, mmdbtest.Record{Prefix: p, Data: mmdbtest.Country(n.cc, n.country)})
		asns = append(asns, mmdbtest.Record{Prefix: p, Data: mmdbtest.ASN(n.asn, n.org)})
	}
	countries = append(countries, mmdbtest.Record{Prefix: "2a00:1450::/32", Data: mmdbtest.Country("DE", "Germany")})
	asns = append(asns, mmdbtest.Record{Prefix: "2a00:1450::/32", Data: mmdbtest.ASN(15169, "Google LLC")})
	c, err := mmdbtest.Build(mmdbtest.Options{DatabaseType: "Test-Country", BuildEpoch: uint64(time.Now().Unix())}, countries)
	if err != nil {
		t.Fatal(err)
	}
	a, err := mmdbtest.Build(mmdbtest.Options{DatabaseType: "Test-ASN", BuildEpoch: uint64(time.Now().Unix())}, asns)
	if err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(dir, 0o755)
	if err := os.WriteFile(filepath.Join(dir, "test-country.mmdb"), c, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "test-asn.mmdb"), a, 0o644); err != nil {
		t.Fatal(err)
	}
}

// writeTestFlows writes ~240 observations: public destinations in many
// networks seen over three weeks (a few for the first time today),
// internal traffic between the test hosts, a LAN admin and a VPN client.
func writeTestFlows(t *testing.T, addrs map[string]string) {
	t.Helper()
	r := rand.New(rand.NewPCG(10, 10))
	type rep struct {
		Source string         `json:"source"`
		Flows  []observe.Flow `json:"flows"`
	}
	out := map[string]*rep{}
	for h := range addrs {
		out[h] = &rep{Source: observe.SourceConntrack}
	}
	add := func(h, dir, proto, peer string, port int, proc string, count, first, last int64) {
		f := observe.Flow{Direction: dir, Protocol: proto, LocalIP: addrs[h], Peer: peer, Port: port, Process: proc, Count: count,
			FirstSeen: first, LastSeen: last}
		if dir == "out" {
			f.LocalPort, f.RemotePort = 32768+r.IntN(28000), port
		} else {
			f.LocalPort, f.RemotePort = port, 32768+r.IntN(28000)
		}
		if proc != "" {
			f.PID = 300 + r.IntN(40000)
		}
		out[h].Flows = append(out[h].Flows, f)
	}
	hosts := []string{"web-1", "server-a", "server-b", "server-c"}
	for _, n := range testNetworks {
		for i := 0; i < n.n; i++ {
			proto := "tcp"
			if n.port == 53 {
				proto = "udp"
			}
			h := hosts[r.IntN(len(hosts))]
			first := -int64(3600 + r.IntN(20*86400)) // never "new" unless below
			if n.asn == 64512 {
				h, first = "web-1", -1200
			}
			add(h, "out", proto, fmt.Sprintf("%s%d", n.prefix, 10+i+boolInt(n.asn == 64512)*20), n.port, n.proc, int64(1+r.IntN(400)), first, max(first, -int64(5+r.IntN(3600))))
		}
	}
	add("web-1", "out", "tcp", "2a00:1450:4001:82a::200e", 443, "curl", 12, -7200, -40)
	// A public address the test databases have no answer for: genuinely
	// unknown (dogfooding correction batch).
	add("web-1", "out", "tcp", "49.12.200.77", 443, "curl", 3, -3600, -60)
	add("web-1", "out", "tcp", addrs["server-a"], 8080, "nginx", 4210, -10*86400, -10)
	add("server-a", "in", "tcp", addrs["web-1"], 8080, "app", 4188, -10*86400, -12)
	add("server-a", "out", "tcp", addrs["server-b"], 5432, "app", 912, -9*86400, -30)
	add("server-b", "in", "tcp", addrs["server-a"], 5432, "postgres", 910, -9*86400, -30)
	add("web-1", "in", "tcp", "192.168.1.25", 22, "sshd", 7, -5*86400, -7200)
	add("web-1", "in", "tcp", "100.101.7.9", 443, "nginx", 35, -2*86400, -100)
	// Home Assistant talking to an ESPHome device's API (the firewall
	// workflow's example rule: TCP 6053 from 192.168.3.132).
	add("web-1", "in", "tcp", "192.168.3.132", 6053, "esphome", 1840, -6*86400, -20)
	b, _ := json.Marshal(out)
	if err := os.WriteFile(netflowsFile, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
