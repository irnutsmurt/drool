// Package sshdriver runs operations on hosts over SSH.
package sshdriver

import (
	"context"
	"time"

	"drool/internal/hostops"
	"drool/internal/sshx"
)

// Become describes privilege escalation (ARCHITECTURE-ALPHA §8.4).
type Become = hostops.Become

func classifySudo(stderr string) (detail, hint string) { return hostops.ClassifySudo(stderr) }

func shQuote(s string) string { return hostops.ShQuote(s) }

func rootBecome(t *sshx.Target, b Become) (Become, error) { return hostops.RootBecome(t.User, b) }

func run(ctx context.Context, c *sshx.Client, b Become, asRoot bool, cmd string, timeout time.Duration) (*sshx.RunResult, error) {
	return hostops.RunCmd(ctx, sshRunner{c}, b, asRoot, cmd, timeout)
}

func looksLikeSudoFailure(s string) bool { return hostops.LooksLikeSudoFailure(s) }
