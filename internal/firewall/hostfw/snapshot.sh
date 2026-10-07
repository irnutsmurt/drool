# DROOL connection snapshot (read-only; DESIGN §19.1): listening sockets and
# established connections, no process names.
printf '@@section listen\n'
ss -Hlnut 2>/dev/null
printf '@@end\n@@section tcp\n'
ss -Hnt state established 2>/dev/null
printf '@@end\n@@section udp\n'
ss -Hnu state established 2>/dev/null
printf '@@end\n'
