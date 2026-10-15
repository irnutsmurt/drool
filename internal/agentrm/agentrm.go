// Package agentrm removes a Shepherd from its host: the files and the
// script that DROOL runs over SSH (as root) and that a Shepherd runs itself
// when it is asked to remove itself (dogfooding correction batch,
// ARCHITECTURE-ALPHA §40). One script, so both paths remove exactly the
// same things.
package agentrm

import (
	"strings"

	"drool/internal/discovery"
)

// Item is one Shepherd file or folder (drool-agent install writes them).
type Item struct{ Path, What string }

// Items are a Shepherd's files on its host.
var Items = []Item{
	{"/etc/systemd/system/drool-agent.service", "the drool-agent service"},
	{"/usr/libexec/drool-agent", "its programs (every installed version)"},
	{"/etc/drool/agent.yaml", "its capability ceiling"},
	{"/etc/drool/release-keys.d", "the release keys it trusts for updates"},
	{"/var/lib/drool-agent", "its identity (private key and certificates) and state"},
	{"/var/log/drool-agent", "its log"},
}

// Script removes a Shepherd (MODE=remove), only reports what is there
// (MODE=check), or is the Shepherd's own removal helper (MODE=self), as
// root. Every step tolerates absence, so running it again finishes what an
// interrupted run started. It ignores SIGHUP and SIGPIPE: a dropped
// connection doesn't stop it halfway.
//
// MODE=self runs detached from the Shepherd (a transient systemd unit, or
// its own session): it waits until the Shepherd has told the controller
// that it accepted the removal (the Shepherd creates GO; after 20 seconds
// it goes ahead anyway: the removal was asked for and accepted), removes
// everything as MODE=remove does, and finally deletes its own directory
// (DIR). It reports nothing: nobody is left to read it.
//
// The caller prepends MODE= (and, for self, GO= and DIR=) lines.
const Script = `set -u
trap '' HUP PIPE
PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
export PATH
U=/etc/systemd/system/drool-agent.service
I=/usr/libexec/drool-agent
kv() { [ "$MODE" = self ] || printf '@@%s %s\n' "$1" "$2"; }
` + discovery.SystemdProbe + `sd=$(sdstate)
procs() {
	for d in /proc/[0-9]*; do
		p=${d#/proc/}
		[ "$p" = "$$" ] && continue
		# Over SSH the parent is the shell running this script. The
		# Shepherd's own helper may have been started by the Shepherd: that
		# parent is the one to stop.
		[ "$MODE" != self ] && [ "$p" = "$PPID" ] && continue
		exe=$(readlink "$d/exe" 2>/dev/null) || exe=""
		case "$exe" in "$I"/*) echo "$p"; continue ;; esac
		cmd=$(tr '\0' ' ' < "$d/cmdline" 2>/dev/null) || continue
		case "$cmd" in *"$I/drool-agent-supervise"*) echo "$p" ;; esac
	done
}
report() {
	for p in "$U" "$I" /etc/drool/agent.yaml /etc/drool/release-keys.d /var/lib/drool-agent /var/log/drool-agent; do
		if [ -e "$p" ] || [ -L "$p" ]; then kv present "$p"; fi
	done
	if [ "$sd" = running ]; then
		kv active "$(systemctl is-active drool-agent.service 2>/dev/null)"
		kv enabled "$(systemctl is-enabled drool-agent.service 2>/dev/null)"
	fi
	for p in $(procs); do kv pid "$p"; done
}
kv systemd "$sd"
if [ "$MODE" = self ]; then
	n=0
	while [ ! -e "$GO" ] && [ $n -lt 20 ]; do sleep 1; n=$((n+1)); done
fi
if [ "$MODE" = remove ] || [ "$MODE" = self ]; then
	if [ "$sd" = running ]; then systemctl disable --now drool-agent.service >/dev/null 2>&1; fi
	# The supervisor of a host without systemd goes with the agent, or it
	# would start the agent again; so do update helpers still running.
	n=0
	while [ -n "$(procs)" ] && [ $n -lt 10 ]; do
		for p in $(procs); do kill "$p" 2>/dev/null; done
		sleep 1
		n=$((n+1))
	done
	for p in $(procs); do kill -9 "$p" 2>/dev/null; done
	rm -f "$U"
	if [ "$sd" = running ]; then systemctl daemon-reload >/dev/null 2>&1; systemctl reset-failed drool-agent.service >/dev/null 2>&1; fi
	rm -rf "$I" /var/lib/drool-agent /var/log/drool-agent /etc/drool/release-keys.d
	rm -f /etc/drool/agent.yaml
	# Only when empty: DROOL's own configuration may live there.
	rmdir /etc/drool 2>/dev/null
	kv removed 1
fi
if [ "$MODE" = self ]; then
	case "$DIR" in /run/drool-shepherd-removal.*|/tmp/drool-shepherd-removal.*|/var/tmp/drool-shepherd-removal.*) rm -rf -- "$DIR" ;; esac
	exit 0
fi
report
kv done 1
`

// Report is what the script printed.
type Report struct {
	Systemd, Active, Enabled string
	Present                  map[string]bool
	PIDs                     int
	Removed, Done            bool
}

// Parse reads the script's "@@key value" lines.
func Parse(out []byte) Report {
	r := Report{Present: map[string]bool{}}
	for _, ln := range strings.Split(string(out), "\n") {
		if !strings.HasPrefix(ln, "@@") {
			continue
		}
		k, v, _ := strings.Cut(strings.TrimSpace(strings.TrimPrefix(ln, "@@")), " ")
		switch k {
		case "systemd":
			r.Systemd = v
		case "active":
			r.Active = v
		case "enabled":
			r.Enabled = v
		case "present":
			r.Present[v] = true
		case "pid":
			r.PIDs++
		case "removed":
			r.Removed = true
		case "done":
			r.Done = true
		}
	}
	return r
}

// Clean reports whether nothing of a Shepherd is left.
func (r Report) Clean() bool { return len(r.Present) == 0 && r.PIDs == 0 && r.Active != "active" }
