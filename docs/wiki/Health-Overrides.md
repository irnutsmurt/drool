# Health overrides

Out of the box, every host uses the **Recommended** health profile, and most fleets never need more.
But a gaming PC, an AI box, a NAS and a Raspberry Pi can have good reasons to differ: the AI box's GPU
*should* be busy; the NAS's media disk *should* be nearly full. Any single health setting can be
changed exactly where it matters, without copying the rest.

## Where settings come from

Settings inherit through five levels, broadest first. The most specific one wins:

**Recommended → Global → Pack → Host → Device**

| Level | Example | Where to change it |
|---|---|---|
| **Global** (every host) | "Disk space: warn at 80%" | **Monitoring → Health checks → Customize…** |
| **Pack** | `media-servers`: no high-CPU warning while transcoding | The Pack's page **→ What this Pack sets → Health checks…** |
| **Host** | `gpu-01`: high GPU utilization on | The host's **Monitoring → Health checks → Customize…** |
| **Device** | `/srv/media` on `nas-01`: warning 92%, critical 98% (while `/` keeps 85% / 95%) | **Customize health check** next to that filesystem, disk, sensor, GPU or service |

**From an alert:** **Adjust when this alerts…** in the alert's panel (Alerts → **Actions…**) or on its
page shows the rule that fired and where it comes from, then lets you change it for that device or
service, the host, one of the host's Packs or every host, in the same place. Each choice shows what
it's set to now; a choice your role can't change is greyed out.

You change only what you need; everything else keeps coming from the level above. Every setting
shows where it comes from: *Recommended*, *Hardware limit*, *Global*, *Pack: media-servers*,
*This host*, *Device: /srv/media*.

![nas-01's Monitoring → Health checks: global custom settings with 3 overrides for individual devices or services, its Pack media-servers with its monitoring priority, the inheritance chain, and every check with its current value, source and a Customize… button](images/monitoring-health-settings.png)

## Before you start

- **Administrators** (**monitoring.settings**) can change everything.
- **Operators** (**monitoring.manage**) can change a host's or device's settings only to make detection
  **stricter** (a lower threshold, a shorter wait). Anything that makes detection *less* sensitive
  (turning a check off, raising a threshold, waiting longer) and every global or Pack change needs an
  administrator.

## Change a setting

1. Open the right level (table above) and click **Customize…** next to the check, for example
   **High CPU utilization** on `media-servers`. Inside a Pack's or a device's checks, the check opens
   in the same window (**Back** returns to the list).
2. Type a value into the fields you mean to change: on/off, warning and critical levels, how long a
   condition must last. An empty field inherits; it shows the value it inherits and from where. When
   a condition counts as recovered is under **More options**. A yes/no check (like a SMART failure)
   only has on/off.
3. For fleet-wide and Pack changes, DROOL first shows the **impact**: how many hosts are affected, how
   many are above the old and the new threshold, and which open problems would close. *This turns a
   check off: matching conditions won't be reported for these hosts* appears when that's the case.
4. Click **Save**.

**Reset to inherited** undoes a change for one field; **Reset all to inherited** for the whole check.
Nothing is copied back: the setting simply follows the level above again.

## What happens when you change something

- **Changes apply at once.** A problem that still qualifies stays the same alert.
- A problem that no longer qualifies (its check turned off, or its threshold now above the current
  value) closes as **Cleared by configuration**. Its message says *CLEARED BY CONFIGURATION*, never
  *RECOVERED*, because the machine itself didn't change. See [Recovered vs Cleared by Configuration](Recovered-vs-Cleared-by-Configuration.md).
- A stricter rule starts counting from the change: lowering a threshold never claims a problem
  existed for the past five minutes.

## Useful to know

- **Checks are independent.** Raising `/srv/media`'s space threshold never weakens SMART or kernel
  error detection for the disk under it; turning off high GPU utilization never affects GPU
  temperature.
- **Devices are recognised by their own identity**: a filesystem by its UUID, a disk by its WWN or
  serial number, an NVIDIA GPU by its UUID, a service by its unit name. A setting stays with the
  device when it's renamed, remounted or reordered. If a customised device disappears, its setting
  is kept (shown as *currently unavailable*) and applies again if it returns; it never moves to a
  different device. Network interfaces are identified by name only.
- **Hosts in several Packs:** if two Packs set the same thing differently, the Pack with the higher
  **monitoring priority** wins. With equal priorities, DROOL doesn't choose: the host's **Health
  checks** say which Packs disagree about which value and what applies meanwhile (the fleet-wide
  value), with **Let ⟨Pack⟩ win…** for each. That raises the Pack's priority just above the other's
  after showing what changes on every host in both; or set the value on the host itself.
- **Per-device settings belong to one host.** A Pack can't target "/srv/media on every member".

## Health versus notifications

Health checks decide *whether something is a problem*. "Don't warn me about GPU load on gpu-01" is a
health setting; "send gpu-01's critical alerts only to Discord" is a
[notification route](Notification-Routing.md).

## Related

[Monitoring Settings](Monitoring-Settings.md) · [Understanding Alerts](Understanding-Alerts.md) ·
[Packs](Packs.md)
