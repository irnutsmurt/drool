// Package sshdriver runs operations on hosts over SSH.
package sshdriver

import "strings"

// Become describes privilege escalation (ARCHITECTURE-ALPHA §8.4).
type Become struct {
	Method   string // "none" or "sudo"
	Password string // sudo password; empty for passwordless sudo
	PTY      bool   // host requires a TTY for sudo (requiretty)
}

// Wrap returns the command to run with privilege escalation and the bytes to
// prepend to its stdin. With a sudo password, -k forces sudo to read the
// password from the first stdin line every time, so a cached sudo timestamp
// can never leak the password into the command's own stdin.
func (b Become) Wrap(cmd string) (string, []byte) {
	if b.Method != "sudo" {
		return cmd, nil
	}
	if b.Password == "" {
		return "sudo -n -- " + cmd, nil
	}
	return "sudo -k -S -p '' -- " + cmd, []byte(b.Password + "\n")
}

// classifySudo explains common sudo failures.
func classifySudo(stderr string) (detail, hint string) {
	s := strings.ToLower(stderr)
	switch {
	case strings.Contains(s, "a password is required"):
		return "sudo requires a password", "Set a sudo password credential for this host, or configure NOPASSWD for the SSH user."
	case strings.Contains(s, "incorrect password"), strings.Contains(s, "sorry, try again"):
		return "sudo rejected the password", "Check the host's sudo password credential."
	case strings.Contains(s, "not in the sudoers"), strings.Contains(s, "is not allowed to"):
		return "the SSH user may not use sudo", "Add the user to sudoers or choose a different user."
	case strings.Contains(s, "must have a tty"), strings.Contains(s, "no tty present"):
		return "sudo requires a terminal (requiretty)", "Edit the host and enable \"Requires a terminal for sudo\"."
	case strings.Contains(s, "command not found"):
		return "sudo is not installed", "Install sudo or log in as root."
	}
	return strings.TrimSpace(stderr), ""
}
