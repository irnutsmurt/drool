# Host monitoring

Every host has its own monitoring page with its current health, history charts, services, disks,
sensors, its health settings and how it's monitored. Open it from **View monitoring →** on the host's
page, by clicking a host on **Monitoring**, or by clicking any tile of the host's **Health** card.

## Before you start

Everyone can view it (**monitoring.read**). Buttons that change something say what they need.

## The page

![nas-01's monitoring page, Overview tab: continuous Shepherd monitoring, open alerts, tiles for CPU, memory, storage (96% /srv/media), temperature, network, services (3/3 monitored as expected; 1 failed, not monitored) and storage devices, and the active health issues](images/monitoring-host-overview.png)

- **The title line** says how the host is monitored and how fresh the data is: *Continuous: the
  Shepherd samples every 10 s · updated just now*, or *Polled over SSH every 5 min (not continuous)*.
- **Collect now** fetches fresh data right away instead of waiting for the next sample or poll
  (**monitoring.manage**: administrators and operators).
- **Open alerts** for this host, if any. A host in maintenance shows a banner with its reason and
  **End maintenance** ([Maintenance Mode](Maintenance-Mode.md)).

## The tabs

| Tab | What's there | More |
|---|---|---|
| **Overview** | Tiles for CPU, Memory, Storage, Temperature, Network, Services and Storage devices, and the host's **Active health issues** | — |
| **Metrics** | Charts for the last hour, 6 hours, 24 hours, 7 days or 30 days | [CPU, Memory and Load](CPU-Memory-and-Load.md), [Filesystems and Disk Space](Filesystems-and-Disk-Space.md) |
| **Services** | Every systemd service, its state and history; **Monitor** (DROOL alerts only on the services you monitor) and **Customize…** (expected running, stopped or ignored); **Actions…** to start, stop, enable or disable it | [Service Monitoring](Service-Monitoring.md) |
| **Storage health** | Each disk's own health report, its partitions, and kernel storage errors | [Storage Health](Storage-Health.md) |
| **Sensors & GPU** | Temperatures with the hardware's own limits; GPU use, memory, temperature, power | [Temperatures](Temperatures.md), [GPUs](GPUs.md) |
| **Health settings** | Which checks run on this host, their thresholds, and where each setting comes from; **Customize** | [Health Overrides](Health-Overrides.md) |
| **Collection & helpers** | How this host is monitored, optional helper tools, and (Shepherd hosts) the Shepherd's resource budget | [Monitoring Through SSH vs Shepherd](Monitoring-Through-SSH-vs-Shepherd.md) |

## Reading the tiles

Each tile shows a symbol and a value: ✓ fine, ⚠ warning, ✕ critical, **?** unknown, ℹ informational.
**?** isn't a problem by itself: it means DROOL can't tell, for example *No sensors — This host exposes
no temperature sensors (normal for VMs and containers)*, or *Health unknown — At least one device's
health can't be read*. See [What "Unknown" Means](What-Unknown-Means.md).

The **Services** tile counts the services you monitor and whether they're as expected (*3/3 monitored
as expected*), or says *None monitored*. Failed services nobody monitors are a note on it (*2 failed,
not monitored*), never a warning ([Service Monitoring](Service-Monitoring.md)).

## Monitoring changes nothing

Looking at a host changes nothing on it, installs nothing and sends nothing anywhere. Monitoring
only *evaluates* health; telling people about problems is the job of [Alerts](Understanding-Alerts.md).

## Related

[Fleet Monitoring](Fleet-Monitoring.md) · [Host Details](Host-Details.md) ·
[Monitoring Settings](Monitoring-Settings.md)
