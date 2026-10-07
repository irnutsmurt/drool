package agent

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"drool/internal/agentcaps"
	"drool/internal/agentrpc/pb"
	"drool/internal/release"

	"google.golang.org/grpc"
)

// releaseFixture signs a fake binary with a fresh key and trusts the key
// locally (as an administrator would with trust-release-key).
func releaseFixture(t *testing.T, a *Agent, m release.Manifest, bin []byte, trust bool) *pb.UpdateOffer {
	t.Helper()
	pub, privPEM, _ := release.GenerateKey()
	priv, _ := release.ParsePrivateKey(privPEM)
	if m.OS == "" {
		m.OS, m.Arch = runtime.GOOS, runtime.GOARCH
	}
	if m.ProtocolMin == 0 {
		m.ProtocolMin, m.ProtocolMax = 1, 1
	}
	s, err := release.Sign(priv, m, bytes.NewReader(bin))
	if err != nil {
		t.Fatal(err)
	}
	if trust {
		os.MkdirAll(a.paths.ReleaseKeys, 0o755)
		os.WriteFile(filepath.Join(a.paths.ReleaseKeys, release.KeyID(pub)+".pub"), []byte(release.FormatPublicKey(pub, "test")+"\n"), 0o644)
	}
	return &pb.UpdateOffer{ReleaseId: "arel_1", ManifestPayload: s.Payload, KeyId: s.KeyID, Signature: s.Signature}
}

func TestVerifyOffer(t *testing.T) {
	full, _ := agentcaps.Preset(agentcaps.PresetFull)
	a := testAgent(t, full)
	bin := []byte("new agent binary")

	o := releaseFixture(t, a, release.Manifest{Version: "v1.1.0"}, bin, true)
	if m, already, err := a.verifyOffer(o); err != nil || already || m.Version != "v1.1.0" {
		t.Fatalf("valid offer: %v %v %v", m, already, err)
	}
	// Signed by a key this agent doesn't trust: a controller without the
	// release key can't push code.
	o = releaseFixture(t, a, release.Manifest{Version: "v1.2.0"}, bin, false)
	if _, _, err := a.verifyOffer(o); err == nil || !strings.Contains(err.Error(), "does not trust") {
		t.Errorf("untrusted key: %v", err)
	}
	// Altered manifest.
	o = releaseFixture(t, a, release.Manifest{Version: "v1.2.0"}, bin, true)
	o.ManifestPayload = bytes.Replace(o.ManifestPayload, []byte(`"v1.2.0"`), []byte(`"v9.9.9"`), 1)
	if _, _, err := a.verifyOffer(o); err == nil {
		t.Error("altered manifest accepted")
	}
	// Wrong architecture.
	o = releaseFixture(t, a, release.Manifest{Version: "v1.2.0", OS: "linux", Arch: "riscv64"}, bin, true)
	if _, _, err := a.verifyOffer(o); err == nil || !strings.Contains(err.Error(), "riscv64") {
		t.Errorf("wrong arch: %v", err)
	}
	// Incompatible protocol with the controller.
	a.protocol = 1
	o = releaseFixture(t, a, release.Manifest{Version: "v1.2.0", ProtocolMin: 2, ProtocolMax: 3}, bin, true)
	if _, _, err := a.verifyOffer(o); err == nil || !strings.Contains(err.Error(), "protocol") {
		t.Errorf("incompatible protocol: %v", err)
	}
	// Downgrades are refused by default.
	o = releaseFixture(t, a, release.Manifest{Version: "v0.9.0"}, bin, true)
	if _, _, err := a.verifyOffer(o); err == nil || !strings.Contains(err.Error(), "downgrade") {
		t.Errorf("downgrade: %v", err)
	}
	// Same version: nothing to do.
	o = releaseFixture(t, a, release.Manifest{Version: "v1.0.0"}, bin, true)
	if _, already, err := a.verifyOffer(o); err != nil || !already {
		t.Errorf("same version: %v %v", already, err)
	}
	// A version that already failed here isn't retried automatically.
	a.paths.markFailed("v1.3.0")
	o = releaseFixture(t, a, release.Manifest{Version: "v1.3.0"}, bin, true)
	if _, _, err := a.verifyOffer(o); err == nil || !strings.Contains(err.Error(), "already failed") {
		t.Errorf("failed before: %v", err)
	}
}

// updateFixture lays out versions/v1 (current) and versions/v2 (staged),
// and a fake running agent whose pid the helper signals.
func updateFixture(t *testing.T) (Paths, *exec.Cmd) {
	t.Helper()
	p := Under(t.TempDir())
	for _, v := range []string{"v1", "v2"} {
		os.MkdirAll(p.versionDir(v), 0o755)
		os.WriteFile(p.binary(v), []byte("#!/bin/sh\n"), 0o755)
	}
	os.MkdirAll(p.State, 0o700)
	if err := p.setLink("current", "v1"); err != nil {
		t.Fatal(err)
	}
	agentProc := exec.Command("sleep", "60")
	agentProc.Start()
	t.Cleanup(func() { agentProc.Process.Kill(); agentProc.Wait() })
	os.WriteFile(filepath.Join(p.State, "agent.pid"), []byte(strconv.Itoa(agentProc.Process.Pid)), 0o600)
	return p, agentProc
}

func readOutcome(t *testing.T, p Paths) outcome {
	t.Helper()
	var o outcome
	if err := readJSONFile(filepath.Join(p.updDir(), "result.json"), &o); err != nil {
		t.Fatalf("no outcome: %v", err)
	}
	return o
}

func TestApplyUpdateHealthy(t *testing.T) {
	p, agentProc := updateFixture(t)
	go func() {
		// The new agent connects and writes its marker.
		time.Sleep(500 * time.Millisecond)
		writeFileAtomic(filepath.Join(p.updDir(), "healthy-v2"), []byte("ok"), 0o600)
	}()
	if err := ApplyUpdate(HelperOptions{Paths: p, ReleaseID: "r", From: "v1", To: "v2", Window: 5 * time.Second, Supervisor: "exit"}); err != nil {
		t.Fatal(err)
	}
	if p.linkedVersion("current") != "v2" || p.linkedVersion("previous") != "v1" {
		t.Fatalf("links: current %s previous %s", p.linkedVersion("current"), p.linkedVersion("previous"))
	}
	if o := readOutcome(t, p); o.State != UpdHealthy {
		t.Fatalf("outcome %+v", o)
	}
	if _, err := p.loadPending(); err == nil {
		t.Error("pending update left behind")
	}
	// The old agent process was told to restart.
	done := make(chan error, 1)
	go func() { done <- agentProc.Wait() }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Error("the running agent was not restarted")
	}
}

func TestApplyUpdateRollsBack(t *testing.T) {
	p, _ := updateFixture(t)
	start := time.Now()
	ApplyUpdate(HelperOptions{Paths: p, ReleaseID: "r", From: "v1", To: "v2", Window: time.Second, Supervisor: "exit"})
	if time.Since(start) < time.Second {
		t.Error("did not wait for the health window")
	}
	if p.linkedVersion("current") != "v1" {
		t.Fatalf("not rolled back: current %s", p.linkedVersion("current"))
	}
	o := readOutcome(t, p)
	if o.State != UpdRolledBack || !strings.Contains(o.Detail, "did not become healthy") {
		t.Fatalf("outcome %+v", o)
	}
	if !p.failedBefore("v2") {
		t.Error("failed version not remembered")
	}
	// Both complete binaries are still there.
	for _, v := range []string{"v1", "v2"} {
		if _, err := os.Stat(p.binary(v)); err != nil {
			t.Errorf("binary %s: %v", v, err)
		}
	}
}

func TestGuardCrashLoopAndInterruptions(t *testing.T) {
	logf := func(string, ...any) {}

	// A new version that keeps crashing: the guard (old binary, run before
	// every start) rolls back after maxStartAttempts starts.
	p, _ := updateFixture(t)
	p.setLink("previous", "v1")
	p.setLink("current", "v2")
	p.savePending(&pending{ReleaseID: "r", From: "v1", To: "v2", Switched: true, Deadline: time.Now().Add(time.Hour),
		HelperPID: os.Getpid(), Supervisor: "exit"})
	for i := 0; i < maxStartAttempts; i++ {
		if err := Guard(p, logf); err != nil {
			t.Fatal(err)
		}
		if p.linkedVersion("current") != "v2" {
			t.Fatalf("rolled back too early (start %d)", i+1)
		}
	}
	Guard(p, logf)
	if p.linkedVersion("current") != "v1" {
		t.Fatal("crash loop not rolled back")
	}
	if o := readOutcome(t, p); o.State != UpdRolledBack {
		t.Fatalf("outcome %+v", o)
	}

	// Power loss before the switch: the old version is still current.
	p, _ = updateFixture(t)
	p.savePending(&pending{ReleaseID: "r", From: "v1", To: "v2", Switched: false, Deadline: time.Now().Add(time.Hour), Supervisor: "exit"})
	Guard(p, logf)
	if p.linkedVersion("current") != "v1" {
		t.Fatal("current changed")
	}
	if o := readOutcome(t, p); o.State != UpdFailed || !strings.Contains(o.Detail, "before switching") {
		t.Fatalf("outcome %+v", o)
	}
	if _, err := p.loadPending(); err == nil {
		t.Error("pending not cleared")
	}

	// Power loss after the switch, past the deadline (the helper is gone):
	// rolled back at the next boot.
	p, _ = updateFixture(t)
	p.setLink("previous", "v1")
	p.setLink("current", "v2")
	p.savePending(&pending{ReleaseID: "r", From: "v1", To: "v2", Switched: true, Deadline: time.Now().Add(-time.Minute), Supervisor: "exit"})
	Guard(p, logf)
	if p.linkedVersion("current") != "v1" {
		t.Fatal("overdue update not rolled back at boot")
	}

	// The new version wrote its marker before the reboot: committed.
	p, _ = updateFixture(t)
	p.setLink("previous", "v1")
	p.setLink("current", "v2")
	os.MkdirAll(p.updDir(), 0o700)
	writeFileAtomic(filepath.Join(p.updDir(), "healthy-v2"), []byte("ok"), 0o600)
	p.savePending(&pending{ReleaseID: "r", From: "v1", To: "v2", Switched: true, Deadline: time.Now().Add(-time.Minute), Supervisor: "exit"})
	Guard(p, logf)
	if p.linkedVersion("current") != "v2" || readOutcome(t, p).State != UpdHealthy {
		t.Fatal("healthy update not committed by the guard")
	}

	// No pending update: the guard does nothing.
	p, _ = updateFixture(t)
	if err := Guard(p, logf); err != nil || p.linkedVersion("current") != "v1" {
		t.Fatalf("guard without an update: %v", err)
	}
}

func TestAtomicLinkSwitch(t *testing.T) {
	p, _ := updateFixture(t)
	// The switch is a rename of a symlink: at every moment `current` points
	// at one complete version directory.
	for i := 0; i < 50; i++ {
		v := []string{"v1", "v2"}[i%2]
		if err := p.setLink("current", v); err != nil {
			t.Fatal(err)
		}
		if got := p.linkedVersion("current"); got != v {
			t.Fatalf("current %s, want %s", got, v)
		}
	}
	if m, _ := filepath.Glob(filepath.Join(p.Install, "current.tmp-*")); len(m) != 0 {
		t.Errorf("temporary links left: %v", m)
	}
}

// fakeReleaseClient serves a release from an offset and can cut the stream
// after a number of bytes, like a dropped connection.
type fakeReleaseClient struct {
	pb.AgentClient
	data    []byte
	cutAt   int // stop with an error after this many bytes (0: never)
	offsets []int64
}

type fakeChunks struct {
	grpc.ClientStream
	c   *fakeReleaseClient
	pos int
}

func (f *fakeReleaseClient) FetchRelease(_ context.Context, in *pb.FetchReleaseRequest, _ ...grpc.CallOption) (grpc.ServerStreamingClient[pb.Chunk], error) {
	f.offsets = append(f.offsets, in.Offset)
	return &fakeChunks{c: f, pos: int(in.Offset)}, nil
}

func (s *fakeChunks) Recv() (*pb.Chunk, error) {
	if s.c.cutAt > 0 && s.pos >= s.c.cutAt {
		s.c.cutAt = 0
		return nil, errors.New("connection reset")
	}
	if s.pos >= len(s.c.data) {
		return nil, io.EOF
	}
	end := min(s.pos+7, len(s.c.data))
	if s.c.cutAt > 0 {
		end = min(end, s.c.cutAt)
	}
	ch := &pb.Chunk{Data: s.c.data[s.pos:end]}
	s.pos = end
	return ch, nil
}

func TestStageResumesInterruptedDownload(t *testing.T) {
	full, _ := agentcaps.Preset(agentcaps.PresetFull)
	a := testAgent(t, full)
	// A "binary" whose preflight (`version`) works.
	bin := []byte("#!/bin/sh\necho v1.1.0\n# padding to make several chunks\n")
	o := releaseFixture(t, a, release.Manifest{Version: "v1.1.0"}, bin, true)
	m, _, err := a.verifyOffer(o)
	if err != nil {
		t.Fatal(err)
	}
	fc := &fakeReleaseClient{data: bin, cutAt: 20}
	s := &session{client: fc}
	if err := a.stage(context.Background(), s, "arel_1", m); err == nil || !strings.Contains(err.Error(), "will resume") {
		t.Fatalf("interrupted download: %v", err)
	}
	if _, err := os.Stat(a.paths.binary("v1.1.0")); err == nil {
		t.Fatal("a partial download was installed as a version")
	}
	if err := a.stage(context.Background(), s, "arel_1", m); err != nil {
		t.Fatalf("resumed download: %v", err)
	}
	if len(fc.offsets) != 2 || fc.offsets[1] != 20 {
		t.Errorf("second request should resume at byte 20: %v", fc.offsets)
	}
	got, _ := os.ReadFile(a.paths.binary("v1.1.0"))
	if !bytes.Equal(got, bin) {
		t.Fatal("staged binary differs")
	}

	// A corrupted partial file: the result fails the signed hash, nothing
	// is installed, and the partial is discarded so the next try starts over.
	bin2 := []byte("#!/bin/sh\necho v1.2.0\n# another build of the agent here\n")
	o = releaseFixture(t, a, release.Manifest{Version: "v1.2.0"}, bin2, true)
	m2, _, _ := a.verifyOffer(o)
	part := filepath.Join(a.paths.Install, "versions", ".partial", m2.Version+"-"+m2.SHA256[:16])
	os.MkdirAll(filepath.Dir(part), 0o700)
	os.WriteFile(part, []byte("#!/bin/sh\necho evil"), 0o700)
	fc2 := &fakeReleaseClient{data: bin2}
	if err := a.stage(context.Background(), &session{client: fc2}, "arel_2", m2); err == nil || !strings.Contains(err.Error(), "rejected") {
		t.Fatalf("corrupted partial: %v", err)
	}
	if _, err := os.Stat(a.paths.binary("v1.2.0")); err == nil {
		t.Fatal("a binary failing the hash was installed")
	}
	if _, err := os.Stat(part); err == nil {
		t.Fatal("the bad partial download was kept")
	}
	if err := a.stage(context.Background(), &session{client: fc2}, "arel_2", m2); err != nil {
		t.Fatalf("retry after discarding the partial: %v", err)
	}
}
