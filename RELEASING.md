# Releasing DROOL

How DROOL is versioned, what a release consists of, how it is built, qualified, signed and
published. The operator-facing side (installing, verifying, upgrading) is in the
[README](README.md); the design reasons are in [`ARCHITECTURE-ALPHA.md`](ARCHITECTURE-ALPHA.md) §34.

## Versions and channels

DROOL's version is [semantic versioning](https://semver.org): `MAJOR.MINOR.PATCH`, with a
prerelease suffix for candidates. The git tag carries a `v`; file, package and image names don't.

| Channel | Comes from | Example | Says |
|---|---|---|---|
| **stable** | the release pipeline, at a tag `vX.Y.Z` | `v1.0.0` | a release |
| **prerelease** | the release pipeline, at a tag `vX.Y.Z-rc.N` (or `-beta.N`) | `v1.1.0-rc.1` | a candidate, for testing |
| **development** | anything else: `make`, a container built from a checkout, the pipeline at an untagged commit | `v1.0.0-12-g3f6ced5` | not a release; *modified* when the source had uncommitted changes |

`drool version` and *Settings → System* show the version, the channel, the full commit and whether
the source was modified; `drool-agent version --json` the same for a Shepherd. A build from
modified source is always *development*, whatever it was stamped.

Three other numbers are versioned on their own and never inferred from DROOL's version:

- the **database schema** (`schema_version`): only increases; an upgrade migrates it forward, an
  older DROOL refuses a newer one;
- the **Shepherd protocol** (`agent_protocol`, now 1): negotiated at every connection; within it,
  capabilities are announced and checked ([user guide: Shepherd versions](docs/wiki/Supported-Platforms.md#shepherd-versions));
- the **backup format** (`format_version` in a backup's manifest, now 1).

What changes the version:

- **PATCH**: fixes, no new behavior an operator has to learn; never a schema change that an older
  patch release of the same minor version can't read back, never a protocol change.
- **MINOR**: new features; may migrate the schema (the pre-upgrade backup covers going back); keeps
  the Shepherd protocol range of the previous minor release.
- **MAJOR**: may drop support (a platform, a protocol version, upgrading straight from releases
  older than the previous major) — said in the release notes.

Within 1.x, upgrading straight from any earlier 1.x release, and from the pre-1.0 builds M15 and
M16 and the tagged Technical Alpha (`v0.1.0-alpha`), is supported and tested.

## What a release consists of

`make release` (= `packaging/release/build.sh`) writes `dist/<version>/`:

| File | What |
|---|---|
| `drool_<v>_linux_{amd64,arm64}.tar.gz` | the controller, both Shepherd binaries, `drool.service` (for `/usr/local/bin`), `INSTALL.md`, `README.md`, `config.example.yaml`, `THIRD_PARTY_NOTICES.txt` |
| `drool-agent_<v>_linux_{amd64,arm64}.tar.gz` | the Shepherd alone (`drool-agent`, `INSTALL.md`, notices) |
| `drool_<v>-1_{amd64,arm64}.deb`, `drool-<v>-1.{x86_64,aarch64}.rpm` | native packages (`<v>` with `~` before a prerelease suffix, as the package managers sort it) |
| `drool_<v>_image.oci.tar` | the container image for `linux/amd64` and `linux/arm64` (an OCI image layout; `docker load`, `podman load`), named `drool:<v>` |
| `compose.yaml`, `drool.container` | Docker Compose and Podman (Quadlet) files with the version filled in |
| `drool_<v>_linux_{amd64,arm64}.spdx.json` | SBOMs (SPDX 2.3) of the binaries: Go modules and the npm packages bundled into the web UI, with licenses |
| `drool_<v>_image_linux_{amd64,arm64}.spdx.json` | SBOMs of the image per platform, including the base image's packages |
| `THIRD_PARTY_NOTICES.txt` | the license texts of everything bundled |
| `LICENSE` | DROOL's own license (when the source has one) |
| `release.json` | version, channel, commit, schema, protocol, the pinned toolchain, the image's digests, every file's sha256 |
| `SHA256SUMS` | sha256 of every file above (`sha256sum --check`) |
| `SHA256SUMS.sig` | added offline by the release manager: [Signing](#signing) |

## Building

```sh
make release                                               # HEAD
packaging/release/build.sh --commit v1.0.0                 # a tag
packaging/release/build.sh --commit <rev> --version v1.0.0-rc.0   # an untagged candidate (development channel)
```

Requirements: git, Docker with BuildKit (the `docker buildx` plugin, or `packaging/release/buildx`
fetches a pinned standalone buildx — nothing is installed), network access to the registries and
the Go and npm package proxies. Everything else runs in containers pinned by digest (the Go and
Node images and the base image in the `Dockerfile`; nfpm and syft in `build.sh`).

What the pipeline guarantees:

- **One source revision.** It builds from a fresh repository holding exactly that commit:
  uncommitted changes and untracked files can't get in. A stable build refuses when the working
  tree has uncommitted changes (they wouldn't be in it, which confuses), and when there is no
  `LICENSE`.
- **One build of each binary.** The Dockerfile is the only build definition. The image's binaries
  are checked to be byte for byte the archived ones, and the host's binaries to report the release's
  version, channel and clean commit.
- **Reproducible.** `SOURCE_DATE_EPOCH` is the commit's time; archives, packages, the image and the
  SBOMs are deterministic. Check it: `build.sh --no-cache --out /tmp/again` at the same commit and
  compare `SHA256SUMS` — they are identical.
- **No secrets.** Nothing in the build needs a key; signing happens later, offline.

The architectures (`linux/amd64`, `linux/arm64`) are built on one machine: Go cross-compiles, and
the image's final stage only copies files, so no emulation is involved.

## Qualifying

A release is qualified by running the tests against its own artifacts. The integration tests take
`-release dist/<version>`: the controller under test is then the release's binary, the Shepherds it
hands out are the release's, and the release tests run.

| Layer | Command | Time |
|---|---|---|
| Formatting, vet, generated code | `make fmt-check vet check-generated`, `go vet -tags integration ./test/integration` | 2 min |
| Go tests with the race detector | `make test-race` | 15 min |
| Web: typecheck, unit tests, build | `make test-web` and `cd web && npm run build` | 2 min |
| API contract | part of the Go tests (`TestOpenAPIConformance`) | — |
| Release artifacts: contents, checksums, SBOMs, notices, signing workflow | `-run 'TestReleaseArtifacts\|TestReleaseSigning'` | 1 min |
| The container image with its `compose.yaml` (documented setup, restrictions, backup, restore; a Compose installation of the alpha upgraded to it and back) | `-run TestContainerImage` | 3 min |
| Packages on every supported distribution (install, upgrade path, remove, purge) | `-run TestReleasePackages` | 5 min |
| Upgrades from v0.1.0-alpha and M16, and going back | `-run TestReleaseUpgrade` | 3 min |
| Older Shepherds (M15, M16) with this controller, through a restore and an update | `-run TestMixedVersionShepherds` | 3 min |
| Integration: every supported distribution in containers, jump hosts, faults, files, Shepherds, firewall detection, Desired State, scheduling, restore, SSH certificates, alerts | `make test-integration` with `-release` | 40 min |
| Browser tests (Chromium) | `-run TestBrowser` | 15 min |
| Real systemd, reboots, sshd and nftables on every supported distribution and arm64 | `-run TestVMPlatform -vm <all>` | ~9 h, two VMs at a time (40 min to 2 h per distribution; the RHEL family's system updates are the slow part under emulation) |
| Native packages under real systemd (the hardened unit, setup, documented restore, upgrade and downgrade, removal) and the release archive's `INSTALL.md` | `-run TestVMReleasePackage -vm debian-12,rockylinux-9,debian-12-arm64` | 30 min |
| A firewall enforced by M15 (its host tool) through the upgrade to this release, over SSH and through a Shepherd | `-run TestVMFirewallUpgrade -vm debian-12` | 20 min |
| Rootless Docker and rootless Podman (Ubuntu 24.04), and the arm64 image under rootless Docker | `-run TestVMContainerRuntimes -vm ubuntu-24.04,debian-12-arm64` | 1 h |

All integration commands: `go test -tags integration ./test/integration -release dist/<version> -v -timeout <time>`.
Every suite has a controller of its own (or cleans up what it made), so each runs alone as well as
together with the others; qualification runs the release suites both ways. Run the browser tests
while no other test process starts or stops: every test process creates and removes Docker
networks, and Chromium aborts requests in flight when the machine's network addresses change
(`net::ERR_NETWORK_CHANGED`), which fails a browser test for reasons outside DROOL.

The VM tests run cloud images under QEMU's software emulation (TCG) inside ordinary, unprivileged
containers: no KVM, no privileged containers, nothing on the build machine's network, firewall or
devices is touched. They download cloud images once into the `drool-vm-images` Docker volume. The
Ubuntu VMs run with one vCPU: Ubuntu's 7.0 kernel can oops while booting with two vCPUs under
multi-threaded TCG (an emulator race, not something a real machine does).

`.github/workflows/ci.yml` runs the fast layers and builds the release on every push, and on `main`
publishes that build's image, once it has passed, as a development image:
`ghcr.io/irnutsmurt/drool:sha-<commit>` and `:main` (never a version tag or `latest`; the job's
token may write packages and only read the repository). `.github/workflows/release.yml` runs the container and package
layers on the release's artifacts.
The VM layers need hours and run on a machine you control before a release is published.

## Signing

The **DROOL release key** signs `SHA256SUMS`, which covers every file. It is an OpenSSH Ed25519 key
(a hardware-backed `ed25519-sk` key works too), kept offline by the release manager: never in this
repository, never in CI, never on a DROOL controller.

```sh
packaging/release/sign.sh dist/<version> /path/to/drool-release-key    # writes SHA256SUMS.sig
```

It checks every file against `SHA256SUMS` first, signs with namespace `drool-release`, and verifies
the signature the way users will, against [`packaging/keys/allowed_signers`](packaging/keys/allowed_signers).
Users check a download with `sha256sum --check` and `ssh-keygen -Y verify`
([user guide: Checking a release's files](docs/wiki/Installing-DROOL.md#checking-a-releases-files)).

**The release key** is created once, offline:

```sh
ssh-keygen -t ed25519 -C release@drool -f drool-release-key     # a passphrase, on an offline machine
```

and its public half is added to `packaging/keys/allowed_signers` as one line —
`release@drool namespaces="drool-release" ssh-ed25519 AAAA…` — in a commit before the first signed
release. Replacing it: add the new key's line, sign the next release with it, keep the old line
until releases signed with it are no longer offered, and say so in the release notes.

This is a different trust domain from the keys DROOL itself uses:

- **Shepherd updates** are signed by each operator with their own release-signing key
  (`drool release keygen`/`sign`): the operator, not the DROOL project, decides which Shepherd
  binaries their hosts may run. The official Shepherd binaries come in the release, checked by
  `SHA256SUMS` and its signature; operators sign the ones they deploy.
- The **Agent Root CA**, the **SSH user CA** and the **master key** belong to each controller.

Until the release key exists, `allowed_signers` lists no key and nothing verifies: no build is an
official release.

## Publishing

No release is published automatically (only `main`'s development image, above). For a release:

1. Upload every file of `dist/<version>/` and `SHA256SUMS.sig` to the release page.
2. If the image goes to a registry, copy the OCI archive without rebuilding it, so its digest stays
   the one in `release.json` — e.g. `skopeo copy oci-archive:drool_<v>_image.oci.tar docker://REGISTRY/drool:<v>`
   — and add the registry name to the README and the release notes.
3. Publish the release notes ([`packaging/release/RELEASE_NOTES.template.md`](packaging/release/RELEASE_NOTES.template.md)).

## Checklist

- [ ] `LICENSE` present and `packaging/release/license` holds its SPDX id (a stable build refuses otherwise).
- [ ] The release key's public half in `packaging/keys/allowed_signers`.
- [ ] Docs describe this release: README (status, quick start, supported platforms, limitations), the
      user guide (installing, updating, supported platforms), DESIGN, ARCHITECTURE-ALPHA, the
      maintainer field (`DROOL_MAINTAINER` or the default in `build.sh`).
- [ ] The docs say which release they describe: the README's `release-status` notes (the line under
      the logo and *Current status*) and the user guide's `docs-version` notes at the top of
      `docs/wiki/Home.md` and `docs/wiki/_Sidebar.md` name the stable release (until the first one they
      say "pre-1.0" and "development version"), and the install pages' "no release yet" notes are gone.
- [ ] The pre-1.0 items in `ARCHITECTURE-ALPHA.md` §35 are settled: the Compose data volume's name is
      decided (#5), and the `internal/recovery` TestHostDecisions race is fixed (#6), not skipped.
- [ ] Tag `vX.Y.Z-rc.N`, `make release`, every qualification layer above green on those artifacts.
- [ ] Tag `vX.Y.Z` on the same commit, `make release`, the fast layers and
      `TestReleaseArtifacts|TestReleaseSigning|TestContainerImage|TestReleasePackages|TestReleaseUpgrade` on the final artifacts
      (the version changed, so the bytes did).
- [ ] Reproducibility: a second `build.sh --no-cache` gives the same `SHA256SUMS`.
- [ ] `sign.sh`, then publish; the release notes from the template.
