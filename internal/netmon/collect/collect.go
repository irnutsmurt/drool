// Package collect is the agent's continuous connection collector (M10).
//
// It prefers conntrack: the kernel announces every new connection it
// tracks, so short-lived connections are seen too. Connection tracking is
// only usable when it is actually tracking this host's connections — in
// practice when a firewall (DROOL's or another) uses it — and the agent has
// CAP_NET_ADMIN. The collector proves that by asking the kernel for the
// agent's own controller connection. Otherwise it samples sockets (ss)
// every sample period, which misses connections that open and close in
// between, and says so.
//
// Socket sampling also runs in conntrack mode: it advances "last seen" for
// long-lived connections and attributes connections to processes. Process
// names come from socket ownership (ss -p): reliable for listening
// services (inbound) and for outbound connections that live long enough
// to be scanned; otherwise unknown. Nothing here reads packet contents or
// changes the host: no modules, sysctls, rules or packages.
package collect

import (
	"context"
	"log/slog"
	"net"
	"net/netip"
	"os/exec"
	"strings"
	"sync"
	"time"

	"drool/internal/firewall/observe"
)

// MaxPending bounds the flows held between reports (a long disconnection
// mustn't grow the agent's memory without limit).
const MaxPending = 20000

// Options configure a collector.
type Options struct {
	// Every is the socket sampling period (default 30s).
	Every time.Duration
	// Control returns the agent's controller connection (local and remote
	// ends), used to prove that conntrack tracks this host's connections.
	Control func() (local, remote netip.AddrPort, ok bool)
	// SS runs `ss -Hntuap` (default: the ss binary).
	SS func(ctx context.Context) (string, error)
	// LocalAddrs lists the host's addresses (default: the interfaces').
	LocalAddrs func() []netip.Addr
	// Conntrack is the kernel interface (default: netlink; tests fake it).
	Conntrack Conntrack
	// DisableConntrack forces socket sampling.
	DisableConntrack bool
	Log              *slog.Logger
	Now              func() time.Time
}

// Conntrack is the connection tracking interface the collector uses.
type Conntrack interface {
	// Usable explains why conntrack can't be used at all, or returns "".
	Usable() string
	// Open subscribes to new-connection events.
	Open() (Events, error)
	// Tracked reports whether the kernel tracks one connection.
	Tracked(proto uint8, src, dst netip.AddrPort) (bool, error)
}

// Events is an open event subscription.
type Events interface {
	// Read delivers events until Close; it returns when closed or failed.
	Read(fn func(proto uint8, src, dst netip.AddrPort)) error
	Close() error
	Lost() int64
}

// Collector collects flows.
type Collector struct {
	opt Options

	mu        sync.Mutex
	agg       *observe.Aggregator
	source    string
	detail    string
	listeners observe.ListenIndex
	local     map[netip.Addr]bool
	forwarded int64
	lostBase  int64
	events    Events
	needAttr  bool
}

// New prepares a collector; Run starts it.
func New(opt Options) *Collector {
	if opt.Every <= 0 {
		opt.Every = 30 * time.Second
	}
	if opt.SS == nil {
		opt.SS = runSS
	}
	if opt.LocalAddrs == nil {
		opt.LocalAddrs = interfaceAddrs
	}
	if opt.Conntrack == nil {
		opt.Conntrack = kernelConntrack{}
	}
	if opt.Log == nil {
		opt.Log = slog.Default()
	}
	if opt.Now == nil {
		opt.Now = time.Now
	}
	agg := observe.NewAggregator()
	agg.Max = MaxPending
	return &Collector{opt: opt, agg: agg, source: observe.SourceSockets, listeners: observe.ListenIndex{}, local: map[netip.Addr]bool{}}
}

func runSS(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	path, err := exec.LookPath("ss")
	if err != nil {
		for _, p := range []string{"/usr/sbin/ss", "/sbin/ss", "/usr/bin/ss", "/bin/ss"} {
			if _, err2 := exec.LookPath(p); err2 == nil {
				path, err = p, nil
				break
			}
		}
		if err != nil {
			return "", err
		}
	}
	out, err := exec.CommandContext(ctx, path, "-Hntuap").Output()
	return string(out), err
}

func interfaceAddrs() []netip.Addr {
	as, err := net.InterfaceAddrs()
	if err != nil {
		return nil
	}
	var out []netip.Addr
	for _, a := range as {
		if p, err := netip.ParsePrefix(a.String()); err == nil {
			out = append(out, p.Addr().Unmap())
		}
	}
	return out
}

// Status returns the source and its explanation.
func (c *Collector) Status() (string, string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.source, c.detail
}

// Run collects until ctx ends.
func (c *Collector) Run(ctx context.Context) {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	defer c.stopEvents()
	var lastSample, lastAttr, lastCheck time.Time
	for {
		now := c.opt.Now()
		if now.Sub(lastSample) >= c.opt.Every {
			lastSample = now
			c.sample(ctx, now)
			// Decide (again) whether conntrack can be used: right after the
			// first sample, then every few minutes (every minute while
			// sampling sockets: a firewall applied later activates it).
			checkEvery := 5 * time.Minute
			if src, _ := c.Status(); src != observe.SourceConntrack {
				checkEvery = time.Minute
			}
			if lastCheck.IsZero() || now.Sub(lastCheck) >= checkEvery {
				lastCheck = now
				c.checkConntrack(ctx)
			}
		} else if c.wantsAttribution() && now.Sub(lastAttr) >= 2*time.Second {
			lastAttr = now
			c.attribute(ctx)
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

func (c *Collector) wantsAttribution() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.needAttr && c.source == observe.SourceConntrack
}

// sample takes one socket sample.
func (c *Collector) sample(ctx context.Context, now time.Time) {
	out, err := c.opt.SS(ctx)
	if err != nil {
		c.opt.Log.Debug("socket sample failed", "err", err)
		return
	}
	socks := observe.ParseSockets(out)
	conns := observe.Conns(socks)
	local := map[netip.Addr]bool{}
	for _, a := range c.opt.LocalAddrs() {
		local[a] = true
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.listeners = observe.NewListenIndex(observe.Listeners(socks))
	c.local = local
	if c.source == observe.SourceConntrack {
		c.agg.Touch(conns, now.Unix())
	} else {
		c.agg.Add(conns, now.Unix())
	}
	c.needAttr = false
}

// attribute scans sockets to name the processes behind flows conntrack
// announced since the last sample.
func (c *Collector) attribute(ctx context.Context) {
	out, err := c.opt.SS(ctx)
	if err != nil {
		return
	}
	socks := observe.ParseSockets(out)
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, cn := range observe.Conns(socks) {
		c.agg.Attribute(cn)
	}
	for _, s := range socks {
		if s.Proto == "udp" && s.State == "UNCONN" && s.Local.Port() != 0 {
			c.agg.AttributeLocal("udp", s.Local, s.Process, s.PID)
		}
	}
	c.needAttr = false
}

func (c *Collector) stopEvents() {
	c.mu.Lock()
	ev := c.events
	c.events = nil
	c.mu.Unlock()
	if ev != nil {
		ev.Close()
	}
}

func (c *Collector) setSource(src, detail string) {
	c.mu.Lock()
	changed := c.source != src || c.detail != detail
	c.source, c.detail = src, detail
	c.mu.Unlock()
	if changed {
		c.opt.Log.Info("connection collection", "source", src, "detail", detail)
	}
}

// checkConntrack switches between conntrack and socket sampling.
func (c *Collector) checkConntrack(ctx context.Context) {
	if c.opt.DisableConntrack {
		c.setSource(observe.SourceSockets, "conntrack disabled in the agent configuration")
		return
	}
	ct := c.opt.Conntrack
	if why := ct.Usable(); why != "" {
		c.stopEvents()
		c.setSource(observe.SourceSockets, why)
		return
	}
	local, remote, ok := netip.AddrPort{}, netip.AddrPort{}, false
	if c.opt.Control != nil {
		local, remote, ok = c.opt.Control()
	}
	if !ok {
		return // not connected: keep the current source
	}
	c.mu.Lock()
	running := c.events != nil
	c.mu.Unlock()
	if !running {
		// Subscribe before checking, so nothing between the check and the
		// subscription is missed.
		ev, err := ct.Open()
		if err != nil {
			c.setSource(observe.SourceSockets, err.Error())
			return
		}
		c.mu.Lock()
		c.events = ev
		c.lostBase = 0
		c.mu.Unlock()
		go c.readEvents(ev)
	}
	yes, err := ct.Tracked(6, local, remote)
	if err != nil || !yes {
		c.stopEvents()
		why := "connection tracking is not tracking this host's connections (it is only active while a firewall uses it)"
		if err != nil {
			why = "conntrack query failed: " + err.Error()
		}
		c.setSource(observe.SourceSockets, why)
		return
	}
	c.setSource(observe.SourceConntrack, "")
}

func (c *Collector) readEvents(ev Events) {
	err := ev.Read(func(proto uint8, src, dst netip.AddrPort) {
		c.event(proto, src, dst)
	})
	c.mu.Lock()
	if c.events == ev {
		c.events = nil
		if c.source == observe.SourceConntrack {
			c.source = observe.SourceSockets
			c.detail = "conntrack events stopped"
			if err != nil {
				c.detail += ": " + err.Error()
			}
		}
	}
	c.mu.Unlock()
}

// event turns a new conntrack entry into a connection of this host.
func (c *Collector) event(proto uint8, src, dst netip.AddrPort) {
	p := map[uint8]string{6: "tcp", 17: "udp"}[proto]
	if p == "" || src.Addr().IsLoopback() || dst.Addr().IsLoopback() {
		return
	}
	now := c.opt.Now().Unix()
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.source != observe.SourceConntrack {
		return // socket sampling counts
	}
	cn := observe.Conn{Proto: p}
	switch {
	case c.local[src.Addr()] && c.local[dst.Addr()]:
		return // the host talking to itself
	case c.local[src.Addr()]:
		cn.Local, cn.Remote, cn.ServerPort = src, dst, int(dst.Port())
	case c.local[dst.Addr()]:
		cn.Local, cn.Remote, cn.ServerPort, cn.Inbound = dst, src, int(dst.Port()), true
		if l, ok := c.listeners.Lookup(p, dst.Addr(), int(dst.Port())); ok {
			cn.Process, cn.PID = l.Process, l.PID
		}
	default:
		c.forwarded++ // routed through the host (containers, a router): not this host's own
		return
	}
	c.agg.Event(cn, now)
	if cn.Process == "" {
		c.needAttr = true
	}
}

// Report drains the flows collected since the last report.
func (c *Collector) Report() observe.Report {
	c.mu.Lock()
	defer c.mu.Unlock()
	r := observe.Report{Source: c.source, Detail: c.detail, Flows: c.agg.Drain(), Every: int(c.opt.Every / time.Second),
		Dropped: c.agg.Dropped, Forwarded: c.forwarded}
	if c.events != nil {
		lost := c.events.Lost()
		r.EventsLost = lost - c.lostBase
		c.lostBase = lost
	}
	if r.EventsLost > 0 {
		r.Detail = strings.TrimSpace(r.Detail + " some new-connection events were lost (kernel buffer overflow); counts may be low")
	}
	c.agg.Dropped, c.forwarded = 0, 0
	return r
}
