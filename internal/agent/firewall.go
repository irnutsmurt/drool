package agent

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/netip"
	"os"
	"strconv"
	"time"

	"drool/internal/agentrpc/pb"
	"drool/internal/driver"
	"drool/internal/firewall/hostfw"
	"drool/internal/hostops"
	"drool/internal/model"
	"drool/internal/netmon/collect"
)

// Features are optional capabilities of this agent build, announced in the
// hello. The controller only sends firewall operations to agents that
// announce "firewall-1"; "netmon-1" agents report "netflows" (M10);
// "monitor-1" agents run system monitoring (M11).
var Features = []string{"firewall-1", "netmon-1", "monitor-1"}

// errReconnect ends a session on purpose: after applying a firewall
// candidate the agent proves the management path with a completely new
// connection (DESIGN §17.1). An old established connection proves nothing.
var errReconnect = errors.New("reconnecting to verify the management path")

func (a *Agent) fwPaths() hostfw.Paths {
	if a.opt.FirewallPaths != nil {
		return *a.opt.FirewallPaths
	}
	return hostfw.DefaultPaths
}

// FirewallReport is Hello.firewall_json and the "firewall" telemetry.
type FirewallReport struct {
	Status    map[string]string `json:"status"`
	Endpoints []EndpointAddrs   `json:"endpoints"`
}

// EndpointAddrs is a controller endpoint as this host resolves it: what the
// agent's management path needs on a host that restricts outbound traffic.
type EndpointAddrs struct {
	Endpoint string   `json:"endpoint"`
	Port     int      `json:"port"`
	Addrs    []string `json:"addrs"`
}

func (a *Agent) firewallReport(ctx context.Context) []byte {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	rep := FirewallReport{Status: map[string]string{}}
	// Firewall state is recovery state, not monitoring: it runs outside the
	// agent's resource budget like any operation. (Inside the CPU quota a
	// slow host's status check outlived its timeout, the hello carried no
	// pending transaction, and a good candidate was rolled back.)
	if res, err := hostfw.Run(ctx, a.run, hostops.Become{Method: "none"}, a.fwPaths(), hostfw.Action{Kind: hostfw.Status}); err == nil {
		rep.Status = res.Values
	}
	for _, ep := range a.endpoints() {
		host, port, err := net.SplitHostPort(ep)
		if err != nil {
			continue
		}
		p, _ := strconv.Atoi(port)
		e := EndpointAddrs{Endpoint: ep, Port: p}
		if ip := net.ParseIP(host); ip != nil {
			e.Addrs = []string{ip.String()}
		} else {
			rctx, rcancel := context.WithTimeout(ctx, 3*time.Second)
			e.Addrs, _ = net.DefaultResolver.LookupHost(rctx, host)
			rcancel()
		}
		rep.Endpoints = append(rep.Endpoints, e)
	}
	b, _ := json.Marshal(rep)
	return b
}

// runFirewall performs one action of the host tooling. Tool-level refusals
// (stale, duplicate, pending, drift, ...) are results, not transport
// errors: the controller interprets them.
func (a *Agent) runFirewall(ctx context.Context, op *model.FirewallOp) (*driver.OpResult, error) {
	res, err := hostfw.Run(ctx, a.run, hostops.Become{Method: "none"}, a.fwPaths(), *op)
	if err != nil {
		return nil, err
	}
	stderr := res.Stderr
	if len(stderr) > 4096 {
		stderr = stderr[len(stderr)-4096:]
	}
	exit := res.Exit
	return &driver.OpResult{ExitCode: &exit, Data: map[string]any{
		"values": res.Values, "multi": res.Multi, "sections": res.Sections,
		"err_code": res.ErrCode, "err_msg": res.ErrMsg, "exit": res.Exit, "stderr": stderr,
	}}, nil
}

// afterFirewallOp runs once the result frame was sent. A pending apply ends
// this session so the controller sees a brand-new connection.
func (a *Agent) afterFirewallOp(op *model.FirewallOp, res *driver.OpResult) {
	if op.Kind != hostfw.Apply || res == nil {
		return
	}
	vals, _ := res.Data["values"].(map[string]string)
	if vals["state"] != "pending" {
		return
	}
	a.log.Info("firewall candidate applied; reconnecting over a new connection to prove the management path", "tx", op.TxID)
	a.oplog.Write(OpEvent{Event: "firewall", Summary: "candidate " + op.TxID + " applied; reconnecting to verify"})
	// End the session the candidate was applied on (not a newer one: the
	// controller may already have asked us to reconnect, and the commit
	// arrives over the new session).
	a.mu.Lock()
	cancel := a.sessionCancel
	a.mu.Unlock()
	go func() {
		time.Sleep(time.Second) // let the result frame leave
		if cancel != nil {
			cancel(errReconnect)
		}
	}()
}

// --- continuous connection collection (DESIGN §19.1, M10): agent hosts only ---

const defaultSampleEvery = 30 * time.Second

func (a *Agent) setFirewallConfig(fc *pb.FirewallConfig) {
	every := time.Duration(fc.SampleSeconds) * time.Second
	if every < 5*time.Second {
		every = defaultSampleEvery
	}
	a.mu.Lock()
	a.learning, a.sampleEvery = fc.Learning, every
	a.mu.Unlock()
}

// Learning reports whether the controller has turned continuous collection on.
func (a *Agent) Learning() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.learning
}

// controlConn is the current controller connection's two ends: the
// collector proves conntrack is tracking by asking about it.
func (a *Agent) controlConn() (netip.AddrPort, netip.AddrPort, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.session == nil || !a.session.local.IsValid() || !a.session.remote.IsValid() {
		return netip.AddrPort{}, netip.AddrPort{}, false
	}
	return a.session.local, a.session.remote, true
}

// collector starts or stops the connection collector. It runs for the
// agent's lifetime (not one session), so a reconnect loses nothing.
func (a *Agent) collector(on bool, every time.Duration) *collect.Collector {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !on {
		if a.collectStop != nil {
			a.collectStop()
			a.collectStop, a.coll = nil, nil
		}
		return nil
	}
	if a.coll == nil {
		parent := a.ctx // a.mu is held: not rootCtx
		if parent == nil {
			parent = context.Background()
		}
		ctx, cancel := context.WithCancel(parent)
		opt := collect.Options{Every: every, DisableConntrack: os.Getenv("DROOL_AGENT_NO_CONNTRACK") == "1"}
		if a.opt.NetCollect != nil {
			opt = *a.opt.NetCollect
		}
		opt.Control, opt.Log = a.controlConn, a.log
		a.coll = collect.New(opt)
		a.collectStop = cancel
		go a.coll.Run(ctx)
	}
	return a.coll
}

func (a *Agent) learnLoop(ctx context.Context, s *session) {
	period := 5 * time.Second
	if a.opt.NetCollect != nil && a.opt.NetCollect.Every > 0 && a.opt.NetCollect.Every < period {
		period = a.opt.NetCollect.Every
	}
	tick := time.NewTicker(period)
	defer tick.Stop()
	var lastSend, lastStatus time.Time
	lastSrc := ""
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		a.mu.Lock()
		on, every := a.learning, a.sampleEvery
		a.mu.Unlock()
		if a.opt.NetCollect != nil && a.opt.NetCollect.Every > 0 {
			every = a.opt.NetCollect.Every
		}
		col := a.collector(on && a.Effective().Monitoring, every)
		if col == nil {
			continue
		}
		now := a.now()
		if now.Sub(lastSend) < 2*every {
			continue
		}
		lastSend = now
		r := col.Report()
		// Empty reports still go out now and then: the controller shows
		// how this host is observed (conntrack or socket sampling).
		if len(r.Flows) == 0 && r.Source == lastSrc && now.Sub(lastStatus) < 5*time.Minute {
			continue
		}
		lastSrc, lastStatus = r.Source, now
		b, _ := json.Marshal(r)
		s.send(&pb.AgentMessage{Msg: &pb.AgentMessage_Telemetry{Telemetry: &pb.Telemetry{Kind: "netflows", Output: b, CollectedAtUnix: now.Unix()}}})
	}
}

// sendFirewallStatus reports the tooling's status (drift, pending
// transactions) with the regular status telemetry, when installed.
func (a *Agent) sendFirewallStatus(ctx context.Context, s *session) {
	if !a.Effective().Monitoring {
		return
	}
	b := a.firewallReport(ctx)
	var rep FirewallReport
	json.Unmarshal(b, &rep)
	if rep.Status["tool_version"] == "" || rep.Status["tool_version"] == "none" {
		return
	}
	s.send(&pb.AgentMessage{Msg: &pb.AgentMessage_Telemetry{Telemetry: &pb.Telemetry{Kind: "firewall", Output: b, CollectedAtUnix: a.now().Unix()}}})
}
