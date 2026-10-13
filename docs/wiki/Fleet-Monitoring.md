# Fleet monitoring

**Monitoring** in the sidebar answers one question for all your hosts at once: *which machines need
attention, and why?* It's the place to start when the Overview shows a problem, and the place to look
over now and then.

Monitoring needs no setup: it starts by itself for every host you add, without installing anything
on it.

## Before you start

Everyone who can sign in can see monitoring (**monitoring.read**). The buttons that change something
(installing helpers, converting hosts to Shepherds) need more; see below.

## The page

![The Monitoring page: 6 of 6 hosts reporting, alert counts, filter buttons (Needs attention 3, CPU 1, Low disk space 1, Storage health 1, Service failures 2…), the host table with health, alerts, monitoring method, CPU, memory, disk, temperature, services and storage devices, and the list of active health issues](images/monitoring-fleet.png)

From the top:

- **How many hosts report**, and how: *6 of 6 hosts reporting: 3 with a Shepherd (agent, continuous),
  3 over SSH (polled)*. See [Monitoring Through SSH vs Shepherd](Monitoring-Through-SSH-vs-Shepherd.md).
- **Two tabs:** **Fleet health** (this page) and **Health checks and settings**
  ([Monitoring Settings](Monitoring-Settings.md)).
- **Alerts:** open alerts by severity, how many are acknowledged, and how many hosts are in
  maintenance. Click to open [Alerts](Understanding-Alerts.md).
- **Filter buttons** with counts: **Needs attention**, **Unreachable / stale**, **CPU**, **Memory**,
  **Low disk space**, **Storage health**, **Service failures**, **Interfaces**, **Temperature**,
  **Reduced fidelity**, **Missing helpers**, **No data yet**, **All hosts**. Click one to list just
  those hosts.
- **The host table:** search by name (**Host**), choose **Sort by** (*Needs attention first*, *Name*,
  *CPU*, *Memory*, *Disk space used*, *Temperature*). Each row shows:
  - **Health** — *Healthy*, or the number of issues with the worst severity;
  - **Alerts** — open alerts, and *maintenance* if the host is in maintenance;
  - **Monitoring** — *Shepherd · continuous* or *SSH · polled*, plus notes such as *reduced
    fidelity* or *helpers would help: nvme-cli*;
  - **CPU**, **Memory**, **Disk** (the fullest filesystem, e.g. *96% /srv/media*), **Temp.**
    (the hottest sensor), **Services** (*50 ok*, or *1 failed*), **Storage devices** (disk health:
    *Warning*, *Health unknown*…);
  - **Updated** — how old the data is.
- **Active health issues:** every problem across the fleet, most severe first, with when it started,
  for example *Critical — nas-01 — /srv/media is over 95% full — 327.7 GiB free of 8.0 TiB*.

Click a host to open its monitoring page ([Host Monitoring](Host-Monitoring.md)).

## Acting on several hosts

Tick hosts in the table, then:

- **Install recommended helpers** — installs the optional tools that would give DROOL more detail on
  those hosts (`smartmontools`, `nvme-cli`, `lm-sensors`), after a preview with counts. It runs as an
  ordinary package run. Needs **monitoring.manage** and **jobs.execute** (administrators and
  operators). See [Storage Health](Storage-Health.md#optional-helpers).
- **Convert to Shepherd** — installs Shepherds on SSH hosts, carefully (one host first, then batches,
  pausing on a failure). Needs administrator rights. See [Installing a Shepherd](Installing-a-Shepherd.md#many-ssh-hosts-at-once).

## What "health issue" means

A **health issue** is a problem that has lasted long enough to count: CPU above 95% for 5 minutes, a
filesystem over 85% on two checks in a row, a service that stayed failed for a minute. Short spikes
never become issues, and an issue clears only once the condition has properly recovered, so issues
don't flap. Warning and Critical issues become [alerts](Understanding-Alerts.md); *Info* issues (such
as reduced monitoring fidelity) are shown here only.

What counts as a problem is adjustable per fleet, Pack, host or device
([Health Overrides](Health-Overrides.md)).

## Next

**[Understanding Alerts](Understanding-Alerts.md)**: be told when a host needs attention, instead of checking this page.

## Related

[Host Monitoring](Host-Monitoring.md) · [What "Unknown" Means](What-Unknown-Means.md) ·
[Monitoring Is Stale](Monitoring-Is-Stale.md)
