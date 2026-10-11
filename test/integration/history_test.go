//go:build integration

package integration

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// Older DROOL builds for upgrade and mixed-version qualification, compiled
// from this repository's history: `git archive` of the commit (nothing is
// written to the repository), stamped with the version `make` gave them.
type historic struct {
	Name string // for messages
	Rev  string // tag or commit
}

var (
	// The tagged Technical Alpha: schema 5, SSH only (no Shepherds).
	alphaBuild = historic{"v0.1.0-alpha", "v0.1.0-alpha"}
	// M15 and M16 on main: what homelabs dogfooding DROOL run.
	m15Build = historic{"M15", "f328ba8"}
	m16Build = historic{"M16", "0bb9535"}
)

var (
	historyMu   sync.Mutex
	historyDone = map[string]string{} // rev/pkg/arch → binary
)

// version is what `make` stamped into builds of the commit.
func (h historic) version(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("git", "-C", root, "describe", "--tags", "--match", "v[0-9]*", "--always", h.Rev).Output()
	if err != nil {
		t.Fatalf("git describe %s: %v", h.Rev, err)
	}
	return strings.TrimSpace(string(out))
}

// build compiles cmd/<pkg> of the commit for linux/<arch> (once per run).
func (h historic) build(t *testing.T, pkg, arch string) string {
	t.Helper()
	historyMu.Lock()
	defer historyMu.Unlock()
	key := h.Rev + "/" + pkg + "/" + arch
	if p, ok := historyDone[key]; ok {
		return p
	}
	full, err := exec.Command("git", "-C", root, "rev-parse", "--verify", h.Rev+"^{commit}").Output()
	if err != nil {
		t.Fatalf("%s: %v", h.Rev, err)
	}
	commit := strings.TrimSpace(string(full))
	dir := filepath.Join(os.TempDir(), "drool-it-history", commit)
	src := filepath.Join(dir, "src")
	if _, err := os.Stat(filepath.Join(src, "go.mod")); err != nil {
		os.RemoveAll(src)
		if err := os.MkdirAll(src, 0o755); err != nil {
			t.Fatal(err)
		}
		archive := exec.Command("sh", "-c", fmt.Sprintf("git -C %s archive %s | tar -x -C %s", shq(root), commit, shq(src)))
		if out, err := archive.CombinedOutput(); err != nil {
			t.Fatalf("git archive %s: %v\n%s", h.Rev, err, out)
		}
	}
	out := filepath.Join(dir, pkg+"-linux-"+arch)
	ver := h.version(t)
	build := exec.Command("go", "build", "-trimpath", "-buildvcs=false",
		"-ldflags", "-X drool/internal/version.Version="+ver+" -X drool/internal/version.Commit="+commit, "-o", out, "./cmd/"+pkg)
	build.Dir = src
	build.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH="+arch)
	if b, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build %s %s: %v\n%s", h.Name, pkg, err, b)
	}
	historyDone[key] = out
	t.Logf("built %s %s (%s) for linux/%s", h.Name, pkg, ver, arch)
	return out
}

// agentDirOf is a directory holding the commit's Shepherd binaries, as a
// controller of that version serves them to new hosts.
func (h historic) agentDirOf(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "agents")
	os.MkdirAll(dir, 0o755)
	for _, arch := range []string{"amd64", "arm64"} {
		b, err := os.ReadFile(h.build(t, "drool-agent", arch))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "drool-agent-linux-"+arch), b, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}
