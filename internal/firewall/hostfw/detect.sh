# DROOL firewall environment detection (read-only; DESIGN §16). Runs as root.
svc() {
	if ! command -v systemctl >/dev/null 2>&1; then echo unknown; return; fi
	s=$(systemctl is-active "$1" 2>/dev/null)
	case $s in
	active | activating | reloading) echo active ;;
	*) if systemctl cat "$1" >/dev/null 2>&1; then echo inactive; else echo absent; fi ;;
	esac
}
printf '@@uid %s\n' "$(id -u)"
if [ -d /run/systemd/system ]; then printf '@@systemd yes\n'; else printf '@@systemd no\n'; fi
if command -v nft >/dev/null 2>&1; then
	printf '@@nft %s\n' "$(command -v nft)"
	printf '@@nft_version %s\n' "$(nft --version 2>/dev/null | head -n 1)"
else
	printf '@@nft none\n'
fi
for c in systemd-run flock sha256sum ss; do
	if command -v "$c" >/dev/null 2>&1; then printf '@@have %s\n' "$c"; else printf '@@missing %s\n' "$c"; fi
done
for s in firewalld ufw docker tailscaled nftables iptables ip6tables netfilter-persistent libvirtd podman; do
	printf '@@service %s %s\n' "$s" "$(svc "$s")"
done
if command -v ufw >/dev/null 2>&1; then printf '@@ufw_status %s\n' "$(ufw status 2>/dev/null | head -n 1)"; fi
if [ -S "${DROOL_FW_DOCKER_SOCK:-/var/run/docker.sock}" ] || pidof dockerd >/dev/null 2>&1; then printf '@@docker_running yes\n'; fi
if pidof tailscaled >/dev/null 2>&1; then printf '@@tailscale_running yes\n'; fi
for t in iptables-legacy-save ip6tables-legacy-save; do
	if command -v "$t" >/dev/null 2>&1; then
		printf '@@legacy_rules %s %s\n' "$t" "$("$t" 2>/dev/null | grep -c '^-A')"
	fi
done
if [ -f /etc/nftables.conf ] && grep -q 'flush ruleset' /etc/nftables.conf 2>/dev/null; then printf '@@distro_conf_flushes /etc/nftables.conf\n'; fi
for f in /etc/resolv.conf /run/systemd/resolve/resolv.conf; do
	[ -f "$f" ] && sed -n 's/^nameserver[[:space:]]\{1,\}\([^[:space:]]*\).*/@@resolver \1/p' "$f"
done
if [ -x /usr/libexec/drool-firewall/drool-fw ]; then printf '@@tool %s\n' "$(/usr/libexec/drool-firewall/drool-fw version 2>/dev/null)"; fi
if command -v nft >/dev/null 2>&1; then
	printf '@@section nft_json\n'
	nft -j list ruleset 2>&1
	printf '\n@@end\n'
fi
