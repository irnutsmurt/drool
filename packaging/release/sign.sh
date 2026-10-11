#!/usr/bin/env bash
# Signs a release with the DROOL release key — offline, by the release
# manager, never in CI (RELEASING.md):
#
#   packaging/release/sign.sh dist/<version> /path/to/drool-release-key
#
# Checks every file against SHA256SUMS first, then writes SHA256SUMS.sig
# (ssh-keygen -Y sign, namespace drool-release) and verifies it the way users
# will, against packaging/keys/allowed_signers (ALLOWED_SIGNERS overrides it).
set -euo pipefail
die() { echo "sign.sh: $*" >&2; exit 1; }
[[ $# -eq 2 ]] || die "usage: sign.sh RELEASE_DIR PRIVATE_KEY"
dir=$1 key=$2
repo=$(cd "$(dirname "$0")/../.." && pwd)
allowed=${ALLOWED_SIGNERS:-$repo/packaging/keys/allowed_signers}
[[ -f $dir/SHA256SUMS && -f $dir/release.json ]] || die "$dir is not a release directory (no SHA256SUMS or release.json)"
[[ ! -e $dir/SHA256SUMS.sig ]] || die "$dir/SHA256SUMS.sig exists already"
grep -qE '^[^#[:space:]]' "$allowed" || die "$allowed lists no key: add the release key's public half first"
(cd "$dir" && sha256sum --check --quiet --strict SHA256SUMS) || die "files in $dir don't match SHA256SUMS"
ssh-keygen -Y sign -f "$key" -n drool-release "$dir/SHA256SUMS" >/dev/null
ssh-keygen -Y verify -f "$allowed" -I release@drool -n drool-release -s "$dir/SHA256SUMS.sig" <"$dir/SHA256SUMS" ||
  { rm -f "$dir/SHA256SUMS.sig"; die "the signature doesn't verify against $allowed: is that the key listed there?"; }
echo "Signed: $dir/SHA256SUMS.sig"
