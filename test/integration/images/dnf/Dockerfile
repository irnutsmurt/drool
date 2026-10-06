# Integration test host: sshd in the foreground, no systemd, runs unprivileged.
ARG BASE=rockylinux:9
FROM $BASE
RUN dnf -y install openssh-server sudo iproute procps-ng \
 && (getent passwd 1000 | cut -d: -f1 | xargs -r userdel -r 2>/dev/null || true) \
 && useradd -m -u 1000 -s /bin/bash deploy && passwd -d deploy \
 && echo 'deploy ALL=(ALL) NOPASSWD:ALL' > /etc/sudoers.d/deploy \
 && useradd -m -u 1001 -s /bin/bash pwuser && echo 'pwuser:drool-it-sudo-pw' | chpasswd \
 && echo 'pwuser ALL=(ALL) ALL' > /etc/sudoers.d/pwuser \
 && for u in deploy pwuser; do install -d -m 700 -o $u -g $u /home/$u/.ssh; done \
 && rm -f /etc/ssh/ssh_host_* \
 && dnf -q makecache
COPY entrypoint.sh /entrypoint.sh
RUN chmod 755 /entrypoint.sh
EXPOSE 22
CMD ["/entrypoint.sh"]
