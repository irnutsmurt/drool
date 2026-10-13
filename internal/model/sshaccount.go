package model

import (
	"errors"
	"regexp"
	"strings"
)

// OpSSHAccount sets up DROOL's own SSH account on a host (After M17,
// ARCHITECTURE-ALPHA §36). Over the host's current SSH access, as root, it
// creates a Linux account that signs in with DROOL's key and may use sudo
// without a password, proves that account on a brand-new connection, and
// only then switches the host to it. It runs on the controller, and it is
// internal: only POST /hosts/{id}/ssh-account submits it, never a job,
// workflow, schedule or re-run.
const OpSSHAccount = "ssh_account"

// DefaultSSHAccount is the account's name unless the administrator picks
// another one.
const DefaultSSHAccount = "drool"

// SSHAccountOp names the account to set up.
type SSHAccountOp struct {
	User string `json:"user"`
}

// A portable user name: lower case, as useradd accepts everywhere; no dot,
// so the account's file in /etc/sudoers.d is one sudo reads.
var sshAccountUserRE = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)

// ValidSSHAccountUser checks the name of a dedicated account.
func ValidSSHAccountUser(u string) error {
	switch {
	case !sshAccountUserRE.MatchString(u):
		return errors.New("the account name must start with a lower-case letter or '_' and contain only lower-case letters, digits, '_' or '-' (at most 32)")
	case u == "root":
		return errors.New("DROOL's account can't be root: choose another name, or keep signing in as root")
	}
	return nil
}

func (o *SSHAccountOp) normalize() error {
	o.User = strings.TrimSpace(o.User)
	if o.User == "" {
		o.User = DefaultSSHAccount
	}
	return ValidSSHAccountUser(o.User)
}

// internalOp reports operations only DROOL itself submits.
func internalOp(t string) bool { return t == OpFirewall || t == OpSSHAccount }

func internalOpError(t string) error {
	if t == OpSSHAccount {
		return errors.New("setting up DROOL's own SSH account is done from the host's page (POST /hosts/{id}/ssh-account), not as a job step")
	}
	return errors.New(`"` + t + `" is an internal operation`)
}
