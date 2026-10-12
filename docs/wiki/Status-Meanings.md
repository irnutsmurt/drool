# Status meanings

Every status word DROOL shows, in one place. Each links to the page that explains it in full.

## Host statuses

| Status | Meaning | What to do |
|---|---|---|
| **Ready** | DROOL can reach and manage the host | Nothing |
| **Untested** | Not tested yet, or its sign-in changed | **Test connection** ([Testing a Connection](Testing-a-Connection.md)) |
| **Needs host key** | DROOL won't connect until you approve the host's SSH key | [SSH Host Key Verification](SSH-Host-Key-Verification.md) |
| **Host key changed** | The host presented a different key than the one approved | [SSH Host Key Changed](SSH-Host-Key-Changed.md) |
| **Access denied** | The host refused DROOL's login | [Authentication Failed](Authentication-Failed.md) |
| **Unreachable** | DROOL can't connect to the host's SSH port | [Host Won't Connect](Host-Wont-Connect.md) |
| **Unsupported** | Not Linux, or no systemd (service management and reboots don't work; commands, scripts and packages do) | [Glossary: supported distributions](Glossary.md#supported-distributions) |

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
| **compatible**, **update available**, **reduced capabilities**, **update required**, **incompatible** | [Shepherd Incompatible](Shepherd-Incompatible.md) |

## Monitoring and alerts

| Status | Page |
|---|---|
| Health **OK**, **Info**, **Warning**, **Critical**, **No data yet** | [Fleet Monitoring](Fleet-Monitoring.md) · [Host Monitoring](Host-Monitoring.md) |
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
| Connections **Covered by policy**, **DROOL management**, **Unclassified**, **Denied**, **would be denied**, **not applied yet** | [Policy Classification](Policy-Classification.md) |

## After a restore

**Reconciled**, **Recovered**, **Waiting**, **Ahead of DROOL**, **Needs attention**, **Incompatible**,
**Blocked for safety**: [What Happens After Restore](What-Happens-After-Restore.md#what-waits-for-you).
