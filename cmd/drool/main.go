// Command drool is the DROOL controller.
package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"golang.org/x/term"

	"drool/internal/agentsvc"
	"drool/internal/alerts"
	"drool/internal/api"
	"drool/internal/artifacts"
	"drool/internal/audit"
	"drool/internal/auth"
	"drool/internal/config"
	"drool/internal/desired"
	"drool/internal/driver/sshdriver"
	"drool/internal/engine"
	"drool/internal/filedeploy"
	"drool/internal/firewall/fwsvc"
	"drool/internal/inventory"
	"drool/internal/logging"
	"drool/internal/mgmt"
	"drool/internal/model"
	"drool/internal/monitor/collect"
	"drool/internal/monitor/monsvc"
	"drool/internal/netmon/netsvc"
	"drool/internal/poller"
	"drool/internal/sched"
	"drool/internal/secrets"
	"drool/internal/sshx"
	"drool/internal/store"
	"drool/internal/store/db"
	"drool/internal/tlsutil"
	"drool/internal/verify"
	"drool/internal/version"
	"drool/web"
)

const usage = `Usage: drool [--config FILE] <command> [options]

Commands:
  serve                 Run the controller
  migrate               Apply database migrations and exit
  backup [-o FILE] [--include-master-key]
                        Back up the controller (safe while it runs)
  restore FILE          Restore a backup (stop the controller first)
  setup code            Issue a new one-time setup code for first-admin setup
  user create           Create a local user (prompts for password); recovery path
  user mfa-reset        Turn off a user's two-factor authentication (lost authenticator); recovery path
  agent-ca status       Agent PKI: root (online/offline), intermediate, trust manifest
  agent-ca export-root  Write the Agent Root CA key and certificate for offline storage
  agent-ca remove-root  Remove the root private key from the controller (and future backups)
  agent-ca rotate       New intermediate CA and trust manifest (--root-key FILE, --revoke-old)
  agent-ca sign-manifest / import-manifest   Sign a trust manifest offline / import it
  release keygen        Create an agent release-signing key pair (keep the private key offline)
  release sign          Sign an agent binary: writes BINARY.release.json
  release verify        Check a signed release against a public key
  version               Print version information

Configuration is read from built-in defaults, then the config file
(default ` + config.DefaultPath + ` if present), then DROOL_* environment variables.
`

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "drool:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	global := flag.NewFlagSet("drool", flag.ContinueOnError)
	global.Usage = func() { fmt.Fprint(os.Stderr, usage) }
	cfgPath := global.String("config", "", "path to config file")
	if err := global.Parse(args); err != nil {
		return err
	}
	rest := global.Args()
	if len(rest) == 0 {
		global.Usage()
		return errors.New("no command given")
	}
	explicit := *cfgPath != ""

	switch rest[0] {
	case "version":
		fmt.Printf("drool %s (commit %s, built %s)\n", version.Version, version.Commit, version.Date)
		return nil
	case "serve":
		return cmdServe(*cfgPath, explicit)
	case "migrate":
		return cmdMigrate(*cfgPath, explicit)
	case "backup":
		return cmdBackup(*cfgPath, explicit, rest[1:])
	case "restore":
		return cmdRestore(*cfgPath, explicit, rest[1:])
	case "setup":
		if len(rest) < 2 || rest[1] != "code" {
			return errors.New("usage: drool setup code")
		}
		return cmdSetupCode(*cfgPath, explicit, rest[2:])
	case "release":
		return cmdRelease(rest[1:])
	case "agent-ca":
		return cmdAgentCA(*cfgPath, explicit, rest[1:])
	case "user":
		switch {
		case len(rest) >= 2 && rest[1] == "create":
			return cmdUserCreate(*cfgPath, explicit, rest[2:])
		case len(rest) >= 2 && rest[1] == "mfa-reset":
			return cmdUserMFAReset(*cfgPath, explicit, rest[2:])
		}
		return errors.New("usage: drool user create --username NAME [--role admin] | drool user mfa-reset --username NAME")
	default:
		global.Usage()
		return fmt.Errorf("unknown command %q", rest[0])
	}
}

func loadConfig(path string, explicit bool) (*config.Loaded, error) {
	cfg, err := config.Load(path, explicit, os.Environ())
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(cfg.DataDir, 0o750); err != nil {
		return nil, fmt.Errorf("create data dir: %w", err)
	}
	return cfg, nil
}

func openStore(ctx context.Context, cfg *config.Loaded) (*store.Store, []string, error) {
	st, err := store.Open(filepath.Join(cfg.DataDir, store.FileName))
	if err != nil {
		return nil, nil, err
	}
	applied, err := st.Migrate(ctx)
	if err != nil {
		st.Close()
		return nil, nil, err
	}
	return st, applied, nil
}

func cmdMigrate(path string, explicit bool) error {
	cfg, err := loadConfig(path, explicit)
	if err != nil {
		return err
	}
	st, applied, err := openStore(context.Background(), cfg)
	if err != nil {
		return err
	}
	defer st.Close()
	if len(applied) == 0 {
		fmt.Println("database is up to date")
	}
	for _, m := range applied {
		fmt.Println("applied", m)
	}
	return nil
}

func cmdUserCreate(path string, explicit bool, args []string) error {
	fs := flag.NewFlagSet("user create", flag.ContinueOnError)
	username := fs.String("username", "", "username (required)")
	role := fs.String("role", auth.RoleAdmin, "role: "+strings.Join(auth.Roles, ", "))
	pwStdin := fs.Bool("password-stdin", false, "read the password from the first line of stdin")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *username == "" {
		return errors.New("--username is required")
	}
	cfg, err := loadConfig(path, explicit)
	if err != nil {
		return err
	}
	password, err := readPassword(*pwStdin)
	if err != nil {
		return err
	}

	ctx := context.Background()
	st, _, err := openStore(ctx, cfg)
	if err != nil {
		return err
	}
	defer st.Close()
	// CLI commands persist audit records to the database only; mirroring them to
	// the log stream would print JSON on stdout in container deployments.
	logs := logging.Discard()
	svc := newAuthService(cfg, st)
	u, err := svc.CreateUser(ctx, *username, password, *role)
	if err != nil {
		return err
	}
	if u.Role == auth.RoleAdmin {
		// An administrator exists now, so pending setup codes must stop working.
		if err := svc.ClearSetupCodes(ctx); err != nil {
			return err
		}
	}
	actor := audit.Actor{Type: audit.ActorSystem, Name: "cli:" + currentOSUser()}
	if err := audit.NewWriter(st, logs.Audit).Record(ctx, audit.Event{
		Action: "user.create", TargetType: "user", TargetID: u.ID, Actor: &actor,
		Details: map[string]any{"username": u.Username, "role": u.Role, "via": "cli"},
	}); err != nil {
		return fmt.Errorf("user created but audit record failed: %w", err)
	}
	fmt.Printf("created user %s (%s) with role %s\n", u.Username, u.ID, u.Role)
	return nil
}

// cmdUserMFAReset turns off a user's two-factor authentication from the
// command line: the way back for the last administrator who lost both the
// authenticator and the recovery codes. Shell access to the controller is
// the authorization; it is audited.
func cmdUserMFAReset(path string, explicit bool, args []string) error {
	fs := flag.NewFlagSet("user mfa-reset", flag.ContinueOnError)
	username := fs.String("username", "", "username (required)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *username == "" {
		return errors.New("--username is required")
	}
	cfg, err := loadConfig(path, explicit)
	if err != nil {
		return err
	}
	ctx := context.Background()
	st, _, err := openStore(ctx, cfg)
	if err != nil {
		return err
	}
	defer st.Close()
	u, err := st.R.GetUserByUsername(ctx, *username)
	if err != nil {
		return fmt.Errorf("no user %q", *username)
	}
	svc := newAuthService(cfg, st)
	actor := audit.Actor{Type: audit.ActorSystem, Name: "cli:" + currentOSUser()}
	w := audit.NewWriter(st, logging.Discard().Audit)
	var had bool
	err = st.InTx(ctx, func(q *db.Queries) error {
		var err error
		if had, err = svc.DisableMFATx(ctx, q, u.ID); err != nil || !had {
			return err
		}
		if err := q.DeleteUserSessions(ctx, u.ID); err != nil {
			return err
		}
		_, err = w.RecordTx(ctx, q, audit.Event{Action: "user.mfa_reset", Actor: &actor, TargetType: "user", TargetID: u.ID,
			Details: map[string]any{"username": u.Username, "via": "cli"}})
		return err
	})
	if err != nil {
		return err
	}
	if !had {
		fmt.Printf("%s doesn't use two-factor authentication; nothing changed\n", u.Username)
		return nil
	}
	fmt.Printf("Two-factor authentication is off for %s; their sessions were ended. They sign in with their password and can set it up again.\n", u.Username)
	return nil
}

func readPassword(fromStdin bool) (string, error) {
	if fromStdin || !term.IsTerminal(int(os.Stdin.Fd())) {
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return "", err
		}
		return strings.TrimRight(line, "\r\n"), nil
	}
	fmt.Fprint(os.Stderr, "Password: ")
	p1, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", err
	}
	fmt.Fprint(os.Stderr, "Confirm password: ")
	p2, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", err
	}
	if string(p1) != string(p2) {
		return "", errors.New("passwords do not match")
	}
	return string(p1), nil
}

func currentOSUser() string {
	if u := os.Getenv("USER"); u != "" {
		return u
	}
	return strconv.Itoa(os.Getuid())
}

func newAuthService(cfg *config.Loaded, st *store.Store) *auth.Service {
	window := cfg.Auth.ReauthWindow
	if os.Getenv("DROOL_TEST_AUTH_TIMING") == "fast" {
		// Browser tests can't wait five minutes for a confirmation to lapse.
		window = 3 * time.Second
	}
	return auth.NewService(st, auth.Options{
		IdleTimeout: cfg.Auth.SessionIdleTimeout, AbsoluteTimeout: cfg.Auth.SessionAbsoluteTimeout,
		ReauthWindow: window,
	})
}

func cmdServe(path string, explicit bool) error {
	cfg, err := loadConfig(path, explicit)
	if err != nil {
		return err
	}
	logs, err := logging.New(cfg.Logging, cfg.DataDir)
	if err != nil {
		return err
	}
	defer logs.Close()
	log := logs.App

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	unlock, err := store.LockDataDir(cfg.DataDir)
	if err != nil {
		return fmt.Errorf("%s: %w (is another drool serve running, or a restore in progress?)", cfg.DataDir, err)
	}
	defer unlock()

	st, applied, err := openStore(ctx, cfg)
	if err != nil {
		return err
	}
	defer st.Close()
	for _, m := range applied {
		log.Info("applied migration", "migration", m)
	}

	tlsRes, err := tlsutil.Setup(cfg.Server, cfg.DataDir)
	if err != nil {
		return err
	}

	reg := prometheus.NewRegistry()
	reg.MustRegister(collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))

	authSvc := newAuthService(cfg, st)
	if os.Getenv("DROOL_TEST_AUTH_TIMING") == "fast" {
		log.Warn("TEST MODE: high-impact actions need re-authentication after 3 s; never use this in production")
	}
	setup, err := prepareSetup(ctx, cfg, authSvc, logs)
	if err != nil {
		return fmt.Errorf("first-admin setup: %w", err)
	}
	auditW := audit.NewWriter(st, logs.Audit)

	box, created, insecureKey, err := loadMasterKey(ctx, cfg, st)
	if err != nil {
		return err
	}
	if created {
		log.Info("generated credential master key", "file", cfg.MasterKeyPath())
	}
	if insecureKey {
		logs.Security.Warn("master key file is readable by group or others; restrict it to the drool user (chmod 600)",
			"file", cfg.MasterKeyPath())
	}
	authSvc.SetSealer(box)
	// Tamper-evident audit history (M16): checkpoints and verification,
	// and the optional remote copy.
	integrity := audit.NewIntegrity(audit.IntegrityDeps{Store: st, Writer: auditW, Keys: box, DataDir: cfg.DataDir, Log: logs.Security})
	if err := integrity.RecoverInterrupted(ctx); err != nil {
		return err
	}
	auditSink := audit.NewSink(audit.SinkDeps{Store: st, Box: box, Log: logs.Security, Controller: installationName()})
	auditW.OnAppend(func(int64) { auditSink.Kick() })
	secretSvc := secrets.NewService(st, box)
	if _, created, err := secretSvc.EnsureControllerKey(ctx, func(q *db.Queries, row db.Secret) error {
		_, err := auditW.RecordTx(ctx, q, audit.Event{Action: "credential.create", Actor: &audit.System,
			TargetType: "credential", TargetID: row.ID,
			Details: map[string]any{"name": row.Name, "kind": row.Kind, "generated": true}})
		return err
	}); err != nil {
		return fmt.Errorf("controller SSH key: %w", err)
	} else if created {
		log.Info("generated controller SSH key", "credential", secrets.ControllerKeyName)
	}
	dialer := &sshx.Dialer{}
	pool := sshx.NewPool(dialer)
	defer pool.Close()
	inv := inventory.NewService(st, secretSvc, dialer, pool)
	arts, err := artifacts.Open(cfg.DataDir, st)
	if err != nil {
		return err
	}
	recorder := &filedeploy.StoreRecorder{Store: st, Audit: auditW}
	executor := &sshdriver.Executor{Pool: pool, Files: &sshdriver.FileDeps{Source: arts, Recorder: recorder}}

	// Agent PKI and endpoint (ARCHITECTURE-ALPHA §23).
	pki, pkiCreated, err := agentsvc.OpenPKI(ctx, st, box, installationName())
	agentsOK := true
	if errors.Is(err, agentsvc.ErrPKIUnusable) {
		// After a restore without the master key: SSH management keeps
		// working; the agent endpoint waits for a new intermediate.
		logs.Security.Error("agent endpoint disabled: " + err.Error())
		agentsOK = false
	} else if err != nil {
		return fmt.Errorf("agent PKI: %w (if the master key was replaced, restore the original key file)", err)
	}
	if agentsOK && pkiCreated {
		log.Info("created the DROOL Agent PKI (root and intermediate CA, trust manifest 1)", "root", pki.RootFingerprint())
		_ = auditW.Record(ctx, audit.Event{Action: "agent_ca.create", Actor: &audit.System,
			Details: map[string]any{"root_fingerprint": pki.RootFingerprint()}})
	}
	if agentsOK && pki.RootOnline() {
		logs.Security.Warn("the Agent Root CA private key is stored on this controller; export it to offline storage and remove it (Agents → Trust in the web UI, or `drool agent-ca export-root` then `drool agent-ca remove-root`)")
	}
	var agents *agentsvc.Service
	if agentsOK {
		if agents, err = agentsvc.New(ctx, agentsvc.Options{Config: cfg.Agent, Store: st, PKI: pki, Audit: auditW, Inventory: inv,
			Artifacts: arts, Recorder: recorder, Log: logs.App.With("component", "agent")}); err != nil {
			return err
		}
	}
	controllerEpoch, err := agentsvc.ControllerEpoch(ctx, st)
	if err != nil {
		return err
	}
	auditSink.SetEpoch(func() string { return controllerEpoch })
	router := &mgmt.Router{St: st, Inv: inv, SSH: executor, Agents: agents}
	fw, err := fwsvc.New(ctx, fwsvc.Deps{Store: st, Inv: inv, Agents: agents, Audit: auditW, Log: logs.App.With("component", "firewall"),
		Decide: router.FirewallDecision})
	if err != nil {
		return fmt.Errorf("firewall: %w", err)
	}
	router.Firewall = fw
	if agents != nil {
		agents.SetFirewallHooks(fw)
	}
	eng := engine.New(st, inv, router, auditW, logs.Job, engine.Config{
		GlobalConcurrency:  cfg.Engine.GlobalConcurrency,
		PerJumpConcurrency: cfg.Engine.PerJumpHostConcurrency,
		OutputCap:          cfg.Engine.OutputCapBytes,
		RetentionDays:      cfg.Retention.JobsDays,
	})
	if err := eng.Start(ctx); err != nil {
		return fmt.Errorf("job engine: %w", err)
	}
	defer eng.Stop()

	pollCfg := poller.Config{
		StatusInterval:  cfg.Poller.StatusInterval,
		UpdatesInterval: cfg.Poller.UpdatesInterval,
		Workers:         cfg.Poller.Workers,
	}
	poll := poller.New(st, inv, eng.Locks(), eng.Saturated, logs.App, pollCfg)
	poll.AgentManaged = router.AgentHealthy
	eng.OnHostChanged(poll.RunNow)
	// System monitoring (M11): agents stream it, SSH hosts are polled.
	mon := monsvc.New(monsvc.Deps{Store: st, Agents: agents, Inventory: inv, Engine: eng, Audit: auditW, Log: logs.App.With("component", "monitoring")})
	if agents != nil {
		agents.SetMonitorHooks(mon)
		agents.SetLocker(func(hostID string) (func(), bool) {
			return eng.Locks().TryAcquire(hostID, "agent-install", model.LockExclusive)
		})
	}
	poll.MonitorEvery = func(ctx context.Context) time.Duration {
		if s := mon.Settings(ctx); s.Enabled {
			return time.Duration(s.SSHPollS) * time.Second
		}
		return 0
	}
	poll.Monitor = func(ctx context.Context, h db.Host) error {
		run := func(ctx context.Context, script string) ([]byte, error) {
			r, err := inv.RunOnHost(ctx, h.ID, "sh -s", []byte(script), false, 2*time.Minute)
			if err != nil {
				return nil, err
			}
			return r.Stdout, nil
		}
		var runRoot collect.Runner
		if inventory.CanBecomeRoot(h) {
			runRoot = func(ctx context.Context, script string) ([]byte, error) {
				r, err := inv.RunOnHost(ctx, h.ID, "sh -s", []byte(script), true, 3*time.Minute)
				if err != nil {
					return nil, err
				}
				return r.Stdout, nil
			}
		}
		return mon.PollSSH(ctx, h.ID, run, runRoot)
	}
	if cfg.Agent.Enabled && agents != nil {
		pki.SetHosts(advertisedHosts(cfg.Agent.Advertise))
		bind := cfg.Agent.Bind
		if bind == "" {
			bind = cfg.Server.Bind
		}
		agentAddr := net.JoinHostPort(bind, strconv.Itoa(cfg.Agent.Port))
		if err := agents.Listen(agentAddr); err != nil {
			return err
		}
		defer agents.Stop()
		go agents.RunRollouts(ctx)
		log.Info("agent endpoint listening", "listen", agentAddr, "root_ca", pki.RootFingerprint())
	}
	go poll.Run(ctx)
	go fw.Run(ctx)
	network := netsvc.New(netsvc.Deps{Store: st, Firewall: fw, GeoIPDir: cfg.GeoIPDir(), Log: logs.App.With("component", "network")})
	go network.Run(ctx)
	// Alerting (M12) consumes the health issues monitoring records.
	alertCfg := alerts.Config{}
	if os.Getenv("DROOL_TEST_ALERT_TIMING") == "fast" {
		log.Warn("TEST MODE: alert timings shortened (maintenance grace 3 s, tick 2 s); never use this in production")
		alertCfg.Grace, alertCfg.Tick, alertCfg.Poll = 3*time.Second, 2*time.Second, 500*time.Millisecond
	}
	alertSvc := alerts.New(alerts.Deps{Store: st, Box: box, Audit: auditW, Log: logs.App.With("component", "alerts"), Config: alertCfg})
	alertSvc.Attach(mon)
	integrity.OnFailure(func(v audit.Verification) {
		_ = alertSvc.RecordEvent(context.Background(), alerts.Event{Kind: "audit.integrity", Severity: "critical", ObjectType: "audit",
			ObjectID: v.ID, Title: "Audit history verification failed: " + v.Problem})
	})
	// The last checkpoint is written when this stops, before the store
	// closes (deferred earlier, so it runs later).
	integrityCtx, stopIntegrity := context.WithCancel(ctx)
	integrityDone := make(chan struct{})
	go func() {
		defer close(integrityDone)
		integrity.Run(integrityCtx)
	}()
	defer func() {
		stopIntegrity()
		<-integrityDone
	}()
	go auditSink.Run(ctx)
	go mon.Run(ctx)
	go alertSvc.Run(ctx)
	// Scheduling and workflows (M13): workflow verification steps run on
	// the controller inside a host's job; the scheduler creates ordinary
	// jobs (and ordinary maintenance) when occurrences are due.
	eng.SetLocal(verify.New(verify.Deps{Store: st, Monitor: mon, Log: logs.Job,
		AgentConnected: func(ctx context.Context, h db.Host) bool { return router.Decide(ctx, h).UseAgent },
		Probe: func(ctx context.Context, hostID string) verify.Reachability {
			out, err := inv.Probe(ctx, hostID)
			if err != nil {
				return verify.Reachability{Detail: err.Error()}
			}
			return verify.Reachability{OK: out.Status == inventory.StatusReady || out.Status == inventory.StatusUnsupported, Detail: out.Detail}
		},
		PollSSH: poll.Monitor}))
	var desiredPaused func(jobID, kind, reason string)
	eng.OnPause(func(jobID, kind, reason string) {
		if kind == engine.PauseBreaker {
			return // shown on the job page; not a rollout decision
		}
		if desiredPaused != nil {
			desiredPaused(jobID, kind, reason)
		}
		_ = alertSvc.RecordEvent(context.Background(), alerts.Event{Kind: "job.paused", Severity: "warning", ObjectType: "job", ObjectID: jobID,
			Title: "Rollout paused: " + reason})
	})
	schedTuning := sched.Tuning{}
	if os.Getenv("DROOL_TEST_SCHED_TIMING") == "fast" {
		log.Warn("TEST MODE: scheduler checks every second and treats runs more than 20 s late as missed; never use this in production")
		schedTuning.Tick = time.Second
		schedTuning.MisfireGrace = 20 * time.Second
	}
	scheduler := sched.New(sched.Deps{Store: st, Engine: eng, Alerts: alertSvc, Audit: auditW, Log: logs.App.With("component", "scheduler"), Tuning: schedTuning})
	// Desired state (M14): observation and comparison, and corrections as
	// ordinary jobs through the engine.
	desiredTuning := desired.Tuning{}
	if os.Getenv("DROOL_TEST_DESIRED_TIMING") == "fast" {
		log.Warn("TEST MODE: desired-state checks every 20 s, automatic restore every 2 s, 3 s after maintenance; never use this in production")
		desiredTuning = desired.Tuning{Tick: 500 * time.Millisecond, CheckEvery: 20 * time.Second, AutoEvery: 2 * time.Second,
			MaintenanceGrace: 3 * time.Second, ProofWait: 30 * time.Second}
	}
	desiredSvc := desired.New(desired.Deps{Store: st, Engine: eng, Monitor: mon, Alerts: alertSvc, Prober: router, Artifacts: arts,
		Audit: auditW, Log: logs.App.With("component", "desired"), Tuning: desiredTuning, PollSSH: poll.Monitor,
		Path: func(ctx context.Context, h db.Host) desired.PathInfo {
			d := router.Decide(ctx, h)
			return desired.PathInfo{AgentConnected: d.UseAgent, AgentState: d.AgentState, Blocked: d.Blocked, Reason: d.Reason}
		},
		Removals: func(ctx context.Context, hostID, pkg string) ([]string, error) {
			res, err := router.Simulate(ctx, hostID, model.Operation{Type: model.OpPackage, Package: &model.PackageOp{Action: "remove", Names: []string{pkg}}})
			if err != nil {
				return nil, err
			}
			var also []string
			for _, c := range res.ExtraRemovals {
				also = append(also, c.Name)
			}
			return also, nil
		}})
	mon.SetServicesHook(desiredSvc.OnServices)
	eng.OnJobDone(desiredSvc.OnJobDone)
	desiredPaused = desiredSvc.OnJobPaused
	if agents != nil {
		agents.SetMonitorHooks(sessionTee{mon, desiredSvc})
	}
	go desiredSvc.Run(ctx)
	go scheduler.Run(ctx)
	go artifactGC(ctx, arts, log)

	srv, err := api.New(api.Deps{
		Config: cfg, Store: st, Auth: authSvc, Audit: auditW, Logs: logs,
		UI: web.Dist(), Registry: reg, TLSMode: tlsRes.Mode,
		Secrets: secretSvc, Inventory: inv, Pool: pool, Engine: eng,
		Poller: poll, StaleAfter: pollCfg.StaleAfter(), Simulator: router, Files: router, Artifacts: arts,
		Agents: agents, Router: router, WebPin: webPin(tlsRes), Firewall: fw, Network: network, Monitor: mon, Alerts: alertSvc,
		Sched: scheduler, Desired: desiredSvc, Integrity: integrity, AuditSink: auditSink,
	})
	if err != nil {
		return err
	}

	addr := net.JoinHostPort(cfg.Server.Bind, strconv.Itoa(cfg.Server.WebPort))
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	httpSrv := &http.Server{
		Handler:           srv.Handler(),
		TLSConfig:         tlsRes.Config,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		IdleTimeout:       120 * time.Second,
		// No WriteTimeout: SSE job streams (M2) are long-lived.
	}

	scheme := "https"
	if tlsRes.Config == nil {
		scheme = "http"
	}
	log.Info("drool starting",
		"version", version.Version, "commit", version.Commit,
		"listen", scheme+"://"+addr, "tls_mode", tlsRes.Mode,
		"config_file", cfg.FilePath, "data_dir", cfg.DataDir)
	if tlsRes.Fingerprint != "" {
		log.Info("TLS certificate", "file", tlsRes.CertFile, "sha256", tlsRes.Fingerprint, "generated", tlsRes.Generated)
	}
	if cfg.IsInsecurePlaintext() {
		logs.Security.Warn("serving plaintext HTTP on a non-loopback address; credentials and session cookies cross the network unencrypted",
			"bind", cfg.Server.Bind)
	}
	_ = auditW.Record(ctx, audit.Event{Action: "system.start", Actor: &audit.System,
		Details: map[string]any{"version": version.Version, "tls_mode": tlsRes.Mode}})

	go srv.RunSessionCleanup(ctx, 10*time.Minute)

	errCh := make(chan error, 1)
	go func() {
		if tlsRes.Config != nil {
			errCh <- httpSrv.ServeTLS(ln, "", "")
		} else {
			errCh <- httpSrv.Serve(ln)
		}
	}()
	srv.SetReady(true)
	printBanner(os.Stderr, cfg, scheme, tlsRes.Fingerprint, setup)

	select {
	case err := <-errCh:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
		log.Info("shutting down")
	}
	srv.SetReady(false)
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := httpSrv.Shutdown(shutdownCtx); err != nil {
		log.Error("shutdown", "err", err)
	}
	_ = auditW.Record(shutdownCtx, audit.Event{Action: "system.stop", Actor: &audit.System})
	return nil
}

// installationName names the Agent Root CA after this machine.
func installationName() string {
	h, _ := os.Hostname()
	if h == "" {
		h = "drool"
	}
	return h
}

func advertisedHosts(adv []string) []string {
	var out []string
	for _, a := range adv {
		if h, _, err := net.SplitHostPort(a); err == nil {
			a = h
		}
		out = append(out, a)
	}
	return out
}

// webPin is curl's --pinnedpubkey value for the web TLS certificate, so
// the install one-liner authenticates the download without trusting a CA.
func webPin(r *tlsutil.Result) func() string {
	return func() string {
		if r == nil || r.Config == nil || len(r.Config.Certificates) == 0 {
			return ""
		}
		c := r.Config.Certificates[0]
		leaf := c.Leaf
		if leaf == nil {
			var err error
			if leaf, err = x509.ParseCertificate(c.Certificate[0]); err != nil {
				return ""
			}
		}
		sum := sha256.Sum256(leaf.RawSubjectPublicKeyInfo)
		return "sha256//" + base64.StdEncoding.EncodeToString(sum[:])
	}
}

// loadMasterKey loads (or on first start creates) the master key, and refuses
// to start when it doesn't fit the database: silently creating a new key
// next to existing credentials would make them all undecryptable.
func loadMasterKey(ctx context.Context, cfg *config.Loaded, st *store.Store) (*secrets.Box, bool, bool, error) {
	n, err := secrets.CountSealed(ctx, st.Reader)
	if err != nil {
		return nil, false, false, err
	}
	recovery := fmt.Sprintf(`Restore the master key file from your backup. If it is permanently lost, keep
everything except the stored credentials with:
    drool backup -o /tmp/drool-recovery.tar.gz
    drool restore --discard-credentials /tmp/drool-recovery.tar.gz
(data directory %s)`, cfg.DataDir)
	if _, err := os.Stat(cfg.MasterKeyPath()); errors.Is(err, os.ErrNotExist) && n > 0 {
		return nil, false, false, fmt.Errorf("the master key file %s is missing, but the database holds %d value(s) encrypted with it (credentials, CA keys, secrets).\n%s",
			cfg.MasterKeyPath(), n, recovery)
	}
	box, created, insecure, err := secrets.LoadOrCreateBox(cfg.MasterKeyPath())
	if err != nil {
		return nil, false, false, err
	}
	if _, err := secrets.VerifyBox(ctx, st.Reader, box); err != nil {
		return nil, false, false, fmt.Errorf("master key %s (key id %s) does not match the database: %v.\n%s",
			cfg.MasterKeyPath(), box.KeyID(), err, recovery)
	}
	return box, created, insecure, nil
}

// artifactGC removes stored files nothing refers to any more (not part of a
// deployment's history, not named by a job still kept) after a day unused.
func artifactGC(ctx context.Context, arts *artifacts.Store, log *slog.Logger) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		if n, err := arts.GC(ctx, 24*time.Hour); err != nil {
			log.Error("artifact garbage collection", "err", err)
		} else if n > 0 {
			log.Info("removed unreferenced artifacts", "count", n)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// sessionTee forwards agent sessions and telemetry to monitoring, and
// tells desired state when an agent (re)connects.
type sessionTee struct {
	mon     *monsvc.Service
	desired *desired.Service
}

func (t sessionTee) OnSession(hostID string, features []string, effectiveJSON []byte) {
	t.mon.OnSession(hostID, features, effectiveJSON)
	t.desired.OnHostConnected(hostID)
}

func (t sessionTee) OnTelemetry(hostID, kind string, payload []byte) {
	t.mon.OnTelemetry(hostID, kind, payload)
}
