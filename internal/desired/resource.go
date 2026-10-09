// Package desired is DROOL's desired state (M14): baselines of typed
// resources (systemd services, packages, managed files) applied globally,
// to groups or to hosts; continuous observation and comparison; drift
// that says exactly what differs; and three operator actions — Review
// difference (read-only), Accept current state (a host-specific
// declaration) and Restore managed state (ordinary typed jobs through the
// job engine, DESIGN §12.1). Automatic restore is optional and off by
// default.
//
// There is no expression language here: a resource is a typed value, and
// nothing in a baseline executes.
package desired

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"drool/internal/model"
)

// Resource kinds.
const (
	KindService = "service"
	KindPackage = "package"
	KindFile    = "file"
)

// Desired values.
const (
	StateRunning   = "running"
	StateStopped   = "stopped"
	StateInstalled = "installed"
	StateAbsent    = "absent"
)

// MaxResources bounds one baseline.
const MaxResources = 200

// Resource is one typed desired resource. Which fields apply depends on
// Kind:
//
//   - service: Name (a systemd .service unit), State running|stopped and/or
//     Enabled. A field left empty is not managed. Restart and reload are
//     actions, not states, and are never desired state.
//   - package: Name, State installed|absent. Versions are not managed.
//   - file: Path and the exact content (Artifact, the SHA-256 of a file in
//     the artifact store); Owner, Group and Mode when they are managed;
//     how it is deployed (Elevated, Validate, Service) as in a file
//     deployment (M6).
type Resource struct {
	Kind    string `json:"kind"`
	Name    string `json:"name,omitempty"`
	State   string `json:"state,omitempty"`
	Enabled *bool  `json:"enabled,omitempty"`

	Path string `json:"path,omitempty"`
	// Artifact is the content's SHA-256; ArtifactName is only for display
	// (the same content under another name is the same declaration).
	Artifact     string                   `json:"artifact,omitempty"`
	ArtifactName string                   `json:"artifact_name,omitempty"`
	Owner        string                   `json:"owner,omitempty"`
	Group        string                   `json:"group,omitempty"`
	Mode         string                   `json:"mode,omitempty"`
	Elevated     bool                     `json:"elevated,omitempty"`
	Validate     string                   `json:"validate,omitempty"`
	Service      *model.FileServiceAction `json:"service,omitempty"`
}

// Key identifies a resource on a host: service:<unit>, package:<name>,
// file:<path>.
func (r Resource) Key() string {
	switch r.Kind {
	case KindService, KindPackage:
		return r.Kind + ":" + r.Name
	case KindFile:
		return KindFile + ":" + r.Path
	}
	return r.Kind + ":?"
}

// KeyKind returns the kind part of a key.
func KeyKind(key string) string {
	k, _, _ := strings.Cut(key, ":")
	return k
}

// KeyName returns the name or path part of a key.
func KeyName(key string) string {
	_, n, _ := strings.Cut(key, ":")
	return n
}

// Normalize validates the resource and puts it in canonical form, reusing
// the job model's validation for names, paths, accounts, modes and
// commands.
func (r *Resource) Normalize() error {
	r.Kind = strings.TrimSpace(r.Kind)
	switch r.Kind {
	case KindService:
		name := strings.TrimSpace(r.Name)
		op := model.Operation{Type: model.OpService, Service: &model.ServiceOp{Name: name, State: r.State, Enabled: r.Enabled}}
		switch r.State {
		case "", StateRunning, StateStopped:
		default:
			return fmt.Errorf("service %s: the state must be running or stopped (restart and reload are actions, not desired states)", name)
		}
		if r.State == "" && r.Enabled == nil {
			return fmt.Errorf("service %s: choose running or stopped, enabled or disabled, or both", name)
		}
		if err := op.Normalize(); err != nil {
			return err
		}
		unit := op.Service.UnitName()
		if !strings.HasSuffix(unit, ".service") {
			return fmt.Errorf("%s: only systemd services (.service units) can be managed as desired state", unit)
		}
		*r = Resource{Kind: KindService, Name: unit, State: r.State, Enabled: r.Enabled}
	case KindPackage:
		name := strings.TrimSpace(r.Name)
		switch r.State {
		case StateInstalled, StateAbsent:
		default:
			return fmt.Errorf("package %s: the state must be installed or absent", name)
		}
		op := model.Operation{Type: model.OpPackage, Package: &model.PackageOp{Action: "install", Names: []string{name}}}
		if err := op.Normalize(); err != nil {
			return err
		}
		if len(op.Package.Names) != 1 {
			return errors.New("name the package")
		}
		*r = Resource{Kind: KindPackage, Name: op.Package.Names[0], State: r.State}
	case KindFile:
		fd := &model.FileDeployOp{Artifact: strings.TrimSpace(r.Artifact), Path: r.Path, Owner: strings.TrimSpace(r.Owner),
			Group: strings.TrimSpace(r.Group), Mode: strings.TrimSpace(r.Mode), Elevated: r.Elevated, Validate: r.Validate, Service: r.Service}
		op := model.Operation{Type: model.OpFileDeploy, FileDeploy: fd}
		if err := op.Normalize(); err != nil {
			return err
		}
		name := strings.TrimSpace(r.ArtifactName)
		if len(name) > 255 {
			name = name[:255]
		}
		*r = Resource{Kind: KindFile, Path: fd.Path, Artifact: fd.Artifact, ArtifactName: name, Owner: fd.Owner, Group: fd.Group,
			Mode: fd.Mode, Elevated: fd.Elevated, Validate: fd.Validate, Service: fd.Service}
	case "":
		return errors.New("each resource needs a kind (service, package or file)")
	default:
		return fmt.Errorf("unknown resource kind %q (supported: service, package, file)", r.Kind)
	}
	return nil
}

// canonical is the resource's identity for comparisons and hashes:
// display-only fields are left out.
func (r Resource) canonical() []byte {
	r.ArtifactName = ""
	b, _ := json.Marshal(r)
	return b
}

// Same reports whether two declarations ask for exactly the same thing.
func (r Resource) Same(o Resource) bool { return bytes.Equal(r.canonical(), o.canonical()) }

// NormalizeResources validates a baseline's resources: each valid, each
// key at most once, at most MaxResources, sorted by key.
func NormalizeResources(rs []Resource) ([]Resource, error) {
	if len(rs) > MaxResources {
		return nil, fmt.Errorf("a baseline may have at most %d resources", MaxResources)
	}
	out := make([]Resource, 0, len(rs))
	seen := map[string]bool{}
	for i := range rs {
		r := rs[i]
		if err := r.Normalize(); err != nil {
			return nil, fmt.Errorf("resource %d: %w", i+1, err)
		}
		if seen[r.Key()] {
			return nil, fmt.Errorf("%s is declared twice", Describe(r.Key()))
		}
		seen[r.Key()] = true
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key() < out[j].Key() })
	return out, nil
}

// HashResources is the content hash of a list of resources.
func HashResources(rs []Resource) string {
	h := sha256.New()
	for _, r := range rs {
		h.Write(r.canonical())
		h.Write([]byte{'\n'})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Describe names a key for people: "jellyfin.service", "package telnet",
// "/etc/caddy/Caddyfile".
func Describe(key string) string {
	switch KeyKind(key) {
	case KindService, KindFile:
		return KeyName(key)
	case KindPackage:
		return "package " + KeyName(key)
	}
	return key
}

// Want describes what a declaration asks for: "running, enabled at boot",
// "installed", "managed content (sha256 1a2b3c…), mode 0644".
func (r Resource) Want() string {
	switch r.Kind {
	case KindService:
		var parts []string
		if r.State != "" {
			parts = append(parts, r.State)
		}
		if r.Enabled != nil {
			parts = append(parts, map[bool]string{true: "enabled at boot", false: "disabled at boot"}[*r.Enabled])
		}
		return strings.Join(parts, ", ")
	case KindPackage:
		return r.State
	case KindFile:
		parts := []string{"managed content (sha256 " + short(r.Artifact) + ")"}
		if r.Owner != "" || r.Group != "" {
			parts = append(parts, "owner "+orAny(r.Owner)+":"+orAny(r.Group))
		}
		if r.Mode != "" {
			parts = append(parts, "mode "+r.Mode)
		}
		return strings.Join(parts, ", ")
	}
	return ""
}

func orAny(s string) string {
	if s == "" {
		return "*"
	}
	return s
}

func short(sha string) string {
	if len(sha) > 12 {
		return sha[:12] + "…"
	}
	return sha
}

// Corrective returns the ordinary typed operation that puts a host into
// this declared state. It is the same operation on every host that needs
// it (the operations are idempotent: a field that already matches is left
// alone), so one job can carry a step per resource and each host runs
// only the steps it needs. Package removals never remove other packages
// unless allowRemovals is set (the package operation's own safeguard).
func (r Resource) Corrective(allowRemovals bool) model.Operation {
	switch r.Kind {
	case KindService:
		return model.Operation{Type: model.OpService, Service: &model.ServiceOp{Name: r.Name, State: r.State, Enabled: r.Enabled}}
	case KindPackage:
		action := "install"
		if r.State == StateAbsent {
			action = "remove"
		}
		return model.Operation{Type: model.OpPackage, Package: &model.PackageOp{Action: action, Names: []string{r.Name}, AllowRemovals: allowRemovals}}
	default:
		return model.Operation{Type: model.OpFileDeploy, FileDeploy: &model.FileDeployOp{Artifact: r.Artifact, Path: r.Path, Owner: r.Owner,
			Group: r.Group, Mode: r.Mode, Elevated: r.Elevated, Validate: r.Validate, Service: r.Service}}
	}
}

// stepOrder orders corrections in one job: packages are installed first
// (a service's unit or a file's directory may come with them), then files
// (a service may need its configuration), then services, and removals
// last (after the services that might use them are stopped).
func stepOrder(r Resource) int {
	switch {
	case r.Kind == KindPackage && r.State == StateInstalled:
		return 0
	case r.Kind == KindFile:
		return 1
	case r.Kind == KindService:
		return 2
	}
	return 3
}
