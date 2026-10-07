package sshdriver

import (
	"context"

	"drool/internal/hostops"
	"drool/internal/model"
	"drool/internal/sshx"
)

// SimResult is a dry run of a package or system-update operation.
type SimResult = hostops.SimResult

// Simulate shows what a package or system-update step would do, from the
// host's current package metadata (no refresh), without changing anything.
func (x *Executor) Simulate(ctx context.Context, t *sshx.Target, b Become, op model.Operation) (*SimResult, error) {
	b, err := hostops.RootBecome(t.User, b)
	if err != nil {
		return nil, err
	}
	sess, err := x.Pool.Get(ctx, t)
	if err != nil {
		return nil, err
	}
	defer sess.Release()
	return x.host(sess.Client, t, b).Simulate(ctx, op)
}
