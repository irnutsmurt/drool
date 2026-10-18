# Run outcome unknown

A host's result on a run page says **outcome unknown** (*the connection to the host was lost while the
command was running; it may have finished, failed, or still be running*) or **interrupted** (*The
controller restarted while this run was in progress. Hosts that were in progress have an unknown
outcome*).

> **Unknown means DROOL doesn't know.** Not failed, not succeeded. The operation may have completed,
> partly happened, or not happened at all, and it may still be running on the host.

The full explanation, with every result word: [Failed, Interrupted and Unknown Outcomes](Failed-Interrupted-and-Unknown-Outcomes.md).

## What to do

1. **Don't just run it again.** Many operations aren't safe to repeat: appending to a file twice,
   moving files that are already moved, a migration that runs halfway twice.
2. **Look at the host.** Use a read-only command to check what happened, for example:
   - did the script's work happen? (`ls -l /srv/backups/` for a backup script);
   - is it still running? (`pgrep -af backup.sh`);
   - for a file deployment: open the file in the [file manager](Browsing-Files.md).
3. **Then decide:** run it again (for example with **Re-run unsuccessful hosts…**), finish it by hand, or
   leave it.

DROOL retries by itself only operations that are naturally safe to repeat (installing or removing a
package, a system update, putting a service in a state), and only when the connection dropped.

## Why it happened

- **The network dropped** between the controller and the host in the middle of the command.
- **The controller restarted** (an update, a crash, a restore) while the run was going. Scheduled runs
  that were interrupted aren't re-run automatically.
- **A Shepherd disappeared** mid-step (the host rebooted, or the Shepherd restarted).

If it happens often for one host, look at its network connection, or consider a
[Shepherd](SSH-vs-Shepherds.md), which survives short network drops better than an SSH session.

## Related

[Failed, Interrupted and Unknown Outcomes](Failed-Interrupted-and-Unknown-Outcomes.md) · [Understanding Run Results](Understanding-Run-Results.md) ·
[Missed Runs](Missed-Runs.md)
