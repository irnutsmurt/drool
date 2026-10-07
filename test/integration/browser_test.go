//go:build integration

package integration

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// TestBrowser runs the web UI's browser tests (web/e2e: Firewall Designer,
// Light/Dark/System theme, contrast in both themes) against a real
// controller and container hosts: jump-a, server-a..c behind it, web-1
// direct. It needs Chromium (CHROME, default /usr/bin/chromium) and the
// web dev dependencies (npm ci in web/).
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
	img := image(t, allDistros[0])
	jumpC := startHost(t, img, "jump-a", true, netOuter)
	jump := addHost(t, "jump-a", jumpC, hostOpts{})
	hosts := []host{jump}
	for _, name := range []string{"server-a", "server-b", "server-c"} {
		c := startHost(t, img, name, false, netOuter)
		hosts = append(hosts, addHost(t, name, c, hostOpts{Via: jump.ID}))
	}
	web := startHost(t, img, "web-1", true, netOuter)
	hosts = append(hosts, addHost(t, "web-1", web, hostOpts{}))
	// Check: records how each host sees DROOL's management connection.
	for _, h := range hosts {
		ctl.must(t, "POST", "/api/v1/firewall/hosts/"+h.ID+"/detect", nil, nil)
	}
	shots := t.TempDir()
	if d := os.Getenv("DROOL_E2E_SHOTS"); d != "" {
		shots = d
	}
	cmd := exec.Command("node", "--test", "--test-concurrency=1", "e2e/designer.test.mjs", "e2e/theme.test.mjs", "e2e/contrast.test.mjs")
	cmd.Dir = filepath.Join(root, "web")
	cmd.Env = append(os.Environ(), "DROOL_E2E_URL="+ctl.base(), "DROOL_E2E_USER="+adminUser, "DROOL_E2E_PASSWORD="+adminPassword,
		"CHROME="+chrome, "DROOL_E2E_SHOTS="+shots)
	start := time.Now()
	out, err := cmd.CombinedOutput()
	t.Logf("browser tests (%s):\n%s", time.Since(start).Round(time.Second), out)
	if err != nil {
		t.Fatalf("browser tests failed: %v (screenshots in %s)", err, shots)
	}
}
