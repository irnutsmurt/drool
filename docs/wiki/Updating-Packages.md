# Updating packages

To update **specific** packages to the newest version in the host's repositories, use the package
action **Upgrade (installed only)**. To update everything, use a [system update](System-Updates.md)
instead.

For example, after a security advisory for OpenSSH, Alex updates just `openssh-server` on every host.

## Before you start

The same as [Installing Packages](Installing-Packages.md): **jobs.execute**, APT or DNF, sudo or
root, and on Shepherd hosts the **Managed** or **Full** preset.

## Steps

1. **Runs → New run → Packages**, **Action: Upgrade (installed only)**.
2. **Packages:** `openssh-server`.
3. Choose the **Targets**.
4. **Preview changes** shows, per host, which version each package would move from and to. A host
   where the package isn't installed shows nothing to do: *Upgrade (installed only)* never installs a
   missing package.
5. If any host would remove other packages, decide about **Allow these removals** (see
   [Removing Packages](Removing-Packages.md)).
6. **Review: run on …**, then **Start run on …** in DROOL's review.

## What DROOL does

- On APT hosts: `apt-get -y install --only-upgrade openssh-server`; on DNF hosts the DNF equivalent.
  Simulated first, like every package change.
- **It doesn't refresh the package lists first.** It uses what the host already knows (DROOL only
  refreshes them by itself when a named package is missing from them entirely). If the new version
  was published recently, the host may not know about it yet: run a [system update](System-Updates.md)
  with **Refresh package metadata first**, or refresh with a command (`sudo apt-get update` or
  `sudo dnf makecache`), then upgrade.
- **It doesn't restart the service.** `sshd` keeps running the old version until it restarts. Add a
  [service restart](Managing-Services.md) afterwards (a [workflow](Workflows.md) can do both in one
  go), or reboot.
- A package that is already the newest version is reported as **no change**.

## Related

[System Updates](System-Updates.md) · [Installing Packages](Installing-Packages.md) ·
[Managing Services](Managing-Services.md)
