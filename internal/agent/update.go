package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"drool/internal/agentrpc/pb"
	"drool/internal/release"
)

// Self-update (ARCHITECTURE-ALPHA §23.10).
//
// Layout under Paths.Install:
//
//	versions/<version>/drool-agent   complete, verified binaries (never edited in place)
//	current  -> versions/<new>       what the service starts
//	previous -> versions/<old>       the last known-good version
//
// The running (old) agent verifies the signed release itself, downloads
// and stages it, then starts a helper process from its own binary. The
// helper records the pending update, switches `current` atomically,
// restarts the service and waits for the new version's health marker,
// which the new agent writes only after the controller acknowledged it.
// Without the marker in time, the helper switches back and restarts the
// old version. A boot-time guard (also the old binary, via `previous`)
// finishes or rolls back an update interrupted by a reboot or power loss,
// and rolls back a new binary that keeps crashing. Rollback therefore
// never depends on code in the new binary.

// TrustedReleaseKeys returns the release keys this host trusts for its
// Shepherd's updates: the DROOL project's official keys, built in (unless
// official is false: agent.yaml updates.official: deny), plus those an
// administrator added locally in Paths.ReleaseKeys (root-owned files).
// Nothing the controller sends can change either.
func (p Paths) TrustedReleaseKeys(official bool) ([]release.PublicKey, error) {
	if err := release.OfficialKeysError(); err != nil {
		return nil, fmt.Errorf("built-in release keys: %w", err)
	}
	var keys []release.PublicKey
	if official {
		keys = release.OfficialKeys()
	}
	ents, err := os.ReadDir(p.ReleaseKeys)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	for _, e := range ents {
		if !strings.HasSuffix(e.Name(), ".pub") {
			continue
		}
		name := filepath.Join(p.ReleaseKeys, e.Name())
		fi, err := os.Stat(name)
		if err != nil {
			continue
		}
		if st, ok := fi.Sys().(*syscall.Stat_t); ok && os.Getuid() == 0 && (st.Uid != 0 || fi.Mode().Perm()&0o022 != 0) {
			return nil, fmt.Errorf("%s must be owned by root and not writable by others (it decides which agent binaries this host will run)", name)
		}
		b, err := os.ReadFile(name)
		if err != nil {
			return nil, err
		}
		k, err := release.ParsePublicKeys(b)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		keys = append(keys, k...)
	}
	return keys, nil
}

// Update states reported to the controller.
const (
	UpdStaged     = "staged"
	UpdUpdating   = "updating"
	UpdHealthy    = "healthy"
	UpdRolledBack = "rolled_back"
	UpdFailed     = "failed"
)

// pending is a started update (Paths.State/update/pending.json).
type pending struct {
	ReleaseID  string    `json:"release_id"`
	From       string    `json:"from"`
	To         string    `json:"to"`
	StartedAt  time.Time `json:"started_at"`
	Deadline   time.Time `json:"deadline"`
	Attempts   int       `json:"attempts"`   // service starts since the switch
	Switched   bool      `json:"switched"`   // current points at To
	HelperPID  int       `json:"helper_pid"` // the waiting helper
	Supervisor string    `json:"supervisor"` // systemd | exit
}

// outcome is the last finished update (result.json), reported in every
// hello until the next update.
type outcome struct {
	ReleaseID string    `json:"release_id"`
	From      string    `json:"from"`
	To        string    `json:"to"`
	State     string    `json:"state"`
	Detail    string    `json:"detail"`
	At        time.Time `json:"at"`
}

func (p Paths) updDir() string             { return filepath.Join(p.State, "update") }
func (p Paths) versionDir(v string) string { return filepath.Join(p.Install, "versions", v) }
func (p Paths) binary(v string) string     { return filepath.Join(p.versionDir(v), "drool-agent") }

func readJSONFile(name string, v any) error {
	b, err := os.ReadFile(name)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

func writeJSONFile(name string, v any) error {
	b, _ := json.MarshalIndent(v, "", "  ")
	return writeFileAtomic(name, b, 0o600)
}

func (p Paths) loadPending() (*pending, error) {
	var pd pending
	if err := readJSONFile(filepath.Join(p.updDir(), "pending.json"), &pd); err != nil {
		return nil, err
	}
	return &pd, nil
}

func (p Paths) savePending(pd *pending) error {
	return writeJSONFile(filepath.Join(p.updDir(), "pending.json"), pd)
}

func (p Paths) clearPending() error {
	err := os.Remove(filepath.Join(p.updDir(), "pending.json"))
	if errors.Is(err, os.ErrNotExist) {
		err = nil
	}
	syncDir(p.updDir())
	return err
}

// finish records the update's outcome, then removes pending.json. In this
// order a reader that sees no pending update always finds its outcome; a
// crash in between leaves pending.json, which the guard resolves again
// (rolling back twice is harmless).
func (p Paths) finish(o outcome) error {
	if err := p.saveOutcome(o); err != nil {
		return err
	}
	return p.clearPending()
}

func (p Paths) saveOutcome(o outcome) error {
	o.At = time.Now().UTC()
	if o.State == UpdRolledBack || o.State == UpdFailed {
		p.markFailed(o.To)
	}
	return writeJSONFile(filepath.Join(p.updDir(), "result.json"), o)
}

func (p Paths) markFailed(version string) {
	var failed []string
	readJSONFile(filepath.Join(p.updDir(), "failed.json"), &failed)
	for _, f := range failed {
		if f == version {
			return
		}
	}
	writeJSONFile(filepath.Join(p.updDir(), "failed.json"), append(failed, version))
}

func (p Paths) failedBefore(version string) bool {
	var failed []string
	readJSONFile(filepath.Join(p.updDir(), "failed.json"), &failed)
	for _, f := range failed {
		if f == version {
			return true
		}
	}
	return false
}

// link points name at target atomically (temporary symlink + rename).
func link(name, target string) error {
	tmp := name + ".tmp-" + strconv.Itoa(os.Getpid())
	os.Remove(tmp)
	if err := os.Symlink(target, tmp); err != nil {
		return err
	}
	if err := os.Rename(tmp, name); err != nil {
		os.Remove(tmp)
		return err
	}
	return syncDir(filepath.Dir(name))
}

// linkedVersion returns the version a symlink (current/previous) points at.
func (p Paths) linkedVersion(name string) string {
	t, err := os.Readlink(filepath.Join(p.Install, name))
	if err != nil {
		return ""
	}
	return filepath.Base(t)
}

func (p Paths) setLink(name, version string) error {
	return link(filepath.Join(p.Install, name), filepath.Join("versions", version))
}

// --- offer handling (old agent) ---

func (a *Agent) reportUpdate(s *session, st, releaseID, version, detail string) {
	if s == nil {
		return
	}
	s.send(&pb.AgentMessage{Msg: &pb.AgentMessage_Update{Update: &pb.UpdateReport{ReleaseId: releaseID, Version: version,
		State: st, Detail: detail, FromVersion: a.opt.Version}}})
}

// Refusal codes (pb.UpdateReport.refusal): the agent turned an offer down
// before changing anything, for a reason that is this host's, not the
// release's. The controller skips such a host instead of counting a failed
// update, so one host's local decision never pauses a fleet's rollout.
const (
	RefuseUntrusted    = "untrusted_key"  // signed by a key this host doesn't trust
	RefuseDenied       = "updates_denied" // agent.yaml updates.policy: deny
	RefuseFailedBefore = "failed_before"  // failed its health check here before
	RefuseDowngrade    = "downgrade"      // older than the running version
	RefuseUpgradePath  = "upgrade_path"   // needs a newer version to upgrade from
	RefuseProtocol     = "protocol"       // doesn't speak this session's protocol
	RefusePlatform     = "platform"       // for another OS or architecture
	RefuseBusy         = "busy"           // another update is in progress
)

// refusal is an offer refused before anything changed.
type refusal struct{ code, msg string }

func (r *refusal) Error() string { return r.msg }

func refuse(code, format string, args ...any) error {
	return &refusal{code: code, msg: fmt.Sprintf(format, args...)}
}

// handleUpdateOffer verifies, downloads, stages and applies an update.
func (a *Agent) handleUpdateOffer(ctx context.Context, s *session, o *pb.UpdateOffer) {
	fail := func(err error) {
		msg := err.Error()
		a.log.Warn("agent update refused", "release", o.ReleaseId, "reason", msg)
		a.oplog.Write(OpEvent{Event: "update", Controller: s.controller, Summary: "refused: " + msg, Outcome: "rejected"})
		r := &pb.UpdateReport{ReleaseId: o.ReleaseId, State: UpdFailed, Detail: msg, FromVersion: a.opt.Version}
		var rf *refusal
		if errors.As(err, &rf) {
			r.Refusal = rf.code
		}
		s.send(&pb.AgentMessage{Msg: &pb.AgentMessage_Update{Update: r}})
	}
	if a.file.Updates.Policy == "deny" {
		fail(refuse(RefuseDenied, "updates are disabled on this host (updates.policy: deny in agent.yaml)"))
		return
	}
	if _, err := a.paths.loadPending(); err == nil {
		fail(refuse(RefuseBusy, "another update is still in progress"))
		return
	}
	m, already, err := a.verifyOffer(o)
	if err != nil {
		fail(err)
		return
	}
	if already {
		a.reportUpdate(s, UpdHealthy, o.ReleaseId, m.Version, "already running this version")
		return
	}
	a.oplog.Write(OpEvent{Event: "update", Controller: s.controller, Summary: fmt.Sprintf("verified release %s (%s, key %s)", m.Version, m.SHA256[:16], o.KeyId)})
	if err := a.stage(ctx, s, o.ReleaseId, m); err != nil {
		fail(err)
		return
	}
	a.reportUpdate(s, UpdStaged, o.ReleaseId, m.Version, "")
	window := time.Duration(o.HealthWindowSeconds) * time.Second
	if window <= 0 {
		window = 2 * time.Minute
	}
	start := a.startHelper
	if a.opt.StartUpdate != nil {
		start = func(id, to string, w time.Duration) error { return a.opt.StartUpdate(id, a.opt.Version, to, w) }
	}
	if err := start(o.ReleaseId, m.Version, window); err != nil {
		fail(fmt.Errorf("could not start the update helper: %v", err))
		return
	}
	a.reportUpdate(s, UpdUpdating, o.ReleaseId, m.Version, "")
}

// verifyOffer is the agent's own verification of an update offer: the
// signature against its trusted release keys, platform, protocol,
// downgrade and upgrade-path rules. already reports the running version.
// A *refusal error is this host's reason not to take it; any other error
// is something wrong with the offer itself (an invalid signature, a
// malformed manifest).
func (a *Agent) verifyOffer(o *pb.UpdateOffer) (*release.Manifest, bool, error) {
	keys, err := a.paths.TrustedReleaseKeys(a.file.OfficialReleases())
	if err != nil {
		return nil, false, err
	}
	m, err := release.Verify(&release.Signed{Payload: o.ManifestPayload, KeyID: o.KeyId, Signature: o.Signature}, keys)
	if errors.Is(err, release.ErrUntrustedKey) {
		return nil, false, refuse(RefuseUntrusted, "release verification failed: %v", err)
	}
	if err != nil {
		return nil, false, fmt.Errorf("release verification failed: %w", err)
	}
	if m.OS != runtime.GOOS || m.Arch != runtime.GOARCH {
		return nil, false, refuse(RefusePlatform, "release is for %s/%s, this host is %s/%s", m.OS, m.Arch, runtime.GOOS, runtime.GOARCH)
	}
	if p := a.negotiated(); p != 0 && (p < m.ProtocolMin || p > m.ProtocolMax) {
		return nil, false, refuse(RefuseProtocol, "release speaks protocol %d–%d, but the controller uses %d with this agent", m.ProtocolMin, m.ProtocolMax, p)
	}
	switch c := release.CompareVersions(m.Version, a.opt.Version); {
	case c == 0:
		return m, true, nil
	case c < 0 && !a.file.Updates.AllowDowngrade:
		return nil, false, refuse(RefuseDowngrade, "release %s is older than the running %s; downgrades are refused (updates.allow_downgrade in agent.yaml)", m.Version, a.opt.Version)
	}
	if m.MinFrom != "" && release.CompareVersions(a.opt.Version, m.MinFrom) < 0 {
		return nil, false, refuse(RefuseUpgradePath, "release %s needs at least version %s to upgrade from (running %s)", m.Version, m.MinFrom, a.opt.Version)
	}
	// A version that failed here before is tried again only when a person
	// asked for it (the controller's Try again); never by itself.
	if a.paths.failedBefore(m.Version) && !o.Retry {
		return nil, false, refuse(RefuseFailedBefore, "version %s already failed its health check on this host; not trying it again automatically", m.Version)
	}
	return m, false, nil
}

// releaseTrust is what the hello says about this host's update trust: the
// controller offers only what the host would install, and can say why a
// host can't take an update before trying.
func (a *Agent) releaseTrust() *pb.ReleaseTrust {
	official := a.file.OfficialReleases()
	rt := &pb.ReleaseTrust{Official: official && len(release.OfficialKeys()) > 0, OfficialDenied: !official,
		UpdatesDenied: a.file.Updates.Policy == "deny", KeyIds: []string{}}
	if keys, err := a.paths.TrustedReleaseKeys(official); err == nil {
		for _, k := range keys {
			rt.KeyIds = append(rt.KeyIds, k.ID())
		}
	}
	return rt
}

func (a *Agent) negotiated() uint32 {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.protocol
}

// stage downloads the binary (resuming a partial download), verifies it
// against the signed manifest and moves it into versions/<v>/.
func (a *Agent) stage(ctx context.Context, s *session, releaseID string, m *release.Manifest) error {
	dst := a.paths.binary(m.Version)
	if f, err := os.Open(dst); err == nil {
		err := release.CheckBinary(m, f)
		f.Close()
		if err == nil {
			return nil // staged earlier
		}
		os.RemoveAll(a.paths.versionDir(m.Version))
	}
	partDir := filepath.Join(a.paths.Install, "versions", ".partial")
	if err := os.MkdirAll(partDir, 0o700); err != nil {
		return err
	}
	part := filepath.Join(partDir, m.Version+"-"+m.SHA256[:16])
	f, err := os.OpenFile(part, os.O_CREATE|os.O_RDWR, 0o700)
	if err != nil {
		return err
	}
	defer f.Close()
	off, _ := f.Seek(0, io.SeekEnd)
	if off > m.Size {
		f.Truncate(0)
		off, _ = f.Seek(0, io.SeekStart)
	}
	if off < m.Size {
		dctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
		defer cancel()
		st, err := s.client.FetchRelease(dctx, &pb.FetchReleaseRequest{ReleaseId: releaseID, Offset: off})
		if err != nil {
			return fmt.Errorf("download: %w", err)
		}
		for {
			c, err := st.Recv()
			if err == io.EOF {
				break
			}
			if err != nil {
				f.Sync()
				return fmt.Errorf("download interrupted (will resume): %w", err)
			}
			if off+int64(len(c.Data)) > m.Size {
				return errors.New("download is larger than the signed size")
			}
			if _, err := f.Write(c.Data); err != nil {
				return err
			}
			off += int64(len(c.Data))
		}
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if err := release.CheckBinary(m, f); err != nil {
		f.Close()
		os.Remove(part)
		return fmt.Errorf("downloaded binary rejected: %w", err)
	}
	f.Close()
	if err := os.MkdirAll(a.paths.versionDir(m.Version), 0o755); err != nil {
		return err
	}
	if err := os.Chmod(part, 0o755); err != nil {
		return err
	}
	if err := os.Rename(part, dst); err != nil {
		return err
	}
	syncDir(a.paths.versionDir(m.Version))
	// Preflight: the new binary must at least run and report its version.
	pctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(pctx, dst, "version").Output()
	if err != nil || strings.TrimSpace(string(out)) != m.Version {
		os.RemoveAll(a.paths.versionDir(m.Version))
		return fmt.Errorf("the new binary did not report version %s (got %q, %v)", m.Version, strings.TrimSpace(string(out)), err)
	}
	return nil
}

// selfBinary is the running (old, known-good) binary's real path.
func selfBinary() (string, error) {
	p, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(p)
}

func (a *Agent) supervisor() string {
	if a.opt.Supervisor != "" {
		return a.opt.Supervisor
	}
	if fi, err := os.Stat("/run/systemd/system"); err == nil && fi.IsDir() {
		return "systemd"
	}
	return "exit"
}

// startHelper launches `drool-agent update-apply` from the running binary,
// outside the agent service so it survives the restart.
func (a *Agent) startHelper(releaseID, to string, window time.Duration) error {
	self, err := selfBinary()
	if err != nil {
		return err
	}
	// A Go test binary running an agent in-process would start itself
	// again (all its tests) instead of a helper: tests use StartUpdate.
	if strings.HasSuffix(self, ".test") {
		return errors.New("refusing to start the update helper from a test binary")
	}
	args := []string{"update-apply", "--release", releaseID, "--from", a.opt.Version, "--to", to,
		"--window", window.String(), "--paths", a.paths.encode(), "--supervisor", a.supervisor()}
	if a.supervisor() == "systemd" {
		unit := "drool-agent-update-" + strings.ReplaceAll(to, ".", "-") + "-" + randomID()[:6]
		cmd := exec.Command("systemd-run", append([]string{"--quiet", "--collect", "--unit=" + unit, "--property=Type=exec", self}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("systemd-run: %v: %s", err, strings.TrimSpace(string(out)))
		}
		return nil
	}
	cmd := exec.Command(self, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	logf, _ := os.OpenFile(filepath.Join(a.paths.Log, "update-helper.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	cmd.Stdout, cmd.Stderr = logf, logf
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

// encode/decode pass Paths to the helper (tests use non-default paths).
func (p Paths) encode() string {
	return strings.Join([]string{p.Config, p.ReleaseKeys, p.State, p.Log, p.Install}, "\x1f")
}

// DecodePaths reverses Paths.encode.
func DecodePaths(s string) (Paths, error) {
	f := strings.Split(s, "\x1f")
	if len(f) != 5 {
		return Paths{}, errors.New("bad --paths")
	}
	return Paths{Config: f[0], ReleaseKeys: f[1], State: f[2], Log: f[3], Install: f[4]}, nil
}

// --- the new agent's part ---

// updateHealthy is called after the controller acknowledged the session:
// if this binary is a pending update's target, record that it is healthy.
// It reports whether an update of this version is waiting to be committed.
func (a *Agent) updateHealthy(ack *pb.HelloAck) bool {
	a.mu.Lock()
	a.protocol = ack.Protocol
	a.mu.Unlock()
	pd, err := a.paths.loadPending()
	if err != nil || pd.To != a.opt.Version {
		return false
	}
	marker := filepath.Join(a.paths.updDir(), "healthy-"+a.opt.Version)
	if err := writeFileAtomic(marker, []byte(time.Now().UTC().Format(time.RFC3339)), 0o600); err != nil {
		a.log.Error("write update health marker", "err", err)
		return false
	}
	a.log.Info("new agent version is healthy (connected and acknowledged by the controller)", "version", a.opt.Version)
	return true
}

// reportCommit waits for the helper or guard to commit the update (it
// removes pending.json and writes result.json) and reports the outcome on
// this session; the hello only carried "updating".
func (a *Agent) reportCommit(ctx context.Context, s *session) {
	t := time.NewTicker(500 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return // the next hello reports it
		case <-t.C:
		}
		if _, err := a.paths.loadPending(); err == nil {
			continue
		}
		if r := a.updateReport(); r != nil && r.Version == a.opt.Version {
			s.send(&pb.AgentMessage{Msg: &pb.AgentMessage_Update{Update: r}})
			return
		}
	}
}

// reportUpdateOutcome logs the last finished update when the agent starts.
func (a *Agent) reportUpdateOutcome() {
	var o outcome
	if readJSONFile(filepath.Join(a.paths.updDir(), "result.json"), &o) == nil && time.Since(o.At) < time.Hour {
		a.log.Info("last agent update", "to", o.To, "state", o.State, "detail", o.Detail)
		a.oplog.Write(OpEvent{Event: "update", Summary: fmt.Sprintf("%s → %s: %s (%s)", o.From, o.To, o.State, o.Detail)})
	}
}

func (a *Agent) updateReport() *pb.UpdateReport {
	if pd, err := a.paths.loadPending(); err == nil {
		return &pb.UpdateReport{ReleaseId: pd.ReleaseID, Version: pd.To, FromVersion: pd.From, State: UpdUpdating}
	}
	var o outcome
	if err := readJSONFile(filepath.Join(a.paths.updDir(), "result.json"), &o); err != nil {
		return nil
	}
	return &pb.UpdateReport{ReleaseId: o.ReleaseID, Version: o.To, FromVersion: o.From, State: o.State, Detail: o.Detail}
}

// --- helper and guard (always the old binary) ---

// HelperOptions are update-apply's arguments.
type HelperOptions struct {
	Paths      Paths
	ReleaseID  string
	From, To   string
	Window     time.Duration
	Supervisor string
	Watch      bool // guard-respawned: the switch already happened
	Log        func(format string, args ...any)
}

// restartAgent restarts the agent service.
func restartAgent(p Paths, supervisor string) error {
	if supervisor == "systemd" {
		out, err := exec.Command("systemctl", "restart", "--no-block", "drool-agent.service").CombinedOutput()
		if err != nil {
			return fmt.Errorf("systemctl restart: %v: %s", err, strings.TrimSpace(string(out)))
		}
		return nil
	}
	// exit mode: signal the agent; its supervisor starts `current` again.
	b, err := os.ReadFile(filepath.Join(p.State, "agent.pid"))
	if err != nil {
		return err
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	if pid <= 1 {
		return errors.New("no agent pid")
	}
	return syscall.Kill(pid, syscall.SIGTERM)
}

// ApplyUpdate is the helper: switch, restart, wait for health, roll back
// on timeout. It runs from the old binary.
func ApplyUpdate(o HelperOptions) error {
	p := o.Paths
	logf := o.Log
	if logf == nil {
		logf = func(string, ...any) {}
	}
	if err := os.MkdirAll(p.updDir(), 0o700); err != nil {
		return err
	}
	pd, err := p.loadPending()
	if !o.Watch || err != nil {
		pd = &pending{ReleaseID: o.ReleaseID, From: o.From, To: o.To, StartedAt: time.Now().UTC(),
			Deadline: time.Now().Add(o.Window).UTC(), Supervisor: o.Supervisor}
	}
	pd.HelperPID = os.Getpid()
	if err := p.savePending(pd); err != nil {
		return err
	}
	if !pd.Switched {
		if _, err := os.Stat(p.binary(pd.To)); err != nil {
			p.finish(outcome{ReleaseID: pd.ReleaseID, From: pd.From, To: pd.To, State: UpdFailed, Detail: "staged binary missing"})
			return err
		}
		os.Remove(filepath.Join(p.updDir(), "healthy-"+pd.To))
		if err := p.setLink("previous", pd.From); err != nil {
			return err
		}
		pd.Switched = true // recorded before the switch: the guard checks `current` itself
		if err := p.savePending(pd); err != nil {
			return err
		}
		if err := p.setLink("current", pd.To); err != nil {
			return err
		}
		logf("switched current to %s (previous: %s)", pd.To, pd.From)
		if err := restartAgent(p, pd.Supervisor); err != nil {
			logf("restart failed: %v", err)
		}
	}
	marker := filepath.Join(p.updDir(), "healthy-"+pd.To)
	for time.Now().Before(pd.Deadline) {
		if _, err := p.loadPending(); err != nil {
			return nil // the boot guard finished or rolled back the update
		}
		if _, err := os.Stat(marker); err == nil {
			p.finish(outcome{ReleaseID: pd.ReleaseID, From: pd.From, To: pd.To, State: UpdHealthy,
				Detail: fmt.Sprintf("running %s; %s kept for rollback", pd.To, pd.From)})
			logf("update to %s is healthy", pd.To)
			return nil
		}
		time.Sleep(time.Second)
	}
	return rollback(p, pd, fmt.Sprintf("%s did not become healthy within %s; rolled back to %s", pd.To, pd.Deadline.Sub(pd.StartedAt).Round(time.Second), pd.From), logf)
}

func rollback(p Paths, pd *pending, detail string, logf func(string, ...any)) error {
	if err := p.setLink("current", pd.From); err != nil {
		return err
	}
	p.finish(outcome{ReleaseID: pd.ReleaseID, From: pd.From, To: pd.To, State: UpdRolledBack, Detail: detail})
	logf("rolled back: %s", detail)
	return restartAgent(p, pd.Supervisor)
}

// maxStartAttempts is how many service starts a new version gets before
// the guard rolls back (crash loops).
const maxStartAttempts = 3

// Guard runs before every agent start (systemd ExecStartPre, from the
// `previous` binary). Without a pending update it does nothing. Otherwise
// it counts starts, rolls back a crash-looping or overdue new version, and
// makes sure a helper is waiting for the health marker (after a reboot the
// original helper is gone).
func Guard(p Paths, logf func(string, ...any)) error {
	pd, err := p.loadPending()
	if err != nil {
		return nil // nothing pending
	}
	if !pd.Switched || p.linkedVersion("current") == pd.From {
		// Interrupted before the switch: the old version is still current.
		return p.finish(outcome{ReleaseID: pd.ReleaseID, From: pd.From, To: pd.To, State: UpdFailed, Detail: "the update was interrupted before switching versions; still running " + pd.From})
	}
	if _, err := os.Stat(filepath.Join(p.updDir(), "healthy-"+pd.To)); err == nil {
		return p.finish(outcome{ReleaseID: pd.ReleaseID, From: pd.From, To: pd.To, State: UpdHealthy, Detail: "running " + pd.To})
	}
	pd.Attempts++
	p.savePending(pd)
	if pd.Attempts > maxStartAttempts || time.Now().After(pd.Deadline) {
		detail := fmt.Sprintf("%s did not become healthy (started %d times); rolled back to %s", pd.To, pd.Attempts-1, pd.From)
		if err := p.setLink("current", pd.From); err != nil {
			return err
		}
		logf("guard: %s", detail)
		return p.finish(outcome{ReleaseID: pd.ReleaseID, From: pd.From, To: pd.To, State: UpdRolledBack, Detail: detail})
	}
	if pd.HelperPID > 0 && syscall.Kill(pd.HelperPID, 0) == nil {
		return nil // the helper is still waiting
	}
	// Re-arm a waiting helper from this (old) binary.
	self, err := selfBinary()
	if err != nil {
		return err
	}
	args := []string{"update-apply", "--watch", "--release", pd.ReleaseID, "--from", pd.From, "--to", pd.To,
		"--paths", p.encode(), "--supervisor", pd.Supervisor}
	var cmd *exec.Cmd
	if pd.Supervisor == "systemd" {
		cmd = exec.Command("systemd-run", append([]string{"--quiet", "--collect", "--unit=drool-agent-update-watch-" + randomID()[:6], "--property=Type=exec", self}, args...)...)
		return cmd.Run()
	}
	cmd = exec.Command(self, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

// WritePID records the agent's pid for exit-mode restarts.
func (p Paths) WritePID() error {
	return writeFileAtomic(filepath.Join(p.State, "agent.pid"), []byte(strconv.Itoa(os.Getpid())), 0o600)
}
