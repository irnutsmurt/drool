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
	"regexp"
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
	env     []string // more environment for docker compose (a .env file's settings)
}

func newComposeProject(t *testing.T, image, composeFile, suffix string) *composeProject {
	t.Helper()
	p := &composeProject{t: t, dir: t.TempDir(), name: "drool-it-" + runID + "-" + suffix, image: image, webPort: freePort()}
	// Every test installation has a data volume of its own, never the
	// default drool_drool-data a real installation here could use.
	p.env = []string{"DROOL_INSTANCE=" + p.name}
	b, err := os.ReadFile(composeFile)
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(p.dir, "compose.yaml"), b, 0o644)
	// No downloads from the internet in tests (GeoIP databases).
	os.WriteFile(filepath.Join(p.dir, "compose.override.yaml"), []byte(fmt.Sprintf(
		"services:\n  drool:\n    ports: !override\n      - \"127.0.0.1:%d:8080\"\n    environment:\n      DROOL_NETWORK_GEOIP_DOWNLOAD: \"false\"\n", p.webPort)), 0o644)
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
	cmd.Env = append(append(os.Environ(), "DROOL_IMAGE="+p.image), p.env...)
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
	cmd.Env = append(append(os.Environ(), "DROOL_IMAGE="+p.image, "COMPOSE_PROJECT_NAME="+p.name), p.env...)
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
		code = setupCodeIn(p.must("logs", "--no-log-prefix", "drool"))
		return code != ""
	})
	return code
}

// setupCodeIn finds the one-time setup code in DROOL's startup banner: the
// first non-empty line after "Setup code:" (journald, through Podman's log
// driver, adds an empty line after each line), the last banner's if there
// are several.
func setupCodeIn(logs string) string {
	var code string
	lines := strings.Split(logs, "\n")
	for i, l := range lines {
		if strings.TrimSpace(l) != "Setup code:" {
			continue
		}
		for _, next := range lines[i+1:] {
			if next = strings.TrimSpace(next); next != "" {
				code = next
				break
			}
		}
	}
	return code
}

func (p *composeProject) controller(setupCode string) *controller {
	jar, _ := cookiejar.New(nil)
	return &controller{port: p.webPort, setupKey: setupCode, client: &http.Client{Jar: jar, Timeout: time.Minute,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}}
}

// TestContainerImage runs the container image as the user guide says, with the
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

	// Something to back up, and the backup as the user guide does it (docs/wiki/Docker-Backup.md).
	before := createSecretOn(t, c, "before-backup")
	p.mustSh("docker compose exec -T drool drool backup -o - > drool-backup.tar.gz")
	p.mustSh("docker compose cp drool:/data/master.key ./master.key")
	out := p.mustSh("docker compose exec -T drool drool backup verify - < drool-backup.tar.gz || test $? -eq 2")
	expect(t, strings.Contains(out, "Restorable"), "backup verify in the container:\n%s", out)
	// With the key kept apart, piped in ahead of the backup: complete.
	out = p.mustSh("chmod 600 master.key && cat master.key drool-backup.tar.gz | docker compose exec -T drool drool backup verify --master-key - -")
	expect(t, strings.Contains(out, "Restorable and complete"), "backup verify with the key on standard input:\n%s", out)
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

	// The user guide's restore (docs/wiki/Restoring-a-Controller.md), command for command.
	// The key reaches the container's unprivileged user on standard input:
	// the operator's copy stays readable only by them, and nothing is mounted.
	restore := strings.Join([]string{
		"docker compose stop drool",
		"cat master.key drool-backup.tar.gz | docker compose run --rm -T drool restore --master-key - -",
		"docker compose start drool",
	}, " && ")
	out = p.mustSh(restore + " && stat -c 'key mode %a' master.key")
	expect(t, strings.Contains(out, "Restored") && strings.Contains(out, "the --master-key file") && strings.Contains(out, "key mode 600"),
		"restore:\n%s", out)
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

// TestContainerImageUpgrade upgrades a Docker Compose installation of the
// tagged Technical Alpha — its own image, built from this repository's
// history — to the image under test the way the user guide says (the new
// compose.yaml in the same directory, so the same volume), and goes back the
// documented way: the older image restores the pre-upgrade backup on the same
// volume. What only containers add is checked here: the volume and its
// ownership across images and base-image versions; the data itself is
// TestReleaseUpgrade's.
func TestContainerImageUpgrade(t *testing.T) {
	image, composeFile := imageUnderTest(t)
	old := "drool-it-" + runID + ":alpha"
	build := exec.Command("sh", "-c", "git -C "+shq(root)+" archive "+alphaBuild.Rev+" | docker build -q -t "+old+" -")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build the alpha's image: %v\n%s", err, out)
	}
	t.Cleanup(func() { docker("image", "rm", old) })
	p := newComposeProject(t, image, composeFile, "upg")
	// The alpha's compose.yaml, with its image instead of a build.
	alphaCompose := fmt.Sprintf("services:\n  drool:\n    image: %s\n    ports:\n      - \"127.0.0.1:%d:8080\"\n    volumes:\n      - drool-data:/data\nvolumes:\n  drool-data:\n", old, p.webPort)
	release, _ := os.ReadFile(filepath.Join(p.dir, "compose.yaml"))
	override, _ := os.ReadFile(filepath.Join(p.dir, "compose.override.yaml"))
	os.Remove(filepath.Join(p.dir, "compose.override.yaml"))
	os.WriteFile(filepath.Join(p.dir, "compose.yaml"), []byte(alphaCompose), 0o644)
	p.must("up", "-d")
	c := p.controller("")
	waitFor(t, 3*time.Minute, "the alpha answering", func() bool {
		resp, err := c.client.Get(c.base() + "/health")
		if err == nil {
			resp.Body.Close()
		}
		return err == nil
	})
	c.setupKey = p.setupCode()
	if err := c.claim(); err != nil {
		t.Fatal(err)
	}
	onAlpha := createSecretOn(t, c, "on-the-alpha")
	p.must("stop")

	// The new release's compose.yaml in the same directory: the same volume.
	os.WriteFile(filepath.Join(p.dir, "compose.yaml"), release, 0o644)
	os.WriteFile(filepath.Join(p.dir, "compose.override.yaml"), override, 0o644)
	p.must("up", "-d")
	p.waitHealthy()
	if err := c.login(); err != nil {
		t.Fatalf("the alpha's administrator in the new image: %v", err)
	}
	var facts struct {
		Backups struct {
			PreUpgrade int `json:"pre_upgrade"`
		} `json:"backups"`
	}
	c.must(t, "GET", "/api/v1/system/security", nil, &facts)
	expect(t, facts.Backups.PreUpgrade == 1, "pre-upgrade backups in the volume: %+v", facts)
	logs := p.must("logs", "--no-log-prefix", "drool")
	backup := regexp.MustCompile(`/data/backups/drool-pre-upgrade-[0-9-]+-schema5\.tar\.gz`).FindString(logs)
	if backup == "" {
		t.Fatalf("the upgrade didn't say where its backup is:\n%s", tail(logs, 20))
	}
	var s struct {
		Items []struct{ ID string } `json:"items"`
	}
	c.must(t, "GET", "/api/v1/secrets", nil, &s)
	found := false
	for _, x := range s.Items {
		found = found || x.ID == onAlpha
	}
	expect(t, found, "the alpha's credential isn't in the upgraded volume")
	afterUpgrade := createSecretOn(t, c, "after-the-upgrade")
	t.Logf("upgraded the alpha's volume to %s; the pre-upgrade backup is %s", image, backup)

	// Going back: the older image restores the pre-upgrade backup on the same
	// volume, then runs.
	p.must("stop", "drool")
	out, err := p.sh("DROOL_IMAGE=" + old + " docker compose run --rm -T drool restore " + backup)
	if err != nil || !strings.Contains(out, "Restored") {
		t.Fatalf("the alpha's image restoring the pre-upgrade backup: %v\n%s", err, out)
	}
	if out, err := p.sh("DROOL_IMAGE=" + old + " docker compose up -d"); err != nil {
		t.Fatalf("the alpha's image again: %v\n%s", err, out)
	}
	waitFor(t, 3*time.Minute, "signing in to the alpha again", func() bool { return c.login() == nil })
	c.must(t, "GET", "/api/v1/secrets", nil, &s)
	got := map[string]bool{}
	for _, x := range s.Items {
		got[x.ID] = true
	}
	expect(t, got[onAlpha] && !got[afterUpgrade], "after going back: the alpha's credential %v, the one made after the upgrade %v", got[onAlpha], got[afterUpgrade])
}
