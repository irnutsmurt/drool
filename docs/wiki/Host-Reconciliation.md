# Host reconciliation

**Reconciliation** is how a [restored](Restoring-a-Controller.md) DROOL makes sure it never undoes a
change that happened after the backup. For each host, DROOL compares what the host has (its Shepherd's
configuration and its DROOL firewall, which carry version numbers) with what the restored database
says, and only then lets the restored controller manage it again.

## Why it exists

Imagine a backup from Sunday. On Monday you tighten `web-01`'s firewall. On Tuesday the controller's
disk dies, and you restore Sunday's backup. Without reconciliation, the restored DROOL would "fix"
`web-01` back to Sunday's firewall, silently undoing Monday's change. With reconciliation, DROOL sees
that `web-01` is **ahead** and asks you first.

## The rule

- A host that has nothing newer than the backup is moved over by itself.
- A host that has something newer is **never rolled back**. You decide: accept the host's state (it
  keeps what it has; DROOL's own configuration then shows the difference), or deal with it another way.
- Until a host is reconciled, its Shepherd refuses the restored controller's commands, and DROOL
  doesn't change its firewall. SSH still works for ordinary runs on hosts with SSH as their fallback.

## Where you see it

**Settings → System → Restore**, with every host's state and the details; **Show all 6 hosts** lists
the reconciled ones too. Each host's page shows its own reconciliation state while it isn't finished.

What each state means, and what to do: [What Happens After Restore](What-Happens-After-Restore.md#what-waits-for-you).

## Accept a host's state

1. Find the host under **Hosts not reconciled yet**, for example `web-01` (*Ahead of DROOL*).
2. Read the details: *firewall: host has newer (revision 3, DROOL 2)*.
3. Click **Accept host's state…** and confirm. *Nothing on the host changes; it is moved to this
   controller's epoch with what it has, and DROOL's own configuration then shows where it differs.*
4. The host shows *Accepted by alex; comparing again*, then **Reconciled**.

Accepting asks you to [confirm it's you](Re-authentication.md) and is recorded in the
[audit log](Audit-Log.md).

## Related

[What Happens After Restore](What-Happens-After-Restore.md) · [Restoring a Controller](Restoring-a-Controller.md) ·
[Restore Needs Attention](Restore-Needs-Attention.md)
