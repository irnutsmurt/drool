# Service monitoring

DROOL keeps track of every systemd service on every host: whether it's running, stopped or failed,
whether it starts at boot, and when its state changed. It **alerts only on the services you
monitor**: tick **Monitor** on a service and DROOL tells you when it stops or fails. A service nobody
monitors is listed with its state (failed included) and never raises a health issue or an alert.

## Where to look

- **Monitoring** (fleet): the **Services** column (*50 listed*, or *✕ 1 monitored problem*; a
  failure nobody monitors shows as *1 failed, not alerting*) and the **Service failures** filter.
- A host's **Services** tile, on its **Overview** and on its monitoring **Summary**: *3/3 monitored
  as expected*, or *None monitored*, with failed services nobody monitors as a note.
- A host's monitoring page, **Services** tab: the full list.

![nas-01's Services tab: a find box, the filters All, Running, Stopped, Failed, Enabled, Disabled and Monitored with counts, Show all units (advanced), and the services with their state, boot setting, since, restarts, a Monitor box, Actions… and a ⋯ menu: backup.service failed but not monitored, so it doesn't alert; jellyfin.service and smbd.service monitored; plex.service expected to be stopped (⚙ Customized)](images/monitoring-services.png)

## The Services tab

- **Find a service…** narrows the list by name or description.
- **Filters** with counts: **Running**, **Stopped**, **Failed**, **Enabled**, **Disabled**, **Monitored**,
  **All**.
- **Show all units (advanced, N hidden)** — by default DROOL hides the noise (one-shot units, units that
  are meant to come and go); tick it to see everything.
- Next to the filters: where the list comes from (*Polled over SSH* or *Reported by the Shepherd*)
  and how old it is.
- **The table:** **Service**, **State** (*Running*, *Stopped*, *Failed* or *Unknown*, with systemd's
  own words such as `active/running`, and a line saying whether it's a problem and why),
  **Enabled** (the boot setting, e.g. `enabled` or `static`), **Since**, **Restarts** (automatic
  restarts since it was last started by hand), **Monitor**.
- **On each row:** **Actions…**: start, stop, restart, enable or disable it
  ([Managing Services](Managing-Services.md#from-a-hosts-services-tab)); and **⋯**, a menu with
  **Customize monitoring…** (what DROOL expects of the service, and how long a problem lasts before
  it alerts) and **State history** (every recorded state change). A service you customized shows
  **⚙ Customized** under its **Monitor** box; click it to open the same settings.

## Monitor

**Ticked:** DROOL expects the service to run on this host. Stopped, failed or not installed is then a
health issue (and an alert): *failed* after 1 minute, *not running* after 2 minutes.

**Not ticked:** the service is inventory only. It's listed with its state and history, a failed one
shows as *Failed* (in grey, with *Failed, but not monitored, so it doesn't alert*), and it never
raises a health issue, an alert, a warning on the Overview or a *Needs attention* item.

A small line under the box says when it isn't that simple:

| It says | Means |
|---|---|
| *from pack media-servers* / *set for every host* | Monitored because a Pack or every-host setting says so |
| *expected stopped* | Monitored the other way round (below): running is the problem |
| *not monitored here (from pack media-servers monitors it)* | This host is an exception to its Pack |
| *ignored on this host* | Never alerts here, whatever a Pack or every host says |

- Ticking and unticking needs **monitoring.manage** (administrators and operators). Unticking a
  service a Pack or every host monitors makes an exception for this host, which only administrators may
  (**monitoring.settings**): the box is greyed out for others, with the reason.
- DROOL keeps collecting the state of every service whatever you set.
- Stopping or starting a service never changes whether it's monitored
  ([Managing Services](Managing-Services.md#the-expected-state-stays-as-it-is)).

## Customize monitoring… (the expected state)

Behind the box is the service's **expected state**. **⋯ → Customize monitoring…** on its row sets the
advanced ones, for this host:

| Expected | Stopped | Running | Failed |
|---|---|---|---|
| **None** (not monitored) | listed, never a problem | listed | listed as failed, never a problem |
| **Running** (the Monitor box) | a problem (*Not running, though expected to run*) | fine | a problem (critical) |
| **Stopped** | fine (*Not alerting: expected to be stopped*) | a problem (*Running, though expected to be stopped*) | fine |
| **Ignored** | shown, never a problem | shown, never a problem | shown, never a problem |

- **Reset to inherited** takes this host's own setting away: the host then follows its Packs and the
  every-host setting, or isn't monitored.
- **Ignored** needs **monitoring.settings**: it stops what a Pack or every host monitors.
- **How long before it alerts, or turning one of its checks off…** opens the service's health checks
  (for example *Failed for at least 10 minutes*). Changing them shows **⚙ Customized** on the row.
- For all of a Pack's hosts, set services on the Pack's page (**Monitored services**); for every host,
  on **Monitoring → Settings** (**Monitored services on every host**). The most specific setting wins:
  this host, then its Packs, then every host.

## Acknowledge, snooze, or ignore

Three different answers to an alert about a service, from the alert's row on **Alerts** (**Actions…**)
or the alert's page:

- **Acknowledge** — *I've seen it.* No more reminders; the alert stays open until the service is
  fine again ([Acknowledging an Alert](Acknowledging-an-Alert.md)).
- **Snooze** — *not now.* Put the host in [maintenance](Maintenance-Mode.md) until a time you
  choose: no notifications meanwhile, health is still recorded, and what's still wrong when it ends is
  notified then.
- **Ignore** — *this is on purpose.* Ignore the service on this host, or expect the state it's in
  (*Expect backup.service to be stopped*). Then it isn't a problem at all: the alert closes as
  *cleared by configuration*, not as a recovery, and nothing alerts later.

## How quickly DROOL notices

- **Shepherd hosts:** services are sampled every 30 seconds, and changes reported within about a
  minute.
- **SSH hosts:** every 5 minutes. Where DROOL's account can't ask systemd itself (a host without D-Bus,
  such as DietPi), DROOL reads the services as root once an hour through the host's sudo, and the tab
  says so; routine polls never use sudo.

## Updating from an earlier DROOL

Earlier versions alerted on every failed service, monitored or not. After the update, those alerts
close by themselves as *cleared by configuration* (*… isn't monitored: DROOL shows its state, and alerts
on a service only when it's monitored*), never as recoveries. Services you had set to Running, Stopped
or Ignored keep their settings; services in a host's old *watched services* are monitored.

## If the Services tab says services aren't available

| It says | What it means |
|---|---|
| *PID 1 is …, not systemd: no service inventory.* | The host doesn't run systemd (a container, another init system). Nothing to monitor here |
| *systemd runs, but systemctl can't reach it as the account DROOL signs in with (…)* | systemd runs, but only root may ask it on this host (no D-Bus system bus, as on DietPi). DROOL reads the list as root with the hourly check where the host's sudo allows it (*Read as root with the hourly storage check …*); give DROOL's account sudo, or install a Shepherd |
| *systemctl isn't installed on this host* | No systemctl at all |

## Related

[Managing Services](Managing-Services.md) · [Service Won't Start](Service-Wont-Start.md) ·
[Health Overrides](Health-Overrides.md) · [Desired State](What-Desired-State-Is.md) (keep a service
running and enabled, and restore it)
