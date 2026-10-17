#!/usr/bin/env bash
# Builds every DROOL release artifact from one source revision (RELEASING.md).
#
#   packaging/release/build.sh [--commit REV] [--version vX.Y.Z[-pre]] [--out DIR]
#                              [--image-name NAME] [--no-cache] [--allow-dirty-worktree]
#
# The build runs from a fresh clone of the commit, so uncommitted changes and
# untracked files can't get into it, with every toolchain pinned by digest in
# containers (Docker with BuildKit: the docker buildx plugin, or a pinned
# standalone buildx, see ./buildx). The same commit gives the same
# bytes. The version and channel come from the commit's tag:
#   vX.Y.Z           stable
#   vX.Y.Z-rc.N …    prerelease
#   anything else    development (git describe; --version may name it)
# Output, in dist/<version>/ (packaging/release/README.md lists it):
#   drool_<v>_linux_<arch>.tar.gz          controller + Shepherd binaries, unit, docs
#   drool-agent_<v>_linux_<arch>.tar.gz    the Shepherd alone
#   drool_<v>-1_<arch>.deb, drool-<v>-1.<arch>.rpm   native packages (systemd)
#   drool_<v>_image.oci.tar                container image (linux/amd64 + linux/arm64)
#   *.spdx.json                            SBOMs (SPDX 2.3)
#   THIRD_PARTY_NOTICES.txt, compose.yaml, drool.container (Podman),
#   release.json, SHA256SUMS
# Signing SHA256SUMS is a separate, offline step (packaging/release/sign.sh):
# no signing key is ever needed here.
set -euo pipefail

NFPM_IMAGE=goreleaser/nfpm@sha256:a662cb167d7b6d3a83920c83d76b12d02b8ac5dd2c13e5c62c15270b23f6df0c   # v2.47.0
SYFT_IMAGE=anchore/syft@sha256:3eb5379ba7b409c3f4069b686110527af0c47df993fa5c10d13e7cf34f49b1aa     # v1.54.1
MAINTAINER=${DROOL_MAINTAINER:-"DROOL maintainers <maintainers@drool.invalid>"}
PLATFORMS=linux/amd64,linux/arm64
ARCHES=(amd64 arm64)

die() { echo "build.sh: $*" >&2; exit 1; }
note() { echo "==> $*" >&2; }

rev=HEAD version="" out="" image_name=drool allow_dirty=0
no_cache=()
while [[ $# -gt 0 ]]; do
  case $1 in
    --commit) rev=$2; shift 2 ;;
    --version) version=$2; shift 2 ;;
    --out) out=$2; shift 2 ;;
    --image-name) image_name=$2; shift 2 ;;
    --no-cache) no_cache=(--no-cache); shift ;;
    --allow-dirty-worktree) allow_dirty=1; shift ;;
    -h|--help) sed -n '2,26p' "$0"; exit 0 ;;
    *) die "unknown option $1 (--help)" ;;
  esac
done

repo=$(git rev-parse --show-toplevel)
commit=$(git -C "$repo" rev-parse --verify --quiet "$rev^{commit}") || die "no commit $rev"
semver='^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.]+)?$'
tag=$(git -C "$repo" describe --exact-match --tags --match 'v[0-9]*' "$commit" 2>/dev/null || true)
if [[ $tag =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  channel=stable v=$tag
elif [[ $tag =~ $semver ]]; then
  channel=prerelease v=$tag
else
  channel=development v=$(git -C "$repo" describe --tags --match 'v[0-9]*' --always "$commit")
fi
if [[ -n $version ]]; then
  [[ $channel == development ]] || die "$commit is tagged $tag: a $channel build's version is its tag"
  [[ $version =~ $semver ]] || die "--version must look like v1.2.3 or v1.2.3-rc.1"
  v=$version
fi
pkgver=${v#v}

# Uncommitted changes can't get in (the build uses a clean clone of the
# commit), but they would make "the release" differ from the tree in front
# of you.
if [[ $commit == "$(git -C "$repo" rev-parse HEAD)" && -n $(git -C "$repo" status --porcelain --untracked-files=no) ]]; then
  if [[ $channel != development && $allow_dirty == 0 ]]; then
    die "the working tree has uncommitted changes; a $channel release is built from $tag alone. Commit or stash them, or --allow-dirty-worktree"
  fi
  note "warning: uncommitted changes in the working tree are NOT part of this build of $commit"
fi

out=${out:-$repo/dist/$pkgver}
[[ $out == /* ]] || out=$PWD/$out # it is mounted into containers (docker run -v)
[[ -e $out ]] && die "$out exists; remove it first"
cache=$repo/.cache/release
mkdir -p "$cache/go"
work=$(mktemp -d "${TMPDIR:-/tmp}/drool-release.XXXXXX")
cleanup() { chmod -R u+w "$work" 2>/dev/null; rm -rf "$work"; }
trap cleanup EXIT

note "DROOL $v ($channel) from $commit"
# A fresh repository with exactly that commit (any commit, not only one a
# branch points at), its history and the tags.
git init --quiet "$work/src"
git -C "$work/src" fetch --quiet --tags --upload-pack='git -c uploadpack.allowAnySHA1InWant=true upload-pack' "$repo" "$commit"
git -C "$work/src" -c advice.detachedHead=false checkout --quiet --detach "$commit"
[[ -z $(git -C "$work/src" status --porcelain) ]] || die "the fresh clone of $commit isn't clean"
src=$work/src
epoch=$(git -C "$src" log -1 --format=%ct "$commit")
date=$(date -u -d "@$epoch" +%Y-%m-%dT%H:%M:%SZ)
export SOURCE_DATE_EPOCH=$epoch

# DROOL's own license: LICENSE plus its SPDX id in packaging/release/license.
license_id=NOASSERTION
if [[ -f $src/LICENSE ]]; then
  [[ -s $src/packaging/release/license ]] || die "LICENSE exists but packaging/release/license (its SPDX id) doesn't"
  license_id=$(tr -d ' \n' < "$src/packaging/release/license")
elif [[ $channel == stable ]]; then
  die "the source has no LICENSE: a stable release can't be distributed without one (the project owner chooses it)"
else
  note "warning: no LICENSE in the source; packages and SBOMs say NOASSERTION. Not distributable as is."
fi

# The packages' maintainer field names who answers for them: a stable
# release needs a real contact (DROOL_MAINTAINER, or the default above).
if [[ $MAINTAINER == *@drool.invalid* ]]; then
  [[ $channel != stable ]] || die "the packages' maintainer is the placeholder \"$MAINTAINER\": a stable release needs a real contact (DROOL_MAINTAINER, or the default in build.sh)"
  note "warning: the packages' maintainer is the placeholder \"$MAINTAINER\" (DROOL_MAINTAINER)"
fi

# The DROOL project's Shepherd release key, built into every binary
# (internal/release/official_keys.txt): Shepherds built without it can
# never take an official update, so a stable release needs it.
if ! grep -qE '^drool-release-ed25519 ' "$src/internal/release/official_keys.txt"; then
  [[ $channel != stable ]] || die "internal/release/official_keys.txt lists no Shepherd release key: this release's Shepherds could never take an official update (RELEASING.md, \"The Shepherd release key\")"
  note "warning: no Shepherd release key in internal/release/official_keys.txt: this build's Shepherds can't take official updates"
fi

arg() { sed -n "s/^ARG $1=//p" "$src/Dockerfile" | head -n1; }
go_image=$(arg GO_IMAGE) node_image=$(arg NODE_IMAGE) base_image=$(arg BASE_IMAGE)
[[ -n $go_image && -n $node_image && -n $base_image ]] || die "Dockerfile lacks pinned GO_IMAGE/NODE_IMAGE/BASE_IMAGE"

# The image index carries the metadata each platform's image has as labels:
# registries such as GitHub's read a multi-platform image's from its index.
label() { sed -n "s/.*org\.opencontainers\.image\.$1=\"\([^\"]*\)\".*/\1/p" "$src/Dockerfile" | head -n1; }
annotations=()
for key in source description; do
  val=$(label "$key")
  [[ -n $val ]] || die "Dockerfile lacks the org.opencontainers.image.$key label"
  annotations+=(--annotation "index:org.opencontainers.image.$key=$val")
done
annotations+=(--annotation "index:org.opencontainers.image.version=$v" --annotation "index:org.opencontainers.image.revision=$commit"
  --annotation "index:org.opencontainers.image.created=$date" --annotation "index:org.opencontainers.image.licenses=$license_id")

# BuildKit: the docker buildx plugin, or a pinned standalone buildx.
buildx=("$repo/packaging/release/buildx")
bargs=(--platform "$PLATFORMS" --build-arg "VERSION=$v" --build-arg "COMMIT=$commit" --build-arg "DATE=$date"
  --build-arg "CHANNEL=$channel" --build-arg "LICENSE=$license_id" "${no_cache[@]}")

note "binaries for $PLATFORMS"
"${buildx[@]}" build --progress=plain "${bargs[@]}" --target artifacts --output "type=local,dest=$work/bin" "$src" \
  >"$work/build-artifacts.log" 2>&1 || { tail -n 40 "$work/build-artifacts.log" >&2;
  die "build failed (multi-platform builds need BuildKit with the containerd image store or a docker-container builder)"; }
mkdir -p "$out"
image=$out/drool_${pkgver}_image.oci.tar
note "container image ($image_name:$pkgver)"
"${buildx[@]}" build --progress=plain "${bargs[@]}" --provenance=false --sbom=false "${annotations[@]}" \
  --output "type=oci,dest=$image,name=$image_name:$pkgver,rewrite-timestamp=true" "$src" \
  >"$work/build-image.log" 2>&1 || { tail -n 40 "$work/build-image.log" >&2; die "image build failed"; }

godocker() {
  docker run --rm --user "$(id -u):$(id -g)" -e HOME=/tmp -e GOCACHE=/cache/build -e GOMODCACHE=/cache/mod \
    -e CGO_ENABLED=0 -e GOFLAGS=-buildvcs=false -v "$cache/go:/cache" -v "$work:/work" -w /work/src "$go_image" "$@"
}
godocker go build -o /work/relkit ./packaging/release/relkit
relkit=$work/relkit

note "checking the binaries"
host_arch=$(uname -m); host_arch=${host_arch/x86_64/amd64}; host_arch=${host_arch/aarch64/arm64}
for arch in "${ARCHES[@]}"; do
  b=$work/bin/linux_$arch
  for f in drool drool-agent-linux-amd64 drool-agent-linux-arm64 THIRD_PARTY_NOTICES.txt; do
    [[ -s $b/$f ]] || die "the build has no linux_$arch/$f"
  done
  for f in drool drool-agent-linux-amd64 drool-agent-linux-arm64; do
    want=$(sha256sum "$b/$f" | cut -d' ' -f1)
    got=$("$relkit" oci -image "$image" -platform "linux/$arch" -file "usr/local/bin/$f")
    [[ $got == "$want" ]] || die "the linux/$arch image's $f isn't the archived one"
  done
  cmp -s "$b/THIRD_PARTY_NOTICES.txt" "$work/bin/linux_amd64/THIRD_PARTY_NOTICES.txt" || die "notices differ between platforms"
done
if [[ -x $work/bin/linux_$host_arch/drool ]]; then
  "$work/bin/linux_$host_arch/drool" version --json >"$work/version.json"
  for want in "\"version\": \"$v\"" "\"channel\": \"$channel\"" "\"commit\": \"$commit\"" '"modified": false'; do
    if ! grep -qF "$want" "$work/version.json"; then
      cat "$work/version.json" >&2
      die "drool version --json lacks $want"
    fi
  done
  [[ $("$work/bin/linux_$host_arch/drool-agent-linux-$host_arch" version) == "$v" ]] || die "drool-agent version isn't $v"
fi

note "archives and packages"
docs=("$src/README.md" "$work/bin/linux_amd64/THIRD_PARTY_NOTICES.txt" "$src/packaging/config.example.yaml")
[[ -f $src/LICENSE ]] && docs+=("$src/LICENSE")
sed 's#^ExecStart=/usr/bin/drool #ExecStart=/usr/local/bin/drool #' "$src/packaging/systemd/drool.service" >"$work/drool.service"
grep -q '^ExecStart=/usr/local/bin/drool serve$' "$work/drool.service" || die "unexpected ExecStart in packaging/systemd/drool.service"
for arch in "${ARCHES[@]}"; do
  b=$work/bin/linux_$arch
  name=drool_${pkgver}_linux_$arch
  "$relkit" tar -out "$out/$name.tar.gz" -prefix "$name/" -epoch "$epoch" \
    "$b/drool" "$b/drool-agent-linux-amd64" "$b/drool-agent-linux-arm64" "$work/drool.service" \
    "$src/packaging/release/INSTALL.archive.md=INSTALL.md" "${docs[@]}"
  name=drool-agent_${pkgver}_linux_$arch
  agent_files=("$b/drool-agent-linux-$arch=drool-agent" "$src/packaging/release/INSTALL.agent.md=INSTALL.md"
    "$work/bin/linux_amd64/THIRD_PARTY_NOTICES.txt")
  [[ -f $src/LICENSE ]] && agent_files+=("$src/LICENSE")
  "$relkit" tar -out "$out/$name.tar.gz" -prefix "$name/" -epoch "$epoch" "${agent_files[@]}"

  st=$work/pkg-$arch
  mkdir -p "$st/share" "$st/doc" "$st/scripts"
  cp "$b/drool" "$b/drool-agent-linux-amd64" "$b/drool-agent-linux-arm64" "$src/packaging/systemd/drool.service" "$st/"
  cp "$src"/packaging/release/scripts/*.sh "$st/scripts/"
  cp "$work/bin/linux_amd64/THIRD_PARTY_NOTICES.txt" "$src/packaging/config.example.yaml" "$st/share/"
  [[ -f $src/LICENSE ]] && cp "$src/LICENSE" "$st/share/"
  cp "$src/README.md" "$src/packaging/release/PACKAGE.md" "$st/doc/"
  sed "s/^license: .*/license: $license_id/" "$src/packaging/release/nfpm.yaml" >"$st/nfpm.yaml"
  find "$st" -exec touch -h -d "@$epoch" {} +
  for packager in deb rpm; do
    docker run --rm --hostname drool-release --user "$(id -u):$(id -g)" -v "$st:/w:ro" -v "$out:/out" -w /w \
      -e SOURCE_DATE_EPOCH -e "ARCH=$arch" -e "PKG_VERSION=$pkgver" -e "MAINTAINER=$MAINTAINER" \
      "$NFPM_IMAGE" package -f nfpm.yaml -p "$packager" -t /out/ >"$work/nfpm-$arch-$packager.log" 2>&1 ||
      { cat "$work/nfpm-$arch-$packager.log" >&2; die "nfpm $packager $arch failed"; }
  done
done
cp "$work/bin/linux_amd64/THIRD_PARTY_NOTICES.txt" "$out/"
[[ -f $src/LICENSE ]] && cp "$src/LICENSE" "$out/"
sed "s#^\(  *image: \).*#\1\${DROOL_IMAGE:-$image_name:$pkgver}#" "$src/compose.yaml" >"$out/compose.yaml"
grep -q "image: \${DROOL_IMAGE:-$image_name:$pkgver}" "$out/compose.yaml" || die "compose.yaml has no image line to pin"
qualified=$image_name
[[ $qualified == */* ]] || qualified=docker.io/library/$qualified
sed "s#^Image=.*#Image=$qualified:$pkgver#" "$src/packaging/podman/drool.container" >"$out/drool.container"
grep -q "^Image=$qualified:$pkgver\$" "$out/drool.container" || die "drool.container has no Image= line to pin"

note "SBOMs"
godocker go mod download
for arch in "${ARCHES[@]}"; do
  sb=$work/sbom-$arch
  mkdir -p "$sb/web"
  cp "$work/bin/linux_$arch"/drool* "$sb/"
  cp "$src/web/package.json" "$src/web/package-lock.json" "$sb/web/"
  syft() {
    docker run --rm -v "$sb:/scan:ro" -v "$out:/release:ro" -v "$cache/go/mod:/gomodcache:ro" \
      -e SYFT_CHECK_FOR_APP_UPDATE=false -e SYFT_GOLANG_SEARCH_LOCAL_MOD_CACHE_LICENSES=true \
      -e SYFT_GOLANG_LOCAL_MOD_CACHE_DIR=/gomodcache "$SYFT_IMAGE" scan "$@" -o spdx-json -q
  }
  syft dir:/scan --source-name drool --source-version "$pkgver" >"$out/drool_${pkgver}_linux_$arch.spdx.json"
  syft "oci-archive:/release/${image##*/}" --platform "linux/$arch" --source-name drool-image \
    --source-version "$pkgver" >"$out/drool_${pkgver}_image_linux_$arch.spdx.json"
done
"$relkit" sbom-normalize -epoch "$epoch" -namespace "https://drool.invalid/spdx/$pkgver" -license "$license_id" "$out"/*.spdx.json

note "release.json and SHA256SUMS"
"$relkit" manifest -dir "$out" -image "$image" -version "$v" -channel "$channel" -commit "$commit" -epoch "$epoch" \
  -license "$license_id" -platforms "$PLATFORMS" \
  -toolchain "go=$go_image,node=$node_image,base=$base_image,nfpm=$NFPM_IMAGE,syft=$SYFT_IMAGE,buildx=$("${buildx[@]}" version | head -n1)"
"$relkit" checksums -dir "$out"
note "done: $out"
ls -l "$out" >&2
