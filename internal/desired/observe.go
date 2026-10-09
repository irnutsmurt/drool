package desired

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"drool/internal/files"
	"drool/internal/hostops"
)

// Observation is what DROOL saw of one resource on one host.
type Observation struct {
	Service *ServiceObs `json:"service,omitempty"`
	Package *PackageObs `json:"package,omitempty"`
	File    *FileObs    `json:"file,omitempty"`
	// Error: this resource couldn't be observed (permission denied, outside
	// the agent's allowed paths, ...). Never a compliant result.
	Error string `json:"error,omitempty"`
	// Source: monitoring (M11 service data) or check (a targeted package
	// or file check).
	Source string `json:"source,omitempty"`
	At     int64  `json:"at"` // controller clock (unix ms)
}

// ServiceObs is a systemd unit's state.
type ServiceObs struct {
	Found     bool   `json:"found"`
	Load      string `json:"load,omitempty"`
	Active    string `json:"active,omitempty"`
	Sub       string `json:"sub,omitempty"`
	FileState string `json:"file_state,omitempty"`
}

// PackageObs is a package's installed state.
type PackageObs struct {
	Installed bool   `json:"installed"`
	Version   string `json:"version,omitempty"`
	Manager   string `json:"manager,omitempty"`
}

// FileObs is a path's state. Content is identified by its SHA-256,
// computed on the host: nothing is downloaded to compare.
type FileObs struct {
	Exists bool   `json:"exists"`
	Type   string `json:"type,omitempty"`   // file | symlink | dir | other
	Target string `json:"target,omitempty"` // a symlink's target
	SHA256 string `json:"sha256,omitempty"`
	Size   int64  `json:"size,omitempty"`
	Owner  string `json:"owner,omitempty"`
	Group  string `json:"group,omitempty"`
	UID    int    `json:"uid,omitempty"`
	GID    int    `json:"gid,omitempty"`
	Mode   string `json:"mode,omitempty"`
}

// Diff is one way the host differs from its desired state.
type Diff struct {
	// Field: state, enabled, installed, exists, type, content, owner,
	// group, mode.
	Field   string `json:"field"`
	Desired string `json:"desired"`
	Current string `json:"current"`
}

// Judgement is the comparison of an observation with a declaration.
type Judgement struct {
	Diffs []Diff
	// Blocker explains why restoring can't work as things are (a masked
	// unit, a symlink in place of a managed file); "" when it can.
	Blocker string
}

// Compliant reports whether nothing differs.
func (j Judgement) Compliant() bool { return len(j.Diffs) == 0 }

// Compare judges what was observed against what is declared. Services
// are judged exactly as the service operation judges them, so a
// correction with nothing left to do is always compliant.
func Compare(r Resource, o Observation) Judgement {
	var j Judgement
	switch r.Kind {
	case KindService:
		s := o.Service
		if s == nil {
			s = &ServiceObs{}
		}
		missing := !s.Found || s.Load == "not-found"
		cur := stateText(s)
		if missing {
			cur = "not installed"
		}
		switch r.State {
		case StateRunning:
			if missing || !hostops.UnitRunning(s.Active) {
				j.Diffs = append(j.Diffs, Diff{Field: "state", Desired: StateRunning, Current: cur})
			}
		case StateStopped:
			if !missing && (hostops.UnitRunning(s.Active) || hostops.UnitTransitional(s.Active)) {
				j.Diffs = append(j.Diffs, Diff{Field: "state", Desired: StateStopped, Current: cur})
			}
		}
		if r.Enabled != nil {
			en := !missing && hostops.UnitEnabled(s.FileState)
			if *r.Enabled != en {
				c := fileStateText(s.FileState)
				if missing {
					c = "not installed"
				}
				j.Diffs = append(j.Diffs, Diff{Field: "enabled", Desired: map[bool]string{true: "enabled at boot", false: "disabled at boot"}[*r.Enabled], Current: c})
			}
		}
		masked := s.Load == "masked" || s.FileState == "masked"
		switch {
		case len(j.Diffs) == 0:
		case masked && (r.State == StateRunning || r.Enabled != nil && *r.Enabled):
			j.Blocker = r.Name + " is masked on the host; unmask it there first (systemctl unmask) if it should run"
		case r.Enabled != nil && *r.Enabled && !missing && cantEnable(s.FileState):
			j.Blocker = fmt.Sprintf("%s can't be enabled at boot (unit file state: %s): it is started by another unit or has no [Install] section", r.Name, s.FileState)
		}
	case KindPackage:
		p := o.Package
		if p == nil {
			p = &PackageObs{}
		}
		switch {
		case r.State == StateInstalled && !p.Installed:
			j.Diffs = append(j.Diffs, Diff{Field: "installed", Desired: StateInstalled, Current: "not installed"})
		case r.State == StateAbsent && p.Installed:
			cur := "installed"
			if p.Version != "" {
				cur += " (" + p.Version + ")"
			}
			j.Diffs = append(j.Diffs, Diff{Field: "installed", Desired: StateAbsent, Current: cur})
		}
	case KindFile:
		f := o.File
		if f == nil {
			f = &FileObs{}
		}
		switch {
		case !f.Exists:
			j.Diffs = append(j.Diffs, Diff{Field: "exists", Desired: "present", Current: "missing"})
			return j
		case f.Type == string(files.KindSymlink):
			j.Diffs = append(j.Diffs, Diff{Field: "type", Desired: "regular file", Current: "symlink to " + f.Target})
			j.Blocker = r.Path + " is a symlink; DROOL never replaces a symlink (manage the target path instead)"
			return j
		case f.Type != "" && f.Type != string(files.KindFile):
			j.Diffs = append(j.Diffs, Diff{Field: "type", Desired: "regular file", Current: f.Type})
			j.Blocker = r.Path + " exists and is not a regular file"
			return j
		}
		if f.SHA256 != r.Artifact {
			j.Diffs = append(j.Diffs, Diff{Field: "content", Desired: "sha256 " + r.Artifact, Current: "sha256 " + f.SHA256})
		}
		if r.Owner != "" && r.Owner != f.Owner && r.Owner != strconv.Itoa(f.UID) {
			j.Diffs = append(j.Diffs, Diff{Field: "owner", Desired: r.Owner, Current: f.Owner})
		}
		if r.Group != "" && r.Group != f.Group && r.Group != strconv.Itoa(f.GID) {
			j.Diffs = append(j.Diffs, Diff{Field: "group", Desired: r.Group, Current: f.Group})
		}
		if r.Mode != "" && r.Mode != f.Mode {
			j.Diffs = append(j.Diffs, Diff{Field: "mode", Desired: r.Mode, Current: f.Mode})
		}
	}
	return j
}

func cantEnable(fileState string) bool {
	switch fileState {
	case "static", "indirect", "generated", "transient":
		return true
	}
	return false
}

// stateText says what a unit is doing in plain words.
func stateText(s *ServiceObs) string {
	switch s.Active {
	case "active":
		if s.Sub == "exited" {
			return "running (exited)"
		}
		return "running"
	case "reloading":
		return "running (reloading)"
	case "inactive":
		return "stopped"
	case "failed":
		return "failed"
	case "activating":
		if s.Sub == "auto-restart" {
			return "restarting after a failure"
		}
		return "starting"
	case "deactivating":
		return "stopping"
	case "":
		return "unknown"
	}
	return s.Active
}

func fileStateText(fs string) string {
	switch {
	case hostops.UnitEnabled(fs):
		return "enabled at boot"
	case fs == "disabled":
		return "disabled at boot"
	case fs == "":
		return "unknown"
	}
	return fs
}

// CurrentText summarizes an observation for lists: "failed, enabled at
// boot", "installed (2.4.1-1)", "sha256 3f2a…, mode 0664, root:root".
func CurrentText(kind string, o Observation) string {
	if o.Error != "" {
		return "couldn't check: " + o.Error
	}
	switch kind {
	case KindService:
		if o.Service == nil {
			return "unknown"
		}
		if !o.Service.Found || o.Service.Load == "not-found" {
			return "not installed"
		}
		return stateText(o.Service) + ", " + fileStateText(o.Service.FileState)
	case KindPackage:
		if o.Package == nil {
			return "unknown"
		}
		if !o.Package.Installed {
			return "not installed"
		}
		if o.Package.Version != "" {
			return "installed (" + o.Package.Version + ")"
		}
		return "installed"
	case KindFile:
		f := o.File
		if f == nil {
			return "unknown"
		}
		switch {
		case !f.Exists:
			return "missing"
		case f.Type == string(files.KindSymlink):
			return "symlink to " + f.Target
		case f.Type != "" && f.Type != string(files.KindFile):
			return f.Type
		}
		return fmt.Sprintf("sha256 %s, %s:%s, mode %s", short(f.SHA256), f.Owner, f.Group, f.Mode)
	}
	return ""
}

// --- targeted checks (packages and files) ---

// Prober runs read-only checks on hosts through their management path
// (mgmt.Router): the agent when it is used, SSH otherwise.
type Prober interface {
	// PackageVersions reports which of names are installed (name →
	// version, "" when not installed) and the package manager, without
	// running any package transaction.
	PackageVersions(ctx context.Context, hostID string, names []string) (string, map[string]string, error)
	// OpenFS opens a filesystem session (files.FS); elevated reads as root.
	OpenFS(ctx context.Context, hostID string, elevated bool) (files.FS, error)
}

// ObserveFile inspects one path without following symlinks and hashes a
// regular file's content on the host.
func ObserveFile(ctx context.Context, fsys files.FS, path string) (FileObs, error) {
	info, err := fsys.Stat(ctx, path)
	var fe *files.Error
	if errors.As(err, &fe) && fe.Code == files.CodeNotFound {
		return FileObs{Exists: false}, nil
	}
	if err != nil {
		return FileObs{}, err
	}
	o := FileObs{Exists: true, Type: string(info.Kind), Size: info.Size, Owner: info.Owner, Group: info.Group,
		UID: info.UID, GID: info.GID, Mode: files.FormatMode(info.Mode), Target: info.Target}
	if info.Kind == files.KindFile {
		sha, err := fsys.Hash(ctx, path)
		if err != nil {
			return FileObs{}, err
		}
		o.SHA256 = sha
	}
	return o, nil
}
