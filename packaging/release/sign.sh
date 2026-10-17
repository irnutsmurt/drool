#!/usr/bin/env bash
# Signs a release — offline, by the release manager, never in CI
# (RELEASING.md):
#
#   packaging/release/sign.sh dist/<version> /path/to/drool-release-key /path/to/shepherd-release.key
#
# 1. Checks every file against SHA256SUMS, writes SHA256SUMS.sig with the
#    DROOL release key (ssh-keygen -Y sign, namespace drool-release) and
#    verifies it the way users will, against packaging/keys/allowed_signers
#    (ALLOWED_SIGNERS overrides it).
# 2. Signs each Shepherd binary with the Shepherd release key (drool release
#    keygen): drool-agent_<v>_linux_<arch>.release.json, which controllers
#    fetch from the release page and verify, and Shepherds verify again,
#    before they update themselves. The key must be one this release's
#    binaries have built in (release.json "official_release_keys"), or no
#    Shepherd would accept the result (OFFICIAL_RELEASE_KEYS="id ..."
#    replaces that list, for tests). DROOL=path/to/drool names the drool
#    program to sign with; by default this release's own, for this machine.
set -euo pipefail
die() { echo "sign.sh: $*" >&2; exit 1; }
[[ $# -eq 3 ]] || die "usage: sign.sh RELEASE_DIR RELEASE_KEY SHEPHERD_KEY"
dir=$1 key=$2 shepherd_key=$3
repo=$(cd "$(dirname "$0")/../.." && pwd)
allowed=${ALLOWED_SIGNERS:-$repo/packaging/keys/allowed_signers}
[[ -f $dir/SHA256SUMS && -f $dir/release.json ]] || die "$dir is not a release directory (no SHA256SUMS or release.json)"
[[ ! -e $dir/SHA256SUMS.sig ]] || die "$dir/SHA256SUMS.sig exists already"
[[ -f $shepherd_key ]] || die "no Shepherd release key at $shepherd_key"
grep -qE '^[^#[:space:]]' "$allowed" || die "$allowed lists no key: add the release key's public half first"
(cd "$dir" && sha256sum --check --quiet --strict SHA256SUMS) || die "files in $dir don't match SHA256SUMS"

# release.json is written by relkit, one field per line at the top.
field() { sed -n "s/^  \"$1\": \"\\(.*\\)\",\$/\\1/p" "$dir/release.json" | head -n1; }
v=$(field version) channel=$(field channel)
[[ -n $v && -n $channel ]] || die "release.json names no version or channel"
pkgver=${v#v}
official=$(sed -n '/"official_release_keys": \[/,/\]/p' "$dir/release.json" | grep -oE '"[0-9a-f]{16}"' | tr -d '"' || true)
[[ -z ${OFFICIAL_RELEASE_KEYS:-} ]] || official=$(tr ' ' '\n' <<<"$OFFICIAL_RELEASE_KEYS")

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
host=$(uname -m); host=${host/x86_64/amd64}; host=${host/aarch64/arm64}
drool=${DROOL:-}
if [[ -z $drool ]]; then
  tar -xzf "$dir/drool_${pkgver}_linux_${host}.tar.gz" -C "$work" "drool_${pkgver}_linux_${host}/drool" ||
    die "can't take drool from the release for this machine ($host): set DROOL=path/to/drool"
  drool=$work/drool_${pkgver}_linux_${host}/drool
fi

# Sign the Shepherds first: nothing is written into the release until both
# signatures are right.
manifests=()
for arch in amd64 arm64; do
  name=drool-agent_${pkgver}_linux_${arch}
  [[ -f $dir/$name.tar.gz ]] || continue
  [[ ! -e $dir/$name.release.json ]] || die "$dir/$name.release.json exists already"
  tar -xzf "$dir/$name.tar.gz" -C "$work" "$name/drool-agent"
  bin=$work/$name/drool-agent
  "$drool" release sign --key "$shepherd_key" --version "$v" --channel "$channel" "$bin" >/dev/null ||
    die "drool release sign refused the $arch Shepherd"
  id=$(grep -oE '"key_id": *"[0-9a-f]{16}"' "$bin.release.json" | grep -oE '[0-9a-f]{16}' || true)
  if ! grep -qx "$id" <<<"$official"; then
    if [[ $channel == stable || $channel == prerelease ]]; then
      die "the Shepherd key $id isn't built into this release (release.json official_release_keys: ${official:-none}): no Shepherd would accept what it signs"
    fi
    echo "sign.sh: warning: the Shepherd key $id isn't built into this development build: its descriptions are for testing only" >&2
  fi
  manifests+=("$bin.release.json=$dir/$name.release.json")
done
[[ ${#manifests[@]} -gt 0 ]] || die "$dir has no Shepherd archives to sign"

ssh-keygen -Y sign -f "$key" -n drool-release "$dir/SHA256SUMS" >/dev/null
ssh-keygen -Y verify -f "$allowed" -I release@drool -n drool-release -s "$dir/SHA256SUMS.sig" <"$dir/SHA256SUMS" ||
  { rm -f "$dir/SHA256SUMS.sig"; die "the signature doesn't verify against $allowed: is that the key listed there?"; }
for m in "${manifests[@]}"; do
  cp "${m%%=*}" "${m#*=}"
done
echo "Signed: $dir/SHA256SUMS.sig"
for m in "${manifests[@]}"; do
  echo "Signed: ${m#*=}"
done
