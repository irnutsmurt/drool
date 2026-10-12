# Missed runs

A schedule can only start runs while DROOL is running. If the controller was stopped, restarting, being
upgraded or restored at a scheduled time, that run is **missed**. DROOL never makes up a pile of missed
runs behind your back: you decide what happens, per schedule.

## Recurring schedules

Choose under **Advanced → If DROOL isn't running at the scheduled time**:

- **Skip it and continue with the next run** (recommended, the default). The schedule's **History**
  records what was missed in **one** entry, however many runs it was: *Missed 3 runs while DROOL was not
  running (until 2026-10-11 10:00 UTC)*.
- **Run one missed occurrence when DROOL returns**: *only the most recent one, never a backlog.* After a
  weekend outage, a daily update runs once, not three times in a row.

A recurring run that would start **more than 5 minutes** after its time counts as missed.

## One-time schedules

A **Once** schedule runs when DROOL returns, however late, and is marked *started late: DROOL was not
running at Sun 11 Oct 03:00 PDT*. If a late start would be a problem (you planned it for a quiet
night), set **Don't start if more than this many minutes late**: a later return records it as *Not started:
DROOL was not running at …, and the run is not started more than 30 minutes late* instead.

## Runs that were in progress

A run that was going when DROOL stopped is marked **interrupted** (*Interrupted: the controller restarted
during the run (not re-run automatically)*). Hosts that were in the middle of something have an
**unknown outcome**: check them before you run it again
([Failed, Interrupted and Unknown Outcomes](Failed-Interrupted-and-Unknown-Outcomes.md)).

## When the previous run is still going

That's not a missed run, but it's decided in the same place, **Advanced → If the previous run is still
going**:

- **Skip the new run** (recommended): the history says *Skipped: the previous run (job …) is still
  active*.
- **Queue one run to start when it finishes**: *at most one waits; further runs are skipped.* The
  history shows *Queued: waiting for the previous run to finish*.

## Maintenance windows

If DROOL wasn't running when a [recurring maintenance window](Maintenance-Windows.md) should have
started, it starts the window when it returns, **with the original end time**. A window that passed
entirely while DROOL was down is recorded as missed and never started late.

## Related

[Schedules](Schedules.md) · [Maintenance Windows](Maintenance-Windows.md) ·
[Run Outcome Unknown](Run-Outcome-Unknown.md)
