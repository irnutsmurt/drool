# Runs a cloud-image VM with real systemd under software emulation (TCG):
# x86_64 guests, and aarch64 guests (VM_ARCH=aarch64) for arm64 testing.
# The container is unprivileged: no --privileged, no /dev/kvm, no extra
# capabilities or devices. Slow, but safe to run on a workstation.
FROM debian:12-slim
RUN apt-get update \
 && apt-get install -y --no-install-recommends qemu-system-x86 qemu-system-arm qemu-efi-aarch64 qemu-utils genisoimage curl ca-certificates \
 && rm -rf /var/lib/apt/lists/*
COPY run-vm.sh /run-vm.sh
RUN chmod 755 /run-vm.sh
EXPOSE 22
ENTRYPOINT ["/run-vm.sh"]
