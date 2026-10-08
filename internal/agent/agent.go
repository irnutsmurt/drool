package agent

import (
	"context"
	"drool/internal/firewall/hostfw"
	"drool/internal/netmon/collect"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"syscall"
	"time"

	"drool/internal/agentcaps"
	"drool/internal/hostexec"
	"drool/internal/monitor/agentmon"
)

// Options configure an agent.
type Options struct {
	Paths   Paths
	Version string
	Log     *slog.Logger
	// Runner executes commands; default hostexec.Local.
	Runner hostexec.Runner
	// Now is the clock (tests).
	Now func() time.Time
	// StatusEvery is the telemetry status interval (default 60s).
	StatusEvery time.Duration
	// RenewCheck is how often the certificate's age is checked (default 1m).
	RenewCheck time.Duration
	// Supervisor tells the update helper how to restart the agent:
	// "systemd" (default when systemd runs) or "exit" (a supervisor such as
	// a test harness restarts the process).
	Supervisor string
	// FirewallPaths redirects the firewall tooling (tests).
	FirewallPaths *hostfw.Paths
	// NetCollect overrides the connection collector's options (tests: a
	// fake ss, a short sample period, a fake conntrack).
	NetCollect *collect.Options
	// Monitor overrides the monitoring engine's options (tests: a fixture
	// root, fake helpers, no /dev/kmsg); MonitorExtra adds a collector
	// (the runaway test build).
	Monitor      *agentmon.Options
	MonitorExtra func(ctx context.Context)
}

// Agent is one running agent.
type Agent struct {
	opt     Options
	paths   Paths
	store   *Store
	ledger  *Ledger
	oplog   *OpLog
	file    *agentcaps.File
	ceiling agentcaps.Caps
	run     hostexec.Runner
	log     *slog.Logger
	now     func() time.Time
	nonce   string

	mu        sync.Mutex
	effective agentcaps.Caps
	running   map[string]context.CancelFunc
	session   *session // current controller session, if any
	ctx       context.Context
	protocol  uint32 // negotiated with the controller

	sessionCancel context.CancelCauseFunc // ends the current session (firewall verification)
	learning      bool
	sampleEvery   time.Duration
	coll          *collect.Collector // connection collector, while collection is on
	collectStop   context.CancelFunc

	mon           *agentmon.Engine // system monitoring (M11), while enabled
	monStop       context.CancelFunc
	monConfigured bool // this process received a MonitorConfig
	diag          *diagHandler
	bgRun         hostexec.Runner // background package checks: a low-priority scope
	monRun        hostexec.Runner // monitoring scripts: inside the agent's budget
}

// LoadCeiling reads and checks /etc/drool/agent.yaml. When the agent runs
// as root, the file must be owned by root and writable by nobody else:
// otherwise any local user who can edit it could widen the ceiling.
func LoadCeiling(path string) (*agentcaps.File, error) {
	fi, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%s does not exist; create it with `drool-agent init --preset monitor|managed|full` (it is the capability ceiling)", path)
		}
		return nil, err
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok && os.Getuid() == 0 {
		if st.Uid != 0 || fi.Mode().Perm()&0o022 != 0 {
			return nil, fmt.Errorf("%s must be owned by root and not writable by group or others (it limits what DROOL may do on this host)", path)
		}
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return agentcaps.ParseFile(b)
}

// New prepares an agent: state, ledger, operation log and ceiling.
func New(opt Options) (*Agent, error) {
	if opt.Now == nil {
		opt.Now = time.Now
	}
	if opt.Log == nil {
		opt.Log = slog.Default()
	}
	if opt.StatusEvery <= 0 {
		opt.StatusEvery = time.Minute
	}
	if opt.RenewCheck <= 0 {
		opt.RenewCheck = time.Minute
	}
	file, err := LoadCeiling(opt.Paths.Config)
	if err != nil {
		return nil, err
	}
	st, err := OpenStore(opt.Paths.State)
	if err != nil {
		return nil, err
	}
	led, err := OpenLedger(opt.Paths.State)
	if err != nil {
		return nil, err
	}
	ol, err := OpenOpLog(opt.Paths.Log)
	if err != nil {
		return nil, err
	}
	diag := newDiagHandler(opt.Log.Handler())
	a := &Agent{opt: opt, paths: opt.Paths, store: st, ledger: led, oplog: ol, file: file, ceiling: file.Capabilities,
		run: opt.Runner, log: slog.New(diag), now: opt.Now, nonce: randomID(), running: map[string]context.CancelFunc{},
		sampleEvery: defaultSampleEvery, diag: diag}
	a.effective = a.computeEffective()
	a.setupRunners()
	return a, nil
}

// computeEffective = ceiling ∩ the controller's stored narrowing request.
func (a *Agent) computeEffective() agentcaps.Caps {
	req := agentcaps.All()
	if b, err := a.store.Requested(); err == nil && len(b) > 0 {
		var r agentcaps.Caps
		if json.Unmarshal(b, &r) == nil {
			req = r
		}
	}
	return agentcaps.Intersect(a.ceiling, req)
}

// Effective returns the effective capabilities.
func (a *Agent) Effective() agentcaps.Caps {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.effective
}

// Close releases resources.
func (a *Agent) Close() error { return a.oplog.Close() }

func machineID() string {
	b, _ := os.ReadFile("/etc/machine-id")
	return strings.TrimSpace(string(b))
}

func bootID() string {
	b, _ := os.ReadFile("/proc/sys/kernel/random/boot_id")
	return strings.TrimSpace(string(b))
}

// Paths returns the agent's directories.
func (a *Agent) Paths() Paths { return a.paths }
