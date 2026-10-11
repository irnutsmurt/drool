package version

import (
	"runtime/debug"
	"testing"
)

func TestBuildProvenance(t *testing.T) {
	full := "0bb9535c1f3e4a2b8d7c6e5f4a3b2c1d0e9f8a7b"
	for _, c := range []struct {
		name     string
		b        Build
		settings []debug.BuildSetting
		channel  string
		commit   string
		modified bool
	}{
		{"release build", Build{Version: "v1.0.0", Channel: Stable, Commit: full},
			[]debug.BuildSetting{{Key: "vcs.revision", Value: full}, {Key: "vcs.modified", Value: "false"}}, Stable, full, false},
		{"prerelease", Build{Version: "v1.0.0-rc.1", Channel: Prerelease, Commit: full}, nil, Prerelease, full, false},
		{"short stamped commit gets the full one", Build{Version: "v1.0.0", Channel: Stable, Commit: "0bb9535"},
			[]debug.BuildSetting{{Key: "vcs.revision", Value: full}}, Stable, full, false},
		{"unstamped commit from buildvcs", Build{Version: "v0.0.0-dev", Channel: Development, Commit: "unknown"},
			[]debug.BuildSetting{{Key: "vcs.revision", Value: full}}, Development, full, false},
		{"a modified tree is never a release", Build{Version: "v1.0.0", Channel: Stable, Commit: full},
			[]debug.BuildSetting{{Key: "vcs.revision", Value: full}, {Key: "vcs.modified", Value: "true"}}, Development, full, true},
		{"git describe --dirty", Build{Version: "v0.1.0-alpha-33-g0bb9535-dirty", Channel: Stable, Commit: "0bb9535"}, nil, Development, "0bb9535", true},
		{"unknown channel", Build{Version: "v1.0.0", Channel: "nightly", Commit: full}, nil, Development, full, false},
		{"a stamp that isn't the recorded revision", Build{Version: "v1.0.0", Channel: Stable, Commit: "abcdef0"},
			[]debug.BuildSetting{{Key: "vcs.revision", Value: full}}, Development, full, false},
	} {
		b := c.b
		b.apply(c.settings)
		b = b.normalize()
		if b.Channel != c.channel || b.Commit != c.commit || b.Modified != c.modified {
			t.Errorf("%s: channel %q commit %q modified %v; want %q %q %v", c.name, b.Channel, b.Commit, b.Modified, c.channel, c.commit, c.modified)
		}
	}
	if got := (Build{Commit: full}).ShortCommit(); got != full[:12] {
		t.Errorf("short commit %q", got)
	}
}
