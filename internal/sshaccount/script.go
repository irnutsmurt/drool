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
// nothing), never installs software, validates the sudo rule with visudo
// before sudo reads it, and ignores SIGHUP so that a dropped connection
// can't stop it halfway: running it again finds what it did and continues.
var accountScript = template.Must(template.New("account").Funcs(template.FuncMap{"q": hostops.ShQuote}).Parse(`set -u
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
for c in useradd usermod getent visudo sudo; do
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
T=$(mktemp /etc/sudoers.d/.drool-account.XXXXXX) || err sudoers "cannot write in /etc/sudoers.d"
cat >"$T" <<DROOL_SUDOERS
# Managed by DROOL: $U is the account DROOL signs in with on this host.
# Full management: $U may run any command as root without a password,
# which makes it root-equivalent. Delete this file to take that away.
Defaults:$U !requiretty
$U ALL=(ALL:ALL) NOPASSWD: ALL
DROOL_SUDOERS
chmod 440 "$T"
if ! out=$(visudo -cf "$T" 2>&1); then
	rm -f "$T"
	err sudoers "visudo rejected DROOL's sudo rule, so it wasn't installed: $(printf '%s' "$out" | tail -n 3 | tr '\n' ' ')"
fi
if [ -f "$S" ] && cmp -s "$T" "$S"; then
	rm -f "$T"
else
	prev=""
	if [ -f "$S" ]; then prev=$(mktemp /etc/sudoers.d/.drool-prev.XXXXXX) && cp -p "$S" "$prev" || err sudoers "cannot keep a copy of $S"; fi
	mv -f "$T" "$S" || { rm -f "$T"; err sudoers "cannot install $S"; }
	# A configuration that validated before must still validate; problems
	# that were already there (another file's mode, say) are reported, not
	# DROOL's to fix. Either way sudo itself must read the whole
	# configuration and grant the rule.
	bad=""
	if [ "$pre_ok" = 1 ] && ! out=$(visudo -c 2>&1); then
		bad="the sudo configuration doesn't validate with DROOL's rule in it: $(printf '%s\n' "$out" | grep -v 'parsed OK$' | head -n 3 | tr '\n' ' ')"
	elif ! out=$(sudo -n -l -U "$U" 2>&1) || ! printf '%s\n' "$out" | grep -q 'NOPASSWD: ALL'; then
		bad="sudo doesn't grant DROOL's rule to $U: $(printf '%s\n' "$out" | head -n 3 | tr '\n' ' ')"
	fi
	if [ -n "$bad" ]; then
		if [ -n "$prev" ]; then mv -f "$prev" "$S"; else rm -f "$S"; fi
		err sudoers "$bad. DROOL took its rule out again"
	fi
	[ -n "$prev" ] && rm -f "$prev"
	kv sudoers_changed 1
fi
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
}

func renderScript(in scriptInput) (string, error) {
	in.MarkerDir, in.Marker, in.Sudoers = markerDir, markerPath(in.User), sudoersPath(in.User)
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
