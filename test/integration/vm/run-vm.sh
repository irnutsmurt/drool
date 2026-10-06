#!/bin/sh
# Usage: run-vm.sh IMAGE_URL
# /images: cache volume for downloaded cloud images
# /seed:   cloud-init user-data and meta-data
set -eu
url=$1
base=/images/$(basename "$url")
if [ ! -s "$base" ]; then
  echo "downloading $url"
  curl -fsSL -o "$base.part" "$url"
  mv "$base.part" "$base"
fi
mkdir -p /work
if [ ! -f /work/disk.qcow2 ]; then
  fmt=$(qemu-img info --output=json "$base" | sed -n 's/.*"format": "\([a-z0-9]*\)".*/\1/p' | head -n1)
  qemu-img create -q -f qcow2 -F "$fmt" -b "$base" /work/disk.qcow2 12G
  genisoimage -quiet -output /work/seed.iso -volid cidata -joliet -rock /seed/user-data /seed/meta-data
fi
# The guest's sshd is forwarded to this container's port 22. Reboots restart
# the guest inside the same qemu process, so the port stays up.
exec qemu-system-x86_64 -machine q35 -accel tcg,thread=multi,tb-size=256 -cpu "${VM_CPU:-max,la57=off}" -smp "${VM_CPUS:-2}" -m "${VM_MEM:-1536}" \
  -drive file=/work/disk.qcow2,if=virtio,cache=unsafe -drive file=/work/seed.iso,if=virtio,media=cdrom \
  -netdev user,id=n0,hostfwd=tcp::22-:22 -device virtio-net-pci,netdev=n0 \
  -nographic -serial mon:stdio
