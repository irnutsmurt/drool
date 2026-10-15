package agent

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"drool/internal/agentrm"
	"drool/internal/agentrpc/pb"
	"drool/internal/hostops"
)

// Removing itself (dogfooding correction batch, ARCHITECTURE-ALPHA §40).
// The controller sends a typed SelfUninstall request over the
// authenticated session. The agent checks that it is meant for it (its
// identity, the controller epoch, the time), starts the removal helper
// (agentrm.Script, MODE=self) as root, detached so that it outlives the
// agent, and answers. The helper waits until the controller has the answer:
// the controller confirms it (SelfUninstall with proceed), and the agent
// then creates the helper's GO file. Without the confirmation (the session
// ended, or an older controller) the agent creates it after proceedWait,
// and the helper doesn't wait more than 20 seconds either. The helper then
// stops and removes the Shepherd (its service or supervisor, programs,
// ceiling, release keys, identity, state and logs) and finally itself. No
// capability is needed: it removes the Shepherd and nothing else. Requests
// that repeat are answered with the removal already started.

// removal is the removal this agent started.
type removal struct {
	requestID, how, dir string
	proceed             chan struct{} // closed when the controller confirms it has the answer
	confirmed, signaled bool
}

// proceedWait is how long the agent waits for the controller's
// confirmation before letting its removal helper go ahead anyway (the
// helper itself waits at most 20 seconds).
const proceedWait = 15 * time.Second

var requestIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// handleSelfUninstall answers a SelfUninstall request.
func (a *Agent) handleSelfUninstall(s *session, m *pb.SelfUninstall) {
	if m.Proceed {
		a.confirmRemoval()
		return
	}
	res := a.selfUninstall(m)
	ev := OpEvent{Event: "uninstall", OpID: m.RequestId, Controller: s.controller, Summary: "remove this Shepherd from the host", Outcome: "ok"}
	if !res.Accepted {
		ev.Outcome, ev.Reason = "rejected", res.Code
	}
	a.oplog.Write(ev)
	err := s.send(&pb.AgentMessage{Msg: &pb.AgentMessage_SelfUninstall{SelfUninstall: res}})
	if !res.Accepted {
		return
	}
	if err != nil {
		a.log.Warn("couldn't tell the controller that this Shepherd removes itself; the removal goes ahead", "err", err)
		a.signalRemoval()
		return
	}
	a.log.Info("removing this Shepherd from the host, as the controller asked", "helper", res.Helper, "reason", m.Reason)
	// Stopped right after sending, the answer could be lost in flight: the
	// helper goes ahead once the controller confirms it has it, when this
	// session ends without that, or after proceedWait.
	a.mu.Lock()
	proceed := a.removal.proceed
	a.mu.Unlock()
	select {
	case <-proceed:
	case <-s.stream.Context().Done():
	case <-time.After(proceedWait):
	}
	a.signalRemoval()
}

// confirmRemoval: the controller has this agent's accepting answer.
func (a *Agent) confirmRemoval() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if r := a.removal; r != nil && !r.confirmed {
		r.confirmed = true
		close(r.proceed)
	}
}

// signalRemoval lets the removal helper go ahead (its GO file), once.
func (a *Agent) signalRemoval() {
	a.mu.Lock()
	r := a.removal
	if r == nil || r.signaled {
		a.mu.Unlock()
		return
	}
	r.signaled = true
	dir := r.dir
	a.mu.Unlock()
	if err := os.WriteFile(filepath.Join(dir, "go"), nil, 0o600); err != nil {
		a.log.Warn("couldn't signal the removal helper; it goes ahead on its own shortly", "err", err)
	}
}

func (a *Agent) selfUninstall(m *pb.SelfUninstall) *pb.SelfUninstallResult {
	res := &pb.SelfUninstallResult{RequestId: m.RequestId}
	refuse := func(code, msg string) *pb.SelfUninstallResult {
		res.Code, res.Error = code, msg
		return res
	}
	if !requestIDRe.MatchString(m.RequestId) {
		return refuse("invalid", "the removal request has no usable id")
	}
	now := a.now()
	if m.ExpiresAtUnix == 0 || now.Unix() > m.ExpiresAtUnix {
		return refuse("expired", fmt.Sprintf("the removal request expired at %s (this host's time is %s)",
			time.Unix(m.ExpiresAtUnix, 0).UTC().Format(time.RFC3339), now.UTC().Format(time.RFC3339)))
	}
	if time.Unix(m.IssuedAtUnix, 0).After(now.Add(maxIssueSkew)) {
		return refuse("invalid", "the removal request was issued in the future; the controller's or this host's clock is wrong")
	}
	id, err := a.store.Identity()
	if err != nil {
		return refuse("identity", err.Error())
	}
	if m.IdentityId != id.IdentityID {
		return refuse("identity", "the removal request is meant for another Shepherd identity")
	}
	if m.ControllerEpoch != id.ControllerEpoch {
		return refuse("epoch", fmt.Sprintf("the removal request comes from controller epoch %s, but this Shepherd belongs to epoch %s", short(m.ControllerEpoch), short(id.ControllerEpoch)))
	}
	for _, it := range agentrm.Items {
		res.Removes = append(res.Removes, it.Path)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.removal != nil {
		res.Accepted, res.Already, res.Helper = true, true, a.removal.how
		return res
	}
	start := a.opt.StartRemoval
	if start == nil {
		// Checked before anything is written: nothing of the helper may
		// appear on a machine where the agent mustn't remove itself.
		if err := a.mayRemoveItself(); err != nil {
			return refuse("start", err.Error())
		}
		start = a.startRemoval
	}
	dir, err := a.removalDir()
	if err != nil {
		return refuse("start", "couldn't create a directory for the removal helper: "+err.Error())
	}
	script := "MODE=self\nGO=" + hostops.ShQuote(filepath.Join(dir, "go")) + "\nDIR=" + hostops.ShQuote(dir) + "\n" + agentrm.Script
	path := filepath.Join(dir, "remove.sh")
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		os.RemoveAll(dir)
		return refuse("start", "couldn't write the removal helper: "+err.Error())
	}
	how, err := start(path)
	if err != nil {
		os.RemoveAll(dir)
		return refuse("start", "couldn't start the removal helper: "+err.Error())
	}
	a.removal = &removal{requestID: m.RequestId, how: how, dir: dir, proceed: make(chan struct{})}
	res.Accepted, res.Helper = true, how
	return res
}

// removing reports whether this agent is removing itself.
func (a *Agent) removing() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.removal != nil
}

// removalDir creates the helper's directory, readable by root only, outside
// everything the helper removes: /run (or /var/tmp without it).
func (a *Agent) removalDir() (string, error) {
	if a.opt.RemovalDir != "" {
		return os.MkdirTemp(a.opt.RemovalDir, "drool-shepherd-removal.")
	}
	d, err := os.MkdirTemp("/run", "drool-shepherd-removal.")
	if err != nil {
		d, err = os.MkdirTemp("/var/tmp", "drool-shepherd-removal.")
	}
	return d, err
}

// startRemoval starts the helper outside the agent: a transient systemd
// unit (stopping drool-agent.service doesn't stop it), or, without
// systemd, a process in its own session (killing the agent and its
// supervisor doesn't kill it). Only an agent installed the standard way,
// running as root, removes itself: the helper removes the standard paths,
// and an agent with other paths (tests, unprivileged runs) must never
// touch them, nor ask the machine's systemd for anything.
func (a *Agent) startRemoval(path string) (string, error) {
	if err := a.mayRemoveItself(); err != nil {
		return "", err
	}
	if a.supervisor() == "systemd" {
		unit := "drool-shepherd-removal-" + randomID()[:8]
		out, err := exec.Command("systemd-run", "--quiet", "--collect", "--unit="+unit, "--property=Type=exec", "/bin/sh", path).CombinedOutput()
		if err != nil {
			return "", fmt.Errorf("systemd-run: %v: %s", err, strings.TrimSpace(string(out)))
		}
		return "systemd unit " + unit + ".service", nil
	}
	cmd := exec.Command("/bin/sh", path)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return "", err
	}
	pid := cmd.Process.Pid
	cmd.Process.Release()
	return fmt.Sprintf("process %d in its own session", pid), nil
}

// mayRemoveItself: only a Shepherd running as root from the standard
// places removes itself (its helper removes those places).
func (a *Agent) mayRemoveItself() error {
	if os.Geteuid() != 0 {
		return errors.New("this Shepherd doesn't run as root, so it can't remove itself")
	}
	if a.paths != Default() {
		return errors.New("this Shepherd isn't installed in the standard places, so it doesn't remove itself")
	}
	return nil
}
