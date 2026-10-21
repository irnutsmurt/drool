# Status meanings

Every status word DROOL shows, in one place. Each links to the page that explains it in full.

## Host statuses

A host has three states, shown separately in its header: **Connection**, **Last check** and **Health**.
A host can be *Ready* and unhealthy at the same time: *Ready* is only about DROOL's access.

**Connection**: can DROOL sign in and manage the host? This is the **Connection** column of the Hosts
list.

| Status | Meaning | What to do |
|---|---|---|
| **Ready** | DROOL can sign in to the host and manage it. This says nothing about the host's health | Nothing |
| **Untested** | Not tested yet, or its sign-in changed | **Test connection** ([Testing a Connection](Testing-a-Connection.md)) |
| **Needs host key** | DROOL won't connect until you approve the host's SSH key | [SSH Host Key Verification](SSH-Host-Key-Verification.md) |
| **Host key changed** | The host presented a different key than the one approved | [SSH Host Key Changed](SSH-Host-Key-Changed.md) |
| **Access denied** | The host refused DROOL's login | [Authentication Failed](Authentication-Failed.md) |
| **Unreachable** | DROOL can't connect to the host's SSH port | [Host Won't Connect](Host-Wont-Connect.md) |
| **Unsupported** | Not Linux, or no systemd (service management and reboots don't work; commands, scripts and packages do) | [Glossary: supported distributions](Glossary.md#supported-distributions) |

**Last check**: did DROOL's latest status check reach the host? **online**, **offline**, **stale** (no
recent check: the values shown are older) or **not checked yet**.

**Health**: how monitoring sees the host, in the host's header and the **Health** column of the Hosts
list and the Monitoring page.

| Health | Meaning |
|---|---|
| **Healthy** | No issue in what monitoring can see; *Healthy · 1 notice* has only an informational one, such as reduced monitoring fidelity. Storage that can't report its own health, such as an SD card, shows **Limited** on [Storage Health](Storage-Health.md) and doesn't count as an issue |
| **Degraded · N issues** | The worst issue is a warning |
| **Unhealthy · N issues** | At least one issue is critical |
| **Stale** | No recent monitoring data: the values shown are older |
| **No data** | Monitoring hasn't reported anything for this host yet |

See [Fleet Monitoring](Fleet-Monitoring.md) and [Host Monitoring](Host-Monitoring.md).

## Run results

**succeeded**, **failed**, **timed out**, **interrupted**, **outcome unknown**, **skipped**, **cancelled**,
**unsupported**, **incompatible**, and while running **queued**, **waiting for host**, **connecting**,
**running**, **rebooting**, **waiting for host to return**, **verifying**: [Failed, Interrupted and Unknown Outcomes](Failed-Interrupted-and-Unknown-Outcomes.md) ·
[Understanding Run Results](Understanding-Run-Results.md). A whole run can also be **paused**
([Canary Hosts](Canary-Hosts.md#when-a-run-is-paused)).

## Shepherds

| Status | Page |
|---|---|
| **connected**, **offline**, **identity conflict** | [Shepherd Offline](Shepherd-Offline.md) · [Shepherd PKI](Shepherd-PKI.md) |
| **compatible**, **reduced capabilities**, **update required**, **incompatible** | [Shepherd Incompatible](Shepherd-Incompatible.md) |
| **Up to date**, **On DROOL's version**, **Update available: …**, **Updating to …**, **Update failed**, **Can't update**, **No update available**, **Not checked** (a Shepherd's update) | [Updating Shepherds](Updating-Shepherds.md) |

## Monitoring and alerts

| Status | Page |
|---|---|
| Host health **Healthy**, **Degraded**, **Unhealthy**, **Stale**, **No data** ([above](#host-statuses)); tiles and checks **OK**, **Info**, **Warning**, **Critical**, **No data yet** | [Fleet Monitoring](Fleet-Monitoring.md) · [Host Monitoring](Host-Monitoring.md) |
| **Unknown** | [What "Unknown" Means](What-Unknown-Means.md) |
| Alerts **Active**, **Acknowledged**, **Resolved**, **Cleared** (not a recovery), **Suppressed by maintenance** | [Understanding Alerts](Understanding-Alerts.md) · [Recovered vs Cleared by Configuration](Recovered-vs-Cleared-by-Configuration.md) |
| Notification **Notified**, **Sending**, **Delivery retrying**, **Partly delivered**, **Delivery failed**, **Suppressed**, **No destination** | [Understanding Alerts](Understanding-Alerts.md#the-alerts-page) |
| Destination **Delivering**, **Delivery failing**, **Not used yet**, **Disabled** | [Notification Failed](Notification-Failed.md) |

## Desired State

**Compliant**, **Drift detected**, **Conflict**, **Unknown**, **Restoring…**, **Restore failed**:
[What Desired State Is](What-Desired-State-Is.md#the-words-drool-uses).

## Firewall

| Status | Page |
|---|---|
| Modes **Off**, **Monitor**, **Guided**, **Enforce** | [Firewall Modes](Firewall-Modes.md) |
| Environment **supported**, **refused**, **not checked** | [Before Enabling Enforcement](Before-Enabling-Enforcement.md) |
| Hosts **not enforced**, **in sync**, **changes not applied**, **addresses propagating**, **transaction pending**, **drift**, **blocked: management path** | [Applying the Firewall Policy](Applying-the-Firewall-Policy.md#what-you-see-afterwards) |
| Transactions **preparing**, **pending**, **committed**, **rolled back**, **failed**, **unknown** | [Automatic Rollback Protection](Automatic-Rollback-Protection.md#transaction-states) |
| Connections **Covered by policy**, **Management path**, **Not in policy**, **Denied**, **Would be denied**, **not enforced**, **not applied yet** | [Policy Classification](Policy-Classification.md) |
| Connections **DROOL management**, **Expected**, **Classified**, **Known service**, **Unknown** | [Classifying Traffic](Classifying-Traffic.md) |

## After a restore

**Reconciled**, **Recovered**, **Waiting**, **Ahead of DROOL**, **Needs attention**, **Incompatible**,
**Blocked for safety**: [What Happens After Restore](What-Happens-After-Restore.md#what-waits-for-you).
