# Troubleshooting

Find what you're seeing, then follow the link. Each page explains what the message means in plain
words, the usual causes, and what to do, starting with the most likely.

## Installing and first login

| You see | Go to |
|---|---|
| No setup code in DROOL's startup message or log | [I don't see a setup code](Setup-Problems.md#i-dont-see-a-setup-code) |
| *invalid or expired setup code*, *No valid setup code*, *too many failed setup attempts* | [My setup code expired](Setup-Problems.md#my-setup-code-expired) |
| *setup is already complete*, or `/setup` shows **Sign in to DROOL** | [DROOL says setup is already complete](Setup-Problems.md#drool-says-setup-is-already-complete) |
| `<this-host's-address>`, or you don't know what to open | [I don't know what address to open](Setup-Problems.md#i-dont-know-what-address-to-open) |
| The browser can't reach DROOL | [The browser can't connect](Setup-Problems.md#the-browser-cant-connect) |
| *no configuration file provided: not found* | [docker compose can't find a compose file](Setup-Problems.md#docker-compose-says-it-cant-find-a-compose-file) |
| Other Docker or Podman messages while installing | [Setup Problems](Setup-Problems.md#other-docker-messages) |

## Hosts and connections

| You see | Go to |
|---|---|
| A host is **Unreachable**; *connection refused*, *timed out*, *no network route* | [Host Won't Connect](Host-Wont-Connect.md) |
| **Host key changed**; *host key for … changed: expected …, got …* | [SSH Host Key Changed](SSH-Host-Key-Changed.md) |
| **Access denied**; *authentication as "drooladmin" failed* | [Authentication Failed](Authentication-Failed.md) |
| *sudo requires a password*, *the SSH user may not use sudo*, *requiretty* | [Sudo Failed](Sudo-Failed.md) |
| **Needs host key** | [SSH Host Key Verification](SSH-Host-Key-Verification.md) |
| **Unsupported** | The host isn't Linux, or lacks systemd ([Status Meanings](Status-Meanings.md#host-statuses)) |

## Shepherds

| You see | Go to |
|---|---|
| A Shepherd is **offline** | [Shepherd Offline](Shepherd-Offline.md) |
| **Incompatible**, **Update required**, **Reduced capabilities**, **Identity conflict** | [Shepherd Incompatible](Shepherd-Incompatible.md) |
| *CapabilityError* in a run | [Shepherd Capability Presets](Shepherd-Capability-Presets.md) |

## Runs, packages and services

| You see | Go to |
|---|---|
| A host's result is **outcome unknown** or **interrupted** | [Run Outcome Unknown](Run-Outcome-Unknown.md) |
| *the package manager is locked by …* | [Package Manager Busy](Package-Manager-Busy.md) |
| *dpkg was interrupted during an earlier operation* | [dpkg Was Interrupted](dpkg-Was-Interrupted.md) |
| *… failed to start (state: failed)* | [Service Won't Start](Service-Wont-Start.md) |
| A run is **paused** | [Canary Hosts](Canary-Hosts.md#when-a-run-is-paused) · [Batches and Failure Thresholds](Batches-and-Failure-Thresholds.md) |
| Every other result word | [Failed, Interrupted and Unknown Outcomes](Failed-Interrupted-and-Unknown-Outcomes.md) |

## Firewall

| You see | Go to |
|---|---|
| A firewall apply was **rolled back** | [Firewall Apply Rolled Back](Firewall-Apply-Rolled-Back.md) |
| Something stopped working after an apply, or you're locked out | [Recovering From a Firewall Problem](Recovering-From-a-Firewall-Problem.md) |
| **refused** under *Firewall environment* | [Before Enabling Enforcement](Before-Enabling-Enforcement.md) |
| **drift** | [Firewall Drift](Firewall-Drift.md) |
| **blocked: management path** | [Management Path Protection](Management-Path-Protection.md) |

## Monitoring, alerts and Desired State

| You see | Go to |
|---|---|
| **Unknown** anywhere | [Host Shows Unknown](Host-Shows-Unknown.md) · [What "Unknown" Means](What-Unknown-Means.md) |
| Monitoring data is old; **No monitoring data**; **Unreachable / stale** | [Monitoring Is Stale](Monitoring-Is-Stale.md) |
| **Delivery failing**, **Delivery retrying**, *may have arrived twice* | [Notification Failed](Notification-Failed.md) |
| **Restore failed** (Desired State), *Automatic restore paused — needs attention* | [Restore Needs Attention](Restore-Needs-Attention.md#desired-state) |

## DROOL itself

| You see | Go to |
|---|---|
| After restoring a backup: hosts **Ahead of DROOL**, **Blocked for safety**, … | [Restore Needs Attention](Restore-Needs-Attention.md) |
| DROOL won't start: missing or wrong master key, an interrupted restore | [Restore Needs Attention](Restore-Needs-Attention.md#drool-wont-start) |
| The browser warns about DROOL's certificate | [First Login](First-Login.md) · [Configuration](Configuration.md#your-own-tls-certificate) |
| Lost your authenticator app | [Two-Factor Authentication](Two-Factor-Authentication.md#if-you-lose-your-authenticator) |
| A **security issue** in the header | [Secure Your Installation](Secure-Your-Installation.md) |
| The audit history failed verification | [Audit Log](Audit-Log.md#if-verification-fails) |

## Still stuck?

- The run's output, the host's **Test connection**, and the [audit log](Audit-Log.md) usually say what
  happened.
- DROOL's own log: `sudo journalctl -u drool -n 50` and the files in `/var/lib/drool/logs/` (package or
  archive), `docker compose logs drool` in `~/drool` (Docker), or `journalctl --user -u drool -n 50`
  (Podman).
