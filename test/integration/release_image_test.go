//go:build integration

package integration

import (
	"crypto/tls"
	"encoding/json"
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

// imageUnderTest loads the release's container image (-release), or builds
// one from this tree with the same Dockerfile — never a binary mounted into
// some other image. It returns the image reference and the compose file
// that goes with it.
func imageUnderTest(t *testing.T) (image, compose string) {
	t.Helper()
	if rel != nil {
		ref := "drool:" + rel.pkgver
		existed := false
		if _, err := docker("image", "inspect", ref); err == nil {
			existed = true
		}
		out := mustDocker(t, "load", "-i", rel.artifact(t, "container-image", ""))
		expect(t, strings.Contains(out, ref), "docker load: %s", out)
		if !existed {
			t.Cleanup(func() { docker("image", "rm", ref) })
		}
		return ref, rel.artifact(t, "compose", "")
	}
	ref := "drool-it-" + runID + ":dev"
	build := exec.Command(filepath.Join(root, "packaging", "release", "buildx"), "build", "--load", "-t", ref,
		"--build-arg", "VERSION=v0.0.0-it", root)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build the image: %v\n%s", err, out)
	}
	t.Cleanup(func() { docker("image", "rm", ref) })
	return ref, filepath.Join(root, "compose.yaml")
}

// composeProject runs the shipped compose.yaml exactly as the documentation
// does, with two changes in an override file: the ports are published on
// 127.0.0.1 only (never on the workstation's network) and on free ports.
type composeProject struct {
	t       *testing.T
	dir     string
	name    string
	image   string
	webPort int
}

func newComposeProject(t *testing.T, image, composeFile, suffix string) *composeProject {
	t.Helper()
	p := &composeProject{t: t, dir: t.TempDir(), name: "drool-it-" + runID + "-" + suffix, image: image, webPort: freePort()}
	b, err := os.ReadFile(composeFile)
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(p.dir, "compose.yaml"), b, 0o644)
	os.WriteFile(filepath.Join(p.dir, "compose.override.yaml"), []byte(fmt.Sprintf(
		"services:\n  drool:\n    ports: !override\n      - \"127.0.0.1:%d:8080\"\n", p.webPort)), 0o644)
	t.Cleanup(func() {
		if !*flagKeep {
			p.run("down", "-v", "--remove-orphans")
		}
	})
	return p
}

// run is `docker compose ARGS` in the project's directory.
func (p *composeProject) run(args ...string) (string, error) {
	return p.runIn(nil, args...)
}

func (p *composeProject) runIn(stdin *os.File, args ...string) (string, error) {
	cmd := exec.Command("docker", append([]string{"compose", "-p", p.name}, args...)...)
	cmd.Dir = p.dir
	cmd.Env = append(os.Environ(), "DROOL_IMAGE="+p.image)
	if stdin != nil {
		cmd.Stdin = stdin
	}
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func (p *composeProject) must(args ...string) string {
	p.t.Helper()
	out, err := p.run(args...)
	if err != nil {
		p.t.Fatalf("docker compose %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return out
}

// sh runs a shell command in the project directory, as the documentation's
// commands are run.
func (p *composeProject) sh(script string) (string, error) {
	cmd := exec.Command("sh", "-c", script)
	cmd.Dir = p.dir
	cmd.Env = append(os.Environ(), "DROOL_IMAGE="+p.image, "COMPOSE_PROJECT_NAME="+p.name)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func (p *composeProject) mustSh(script string) string {
	p.t.Helper()
	out, err := p.sh(script)
	if err != nil {
		p.t.Fatalf("%s: %v\n%s", script, err, out)
	}
	return out
}

func (p *composeProject) container() string {
	return strings.TrimSpace(p.must("ps", "-q", "drool"))
}

func (p *composeProject) waitHealthy() {
	p.t.Helper()
	waitFor(p.t, 3*time.Minute, "the container's health check", func() bool {
		id := p.container()
		if id == "" {
			return false
		}
		out, _ := docker("inspect", "-f", "{{.State.Health.Status}}", id)
		return out == "healthy"
	})
}

// setupCode reads the one-time setup code from `docker compose logs drool`,
// where the documentation says it is.
func (p *composeProject) setupCode() string {
	p.t.Helper()
	var code string
	waitFor(p.t, time.Minute, "the setup code in the logs", func() bool {
		lines := strings.Split(p.must("logs", "--no-log-prefix", "drool"), "\n")
		for i, l := range lines {
			if strings.TrimSpace(l) == "Setup code:" && i+1 < len(lines) {
				code = strings.TrimSpace(lines[i+1])
			}
		}
		return code != ""
	})
	return code
}

func (p *composeProject) controller(setupCode string) *controller {
	jar, _ := cookiejar.New(nil)
	return &controller{port: p.webPort, setupKey: setupCode, client: &http.Client{Jar: jar, Timeout: time.Minute,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}}
}

// TestContainerImage runs the container image as the README says, with the
// compose.yaml that ships with it: docker compose up -d, the setup code
// from the logs, the browser setup (here: its API), restarts, the health
// check, and the documented backup, master key copy and restore commands.
// The container keeps every restriction compose.yaml gives it (read-only,
// no capabilities, no-new-privileges, an unprivileged user, a tmpfs /tmp, a
// named volume) and gets nothing else: no host network, devices, Docker
// socket or host mounts.
func TestContainerImage(t *testing.T) {
	image, composeFile := imageUnderTest(t)
	p := newComposeProject(t, image, composeFile, "img")
	p.must("up", "-d")
	p.waitHealthy()
	code := p.setupCode()
	c := p.controller(code)
	if err := c.claim(); err != nil {
		t.Fatal(err)
	}

	// What it runs with: exactly compose.yaml's restrictions.
	var insp []struct {
		Config struct {
			User   string
			Labels map[string]string
		}
		HostConfig struct {
			Privileged      bool
			ReadonlyRootfs  bool
			CapAdd, CapDrop []string
			SecurityOpt     []string
			NetworkMode     string
			Devices         []any
			Tmpfs           map[string]string
		}
		Mounts []struct {
			Type, Destination string
		}
	}
	if err := json.Unmarshal([]byte(mustDocker(t, "inspect", p.container())), &insp); err != nil || len(insp) != 1 {
		t.Fatalf("inspect: %v", err)
	}
	in := insp[0]
	expect(t, in.Config.User == "nonroot" && !in.HostConfig.Privileged && in.HostConfig.ReadonlyRootfs &&
		strings.Join(in.HostConfig.CapDrop, ",") == "ALL" && len(in.HostConfig.CapAdd) == 0 &&
		strings.Join(in.HostConfig.SecurityOpt, ",") == "no-new-privileges:true" && len(in.HostConfig.Devices) == 0 &&
		in.HostConfig.NetworkMode != "host", "container settings: %+v", in)
	_, tmp := in.HostConfig.Tmpfs["/tmp"]
	expect(t, tmp && len(in.Mounts) == 1 && in.Mounts[0].Type == "volume" && in.Mounts[0].Destination == "/data",
		"mounts %+v, tmpfs %v", in.Mounts, in.HostConfig.Tmpfs)

	var info struct {
		Version, Channel, Commit string
		Modified                 bool
		Platform                 string
	}
	c.must(t, "GET", "/api/v1/system/info", nil, &info)
	t.Logf("image %s: drool %s (%s, commit %.12s, %s)", image, info.Version, info.Channel, info.Commit, info.Platform)
	if rel != nil {
		expect(t, info.Version == rel.Version && info.Channel == rel.Channel && info.Commit == rel.Commit && !info.Modified,
			"the image's controller says %+v; the release is %s %s %s", info, rel.Version, rel.Channel, rel.Commit)
		expect(t, in.Config.Labels["org.opencontainers.image.version"] == rel.Version &&
			in.Config.Labels["org.opencontainers.image.revision"] == rel.Commit, "image labels %v", in.Config.Labels)
	}
	resp, err := c.client.Get(c.base() + "/agent/install.sh")
	if err != nil {
		t.Fatal(err)
	}
	var script strings.Builder
	buf := make([]byte, 64<<10)
	for {
		n, err := resp.Body.Read(buf)
		script.Write(buf[:n])
		if err != nil {
			break
		}
	}
	resp.Body.Close()
	expect(t, strings.Count(script.String(), "SUM='")-strings.Count(script.String(), "SUM=''") == 2,
		"the image doesn't serve both Shepherd binaries:\n%s", script.String())

	// Something to back up, and the backup as the README does it.
	before := createSecretOn(t, c, "before-backup")
	p.mustSh("docker compose exec -T drool drool backup -o - > drool-backup.tar.gz")
	p.mustSh("docker compose cp drool:/data/master.key ./master.key")
	out := p.mustSh("docker compose exec -T drool drool backup verify - < drool-backup.tar.gz || test $? -eq 2")
	expect(t, strings.Contains(out, "Restorable"), "backup verify in the container:\n%s", out)
	after := createSecretOn(t, c, "after-backup")

	// A restart keeps everything; setup stays closed.
	p.must("restart", "drool")
	p.waitHealthy()
	if err := c.login(); err != nil {
		t.Fatalf("sign in after a restart: %v", err)
	}
	if st, _ := c.do("POST", "/api/v1/setup/claim", map[string]string{"code": code, "username": "x", "password": "another password 123"}, nil); st < 400 {
		t.Errorf("setup claimed again after a restart: %d", st)
	}

	// The README's restore, command for command.
	restore := strings.Join([]string{
		"docker compose stop drool",
		"chmod 644 master.key",
		`docker compose run --rm -T -v "$PWD/master.key:/tmp/master.key:ro" drool restore --master-key /tmp/master.key - < drool-backup.tar.gz`,
		"chmod 600 master.key",
		"docker compose start drool",
	}, " && ")
	out = p.mustSh(restore)
	expect(t, strings.Contains(out, "Restored"), "restore:\n%s", out)
	t.Logf("the documented restore:\n%s", out)
	p.waitHealthy()
	if err := c.login(); err != nil {
		t.Fatalf("sign in after the restore: %v", err)
	}
	names := map[string]bool{}
	var secrets struct {
		Items []struct{ ID, Name string } `json:"items"`
	}
	c.must(t, "GET", "/api/v1/secrets", nil, &secrets)
	for _, s := range secrets.Items {
		names[s.ID] = true
	}
	expect(t, names[before] && !names[after], "after the restore: the backup's credential %v, the later one %v", names[before], names[after])
	var rs struct {
		Restore *struct{ Status string } `json:"restore"`
	}
	c.must(t, "GET", "/api/v1/system/restore", nil, &rs)
	expect(t, rs.Restore != nil, "the controller doesn't know it was restored")
	expect(t, !strings.Contains(p.must("logs", "drool"), "panic"), "panic in the logs")
}

func createSecretOn(t *testing.T, c *controller, name string) string {
	t.Helper()
	var s struct {
		ID string `json:"id"`
	}
	c.must(t, "POST", "/api/v1/secrets", map[string]any{"name": name, "kind": "password", "value": "secret-" + name}, &s)
	return s.ID
}
