package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"fleetmgr/internal/audit"
	"fleetmgr/internal/auth"
	"fleetmgr/internal/config"
	"fleetmgr/internal/logging"
)

// setupState is what the startup banner needs to show.
type setupState struct {
	required bool
	code     string    // generated code to display once; "" if none
	expires  time.Time // for generated codes
	provided bool      // an operator-provided secret is registered
}

// prepareSetup runs at startup. If no administrator exists it registers the
// provided setup secret or issues a fresh one-time code; otherwise it removes
// any leftover codes.
func prepareSetup(ctx context.Context, cfg *config.Loaded, svc *auth.Service, logs *logging.Loggers) (setupState, error) {
	required, err := svc.SetupRequired(ctx)
	if err != nil {
		return setupState{}, err
	}
	if !required {
		return setupState{}, svc.ClearSetupCodes(ctx)
	}
	st := setupState{required: true}
	secret, err := cfg.ProvidedSetupSecret()
	if err != nil {
		return st, err
	}
	if secret != "" {
		if err := svc.RegisterProvidedSetupSecret(ctx, secret); err != nil {
			return st, err
		}
		st.provided = true
		logs.App.Info("setup required; operator-provided setup secret registered")
		return st, nil
	}
	st.code, st.expires, err = svc.IssueSetupCode(ctx, cfg.Setup.CodeTTL)
	if err != nil {
		return st, err
	}
	// The code itself is only ever printed by printBanner, never logged.
	logs.App.Info("setup required; one-time setup code issued and shown on the console",
		"expires_at", st.expires.UTC().Format(time.RFC3339))
	return st, nil
}

// printBanner writes the human-oriented startup message to w (stderr, which
// also ends up in `docker compose logs`).
func printBanner(w io.Writer, cfg *config.Loaded, scheme, fingerprint string, st setupState) {
	urls := displayURLs(cfg, scheme)
	line := strings.Repeat("=", 64)
	fmt.Fprintln(w, line)
	fmt.Fprintln(w, " Fleet Manager is ready.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, " Open:")
	path := ""
	if st.required {
		path = "/setup"
	}
	for _, u := range urls {
		fmt.Fprintf(w, "   %s%s\n", u, path)
	}
	if st.required {
		fmt.Fprintln(w)
		switch {
		case st.code != "":
			fmt.Fprintln(w, " Setup code:")
			fmt.Fprintf(w, "   %s\n", st.code)
			fmt.Fprintln(w)
			fmt.Fprintf(w, " Single use. Expires %s (in %s).\n",
				st.expires.Local().Format("15:04 MST"), roundDuration(time.Until(st.expires)))
			fmt.Fprintln(w, " New code: restart, or run `fleetmgr setup code`.")
		case st.provided:
			fmt.Fprintln(w, " Setup code: the setup secret you provided")
			fmt.Fprintln(w, " (FLEETMGR_SETUP_SECRET or FLEETMGR_SETUP_SECRET_FILE).")
		}
	}
	if scheme == "https" && cfg.Server.TLS.Mode == "self_signed" {
		fmt.Fprintln(w)
		fmt.Fprintln(w, " Your browser will warn about the self-signed certificate; that is")
		fmt.Fprintln(w, " expected. You can install your own certificate later in the config.")
		fmt.Fprintf(w, " Certificate SHA-256: %s\n", fingerprint)
	}
	fmt.Fprintln(w, line)
}

// displayURLs lists addresses a user can reach the UI at.
func displayURLs(cfg *config.Loaded, scheme string) []string {
	port := strconv.Itoa(cfg.Server.WebPort)
	mk := func(host string) string { return scheme + "://" + net.JoinHostPort(host, port) }
	bind := net.ParseIP(cfg.Server.Bind)
	if bind == nil || !bind.IsUnspecified() {
		return []string{mk(cfg.Server.Bind)}
	}
	if inContainer() {
		// Container addresses aren't reachable from the user's browser.
		return []string{scheme + "://<this-host's-address>:" + port}
	}
	var out []string
	if addrs, err := net.InterfaceAddrs(); err == nil {
		for _, a := range addrs {
			n, ok := a.(*net.IPNet)
			if !ok || n.IP.IsLoopback() || n.IP.IsLinkLocalUnicast() || n.IP.To4() == nil {
				continue
			}
			out = append(out, mk(n.IP.String()))
		}
	}
	if len(out) == 0 {
		out = append(out, mk("localhost"))
	}
	return out
}

func inContainer() bool {
	for _, p := range []string{"/.dockerenv", "/run/.containerenv"} {
		if _, err := os.Stat(p); err == nil {
			return true
		}
	}
	return false
}

func roundDuration(d time.Duration) string {
	if d < time.Minute {
		return "under a minute"
	}
	return fmt.Sprintf("%d min", int(d.Round(time.Minute).Minutes()))
}

// cmdSetupCode issues a new one-time setup code (e.g. after the startup code
// expired). It works while the server is running.
func cmdSetupCode(path string, explicit bool, args []string) error {
	fs := flag.NewFlagSet("setup code", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
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
	svc := newAuthService(cfg, st)
	code, expires, err := svc.IssueSetupCode(ctx, cfg.Setup.CodeTTL)
	if errors.Is(err, auth.ErrSetupClosed) {
		return errors.New("setup is already complete (an administrator exists); use `fleetmgr user create` to add users")
	}
	if err != nil {
		return err
	}
	// CLI commands persist audit records to the database only; mirroring them to
	// the log stream would print JSON on stdout in container deployments.
	logs := logging.Discard()
	actor := audit.Actor{Type: audit.ActorSystem, Name: "cli:" + currentOSUser()}
	_ = audit.NewWriter(st, logs.Audit).Record(ctx, audit.Event{
		Action: "setup.code_issue", Actor: &actor,
		Details: map[string]any{"expires_at": expires.UTC().Format(time.RFC3339), "via": "cli"},
	})
	fmt.Printf("Setup code: %s\nSingle use. Expires %s. Any previously generated code is no longer valid.\n",
		code, expires.Local().Format("15:04 MST"))
	return nil
}
