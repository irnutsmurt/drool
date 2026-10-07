package sshdriver

import (
	"context"
	"io"
	"strings"

	"drool/internal/driver"
	"drool/internal/filedeploy"
	"drool/internal/hostexec"
	"drool/internal/hostops"
	"drool/internal/model"
	"drool/internal/sshx"
)

// Executor runs operations over pooled SSH connections.
type Executor struct {
	Pool *sshx.Pool
	// Files is needed for file deployments: the artifact store and where
	// deployment history is recorded.
	Files *FileDeps
}

// FileDeps are what file deployments need from the controller.
type FileDeps struct {
	Source   filedeploy.Source
	Recorder filedeploy.Recorder
}

// Execute runs a command or script operation on t.
func (x *Executor) Execute(ctx context.Context, t *sshx.Target, b Become, op model.Operation,
	out driver.OutputSink, started func()) (*driver.OpResult, error) {

	switch op.Type {
	case model.OpPackage, model.OpSystemUpdate, model.OpService, model.OpReboot:
		return x.executeTyped(ctx, t, b, op, out, started)
	case model.OpFileDeploy:
		return x.executeFileDeploy(ctx, t, b, op.FileDeploy, out, started)
	}
	var interp, body string
	var become bool
	switch op.Type {
	case model.OpCommand:
		interp, body, become = "sh", op.Command.Command+"\n", op.Command.Become
	case model.OpScript:
		interp, body, become = op.Script.Interpreter, op.Script.Body.Text, op.Script.Become
		if !strings.HasSuffix(body, "\n") {
			body += "\n"
		}
	default:
		return nil, model.Errorf(model.ClassValidation, false, "the SSH driver cannot run %q operations yet", op.Type)
	}
	if !become {
		b = Become{Method: "none", PTY: b.PTY}
	} else if b.Method != "sudo" {
		// become requested on a host without sudo: we already log in as root
		// (or the admin chose "none"); run as the login user.
		b.Method = "none"
	}

	sess, err := x.Pool.Get(ctx, t)
	if err != nil {
		return nil, &driver.NotStartedError{Err: err}
	}
	defer sess.Release()
	return hostops.Shell(ctx, sshRunner{sess.Client}, b, interp, body, out, started)
}

// executeTyped runs package, system-update, service and reboot operations,
// which always need root.
func (x *Executor) executeTyped(ctx context.Context, t *sshx.Target, b Become, op model.Operation,
	out driver.OutputSink, started func()) (*driver.OpResult, error) {
	b, err := hostops.RootBecome(t.User, b)
	if err != nil {
		return nil, err
	}
	sess, err := x.Pool.Get(ctx, t)
	if err != nil {
		return nil, &driver.NotStartedError{Err: err}
	}
	defer sess.Release()
	h := x.host(sess.Client, t, b)
	switch op.Type {
	case model.OpPackage:
		return h.Package(ctx, op.Package, out, started)
	case model.OpSystemUpdate:
		return h.SystemUpdate(ctx, op.SystemUpdate, out, started)
	case model.OpService:
		return h.Service(ctx, op.Service, out, started)
	default:
		return h.RebootOp(ctx, op.Reboot, out, started)
	}
}

// host binds hostops to an SSH connection; reboots wait with fresh,
// host-key-verified connections.
func (x *Executor) host(c *sshx.Client, t *sshx.Target, b Become) hostops.Host {
	return hostops.Host{R: sshRunner{c}, B: b,
		Rebooting: func() { x.Pool.Evict(t.HostID) },
		WaitReboot: func(ctx context.Context, res *hostops.RebootResult, out driver.OutputSink) error {
			return x.waitReboot(ctx, t, res, out)
		},
	}
}

// sshRunner adapts sshx.Client to hostexec.Runner.
type sshRunner struct{ c *sshx.Client }

func (r sshRunner) Run(ctx context.Context, cmd string, opt hostexec.RunOptions) (*hostexec.RunResult, error) {
	return r.c.Run(ctx, cmd, opt)
}

func (r sshRunner) Start(cmd string, stdin []byte, stdout, stderr io.Writer, pty bool) (hostexec.Process, error) {
	p, err := r.c.Start(cmd, stdin, stdout, stderr, pty)
	if err != nil {
		return nil, err
	}
	return p, nil
}
