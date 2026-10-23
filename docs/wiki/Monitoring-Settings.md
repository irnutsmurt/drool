# Monitoring settings

**Monitoring → Health checks** decides what monitoring considers unhealthy on every host. Under
**Advanced** are how often hosts are observed, how long history is kept, the Shepherd resource budget
and which helper tools DROOL may install. Most installations never need to change anything here.

![Monitoring → Health checks: the health checks for every host (the Recommended profile, with Disk space changed for every host) and the inheritance chain Recommended → Global → Pack → Host → Device, each check with its value, source and Customize… or Edit… and Reset; below, the services watched on every host, the settings of Packs, hosts and devices, and Advanced](images/monitoring-settings.png)

## Before you start

Everyone can read these settings. Changing them needs **monitoring.settings** (administrators).
Everything you change is in the audit log.

## Health checks for every host

Every check is listed with what it does now and where that comes from; **Customize…** next to one
changes it for the whole fleet. Out of the box every host uses the **Recommended** profile.
Temperatures use each sensor's own hardware limits; high GPU utilization is off.

Settings inherit **Recommended → Global → Pack → Host → Device**, and the most specific one wins. How
to change any level is explained in [Health Overrides](Health-Overrides.md). The quickest way is often
from the alert itself: **Adjust when this alerts…**.

Below the checks:

- **Monitored services on every host**: the services DROOL watches everywhere
  ([Service Monitoring](Service-Monitoring.md)).
- **Set for Packs, hosts and devices**: every setting made below the fleet level, with where it's set
  and **Reset…** (it asks first), and each Pack's **monitoring priority**.

This decides what is *unhealthy*. Who gets told about it is separate:
[Notification Routing](Notification-Routing.md).

## Advanced: collection, history, Shepherd resource budget, SSH polling and helpers

**▸ Advanced** opens the settings about *how* hosts are observed. It stays open in the page's address,
so a link can lead straight to it.

- **Monitor hosts** — switch monitoring on or off for the whole fleet. *Shepherds stream continuously;
  SSH hosts are polled.* Turning it off asks first: once saved, no host is observed, health stays as
  last seen and is shown as stale, and no new alert opens. Several features depend on it: Desired
  State reads services from monitoring, and workflows' *Verify services* and *Verify health* steps
  need it.
- **Keep hourly history for (days)** — default 30. One-minute values are kept for about a day and
  five-minute averages for eight days.

**Shepherds (DROOL agents, continuous):**

| Setting | Default |
|---|---|
| CPU, memory, network, disk IO every (s) | 10 |
| Filesystems, sensors, GPU every (s) | 60 |
| Services every (s) | 30 |
| Storage devices every (s) | 300 |
| SMART / NVMe health every (s) | 1800 (30 minutes) |
| Offline buffer (KB) | 2048: what a Shepherd keeps in memory while DROOL is unreachable (about a day for a typical host). When full, the oldest routine samples go and the gap is reported |

Empty fields use the defaults DROOL measured as reasonable.

**Default Shepherd resource budget** — **CPU quota (% of one CPU)** (25), **CPU weight** (20), **Memory
high (MB)** (96), **Memory max (MB)** (160). The budget limits monitoring only; package installs and
other operations run outside it. One host can get its own budget on its **Collection & helpers** tab.

**SSH hosts (polled):**

- **Poll every (s)** — default 300 (5 minutes).
- **Storage health every (s)** — default 3600 (hourly).
- **Read SMART and kernel storage messages with sudo where the host allows it** — only during the
  storage health check, *so routine polls don't fill the host's auth log*.

Click **Save collection settings**.

### Monitoring helper approvals

*A standing approval lets DROOL install an allowlisted helper (smartmontools, nvme-cli, lm-sensors)
wherever it would add monitoring detail, as a normal package job on your behalf.* Choose the
**Helper**, **Where** (*Every host* or *A pack*) and click **Approve**. Without an approval, helpers
are installed only when someone clicks **Install**. Removing an approval never uninstalls anything.

## Related

[Health Overrides](Health-Overrides.md) · [Monitoring Through SSH vs Shepherd](Monitoring-Through-SSH-vs-Shepherd.md) ·
[Storage Health](Storage-Health.md#optional-helpers)
