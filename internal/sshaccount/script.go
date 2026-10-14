package sshaccount

import (
	"bytes"
	"strings"
	"text/template"

	"drool/internal/hostops"
)

// Where the account's pieces live on the host.
const (
	// markerDir holds one root-only file per account DROOL created: which
	// controller key set it up, and its user ID. Without it DROOL treats an
	// account as someone else's, whatever its name.
	markerDir  = "/var/lib/drool/ssh-accounts"
	sudoersDir = "/etc/sudoers.d"
)

func markerPath(user string) string  { return markerDir + "/" + user }
func sudoersPath(user string) string { return sudoersDir + "/drool-account-" + user }

// accountScript checks a host for DROOL's own account ("check": reads
// only) or sets it up ("apply"). It runs as root over the host's current
// SSH access. It never touches an account DROOL didn't create (the marker
// file, written before the account and tied to DROOL's key and the
// account's user ID, is the only proof DROOL accepts; the name proves
// nothing), never installs software, installs only a sudo rule this host's
// own visudo accepted (sudoPolicy), and ignores SIGHUP so that a dropped
// connection can't stop it halfway: running it again finds what it did and
// continues.
var accountScript = template.Must(template.New("account").Funcs(template.FuncMap{"q": hostops.ShQuote, "sudoPolicy": func() string { return sudoPolicy }}).Parse(`set -u
PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
export PATH
umask 077
U={{q .User}}
KEY={{q .Key}}
KEYFP={{q .Fingerprint}}
TOKEN={{q .Token}}
CONTROLLER={{q .Controller}}
MDIR={{q .MarkerDir}}
M={{q .Marker}}
S={{q .Sudoers}}
err() { printf '@@ERR %s %s\n' "$1" "$2"; exit 1; }
kv() { printf '@@%s %s\n' "$1" "$2"; }
[ "$(id -u)" = 0 ] || err notroot "the setup didn't run as root"
missing=""
for c in useradd usermod getent visudo sudo runuser setsid; do
	command -v "$c" >/dev/null 2>&1 || missing="$missing $c"
done
[ -z "$missing" ] || err prereq "this host doesn't have:$missing. DROOL doesn't install software to set up its account: install it first (visudo comes with sudo), or keep signing in with this account"
grep -Eq '^[[:space:]]*[#@]includedir[[:space:]]+/etc/sudoers\.d/?[[:space:]]*$' /etc/sudoers 2>/dev/null ||
	err nosudoersd "/etc/sudoers doesn't read /etc/sudoers.d (it has no includedir line for it), where DROOL puts the account's sudo rule"
SH=/bin/sh
[ -x /bin/bash ] && SH=/bin/bash
state=new
pw=$(getent passwd "$U" || true)
if [ -n "$pw" ]; then
	[ -f "$M" ] || err exists "an account named $U already exists on this host, and DROOL didn't create it. DROOL never takes over an existing account: choose another name, or keep signing in with an existing account"
	mkey=$(sed -n 's/^key=//p' "$M" | head -n 1)
	muid=$(sed -n 's/^uid=//p' "$M" | head -n 1)
	mtok=$(sed -n 's/^token=//p' "$M" | head -n 1)
	[ "$mkey" = "$KEYFP" ] || err foreign "the account $U was set up by a DROOL controller with another SSH key ($mkey). Choose another name, or, if that was this DROOL before its key was replaced, remove that account from the host first (its sudo rule $S, the account, and $M)"
	uid=$(printf '%s\n' "$pw" | cut -d: -f3)
	if [ -n "$muid" ]; then
		[ "$muid" = "$uid" ] || err replaced "the account $U isn't the one DROOL created (it has user ID $uid; DROOL's had $muid). Choose another name"
	else
		gecos=$(printf '%s\n' "$pw" | cut -d: -f5)
		case "$gecos" in
			*"setup $mtok"*) [ -n "$mtok" ] || err exists "an account named $U already exists on this host, and DROOL can't tell that it created it. Choose another name" ;;
			*) err exists "an account named $U already exists on this host, and DROOL can't tell that it created it. Choose another name" ;;
		esac
	fi
	state=existing
	SH=$(printf '%s\n' "$pw" | cut -d: -f7)
else
	getent group "$U" >/dev/null 2>&1 && err group "a group named $U already exists on this host, and DROOL creates its account with a group of the same name. Choose another name"
fi
kv account "$state"
kv shell "$SH"
if ! pre=$(visudo -c 2>&1); then
	kv sudo_warning "visudo -c reports problems that are already on this host: $(printf '%s\n' "$pre" | grep -v 'parsed OK$' | head -n 3 | awk 'NR>1{printf "; "}{printf "%s",$0}')"
	pre_ok=0
else
	pre_ok=1
fi
if command -v sshd >/dev/null 2>&1 || [ -x /usr/sbin/sshd ]; then
	SSHD=$(command -v sshd 2>/dev/null || echo /usr/sbin/sshd)
	limits=$("$SSHD" -T 2>/dev/null | grep -Ei '^(allowusers|allowgroups|denyusers|denygroups) ' | tr '\n' ';' || true)
	[ -n "$limits" ] && kv sshd_limits "$limits"
fi
{{if eq .Mode "check"}}kv ok 1
exit 0
{{end}}trap '' HUP PIPE
created=0
if [ "$state" = new ]; then
	mkdir -p "$MDIR" && chmod 700 "$MDIR" || err state "cannot create $MDIR"
	printf 'controller=%s\nkey=%s\ntoken=%s\ncreated=%s\n' "$CONTROLLER" "$KEYFP" "$TOKEN" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" >"$M.tmp" &&
		mv -f "$M.tmp" "$M" || err state "cannot write $M"
	if ! out=$(useradd -r -m -s "$SH" -c "DROOL management account (setup $TOKEN)" "$U" 2>&1); then
		err useradd "useradd couldn't create $U: $(printf '%s' "$out" | head -c 300)"
	fi
	uid=$(id -u "$U") && printf 'uid=%s\n' "$uid" >>"$M" || err state "cannot record the account's user ID in $M"
	created=1
fi
kv created "$created"
usermod -p '*' "$U" >/dev/null 2>&1 || err usermod "cannot turn off password sign-in for $U"
H=$(getent passwd "$U" | cut -d: -f6)
G=$(id -gn "$U")
case "$H" in /?*) ;; *) err home "the account $U has no usable home directory ($H)" ;; esac
[ -d "$H" ] || err home "the home directory of $U ($H) doesn't exist"
[ ! -L "$H/.ssh" ] && [ ! -L "$H/.ssh/authorized_keys" ] || err home "$H/.ssh is a symbolic link; DROOL doesn't write through one"
mkdir -p "$H/.ssh" && chown "$U:$G" "$H/.ssh" && chmod 700 "$H/.ssh" || err ssh "cannot prepare $H/.ssh"
AK="$H/.ssh/authorized_keys"
body=$(printf '%s\n' "$KEY" | cut -d' ' -f1,2)
if ! grep -qF "$body" "$AK" 2>/dev/null; then
	printf '%s\n' "$KEY" >>"$AK" || err ssh "cannot write $AK"
	kv key_added 1
fi
chown "$U:$G" "$AK" && chmod 600 "$AK" || err ssh "cannot set the owner and mode of $AK"
if command -v restorecon >/dev/null 2>&1; then restorecon -R "$H/.ssh" >/dev/null 2>&1 || true; fi
SD={{q .SudoersDir}}
PRE=$(mktemp) || err state "cannot create a temporary file"
trap 'rm -f "$PRE"' EXIT
printf '%s\n' "$pre" | grep -v 'parsed OK$' >"$PRE" || true
{{sudoPolicy}}
kv home "$H"
kv uid "$(id -u "$U")"
kv done 1
`))

type scriptInput struct {
	Mode        string // check | apply
	User        string
	Key         string // DROOL's authorized_keys line
	Fingerprint string
	Token       string
	Controller  string
	MarkerDir   string
	Marker      string
	Sudoers     string
	SudoersDir  string
}

func renderScript(in scriptInput) (string, error) {
	in.MarkerDir, in.Marker, in.Sudoers, in.SudoersDir = markerDir, markerPath(in.User), sudoersPath(in.User), sudoersDir
	var b bytes.Buffer
	err := accountScript.Execute(&b, in)
	return b.String(), err
}

// parseOutput reads the script's "@@key value" lines and its "@@ERR code
// message" line.
func parseOutput(out []byte) (vals map[string]string, code, msg string) {
	vals = map[string]string{}
	for _, line := range strings.Split(string(out), "\n") {
		rest, ok := strings.CutPrefix(strings.TrimRight(line, "\r"), "@@")
		if !ok {
			continue
		}
		k, v, _ := strings.Cut(rest, " ")
		if k == "ERR" {
			code, msg, _ = strings.Cut(v, " ")
			continue
		}
		vals[k] = v
	}
	return vals, code, msg
}

// sudoPolicy installs the sudo rule DROOL's account needs, and only what this
// host's own sudo accepts and uses (After M17, ARCHITECTURE-ALPHA §36.2). The
// rule is full sudo without a password. A requiretty exception is added
// only if sudo, run as the account without a terminal the way DROOL will,
// asks for one, and only if this host's visudo accepts it: sudo
// implementations differ (sudo-rs, Ubuntu's default since 25.10, has no
// requiretty setting, and its visudo rejects the line), so nothing is taken
// for granted by distribution or version. Every proposed file is checked
// with visudo -cf before it is installed; once installed, the whole
// configuration must report no problem it didn't report before (problems
// already on the host are reported, not DROOL's to fix), and sudo itself
// must let the account run a command as root without a password and
// without a terminal. Otherwise DROOL's file is taken out again, and a file
// it replaced is put back.
//
// It runs inside the account script, as root, with U (the account), S (its
// sudoers file), SD (the sudoers directory), pre_ok (1: visudo -c passed
// before) and PRE (a file with the problems visudo -c reported before), and
// the script's err and kv. Its own tests run it against stand-in commands.
const sudoPolicy = `RULE="$U ALL=(ALL:ALL) NOPASSWD: ALL"
TTYX="Defaults:$U !requiretty"
# candidate: a proposed sudoers file ($1: an extra line, or none) in a
# dot-named file (sudo skips those), checked with this host's own visudo.
candidate() {
	T=$(mktemp "$SD/.drool-account.XXXXXX") || err sudoers "cannot write in $SD"
	{
		printf '%s\n' "# Managed by DROOL: $U is the account DROOL signs in with on this host." \
			"# Full management: $U may run any command as root without a password," \
			"# which makes it root-equivalent. Delete this file to take that away."
		[ -z "$1" ] || printf '%s\n' "$1"
		printf '%s\n' "$RULE"
	} >"$T" && chmod 440 "$T" || { rm -f "$T"; err sudoers "cannot write a sudo rule in $SD"; }
	if ! vout=$(visudo -cf "$T" 2>&1); then
		rm -f "$T"
		return 1
	fi
}
place() {
	if [ -f "$S" ] && cmp -s "$T" "$S"; then rm -f "$T"; return 0; fi
	mv -f "$T" "$S" || { rm -f "$T"; undo "cannot install $S"; }
	changed=1
}
undo() {
	if [ -n "$prev" ]; then mv -f "$prev" "$S"; else rm -f "$S"; fi
	err "${2:-sudoers}" "$1. DROOL took its rule out again"
}
# worse: the whole configuration reports a problem it didn't report before.
worse() {
	post=$(visudo -c 2>&1) && return 1
	if [ "$pre_ok" = 1 ]; then wout=$(printf '%s\n' "$post" | grep -v 'parsed OK$'); return 0; fi
	wout=$(printf '%s\n' "$post" | grep -v 'parsed OK$' | grep -Fxvf "$PRE")
	[ -n "$wout" ]
}
# probe: sudo as the account, without a terminal, as DROOL will run it. It
# passes when sudo ran id -u as root; what sudo said (pmsg) only explains a
# failure: warnings such as "unable to resolve host" don't make one.
probe() {
	perr=$(mktemp) || err state "cannot create a temporary file"
	pout=$(LC_ALL=C setsid -w runuser -u "$U" -- sudo -n -- id -u </dev/null 2>"$perr")
	pstatus=$?
	pmsg=$(head -c 400 "$perr" | tr '\n' ' ')
	rm -f "$perr"
	[ "$pstatus" = 0 ] && [ "$pout" = 0 ]
}
prev=""
if [ -f "$S" ]; then
	prev=$(mktemp "$SD/.drool-prev.XXXXXX") && cp -p "$S" "$prev" || err sudoers "cannot keep a copy of $S"
fi
changed=0
candidate "" || { [ -z "$prev" ] || rm -f "$prev"; err sudoers "visudo rejected DROOL's sudo rule, so it wasn't installed: $(printf '%s' "$vout" | tail -n 3 | tr '\n' ' ')"; }
place
worse && undo "the sudo configuration reports a new problem with DROOL's rule in it: $(printf '%s\n' "$wout" | head -n 3 | tr '\n' ' ')"
if ! probe; then
	printf '%s\n' "$pmsg" | grep -qiE 'must have a tty|requiretty' ||
		undo "sudo doesn't let $U run commands as root without a password and without a terminal: ${pmsg:-it printed \"$pout\" instead of 0}"
	# This host's sudo requires a terminal for the account (requiretty).
	kv sudo_tty required
	candidate "$TTYX" || undo "this host's sudo requires a terminal for $U (requiretty), and its visudo doesn't accept an exception for the account: $(printf '%s' "$vout" | tail -n 2 | tr '\n' ' ')" sudotty
	place
	worse && undo "the sudo configuration reports a new problem with DROOL's rule in it: $(printf '%s\n' "$wout" | head -n 3 | tr '\n' ' ')"
	probe || undo "this host's sudo still requires a terminal for $U with an exception for the account in DROOL's rule: $pmsg" sudotty
	kv sudo_exception requiretty
fi
[ -z "$prev" ] || rm -f "$prev"
[ "$changed" = 0 ] || kv sudoers_changed 1
kv sudo_rule "$(grep -v '^#' "$S" | tr '\n' ';')"`
