package sshdriver

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"drool/internal/discovery"
	"drool/internal/driver"
	"drool/internal/model"
	"drool/internal/sshx"
)

// ProbeTimeout bounds each remote check.
const ProbeTimeout = 20 * time.Second

// ProbeOptions controls which checks run.
type ProbeOptions struct {
	Become Become
	// JumpOnly hosts only need to be reachable and accept authentication.
	JumpOnly bool
}

// Probe tests connectivity step by step over a fresh connection (no pool):
// reachability, host key, authentication, privilege escalation, Linux, and
// systemd. It changes nothing on the host.
func Probe(ctx context.Context, d *sshx.Dialer, t *sshx.Target, opt ProbeOptions) (*driver.ProbeResult, error) {
	res := &driver.ProbeResult{}
	add := func(name, status, detail string, err error) {
		st := driver.ProbeStep{Name: name, Status: status, Detail: detail}
		if err != nil {
			oe := model.AsOpError(err)
			st.ErrorClass, st.Hint = string(oe.Class), oe.Hint
			if st.Detail == "" {
				st.Detail = strings.TrimPrefix(oe.Error(), string(oe.Class)+": ")
			}
		}
		res.Steps = append(res.Steps, st)
	}
	skipRest := func(names ...string) {
		for _, n := range names {
			add(n, driver.StepSkipped, "", nil)
		}
	}
	remote := []string{"privilege escalation", "operating system", "init system"}
	if opt.JumpOnly {
		remote = nil
	}

	c, err := d.Dial(ctx, t)
	if err != nil {
		switch model.ClassOf(err) {
		case model.ClassHostKey:
			add("reachable", driver.StepOK, "", nil)
			add("host key", driver.StepFailed, "", err)
			skipRest(append([]string{"authentication"}, remote...)...)
		case model.ClassAuthentication:
			add("reachable", driver.StepOK, "", nil)
			add("host key", driver.StepOK, "matches pinned key", nil)
			add("authentication", driver.StepFailed, "", err)
			skipRest(remote...)
		default:
			add("reachable", driver.StepFailed, "", err)
			skipRest(append([]string{"host key", "authentication"}, remote...)...)
		}
		return res, err
	}
	defer c.Close()
	add("reachable", driver.StepOK, "", nil)
	add("host key", driver.StepOK, "matches pinned key", nil)
	add("authentication", driver.StepOK, "as "+t.User, nil)
	if opt.JumpOnly {
		res.OK = true
		return res, nil
	}

	run := func(cmd string, stdin []byte, pty bool) (*sshx.RunResult, error) {
		rctx, cancel := context.WithTimeout(ctx, ProbeTimeout)
		defer cancel()
		return c.Run(rctx, cmd, sshx.RunOptions{Stdin: stdin, PTY: pty, MaxOutput: 64 << 10})
	}

	// Privilege escalation.
	var failure error
	if opt.Become.Method != "sudo" {
		add("privilege escalation", driver.StepSkipped, "not configured; operations run as "+t.User, nil)
	} else {
		cmd, stdin := opt.Become.Wrap("true")
		r, err := run(cmd, stdin, opt.Become.PTY)
		switch {
		case err != nil:
			add("privilege escalation", driver.StepFailed, "", err)
			failure = err
		case r.ExitCode != 0:
			detail, hint := classifySudo(string(r.Stderr) + string(r.Stdout))
			failure = &model.OpError{Class: model.ClassAuthorization, Message: detail, Hint: hint}
			add("privilege escalation", driver.StepFailed, detail, failure)
		default:
			mode := "passwordless sudo"
			if opt.Become.Password != "" {
				mode = "sudo with password"
			}
			add("privilege escalation", driver.StepOK, mode, nil)
		}
	}

	// Operating system.
	if r, err := run("uname -sr", nil, false); err != nil {
		add("operating system", driver.StepFailed, "", err)
		failure = errors.Join(failure, err)
	} else if out := strings.TrimSpace(string(r.Stdout)); !strings.HasPrefix(out, "Linux") {
		e := &model.OpError{Class: model.ClassConfiguration, Message: fmt.Sprintf("not a Linux host (%q)", out),
			Hint: "DROOL manages Linux hosts only."}
		add("operating system", driver.StepFailed, "", e)
		failure = errors.Join(failure, e)
	} else {
		add("operating system", driver.StepOK, out, nil)
	}

	// Init system: the same evidence as discovery (PID 1 first), which an
	// account without access to /run/systemd reads too.
	if r, err := run(discovery.SystemdProbe+`printf '%s %s\n' "$(sdstate)" "$(cat /proc/1/comm 2>/dev/null)"`, nil, false); err != nil {
		add("init system", driver.StepFailed, "", err)
		failure = errors.Join(failure, err)
	} else {
		state, pid1, _ := strings.Cut(strings.TrimSpace(string(r.Stdout)), " ")
		switch state {
		case discovery.SystemdRunning:
			add("init system", driver.StepOK, "systemd", nil)
		case discovery.SystemdUnknown:
			add("init system", driver.StepOK, "probably systemd (PID 1 and /run/systemd are hidden from this account; DROOL asks systemctl)", nil)
		default:
			msg := "systemd is not running"
			if pid1 != "" {
				msg = "PID 1 is " + pid1 + ", not systemd"
			}
			e := &model.OpError{Class: model.ClassConfiguration, Message: msg,
				Hint: "Commands, scripts and package operations work; service management and reboots need systemd."}
			add("init system", driver.StepFailed, "", e)
			failure = errors.Join(failure, e)
		}
	}

	res.OK = failure == nil
	return res, nil
}
