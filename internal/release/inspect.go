package release

import (
	"debug/buildinfo"
	"errors"
	"fmt"
	"regexp"
	"runtime/debug"
	"strings"
)

// AgentPackage is the main package of a drool-agent binary.
const AgentPackage = "drool/cmd/drool-agent"

// BinaryInfo is what a Go binary records about its own build.
type BinaryInfo struct {
	Package  string // main package path
	OS, Arch string
	// Version is the version the binary reports, when its build records it:
	// the stamped drool/internal/version.Version (builds without -trimpath)
	// or, for a clean build at a release tag, the module version Go takes
	// from the tag. "" when it can't be told without running the binary.
	Version  string
	Revision string // source revision (buildvcs), when recorded
	Modified bool   // built from a tree with uncommitted changes
}

// Inspect reads a binary's build information without running it, so a
// release for another architecture can be checked on any machine.
func Inspect(path string) (BinaryInfo, error) {
	bi, err := buildinfo.ReadFile(path)
	if err != nil {
		return BinaryInfo{}, fmt.Errorf("%s: not a Go binary with build information: %w", path, err)
	}
	return inspect(bi), nil
}

// releaseVersion is a release tag's version as Go records it for the main
// module; pseudo-versions (untagged commits) and "+dirty" builds aren't.
var releaseVersion = regexp.MustCompile(`^v\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?$`)
var pseudoVersion = regexp.MustCompile(`\d{14}-[0-9a-f]{12}$`)

func inspect(bi *debug.BuildInfo) BinaryInfo {
	b := BinaryInfo{Package: bi.Path}
	if v := bi.Main.Version; releaseVersion.MatchString(v) && !pseudoVersion.MatchString(v) {
		b.Version = v
	}
	for _, s := range bi.Settings {
		switch s.Key {
		case "GOOS":
			b.OS = s.Value
		case "GOARCH":
			b.Arch = s.Value
		case "vcs.revision":
			b.Revision = s.Value
		case "vcs.modified":
			b.Modified = s.Value == "true"
		case "-ldflags": // not recorded with -trimpath
			if v := stampedVersion(s.Value); v != "" {
				b.Version = v
			}
		}
	}
	return b
}

// stampedVersion finds -X drool/internal/version.Version=V in ldflags
// (quoted or not; the last one wins, as with the linker).
func stampedVersion(ldflags string) string {
	const key = "drool/internal/version.Version="
	v := ""
	for rest := ldflags; ; {
		i := strings.Index(rest, key)
		if i < 0 {
			return v
		}
		rest = rest[i+len(key):]
		end := strings.IndexAny(rest, " \t'\"")
		if end < 0 {
			end = len(rest)
		}
		v = rest[:end]
	}
}

// CheckAgentBinary refuses to sign something an agent would reject after
// downloading it: another program, or a manifest whose version, OS or
// architecture isn't the binary's own (the agent runs the new binary's
// "version" before switching to it). Without a recorded version the
// manifest must name one.
func CheckAgentBinary(b BinaryInfo, m Manifest) error {
	switch {
	case b.Package != AgentPackage:
		return fmt.Errorf("this is %s, not a drool-agent build (%s)", b.Package, AgentPackage)
	case b.Version == "" && m.Version == "":
		return errors.New("this build doesn't record which version it reports: give it with --version (exactly what `drool-agent version` prints)")
	case b.Version != "" && m.Version != "" && m.Version != b.Version:
		return fmt.Errorf("the binary reports version %s, not %s: agents refuse a release whose binary reports another version", b.Version, m.Version)
	case m.OS != "" && b.OS != "" && m.OS != b.OS, m.Arch != "" && b.Arch != "" && m.Arch != b.Arch:
		return fmt.Errorf("the binary is for %s/%s, not %s/%s", b.OS, b.Arch, m.OS, m.Arch)
	}
	return nil
}
