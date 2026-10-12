# Maintenance windows

A **recurring maintenance window** is [maintenance](Maintenance-Mode.md) that starts and ends by itself
on a timetable, for example *every Sunday from 02:30 to 04:30*. Scheduled work can be tied to it, so
updates and reboots happen inside the window while nobody is messaged about the alerts they cause.

In the examples, the *Sunday patch window* covers the `media-servers` Pack every Sunday at 02:30 for 2
hours, and the *Weekly media server update* schedule runs inside it at 03:00.

## Before you start

You need both **schedules.manage** and **maintenance.manage** (administrators and operators).

## Create a recurring window

1. Open **Alerts → Maintenance** and, under **Recurring windows**, click **New recurring window**.
2. **Name**: for example *Sunday patch window*.
3. **When**: how it repeats (*Every week*, Sunday, 02:30) and the time zone, as for any
   [schedule](Schedules.md#create-a-schedule-from-scratch).
4. **Targets**: hosts and Packs. A Pack's membership is checked **live** while the window lasts.
5. **Window → How long**: *1 hour*, *2 hours*, *4 hours*, *8 hours* or *1 day*, or any number of
   **Minutes** (at most 7 days).
6. **Reason** (recommended): shown on the alerts it suppresses, for example *Weekly patching*.
7. Check the **Preview** and click **Create schedule**.

Each time, the window starts ordinary maintenance for its hosts: notifications are paused, and
monitoring, alerts and every safety mechanism carry on. The window's page has **Start now** to start
an occurrence early.

## Run a schedule inside a window

Scheduled runs **skip hosts that are in maintenance** (*skipped: maintenance active*), so a nightly run
doesn't touch a machine someone is working on. To make a schedule do its work *during* a window
instead:

1. Open the schedule and click **Edit** (or create a new one).
2. Under **Advanced → Maintenance window → Run inside**, choose the window, for example *Sunday patch
   window (Every Sunday at 02:30)*.
3. Make sure the schedule's time falls inside the window (03:00 is inside 02:30–04:30). The
   **Preview** warns when the next run is outside the window's next occurrence: *a run when the window
   isn't active is skipped*.
4. Save.

The schedule now runs **only while that window is active**, and may work on the hosts the window put
in maintenance.

**Other maintenance still applies.** If Sam has put `media-02` in maintenance by hand (*Replacing the
power supply*), the weekly update skips `media-02` even though it runs inside the Sunday window: a
window never lets a run past someone else's maintenance.

## Good to know

- Runs you start yourself, and **Run now** on a schedule, aren't affected by maintenance.
- If DROOL was down when a window should have started, it starts the window when it returns, with the
  original end time, or records it as missed if the window has already ended. It never sends
  notifications late.
- Disabling a window stops future occurrences; an occurrence already in progress continues until its
  end time (end it on **Alerts → Maintenance** with **End now**).

## Related

[Maintenance Mode](Maintenance-Mode.md) · [Schedules](Schedules.md) · [Missed Runs](Missed-Runs.md) ·
[System Updates](System-Updates.md)
