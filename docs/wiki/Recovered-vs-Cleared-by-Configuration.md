# Recovered vs cleared by configuration

An alert can close in three ways, and DROOL never mixes them up. Only one of them means the machine
got better.

| How it closed | What happened | The alert shows | The message says |
|---|---|---|---|
| **Recovered** | Monitoring saw the condition go away: the service runs again, the disk has space again | **Resolved** | **RECOVERED: media-01** |
| **Cleared by configuration** | Someone changed a health check so the condition no longer counts: the check was **turned off**, or its **threshold changed** | **Cleared: check turned off (not a recovery)** or **Cleared: threshold changed (not a recovery)** | **CLEARED BY CONFIGURATION: gpu-01**, and *This is not a recovery* |
| **Closed, no longer reported** | The thing the alert was about disappeared: a disk was removed, a filesystem unmounted, a service uninstalled | **Closed: no longer reported (not a recovery)** | **CLOSED, NO LONGER REPORTED: nas-01**, and *This is not a recovery: the host no longer reports it* |

## Why it matters

Say `gpu-01`'s CPU has been at 99% for an hour, and Alex decides that's normal for an AI workstation
and turns off **High CPU utilization** for that host ([Health Overrides](Health-Overrides.md)). The
alert closes, but the CPU is still at 99%: nothing *recovered*. If DROOL said "RECOVERED", anyone
reading the message would believe the problem went away by itself. So the closing message says what
really happened:

```text
CLEARED BY CONFIGURATION: gpu-01
Cleared by configuration: CPU at 95% or more for 5 minutes
Why: High CPU utilization was turned off (Custom for this host). This is a configuration change, not a recovery.
This is not a recovery: the check was turned off; the condition may still be present.
Was open for: 1h 2m
Open in DROOL: https://drool.example.net:8080/alerts/alr_01m4rv0b49ss7cbajev2vdh3fj
```

(In the examples, `gpu-01` was in maintenance at the time, so this message was held back: the
alert's timeline says *Closing notification suppressed by maintenance (Replacing the GPU fan)*.)

The same goes for a threshold: if Alex raises the space warning for `/srv/media` on `nas-01` above
its current 96%, the alert closes as *Cleared: threshold changed*, and the message says *This is not a
recovery: the rule changed, not the machine.*

## Where you see it

- On **Alerts → Resolved / history**, in the **State** column.

  ![Alerts → Resolved / history: two jellyfin.service failed alerts on media-01 (Resolved, one acknowledged by sam), "jellyfin.service is not installed" (Resolved), and gpu-01's CPU alert "Cleared: check turned off (not a recovery)", which was suppressed by maintenance](images/alerts-history.png)

- On the alert's page: the badge, the **Details** card (*Cleared: check turned off* with the time, and
  **Why**), and the timeline (*Cleared by configuration — High CPU utilization was turned off (Custom
  for this host)…*, then *Alert closed after 1h 2m (not a recovery)*).

  ![gpu-01's alert "CPU at 95% or more for 5 minutes": Warning, Cleared: check turned off (not a recovery), suppressed by maintenance; the timeline from "Condition first observed" to "Closing notification suppressed by maintenance", and the Details card with the current value 99% and Why](images/alert-cleared.png)

- In webhooks, as `"event": "cleared"` with a `resolution` of `disabled`, `reconfigured` or `gone`
  ([Webhook Reference](Webhook-Reference.md#the-message-format)).

The closing message goes to the same destinations a recovery would have gone to (the ones that got
the alert, if their route sends recoveries), unless the host is in maintenance.

## Good to know

- Changes to health checks apply immediately. A problem that **still** qualifies under the new
  setting stays the same alert; it isn't closed and reopened.
- A **stricter** setting never pretends a problem existed in the past: if the new rule needs a
  condition to last 5 minutes, those 5 minutes start when you save.
- Turning a check off can hide a real problem. Before a fleet-wide or Pack change, DROOL shows how
  many hosts and open problems it affects ([Health Overrides](Health-Overrides.md#change-a-setting)).

## Related

[Understanding Alerts](Understanding-Alerts.md) · [Health Overrides](Health-Overrides.md) ·
[Monitoring Settings](Monitoring-Settings.md)
