# Installing packages

DROOL installs packages by name with each host's own package manager (APT on Debian and Ubuntu, DNF
on Rocky, AlmaLinux, RHEL and Fedora), from the repositories already configured on the host. One run
can install the same package on hosts of both families, as long as the package has the same name on
both.

In the examples, Alex installs `htop` on the media servers.

## Before you start

- You need **jobs.execute** (administrators and operators).
- The hosts need **APT or DNF** (other distributions aren't supported for packages), and a way for
  DROOL to become root: sudo, or a root login. DROOL always installs as root.
- Shepherd hosts need the **Managed** or **Full** preset. *Managed* allows only packages from
  repositories already configured on the host, which is all DROOL ever installs anyway.
- The hosts need to reach their package repositories (the internet or your local mirror).

## Steps

1. **Runs → New run → Packages**.
2. **Action:** **Install**.
3. **Packages:** the names, separated by spaces, for example `htop` or `htop ncdu`. Names only: no
   file paths, no URLs, no version numbers (`htop=3.2.2` isn't accepted).
4. Choose the **Targets**.
5. Click **Preview changes**. DROOL runs a *dry run* on each host (`apt-get -s` or the DNF
   equivalent) and shows what each would install, upgrade or remove. **Nothing is changed by the
   preview.**
6. If a host would **remove other packages**, a red box says so (*1 host would remove other
   packages*). Without your permission, the run stops on that host before changing anything. Tick
   **Allow these removals** only if you've read the list and agree.
7. Click **Run on 2 hosts** and confirm.

## What DROOL does on each host

1. **Simulates** the installation again, right before doing it. If that simulation shows a removal
   you didn't allow, the host stops here, unchanged.
2. **Installs** non-interactively: `apt-get -y install htop` (or `dnf -y install htop`). On APT hosts,
   DROOL keeps your existing configuration files if a package ships a new default one (the dpkg
   options *confdef* and *confold*), and doesn't let Ubuntu's *needrestart* restart services.
3. **Records** what changed. A package that was already installed is left alone and reported as
   **no change**.

If a host's package lists don't know a package at all (for example on a freshly installed machine
whose lists were never downloaded), DROOL refreshes them once (*not in the local package metadata;
refreshing it and trying again*) and tries again.

If another program is using the package manager (often *unattended-upgrades* or a person running apt),
DROOL waits up to two minutes for the lock and then retries a few times before failing with
[Package Manager Busy](Package-Manager-Busy.md). If an earlier dpkg run was interrupted, DROOL
doesn't repair it on its own; it fails with the fix (`sudo dpkg --configure -a`), see
[dpkg Was Interrupted](dpkg-Was-Interrupted.md).

## Good to know

- **Recommended packages:** APT installs a package's *recommended* packages too (its default).
  DROOL has no option to turn that off.
- **Services aren't restarted.** If an install pulls in updated libraries, services keep using the
  old ones until they restart. Restart the service, or reboot, when that matters
  ([Managing Services](Managing-Services.md)).
- **Different names per distribution** (for example `apache2` vs `httpd`): use **Only run on** in a
  [script](Running-a-Script.md), or two runs targeting each family.
- **Want it to stay installed?** Add the package to a [Desired State](What-Desired-State-Is.md)
  baseline: DROOL then tells you if it's ever removed.

## Related

[Removing Packages](Removing-Packages.md) · [Updating Packages](Updating-Packages.md) ·
[System Updates](System-Updates.md) · [Understanding Run Results](Understanding-Run-Results.md)
