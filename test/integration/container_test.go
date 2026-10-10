//go:build integration

package integration

import (
	"crypto/tls"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestRestrictedContainer runs the controller the way compose.yaml does
// (M16, ARCHITECTURE-ALPHA §33.12): the distroless image as its unprivileged
// user, a read-only root file system, every capability dropped,
// no-new-privileges, a tmpfs /tmp and a named volume for /data — with this
// build's binary mounted over the image's. Setup, the health check, a
// backup and its verification work; nothing more is needed. The container
// is ordinary and unprivileged, its port published on 127.0.0.1 only.
func TestRestrictedContainer(t *testing.T) {
	if _, err := docker("image", "inspect", "drool:local"); err != nil {
		t.Skip("needs the drool:local image (docker compose build)")
	}
	// A static build, as the Dockerfile makes (the image has no C library).
	bin := filepath.Join(t.TempDir(), "drool")
	build := exec.Command("go", "build", "-trimpath", "-o", bin, "./cmd/drool")
	build.Dir, build.Env = root, append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("static build: %v\n%s", err, out)
	}
	os.Chmod(filepath.Dir(bin), 0o755)
	name := "drool-it-" + runID + "-restricted"
	vol := name + "-data"
	mustDocker(t, "volume", "create", vol)
	t.Cleanup(func() { docker("rm", "-f", name); docker("volume", "rm", "-f", vol) })
	port := freePort()
	setup := "it-setup-" + randHex(8)
	mustDocker(t, "run", "-d", "--name", name,
		"--read-only", "--tmpfs", "/tmp:rw,size=16m", "--cap-drop", "ALL", "--security-opt", "no-new-privileges:true",
		"-v", vol+":/data", "-v", bin+":/usr/local/bin/drool:ro",
		"-p", fmt.Sprintf("127.0.0.1:%d:8080", port),
		"-e", "DROOL_SETUP_SECRET="+setup, "-e", "DROOL_AGENT_ENABLED=false",
		"drool:local", "serve")
	waitFor(t, 60*time.Second, "the container's health check", func() bool {
		out, err := docker("exec", name, "/usr/local/bin/drool", "healthcheck")
		return err == nil && strings.Contains(out, `"database":"ok"`)
	})
	// What it runs with.
	insp := mustDocker(t, "inspect", "-f", "{{.HostConfig.Privileged}} {{.HostConfig.ReadonlyRootfs}} {{.HostConfig.CapDrop}} {{.HostConfig.CapAdd}} {{.Config.User}}", name)
	expect(t, strings.TrimSpace(insp) == "false true [ALL] [] nonroot", "container settings: %s", insp)

	// First-admin setup and the API over the published port.
	jar, _ := cookiejar.New(nil)
	c := &controller{port: port, setupKey: setup, client: &http.Client{Jar: jar, Timeout: time.Minute, Transport: &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}}
	if err := c.claim(); err != nil {
		t.Fatal(err)
	}
	var info struct {
		Version string `json:"version"`
	}
	st, err := c.do("GET", "/api/v1/system/info", nil, &info)
	expect(t, err == nil && st == 200 && info.Version != "", "system info: %d %v", st, err)

	// A backup to standard output, and its verification through /tmp.
	b, err := exec.Command("docker", "exec", name, "/usr/local/bin/drool", "backup", "-o", "-").Output()
	if err != nil {
		t.Fatalf("backup in the container: %v", err)
	}
	verify := exec.Command("docker", "exec", "-i", name, "/usr/local/bin/drool", "backup", "verify", "-")
	verify.Stdin = strings.NewReader(string(b))
	out, err := verify.CombinedOutput()
	// No master key in the backup, but nothing sealed but the controller's
	// own key: restorable; complete only with the key file.
	expect(t, strings.Contains(string(out), "Restorable"), "verify in the container: %v\n%s", err, out)
}
