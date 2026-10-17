# Acknowledging an alert

**Acknowledging** an alert tells everyone else "someone is on it". It's how a team avoids two people
fixing the same disk, and how you stop reminders while you work.

Acknowledging does **not** close the alert and doesn't change anything on the host. The alert stays
open until monitoring sees the condition go away ([Understanding Alerts](Understanding-Alerts.md#nobody-can-close-an-alert)).

Acknowledging is one of three answers. **Actions…** on an alert's row on **Alerts** opens the alert
in a panel beside the list, with all three; the alert's page has them too (**Acknowledge** and
**Quieter, for a while or for good**):

| You mean | Do |
|---|---|
| *I've seen it, I'm on it* | **Acknowledge…** (this page): no more reminders |
| *Not now* (planned work, a reboot) | **Snooze…**: [maintenance](Maintenance-Mode.md) for the alert's host, until a time you choose. No notifications meanwhile; what's still wrong when it ends is notified then |
| *This is on purpose* | **Ignore…**: change the setting behind it, for that host only. For a service: ignore it on the host, or expect the state it's in ([Service Monitoring](Service-Monitoring.md#customize-monitoring-the-expected-state)); for anything else: turn that check off for that disk, filesystem or sensor, or for the host ([Health Overrides](Health-Overrides.md)). DROOL says exactly what changes and where it's undone. The alert closes as *cleared by configuration*, not as a recovery, and nothing alerts later |

## Before you start

- You need **alerts.acknowledge** (administrators and operators).
- Only an **Active** alert can be acknowledged. A resolved alert has nothing left to acknowledge.

## Steps

From the list:

1. Open **Alerts**. On the alert's row, click **Actions…** (or the alert's title). The alert opens in
   a panel beside the list: what's wrong, where, since when, and who was told.
2. Click **Acknowledge…**. The form opens in the same panel (**Back** returns to the alert).
3. Optionally write a **Note** for the others (up to 500 characters), for example *Replacing the failed
   disk this evening*, and click **Acknowledge**.

![The panel beside the Alerts list for "jellyfin.service failed" on media-01: critical, active, since when, notification partly delivered, and what failed (the unit, expected to run, failed); Deal with it: Acknowledge…, Snooze… and Ignore…, each with what it does; Look further: View host media-01, View jellyfin.service and Open the alert](images/alert-quick-actions.png)

Or from the alert's page:

1. Open **Alerts**, click the problem, for example *sdb: kernel reports IO errors* on `nas-01`, and
   in the panel click **Open the alert**.
2. In the **Acknowledge** box on the right, optionally write a **Note** for the others (up to 500
   characters), for example *Replacing the failed disk this evening*.
3. Click **Acknowledge**.

The alert moves to the **Acknowledged** tab, with your name. The note and your name appear on the
alert's page and in its timeline, for example *Acknowledged by sam: Restarting it after the config fix*.

![The alert "jellyfin.service failed" on media-01, acknowledged by sam with the note "Restarting it after the config fix": the timeline, the notifications (delivered to Home Assistant, retrying to Ops mailbox) and the Acknowledgement card](images/alert-acknowledged.png)

## What changes, and what doesn't

| | After acknowledging |
|---|---|
| The alert | Stays open, on the **Acknowledged** tab |
| Reminders | Stop (any reminder not sent yet is cancelled) |
| If it gets worse (Warning → Critical) | You're still told: an *escalated* message is sent as usual |
| When it's fixed | The recovery message is sent as usual, and says who acknowledged it and their note |
| If maintenance ends while it's still open | No new message is sent (it has an owner) |
| The host, the health issue, monitoring | Nothing changes |

An acknowledgement can't be taken back or edited. If you acknowledged the wrong alert, tell your team;
the alert still closes on its own when the condition recovers.

Every acknowledgement is recorded in the [audit log](Audit-Log.md).

## Ignore… in more detail

**Ignore…** lists the settings that would make this condition stop being a problem, for this host only,
and who may apply each:

- **A service:** *Ignore backup.service on nas-01* (it stays listed, but never alerts there, even where
  a Pack or every host monitors it; administrators), *Stop monitoring backup.service on nas-01* (when
  only this host's own setting monitors it), or the state it's in on purpose: *Expect backup.service to
  be stopped on nas-01* (or *to run*). DROOL never guesses that last one for you.
- **Anything else:** *Turn off “Disk health (SMART / NVMe)” for sdb on nas-01*: that check, for that
  disk (by its identity), on that host. Other disks, other hosts and other checks keep working. Where
  the subject has no identity of its own, the check is turned off for the host, and DROOL says so.

Turning a check off or ignoring a service needs **monitoring.settings** (administrators): for others,
the option is shown greyed out, with what it needs. Each one says where it's undone (the host's
Services tab, **⋯ → Customize monitoring… → Reset to inherited**, or its health settings, **Reset all
to inherited**).

![Ignore, a step in the alert's panel for "jellyfin.service failed" on media-01: Ignore jellyfin.service on media-01, Stop monitoring jellyfin.service on media-01, or Expect jellyfin.service to be stopped on media-01, what each changes, that the alert then closes as cleared by configuration, not as a recovery, and where it's undone; Back and Change the setting](images/alert-ignore.png)

## Related

[Understanding Alerts](Understanding-Alerts.md) · [Maintenance Mode](Maintenance-Mode.md) ·
[Notification Routing](Notification-Routing.md#reminders)
