package sshaccount

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"text/template"
	"time"

	"drool/internal/hostexec"
	"drool/internal/hostops"
)

// Cleaning up after DROOL (dogfooding correction batch, ARCHITECTURE-ALPHA
// §40). When a host is removed with "Clean up DROOL access", DROOL removes
// the account it created there. That account is how DROOL signs in, so its
// last session only stages a root-owned helper and starts it detached (a
// transient systemd unit, or its own session): the helper waits for DROOL's
// sessions to end, then removes the sudo rule, the account (with its home
// and DROOL's key), a group only when it is provably the one created with
// the account, the marker, and finally itself. DROOL has removed its own
// way in and can't watch that finish: the outcome is "initiated".
//
// Ownership is proven as the setup proves it: the marker file DROOL wrote
// before creating the account, with DROOL's key fingerprint and the
// account's user ID. The name proves nothing. Anything that doesn't match is
// refused before anything changes, and the helper checks again before it
// removes anything. Every step tolerates what an earlier, interrupted run
// already did.

// The helper's directory: root-only, outside everything it removes.
const cleanupDirPattern = "drool-account-cleanup."

var cleanupTmpl = template.Must(template.New("cleanup").Funcs(template.FuncMap{"q": hostops.ShQuote}).Parse(`set -u
PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
export PATH
umask 077
U={{q .User}}
KEYFP={{q .Fingerprint}}
M={{q .Marker}}
S={{q .Sudoers}}
err() { printf '@@ERR %s %s\n' "$1" "$2"; exit 1; }
kv() { printf '@@%s %s\n' "$1" "$2"; }
[ "$(id -u)" = 0 ] || err notroot "the cleanup didn't run as root"
pw=$(getent passwd "$U" || true)
if [ -z "$pw" ]; then
	[ -f "$M" ] || err gone "there's no account named $U on this host any more, and no record of it: nothing of DROOL's account is left to clean up"
	err gone "the account $U is gone already; only its record ($M) is left"
fi
[ -f "$M" ] || err notours "DROOL has no record (the marker $M) of creating the account $U on this host, so it doesn't remove it: it never removes an account it can't prove it created"
mkey=$(sed -n 's/^key=//p' "$M" | head -n 1)
muid=$(sed -n 's/^uid=//p' "$M" | head -n 1)
[ "$mkey" = "$KEYFP" ] || err foreign "the account $U was set up by a DROOL controller with another SSH key ($mkey), so this DROOL doesn't remove it"
uid=$(printf '%s\n' "$pw" | cut -d: -f3)
[ -n "$muid" ] && [ "$muid" = "$uid" ] || err replaced "the account $U isn't the one DROOL created (it has user ID $uid; DROOL's had ${muid:-none recorded}), so DROOL doesn't remove it"
missing=""
for c in userdel getent; do
	command -v "$c" >/dev/null 2>&1 || missing="$missing $c"
done
[ -z "$missing" ] || err prereq "this host doesn't have:$missing, which removing the account needs"
kv owned "$U"
kv uid "$uid"
{{if eq .Mode "check"}}kv ok 1
{{else}}trap '' HUP PIPE
D=$(mktemp -d /run/{{.DirPattern}}XXXXXX 2>/dev/null || mktemp -d /var/tmp/{{.DirPattern}}XXXXXX) || err state "cannot create a directory for the cleanup helper"
cat >"$D/cleanup.sh" <<'DROOL_ACCOUNT_CLEANUP'
{{.Helper}}DROOL_ACCOUNT_CLEANUP
[ -s "$D/cleanup.sh" ] || err state "cannot write the cleanup helper"
chmod 700 "$D/cleanup.sh"
if [ "$(cat /proc/1/comm 2>/dev/null)" = systemd ] && command -v systemd-run >/dev/null 2>&1; then
	unit=drool-account-cleanup-{{.Token}}
	systemd-run --quiet --collect --unit="$unit" --property=Type=exec /bin/sh "$D/cleanup.sh" >/dev/null 2>&1 ||
		{ rm -rf -- "$D"; err start "systemd couldn't start the cleanup helper"; }
	kv started "systemd unit $unit.service"
else
	command -v setsid >/dev/null 2>&1 || { rm -rf -- "$D"; err prereq "this host has no setsid to start the cleanup outside DROOL's session"; }
	setsid /bin/sh "$D/cleanup.sh" </dev/null >/dev/null 2>&1 &
	kv started "process $! in its own session"
fi
kv helper "$D/cleanup.sh"
kv done 1
{{end}}`))

// cleanupHelper runs as root, detached. It reads everything from the host
// again: the marker, the account, its group. Interrupted, it is simply run
// again (the next cleanup stages it anew, or an administrator runs it).
var cleanupHelper = template.Must(template.New("helper").Funcs(template.FuncMap{"q": hostops.ShQuote}).Parse(`set -u
trap '' HUP PIPE
PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
export PATH
U={{q .User}}
KEYFP={{q .Fingerprint}}
M={{q .Marker}}
S={{q .Sudoers}}
MDIR={{q .MarkerDir}}
SD={{q .SudoersDir}}
HEADER="# Managed by DROOL: $U is the account DROOL signs in with on this host."
me=$(dirname "$0")
log() { command -v logger >/dev/null 2>&1 && logger -t drool-account-cleanup -- "$*"; return 0; }
done_() {
	case "$me" in /run/{{.DirPattern}}*|/var/tmp/{{.DirPattern}}*) rm -rf -- "$me" ;; esac
	exit "$1"
}
mval() { sed -n "s/^$1=//p" "$M" 2>/dev/null | head -n 1; }
muid=$(mval uid)
pw=$(getent passwd "$U" || true)
# A record left by another DROOL (another key) isn't this one's to touch,
# with or without the account.
if [ -f "$M" ] && [ "$(mval key)" != "$KEYFP" ]; then
	log "$M was written by another DROOL: left alone"
	done_ 1
fi
if [ -n "$pw" ]; then
	# Still provably DROOL's: the marker, DROOL's key, the same user ID.
	uid=$(printf '%s\n' "$pw" | cut -d: -f3)
	if [ ! -f "$M" ] || [ "$(mval key)" != "$KEYFP" ] || [ -z "$muid" ] || [ "$muid" != "$uid" ]; then
		log "$U isn't provably the account DROOL created any more: left alone"
		done_ 1
	fi
	# DROOL's sessions end first: an account can't be removed while it
	# runs anything. They are DROOL's own; what's left after a while ends.
	pids() {
		for d in /proc/[0-9]*; do
			u=$(sed -n 's/^Uid:[[:space:]]*\([0-9]*\).*/\1/p' "$d/status" 2>/dev/null) || continue
			[ "$u" = "$uid" ] && echo "${d#/proc/}"
		done
	}
	n=0
	while [ -n "$(pids)" ] && [ $n -lt 30 ]; do sleep 1; n=$((n+1)); done
	for p in $(pids); do kill "$p" 2>/dev/null; done
	n=0
	while [ -n "$(pids)" ] && [ $n -lt 10 ]; do sleep 1; n=$((n+1)); done
	for p in $(pids); do kill -9 "$p" 2>/dev/null; done
	# The group created with the account is proven by the marker's gid, or,
	# from an older marker, by being the account's own group (recorded
	# before the account goes, so that a rerun still knows).
	gid=$(printf '%s\n' "$pw" | cut -d: -f4)
	if [ -z "$(mval gid)" ] && [ "$(getent group "$U" | cut -d: -f3)" = "$gid" ]; then
		printf 'gid=%s\n' "$gid" >>"$M"
	fi
fi
# The sudo rule: only DROOL's file for this account (its first line says so).
if [ -f "$S" ] && [ "$(head -n 1 "$S")" = "$HEADER" ]; then
	rm -f -- "$S"
fi
for f in "$SD"/.drool-account.* "$SD"/.drool-prev.*; do
	[ -f "$f" ] && [ "$(head -n 1 "$f")" = "$HEADER" ] && rm -f -- "$f"
done
# The account, with its home (DROOL's key is in it) and mail spool, when
# they are the account's own.
if [ -n "$pw" ]; then
	H=$(printf '%s\n' "$pw" | cut -d: -f6)
	case "$H" in
	/|/root|/home|/var|/var/lib|/usr|/etc|/opt|/srv|/tmp|/bin|/sbin|/lib|/lib64|/run|/dev|/proc|/sys|"") ;;
	/?*) [ -d "$H" ] && [ ! -L "$H" ] && [ "$(stat -c %u "$H" 2>/dev/null)" = "$uid" ] && rm -rf -- "$H" ;;
	esac
	for f in /var/mail/"$U" /var/spool/mail/"$U"; do
		[ -f "$f" ] && [ "$(stat -c %u "$f" 2>/dev/null)" = "$uid" ] && rm -f -- "$f"
	done
	if ! out=$(userdel "$U" 2>&1); then
		getent passwd "$U" >/dev/null 2>&1 && { log "userdel $U: $out"; done_ 1; }
	fi
fi
# The group: only the one the marker proves, when nobody else uses it.
mgid=$(mval gid)
g=$(getent group "$U" || true)
if [ -n "$g" ] && [ -n "$mgid" ] && [ "$(printf '%s\n' "$g" | cut -d: -f3)" = "$mgid" ] && [ -z "$(printf '%s\n' "$g" | cut -d: -f4)" ]; then
	if ! getent passwd | cut -d: -f4 | grep -qx "$mgid"; then
		groupdel "$U" >/dev/null 2>&1 || log "groupdel $U failed"
	fi
fi
# The record goes last: until then, a rerun can prove what is DROOL's.
rm -f -- "$M"
rmdir "$MDIR" 2>/dev/null
rmdir "$(dirname "$MDIR")" 2>/dev/null
log "DROOL's account $U was removed"
done_ 0
`))

type cleanupInput struct {
	Mode                                   string // check | start
	User, Fingerprint, Token               string
	Marker, MarkerDir, Sudoers, SudoersDir string
	DirPattern                             string
	Helper                                 string
}

func renderCleanup(mode, user, fingerprint string) (string, error) {
	in := cleanupInput{Mode: mode, User: user, Fingerprint: fingerprint, Token: newToken(), Marker: markerPath(user), MarkerDir: markerDir,
		Sudoers: sudoersPath(user), SudoersDir: sudoersDir, DirPattern: cleanupDirPattern}
	var h bytes.Buffer
	if err := cleanupHelper.Execute(&h, in); err != nil {
		return "", err
	}
	if strings.Contains(h.String(), "\nDROOL_ACCOUNT_CLEANUP\n") {
		return "", errors.New("the cleanup helper contains its own end marker")
	}
	in.Helper = h.String()
	var b bytes.Buffer
	err := cleanupTmpl.Execute(&b, in)
	return b.String(), err
}

// CleanupHelper renders the helper alone: what the cleanup starts on the
// host (integration tests run it by hand to check interrupted runs).
func CleanupHelper(user, fingerprint string) (string, error) {
	in := cleanupInput{User: user, Fingerprint: fingerprint, Marker: markerPath(user), MarkerDir: markerDir, Sudoers: sudoersPath(user),
		SudoersDir: sudoersDir, DirPattern: cleanupDirPattern}
	var h bytes.Buffer
	err := cleanupHelper.Execute(&h, in)
	return h.String(), err
}

// CleanupPlan says whether DROOL can remove the account it created on a
// host, checked on the host (read-only) over that account.
type CleanupPlan struct {
	// Applies: the host signs in with an account DROOL created
	// (ssh_account_at). Without it there's nothing of DROOL's to remove.
	Applies bool   `json:"applies"`
	User    string `json:"user,omitempty"`
	// OK: the host proved the account is DROOL's (the marker, its key, the
	// user ID); otherwise Code and Message say why not.
	OK      bool     `json:"ok"`
	Code    string   `json:"code,omitempty"`
	Message string   `json:"message,omitempty"`
	Removes []string `json:"removes"`
}

// CleanupResult is what starting the cleanup did.
type CleanupResult struct {
	// Outcome: initiated (the helper runs; DROOL can't watch it finish),
	// refused (not provably DROOL's, or the host lacks what it needs;
	// nothing changed), unknown (the host didn't answer: it may have
	// started).
	Outcome string `json:"outcome"`
	User    string `json:"user"`
	Helper  string `json:"helper,omitempty"` // how it runs
	Code    string `json:"code,omitempty"`
	Message string `json:"message"`
}

func (s *Service) cleanupTarget(ctx context.Context, hostID string) (user string, ok bool, err error) {
	h, err := s.d.Store.R.GetHost(ctx, hostID)
	if err != nil {
		return "", false, err
	}
	if !h.SshAccountAt.Valid || h.SshConfigured == 0 || h.SshUser == "" {
		return h.SshUser, false, nil
	}
	return h.SshUser, true, nil
}

func cleanupRemoves(user string) []string {
	return []string{sudoersPath(user) + " (its sudo rule)", "the account " + user + ", its home directory with DROOL's SSH key, and its mail spool",
		"its group " + user + ", when it is the one created with the account and nobody else uses it", markerPath(user) + " (DROOL's record of the account)"}
}

// errSignIn: DROOL couldn't sign in to run the cleanup (nothing ran).
type errSignIn struct{ err error }

func (e *errSignIn) Error() string { return e.err.Error() }
func (e *errSignIn) Unwrap() error { return e.err }

// runCleanup runs the cleanup script as root over the host's current SSH
// access (the account itself). An error is either errSignIn (nothing ran)
// or the connection failing while the script ran (it may have).
func (s *Service) runCleanup(ctx context.Context, hostID, mode string) (map[string]string, string, string, error) {
	key, err := s.controllerKey(ctx, false)
	if err != nil {
		return nil, "", "", &errSignIn{err}
	}
	user, _, err := s.cleanupTarget(ctx, hostID)
	if err != nil {
		return nil, "", "", &errSignIn{err}
	}
	script, err := renderCleanup(mode, user, key.fingerprint)
	if err != nil {
		return nil, "", "", &errSignIn{err}
	}
	runner, become, release, err := s.d.Hosts.RootRunner(ctx, hostID, true)
	if err != nil {
		return nil, "", "", &errSignIn{err}
	}
	defer release()
	cmd, prefix := become.Wrap("sh -s")
	rctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	run, err := runner.Run(rctx, cmd, hostexec.RunOptions{Stdin: append(prefix, script...), PTY: become.PTY, MaxOutput: 1 << 16})
	if err != nil {
		return nil, "", "", err
	}
	vals, code, msg := parseOutput(run.Stdout)
	if code == "" && (run.ExitCode != 0 || vals["done"] != "1" && vals["ok"] != "1") {
		code, msg = "root", fmt.Sprintf("the cleanup couldn't run as root (exit status %d): %s", run.ExitCode, strings.TrimSpace(string(run.Stderr)))
	}
	return vals, code, msg, nil
}

// PlanCleanup checks, read-only, whether DROOL may remove the account it
// created on the host.
func (s *Service) PlanCleanup(ctx context.Context, hostID string) (*CleanupPlan, error) {
	user, applies, err := s.cleanupTarget(ctx, hostID)
	if err != nil {
		return nil, err
	}
	p := &CleanupPlan{Applies: applies, Removes: []string{}}
	if !applies {
		return p, nil
	}
	p.User, p.Removes = user, cleanupRemoves(user)
	_, code, msg, err := s.runCleanup(ctx, hostID, "check")
	switch {
	case err != nil:
		p.Code, p.Message = "unreachable", fmt.Sprintf("DROOL can't sign in to the host as %s right now (%s), so it can't check or remove the account.", user, sentence(opMessage(err)))
	case code != "":
		p.Code, p.Message = code, msg
	default:
		p.OK = true
	}
	return p, nil
}

// Cleanup stages and starts the helper that removes DROOL's account from
// the host, over that account's last session, then drops DROOL's pooled
// connections to the host so that the account's processes end. The caller
// holds the host's lock and deletes the host afterwards.
func (s *Service) Cleanup(ctx context.Context, hostID string) (*CleanupResult, error) {
	user, applies, err := s.cleanupTarget(ctx, hostID)
	if err != nil {
		return nil, err
	}
	res := &CleanupResult{User: user}
	if !applies {
		res.Outcome, res.Code, res.Message = "refused", "not_drools", "this host doesn't sign in with an account DROOL created"
		return res, nil
	}
	vals, code, msg, err := s.runCleanup(ctx, hostID, "start")
	var signIn *errSignIn
	switch {
	case errors.As(err, &signIn):
		res.Outcome, res.Code = "refused", "unreachable"
		res.Message = fmt.Sprintf("DROOL couldn't sign in to the host as %s to start the cleanup (%s). Nothing changed.", user, sentence(opMessage(err)))
		return res, nil
	case err != nil:
		res.Outcome, res.Code = "unknown", "lost"
		res.Message = fmt.Sprintf("The connection was lost while DROOL was starting the cleanup of %s (%s), so DROOL doesn't know whether it started.",
			user, sentence(opMessage(err)))
		return res, nil
	case code != "":
		res.Outcome, res.Code, res.Message = "refused", code, sentence(msg)+" Nothing changed."
		return res, nil
	}
	if s.d.Pool != nil {
		s.d.Pool.Evict(hostID) // the account's sessions end, so the helper can remove it
	}
	res.Outcome, res.Helper = "initiated", vals["started"]
	res.Message = fmt.Sprintf("Cleanup initiated: a helper (%s) removes DROOL's account %s, its sudo rule and DROOL's record of it from the host. "+
		"DROOL intentionally removed its own management access and can't verify the final state through that account.", res.Helper, user)
	return res, nil
}
