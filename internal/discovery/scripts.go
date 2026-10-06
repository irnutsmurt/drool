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
sd() { [ -d /run/systemd/system ]; }
dfp() { df -PTk -x tmpfs -x devtmpfs -x squashfs -x overlay -x efivarfs -x ramfs -x nsfs -x fuse.lxcfs 2>/dev/null || df -Pk 2>/dev/null; }
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
sd && echo systemd-running
s services
sd && systemctl list-units --type=service --state=running --no-legend --plain --no-pager 2>/dev/null
s failed
sd && systemctl list-units --state=failed --no-legend --plain --no-pager 2>/dev/null
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
if sd; then echo systemd-running; systemctl list-units --state=failed --no-legend --plain --no-pager 2>/dev/null; fi
s reboot-file
if [ -f /var/run/reboot-required ]; then echo required; head -n 5 /var/run/reboot-required.pkgs 2>/dev/null; fi
s kernels
uname -r 2>/dev/null
ls -1 /lib/modules 2>/dev/null
s end
`

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
