# DROOL connection snapshot (read-only; DESIGN §19.1, M10): every TCP/UDP
# socket with its state and, when visible, its owning process (ss -p).
# No packet contents.
printf '@@section sockets\n'
ss -Hntuap 2>/dev/null
printf '@@end\n'
