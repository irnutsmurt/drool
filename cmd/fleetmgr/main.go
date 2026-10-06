// Command fleetmgr is the Fleet Manager controller.
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
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

	"fleetmgr/internal/api"
	"fleetmgr/internal/audit"
	"fleetmgr/internal/auth"
	"fleetmgr/internal/config"
	"fleetmgr/internal/logging"
	"fleetmgr/internal/store"
	"fleetmgr/internal/tlsutil"
	"fleetmgr/internal/version"
	"fleetmgr/web"
)

const usage = `Usage: fleetmgr [--config FILE] <command> [options]

Commands:
  serve                 Run the controller
  migrate               Apply database migrations and exit
  user create           Create a local user (prompts for password)
  version               Print version information

Configuration is read from built-in defaults, then the config file
(default ` + config.DefaultPath + ` if present), then FLEETMGR_* environment variables.
`

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "fleetmgr:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	global := flag.NewFlagSet("fleetmgr", flag.ContinueOnError)
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
		fmt.Printf("fleetmgr %s (commit %s, built %s)\n", version.Version, version.Commit, version.Date)
		return nil
	case "serve":
		return cmdServe(*cfgPath, explicit)
	case "migrate":
		return cmdMigrate(*cfgPath, explicit)
	case "user":
		if len(rest) < 2 || rest[1] != "create" {
			return errors.New("usage: fleetmgr user create --username NAME [--role admin]")
		}
		return cmdUserCreate(*cfgPath, explicit, rest[2:])
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
	logs := logging.Discard()
	if l, err := logging.New(cfg.Logging, cfg.DataDir); err == nil {
		logs = l
		defer l.Close()
	}
	svc := auth.NewService(st, auth.Options{
		IdleTimeout: cfg.Auth.SessionIdleTimeout, AbsoluteTimeout: cfg.Auth.SessionAbsoluteTimeout,
	})
	u, err := svc.CreateUser(ctx, *username, password, *role)
	if err != nil {
		return err
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

	authSvc := auth.NewService(st, auth.Options{
		IdleTimeout: cfg.Auth.SessionIdleTimeout, AbsoluteTimeout: cfg.Auth.SessionAbsoluteTimeout,
	})
	auditW := audit.NewWriter(st, logs.Audit)

	srv, err := api.New(api.Deps{
		Config: cfg, Store: st, Auth: authSvc, Audit: auditW, Logs: logs,
		UI: web.Dist(), Registry: reg, TLSMode: tlsRes.Mode,
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
	log.Info("fleetmgr starting",
		"version", version.Version, "commit", version.Commit,
		"listen", scheme+"://"+addr, "tls_mode", tlsRes.Mode,
		"config_file", cfg.FilePath, "data_dir", cfg.DataDir)
	if tlsRes.Fingerprint != "" {
		log.Info("TLS certificate", "file", tlsRes.CertFile, "sha256", tlsRes.Fingerprint, "generated", tlsRes.Generated)
		if tlsRes.Generated {
			fmt.Fprintf(os.Stderr, "Generated self-signed certificate %s\nSHA-256 fingerprint: %s\n",
				tlsRes.CertFile, tlsRes.Fingerprint)
		}
	}
	if cfg.IsInsecurePlaintext() {
		logs.Security.Warn("serving plaintext HTTP on a non-loopback address; credentials and session cookies cross the network unencrypted",
			"bind", cfg.Server.Bind)
	}
	if n, err := st.R.CountUsers(ctx); err == nil && n == 0 {
		log.Warn("no users exist; create an administrator with: fleetmgr user create --username admin")
		fmt.Fprintln(os.Stderr, "No users exist. Create an administrator with: fleetmgr user create --username admin")
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
