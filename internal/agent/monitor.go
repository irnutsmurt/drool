package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"drool/internal/agentrpc/pb"
	"drool/internal/monitor/agentmon"
	"drool/internal/monitor/source"
)

// System monitoring (M11). The engine runs for the agent's lifetime, not
// one session: while the controller is away it keeps sampling into its
// bounded in-memory outbox; a reconnect delivers what fits and reports
// the gap. Its only state on disk is the controller's last config
// (monitor.json, written when it changes), so monitoring resumes at boot
// before the controller is reachable.

func (a *Agent) monitorConfigPath() string { return filepath.Join(a.paths.State, "monitor.json") }

// startMonitor starts the engine if the ceiling allows monitoring and a
// controller has configured it (now or before a restart).
func (a *Agent) startMonitor() {
	if !a.Effective().Monitoring {
		return
	}
	b, err := os.ReadFile(a.monitorConfigPath())
	if err != nil {
		return // never configured: wait for a controller that speaks monitor-1
	}
	var c agentmon.Config
	if json.Unmarshal(b, &c) != nil || !c.Enabled {
		return
	}
	a.ensureMonitor(c)
}

func (a *Agent) ensureMonitor(c agentmon.Config) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.mon == nil {
		opt := agentmon.Options{Source: &source.Local{Timeout: 30 * time.Second, MaxOutput: 1 << 20, Slots: 1},
			StateDir: a.paths.State, Log: a.log, Now: a.now}
		if a.opt.Monitor != nil {
			opt = *a.opt.Monitor
			if opt.Log == nil {
				opt.Log = a.log
			}
		}
		if a.opt.MonitorExtra != nil {
			opt.Extra = a.opt.MonitorExtra
		}
		a.mon = agentmon.New(opt)
		parent := a.ctx
		if parent == nil {
			parent = context.Background()
		}
		ctx, cancel := context.WithCancel(parent)
		a.monStop = cancel
		go a.mon.Run(ctx)
		a.log.Info("system monitoring started")
	}
	if err := a.mon.SetConfig(c); err != nil {
		a.log.Warn("monitoring config refused", "err", err)
	}
}

func (a *Agent) stopMonitor() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.monStop != nil {
		a.monStop()
		a.mon, a.monStop = nil, nil
		a.log.Info("system monitoring stopped")
	}
}

// Monitor returns the engine (nil while monitoring is off).
func (a *Agent) Monitor() *agentmon.Engine {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.mon
}

// setMonitorConfig handles MonitorConfig from the controller. The
// controller can tune monitoring but never widen the ceiling: without
// the monitoring capability nothing runs.
func (a *Agent) setMonitorConfig(mc *pb.MonitorConfig) {
	var c agentmon.Config
	if err := json.Unmarshal(mc.ConfigJson, &c); err != nil {
		a.log.Warn("unreadable monitoring config", "err", err)
		return
	}
	if err := c.Validate(); err != nil {
		a.log.Warn("monitoring config refused", "err", err)
		return
	}
	a.mu.Lock()
	a.monConfigured = true
	a.mu.Unlock()
	if old, err := os.ReadFile(a.monitorConfigPath()); err != nil || string(old) != string(mc.ConfigJson) {
		// Configuration state, written only when it changes.
		if err := writeFileAtomic(a.monitorConfigPath(), mc.ConfigJson, 0o600); err != nil {
			a.log.Warn("saving the monitoring config", "err", err)
		}
	}
	a.applyLimits(c.Limits)
	if !c.Enabled || !a.Effective().Monitoring {
		a.stopMonitor()
		return
	}
	a.ensureMonitor(c)
}

// storedLimits are the last budget the controller chose (nil: defaults).
func (a *Agent) storedLimits() *agentmon.Limits {
	b, err := os.ReadFile(a.monitorConfigPath())
	if err != nil {
		return nil
	}
	var c agentmon.Config
	if json.Unmarshal(b, &c) != nil {
		return nil
	}
	return c.Limits
}

// monitorSendLoop delivers the outbox while the session lasts. Sending
// is only enabled once this controller has sent a MonitorConfig (an
// older controller wouldn't understand the telemetry).
func (a *Agent) monitorSendLoop(ctx context.Context, s *session) {
	every := 15 * time.Second
	if a.opt.Monitor != nil && a.opt.Monitor.Tick > 0 && a.opt.Monitor.Tick < time.Second {
		every = 200 * time.Millisecond // tests
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		a.mu.Lock()
		mon, ok := a.mon, a.monConfigured
		a.mu.Unlock()
		if mon == nil || !ok {
			continue
		}
		if err := a.deliver(s, mon); err != nil {
			return // the session ended; the rest was requeued
		}
	}
}

// maxPointsPerMessage keeps telemetry messages small after a long outage.
const maxPointsPerMessage = 120

func (a *Agent) deliver(s *session, mon *agentmon.Engine) error {
	out := mon.Outbox()
	for {
		b := out.Drain(maxPointsPerMessage)
		sent := agentmon.Batch{Latest: map[string][]byte{}}
		send := func(kind string, payload []byte) error {
			return s.send(&pb.AgentMessage{Msg: &pb.AgentMessage_Telemetry{Telemetry: &pb.Telemetry{Kind: kind, Output: payload, CollectedAtUnix: a.now().Unix()}}})
		}
		var err error
		// Important events first, then points, then the latest reports.
		if len(b.Events) > 0 || b.LostEv > 0 {
			payload, _ := json.Marshal(map[string]any{"events": b.Events, "lost": b.LostEv})
			if err = send(agentmon.KindEvents, payload); err == nil {
				b.Events, b.LostEv = nil, 0
			}
		}
		if err == nil && b.Metrics != nil {
			payload, _ := json.Marshal(b.Metrics)
			if err = send(agentmon.KindMetrics, payload); err == nil {
				b.Metrics = nil
			}
		}
		for _, k := range b.Order {
			if err != nil {
				break
			}
			if err = send(k, b.Latest[k]); err == nil {
				sent.Latest[k] = nil
			}
		}
		if err != nil {
			for k := range sent.Latest {
				delete(b.Latest, k)
			}
			var order []string
			for _, k := range b.Order {
				if _, ok := b.Latest[k]; ok {
					order = append(order, k)
				}
			}
			b.Order = order
			out.Requeue(b)
			return err
		}
		if used, _, _ := out.Bytes(); used == 0 {
			return nil
		}
	}
}

// monitorRequest handles RequestTelemetry for monitoring kinds.
func (a *Agent) monitorRequest(kind string) bool {
	mon := a.Monitor()
	switch kind {
	case agentmon.KindServices, agentmon.KindStorage, "smart", agentmon.KindState:
		if mon != nil {
			if kind == "smart" || kind == agentmon.KindStorage {
				if l, ok := mon.Source().(*source.Local); ok {
					l.Rescan() // a helper may just have been installed
				}
			}
			mon.Request(kind)
		}
		return true
	}
	return false
}
