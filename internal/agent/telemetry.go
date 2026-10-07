package agent

import (
	"context"
	"net"
	"sort"
	"strings"
	"time"

	"drool/internal/agentrpc/pb"
	"drool/internal/discovery"
	"drool/internal/hostexec"
	"drool/internal/ops/pkg"
)

// Telemetry intervals. Status follows Options.StatusEvery; facts are also
// sent right away when the host's addresses change (DESIGN §5.2).
const (
	factsEvery   = 6 * time.Hour
	updatesEvery = 6 * time.Hour
	refreshEvery = 24 * time.Hour
	addrPoll     = 10 * time.Second
)

// collect runs one of the collection scripts the SSH path uses, locally.
// The controller parses the raw output with the same code, so both paths
// produce identical inventory.
func (a *Agent) collect(ctx context.Context, kind string) (*pb.Telemetry, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	t := &pb.Telemetry{Kind: kind, CollectedAtUnix: a.now().Unix()}
	var script string
	switch kind {
	case "facts":
		script = discovery.DiscoverScript
	case "status":
		script = discovery.StatusScript
	case "updates":
		r, err := a.run.Run(ctx, pkg.DetectCommand, hostexec.RunOptions{})
		if err != nil {
			return nil, err
		}
		switch strings.TrimSpace(string(r.Stdout)) {
		case "apt-get":
			script, t.PackageManager = discovery.UpdatesAPTScript, "apt"
		case "dnf", "yum":
			script, t.PackageManager = discovery.UpdatesDNFScript, "dnf"
		default:
			return nil, nil // no supported package manager: nothing to report
		}
	}
	r, err := a.run.Run(ctx, "sh -s", hostexec.RunOptions{Stdin: []byte(script), MaxOutput: 4 << 20})
	if err != nil {
		return nil, err
	}
	t.Output = r.Stdout
	return t, nil
}

func (a *Agent) sendTelemetry(ctx context.Context, s *session, kind string) {
	if !a.Effective().Monitoring {
		return // monitoring not allowed by the ceiling: heartbeats only
	}
	t, err := a.collect(ctx, kind)
	if err != nil || t == nil {
		if err != nil {
			a.log.Warn("telemetry collection failed", "kind", kind, "err", err)
		}
		return
	}
	s.send(&pb.AgentMessage{Msg: &pb.AgentMessage_Telemetry{Telemetry: t}})
}

// refreshMetadata keeps update counts meaningful (apt-get update / dnf
// makecache), only when the packages capability allows it.
func (a *Agent) refreshMetadata(ctx context.Context) {
	if !a.Effective().Packages.Enabled {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	r, err := a.run.Run(ctx, pkg.DetectCommand, hostexec.RunOptions{})
	if err != nil {
		return
	}
	cmd := discovery.RefreshAPT
	if m := strings.TrimSpace(string(r.Stdout)); m == "dnf" || m == "yum" {
		cmd = discovery.RefreshDNF
	} else if m != "apt-get" {
		return
	}
	a.run.Run(ctx, cmd, hostexec.RunOptions{})
}

func addrSet() string {
	addrs, _ := net.InterfaceAddrs()
	var s []string
	for _, a := range addrs {
		s = append(s, a.String())
	}
	sort.Strings(s)
	return strings.Join(s, ",")
}

// telemetryLoop reports facts, status and updates while the session lasts.
func (a *Agent) telemetryLoop(ctx context.Context, s *session) {
	a.sendTelemetry(ctx, s, "facts")
	a.sendTelemetry(ctx, s, "status")
	a.sendTelemetry(ctx, s, "updates")
	status := time.NewTicker(a.opt.StatusEvery)
	defer status.Stop()
	slow := time.NewTicker(time.Minute)
	defer slow.Stop()
	addr := time.NewTicker(addrPoll)
	defer addr.Stop()
	lastFacts, lastUpdates, lastRefresh := a.now(), a.now(), time.Time{}
	lastAddrs := addrSet()
	for {
		select {
		case <-ctx.Done():
			return
		case <-status.C:
			a.sendTelemetry(ctx, s, "status")
			a.sendFirewallStatus(ctx, s)
		case <-addr.C:
			if cur := addrSet(); cur != lastAddrs {
				lastAddrs = cur
				a.log.Info("addresses changed; reporting")
				a.sendTelemetry(ctx, s, "facts")
				lastFacts = a.now()
			}
		case <-slow.C:
			now := a.now()
			if now.Sub(lastRefresh) > refreshEvery {
				a.refreshMetadata(ctx)
				lastRefresh = now
			}
			if now.Sub(lastFacts) > factsEvery {
				a.sendTelemetry(ctx, s, "facts")
				lastFacts = now
			}
			if now.Sub(lastUpdates) > updatesEvery {
				a.sendTelemetry(ctx, s, "updates")
				lastUpdates = now
			}
		}
	}
}
