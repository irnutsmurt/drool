package release

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"strings"
	"testing"
)

func TestStampedVersion(t *testing.T) {
	for in, want := range map[string]string{
		"-s -w -X drool/internal/version.Version=v1.0.0 -X drool/internal/version.Commit=abc": "v1.0.0",
		"-X 'drool/internal/version.Version=v1.0.0-rc.1'":                                     "v1.0.0-rc.1",
		`-X "drool/internal/version.Version=v2.0.0"`:                                          "v2.0.0",
		"-X drool/internal/version.Version=v1 -X drool/internal/version.Version=v1.1.0":       "v1.1.0",
		"-s -w": "",
		"":      "",
	} {
		if got := stampedVersion(in); got != want {
			t.Errorf("stampedVersion(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestModuleVersion(t *testing.T) {
	for v, want := range map[string]string{
		"v1.0.0":      "v1.0.0",
		"v1.0.0-rc.1": "v1.0.0-rc.1",
		"v0.1.0-alpha.0.20261010221002-0bb9535166cc":       "",
		"v0.1.0-alpha.0.20261010221002-0bb9535166cc+dirty": "",
		"v1.0.1-0.20261010221002-0bb9535166cc":             "",
		"(devel)":                                          "",
		"v1.0.0+dirty":                                     "",
	} {
		if got := inspect(&debug.BuildInfo{Main: debug.Module{Version: v}}).Version; got != want {
			t.Errorf("module version %q: %q, want %q", v, got, want)
		}
	}
}

func TestCheckAgentBinary(t *testing.T) {
	agent := inspect(&debug.BuildInfo{Path: AgentPackage, Settings: []debug.BuildSetting{
		{Key: "GOOS", Value: "linux"}, {Key: "GOARCH", Value: "arm64"},
		{Key: "-ldflags", Value: "-s -w -X drool/internal/version.Version=v1.0.0"},
		{Key: "vcs.revision", Value: "0123"}, {Key: "vcs.modified", Value: "false"}}})
	if agent.OS != "linux" || agent.Arch != "arm64" || agent.Version != "v1.0.0" || agent.Revision != "0123" || agent.Modified {
		t.Fatalf("inspect: %+v", agent)
	}
	ok := []Manifest{{}, {Version: "v1.0.0", OS: "linux", Arch: "arm64"}}
	for _, m := range ok {
		if err := CheckAgentBinary(agent, m); err != nil {
			t.Errorf("%+v: %v", m, err)
		}
	}
	controller := agent
	controller.Package = "drool/cmd/drool"
	unstamped := agent
	unstamped.Version = ""
	if err := CheckAgentBinary(unstamped, Manifest{Version: "v1.0.0"}); err != nil {
		t.Errorf("an explicit version for a build that doesn't record one: %v", err)
	}
	for _, c := range []struct {
		b    BinaryInfo
		m    Manifest
		want string
	}{
		{controller, Manifest{}, "not a drool-agent build"},
		{unstamped, Manifest{}, "give it with --version"},
		{agent, Manifest{Version: "1.0.0"}, "reports version v1.0.0"},
		{agent, Manifest{Arch: "amd64"}, "linux/arm64, not"},
		{agent, Manifest{OS: "freebsd"}, "linux/arm64, not"},
	} {
		if err := CheckAgentBinary(c.b, c.m); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%+v with %+v: %v, want %q", c.b, c.m, err, c.want)
		}
	}
}

// A real cross-built agent: what a release manager signs on an amd64
// machine for arm64 hosts.
func TestInspectRealAgentBuild(t *testing.T) {
	if testing.Short() {
		t.Skip("builds drool-agent")
	}
	gobin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("no go toolchain on PATH")
	}
	out := filepath.Join(t.TempDir(), "drool-agent")
	cmd := exec.Command(gobin, "build", "-trimpath", "-ldflags", "-s -w -X drool/internal/version.Version=v9.8.7", "-o", out, "drool/cmd/drool-agent")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH=arm64")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, b)
	}
	b, err := Inspect(out)
	if err != nil {
		t.Fatal(err)
	}
	// -trimpath keeps -ldflags out of the build information, and an untagged
	// commit's module version is a pseudo-version: the version can't be
	// told, the platform can.
	if b.Package != AgentPackage || b.OS != "linux" || b.Arch != "arm64" || b.Version != "" {
		t.Fatalf("inspect: %+v", b)
	}
	if _, err := Inspect(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Error("a missing file inspected")
	}
}
