# Restore needs attention

This page covers three kinds of "restore": DROOL won't start after restoring a backup, hosts that wait
after a controller restore, and Desired State restores that failed.

## DROOL won't start

DROOL refuses to start rather than run in a state that could lose data. The message says which case:

| Message | What to do |
|---|---|
| *the master key file … is missing, but the database holds N value(s) encrypted with it (credentials, CA keys, secrets)* | Put the right `master.key` back (from your copy), or restore your latest backup without credentials ([Recovering Without Credentials](Recovering-Without-Credentials.md)) |
| *master key … (key id …) does not match the database* | A different key file is in place, for example after a reinstall. Put the key that belongs to this database back |
| *… DROOL doesn't start on a half-restored data directory. Run drool restore --recover to put the previous installation back …* | A restore was interrupted. Run `drool restore --recover` (native: `sudo -u drool drool restore --recover`), then restore again if you still want to |
| *… belongs to the user drool; run DROOL's commands as that user …* | You ran a `drool` command as root on a native installation. Use `sudo -u drool drool …` |

DROOL's log has the full message: `journalctl -u drool -n 50` (native), `docker compose logs drool`
(Docker).

## Hosts waiting after a controller restore

After [restoring a controller](Restoring-a-Controller.md), **Settings → System → Restore** lists hosts
that aren't reconciled yet: **Ahead of DROOL**, **Needs attention**, **Incompatible**, **Blocked for
safety**, or **Waiting**. Until a host is reconciled, DROOL doesn't change its firewall, and its Shepherd
refuses this controller's commands. What each state means and what to do:
[What Happens After Restore](What-Happens-After-Restore.md#what-waits-for-you).

## Desired State

### Restore failed

A Desired State resource shows **Restore failed** (or *Restore not verified*): a correction ran, but
DROOL didn't see the result it wanted afterwards.

1. Open the resource on the host's Desired State page: the note says why.
2. Open the correction's run (linked from the resource's history) and read the host's output.
3. Fix the cause (a service that won't start, a package that can't be removed without others, a full
   disk), then **Restore managed state** again. Or, if the host is right as it is,
   [accept its current state](Accept-Current-State.md).

### Automatic restore paused — needs attention

[Automatic restore](Automatic-Restore.md) pauses a baseline when an automatic run's rollout paused (a
canary failed, too many failures), or its owner lost the permissions. The baseline's page and the
Overview show the reason. Fix it, then an administrator clicks **Resume automatic restore (as me)**.

## Related

[Restoring a Controller](Restoring-a-Controller.md) · [What Happens After Restore](What-Happens-After-Restore.md) ·
[Restore Managed State](Restore-Managed-State.md) · [The Master Key](Master-Key.md)
