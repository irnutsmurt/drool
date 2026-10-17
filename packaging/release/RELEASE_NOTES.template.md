# DROOL <version>

<!-- Fill in every section. Write what changed for an operator, plainly; keep
limitations and breaking changes as they are rather than leaving them out.
Delete these comments. -->

Released <date> · commit `<full commit>` · channel <stable|prerelease>

## Highlights

<!-- The user-visible changes, most important first: what an operator can do
now that they couldn't, and what works differently. -->

-

## Security and recovery

<!-- Fixes and changes to authentication, credentials, the CAs, the audit
history, backup and restore, upgrades. Advisories with their ids. "None" if
none. -->

-

## Breaking changes

<!-- Anything that needs an operator's action or changes behavior they rely
on: configuration, API, CLI, file locations, dropped platforms. "None" if
none. -->

-

## Upgrading

1. Read *Breaking changes* above.
2. Make a backup and make sure you have the master key (README: *Backup and restore*). DROOL backs
   the database up by itself before migrating it — <yes: this release migrates the schema from N to M |
   no: this release doesn't change the database schema (N)> — but that copy stays on the
   controller's disk; each pre-upgrade backup is about the size of a `drool backup`, and the newest
   three are kept (`backup.before_upgrade`).
3. Upgrade the controller (README: *Upgrading*): the package, the archive, or the image with this
   release's `compose.yaml` / `drool.container` in the same place as before.
4. Update the Shepherds: sign this release's Shepherd binaries with your release key and roll them
   out (*Shepherds → Updates*).

Going back: <with a schema change: stop DROOL, install the older version, restore
`drool-pre-upgrade-<time>-schema<N>.tar.gz` with it | without one: install the older version again>
(README: *Going back*).

## Database, protocol and compatibility

| | |
|---|---|
| Database schema | <M> (<from N: migrations … | unchanged>) |
| Shepherd protocol | <1–1> |
| Shepherd capabilities used | <firewall-1, netmon-1, monitor-1, epoch-1> |
| Shepherds this controller works with | <every protocol-1 Shepherd; tested: this release's, M15, M16, …> |
| Shepherds that need an update for something | <e.g. before M16: moving to a restored controller's epoch> |
| Upgrades tested from | <v0.1.0-alpha, M15, M16, 1.x…> |

## Supported platforms

<!-- From the user guide's Supported Platforms page (docs/wiki/Supported-Platforms.md), and say what
changed since the last release. -->

- Controller: container image (linux/amd64, linux/arm64) on Docker (also rootless) and rootless
  Podman; `.deb` for Debian 12, 13 and Ubuntu 22.04, 24.04, 26.04; `.rpm` for Rocky Linux and
  AlmaLinux 9, 10 and Fedora <44>.
- Managed hosts: Debian 12, 13; Ubuntu 22.04, 24.04, 26.04; Rocky Linux 9, 10; AlmaLinux 9, 10;
  Fedora <44>; RHEL 9 and 10 expected to work.
- Shepherd: amd64, arm64.

## Known limitations

<!-- The README's Major limitations and the user guide's limits that matter for this release, and
anything found during qualification. -->

-

## Files and verification

| File | sha256 |
|---|---|
| <every file from SHA256SUMS> | <hash> |

<!-- If the image is published to a registry, its name and digest
(release.json "image.index_digest"). -->

```sh
sha256sum --check --ignore-missing SHA256SUMS
ssh-keygen -Y verify -f allowed_signers -I release@drool -n drool-release -s SHA256SUMS.sig < SHA256SUMS
```

The release key: `<the allowed_signers line>` (also in `packaging/keys/allowed_signers`).
DROOL is licensed under the GNU General Public License, version 3 (`LICENSE`, SPDX `GPL-3.0-only`).
SBOMs (SPDX 2.3) and `THIRD_PARTY_NOTICES.txt` list everything bundled and its license.

## Qualification

<!-- What ran on these artifacts (RELEASING.md "Qualifying") and the results:
suites, counts, distributions, durations. Anything skipped, and why. -->
