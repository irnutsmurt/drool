package model

import "errors"

// OpSSHCATrust makes a host trust (or stop trusting) DROOL's SSH user
// certificate authority, then proves it with a fresh connection that
// authenticates with a certificate only (JIT SSH, M16). It runs on the
// controller (it needs the CA), over the host's current SSH access, as root.
const OpSSHCATrust = "ssh_ca_trust"

// SSHCATrustOp configures trust in DROOL's SSH certificate authorities.
type SSHCATrustOp struct {
	// Action: "install" (default) writes /etc/ssh/drool_user_ca.pub with
	// every CA that isn't retired, points TrustedUserCAKeys at it in an
	// sshd_config.d file, checks the configuration and reloads sshd;
	// "remove" takes DROOL's files away again.
	Action string `json:"action,omitempty"`
}

func (o *SSHCATrustOp) normalize() error {
	switch o.Action {
	case "":
		o.Action = "install"
	case "install", "remove":
	default:
		return errors.New(`action must be "install" or "remove"`)
	}
	return nil
}
