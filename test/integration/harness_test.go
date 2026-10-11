//go:build integration

// Package integration runs the real drool binary against real distributions
// in unprivileged containers (sshd only, no systemd; nothing privileged and
// no host devices). Run with:
//
//	make test-integration                  # whole matrix
//	go test -tags integration ./test/integration -run 'Matrix/debian' -v
//
// Requires Docker and network access (package repositories).
package integration

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"drool/internal/release"
)

// distro is one entry of the matrix (ARCHITECTURE-ALPHA §18).
type distro struct {
	Name, Family, Base string
	OSFamily           string // expected discovered os_family
	ID                 string // expected os-release ID
	PkgManager         string
	// Package pair for the removal-approval test: removing Dep also removes Pkg.
	Pkg, Dep string
}

var allDistros = []distro{
	{"debian-12", "apt", "debian:12", "debian", "debian", "apt", "vim", "vim-runtime"},
	{"debian-13", "apt", "debian:13", "debian", "debian", "apt", "vim", "vim-runtime"},
	{"ubuntu-22.04", "apt", "ubuntu:22.04", "debian", "ubuntu", "apt", "vim", "vim-runtime"},
	{"ubuntu-24.04", "apt", "ubuntu:24.04", "debian", "ubuntu", "apt", "vim", "vim-runtime"},
	{"ubuntu-26.04", "apt", "ubuntu:26.04", "debian", "ubuntu", "apt", "vim", "vim-runtime"},
	{"rockylinux-9", "dnf", "rockylinux:9", "redhat", "rocky", "dnf", "vim-enhanced", "vim-common"},
	{"rockylinux-10", "dnf", "rockylinux/rockylinux:10", "redhat", "rocky", "dnf", "vim-enhanced", "vim-common"},
	{"almalinux-9", "dnf", "almalinux:9", "redhat", "almalinux", "dnf", "vim-enhanced", "vim-common"},
	{"almalinux-10", "dnf", "almalinux:10", "redhat", "almalinux", "dnf", "vim-enhanced", "vim-common"},
	{"fedora-44", "dnf", "fedora:44", "redhat", "fedora", "dnf", "vim-enhanced", "vim-common"},
}

var (
	flagDistros = flag.String("distros", "", "comma-separated subset of the matrix (default: all)")
	flagKeep    = flag.Bool("keep", false, "keep containers, network and data directory after the run")
	// flagRelease qualifies a release: the controller and the Shepherds it
	// hands out are the release's own binaries, and the release tests
	// (packages, image, upgrade, mixed versions) use its artifacts.
	flagRelease = flag.String("release", "", "a release directory made by make release (dist/<version>): test its artifacts instead of this tree's builds")
)

const sudoPassword = "drool-it-sudo-pw" // set in images/*/Dockerfile

// agentV1 is the Shepherd the controller hands out: this tree's build, or
// the release's under -release.
var agentV1 = "v1.0.0"

// Newer Shepherd versions built for the update tests.
const (
	agentV2     = "v1.1.0"
	agentBroken = "v1.2.0"
	// agentRunaway carries a deliberately runaway collector (-tags droolrunaway).
	agentRunaway = "v1.3.0"
)

var (
	root     string // repo root
	binPath  string
	runID    string
	netOuter string // controller-reachable containers
	netInner string // only reachable through the jump host
	ctl      *controller

	agentDir, releaseDir string
	agentGateway         string
	agentPort            int
	imagesMu             sync.Mutex
	built                = map[string]error{}
)

func TestMain(m *testing.M) {
	flag.Parse()
	code, err := setup(m)
	if err != nil {
		fmt.Fprintln(os.Stderr, "integration setup:", err)
		code = 1
	}
	os.Exit(code)
}

func setup(m *testing.M) (int, error) {
	if _, err := exec.LookPath("docker"); err != nil {
		return 0, errors.New("docker is required")
	}
	_, file, _, _ := runtime.Caller(0)
	root = filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	runID = randHex(4)
	work, err := os.MkdirTemp("", "drool-it-")
	if err != nil {
		return 0, err
	}
	binPath = filepath.Join(work, "drool")
	agentDir = filepath.Join(work, "agent-dist")
	releaseDir = filepath.Join(work, "agent-releases")
	if *flagRelease != "" {
		// The release's controller and Shepherd binaries, as shipped.
		r, err := openRelease(*flagRelease, filepath.Join(work, "release"))
		if err != nil {
			return 0, fmt.Errorf("-release: %w", err)
		}
		rel = r
		binPath, agentDir, agentV1 = r.drool, r.binDir, r.Version
		if release.CompareVersions(agentV1, agentV2) >= 0 {
			return 0, fmt.Errorf("-release: the update tests' Shepherd versions (%s…) must be newer than the release (%s)", agentV2, agentV1)
		}
		fmt.Fprintf(os.Stderr, "qualifying release %s (%s, commit %s)\n", r.Version, r.Channel, r.Commit)
	} else {
		build := exec.Command("go", "build", "-o", binPath, "./cmd/drool")
		build.Dir, build.Stdout, build.Stderr = root, os.Stderr, os.Stderr
		if err := build.Run(); err != nil {
			return 0, fmt.Errorf("build drool: %w", err)
		}
	}
	// Agent builds: the one the controller hands out (unless the release's),
	// a newer one for update tests, and a deliberately broken newer one for
	// rollback tests.
	// arm64 builds of the update releases and of the handed-out Shepherd go
	// to arm64 VMs (-vm debian-12-arm64): "<name>-arm64".
	type agentBuild struct{ out, version, tags, arch string }
	var builds []agentBuild
	for _, arch := range []string{"amd64", "arm64"} {
		sfx := map[string]string{"amd64": "", "arm64": "-arm64"}[arch]
		builds = append(builds,
			agentBuild{filepath.Join(releaseDir, "drool-agent-"+agentV2+sfx), agentV2, "", arch},
			agentBuild{filepath.Join(releaseDir, "drool-agent-"+agentBroken+sfx), agentBroken, "droolbroken", arch})
		if rel == nil {
			builds = append(builds, agentBuild{filepath.Join(agentDir, "drool-agent-linux-"+arch), agentV1, "", arch})
		}
	}
	builds = append(builds, agentBuild{filepath.Join(releaseDir, "drool-agent-"+agentRunaway), agentRunaway, "droolrunaway", "amd64"})
	for _, b := range builds {
		args := []string{"build", "-trimpath", "-ldflags", "-X drool/internal/version.Version=" + b.version, "-o", b.out}
		if b.tags != "" {
			args = append(args, "-tags", b.tags)
		}
		cmd := exec.Command("go", append(args, "./cmd/drool-agent")...)
		cmd.Dir, cmd.Stdout, cmd.Stderr = root, os.Stderr, os.Stderr
		cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH="+b.arch)
		if err := cmd.Run(); err != nil {
			return 0, fmt.Errorf("build drool-agent %s for %s: %w", b.version, b.arch, err)
		}
	}
	netOuter, netInner = "drool-it-"+runID+"-outer", "drool-it-"+runID+"-inner"
	for _, n := range []string{netOuter, netInner} {
		if _, err := docker("network", "create", n); err != nil {
			return 0, err
		}
	}
	defer func() {
		if *flagKeep {
			fmt.Fprintf(os.Stderr, "kept: data dir %s, networks %s %s, containers drool-it-%s-*\n", work, netOuter, netInner, runID)
			return
		}
		out, _ := docker("ps", "-aq", "--filter", "name=drool-it-"+runID+"-")
		if ids := strings.Fields(out); len(ids) > 0 {
			docker(append([]string{"rm", "-f"}, ids...)...)
		}
		docker("network", "rm", netOuter, netInner)
		os.RemoveAll(work)
	}()

	// The agent endpoint listens on the test network's gateway only, so
	// containers reach it and nothing else on the workstation does.
	gw, err := docker("network", "inspect", "-f", "{{(index .IPAM.Config 0).Gateway}}", netOuter)
	if err != nil {
		return 0, err
	}
	agentGateway, agentPort = strings.TrimSpace(gw), freePort()
	netflowsFile = filepath.Join(work, "netflows.json")
	fwControlFile = filepath.Join(work, "fw-control")
	ctl = &controller{dataDir: filepath.Join(work, "data"), extraEnv: controllerEnv(agentPort)}
	if err := ctl.start(); err != nil {
		return 0, err
	}
	defer ctl.stop()
	if err := ctl.claim(); err != nil {
		return 0, err
	}
	return m.Run(), nil
}

// controllerEnv is how the test controllers run: the agent endpoint on the
// test network's gateway only, and the test-mode timings and fixtures.
func controllerEnv(agentPort int) []string {
	return []string{
		// Small enough to exercise the cap without huge outputs.
		"DROOL_ENGINE_OUTPUT_CAP_BYTES=1048576",
		"DROOL_AGENT_BIND=" + agentGateway,
		fmt.Sprintf("DROOL_AGENT_PORT=%d", agentPort),
		fmt.Sprintf("DROOL_AGENT_ADVERTISE=%s:%d", agentGateway, agentPort),
		"DROOL_AGENT_BINARIES_DIR=" + agentDir,
		// Per-host firewall test control (delay or skip commits; M8 tests).
		"DROOL_TEST_FW_CONTROL=" + fwControlFile,
		// Synthetic network observations for the browser tests (M10).
		"DROOL_TEST_NETFLOWS_FILE=" + netflowsFile,
		// Synthetic monitoring data for hosts that only TestBrowser creates
		// (nas-01, pi-sd, gpu-01, edge-usb, old-vm).
		"DROOL_TEST_MONITOR_FILE=" + filepath.Join(root, "test", "integration", "testdata", "monitor.json"),
		"DROOL_TEST_ALERT_TIMING=fast",
		// Scheduler ticks every second; runs > 20 s late count as missed.
		"DROOL_TEST_SCHED_TIMING=fast",
		// Desired state checks hosts every 20 s; automatic restore every 2 s.
		"DROOL_TEST_DESIRED_TIMING=fast",
		// Re-authentication lapses after 3 s (M16 browser tests).
		"DROOL_TEST_AUTH_TIMING=fast",
	}
}

// freshController runs a controller of its own for the rest of t — claimed,
// signed in, with nothing any other test made — and points the domain
// helpers (ctl) at it, so the test neither sees nor leaves state for others
// (M17: release suites run alone or together alike). Only for tests that
// aren't parallel at the top level. extraEnv adds to controllerEnv.
func freshController(t *testing.T, extraEnv ...string) *controller {
	t.Helper()
	dir := t.TempDir()
	c := &controller{dataDir: filepath.Join(dir, "data"), extraEnv: append(controllerEnv(freePort()), extraEnv...)}
	if err := c.start(); err != nil {
		t.Fatal(err)
	}
	if err := c.claim(); err != nil {
		c.stop()
		t.Fatal(err)
	}
	shared := ctl
	ctl = c
	t.Cleanup(func() {
		ctl = shared
		c.stop()
		if t.Failed() {
			if b, err := os.ReadFile(filepath.Join(dir, "serve.log")); err == nil {
				lines := strings.Split(strings.TrimSpace(string(b)), "\n")
				t.Logf("its controller's last log lines:\n%s", strings.Join(lines[max(0, len(lines)-30):], "\n"))
			}
		}
	})
	return c
}

func selected() []distro {
	if *flagDistros == "" {
		return allDistros
	}
	want := map[string]bool{}
	for _, n := range strings.Split(*flagDistros, ",") {
		want[strings.TrimSpace(n)] = true
	}
	var out []distro
	for _, d := range allDistros {
		if want[d.Name] {
			out = append(out, d)
		}
	}
	return out
}

func randHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// --- docker ---

func docker(args ...string) (string, error) {
	cmd := exec.Command("docker", args...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return out.String(), fmt.Errorf("docker %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(errb.String()))
	}
	return strings.TrimSpace(out.String()), nil
}

func mustDocker(t *testing.T, args ...string) string {
	t.Helper()
	out, err := docker(args...)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func image(t *testing.T, d distro) string {
	t.Helper()
	tag := "drool-it-" + d.Name
	imagesMu.Lock()
	err, done := built[d.Name]
	imagesMu.Unlock()
	if !done {
		_, err = docker("build", "-q", "--build-arg", "BASE="+d.Base, "-t", tag, filepath.Join(root, "test", "integration", "images", d.Family))
		imagesMu.Lock()
		built[d.Name] = err
		imagesMu.Unlock()
	}
	if err != nil {
		t.Fatalf("build image for %s: %v", d.Name, err)
	}
	return tag
}

// container is a test host.
type container struct {
	Name string // docker name, also its DNS name on the networks
	Port int    // published SSH port on 127.0.0.1 (0 if not published)
	img  string
	nets []string
}

// startHost runs a host container. publish exposes SSH on loopback for the
// controller; hosts behind the jump host are not published.
func startHost(t *testing.T, img, short string, publish bool, nets ...string) *container {
	t.Helper()
	c := &container{Name: "drool-it-" + runID + "-" + short, img: img, nets: nets}
	c.run(t, publish, 0)
	t.Cleanup(func() {
		if !*flagKeep {
			docker("rm", "-f", c.Name)
		}
	})
	return c
}

func (c *container) run(t *testing.T, publish bool, port int) {
	t.Helper()
	args := []string{"run", "-d", "--name", c.Name, "--hostname", strings.TrimPrefix(c.Name, "drool-it-"+runID+"-"),
		"--network", c.nets[0], "--memory", "1g", "--cpus", "2"}
	if publish {
		// A fixed port: Docker reassigns ephemeral ports when a container
		// restarts, which would look like the host moving.
		if port == 0 {
			port = freePort()
		}
		args = append(args, "-p", fmt.Sprintf("127.0.0.1:%d:22", port))
	}
	mustDocker(t, append(args, c.img)...)
	for _, n := range c.nets[1:] {
		mustDocker(t, "network", "connect", n, c.Name)
	}
	if publish {
		c.Port = port
	}
	// Wait for sshd to have generated keys.
	waitFor(t, 30*time.Second, "sshd in "+c.Name, func() bool {
		_, err := docker("exec", c.Name, "test", "-f", "/etc/ssh/ssh_host_ed25519_key.pub")
		return err == nil
	})
	c.authorize(t)
}

// recreate replaces the container with a fresh one (new host keys) on the
// same name and port: what a reinstalled or impersonated host looks like.
func (c *container) recreate(t *testing.T) {
	t.Helper()
	mustDocker(t, "rm", "-f", c.Name)
	c.run(t, c.Port != 0, c.Port)
}

func (c *container) authorize(t *testing.T) {
	t.Helper()
	key := ctl.controllerKey(t)
	for _, u := range []string{"deploy", "pwuser"} {
		mustDocker(t, "exec", c.Name, "sh", "-c", fmt.Sprintf("echo %q >> /home/%s/.ssh/authorized_keys", key, u))
	}
}

func (c *container) exec(t *testing.T, script string) string {
	t.Helper()
	return mustDocker(t, "exec", c.Name, "sh", "-c", script)
}

func (c *container) fingerprint(t *testing.T) string {
	t.Helper()
	out := c.exec(t, "ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub")
	return strings.Fields(out)[1]
}

func waitFor(t *testing.T, d time.Duration, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %s waiting for %s", d, what)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// --- controller ---

type controller struct {
	bin      string // the drool binary to run; default binPath (this tree's or the release's)
	dataDir  string
	extraEnv []string
	port     int
	cmd      *exec.Cmd
	exited   chan struct{}
	client   *http.Client
	csrf     string
	mu       sync.Mutex
	pubKey   string
	setupKey string
}

// agentListenPort is the port this controller's Shepherd endpoint listens
// on: the shared controller's agentPort, or the one freshController chose.
func (c *controller) agentListenPort() int {
	port := 0
	for _, kv := range c.extraEnv {
		if v, ok := strings.CutPrefix(kv, "DROOL_AGENT_PORT="); ok {
			fmt.Sscan(v, &port)
		}
	}
	return port
}

func freePort() int {
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func (c *controller) base() string { return fmt.Sprintf("https://127.0.0.1:%d", c.port) }

func (c *controller) start() error {
	if c.port == 0 {
		c.port = freePort()
		c.setupKey = "it-setup-" + randHex(8)
	}
	exe := binPath
	if c.bin != "" {
		exe = c.bin
	}
	c.cmd = exec.Command(exe, "serve")
	c.cmd.Env = append(os.Environ(),
		"DROOL_DATA_DIR="+c.dataDir,
		"DROOL_SERVER_BIND=127.0.0.1",
		fmt.Sprintf("DROOL_SERVER_WEB_PORT=%d", c.port),
		"DROOL_SETUP_SECRET="+c.setupKey,
		"DROOL_LOGGING_OUTPUT=file",
		"DROOL_LOGGING_LEVEL=debug",
	)
	c.cmd.Env = append(c.cmd.Env, c.extraEnv...)
	// Don't leave a controller behind if the test binary is killed.
	c.cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
	log, err := os.OpenFile(filepath.Join(filepath.Dir(c.dataDir), "serve.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	c.cmd.Stdout, c.cmd.Stderr = log, log
	if err := c.cmd.Start(); err != nil {
		return err
	}
	c.exited = make(chan struct{})
	go func() { c.cmd.Wait(); log.Close(); close(c.exited) }()
	if c.client == nil {
		jar, _ := cookiejar.New(nil)
		c.client = &http.Client{Jar: jar, Timeout: 5 * time.Minute, Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
	}
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-c.exited:
			return fmt.Errorf("drool serve exited during startup; see %s", filepath.Join(filepath.Dir(c.dataDir), "serve.log"))
		default:
		}
		if r, err := c.client.Get(c.base() + "/health"); err == nil {
			r.Body.Close()
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return errors.New("drool serve did not become healthy")
}

func (c *controller) stop() {
	if c.cmd == nil || c.cmd.Process == nil {
		return
	}
	c.cmd.Process.Signal(syscall.SIGTERM)
	select {
	case <-c.exited:
	case <-time.After(20 * time.Second):
		c.cmd.Process.Kill()
		<-c.exited
	}
}

// crash kills the controller without a clean shutdown.
func (c *controller) crash() {
	c.cmd.Process.Kill()
	<-c.exited
}

const adminUser, adminPassword = "admin", "integration test password"

func (c *controller) claim() error {
	var me struct {
		CSRFToken string `json:"csrf_token"`
	}
	st, err := c.do("POST", "/api/v1/setup/claim", map[string]string{"code": c.setupKey, "username": adminUser, "password": adminPassword}, &me)
	if err != nil || st != 201 && st != 200 {
		return fmt.Errorf("setup claim: %d %v", st, err)
	}
	return c.login()
}

func (c *controller) login() error {
	var me struct {
		CSRFToken string `json:"csrf_token"`
	}
	st, err := c.do("POST", "/api/v1/auth/login", map[string]string{"username": adminUser, "password": adminPassword}, &me)
	if err != nil || st != 200 {
		return fmt.Errorf("login: %d %v", st, err)
	}
	c.csrf = me.CSRFToken
	return nil
}

// do sends a JSON request; out may be nil. Non-2xx statuses are returned,
// not errors. A *json.RawMessage out receives the body whatever the status.
// High-impact actions need a recent re-authentication (M16; 3 s in the
// test controller): like the UI, do confirms with the password and repeats.
func (c *controller) do(method, path string, body, out any) (int, error) {
	send := func() (*http.Response, []byte, error) {
		var rd io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rd = bytes.NewReader(b)
		}
		req, _ := http.NewRequestWithContext(context.Background(), method, c.base()+path, rd)
		req.Header.Set("Content-Type", "application/json")
		if c.csrf != "" {
			req.Header.Set("X-DROOL-CSRF", c.csrf)
		}
		r, err := c.client.Do(req)
		if err != nil {
			return nil, nil, err
		}
		defer r.Body.Close()
		data, _ := io.ReadAll(r.Body)
		return r, data, nil
	}
	r, data, err := send()
	if err != nil {
		return 0, err
	}
	if r.StatusCode == http.StatusForbidden && path != "/api/v1/auth/reauth" && strings.Contains(string(data), `"code":"reauth_required"`) {
		if st, err := c.do("POST", "/api/v1/auth/reauth", map[string]string{"password": adminPassword}, nil); err != nil || st != 200 {
			return st, fmt.Errorf("re-authenticate: %d %v", st, err)
		}
		if r, data, err = send(); err != nil {
			return 0, err
		}
	}
	if raw, ok := out.(*json.RawMessage); ok {
		*raw = data
		return r.StatusCode, nil
	}
	if out != nil && len(data) > 0 && r.StatusCode < 300 {
		if err := json.Unmarshal(data, out); err != nil {
			return r.StatusCode, fmt.Errorf("decode %s %s: %v: %s", method, path, err, data)
		}
	}
	if r.StatusCode >= 300 && out != nil {
		if p, ok := out.(*problem); ok {
			json.Unmarshal(data, p)
		}
	}
	return r.StatusCode, nil
}

type problem struct {
	Title  string `json:"title"`
	Detail string `json:"detail"`
	Code   string `json:"code"`
}

func (c *controller) must(t *testing.T, method, path string, body, out any) {
	t.Helper()
	var raw json.RawMessage
	st, err := c.do(method, path, body, &raw)
	if err != nil {
		t.Fatal(err)
	}
	if st >= 300 {
		t.Fatalf("%s %s: HTTP %d %s", method, path, st, raw)
	}
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			t.Fatalf("decode %s %s: %v", method, path, err)
		}
	}
}

func (c *controller) controllerKey(t *testing.T) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.pubKey == "" {
		var k struct {
			PublicKey string `json:"public_key"`
		}
		c.must(t, "GET", "/api/v1/secrets/controller-ssh-key", nil, &k)
		c.pubKey = strings.TrimSpace(k.PublicKey)
	}
	return c.pubKey
}

// --- domain helpers ---

type hostOpts struct {
	User         string
	CredentialID string // default: controller-default (empty → server default)
	BecomeCredID string
	Via          string // jump host id
	Address      string // default 127.0.0.1
	Port         int
	JumpOnly     bool
}

type host struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"status_detail"`
}

// addHost creates a host, approves the container's host key by its
// fingerprint (obtained out of band, as an operator should), and runs the
// connection test.
func addHost(t *testing.T, name string, c *container, o hostOpts) host {
	t.Helper()
	if o.User == "" {
		o.User = "deploy"
	}
	addr, port := "127.0.0.1", c.Port
	if o.Via != "" {
		addr, port = c.Name, 22
	}
	body := map[string]any{"name": name, "address": addr, "port": port, "user": o.User, "become_method": "sudo", "jump_only": o.JumpOnly}
	if o.CredentialID != "" {
		body["credential_id"] = o.CredentialID
	}
	if o.BecomeCredID != "" {
		body["become_credential_id"] = o.BecomeCredID
	}
	if o.Via != "" {
		body["via_host_id"] = o.Via
	}
	var h host
	ctl.must(t, "POST", "/api/v1/hosts", body, &h)
	ctl.must(t, "POST", "/api/v1/hosts/"+h.ID+"/hostkeys/scan", nil, nil)
	ctl.must(t, "POST", "/api/v1/hosts/"+h.ID+"/hostkeys/approve", map[string]string{"fingerprint": c.fingerprint(t)}, nil)
	probe(t, h.ID)
	return getHost(t, h.ID)
}

type probeResult struct {
	OK    bool `json:"ok"`
	Steps []struct {
		Name       string `json:"name"`
		Status     string `json:"status"`
		Detail     string `json:"detail"`
		ErrorClass string `json:"error_class"`
		Hint       string `json:"hint"`
	} `json:"steps"`
}

func probe(t *testing.T, id string) probeResult {
	t.Helper()
	var p probeResult
	ctl.must(t, "POST", "/api/v1/hosts/"+id+"/probe", nil, &p)
	return p
}

func (p probeResult) step(name string) (status, detail, class string) {
	for _, s := range p.Steps {
		if s.Name == name {
			return s.Status, s.Detail, s.ErrorClass
		}
	}
	return "", "", ""
}

func getHost(t *testing.T, id string) host {
	t.Helper()
	var h host
	ctl.must(t, "GET", "/api/v1/hosts/"+id, nil, &h)
	return h
}

func createSecret(t *testing.T, name, kind, value string) string {
	t.Helper()
	var s struct {
		ID string `json:"id"`
	}
	ctl.must(t, "POST", "/api/v1/secrets", map[string]any{"name": name, "kind": kind, "value": value}, &s)
	return s.ID
}

type jobHost struct {
	State        string `json:"state"`
	ExitCode     *int   `json:"exit_code"`
	ErrorClass   string `json:"error_class"`
	ErrorMessage string `json:"error_message"`
	ErrorHint    string `json:"error_hint"`
	Transport    string `json:"transport"`
	Steps        []struct {
		Attempt   int            `json:"attempt"`
		State     string         `json:"state"`
		Changed   *bool          `json:"changed"`
		Truncated bool           `json:"truncated"`
		Result    map[string]any `json:"result"`
	} `json:"steps"`
}

func (j jobHost) String() string {
	return fmt.Sprintf("state=%s exit=%v %s: %s (%s)", j.State, deref(j.ExitCode), j.ErrorClass, j.ErrorMessage, j.ErrorHint)
}

func (j jobHost) changed() *bool {
	if len(j.Steps) == 0 {
		return nil
	}
	return j.Steps[len(j.Steps)-1].Changed
}

func deref(p *int) any {
	if p == nil {
		return nil
	}
	return *p
}

func submit(t *testing.T, hostIDs []string, step map[string]any) string {
	t.Helper()
	var j struct {
		ID string `json:"id"`
	}
	ctl.must(t, "POST", "/api/v1/jobs", map[string]any{"steps": []any{step}, "targets": map[string]any{"hosts": hostIDs}}, &j)
	return j.ID
}

func waitJob(t *testing.T, id string, d time.Duration) string {
	t.Helper()
	var status string
	waitFor(t, d, "job "+id, func() bool {
		var j struct {
			Status string `json:"status"`
		}
		ctl.must(t, "GET", "/api/v1/jobs/"+id, nil, &j)
		status = j.Status
		return status == "completed" || status == "cancelled" || status == "interrupted" || status == "paused"
	})
	return status
}

func getJobHost(t *testing.T, jobID, hostID string) jobHost {
	t.Helper()
	var jh jobHost
	ctl.must(t, "GET", "/api/v1/jobs/"+jobID+"/hosts/"+hostID, nil, &jh)
	return jh
}

// run submits a one-step job on one host and waits for it.
func run(t *testing.T, hostID string, step map[string]any) jobHost {
	t.Helper()
	id := submit(t, []string{hostID}, step)
	wait := 15 * time.Minute
	if ts, ok := step["timeout_s"].(int); ok && time.Duration(ts)*time.Second+time.Minute > wait {
		wait = time.Duration(ts)*time.Second + time.Minute // the step's own timeout decides (slow initramfs builds under emulation)
	}
	waitJob(t, id, wait)
	return getJobHost(t, id, hostID)
}

func output(t *testing.T, jobID, hostID string) string {
	t.Helper()
	var o struct {
		Segments []struct {
			Text string `json:"text"`
		} `json:"segments"`
	}
	ctl.must(t, "GET", "/api/v1/jobs/"+jobID+"/hosts/"+hostID+"/output", nil, &o)
	var b strings.Builder
	for _, s := range o.Segments {
		b.WriteString(s.Text)
	}
	return b.String()
}

func cmd(command string, become bool) map[string]any {
	return map[string]any{"type": "command", "command": command, "become": become}
}

func expect(t *testing.T, cond bool, format string, args ...any) {
	t.Helper()
	if !cond {
		t.Errorf(format, args...)
	}
}

// raw sends a non-JSON request (uploads, downloads) with the session.
func (c *controller) raw(ctx context.Context, method, path string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.base()+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	if c.csrf != "" {
		req.Header.Set("X-DROOL-CSRF", c.csrf)
	}
	return c.client.Do(req)
}

func execCommand(name string, args ...string) *exec.Cmd { return exec.Command(name, args...) }

// insecureClient talks to a test controller's self-signed HTTPS with a
// session cookie jar.
func insecureClient() *http.Client {
	jar, _ := cookiejar.New(nil)
	return &http.Client{Jar: jar, Timeout: time.Minute, Transport: &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
}

func readAll(r *http.Response) string {
	defer r.Body.Close()
	b, _ := io.ReadAll(r.Body)
	return string(b)
}

// newRawRequest builds an authenticated request for a non-JSON body.
func newRawRequest(method, path string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequest(method, ctl.base()+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	if ctl.csrf != "" {
		req.Header.Set("X-DROOL-CSRF", ctl.csrf)
	}
	return req, nil
}
