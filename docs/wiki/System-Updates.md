# System updates

A system update installs all available updates on a host: the equivalent of `apt-get upgrade` or
`dnf upgrade`. DROOL previews it first, never removes packages without your permission, and can reboot
the host afterwards when the update requires it.

## Before you start

- You need **jobs.execute** (administrators and operators).
- The hosts need **APT or DNF**, sudo or a root login, and access to their repositories. Shepherd
  hosts need the **Managed** or **Full** preset.
- **Find out which hosts need updates:** the **Hosts** list has an **UPDATES** column (for example
  *1 (1 security)*), the Overview has an **Updates and reboots** card, and each host's page shows
  *Updates: 0 pending, none security · checked 12 min ago*. DROOL checks every 6 hours.

## Steps

1. **Runs → New run → System update**, or **Install updates** on a host's page.
2. Choose the **Mode**:

   | Mode | What it does |
   |---|---|
   | **Upgrade** | Upgrades installed packages without removing any (`apt-get upgrade` / `dnf upgrade`). Updates that need new or removed packages are held back on Debian/Ubuntu. |
   | **Full upgrade** | Also installs or removes packages when an upgrade needs it (`apt-get full-upgrade`; the same as Upgrade on DNF). If anything would be removed, the job stops unless you allow it. |
   | **Security updates only** | Only security updates (`dnf upgrade --security`). **RHEL family and Fedora only**: Debian and Ubuntu hosts fail with *security-only updates are not supported on Debian/Ubuntu in this version*. |

3. **Refresh package metadata first** is ticked by default (`apt-get update` / `dnf makecache`).
   Leave it on, or the hosts update only to what they already knew about.
4. **Reboot afterwards:** **Never**, **Only if the update requires it**, or **Always**.
5. Choose the **Targets**, and for more than a few hosts a **Rollout** (one host first, then
   batches): updates are exactly the kind of change you want to try on one host first
   ([Canary Hosts](Canary-Hosts.md)).
6. Click **Preview changes**: a dry run per host listing *N to upgrade, N new, N to remove*. Nothing
   is changed. The preview uses each host's current package lists; the run refreshes them first, so a
   few more updates may appear then. New removals would still stop the run unless allowed.
7. If a host would remove packages, decide about **Allow these removals**.
8. **Run on …** and confirm. The confirmation repeats the mode, and whether hosts will reboot.

## What DROOL does on each host

1. Refreshes the package lists (unless you turned that off).
2. Simulates the update. If it would remove a package you didn't allow, the host stops, unchanged.
3. Installs the updates non-interactively, keeping your modified configuration files, without
   restarting services.
4. With **Only if the update requires it**: reboots if the host says a reboot is required
   (`/var/run/reboot-required` on Debian and Ubuntu, `needs-restarting` on the RHEL family). With
   **Always**: reboots. Then waits until the host comes back with its trusted key and a new boot id
   ([Rebooting Hosts](Rebooting-Hosts.md)).
5. Checks the host's status and updates count again.

## Things to know

- **Services keep running old code** until they restart. DROOL doesn't restart services during
  updates (on Ubuntu, *needrestart* is suspended for DROOL's runs). Reboot afterwards, or restart the
  affected services, when an update says so.
- **Kernel updates need a reboot.** Hosts that need one show **Reboot: required** on their page and in
  the **REBOOT** column of the Hosts list.
- **A busy package manager** (unattended-upgrades) makes DROOL wait and retry; see
  [Package Manager Busy](Package-Manager-Busy.md).
- **While a firewall change is pending** on a host (during its automatic-rollback window), DROOL
  refuses system updates and reboots there.
- **Every Sunday at 3:00?** Schedule it, with a check that your services still run afterwards:
  [Schedules](Schedules.md) and [Workflows](Workflows.md).

## Related

[Updating Packages](Updating-Packages.md) · [Rebooting Hosts](Rebooting-Hosts.md) ·
[Workflows](Workflows.md) · [Package Manager Busy](Package-Manager-Busy.md)
