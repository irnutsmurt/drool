package fwsvc

import (
	"context"
	"errors"
	"time"

	"drool/internal/audit"
	"drool/internal/firewall/hostfw"
)

// Restore reconciliation over SSH (ARCHITECTURE-ALPHA §33.1). Hosts with a
// Shepherd move their firewall state themselves, inside the agent's epoch
// transition; hosts managed over SSH are compared and moved from here.

// ErrNoSSH: the host has no SSH path DROOL may use.
var ErrNoSSH = errors.New("the host has no SSH management path")

func (s *Service) sshHost(ctx context.Context, hostID string) error {
	h, err := s.d.Store.R.GetHost(ctx, hostID)
	if err != nil {
		return err
	}
	if !s.sshPathConfigured(h) && !(h.ManagementMethod == "ssh" && h.SshConfigured != 0) {
		return ErrNoSSH
	}
	return nil
}

// SSHStatus reads the host firewall tool's status over a new SSH
// connection ("tool_version" none: DROOL's tool isn't installed).
func (s *Service) SSHStatus(ctx context.Context, hostID string) (map[string]string, error) {
	if err := s.sshHost(ctx, hostID); err != nil {
		return nil, err
	}
	cctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	c, err := s.sshConn(cctx, hostID, true)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	res, err := c.Run(cctx, hostfw.Action{Kind: hostfw.Status})
	if err != nil {
		return nil, err
	}
	s.recordStatus(ctx, hostID, res.Values)
	return res.Values, nil
}

// SSHRebase moves the host's committed policy (expected at revision rev
// with hash, in epoch from) to this controller's epoch, without changing
// a rule. The tool refuses when the committed policy isn't the expected
// one or a transaction is pending. The controller's record of the domain
// is moved with it.
func (s *Service) SSHRebase(ctx context.Context, hostID, from string, rev int64, hash string) error {
	if err := s.sshHost(ctx, hostID); err != nil {
		return err
	}
	lk := s.hostLock(hostID)
	lk.Lock()
	defer lk.Unlock()
	cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	c, err := s.sshConn(cctx, hostID, true)
	if err != nil {
		return err
	}
	defer c.Close()
	if _, err := c.Run(cctx, hostfw.Action{Kind: hostfw.Rebase, FromEpoch: from, Epoch: s.epoch, Revision: rev, Hash: hash}); err != nil {
		return err
	}
	if res, err := c.Run(cctx, hostfw.Action{Kind: hostfw.Status}); err == nil {
		s.recordStatus(ctx, hostID, res.Values)
	}
	s.audit(ctx, "firewall.rebase", hostID, audit.Success, map[string]any{"from_epoch": from, "epoch": s.epoch, "revision": rev})
	return nil
}

// Epoch is the controller epoch the firewall service writes.
func (s *Service) Epoch() string { return s.epoch }
