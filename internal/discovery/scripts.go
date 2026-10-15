// Package discovery collects host facts and status over a remote shell and
// parses the results (ARCHITECTURE-ALPHA §10.1, §11.8).
//
// Remote scripts are POSIX sh and dash-compatible, need only coreutils,
// iproute2 and procps-style tools that the supported distros ship, and never
// Python or jq. Each section is introduced by a "@@DROOL:name@@" line; every
// command tolerates failure so one missing tool only empties its section.
package discovery

const header = `set +e
s() { printf '\n@@DROOL:%s@@\n' "$1"; }
` + SystemdProbe + `dfp() { df -PTk -x tmpfs -x devtmpfs -x squashfs -x overlay -x efivarfs -x ramfs -x nsfs -x fuse.lxcfs 2>/dev/null || df -Pk 2>/dev/null; }
`

// SystemdProbe is the one systemd detection DROOL runs on hosts (discovery,
// the status check, the connection test, removing a Shepherd), the same
// over SSH as the login user and through a Shepherd as root. sdstate prints
// "running" when PID 1 is systemd, "none" when PID 1 is something else, and
// when PID 1 can't be read (/proc mounted with hidepid) decides from
// systemd's runtime directory: "running" if /run/systemd/system exists,
// "unknown" if this account can't look into /run/systemd, else "none". It
// needs no privileges. Neither systemctl's presence (a container can have
// the binary under another init) nor its exit status (a degraded system, or
// "Failed to connect to bus" for an account on a host without a D-Bus
// system bus) says whether systemd runs.
//
// sdlist runs systemctl with its arguments and prints its output. When
// systemctl fails it prints "@@sd-error permission MESSAGE" (it can't reach
// systemd as this account: no D-Bus system bus, denied, or it can't see
// /run/systemd, when systemctl even claims the system wasn't booted with
// systemd although sdok found PID 1 to be systemd) or "@@sd-error failed
// MESSAGE", so a list that couldn't be read is never taken for an empty one.
// (DietPi and other minimal images run systemd without D-Bus: only root can
// ask systemd there, through its private socket.)
const SystemdProbe = `sdstate() {
	_p1=$(cat /proc/1/comm 2>/dev/null)
	case "$_p1" in
	systemd) echo running; return 0 ;;
	?*) echo none; return 0 ;;
	esac
	if [ -d /run/systemd/system ]; then echo running
	elif [ -e /run/systemd ] && [ ! -x /run/systemd ]; then echo unknown
	else echo none; fi
}
sdok() { [ "$(sdstate)" != none ]; }
sdlist() {
	if _o=$(systemctl "$@" 2>/dev/null); then [ -n "$_o" ] && printf '%s\n' "$_o"; return 0; fi
	_e=$(systemctl "$@" 2>&1 >/dev/null | head -n 2 | tr '\n' ' ' | sed 's/ *$//')
	case "$_e" in
	*"Failed to connect to bus"*|*"ccess denied"*|*"ermission denied"*|*"uthentication required"*|*"not been booted with systemd"*) printf '@@sd-error permission %s\n' "$_e" ;;
	*) printf '@@sd-error failed %s\n' "${_e:-systemctl failed}" ;;
	esac
	return 1
}
`

// DiscoverScript gathers full inventory facts. It runs as the login user:
// nothing here needs root (listening ports show process names only for the
// user's own processes unless the login user is root).
const DiscoverScript = header + `s os-release
cat /etc/os-release 2>/dev/null || cat /usr/lib/os-release 2>/dev/null
s uname
uname -s 2>/dev/null; uname -r 2>/dev/null; uname -m 2>/dev/null
s hostname
hostname 2>/dev/null || cat /proc/sys/kernel/hostname 2>/dev/null
s boot-id
cat /proc/sys/kernel/random/boot_id 2>/dev/null
s cpu
nproc 2>/dev/null || getconf _NPROCESSORS_ONLN 2>/dev/null
grep -m1 '^model name' /proc/cpuinfo 2>/dev/null
s meminfo
grep -E '^(MemTotal|MemAvailable|SwapTotal|SwapFree):' /proc/meminfo 2>/dev/null
s df
dfp
s ip-addr
ip -o addr show 2>/dev/null
s ip-link
ip -o link show 2>/dev/null
s pkg
for c in apt-get dnf yum zypper pacman; do command -v "$c" >/dev/null 2>&1 && echo "$c"; done
s init
cat /proc/1/comm 2>/dev/null
echo "systemd=$(sdstate)"
s services
sdok && sdlist list-units --type=service --state=running --no-legend --plain --no-pager
s failed
sdok && sdlist list-units --state=failed --no-legend --plain --no-pager
s ports
ss -Htlnup 2>/dev/null
s containers
for c in docker podman containerd; do command -v "$c" >/dev/null 2>&1 && echo "$c"; done
s virt
systemd-detect-virt 2>/dev/null
s end
`

// StatusScript is the lightweight 5-minute check.
const StatusScript = header + `s boot-id
cat /proc/sys/kernel/random/boot_id 2>/dev/null
s uptime
cat /proc/uptime 2>/dev/null
s loadavg
cat /proc/loadavg 2>/dev/null
s meminfo
grep -E '^(MemTotal|MemAvailable):' /proc/meminfo 2>/dev/null
s df
dfp
s failed
echo "systemd=$(sdstate)"
sdok && sdlist list-units --state=failed --no-legend --plain --no-pager
s reboot-file
if [ -f /var/run/reboot-required ]; then echo required; head -n 5 /var/run/reboot-required.pkgs 2>/dev/null; fi
s kernels
` + InstalledKernelsCommand + `
s end
`

// InstalledKernelsCommand prints the running kernel release, then every
// installed one. /boot is authoritative; /lib/modules can keep directories of
// removed kernels, so it is only a fallback.
const InstalledKernelsCommand = `uname -r 2>/dev/null; k=$(ls -1 /boot 2>/dev/null | sed -n 's/^vmlinuz-//p'); if [ -n "$k" ]; then echo "$k"; else ls -1 /lib/modules 2>/dev/null; fi`

// UpdatesAPTScript lists pending updates from the local package cache
// (no refresh, no root).
const UpdatesAPTScript = header + `s apt
apt list --upgradable 2>/dev/null; echo "@@rc=$?"
s end
`

// UpdatesDNFScript lists pending and security updates from the local cache
// (-C: no network, no root) and checks whether a reboot is needed. It tries
// dnf4 forms first, then dnf5 forms.
const UpdatesDNFScript = header + `s dnf-check
dnf -q -C check-update 2>/dev/null; echo "@@rc=$?"
s dnf-security
dnf -q -C updateinfo list --security 2>/dev/null; rc=$?; echo "@@rc=$rc"
if [ "$rc" -ne 0 ]; then
  s dnf5-security
  dnf -q -C advisory list --security 2>/dev/null; echo "@@rc=$?"
fi
s needs-restarting
if command -v needs-restarting >/dev/null 2>&1; then needs-restarting -r >/dev/null 2>&1; echo "@@rc=$?"
else dnf needs-restarting -r >/dev/null 2>&1; echo "@@rc=$?"; fi
s end
`

// RefreshAPT and RefreshDNF refresh package metadata; they need root.
const (
	RefreshAPT = "DEBIAN_FRONTEND=noninteractive apt-get -qq update"
	RefreshDNF = "dnf -q -y makecache"
)
