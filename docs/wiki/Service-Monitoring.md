# Service monitoring

DROOL keeps track of every systemd service on every host: whether it's running, stopped or failed,
whether it starts at boot, and when its state changed. What counts as a problem depends on the
service's **expected state**: a service that should run and doesn't is a health issue (and an alert);
a service you stopped on purpose isn't.

## Where to look

- **Monitoring** (fleet): the **Services** column (*50 ok*, *1 failed*) and the **Service failures**
  filter.
- A host's **Services** tile on its monitoring Overview (*32/32 healthy*).
- A host's monitoring page, **Services** tab: the full list.

![nas-01's Services tab: filters All, Running, Stopped, Failed, Enabled and Disabled with counts, the "Show all units" switch, and the services with their state, boot setting, since, restarts, expected state and Actions…: backup.service failed (a health issue), jellyfin.service and smbd.service expected to run (set on this host), plex.service stopped and expected to be stopped, so not alerting](images/monitoring-services.png)

## The Services tab

- **Filters** with counts: **Running**, **Stopped**, **Failed**, **Enabled**, **Disabled**, **All**.
- **Show all units (advanced, N hidden)** — by default DROOL hides the noise (one-shot units, units that
  are meant to come and go); tick it to see everything.
- Next to the filters: where the list comes from (*Polled over SSH* or *Reported by the Shepherd*)
  and how old it is.
- **The table:** **Service**, **State** (*Running*, *Stopped*, *Failed* or *Unknown*, with systemd's
  own words such as `active/running`, and a line saying whether it's a problem and why),
  **Enabled** (the boot setting, e.g. `enabled` or `static`), **Since**, **Restarts** (automatic
  restarts since it was last started by hand), **Expected**.
- **On each row:** **Customize health check** (per-service settings), **History** (every recorded state
  change), and **Actions…**: start, stop, restart, enable or disable it ([Managing Services](Managing-Services.md#from-a-hosts-services-tab)).

## Expected state

Each service has an expected state. Without one, DROOL decides by itself: a **failed** service is a
problem, and **stopped** is fine (most services stop normally). When that isn't right for a service,
set what you expect:

| Expected | Stopped | Running | Failed |
|---|---|---|---|
| **Automatic** (nothing set) | fine | fine | a problem |
| **Running** | a problem (*Not running, though expected to run*) | fine | a problem |
| **Stopped** | fine (*Not alerting: expected to be stopped*) | a problem (*Running, though expected to be stopped*) | fine |
| **Ignored** | shown, never a problem | shown, never a problem | shown, never a problem |

- Set it in the **Expected** column for this host, for all of a Pack's hosts on the Pack's page
  (**Expected services**), or for every host on **Monitoring → Settings** (**Expected services on
  every host**). The most specific setting wins: this host, then its Packs, then every host. The row
  says where its setting came from: *set on this host*, *from pack media-servers*, *set for every
  host*.
- **Running** and **Stopped** need **monitoring.manage** (administrators and operators). **Ignored**
  turns a service's checks off, so it needs **monitoring.settings** (administrators).
- DROOL keeps collecting the state of every service whatever you set: an ignored service still shows
  as stopped or failed, with its history.
- A problem becomes a health issue once it lasts: *failed* after 1 minute, *not running* or *running
  unexpectedly* after 2 minutes (recommended settings, changeable in **Customize health check**).
- Stopping or starting a service never changes its expected state by itself
  ([Managing Services](Managing-Services.md#the-expected-state-stays-as-it-is)).

## Acknowledge, snooze, or expect it

Three different answers to an alert about a service:

- **Acknowledge** — *I've seen it.* No more reminders; the alert stays open until the service is
  fine again ([Acknowledging an Alert](Acknowledging-an-Alert.md)).
- **Snooze** — *not now.* Put the host (or its Pack) in [maintenance](Maintenance-Mode.md) until a time
  you choose: no notifications meanwhile, health is still recorded, and what's still wrong when it
  ends is notified then.
- **Expected or Ignored** — *this is on purpose.* Set the expected state above. Then it isn't a problem
  at all, so nothing alerts, now or later.

The alert's own page offers all three under **Quieter, for a while or for good**.

## How quickly DROOL notices

- **Shepherd hosts:** services are sampled every 30 seconds, and changes reported within about a
  minute.
- **SSH hosts:** every 5 minutes. Where DROOL's account can't ask systemd itself (a host without D-Bus,
  such as DietPi), DROOL reads the services as root once an hour through the host's sudo, and the tab
  says so; routine polls never use sudo.

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
