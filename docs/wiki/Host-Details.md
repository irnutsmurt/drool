# Host details

Click a host's name anywhere in DROOL to open it. Everything about one host is in one place: a
header that stays at the top, and the host's sections below it, each at its own address. The first
section, the **Overview**, is a summary built to answer three questions in order: *what needs
attention?*, *is this host okay?*, and *what happened lately?*. Every summary links to the section
with the details.

![web-01's Overview: the header (web-01; Ubuntu 24.04.5 LTS, 10.10.40.21, managed via SSH; Connection Ready, Last check online, Health Degraded · 1 issue, 1 open alert) with Run command, Install updates and Actions; the sections Overview, Monitoring, Files, Network, Firewall, Desired State, Management and Activity; Needs attention (/ 91% full, the open alert, 2 drifted resources); Health now with its tiles; the Alerts card; the Desired State, Firewall, Network and Files summaries; Recent runs, Recent events and the System card](images/host-page-web-01.png)

## The top of the page

The header is the same in every section of the host:

- **The host's name.**
- **What the host is**: the operating system and address, for example
  `Debian GNU/Linux 12 (bookworm)` and `10.10.20.15`, and how DROOL manages it (*Managed via*
  **SSH** or **Shepherd**).
- **Its state, each part named**, because they answer different questions:
  - **Connection**: can DROOL sign in and manage this host? **Ready** means yes, and only that: a
    *Ready* host can still be unhealthy.
  - **Last check**: did the latest status check reach the host? **online**, **offline**, **stale** or
    **not checked yet**.
  - **Health**: how monitoring sees the host. **Healthy**, **Degraded** (its worst issue is a
    warning), **Unhealthy** (something is critical), with the number of issues, or **Stale** and
    **No data** when there is nothing recent to judge by.
  - Then, when there are any, its open alerts, maintenance, and a run working on it now.

  All the words are explained in [Status Meanings](Status-Meanings.md#host-statuses). When something is
  wrong with the connection, a coloured note under the header says what it means and what to do.
- **Buttons** (what you see depends on your role): **Run command**, **Install updates…**, and
  **Actions**, a menu with the rest:

  | Button | What it does |
  |---|---|
  | **Run command** | Opens a new run with this host already selected ([Running a Command](Running-a-Command.md)). A host that doesn't work yet shows **Test connection** here instead |
  | **Install updates…** | The review of a system update of this host (its dry run, what follows), then the run, followed in place ([System Updates](System-Updates.md#install-updates-where-you-see-them)). On a phone it's under **Actions** |
  | **Actions → Reboot…** | A new run that reboots this host and waits for it ([Rebooting Hosts](Rebooting-Hosts.md)) |
  | **Actions → Refresh inventory** | Collects inventory and status now instead of at the next scheduled check |
  | **Actions → Test connection** | The step-by-step connection test ([Testing a Connection](Testing-a-Connection.md)) |
  | **Actions → Start maintenance…** | Pauses notifications for this host during planned work ([Maintenance Mode](Maintenance-Mode.md)) |
  | **Actions → Edit host details** | Change the connection settings, tags or description ([Adding a Host](Adding-a-Host.md#changing-a-host-later)) |
  | **Actions → Remove host…** | From DROOL only, or cleaning up what DROOL installed there first ([Removing a Host](Removing-a-Host.md)) |

  The buttons that open a new run don't do anything yet: you review the run first and start it there.
- **The sections**, in a row under the header (on a phone, the **Section** list): **Overview**,
  **Monitoring**, **Files**, **Network**, **Firewall**, **Desired State**, **Management** and
  **Activity**. You see the ones your role may open. A jump host has no **Monitoring**, **Files**,
  **Network** or **Desired State**: DROOL doesn't manage it.

## The Overview

| Part | What it shows | Details |
|---|---|---|
| **Needs attention** | Everything about this host that wants a person, most serious first, for example *Desired State: 2 drifted*, each with a link. When there's nothing, it says *Nothing needs attention on this host right now.* | — |
| **Health now** | Tiles for CPU, Memory, Storage, Temperature, Network, Services, Storage devices and Active health issues, how the host is monitored (*Polled over SSH every 5 min* or *Continuous: the Shepherd samples every 10 s*), pending updates, whether a reboot is required, the last status check, and uptime | **Services →**, **Monitoring →** ([Host Monitoring](Host-Monitoring.md)) |
| **Alerts** | Open alerts for this host; **History**; **Start maintenance** | [Understanding Alerts](Understanding-Alerts.md), [Maintenance Mode](Maintenance-Mode.md) |
| **Desired State** | Whether the host matches its baselines, and what differs | **Open →** ([Drift](Drift.md)) |
| **Firewall** | The firewall status and mode, and whether DROOL filters this host | **Open →** ([Firewall Modes](Firewall-Modes.md)) |
| **Network** | How connections are observed, a count of observed connections, and how many aren't in the policy or are denied | **Open →** ([What DROOL Can See](What-DROOL-Can-See.md)) |
| **Files** | How many files DROOL deployed here, and the first few, each linking to its deployment | **Open →** ([Browsing Files](Browsing-Files.md), [Rolling Back a File](Rolling-Back-a-File.md)) |
| **Recent runs** | The last runs that included this host | **Activity →** |
| **Recent events** | What happened lately: alerts opened, acknowledged or recovered, maintenance | **Alert history →** |
| **System** | OS, kernel, hostname, CPU, memory, package manager, init system, virtualization, when it was discovered, and (expandable) filesystems, addresses and interfaces | — |

While a host waits for its SSH key to be approved, the Overview starts with **Approve the host key**
([SSH Host-Key Verification](SSH-Host-Key-Verification.md)).

## The other sections

| Section | What it holds | Guide |
|---|---|---|
| **Monitoring** | Metrics, services, storage health, sensors, health settings and how the host is monitored | [Host Monitoring](Host-Monitoring.md) |
| **Files** | The file manager for this host | [Browsing Files](Browsing-Files.md) |
| **Network** | The connections this host makes and accepts, and what it listens on | [What DROOL Can See](What-DROOL-Can-See.md) |
| **Firewall** | The policy in effect here, the mode, enforcement and history | [Firewall Modes](Firewall-Modes.md) |
| **Desired State** | What should stay as defined here: drift, conflicts, restore | [Drift](Drift.md) |
| **Management** | How DROOL reaches the host, and what it belongs to (the cards below) | [SSH vs Shepherds](SSH-vs-Shepherds.md) |
| **Activity** | **Runs** that included this host, **Schedules** that may target it, and its **Audit log** | [Understanding Run Results](Understanding-Run-Results.md) |

The **Management** section has **Edit host details** at the top, and these cards:

| Card | What it shows |
|---|---|
| **Connection** | SSH or Shepherd, the fallback, what DROOL uses right now (*Operations now*); **Test connection**, and the buttons that switch between SSH and the Shepherd |
| **SSH access** | The SSH user and address, credential, sudo setting (*Privilege*), jump host (*Connect through*), trusted host key, last test, and how DROOL signs in, with **Set up a dedicated DROOL SSH account…** and the SSH certificate buttons |
| **Shepherd (DROOL agent)** | Whether the host has a Shepherd, its version, state and certificate; buttons to install, update, uninstall or revoke it |
| **Membership** | Packs (each links to its page; **Change…** adds the host to Packs or removes it), tags, description |
| **Danger zone** | **Remove host…** (also under **Actions**) ([Removing a Host](Removing-a-Host.md)) |

A Shepherd host's **Shepherd (DROOL agent)** card also has **Shepherd details**: its protocol,
identity, certificate, trust manifest, when it was last seen and from where, and **What DROOL may do
on this host** (its capabilities, with **Narrow…** to restrict them further). See
[Shepherd Capability Presets](Shepherd-Capability-Presets.md).

## How fresh is this information?

- **Status** (online, uptime, load, disk, failed services, reboot required): checked every 5 minutes.
- **Monitoring**: every 5 minutes over SSH, or every 10 seconds through a Shepherd. Each summary says
  when it was last updated (*updated 2 min ago*).
- **Updates**: checked every 6 hours (*checked 12 min ago*).
- **Inventory** (the System card): when the host was added, and again with **Refresh inventory**.

**Actions → Refresh inventory** fetches status and inventory right away.

## Related

[Adding a Host](Adding-a-Host.md) · [Status Meanings](Status-Meanings.md) · [Host Monitoring](Host-Monitoring.md)
