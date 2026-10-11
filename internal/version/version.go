// Package version identifies a build. The release pipeline
// (packaging/release) stamps Version, Commit, Date and Channel with
// -ldflags; Go itself records the source revision and whether the tree had
// uncommitted changes (buildvcs) in every build made from a git checkout.
// The product version, the agent protocol and the database schema are
// versioned separately (ARCHITECTURE-ALPHA §34.2).
package version

import (
	"runtime"
	"runtime/debug"
	"strings"
)

var (
	// Version is the product version, "v"-prefixed SemVer for releases
	// (v1.0.0, v1.0.0-rc.1); git describe output for other builds.
	Version = "v0.0.0-dev"
	Commit  = "unknown"
	// Date is when the source was committed (release builds), RFC 3339.
	Date = "unknown"
	// Channel is "stable" (built from a vX.Y.Z tag), "prerelease" (a
	// vX.Y.Z-rc.N, -beta.N … tag) or "development" (anything else). Only
	// the release pipeline sets the first two.
	Channel = "development"
)

// Release channels.
const (
	Stable      = "stable"
	Prerelease  = "prerelease"
	Development = "development"
)

// Build describes this binary.
type Build struct {
	Version string `json:"version"`
	Channel string `json:"channel"`
	// Commit is the full source revision when it is known.
	Commit string `json:"commit"`
	// Modified: built from a tree with uncommitted changes. Such a build is
	// never a release, whatever it was stamped.
	Modified  bool   `json:"modified"`
	BuildDate string `json:"build_date"`
	// mismatch: the stamped commit isn't the revision the toolchain
	// recorded, so the stamp can't be trusted.
	mismatch  bool
	GoVersion string `json:"go_version"`
	Platform  string `json:"platform"` // linux/amd64
}

// Get describes this binary.
func Get() Build {
	b := Build{Version: Version, Channel: Channel, Commit: Commit, BuildDate: Date,
		GoVersion: runtime.Version(), Platform: runtime.GOOS + "/" + runtime.GOARCH}
	if bi, ok := debug.ReadBuildInfo(); ok {
		b.apply(bi.Settings)
	}
	return b.normalize()
}

func (b *Build) apply(settings []debug.BuildSetting) {
	for _, s := range settings {
		switch s.Key {
		case "vcs.revision":
			// The stamped commit may be abbreviated; the recorded one is full,
			// and it is what the binary was built from.
			if b.Commit != "" && b.Commit != "unknown" && !strings.HasPrefix(s.Value, b.Commit) {
				b.mismatch = true
			}
			b.Commit = s.Value
		case "vcs.modified":
			if s.Value == "true" {
				b.Modified = true
			}
		}
	}
}

func (b Build) normalize() Build {
	if strings.Contains(b.Version, "dirty") {
		b.Modified = true
	}
	if b.Modified || b.mismatch || b.Channel != Stable && b.Channel != Prerelease {
		b.Channel = Development
	}
	return b
}

// ShortCommit is the commit abbreviated for display.
func (b Build) ShortCommit() string {
	if len(b.Commit) > 12 {
		return b.Commit[:12]
	}
	return b.Commit
}

// Source says whether the build matches its commit.
func (b Build) Source() string {
	if b.Modified {
		return "modified (uncommitted changes; not a release)"
	}
	return "clean"
}
