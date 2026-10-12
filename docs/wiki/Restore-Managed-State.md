# Restore managed state

**Restore managed state** puts drifted hosts back the way their baselines say: it reinstalls or
removes packages, rewrites files, and starts, stops, enables or disables services. DROOL shows you
exactly what it will change **before** anything happens, then does it as an ordinary
[run](Understanding-Run-Results.md).

In the examples, Alex restores `web-01`: DROOL will create `/etc/motd` and remove `telnet`.

## Before you start

- You need **desired.restore** and **jobs.execute** (administrators and operators). Restoring files
  also needs **files.deploy**, and **files.elevate** for files written as root.
- The resource must have drifted, and DROOL must be able to check it now. **Conflicts** and
  **Unknown** resources can't be restored: resolve them first ([Conflicts](Conflicts.md),
  [Host Shows Unknown](Host-Shows-Unknown.md)).
- [Review the difference](Review-Difference.md) first, especially for files: restoring **overwrites**
  whatever is on the host now. If the host's version is the one you want to keep, use
  [Accept Current State](Accept-Current-State.md) instead.
- If the host is in [maintenance](Maintenance-Mode.md), someone may be working on it: DROOL warns you.

## Steps

You can restore one resource, one host, one baseline, or everything:

| To restore | Click |
|---|---|
| One resource | On the host's Desired State page, open the resource → **Restore managed state** |
| Everything on one host | On the host's Desired State page → **Restore managed state…** |
| Everything one baseline manages | On the baseline's page → **Restore managed state…** |
| Every drifted host | **Desired State → Overview → Restore managed state…** |

1. Click one of the above. DROOL checks the hosts again (*Checking the hosts and planning the
   correction…*) and shows the plan.
2. Read the plan:
   - **Corrections**: every host and resource, what it is **Now** and what it **Will be**.
   - **Steps**: the run's steps, in this order: *packages are installed first, then files, then
     services; removals come last.*
   - **Rollout**: for several hosts, one canary host first, then batches of five, pausing after two
     failures. **Change rollout** to adjust it ([Canary Hosts](Canary-Hosts.md)).
   - What **can't** be restored right now, and why.
3. If a prohibited package can only be removed together with packages that depend on it, decide
   whether to tick **Also remove packages that depend on the prohibited ones**. Unticked (the
   default), *such a removal fails safely on that host and nothing is removed there*.
4. Click **Restore 2 resources** (the button counts them).

![Restore managed state on web-01: 2 corrections on 1 host as one ordinary job; /etc/motd from missing to the managed content, owner root:root, mode 0644; package telnet from installed (0.17+2.5-3ubuntu4.2) to absent; the steps Deploy /etc/motd and remove telnet; Cancel and Restore 2 resources](images/restore-dialog.png)

## What happens

- DROOL creates **one ordinary run** for all the corrections. It's listed under **Runs**, with live
  output, and can be cancelled like any run. Each host runs only its own corrections.
- Every safeguard of the underlying operation applies: files are deployed with a backup of the
  previous version, [validation](Validation-Before-Deployment.md) and rollback; a package removal that
  would remove other packages stops unless you allowed it; a Shepherd's
  [capability preset](Shepherd-Capability-Presets.md) still limits what it may do.
- Afterwards DROOL checks the hosts again and shows **Restored** only when it **sees** the result.
  Otherwise the resource shows **Restore failed** (or *Restore not verified*), with the reason.
- If nothing differs any more when you confirm, no run is created.

## Hosts DROOL leaves out

Some hosts aren't changed even if they drifted, and the plan says why:

- a host whose Shepherd belongs to another (restored) controller and hasn't been reconciled yet
  ([Host Reconciliation](Host-Reconciliation.md));
- a quarantined Shepherd;
- a file that is newer on the host than in DROOL's records;
- a managed file whose path is a symbolic link on the host (DROOL never follows or replaces one).

## Limits

- One run makes at most 50 different changes. Larger restores are split by host; for a single host
  that needs more, restore it in parts.
- Files larger than 64 MiB can't be backed up first, so they can't be rolled back if something goes
  wrong.

## Related

[Drift](Drift.md) · [Automatic Restore](Automatic-Restore.md) · [Understanding Run Results](Understanding-Run-Results.md) ·
[Restore Needs Attention](Restore-Needs-Attention.md)
