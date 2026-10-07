package hostops

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"drool/internal/driver"
	"drool/internal/hostexec"
	"drool/internal/model"
	"drool/internal/ops/pkg"
)

// Host is a host reachable through a Runner. Typed operations run as root
// through B (Method "none" when already root: a root SSH login, or the
// agent).
type Host struct {
	R hostexec.Runner
	B Become
	// Rebooting is called right after a reboot was triggered (the SSH
	// path drops its pooled connection).
	Rebooting func()
	// WaitReboot waits for the host to come back with a new boot id and
	// fills in res. Nil means the caller waits elsewhere (the agent can't
	// wait for its own reboot; the controller does).
	WaitReboot func(ctx context.Context, res *RebootResult, out driver.OutputSink) error
}

// RootBecome returns how to become root for typed operations
// (ARCHITECTURE-ALPHA §5.1): directly for root, otherwise through sudo.
func RootBecome(user string, b Become) (Become, error) {
	if user == "root" {
		return Become{Method: "none", PTY: b.PTY}, nil
	}
	if b.Method != "sudo" {
		return b, &model.OpError{Class: model.ClassConfiguration,
			Message: "this operation needs root, but the host logs in as " + user + " without sudo",
			Hint:    "Enable sudo for this host or log in as root."}
	}
	return b, nil
}

func Note(out driver.OutputSink, format string, args ...any) {
	fmt.Fprintf(out.Stderr(), "[drool] "+format+"\n", args...)
}

// RunCmd runs a short command, as root when asRoot.
func RunCmd(ctx context.Context, c hostexec.Runner, b Become, asRoot bool, cmd string, timeout time.Duration) (*hostexec.RunResult, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var stdin []byte
	pty := false
	if asRoot {
		cmd, stdin = b.Wrap(cmd)
		pty = b.PTY
	}
	return c.Run(ctx, cmd, hostexec.RunOptions{Stdin: stdin, PTY: pty, MaxOutput: 8 << 20})
}

func backendFor(ctx context.Context, c hostexec.Runner) (pkg.Backend, error) {
	r, err := RunCmd(ctx, c, Become{}, false, "sh -c "+ShQuote(pkg.DetectCommand), 30*time.Second)
	if err != nil {
		return nil, err
	}
	return pkg.ForManager(strings.TrimSpace(string(r.Stdout)))
}

func versions(ctx context.Context, c hostexec.Runner, be pkg.Backend, names []string) (map[string]string, error) {
	var cmd string
	if names == nil {
		cmd = be.AllVersions()
	} else {
		cmd = be.Versions(names)
	}
	r, err := RunCmd(ctx, c, Become{}, false, "sh -c "+ShQuote(cmd), time.Minute)
	if err != nil {
		return nil, err
	}
	return be.ParseVersions(string(r.Stdout), names), nil
}

func simulate(ctx context.Context, c hostexec.Runner, b Become, be pkg.Backend, action string, names []string) (pkg.Plan, error) {
	cmd, err := be.Simulate(action, names)
	if err != nil {
		return pkg.Plan{}, err
	}
	r, err := RunCmd(ctx, c, b, be.SimulateNeedsRoot(), "sh -c "+ShQuote(cmd), 5*time.Minute)
	if err != nil {
		return pkg.Plan{}, err
	}
	return be.ParsePlan(string(r.Stdout)+string(r.Stderr), r.ExitCode)
}

// simulateNamed simulates a named-package change. When the package isn't in
// the host's metadata cache (or there is no cache yet, as on a freshly
// installed host), it refreshes the metadata once and simulates again.
func simulateNamed(ctx context.Context, c hostexec.Runner, b Become, be pkg.Backend, action string, names []string, out driver.OutputSink) (pkg.Plan, error) {
	plan, err := simulate(ctx, c, b, be, action, names)
	if err == nil || !staleCache(err) {
		return plan, err
	}
	if out != nil {
		Note(out, "not in the local package metadata; refreshing it and trying again")
	}
	r, rerr := RunCmd(ctx, c, b, true, "sh -c "+ShQuote(be.Refresh()), 5*time.Minute)
	if rerr != nil {
		return pkg.Plan{}, rerr
	}
	if r.ExitCode != 0 {
		return pkg.Plan{}, be.Classify(string(r.Stdout)+string(r.Stderr), r.ExitCode)
	}
	return simulate(ctx, c, b, be, action, names)
}

// staleCache reports errors a metadata refresh may fix.
func staleCache(err error) bool {
	oe := model.AsOpError(err)
	return (oe.Class == model.ClassValidation && strings.HasPrefix(oe.Message, "package not found")) ||
		(oe.Class == model.ClassPackageManager && strings.HasPrefix(oe.Message, "no package metadata cache"))
}

func rebootRequired(ctx context.Context, c hostexec.Runner, be pkg.Backend) (*bool, string) {
	r, err := RunCmd(ctx, c, Become{}, false, "sh -c "+ShQuote(be.RebootRequired()), time.Minute)
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
func (h Host) Simulate(ctx context.Context, op model.Operation) (*SimResult, error) {
	be, err := backendFor(ctx, h.R)
	if err != nil {
		return nil, err
	}
	action, names := planAction(op)
	var plan pkg.Plan
	if op.Type == model.OpPackage {
		plan, err = simulateNamed(ctx, h.R, h.B, be, action, names, nil)
	} else {
		plan, err = simulate(ctx, h.R, h.B, be, action, names)
	}
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

// Package installs, removes or upgrades named packages.
func (h Host) Package(ctx context.Context, op *model.PackageOp, out driver.OutputSink, started func()) (*driver.OpResult, error) {
	c, b := h.R, h.B
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
	plan, err := simulateNamed(ctx, c, b, be, op.Action, op.Names, out)
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
		Note(out, "nothing to do: %s", map[string]string{"install": "already installed", "remove": "not installed",
			"upgrade": "already the newest version"}[op.Action])
		started()
		zero := 0
		f := false
		if req, why := rebootRequired(ctx, c, be); req != nil {
			data["reboot_required"], data["reboot_reason"] = *req, why
		}
		return &driver.OpResult{ExitCode: &zero, Changed: &f, Data: data}, nil
	}
	cmd, err := be.Exec(op.Action, op.Names)
	if err != nil {
		return nil, err
	}
	Note(out, "%s", cmd)
	r, err := RunStreamed(ctx, c, b, "sh", cmd+"\n", out, started)
	if err != nil {
		return nil, err
	}
	res := &driver.OpResult{ExitCode: &r.Exit, Data: data}
	if r.Exit != 0 {
		return res, be.Classify(r.Head+"\n"+r.Tail, r.Exit)
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

// SystemUpdate upgrades the system per mode, then reboots if asked.
func (h Host) SystemUpdate(ctx context.Context, op *model.SystemUpdateOp, out driver.OutputSink, started func()) (*driver.OpResult, error) {
	c, b := h.R, h.B
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
		Note(out, "refreshing package metadata")
		r, err := RunStreamed(ctx, c, b, "sh", be.Refresh()+"\n", out, nil)
		if err != nil {
			return nil, err
		}
		if r.Exit != 0 {
			return &driver.OpResult{ExitCode: &r.Exit}, be.Classify(r.Head+"\n"+r.Tail, r.Exit)
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
		Note(out, "the system is up to date")
	} else {
		Note(out, "%d to upgrade, %d to install, %d to remove", len(plan.Upgrade), len(plan.Install), len(plan.Remove))
		cmd, _ := be.Exec(action, nil)
		Note(out, "%s", cmd)
		r, err := RunStreamed(ctx, c, b, "sh", cmd+"\n", out, nil)
		if err != nil {
			return nil, err
		}
		res.ExitCode = &r.Exit
		if r.Exit != 0 {
			return res, be.Classify(r.Head+"\n"+r.Tail, r.Exit)
		}
	}
	req, why := rebootRequired(ctx, c, be)
	if req != nil {
		data["reboot_required"], data["reboot_reason"] = *req, why
	}
	if op.Reboot == "always" || (op.Reboot == "if_required" && req != nil && *req) {
		Note(out, "rebooting (%s)", map[string]string{"always": "requested", "if_required": why}[op.Reboot])
		rb, err := h.Reboot(ctx, true, out)
		data["reboot"] = rb
		if err != nil {
			return res, err
		}
		*res.Changed = true
	} else if op.Reboot == "if_required" && req == nil {
		Note(out, "could not determine whether a reboot is required; not rebooting")
	}
	return res, nil
}
