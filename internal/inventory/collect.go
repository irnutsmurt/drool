package inventory

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	"drool/internal/discovery"
	"drool/internal/model"
	"drool/internal/sshx"
	"drool/internal/store/db"
)

// Timeouts for read-only collection and the (root) metadata refresh.
const (
	collectTimeout = 2 * time.Minute
	refreshTimeout = 10 * time.Minute
	maxCollectOut  = 4 << 20
)

// runScript sends script to `sh -s` over a pooled connection. With become,
// the host's privilege escalation wraps the shell.
func (s *Service) runScript(ctx context.Context, c *Conn, script string, become bool, timeout time.Duration) (*sshx.RunResult, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	sess, err := s.pool.Get(ctx, c.Target)
	if err != nil {
		return nil, err
	}
	defer sess.Release()
	cmd, stdin := "sh -s", []byte(script)
	pty := false
	if become && c.Become.Method == "sudo" {
		var prefix []byte
		cmd, prefix = c.Become.Wrap(cmd)
		stdin = append(prefix, stdin...)
		pty = c.Become.PTY
	}
	return sess.Run(ctx, cmd, sshx.RunOptions{Stdin: stdin, MaxOutput: maxCollectOut, PTY: pty})
}

// CanBecomeRoot reports whether DROOL can run root commands on the host
// (root login or sudo configured).
func CanBecomeRoot(h db.Host) bool {
	return h.SshUser == "root" || h.BecomeMethod == "sudo"
}

// Discover collects facts over SSH. It needs no privileges.
func (s *Service) Discover(ctx context.Context, hostID string) (*discovery.Facts, error) {
	c, err := s.Resolve(ctx, hostID, true)
	if err != nil {
		return nil, err
	}
	res, err := s.runScript(ctx, c, discovery.DiscoverScript, false, collectTimeout)
	if err != nil {
		return nil, err
	}
	f, _ := discovery.ParseDiscover(string(res.Stdout))
	return &f, nil
}

// StoreFactsTx saves facts collected over SSH and refreshes discovered
// addresses (informational only; DESIGN §5.2 convergence comes later).
func (s *Service) StoreFactsTx(ctx context.Context, q *db.Queries, hostID string, f *discovery.Facts) error {
	return s.StoreFactsSourceTx(ctx, q, hostID, f, "discovered")
}

// StoreFactsSourceTx saves facts; source is the address source
// ("discovered" for SSH, "agent" for authenticated agent telemetry).
func (s *Service) StoreFactsSourceTx(ctx context.Context, q *db.Queries, hostID string, f *discovery.Facts, source string) error {
	raw, err := json.Marshal(f)
	if err != nil {
		return err
	}
	now := s.now().UnixMilli()
	nullInt := func(v int64) sql.NullInt64 { return sql.NullInt64{Int64: v, Valid: v > 0} }
	if err := q.UpsertHostFacts(ctx, db.UpsertHostFactsParams{
		HostID: hostID, CollectedAt: now, OsFamily: f.OSFamily, Distro: f.Distro, DistroVersion: f.DistroVersion,
		DistroName: f.DistroName, Kernel: f.Kernel, Arch: f.Arch, Hostname: f.Hostname,
		CpuCount: nullInt(int64(f.CPUCount)), MemBytes: nullInt(f.MemBytes), PkgManager: f.PkgManager,
		InitSystem: f.InitSystem, ContainerRuntimes: strings.Join(f.ContainerRuntimes, ","), FactsJson: string(raw),
	}); err != nil {
		return err
	}
	for _, iface := range f.Interfaces {
		for _, a := range iface.Addresses {
			ip := net.ParseIP(a.Address)
			if ip == nil || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
				continue
			}
			if err := q.UpsertObservedAddress(ctx, db.UpsertObservedAddressParams{
				HostID: hostID, Address: a.Address, Family: int64(a.Family), Interface: iface.Name, Source: source,
				FirstSeenAt: now, LastSeenAt: now,
			}); err != nil {
				return err
			}
		}
	}
	return q.DeleteStaleObservedAddresses(ctx, db.DeleteStaleObservedAddressesParams{HostID: hostID, LastSeenAt: now})
}

// CheckStatus runs the lightweight status check.
func (s *Service) CheckStatus(ctx context.Context, hostID string) (*discovery.Status, error) {
	c, err := s.Resolve(ctx, hostID, true)
	if err != nil {
		return nil, err
	}
	res, err := s.runScript(ctx, c, discovery.StatusScript, false, collectTimeout)
	if err != nil {
		return nil, err
	}
	st := discovery.ParseStatus(string(res.Stdout))
	return &st, nil
}

func nullF(p *float64) sql.NullFloat64 {
	if p == nil {
		return sql.NullFloat64{}
	}
	return sql.NullFloat64{Float64: *p, Valid: true}
}

func nullI64(p *int64) sql.NullInt64 {
	if p == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: *p, Valid: true}
}

func nullIntP(p *int) sql.NullInt64 {
	if p == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: int64(*p), Valid: true}
}

func nullBool(p *bool) sql.NullInt64 {
	if p == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: boolInt(*p), Valid: true}
}

// StatusOutcome is what a status check did to a host, for logging.
type StatusOutcome struct {
	Rebooted    bool   // boot id changed since the last check
	BecameReady bool   // was unreachable, now reachable
	NewStatus   string // host status set by this check ("" = unchanged)
}

// RecordStatusTx stores a status check result (st nil with checkErr set
// for a failed check) and keeps the host's reachability status current.
func (s *Service) RecordStatusTx(ctx context.Context, q *db.Queries, hostID string, st *discovery.Status, checkErr error) (StatusOutcome, error) {
	var out StatusOutcome
	now := s.now().UnixMilli()
	h, err := q.GetHost(ctx, hostID)
	if err != nil {
		return out, err
	}
	prev, prevErr := q.GetHostStatus(ctx, hostID)
	p := db.UpsertStatusCheckParams{HostID: hostID, CheckedAt: now}
	if checkErr == nil {
		p.Reachable = 1
		p.BootID, p.UptimeS, p.Load1 = st.BootID, nullI64(st.UptimeS), nullF(st.Load1)
		p.MemUsedPct, p.MaxFsUsedPct, p.MaxFsMount = nullF(st.MemUsedPct), nullF(st.MaxFSUsedPct), st.MaxFSMount
		p.FailedUnits, p.FailedUnitNames = nullIntP(st.FailedUnits), strings.Join(st.FailedUnitNames, ",")
		p.RebootRequired, p.RebootReason = nullBool(st.RebootRequired), st.RebootReason
		out.Rebooted = prevErr == nil && prev.BootID != "" && st.BootID != "" && prev.BootID != st.BootID
	} else {
		oe := model.AsOpError(checkErr)
		p.LastErrorClass, p.LastError = string(oe.Class), truncate(errMsg(oe), 500)
		p.MaxFsMount = prev.MaxFsMount
	}
	if err := q.UpsertStatusCheck(ctx, p); err != nil {
		return out, err
	}

	// Keep the host status (set by connection tests) in step with reality.
	newStatus, detail := "", ""
	if checkErr == nil {
		if h.Status == StatusUnreachable {
			newStatus, detail = StatusReady, "reachable again"
			out.BecameReady = true
		}
	} else {
		switch model.ClassOf(checkErr) {
		case model.ClassHostKey:
			newStatus, detail = StatusHostKeyChanged, errMsg(model.AsOpError(checkErr))
			var mm *sshx.HostKeyMismatchError
			if errors.As(checkErr, &mm) && mm.Host == h.Name {
				if _, err := s.RecordOfferedKeysTx(ctx, q, hostID, []ssh.PublicKey{mm.Presented}); err != nil {
					return out, err
				}
			}
		case model.ClassAuthentication:
			newStatus, detail = StatusAuthFailed, errMsg(model.AsOpError(checkErr))
		case model.ClassConnection, model.ClassTimeout:
			if h.Status == StatusReady {
				newStatus, detail = StatusUnreachable, errMsg(model.AsOpError(checkErr))
			}
		}
		if newStatus == h.Status {
			newStatus = ""
		}
	}
	if newStatus != "" {
		out.NewStatus = newStatus
		if err := q.UpdateHostStatus(ctx, db.UpdateHostStatusParams{Status: newStatus, StatusDetail: truncate(detail, 500),
			LastProbeAt: h.LastProbeAt, UpdatedAt: now, ID: hostID}); err != nil {
			return out, err
		}
	}
	return out, nil
}

func errMsg(oe *model.OpError) string {
	if oe.Message != "" {
		return oe.Message
	}
	return oe.Error()
}

// CheckUpdates counts pending updates from the local package cache.
func (s *Service) CheckUpdates(ctx context.Context, hostID, pkgManager string) (*discovery.Updates, error) {
	c, err := s.Resolve(ctx, hostID, true)
	if err != nil {
		return nil, err
	}
	var script string
	switch pkgManager {
	case "apt":
		script = discovery.UpdatesAPTScript
	case "dnf", "yum":
		script = discovery.UpdatesDNFScript
	default:
		return nil, model.Errorf(model.ClassConfiguration, false, "update checks are not supported for package manager %q", pkgManager)
	}
	res, err := s.runScript(ctx, c, script, false, collectTimeout)
	if err != nil {
		return nil, err
	}
	var u discovery.Updates
	if pkgManager == "apt" {
		u = discovery.ParseUpdatesAPT(string(res.Stdout))
	} else {
		u = discovery.ParseUpdatesDNF(string(res.Stdout))
	}
	return &u, nil
}

// RecordUpdatesTx stores an update check. Reboot information from the check
// (needs-restarting) overrides the status check's heuristic.
func (s *Service) RecordUpdatesTx(ctx context.Context, q *db.Queries, hostID string, u *discovery.Updates) error {
	cur, err := q.GetHostStatus(ctx, hostID)
	if err != nil {
		return err
	}
	reboot, reason := cur.RebootRequired, cur.RebootReason
	if u.RebootRequired != nil {
		reboot, reason = nullBool(u.RebootRequired), u.RebootReason
	}
	return q.UpdateStatusUpdates(ctx, db.UpdateStatusUpdatesParams{
		UpdatesAvailable: nullIntP(u.Available), SecurityUpdates: nullIntP(u.Security),
		UpdatesCheckedAt: sql.NullInt64{Int64: s.now().UnixMilli(), Valid: true},
		RebootRequired:   reboot, RebootReason: reason, HostID: hostID,
	})
}

// RefreshMetadata refreshes package metadata as root (apt-get update /
// dnf makecache). Hosts without root access are skipped by the caller.
func (s *Service) RefreshMetadata(ctx context.Context, hostID, pkgManager string) error {
	c, err := s.Resolve(ctx, hostID, true)
	if err != nil {
		return err
	}
	if !CanBecomeRoot(c.Host) {
		return model.Errorf(model.ClassConfiguration, false, "no root access configured")
	}
	cmd := discovery.RefreshAPT
	if pkgManager == "dnf" || pkgManager == "yum" {
		cmd = discovery.RefreshDNF
	}
	res, err := s.runScript(ctx, c, cmd+"\n", c.Host.SshUser != "root", refreshTimeout)
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return &model.OpError{Class: model.ClassPackageManager, Retryable: true,
			Message: "metadata refresh failed: " + truncate(strings.TrimSpace(string(res.Stderr)), 300)}
	}
	return s.st.W.UpdateStatusRefreshed(ctx, db.UpdateStatusRefreshedParams{
		MetadataRefreshedAt: sql.NullInt64{Int64: s.now().UnixMilli(), Valid: true}, HostID: hostID})
}

// HostOS returns the discovered OS of a host for evaluating job conditions.
func (s *Service) HostOS(ctx context.Context, hostID string) (model.HostOS, error) {
	f, err := s.st.R.GetHostFacts(ctx, hostID)
	if errors.Is(err, sql.ErrNoRows) {
		return model.HostOS{}, nil
	}
	if err != nil {
		return model.HostOS{}, err
	}
	return model.HostOS{Family: f.OsFamily, Distro: f.Distro, Known: f.OsFamily != "" && f.OsFamily != "unknown"}, nil
}
