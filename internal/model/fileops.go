package model

import (
	"errors"
	"fmt"
	"path"
	"regexp"
	"strconv"
	"strings"
)

// OpFileDeploy deploys a stored artifact to a path (ARCHITECTURE-ALPHA §10.7).
const OpFileDeploy = "file_deploy"

// FileDeployOp installs a file from the controller's artifact store:
// preview → stage → validate → keep the previous version → atomic install →
// verify → optional service action. Content is referenced by SHA-256, never
// embedded in the job.
type FileDeployOp struct {
	Artifact string `json:"artifact"` // SHA-256 of the content in the artifact store
	Path     string `json:"path"`
	// Overwrite: "replace" (default) or "create_only" (leave an existing
	// file alone).
	Overwrite string `json:"overwrite,omitempty"`
	Owner     string `json:"owner,omitempty"` // empty: keep the existing file's, or the default
	Group     string `json:"group,omitempty"`
	Mode      string `json:"mode,omitempty"` // octal, e.g. "0644"; empty: keep / default 0644
	// Elevated deploys as root through the host's privilege escalation.
	Elevated bool `json:"elevated"`
	// Validate runs on the staged file before anything is replaced; {path}
	// is replaced by the staged file's path. A failure leaves the live file
	// untouched.
	Validate string `json:"validate,omitempty"`
	// Service is reloaded or restarted after a successful install that
	// changed the file. Its outcome is reported separately.
	Service       *FileServiceAction `json:"service,omitempty"`
	TimeoutS      int                `json:"timeout_s"`
	When          *When              `json:"when,omitempty"`
	RollbackOf    string             `json:"rollback_of,omitempty"`    // set by the controller for rollbacks
	Remove        bool               `json:"remove,omitempty"`         // rollback of a file that didn't exist before
	ExpectCurrent string             `json:"expect_current,omitempty"` // rollback: the content DROOL installed
}

// FileServiceAction is the service step after a deployment.
type FileServiceAction struct {
	Name   string `json:"name"`
	Action string `json:"action"` // reload | restart
}

var (
	sha256RE  = regexp.MustCompile(`^[0-9a-f]{64}$`)
	accountRE = regexp.MustCompile(`^[A-Za-z0-9_.][A-Za-z0-9_.-]{0,63}\$?$`)
)

// ParseOctalMode parses "644" / "0644" style modes.
func ParseOctalMode(s string) (uint32, error) {
	if s == "" || len(s) > 5 {
		return 0, errors.New("mode must be octal, like 644 or 0755")
	}
	m, err := strconv.ParseUint(s, 8, 32)
	if err != nil || m > 0o7777 {
		return 0, errors.New("mode must be octal, like 644 or 0755")
	}
	return uint32(m), nil
}

func (o *FileDeployOp) normalize() error {
	o.Path = strings.TrimSpace(o.Path)
	if !strings.HasPrefix(o.Path, "/") || strings.ContainsAny(o.Path, "\x00\n\r") || len(o.Path) > 4096 {
		return errors.New("path must be an absolute file path")
	}
	o.Path = path.Clean(o.Path)
	if o.Path == "/" || strings.HasSuffix(o.Path, "/.") {
		return errors.New("path must name a file")
	}
	if !o.Remove && !sha256RE.MatchString(o.Artifact) {
		return errors.New("artifact must be the SHA-256 of an uploaded file")
	}
	if o.ExpectCurrent != "" && !sha256RE.MatchString(o.ExpectCurrent) {
		return errors.New("expect_current must be a SHA-256")
	}
	switch o.Overwrite {
	case "":
		o.Overwrite = "replace"
	case "replace", "create_only":
	default:
		return errors.New(`overwrite must be "replace" or "create_only"`)
	}
	for _, n := range []string{o.Owner, o.Group} {
		if n != "" && !accountRE.MatchString(n) {
			return fmt.Errorf("invalid user or group name %q", n)
		}
	}
	if o.Mode != "" {
		m, err := ParseOctalMode(o.Mode)
		if err != nil {
			return err
		}
		o.Mode = fmt.Sprintf("%04o", m)
	}
	o.Validate = strings.TrimSpace(o.Validate)
	if len(o.Validate) > MaxCommandBytes {
		return errors.New("validation command is too long")
	}
	if o.Service != nil {
		o.Service.Name = strings.TrimSpace(o.Service.Name)
		if o.Service.Name == "" && o.Service.Action == "" {
			o.Service = nil
		} else {
			if !unitNameRE.MatchString(o.Service.Name) {
				return fmt.Errorf("invalid service name %q", o.Service.Name)
			}
			if o.Service.Action != "reload" && o.Service.Action != "restart" {
				return errors.New(`service action must be "reload" or "restart"`)
			}
		}
	}
	if err := o.When.normalize(); err != nil {
		return err
	}
	return normTimeout(&o.TimeoutS, 600)
}

// UnitName returns the service's systemd unit name.
func (a *FileServiceAction) UnitName() string {
	return (&ServiceOp{Name: a.Name}).UnitName()
}
