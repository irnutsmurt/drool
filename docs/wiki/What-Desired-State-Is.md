# What Desired State is

**Desired State** answers three questions: *What should this machine look like? Is it still that way?
And how do I safely put it back?*

You write down what a machine should have in a **baseline**, for example *Server Defaults*:

- the package `openssh-server` installed, and `telnet` **not** installed;
- `/etc/motd` with exactly this content, owned by `root:root`, mode `0644`.

DROOL then keeps checking every host the baseline applies to. When a host no longer matches (someone
installed `telnet` on `web-01`, or edited `/etc/motd` on `media-02`), DROOL shows it as **drift**,
exactly what differs, and lets you decide: put it back, keep the change for that host, or change the
baseline. Nothing on a host ever changes until someone (or, if you turn it on,
[automatic restore](Automatic-Restore.md)) chooses to restore it.

## What a baseline can say

| Kind | What you can require |
|---|---|
| **Service** | Running or stopped, and/or enabled or disabled at boot (or *Leave alone* for either) |
| **Package** | Installed, or **not installed (prohibited)**. No versions |
| **File** | Exact content, and optionally owner, group and permissions; a validation command (for example `sshd -t -f {path}`) and a service to reload or restart after it changes |

That's all. There are **no scripts, expressions or conditions**: DROOL doesn't evaluate statements like
"SSH root login must be disabled", but it can manage the configuration file that does it. Users, cron
jobs, kernel settings, containers and package versions can't be managed. The firewall isn't part of
Desired State either: it has its own [policy](What-DROOLs-Firewall-Does.md), and the Desired State
pages link to it.

## The words DROOL uses

| Status | Meaning |
|---|---|
| **Compliant** | DROOL checked, and the host matches |
| **Drift detected** | The host doesn't match: something differs ([Drift](Drift.md)) |
| **Conflict** | Two of the host's Packs say different things about the same resource, with equal priority. DROOL doesn't pick one ([Conflicts](Conflicts.md)) |
| **Unknown** | DROOL couldn't check right now: the host is unreachable, its data is too old, or monitoring is off. **Unknown is never treated as compliant** ([What "Unknown" Means](What-Unknown-Means.md)) |
| **Restoring…** | A correction is running |
| **Restore failed** | A correction ran but DROOL didn't see the result it wanted, with the reason |

## Where to find it

- **Desired State** in the menu: **Overview** (which hosts need attention), **Baselines** and
  **History**.
- A host's **Overview** has a **Desired State** tile, and its **Desired State** section shows
  everything managed on that host, where each desired value comes from, and its history.
- The **Overview** page lists hosts with drift among the things that need attention.

![Desired State → Overview: 2 hosts with drift (3 resources), 0 conflicts, 0 failed restores, 6 resources that can't be verified right now; the firewall policy link; and the hosts needing attention: media-02 and web-01 with drift, gpu-01 and nas-01 unknown because their host keys aren't approved](images/desired-overview.png)

## How DROOL checks

- **Services** come from DROOL's monitoring, so [monitoring must be on](Monitoring-Settings.md#collection).
  Shepherds report changes within about a minute; SSH hosts are polled every 5 minutes.
- **Packages** are checked with a read-only query of just those packages: no updates, no root needed.
- **Files** are checked by size, owner, group, permissions and a SHA-256 checksum computed on the
  host. Their content is only copied to DROOL when you [review](Review-Difference.md) or
  [accept](Accept-Current-State.md) it.
- Packages and files are checked **every 15 minutes** per host, and sooner after a change, a
  reconnect, a correction, or **Check now** (on a host) / **Check all now** (on the Overview).

## Before you start

| What | Who |
|---|---|
| See baselines, desired and current state, drift and history | Everyone (**desired.read**) |
| **Check now** and [restore managed state](Restore-Managed-State.md) | Administrators and operators (**desired.restore**), who also need **jobs.execute**, and for files **files.deploy** (and **files.elevate** for files that need root) |
| Create and change baselines and Pack priorities, [accept current state](Accept-Current-State.md), set or reset a host's own values | Administrators (**desired.manage**) |
| Turn [automatic restore](Automatic-Restore.md) on or off | Administrators (**desired.auto**) |

Everything is recorded in the [audit log](Audit-Log.md): baselines and priorities, accepting,
restoring (by hand and automatically), reviewing file contents, and automatic-restore changes.

## Next steps

1. [Create a baseline](Creating-a-Baseline.md).
2. Decide [where it applies](Applying-a-Baseline.md).
3. When something drifts: [review the difference](Review-Difference.md), then
   [restore managed state](Restore-Managed-State.md) or [accept the current state](Accept-Current-State.md).

## Related

[Drift](Drift.md) · [Conflicts](Conflicts.md) · [Automatic Restore](Automatic-Restore.md)
