package sshdriver

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"drool/internal/driver"
	"drool/internal/model"
	"drool/internal/ops/pkg"
	"drool/internal/sshx"
)

// Typed operations always need root (ARCHITECTURE-ALPHA §5.1).
func rootBecome(t *sshx.Target, b Become) (Become, error) {
	if t.User == "root" {
		return Become{Method: "none", PTY: b.PTY}, nil
	}
	if b.Method != "sudo" {
		return b, &model.OpError{Class: model.ClassConfiguration,
			Message: "this operation needs root, but the host logs in as " + t.User + " without sudo",
			Hint:    "Enable sudo for this host or log in as root."}
	}
	return b, nil
}

func note(out driver.OutputSink, format string, args ...any) {
	fmt.Fprintf(out.Stderr(), "[drool] "+format+"\n", args...)
}

// run runs a short command, as root when asRoot.
func run(ctx context.Context, c *sshx.Client, b Become, asRoot bool, cmd string, timeout time.Duration) (*sshx.RunResult, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var stdin []byte
	pty := false
	if asRoot {
		cmd, stdin = b.Wrap(cmd)
		pty = b.PTY
	}
	return c.Run(ctx, cmd, sshx.RunOptions{Stdin: stdin, PTY: pty, MaxOutput: 8 << 20})
}

func backendFor(ctx context.Context, c *sshx.Client) (pkg.Backend, error) {
	r, err := run(ctx, c, Become{}, false, "sh -c "+shQuote(pkg.DetectCommand), 30*time.Second)
	if err != nil {
		return nil, err
	}
	return pkg.ForManager(strings.TrimSpace(string(r.Stdout)))
}

func versions(ctx context.Context, c *sshx.Client, be pkg.Backend, names []string) (map[string]string, error) {
	var cmd string
	if names == nil {
		cmd = be.AllVersions()
	} else {
		cmd = be.Versions(names)
	}
	r, err := run(ctx, c, Become{}, false, "sh -c "+shQuote(cmd), time.Minute)
	if err != nil {
		return nil, err
	}
	return be.ParseVersions(string(r.Stdout), names), nil
}

func simulate(ctx context.Context, c *sshx.Client, b Become, be pkg.Backend, action string, names []string) (pkg.Plan, error) {
	cmd, err := be.Simulate(action, names)
	if err != nil {
		return pkg.Plan{}, err
	}
	r, err := run(ctx, c, b, be.SimulateNeedsRoot(), "sh -c "+shQuote(cmd), 5*time.Minute)
	if err != nil {
		return pkg.Plan{}, err
	}
	return be.ParsePlan(string(r.Stdout)+string(r.Stderr), r.ExitCode)
}

func rebootRequired(ctx context.Context, c *sshx.Client, be pkg.Backend) (*bool, string) {
	r, err := run(ctx, c, Become{}, false, "sh -c "+shQuote(be.RebootRequired()), time.Minute)
	if err != nil {
		return nil, ""
	}
	return be.ParseRebootRequired(string(r.Stdout))
}

// SimResult is a dry run of a package or system-update operation.
type SimResult struct {
	PackageManager string       `json:"package_manager"`
	Plan           pkg.Plan     `json:"plan"`
	ExtraRemovals  []pkg.Change `json:"extra_removals"` // removals that need allow_removals
}

// Simulate shows what a package or system-update step would do, from the
// host's current package metadata (no refresh), without changing anything.
func (x *Executor) Simulate(ctx context.Context, t *sshx.Target, b Become, op model.Operation) (*SimResult, error) {
	b, err := rootBecome(t, b)
	if err != nil {
		return nil, err
	}
	sess, err := x.Pool.Get(ctx, t)
	if err != nil {
		return nil, err
	}
	defer sess.Release()
	be, err := backendFor(ctx, sess.Client)
	if err != nil {
		return nil, err
	}
	action, names := planAction(op)
	plan, err := simulate(ctx, sess.Client, b, be, action, names)
	if err != nil {
		return nil, err
	}
	res := &SimResult{PackageManager: be.Name(), Plan: plan, ExtraRemovals: []pkg.Change{}}
	if op.Type == model.OpPackage && op.Package.Action == "remove" {
		res.ExtraRemovals = append(res.ExtraRemovals, plan.ExtraRemovals(names)...)
	} else {
		res.ExtraRemovals = append(res.ExtraRemovals, plan.Remove...)
	}
	return res, nil
}

func planAction(op model.Operation) (string, []string) {
	if op.Type == model.OpPackage {
		return op.Package.Action, op.Package.Names
	}
	return map[string]string{"upgrade": "system-upgrade", "full_upgrade": "system-full-upgrade", "security": "system-security"}[op.SystemUpdate.Mode], nil
}

// executePackage installs, removes or upgrades named packages.
func (x *Executor) executePackage(ctx context.Context, c *sshx.Client, t *sshx.Target, b Become, op *model.PackageOp,
	out driver.OutputSink, started func()) (*driver.OpResult, error) {
	be, err := backendFor(ctx, c)
	if err != nil {
		return nil, err
	}
	before, err := versions(ctx, c, be, op.Names)
	if err != nil {
		return nil, err
	}
	if op.Action == "upgrade" {
		var missing []string
		for _, n := range op.Names {
			if before[n] == "" {
				missing = append(missing, n)
			}
		}
		if len(missing) > 0 {
			return nil, model.Errorf(model.ClassValidation, false, "not installed, so it cannot be upgraded: %s", strings.Join(missing, ", "))
		}
	}
	plan, err := simulate(ctx, c, b, be, op.Action, op.Names)
	if err != nil {
		return nil, err
	}
	extra := plan.ExtraRemovals(op.Names)
	if op.Action != "remove" {
		extra = plan.Remove
	}
	if len(extra) > 0 && !op.AllowRemovals {
		return nil, pkg.RemovalError(extra)
	}
	data := map[string]any{"package_manager": be.Name(), "plan": plan}
	if plan.Empty() {
		note(out, "nothing to do: %s", map[string]string{"install": "already installed", "remove": "not installed",
			"upgrade": "already the newest version"}[op.Action])
		started()
		zero := 0
		f := false
		return &driver.OpResult{ExitCode: &zero, Changed: &f, Data: data}, nil
	}
	cmd, err := be.Exec(op.Action, op.Names)
	if err != nil {
		return nil, err
	}
	note(out, "%s", cmd)
	r, err := runStreamed(ctx, c, b, "sh", cmd+"\n", out, started)
	if err != nil {
		return nil, err
	}
	res := &driver.OpResult{ExitCode: &r.exit, Data: data}
	if r.exit != 0 {
		return res, be.Classify(r.head+"\n"+r.tail, r.exit)
	}
	after, err := versions(ctx, c, be, op.Names)
	if err == nil {
		changes := diffVersions(before, after)
		data["changes"] = changes
		changed := len(changes) > 0 || len(plan.Remove) > 0 || len(plan.Install) > 0 || len(plan.Upgrade) > 0
		res.Changed = &changed
		// Verify the requested end state.
		for _, n := range op.Names {
			installed := after[n] != ""
			if (op.Action == "install" && !installed) || (op.Action == "remove" && installed) {
				return res, model.Errorf(model.ClassPackageManager, false, "%s finished, but %s is %s", be.Name(), n,
					map[bool]string{true: "still installed", false: "not installed"}[installed])
			}
		}
	}
	if req, why := rebootRequired(ctx, c, be); req != nil {
		data["reboot_required"], data["reboot_reason"] = *req, why
	}
	return res, nil
}

func diffVersions(before, after map[string]string) []pkg.Change {
	var out []pkg.Change
	seen := map[string]bool{}
	for n, a := range after {
		seen[n] = true
		if before[n] != a {
			out = append(out, pkg.Change{Name: n, From: before[n], To: a})
		}
	}
	for n, b := range before {
		if !seen[n] && b != "" {
			out = append(out, pkg.Change{Name: n, From: b})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// executeSystemUpdate upgrades the system per mode, then reboots if asked.
func (x *Executor) executeSystemUpdate(ctx context.Context, c *sshx.Client, t *sshx.Target, b Become, op *model.SystemUpdateOp,
	out driver.OutputSink, started func()) (*driver.OpResult, error) {
	be, err := backendFor(ctx, c)
	if err != nil {
		return nil, err
	}
	action := map[string]string{"upgrade": "system-upgrade", "full_upgrade": "system-full-upgrade", "security": "system-security"}[op.Mode]
	if _, err := be.Exec(action, nil); err != nil {
		return nil, err // e.g. security-only on APT
	}
	started()
	if op.RefreshMetadata() {
		note(out, "refreshing package metadata")
		r, err := runStreamed(ctx, c, b, "sh", be.Refresh()+"\n", out, nil)
		if err != nil {
			return nil, err
		}
		if r.exit != 0 {
			return &driver.OpResult{ExitCode: &r.exit}, be.Classify(r.head+"\n"+r.tail, r.exit)
		}
	}
	plan, err := simulate(ctx, c, b, be, action, nil)
	if err != nil {
		return nil, err
	}
	if len(plan.Remove) > 0 && !op.AllowRemovals {
		return nil, pkg.RemovalError(plan.Remove)
	}
	data := map[string]any{"package_manager": be.Name(), "mode": op.Mode, "plan": plan}
	zero := 0
	res := &driver.OpResult{ExitCode: &zero, Data: data}
	changed := !plan.Empty()
	res.Changed = &changed
	if plan.Empty() {
		note(out, "the system is up to date")
	} else {
		note(out, "%d to upgrade, %d to install, %d to remove", len(plan.Upgrade), len(plan.Install), len(plan.Remove))
		cmd, _ := be.Exec(action, nil)
		note(out, "%s", cmd)
		r, err := runStreamed(ctx, c, b, "sh", cmd+"\n", out, nil)
		if err != nil {
			return nil, err
		}
		res.ExitCode = &r.exit
		if r.exit != 0 {
			return res, be.Classify(r.head+"\n"+r.tail, r.exit)
		}
	}
	req, why := rebootRequired(ctx, c, be)
	if req != nil {
		data["reboot_required"], data["reboot_reason"] = *req, why
	}
	if op.Reboot == "always" || (op.Reboot == "if_required" && req != nil && *req) {
		note(out, "rebooting (%s)", map[string]string{"always": "requested", "if_required": why}[op.Reboot])
		rb, err := x.reboot(ctx, c, t, b, true, out)
		data["reboot"] = rb
		if err != nil {
			return res, err
		}
		*res.Changed = true
	} else if op.Reboot == "if_required" && req == nil {
		note(out, "could not determine whether a reboot is required; not rebooting")
	}
	return res, nil
}
