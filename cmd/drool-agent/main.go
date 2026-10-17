// Command drool-agent is the DROOL agent (ARCHITECTURE-ALPHA §23). It runs
// as root on a managed host, connects outbound to the controller over
// mutual TLS, and acts only within the capability ceiling in
// /etc/drool/agent.yaml.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"drool/internal/agent"
	"drool/internal/agentcaps"
	"drool/internal/agentrpc"
	"drool/internal/monitor/agentmon"
	"drool/internal/release"
	"drool/internal/version"
)

const usage = `drool-agent: the DROOL agent

Usage:
  drool-agent install --token TOKEN [--preset monitor|managed|full] [--trust-release-key FILE] [--no-systemd]
                                        install this binary, write the ceiling, enroll, start the service
  drool-agent init --preset NAME        write /etc/drool/agent.yaml from a preset (never overwrites)
  drool-agent presets                   show exactly what each preset allows
  drool-agent enroll --token TOKEN      enroll with a controller (single-use token)
  drool-agent run                       run the agent (the service does this)
  drool-agent status                    identity, certificate, trust, endpoints, capabilities
  drool-agent trust-release-key FILE    trust an additional release-signing key (security-sensitive)
  drool-agent version [--json]          print the version (--json: commit, channel, protocol, features)

Paths can be redirected for testing with --root DIR (everything under DIR).
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	cmd, args := os.Args[1], os.Args[2:]
	var err error
	switch cmd {
	case "version":
		err = cmdVersion(args)
	case "run":
		err = cmdRun(args)
	case "enroll":
		err = cmdEnroll(args)
	case "init":
		err = cmdInit(args)
	case "presets":
		cmdPresets()
	case "install":
		err = cmdInstall(args)
	case "status":
		err = cmdStatus(args)
	case "trust-release-key":
		err = cmdTrustKey(args)
	case "update-apply":
		err = cmdUpdateApply(args)
	case "update-guard":
		err = cmdUpdateGuard(args)
	case "help", "-h", "--help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", cmd, usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "drool-agent:", err)
		os.Exit(1)
	}
}

func pathsFlag(fs *flag.FlagSet) *string {
	return fs.String("root", "", "keep all files under this directory (testing)")
}

func paths(root string) agent.Paths {
	if root == "" {
		return agent.Default()
	}
	return agent.Under(root)
}

func logger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
}

func cmdRun(args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	root := pathsFlag(fs)
	sup := fs.String("supervisor", "", "systemd or exit (how updates restart the agent)")
	status := fs.Duration("status-every", time.Minute, "telemetry status interval")
	fs.Parse(args)
	p := paths(*root)
	if brokenForTest != "" {
		// Test builds only (go build -tags droolbroken): a release that
		// passes signature and preflight checks but never becomes healthy.
		fmt.Fprintln(os.Stderr, "drool-agent: deliberately broken test build:", brokenForTest)
		if brokenForTest == "hang" {
			select {}
		}
		os.Exit(3)
	}
	a, err := agent.New(agent.Options{Paths: p, Version: version.Version, Log: logger(), Supervisor: *sup, StatusEvery: *status,
		MonitorExtra: runawayCollector})
	if err != nil {
		return err
	}
	defer a.Close()
	if err := p.WritePID(); err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	return a.Run(ctx)
}

func cmdEnroll(args []string) error {
	fs := flag.NewFlagSet("enroll", flag.ExitOnError)
	root := pathsFlag(fs)
	token := fs.String("token", "", "enrollment token from the controller")
	fs.Parse(args)
	if *token == "" {
		return errors.New("--token is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	id, err := agent.Enroll(ctx, paths(*root), *token, version.Version)
	if err != nil {
		return err
	}
	fmt.Printf("Enrolled as host %q (identity %s).\n", id.HostName, id.IdentityID)
	return nil
}

func describe(c agentcaps.Caps) string {
	var b strings.Builder
	yes := func(v bool) string {
		if v {
			return "yes"
		}
		return "no"
	}
	fmt.Fprintf(&b, "  monitoring  %s\n", yes(c.Monitoring))
	fmt.Fprintf(&b, "  services    %s", yes(c.Services.Enabled))
	if len(c.Services.Allowed) > 0 {
		fmt.Fprintf(&b, " (only %s)", strings.Join(c.Services.Allowed, ", "))
	}
	fmt.Fprintf(&b, "\n  reboot      %s\n", yes(c.Reboot))
	fmt.Fprintf(&b, "  packages    %s", yes(c.Packages.Enabled))
	if c.Packages.Enabled && c.Packages.ConfiguredRepositoriesOnly {
		b.WriteString(" (configured repositories only)")
	}
	fmt.Fprintf(&b, "\n  files       %s", yes(c.Files.Enabled))
	if c.Files.Enabled {
		if len(c.Files.AllowedPaths) > 0 {
			fmt.Fprintf(&b, " (only %s)", strings.Join(c.Files.AllowedPaths, ", "))
		} else {
			b.WriteString(" (whole filesystem)")
		}
	}
	fmt.Fprintf(&b, "\n  shell       %s\n  firewall    %s (DROOL firewall policy, applied as dead-man transactions)\n  users       %s (reserved)\n", yes(c.Shell), yes(c.Firewall), yes(c.Users))
	if re := c.RootEquivalent(); len(re) > 0 {
		b.WriteString("  ROOT-EQUIVALENT: the controller can do anything root can on this host, because of:\n")
		for _, r := range re {
			b.WriteString("    - " + r + "\n")
		}
	}
	return b.String()
}

func cmdPresets() {
	for _, n := range []string{agentcaps.PresetMonitor, agentcaps.PresetManaged, agentcaps.PresetFull} {
		c, _ := agentcaps.Preset(n)
		fmt.Printf("%s:\n%s\n", n, describe(c))
	}
}

func writeCeiling(p agent.Paths, preset string, force bool) (bool, error) {
	if _, err := os.Stat(p.Config); err == nil && !force {
		return false, nil // never overwrite a local decision
	}
	c, err := agentcaps.Preset(preset)
	if err != nil {
		return false, err
	}
	if err := os.MkdirAll(filepath.Dir(p.Config), 0o755); err != nil {
		return false, err
	}
	return true, os.WriteFile(p.Config, agentcaps.Render(c, preset), 0o600)
}

func cmdInit(args []string) error {
	fs := flag.NewFlagSet("init", flag.ExitOnError)
	root := pathsFlag(fs)
	preset := fs.String("preset", "", "monitor, managed or full")
	force := fs.Bool("force", false, "replace an existing agent.yaml")
	fs.Parse(args)
	p := paths(*root)
	wrote, err := writeCeiling(p, *preset, *force)
	if err != nil {
		return err
	}
	if !wrote {
		return fmt.Errorf("%s already exists; edit it or use --force", p.Config)
	}
	c, _ := agentcaps.Preset(*preset)
	fmt.Printf("Wrote %s (%s):\n%s", p.Config, *preset, describe(c))
	return nil
}

func copyFile(src, dst string, perm os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".tmp"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, perm)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return err
	}
	out.Close()
	return os.Rename(tmp, dst)
}

// unit is the service. The resource budget (M11) keeps the agent from
// ever competing with the host's workload: a low CPU weight, a hard CPU
// quota, a memory high-water mark and a hard ceiling. Operations run in
// their own scopes outside it (see internal/agent/limits.go); the
// controller may tune the budget at runtime.
var unit = fmt.Sprintf(`[Unit]
Description=DROOL agent
After=network-online.target
Wants=network-online.target
StartLimitIntervalSec=0

[Service]
Type=simple
# The update guard runs from the previous (known-good) version: it rolls
# back a new version that crash-loops or never becomes healthy.
ExecStartPre=-/usr/libexec/drool-agent/previous/drool-agent update-guard
ExecStart=/usr/libexec/drool-agent/current/drool-agent run
Restart=always
RestartSec=5
UMask=0077
# Resource budget: the managed workload wins.
CPUWeight=%d
CPUQuota=%d%%
MemoryHigh=%dM
MemoryMax=%dM
IOWeight=20
TasksMax=256
OOMScoreAdjust=300

[Install]
WantedBy=multi-user.target
`, agentmon.DefaultCPUWeight, agentmon.DefaultCPUQuotaPct, agentmon.DefaultMemoryHighMB, agentmon.DefaultMemoryMaxMB)

func cmdInstall(args []string) error {
	fs := flag.NewFlagSet("install", flag.ExitOnError)
	root := pathsFlag(fs)
	token := fs.String("token", "", "enrollment token")
	tokenFile := fs.String("token-file", "", "read the enrollment token from this file (- for stdin; keeps it out of the process list)")
	preset := fs.String("preset", "", "ceiling preset if /etc/drool/agent.yaml doesn't exist yet")
	keyFile := fs.String("trust-release-key", "", "also trust this release-signing public key")
	noSystemd := fs.Bool("no-systemd", false, "don't install or start the systemd service")
	fs.Parse(args)
	p := paths(*root)
	if os.Getuid() != 0 && *root == "" {
		return errors.New("install must run as root")
	}
	if *tokenFile != "" {
		var b []byte
		var err error
		if *tokenFile == "-" {
			b, err = io.ReadAll(io.LimitReader(os.Stdin, 64<<10))
		} else {
			b, err = os.ReadFile(*tokenFile)
		}
		if err != nil {
			return err
		}
		*token = strings.TrimSpace(string(b))
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	v := version.Version
	vdir := filepath.Join(p.Install, "versions", v)
	if err := os.MkdirAll(vdir, 0o755); err != nil {
		return err
	}
	dst := filepath.Join(vdir, "drool-agent")
	if real, _ := filepath.EvalSymlinks(self); real != dst {
		if err := copyFile(self, dst, 0o755); err != nil {
			return err
		}
	}
	tmp := filepath.Join(p.Install, "current.tmp")
	os.Remove(tmp)
	if err := os.Symlink(filepath.Join("versions", v), tmp); err != nil {
		return err
	}
	if err := os.Rename(tmp, filepath.Join(p.Install, "current")); err != nil {
		return err
	}
	fmt.Printf("Installed drool-agent %s in %s.\n", v, vdir)

	if _, err := os.Stat(p.Config); err != nil {
		if *preset == "" {
			return fmt.Errorf("%s doesn't exist: choose what DROOL may do here with --preset monitor, managed or full (see `drool-agent presets`)", p.Config)
		}
		if _, err := writeCeiling(p, *preset, false); err != nil {
			return err
		}
		c, _ := agentcaps.Preset(*preset)
		fmt.Printf("Wrote the capability ceiling %s (%s):\n%s", p.Config, *preset, describe(c))
	} else {
		fmt.Printf("Keeping the existing capability ceiling %s.\n", p.Config)
	}
	if *keyFile != "" {
		if err := trustKey(p, *keyFile); err != nil {
			return err
		}
	}
	st, err := agent.OpenStore(p.State)
	if err != nil {
		return err
	}
	_, idErr := st.Identity()
	switch {
	case errors.Is(idErr, agent.ErrNotEnrolled):
		if *token == "" {
			return errors.New("--token is required for the first installation")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		id, err := agent.Enroll(ctx, p, *token, v)
		if err != nil {
			return err
		}
		fmt.Printf("Enrolled as host %q (identity %s).\n", id.HostName, id.IdentityID)
	case *token != "":
		// A token on an enrolled host means "enroll again" (e.g. after the
		// identity was revoked). Enroll beside the current identity first,
		// so a bad token never breaks a working agent.
		if err := reenroll(p, *token, v); err != nil {
			return err
		}
	default:
		fmt.Println("Already enrolled; keeping the identity.")
	}
	if *noSystemd {
		return nil
	}
	if fi, err := os.Stat("/run/systemd/system"); err != nil || !fi.IsDir() {
		return startSupervisor(p)
	}
	if err := os.WriteFile("/etc/systemd/system/drool-agent.service", []byte(unit), 0o644); err != nil {
		return err
	}
	for _, c := range [][]string{{"daemon-reload"}, {"enable", "drool-agent.service"}, {"restart", "drool-agent.service"}} {
		if out, err := exec.Command("systemctl", c...).CombinedOutput(); err != nil {
			return fmt.Errorf("systemctl %s: %v: %s", strings.Join(c, " "), err, strings.TrimSpace(string(out)))
		}
	}
	fmt.Println("The drool-agent service is running.")
	return nil
}

// supervisor mirrors the systemd unit on hosts without systemd: the update
// guard (from the previous, known-good version) before every start, then
// the agent, restarted when it exits.
const supervisor = `#!/bin/sh
# DROOL agent supervisor for hosts without systemd (written by drool-agent install).
D=%s
while :; do
  [ -x "$D/previous/drool-agent" ] && "$D/previous/drool-agent" update-guard %s
  "$D/current/drool-agent" run --supervisor exit %s
  sleep 2
done
`

// identityFiles are the enrollment's files in the state directory.
var identityFiles = []string{"identity.key", "identity.json", "cert.pem", "root.pem", "manifest.json", "endpoints.json", "domains.json", "requested-caps.json"}

// reenroll enrolls with a new identity key in a scratch directory and, only
// when that worked, moves the old identity aside (never deleted) and the
// new one into place.
func reenroll(p agent.Paths, token, v string) error {
	scratch := filepath.Join(p.State, ".enroll-new")
	os.RemoveAll(scratch)
	if err := os.MkdirAll(scratch, 0o700); err != nil {
		return err
	}
	defer os.RemoveAll(scratch)
	np := p
	np.State = scratch
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	id, err := agent.Enroll(ctx, np, token, v)
	if err != nil {
		return fmt.Errorf("enrolling again (the current identity is unchanged): %w", err)
	}
	old := filepath.Join(p.State, "replaced-"+time.Now().UTC().Format("20060102T150405Z"))
	if err := os.MkdirAll(old, 0o700); err != nil {
		return err
	}
	for _, f := range append(identityFiles, filepath.Join("update", "result.json")) {
		if err := os.Rename(filepath.Join(p.State, f), filepath.Join(old, filepath.Base(f))); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	for _, f := range identityFiles {
		if f == "identity.json" {
			continue // last: marks the enrollment complete
		}
		if err := os.Rename(filepath.Join(scratch, f), filepath.Join(p.State, f)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	if err := os.Rename(filepath.Join(scratch, "identity.json"), filepath.Join(p.State, "identity.json")); err != nil {
		return err
	}
	fmt.Printf("Enrolled again as host %q (identity %s); the previous identity was moved to %s.\n", id.HostName, id.IdentityID, old)
	return nil
}

// stopRunning stops a supervisor and agent started by an earlier install
// (hosts without systemd), so only one agent ever uses the state directory.
func stopRunning(p agent.Paths) {
	script := filepath.Join(p.Install, "drool-agent-supervise")
	ents, _ := os.ReadDir("/proc")
	var agents []int
	for _, e := range ents {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid == os.Getpid() {
			continue
		}
		b, err := os.ReadFile(filepath.Join("/proc", e.Name(), "cmdline"))
		if err != nil {
			continue
		}
		argv := strings.Split(strings.TrimRight(string(b), "\x00"), "\x00")
		switch {
		case len(argv) >= 2 && argv[1] == script:
			syscall.Kill(pid, syscall.SIGTERM) // the supervisor first, or it restarts the agent
		case len(argv) >= 2 && strings.HasPrefix(argv[0], p.Install+"/") && argv[1] == "run":
			agents = append(agents, pid)
		}
	}
	for _, pid := range agents {
		syscall.Kill(pid, syscall.SIGTERM)
	}
	if len(agents) > 0 {
		time.Sleep(time.Second)
	}
}

func startSupervisor(p agent.Paths) error {
	stopRunning(p)
	root := ""
	if p.Install != agent.Default().Install {
		root = "--root " + filepath.Dir(p.Install)
	}
	script := filepath.Join(p.Install, "drool-agent-supervise")
	if err := os.WriteFile(script, []byte(fmt.Sprintf(supervisor, p.Install, root, root)), 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(p.Log, 0o700); err != nil {
		return err
	}
	logf, err := os.OpenFile(filepath.Join(p.Log, "agent.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	cmd := exec.Command("/bin/sh", script)
	cmd.Stdout, cmd.Stderr = logf, logf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	cmd.Process.Release()
	fmt.Printf("systemd is not running here: started %s in the background (log %s/agent.log).\n"+
		"It does not survive a reboot; start it from your init system.\n", script, p.Log)
	return nil
}

func trustKey(p agent.Paths, file string) error {
	b, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	keys, err := release.ParsePublicKeys(b)
	if err != nil || len(keys) != 1 {
		return fmt.Errorf("%s: expected exactly one release public key line (%v)", file, err)
	}
	if err := os.MkdirAll(p.ReleaseKeys, 0o755); err != nil {
		return err
	}
	dst := filepath.Join(p.ReleaseKeys, keys[0].ID()+".pub")
	if err := os.WriteFile(dst, []byte(release.FormatPublicKey(keys[0].Key, keys[0].Name)+"\n"), 0o644); err != nil {
		return err
	}
	fmt.Printf("SECURITY: this host now runs agent updates signed by key %s (%s), stored in %s.\n"+
		"Anyone holding that key's private half can install code that runs as root here. Remove the file to stop trusting it.\n",
		keys[0].ID(), keys[0].Name, dst)
	return nil
}

func cmdTrustKey(args []string) error {
	fs := flag.NewFlagSet("trust-release-key", flag.ExitOnError)
	root := pathsFlag(fs)
	fs.Parse(args)
	if fs.NArg() != 1 {
		return errors.New("usage: drool-agent trust-release-key FILE")
	}
	return trustKey(paths(*root), fs.Arg(0))
}

// cmdVersion prints exactly the version and nothing else: an agent that
// updates runs the new binary's "version" before switching to it and
// requires its signed version back (agent.ApplyUpdate's preflight), in
// every Shepherd release so far. Details are behind --json.
func cmdVersion(args []string) error {
	fs := flag.NewFlagSet("version", flag.ExitOnError)
	asJSON := fs.Bool("json", false, "print build details as JSON")
	fs.Parse(args)
	if !*asJSON {
		fmt.Println(version.Version)
		return nil
	}
	r := agentrpc.Local()
	out := struct {
		version.Build
		Protocol struct {
			Min uint32 `json:"min"`
			Max uint32 `json:"max"`
		} `json:"protocol"`
		Features []string `json:"features"`
		// The DROOL project's Shepherd release keys this binary trusts for
		// its updates (unless its host's agent.yaml denies them).
		OfficialReleaseKeys []string `json:"official_release_keys"`
	}{Build: version.Get(), Features: agent.Features, OfficialReleaseKeys: release.OfficialKeyIDs()}
	out.Protocol.Min, out.Protocol.Max = r.Min, r.Max
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

func cmdStatus(args []string) error {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	root := pathsFlag(fs)
	fs.Parse(args)
	p := paths(*root)
	st, err := agent.OpenStore(p.State)
	if err != nil {
		return err
	}
	b := version.Get()
	fmt.Printf("drool-agent %s (%s, commit %s, protocol %s–%s)\n", b.Version, b.Channel, b.ShortCommit(), agentrpc.ProtocolMin, agentrpc.ProtocolMax)
	id, err := st.Identity()
	if err != nil {
		fmt.Println("Not enrolled.")
	} else {
		fmt.Printf("Host:        %s (%s)\nIdentity:    %s\nEpoch:       %s\n", id.HostName, id.HostID, id.IdentityID, id.ControllerEpoch)
		if chain, err := st.Chain(); err == nil {
			fmt.Printf("Certificate: valid until %s\n", chain[0].NotAfter.Local().Format(time.RFC1123))
		}
		if m, _, err := st.Manifest(); err == nil {
			fmt.Printf("Trust:       manifest version %d, %d trusted intermediate(s)\n", m.Version, len(m.TrustedIntermediates))
		}
		if ep, err := st.Endpoints(); err == nil {
			fmt.Printf("Controller:  %s\n", strings.Join(ep.All(), ", "))
		}
	}
	f, err := agent.LoadCeiling(p.Config)
	if err != nil {
		return err
	}
	fmt.Printf("Capability ceiling (%s):\n%s", p.Config, describe(f.Capabilities))
	if keys, err := p.TrustedReleaseKeys(f.OfficialReleases()); err == nil {
		official := "trusted"
		if !f.OfficialReleases() {
			official = "not trusted (updates.official: deny)"
		} else if len(release.OfficialKeys()) == 0 {
			official = "none built into this build"
		}
		fmt.Printf("Official DROOL releases: %s\n", official)
		fmt.Printf("Trusted release keys: %d\n", len(keys))
		for _, k := range keys {
			fmt.Printf("  %s %s\n", k.ID(), k.Name)
		}
	}
	var o map[string]any
	if b, err := os.ReadFile(filepath.Join(p.State, "update", "result.json")); err == nil && json.Unmarshal(b, &o) == nil {
		fmt.Printf("Last update: %v → %v: %v (%v)\n", o["from"], o["to"], o["state"], o["detail"])
	}
	return nil
}

func helperLog(p agent.Paths) func(string, ...any) {
	return func(format string, args ...any) {
		f, err := os.OpenFile(filepath.Join(p.Log, "update-helper.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return
		}
		defer f.Close()
		fmt.Fprintf(f, "%s %s\n", time.Now().UTC().Format(time.RFC3339), fmt.Sprintf(format, args...))
	}
}

func cmdUpdateApply(args []string) error {
	fs := flag.NewFlagSet("update-apply", flag.ExitOnError)
	rel := fs.String("release", "", "")
	from := fs.String("from", "", "")
	to := fs.String("to", "", "")
	window := fs.Duration("window", 2*time.Minute, "")
	enc := fs.String("paths", "", "")
	sup := fs.String("supervisor", "systemd", "")
	watch := fs.Bool("watch", false, "")
	fs.Parse(args)
	p := agent.Default()
	if *enc != "" {
		var err error
		if p, err = agent.DecodePaths(*enc); err != nil {
			return err
		}
	}
	return agent.ApplyUpdate(agent.HelperOptions{Paths: p, ReleaseID: *rel, From: *from, To: *to, Window: *window,
		Supervisor: *sup, Watch: *watch, Log: helperLog(p)})
}

func cmdUpdateGuard(args []string) error {
	fs := flag.NewFlagSet("update-guard", flag.ExitOnError)
	root := pathsFlag(fs)
	fs.Parse(args)
	p := paths(*root)
	return agent.Guard(p, helperLog(p))
}
