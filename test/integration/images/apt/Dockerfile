# Integration test host: sshd in the foreground, no systemd, runs unprivileged.
ARG BASE=debian:12
FROM $BASE
ENV DEBIAN_FRONTEND=noninteractive
RUN apt-get update \
 && apt-get install -y --no-install-recommends openssh-server sudo iproute2 procps ca-certificates \
 && (getent passwd 1000 | cut -d: -f1 | xargs -r userdel -r 2>/dev/null || true) \
 && useradd -m -u 1000 -s /bin/bash deploy && passwd -d deploy \
 && echo 'deploy ALL=(ALL) NOPASSWD:ALL' > /etc/sudoers.d/deploy \
 && useradd -m -u 1001 -s /bin/bash pwuser && echo 'pwuser:drool-it-sudo-pw' | chpasswd \
 && echo 'pwuser ALL=(ALL) ALL' > /etc/sudoers.d/pwuser \
 && for u in deploy pwuser; do install -d -m 700 -o $u -g $u /home/$u/.ssh; done \
 && rm -f /etc/ssh/ssh_host_* && mkdir -p /run/sshd
COPY entrypoint.sh /entrypoint.sh
RUN chmod 755 /entrypoint.sh
EXPOSE 22
CMD ["/entrypoint.sh"]
