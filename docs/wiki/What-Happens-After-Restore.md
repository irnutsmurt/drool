# What happens after a restore

A backup is a picture of the past. While it sat on a shelf, the world moved on: firewalls were applied,
Shepherds were reconfigured, maybe a host was replaced. So a restored DROOL **doesn't assume nothing
happened**. It compares each host with the restored data before it changes anything there.

## The new controller epoch

The restored controller gets a new **controller epoch** (an identity for "this controller, since this
restore"). Shepherds and DROOL's firewall tool on each host still answer to the old one, and **refuse
the restored controller's commands and firewall changes** until DROOL has compared the host with the
restored data. A host with SSH as its fallback still runs ordinary runs over SSH meanwhile.

## What DROOL does by itself

- A host that is **unchanged** since the backup (or for which DROOL has newer configuration) is moved to
  the new epoch automatically, a few hosts at a time.
- **Nothing that was in progress when the backup was taken is repeated**: unfinished runs are marked
  *interrupted* with an unknown outcome, open firewall transactions are closed as unknown, and unsent
  notifications are dropped.
- **Automatic things are held until you resume them**: job schedules, automatic Desired State restore,
  and Shepherd update rollouts. *Schedules count from when they're enabled again; runs due while they
  were held are not made up.*
- **Shepherds enrolled after the backup** are refused, and listed on the Shepherds page: enroll them
  again.

## What waits for you

Open **Settings → System → Restore**.

![Settings → System → Restore, reconciling: restored from a backup made at 2:40:18 PM; 2 hosts blocked for safety, 1 ahead of DROOL, 1 reconciled, 2 recovered; gpu-01 and nas-01 are blocked because of their SSH host keys; web-01 is ahead of DROOL because its firewall changed after the backup (revision 3, DROOL 2), with Accept host's state… and Check again; and Finish reconciliation…](images/restore-reconciliation.png)

| State | Means | What to do |
|---|---|---|
| **Reconciled** | *Compared with the restored data and moved to this controller's epoch.* | Nothing |
| **Recovered** | *Observed again; nothing versioned to reconcile* (for example an SSH host without DROOL's firewall) | Nothing |
| **Waiting** | Not compared yet: its Shepherd hasn't connected, or it's next in line | Wait, or **Check again** |
| **Ahead of DROOL** | Changed **after** the backup (a firewall applied, a Shepherd's configuration changed). *Nothing is rolled back* | **Accept host's state…**: nothing on the host changes; DROOL moves it to the new epoch as it is, and its own (older) configuration then shows where it differs. Or re-enroll the Shepherd |
| **Needs attention** | Disagrees with the restored data in a way DROOL won't guess about | Look at the details; deal with the cause, then **Check again** |
| **Incompatible** | Its Shepherd is too old to move to the new epoch | [Update the Shepherd](Updating-Shepherds.md); it's compared again when it reconnects |
| **Blocked for safety** | Not reconciled until a cause is resolved, for example the host's SSH key differs from the one DROOL trusts | Check the host key ([SSH Host Key Changed](SSH-Host-Key-Changed.md)), accept the new key on the host page if it's right, then **Check again** |

In the examples, `web-01` is **Ahead of DROOL**: Alex applied a firewall change after the backup
(*firewall: host has newer (revision 3, DROOL 2)*). Accepting its state keeps the host's newer
firewall; DROOL's firewall page then shows that DROOL's policy differs from what the host has, so Alex
can decide whether to apply DROOL's version again.

`gpu-01` and `nas-01` show *Blocked for safety* with a host-key message, although their keys never
changed: their host keys were never approved in the first place. Approve them on their host pages
([SSH Host Key Verification](SSH-Host-Key-Verification.md)), then **Check again**.

## Finishing

The reconciliation **finishes by itself** once every host is reconciled. **Finish reconciliation…**
ends it earlier, for example when a host is gone for good. Then:

1. Enable the held schedules again (open each schedule, **Enable**).
2. Resume automatic Desired State restore, and Shepherd update rollouts, if you used them.
3. If you revoked any Shepherd **after** the backup was made, revoke it again (Shepherds from before
   this version don't report revocations they missed); the [remote audit copy](Remote-Audit-Copy.md)
   lists them.

## Related

[Host Reconciliation](Host-Reconciliation.md) · [Restoring a Controller](Restoring-a-Controller.md) ·
[Restore Needs Attention](Restore-Needs-Attention.md)
