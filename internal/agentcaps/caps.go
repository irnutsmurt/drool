// Package agentcaps defines the agent's capability ceiling (DESIGN §6.7,
// ARCHITECTURE-ALPHA §23.6):
//
//	local ceiling (root-owned /etc/drool/agent.yaml, changed only locally)
//	  ∩ controller narrowing (may narrow, never widen)
//	  = effective capabilities
//
// The agent enforces it; the controller only displays and narrows it.
package agentcaps

import (
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"slices"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Caps is a set of capabilities: the ceiling, a narrowing request, or the
// effective result. The zero value allows nothing.
type Caps struct {
	Monitoring bool     `json:"monitoring" yaml:"monitoring"`
	Shell      bool     `json:"shell" yaml:"shell"`
	Packages   Packages `json:"packages" yaml:"packages"`
	Services   Services `json:"services" yaml:"services"`
	Reboot     bool     `json:"reboot" yaml:"reboot"`
	Files      Files    `json:"files" yaml:"files"`
	Firewall   bool     `json:"firewall" yaml:"firewall"` // firewall transactions and set updates (M8)
	Users      bool     `json:"users" yaml:"users"`       // reserved: user management
}

// Packages: install/remove/upgrade and system updates.
type Packages struct {
	Enabled bool `json:"enabled" yaml:"enabled"`
	// Only packages from repositories already configured on the host: no
	// paths, URLs or controller-supplied repositories.
	ConfiguredRepositoriesOnly bool `json:"configured_repositories_only" yaml:"configured_repositories_only"`
}

// Services: systemd service operations. An empty Allowed list with
// Enabled means every service.
type Services struct {
	Enabled bool     `json:"enabled" yaml:"enabled"`
	Allowed []string `json:"allowed,omitempty" yaml:"allowed,omitempty"`
}

// Files: the file manager and file deployment. An empty AllowedPaths list
// with Enabled means the whole filesystem (root-equivalent).
type Files struct {
	Enabled      bool     `json:"enabled" yaml:"enabled"`
	AllowedPaths []string `json:"allowed_paths,omitempty" yaml:"allowed_paths,omitempty"`
	// Validation commands for file deployments run as root. Without the
	// shell capability only these exact templates are allowed.
	AllowedValidationCommands []string `json:"allowed_validation_commands,omitempty" yaml:"allowed_validation_commands,omitempty"`
}

// UnmarshalYAML accepts `packages: true|false` or a mapping.
func (p *Packages) UnmarshalYAML(n *yaml.Node) error {
	var b bool
	if n.Decode(&b) == nil {
		*p = Packages{Enabled: b}
		return nil
	}
	type plain Packages
	v := plain{Enabled: true}
	if err := n.Decode(&v); err != nil {
		return err
	}
	*p = Packages(v)
	return nil
}

// UnmarshalYAML accepts `services: true|false` or a mapping with allowed.
func (s *Services) UnmarshalYAML(n *yaml.Node) error {
	var b bool
	if n.Decode(&b) == nil {
		*s = Services{Enabled: b}
		return nil
	}
	type plain Services
	v := plain{Enabled: true}
	if err := n.Decode(&v); err != nil {
		return err
	}
	*s = Services(v)
	return nil
}

// UnmarshalYAML accepts `files: true|false` or a mapping.
func (f *Files) UnmarshalYAML(n *yaml.Node) error {
	var b bool
	if n.Decode(&b) == nil {
		*f = Files{Enabled: b}
		return nil
	}
	type plain Files
	v := plain{Enabled: true}
	if err := n.Decode(&v); err != nil {
		return err
	}
	*f = Files(v)
	return nil
}

// Normalize cleans paths and unit names and validates the result.
func (c *Caps) Normalize() error {
	for i, p := range c.Files.AllowedPaths {
		if !strings.HasPrefix(p, "/") {
			return fmt.Errorf("files.allowed_paths: %q must be absolute", p)
		}
		c.Files.AllowedPaths[i] = path.Clean(p)
	}
	c.Files.AllowedPaths = dedupe(c.Files.AllowedPaths)
	for i, u := range c.Services.Allowed {
		c.Services.Allowed[i] = UnitName(u)
	}
	c.Services.Allowed = dedupe(c.Services.Allowed)
	if !c.Packages.Enabled {
		c.Packages.ConfiguredRepositoriesOnly = false
	}
	if !c.Services.Enabled {
		c.Services.Allowed = nil
	}
	if !c.Files.Enabled {
		c.Files.AllowedPaths, c.Files.AllowedValidationCommands = nil, nil
	}
	return nil
}

func dedupe(s []string) []string {
	if len(s) == 0 {
		return nil
	}
	sort.Strings(s)
	return slices.Compact(s)
}

// UnitName adds .service to a bare unit name.
func UnitName(u string) string {
	u = strings.TrimSpace(u)
	if u != "" && !strings.Contains(u, ".") {
		u += ".service"
	}
	return u
}

// --- intersection ---

// Intersect returns ceiling ∩ requested. Narrowing can only remove
// authority: anything the ceiling doesn't grant stays denied whatever the
// request says.
func Intersect(ceiling, req Caps) Caps {
	ceiling, req = ceiling.clone(), req.clone()
	ceiling.Normalize()
	req.Normalize()
	out := Caps{
		Monitoring: ceiling.Monitoring && req.Monitoring,
		Shell:      ceiling.Shell && req.Shell,
		Reboot:     ceiling.Reboot && req.Reboot,
		Firewall:   ceiling.Firewall && req.Firewall,
		Users:      ceiling.Users && req.Users,
	}
	if ceiling.Packages.Enabled && req.Packages.Enabled {
		out.Packages = Packages{Enabled: true,
			ConfiguredRepositoriesOnly: ceiling.Packages.ConfiguredRepositoriesOnly || req.Packages.ConfiguredRepositoriesOnly}
	}
	if ceiling.Services.Enabled && req.Services.Enabled {
		out.Services.Enabled = true
		switch {
		case len(ceiling.Services.Allowed) == 0:
			out.Services.Allowed = slices.Clone(req.Services.Allowed)
		case len(req.Services.Allowed) == 0:
			out.Services.Allowed = slices.Clone(ceiling.Services.Allowed)
		default:
			for _, u := range req.Services.Allowed {
				if slices.Contains(ceiling.Services.Allowed, u) {
					out.Services.Allowed = append(out.Services.Allowed, u)
				}
			}
			if len(out.Services.Allowed) == 0 {
				out.Services.Enabled = false // the lists don't overlap
			}
		}
	}
	if ceiling.Files.Enabled && req.Files.Enabled {
		out.Files.Enabled = true
		switch {
		case len(ceiling.Files.AllowedPaths) == 0:
			out.Files.AllowedPaths = slices.Clone(req.Files.AllowedPaths)
		case len(req.Files.AllowedPaths) == 0:
			out.Files.AllowedPaths = slices.Clone(ceiling.Files.AllowedPaths)
		default:
			for _, a := range ceiling.Files.AllowedPaths {
				for _, b := range req.Files.AllowedPaths {
					switch {
					case Within(b, a):
						out.Files.AllowedPaths = append(out.Files.AllowedPaths, b)
					case Within(a, b):
						out.Files.AllowedPaths = append(out.Files.AllowedPaths, a)
					}
				}
			}
			if len(out.Files.AllowedPaths) == 0 {
				out.Files.Enabled = false
			}
		}
		// Validation templates are never widened by the controller.
		out.Files.AllowedValidationCommands = slices.Clone(ceiling.Files.AllowedValidationCommands)
	}
	out.Normalize()
	return out
}

func (c Caps) clone() Caps {
	c.Services.Allowed = slices.Clone(c.Services.Allowed)
	c.Files.AllowedPaths = slices.Clone(c.Files.AllowedPaths)
	c.Files.AllowedValidationCommands = slices.Clone(c.Files.AllowedValidationCommands)
	return c
}

// Within reports whether p is root or inside it (both cleaned, absolute).
func Within(p, root string) bool {
	if root == "/" {
		return strings.HasPrefix(p, "/")
	}
	return p == root || strings.HasPrefix(p, root+"/")
}

// --- root equivalence and presets ---

// RootEquivalent lists the capabilities that effectively grant root.
func (c Caps) RootEquivalent() []string {
	var out []string
	if c.Shell {
		out = append(out, "shell (commands and scripts run as root)")
	}
	if c.Files.Enabled && len(c.Files.AllowedPaths) == 0 {
		out = append(out, "files without allowed_paths (can rewrite /etc/sudoers, systemd units, ...)")
	}
	if c.Files.Enabled && slices.Contains(c.Files.AllowedPaths, "/") {
		out = append(out, "files with allowed path / (the whole filesystem)")
	}
	if c.Packages.Enabled && !c.Packages.ConfiguredRepositoriesOnly {
		out = append(out, "packages without configured_repositories_only (package scripts run as root)")
	}
	if c.Users {
		out = append(out, "users (can create administrators)")
	}
	return out
}

// Preset names (DESIGN §6.7).
const (
	PresetMonitor = "monitor"
	PresetManaged = "managed"
	PresetFull    = "full"
)

// Preset returns an installer preset's ceiling.
func Preset(name string) (Caps, error) {
	switch name {
	case PresetMonitor:
		return Caps{Monitoring: true}, nil
	case PresetManaged:
		return Caps{Monitoring: true, Services: Services{Enabled: true}, Reboot: true,
			Packages: Packages{Enabled: true, ConfiguredRepositoriesOnly: true}, Firewall: true}, nil
	case PresetFull:
		return Caps{Monitoring: true, Shell: true, Services: Services{Enabled: true}, Reboot: true,
			Packages: Packages{Enabled: true}, Files: Files{Enabled: true}, Firewall: true, Users: true}, nil
	}
	return Caps{}, fmt.Errorf("unknown preset %q (monitor, managed, full)", name)
}

// All grants everything; used as the default controller request (no narrowing).
func All() Caps {
	return Caps{Monitoring: true, Shell: true, Packages: Packages{Enabled: true}, Services: Services{Enabled: true},
		Reboot: true, Files: Files{Enabled: true}, Firewall: true, Users: true}
}

// --- configuration file ---

// File is /etc/drool/agent.yaml.
type File struct {
	Capabilities Caps `yaml:"capabilities"`
	// Optional settings that are local decisions too.
	Controller struct {
		// Extra endpoints to try (host:port), in addition to those learned
		// at enrollment.
		Endpoints []string `yaml:"endpoints"`
	} `yaml:"controller"`
	Updates struct {
		// Updates of the agent: "allow" (default) or "deny". Deny keeps the
		// agent on its installed version whatever the controller asks.
		Policy string `yaml:"policy"`
		// Allow installing an older release than the running one.
		AllowDowngrade bool `yaml:"allow_downgrade"`
		// Official DROOL releases, signed with the project's key built
		// into the agent: "allow" (default) or "deny". Deny trusts only the
		// keys in /etc/drool/release-keys.d (an operator's own builds).
		Official string `yaml:"official"`
	} `yaml:"updates"`
	Identity struct {
		// What to do when the controller detects this identity twice:
		// only "quarantine" is implemented (DESIGN §6.3).
		OnConflict string `yaml:"on_conflict"`
	} `yaml:"identity"`
}

// OfficialReleases reports whether this host trusts the DROOL project's
// official release keys for the agent's updates (updates.official).
func (f *File) OfficialReleases() bool { return f.Updates.Official != "deny" }

// ParseFile parses agent.yaml strictly (unknown keys are errors, so a typo
// can't silently grant or deny something).
func ParseFile(data []byte) (*File, error) {
	var f File
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("agent.yaml: %w", err)
	}
	if err := f.Capabilities.Normalize(); err != nil {
		return nil, fmt.Errorf("agent.yaml: %w", err)
	}
	switch f.Updates.Policy {
	case "", "allow", "deny":
	default:
		return nil, fmt.Errorf("agent.yaml: updates.policy must be allow or deny")
	}
	switch f.Updates.Official {
	case "", "allow", "deny":
	default:
		return nil, fmt.Errorf("agent.yaml: updates.official must be allow or deny")
	}
	return &f, nil
}

// Render writes an agent.yaml for a ceiling, with comments that explain
// what root-equivalent means.
func Render(c Caps, preset string) []byte {
	var b strings.Builder
	b.WriteString("# DROOL agent capability ceiling (DESIGN §6.7).\n")
	b.WriteString("# This file is the upper limit of what the DROOL controller may do on this host.\n")
	b.WriteString("# The controller can narrow it but never widen it. Only root on this host can change it;\n")
	b.WriteString("# restart the agent afterwards (systemctl restart drool-agent).\n")
	if preset != "" {
		fmt.Fprintf(&b, "# Created from the %q preset.\n", preset)
	}
	if re := c.RootEquivalent(); len(re) > 0 {
		b.WriteString("#\n# ROOT-EQUIVALENT: with these, the controller can do anything root can on this host:\n")
		for _, r := range re {
			b.WriteString("#   - " + r + "\n")
		}
	}
	out, _ := yaml.Marshal(File{Capabilities: c})
	// Drop the empty optional sections yaml.Marshal adds.
	s := string(out)
	if i := strings.Index(s, "controller:"); i >= 0 {
		s = s[:i]
	}
	b.WriteString(s)
	return []byte(b.String())
}

// --- checks ---

// Error is a capability denial. It is never a reason to try another
// transport: the ceiling is the host owner's decision.
type Error struct {
	Capability string
	Message    string
}

func (e *Error) Error() string { return e.Message }

func deny(capability, format string, a ...any) error {
	return &Error{Capability: capability, Message: fmt.Sprintf(format, a...)}
}

var plainPackage = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9+._:@~=-]*$`)

// protected are the agent's own files: never reachable through the files
// capability, so the controller can't rewrite the ceiling, the agent's
// identity, its operation log or its binaries through file operations.
var protected = []string{"/etc/drool", "/var/lib/drool-agent", "/var/log/drool-agent", "/usr/libexec/drool-agent",
	// The firewall tooling's state and the boot loader (DESIGN §17.0): only
	// the firewall capability changes them, through transactions.
	"/var/lib/drool", "/usr/libexec/drool-firewall", "/etc/systemd/system/drool-firewall.service"}

// AllowPath checks a path against the files capability.
func (c Caps) AllowPath(p string) error {
	if !c.Files.Enabled {
		return deny("files", "the agent's capability ceiling does not allow file access on this host")
	}
	p = path.Clean(p)
	for _, pr := range protected {
		// Listing a parent like /etc is fine; anything inside the agent's
		// own directories is not.
		if Within(p, pr) {
			return deny("files", "%s belongs to the DROOL agent itself and is never accessible through file operations", p)
		}
	}
	if len(c.Files.AllowedPaths) == 0 {
		return nil
	}
	for _, a := range c.Files.AllowedPaths {
		if Within(p, a) {
			return nil
		}
	}
	return deny("files", "%s is outside this agent's allowed paths (%s)", p, strings.Join(c.Files.AllowedPaths, ", "))
}

// AllowWrite checks a path for a change: like AllowPath, and the path must
// not contain a protected directory (no recursive delete or rename of a
// parent of the agent's files).
func (c Caps) AllowWrite(p string) error {
	if err := c.AllowPath(p); err != nil {
		return err
	}
	p = path.Clean(p)
	for _, pr := range protected {
		if Within(pr, p) {
			return deny("files", "%s contains the DROOL agent's own files", p)
		}
	}
	return nil
}

// AllowMode refuses setuid/setgid bits unless file access is unrestricted
// (a restricted file capability must not mint setuid programs).
func (c Caps) AllowMode(mode uint32) error {
	if mode&0o6000 != 0 && len(c.Files.AllowedPaths) > 0 {
		return deny("files", "setuid/setgid permissions are not allowed with restricted file access")
	}
	return nil
}

// AllowValidation checks a file deployment's validation command.
func (c Caps) AllowValidation(cmd string) error {
	if cmd == "" || c.Shell || slices.Contains(c.Files.AllowedValidationCommands, cmd) {
		return nil
	}
	return deny("shell", "the validation command %q runs as root, which needs the shell capability or an entry in files.allowed_validation_commands", cmd)
}

// AllowService checks a systemd unit.
func (c Caps) AllowService(unit string) error {
	if !c.Services.Enabled {
		return deny("services", "the agent's capability ceiling does not allow service management on this host")
	}
	unit = UnitName(unit)
	if len(c.Services.Allowed) > 0 && !slices.Contains(c.Services.Allowed, unit) {
		return deny("services", "%s is not in this agent's allowed services (%s)", unit, strings.Join(c.Services.Allowed, ", "))
	}
	return nil
}

// AllowPackages checks package names.
func (c Caps) AllowPackages(names []string) error {
	if !c.Packages.Enabled {
		return deny("packages", "the agent's capability ceiling does not allow package management on this host")
	}
	if c.Packages.ConfiguredRepositoriesOnly {
		for _, n := range names {
			if !plainPackage.MatchString(n) {
				return deny("packages", "%q is not a plain package name; only packages from configured repositories are allowed", n)
			}
		}
	}
	return nil
}

// AllowShell checks commands and scripts.
func (c Caps) AllowShell() error {
	if !c.Shell {
		return deny("shell", "the agent's capability ceiling does not allow commands or scripts on this host")
	}
	return nil
}

// AllowReboot checks reboots.
// AllowFirewall checks firewall changes (transactions, set updates).
func (c Caps) AllowFirewall() error {
	if !c.Firewall {
		return deny("firewall", "the agent's capability ceiling does not allow firewall changes on this host")
	}
	return nil
}

// AllowMonitoring checks read-only inspection (firewall detection, status,
// connection snapshots).
func (c Caps) AllowMonitoring() error {
	if !c.Monitoring {
		return deny("monitoring", "the agent's capability ceiling does not allow monitoring on this host")
	}
	return nil
}

func (c Caps) AllowReboot() error {
	if !c.Reboot {
		return deny("reboot", "the agent's capability ceiling does not allow reboots on this host")
	}
	return nil
}
