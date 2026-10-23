# System updates

A system update installs all available updates on a host: the equivalent of `apt-get upgrade` or
`dnf upgrade`. DROOL previews it first, never removes packages without your permission, and can reboot
the host afterwards when the update requires it.

## Before you start

- You need **jobs.execute** (administrators and operators).
- The hosts need **APT or DNF**, sudo or a root login, and access to their repositories. Shepherd
  hosts need the **Managed** or **Full** preset.
- **Find out which hosts need updates:** the **Hosts** list has an **UPDATES** column (for example
  *1 (1 security)*), the Overview has an **Updates and reboots** card, and each host's **Overview**
  shows, under **Health now**, **Updates** *0 pending · checked 12 min ago*. DROOL checks every 6
  hours. A host that isn't reporting now (offline, or its status is old) shows its last known count,
  marked *last known*; the Overview's totals leave it out and say so.

## Install updates where you see them

**Install updates…** is wherever pending updates show: a host's header (in **Actions** on a phone),
**Updates** on the host's **Overview**, the **Hosts** list after selecting hosts, the Overview's
**Updates and reboots** card, and a Pack's page.
It opens one review:

1. DROOL asks each host's package manager what would change (a dry run: nothing changes) and lists
   it per host (*web-01 · apt · 2 to upgrade, 1 new*), with **Show packages**.
2. It says what follows: *No host reboots* (or which do), and, for two or more hosts, the recommended
   rollout (*1 canary host first, then batches of 5, up to 5 at a time, pause after 2 failures*). If a
   host would remove other packages, it says so, and the run stops there unless you tick **Allow these
   removals**.
3. **Options** has the [mode](#modes-and-options), **Refresh package metadata first** and **Reboot
   afterwards**, set as DROOL recommends: *Upgrade, refresh first, no reboot*.
4. **Install updates on web-01** (or *on 3 hosts*) starts the ordinary system update run. The same
   dialog shows how it goes (*Installing updates on web-01*, each host's step in words, such as
   *Waiting its turn* or *Rebooting*), then how it ended: *Updates installed on web-01*, or, when
   not every host updated, *1 updated · 1 failed* with each host's error. You can close it: the run
   continues, and **View the run** opens its page with the full output.
5. After the run, DROOL checks each updated host's pending updates again, and the dialog says what
   is left (*No pending updates left*). Until that check is in, it says so: the count DROOL shows is
   then from before the run.

To change the rollout, schedule the update or save it as a workflow, choose **open it in the run
composer** in the review: it opens **Runs → New run → System update** with the same hosts.

## Modes and options

**Runs → New run → System update** has every option:

1. Choose the **Mode**:

   | Mode | What it does |
   |---|---|
   | **Upgrade** | Upgrades installed packages without removing any (`apt-get upgrade` / `dnf upgrade`). Updates that need new or removed packages are held back on Debian/Ubuntu. |
   | **Full upgrade** | Also installs or removes packages when an upgrade needs it (`apt-get full-upgrade`; the same as Upgrade on DNF). If anything would be removed, the job stops unless you allow it. |
   | **Security updates only** | Only security updates (`dnf upgrade --security`). **RHEL family and Fedora only**: Debian and Ubuntu hosts fail with *security-only updates are not supported on Debian/Ubuntu in this version*. |

2. **Refresh package metadata first** is ticked by default (`apt-get update` / `dnf makecache`).
   Leave it on, or the hosts update only to what they already knew about.
3. **Reboot afterwards:** **Never**, **Only if the update requires it**, or **Always**.
4. Choose the **Targets**. For two or more hosts, the **Rollout** starts as DROOL's recommended one
   (one host first, then batches): updates are exactly the kind of change you want to try on one host
   first ([Canary Hosts](Canary-Hosts.md)). **Change rollout…** adjusts it.
5. Click **Preview changes**: a dry run per host listing *N to upgrade, N new, N to remove*. Nothing
   is changed. The preview uses each host's current package lists; the run refreshes them first, so a
   few more updates may appear then. New removals would still stop the run unless allowed.
6. If a host would remove packages, decide about **Allow these removals**.
7. Click **Review: run on …**. DROOL's review repeats the mode, what the preview found, how the run
   rolls out, and whether hosts will reboot. Click **Start run on …**.

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
