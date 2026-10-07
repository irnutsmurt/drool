package inventory

import (
	"context"
	"time"

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
