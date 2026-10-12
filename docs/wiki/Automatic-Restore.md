# Automatic restore

Normally DROOL only **shows** drift, and a person decides to [restore](Restore-Managed-State.md) it.
With **automatic restore** turned on for a baseline, DROOL corrects drift from that baseline by itself,
using the same ordinary runs, carefully enough not to fight anyone.

It's **off by default**, for every baseline. Turn it on only for things that should never differ and
that nobody changes on purpose, such as a prohibited package or a security setting.

## Before you start

- You need **desired.auto** (administrators).
- **You become the baseline's owner.** Automatic runs use **your** permissions, and DROOL checks before
  every automatic run that you still exist, are enabled, and have the permissions.
- Make sure the baseline is right: try a restore by hand first.

## Turn it on

1. Open the baseline and click **Edit**.
2. Open **Advanced: automatic restore (off)** (click **Show**).
3. Tick **Automatically restore managed state**. *Off by default. When on, DROOL corrects drift from
   this baseline by itself — never during maintenance, with bounded retries, pausing when something
   keeps changing it back — as an ordinary job run with your permissions (you become its owner).*
4. Choose the **Rollout of automatic corrections** (for several hosts: one canary first, then batches,
   pausing after failures; or all hosts at once).
5. Save the baseline.

The baseline's page shows **Automatic restore** and its state; to switch it off, click **Turn off**.

## How it avoids fighting people

| Situation | Automatic restore |
|---|---|
| The host is in [maintenance](Maintenance-Mode.md) | Does nothing, and waits 5 minutes after maintenance ends |
| The host has its own accepted value, or a [conflict](Conflicts.md) | Never touches it |
| Removing a prohibited package would remove others too | Never does it |
| A correction failed | Tries again after 15 minutes, then after an hour, then **pauses** for that resource |
| Something keeps changing it back (3 times in a day) | **Pauses** for that resource |
| An automatic run's rollout paused (a canary failed, too many failures), or the owner lost the permissions | **Pauses for the whole baseline**: *Automatic restore paused — needs attention* |

At most one automatic run per baseline, and three in total, run at the same time.

## When it pauses

The baseline's page, and the Desired State Overview, say *Automatic restore paused — needs attention*,
with the reason. Look at the failed run or the resource, fix the cause, and then an administrator
clicks **Resume automatic restore (as me)**, becoming the owner.

## Good to know

- Automatic runs appear under **Runs** like any other run, and in the [audit log](Audit-Log.md) with
  the owner.
- **New run** warns when a run you're about to start contradicts desired state and automatic restore
  may undo it (*Stopping it will create desired-state drift*).
- If you need to change something a baseline with automatic restore manages, put the host in
  maintenance first, or change the desired state.

## Related

[Restore Managed State](Restore-Managed-State.md) · [Drift](Drift.md) · [Maintenance Mode](Maintenance-Mode.md) ·
[Restore Needs Attention](Restore-Needs-Attention.md)
