# Monitoring is stale

A host's monitoring data is out of date: the fleet page's **Updated** column shows an old time, the
**Unreachable / stale** filter lists the host, or the host has the health issue **No monitoring data**
(*stale*). DROOL treats old data as **unknown**, never as healthy.

## How DROOL decides it's stale

Each host is expected to report on a schedule: a Shepherd every few seconds, an SSH host every poll
(every 5 minutes by default). When **3 reports in a row** are missing, DROOL raises **No monitoring
data** for the host, which becomes an [alert](Understanding-Alerts.md) like any other health issue
(category *Unreachable / stale*).

## Usual causes

| Cause | Check |
|---|---|
| **The host is down or unreachable** | Its status on the host page; [Host Won't Connect](Host-Wont-Connect.md) |
| **Its Shepherd is offline** | [Shepherd Offline](Shepherd-Offline.md) |
| **SSH polling fails** (a host-key change, a credential problem) | **Actions → Test connection** on the host's page |
| **Monitoring is off**, for the whole fleet | **Monitoring → Health checks and settings → Monitor hosts** |
| **The controller was down** | Data from that time is missing; Shepherds deliver what they kept in memory (about a day for a typical host) when it's back |
| **The host's clock is behind** (a Shepherd host) | A Shepherd's reports carry its host's time, so they arrive already too old to count. Check the clock on the host (`timedatectl`: *System clock synchronized: yes*) and set up time synchronization; boards without a battery-backed clock, like a Raspberry Pi, need it after every boot |

## What to do

1. Open the host's monitoring page: the header says how the host is monitored and when it last
   reported.
2. Fix the cause (table above).
3. Monitoring catches up by itself; **Collect now** on the host's monitoring page asks for fresh data
   right away. The **No monitoring data** issue clears when recent reports arrive again, and the alert
   resolves as recovered. A report dated further back than the limit doesn't clear it.

## Related

[Monitoring Through SSH vs Shepherd](Monitoring-Through-SSH-vs-Shepherd.md) · [What "Unknown" Means](What-Unknown-Means.md) ·
[Fleet Monitoring](Fleet-Monitoring.md)
