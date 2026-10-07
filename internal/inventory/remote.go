package inventory

import (
	"context"
	"time"

	"drool/internal/driver/sshdriver"
	"drool/internal/hostexec"
	"drool/internal/hostops"
	"drool/internal/sshx"
)

// RunOnHost runs cmd over the host's pinned SSH connection, as root
// (through the host's sudo setting) when asRoot. stdin follows the sudo
// password, if any. Used to bootstrap the agent on SSH-managed hosts.
func (s *Service) RunOnHost(ctx context.Context, hostID, cmd string, stdin []byte, asRoot bool, timeout time.Duration) (*sshx.RunResult, error) {
	c, err := s.Resolve(ctx, hostID, true)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	sess, err := s.pool.Get(ctx, c.Target)
	if err != nil {
		return nil, err
	}
	defer sess.Release()
	pty := false
	if asRoot {
		b, err := hostops.RootBecome(c.Target.User, c.Become)
		if err != nil {
			return nil, err
		}
		var prefix []byte
		cmd, prefix = b.Wrap(cmd)
		stdin = append(prefix, stdin...)
		pty = b.PTY
	}
	return sess.Run(ctx, cmd, sshx.RunOptions{Stdin: stdin, PTY: pty, MaxOutput: 1 << 20})
}

// RootRunner returns a runner for a host and the privilege escalation to
// run as root. fresh dials a brand-new connection through the whole jump
// chain and never uses the pool: firewall verification must not ride on an
// established, multiplexed connection (DESIGN §7, §17.2). release closes
// what was opened.
func (s *Service) RootRunner(ctx context.Context, hostID string, fresh bool) (hostexec.Runner, hostops.Become, func(), error) {
	c, err := s.Resolve(ctx, hostID, true)
	if err != nil {
		return nil, hostops.Become{}, nil, err
	}
	b, err := hostops.RootBecome(c.Target.User, c.Become)
	if err != nil {
		return nil, hostops.Become{}, nil, err
	}
	if fresh {
		cl, err := s.dialer.Dial(ctx, c.Target)
		if err != nil {
			return nil, b, nil, err
		}
		return sshdriver.Runner(cl), b, func() { cl.Close() }, nil
	}
	sess, err := s.pool.Get(ctx, c.Target)
	if err != nil {
		return nil, b, nil, err
	}
	return sshdriver.Runner(sess.Client), b, sess.Release, nil
}

// JumpHostID returns a host's SSH jump host ("" for none).
func (s *Service) JumpHostID(ctx context.Context, hostID string) string {
	c, err := s.Resolve(ctx, hostID, true)
	if err != nil || c.Target.Via == nil {
		return ""
	}
	return c.Target.Via.HostID
}
