# Schedules

A **schedule** runs something at the times you choose: a run's steps, a saved
[workflow](Workflows.md), or a [maintenance window](Maintenance-Windows.md). In the examples, Alex
schedules *Weekly media server update*: every Sunday at 03:00, the `media-servers` Pack runs the
*Update media servers* workflow.

Every scheduled run is an ordinary run: it appears under **Runs** with live output and per-host
results, and it's checked, logged and cancellable exactly like a run you start yourself. You never
write cron expressions.

## Before you start

- You need **schedules.manage** (administrators and operators), **and** the permissions the steps
  themselves need (for example **jobs.execute** for commands). Everyone can see schedules and their
  history.
- Try what you want to schedule once by hand first ([Running a Command](Running-a-Command.md),
  [Running a Script](Running-a-Script.md), [System Updates](System-Updates.md)). A schedule repeats
  mistakes as reliably as it repeats successes.

## The quickest way: schedule a run you've set up

1. Open **Runs → New run** and set it up as usual: what to do, the targets, the options.
2. Instead of running it, click **Schedule this run**. The schedule form opens with everything filled
   in.
3. Choose when (below), check the **Preview**, and click **Create schedule**.

## Create a schedule from scratch

Open **Schedules → New schedule**.

1. **Name**, for example *Weekly media server update*. **Enabled** is ticked; untick it to save the
   schedule without it running yet.
2. **When**: choose how it **Repeats**:

   | Repeat | You choose |
   |---|---|
   | **Once** | A date and time |
   | **Every few hours** | **Every (hours)** (1–168) and the **First run** |
   | **Every day** | A **Time** |
   | **Selected weekdays** | The days (for example Monday to Friday) and a **Time** |
   | **Every week** | The **Day** and a **Time** |
   | **Every month** | The **Day of the month** (1–31) and a **Time**. In shorter months, a day they don't have means the last day |

   and the **Time zone** (your browser's is suggested). Times are in that zone, even when DROOL's
   own server is somewhere else, so moving the server never moves your schedules. The next run times
   appear below as you edit.
3. **Targets**: tick hosts and Packs, or type tags. **Packs and tags are looked up again at every
   run**: a host you add to `media-servers` next month takes part from then on.
4. **What to run**: **Steps** (add them as in a run: commands, scripts, packages, system updates,
   services, reboots, file deployments, and checks), or **A saved workflow**.
5. For a change to several hosts, DROOL suggests a careful rollout: **Use recommended rollout** (one
   host first, then five at a time, pausing if two fail). See [Canary Hosts](Canary-Hosts.md) and
   [Batches and Failure Thresholds](Batches-and-Failure-Thresholds.md).
6. Check the **Preview** on the right, then click **Create schedule**.

![The schedule form for Weekly media server update: every week on Sunday at 03:00 in America/Los_Angeles with the next five runs, the media-servers Pack ticked, the saved workflow "Update media servers" (revision 1), the Advanced summary, and the Preview with the next runs, the canary and batch, the steps, reboots, verification, the maintenance window and the policies](images/schedule-editor.png)

### The preview

The preview shows what would happen **if the schedule ran now**: **When** (and the next runs),
**Targets now** with the order and waves (*Canary: media-01*, *Batch 1: media-02, nas-01*) and any host
that would be skipped because it's in maintenance, the **Steps**, whether hosts may **reboot**, what
**Verification** runs afterwards, the **Rollout**, and the **Policies** for missed and overlapping
runs.

For package and system updates, **Package changes → Check package changes now** does a dry run on
each host: *This shows what would change today. Repositories change, so a later run may install
more (or different) updates.*

## Advanced options

Click **Show** on the **Advanced** card. Without opening it, the card sums up the current choices.

- **Rollout**: canary, batch size, hosts at a time, pause after failures.
- **If DROOL isn't running at the scheduled time**: see [Missed Runs](Missed-Runs.md).
- **If the previous run is still going**: **Skip the new run** (recommended), or **Queue one run to
  start when it finishes** (*at most one waits; further runs are skipped*).
- **Maintenance window → Run inside**: tie the schedule to a recurring maintenance window, so it runs
  only while that window is active. See [Maintenance Windows](Maintenance-Windows.md).

## The schedule's page

Open a schedule from **Schedules** to see:

- **Run now** (runs it immediately, as **you**, with your permissions; hosts in maintenance are *not*
  skipped, and the next scheduled run doesn't change), **Disable** / **Enable**, and **Edit**. Only
  the schedule's owner and administrators can edit it or enable it again.
- **Settings**: the next run, the targets (*resolved again at every run*), the rollout, what happens
  if DROOL was down or the previous run is still going, the maintenance window, the **Owner** and the
  **Revision**.
- **History**: every time the schedule was due, when it started, what happened (*succeeded*, *failed
  on 2 hosts*, *skipped* and why, *missed*, *blocked*), and **View run**.

![The schedule Daily disk check: enabled, df -h / every day at 06:00; its settings (next run, targets, rollout, missed and overlapping runs, maintenance window, owner alex, revision 1) and its history with one run started by "Run now by alex", completed with failures: 2 succeeded, 1 failed](images/schedule-detail.png)

**Schedules** lists every schedule with its state, next run, when, targets, how its runs went, the
last result and the owner. **Show archived** lists archived ones.

## Who a scheduled run runs as

Every schedule has an **owner**: whoever created it, or whoever last changed it. Scheduled runs use
the owner's permissions, and DROOL checks them **before every run**. If the owner's account was
disabled or deleted, or no longer has the permissions the steps need, the run is **blocked**, the
schedule is **disabled** and marked **Needs attention** with the reason. An administrator can
**Change owner** (**schedules.admin**), then enable it again.

When an administrator changes someone else's schedule, the administrator becomes its owner (*Saving
makes you this schedule's owner: future runs use your permissions*), so a schedule never runs one
person's changes with someone else's rights.

## Changing, disabling and stopping

- **Changes apply to future runs only.** A run in progress, and earlier runs, keep exactly what they
  ran.
- **Disabling a schedule doesn't stop a run in progress**, and **cancelling a run doesn't disable its
  schedule**: its next run happens as planned. Both pages remind you.
- **Archive** stops the schedule for good and keeps its history and past runs.

## Daylight saving time

- If the time doesn't exist that day (clocks jump from 02:00 to 03:00 and you chose 02:30), that
  day's run happens at the first valid time after the jump (03:00).
- If the time happens twice (clocks go back), it runs once, the first time.
- **Every few hours** counts real hours, so its clock times shift by an hour across a change.

## Good to know

- Times have minute precision; a run starts within about 15 seconds of its time.
- Scheduled runs **skip hosts in maintenance** (*skipped: maintenance active*), unless the schedule
  runs inside that maintenance window. **Run now** and runs you start yourself aren't affected.
- Schedules never get around a host's limits: a host whose Shepherd preset or sudo setting doesn't
  allow a step fails or is skipped with the reason, as in any run.
- The results of scheduled runs aren't sent as notifications. Watch the schedule's **History** or the
  events on the **Overview**.
- Creating, changing, enabling, disabling, archiving, running now and changing owners are all
  recorded in the [audit log](Audit-Log.md).

## Related

[Workflows](Workflows.md) · [Missed Runs](Missed-Runs.md) · [Maintenance Windows](Maintenance-Windows.md) ·
[Canary Hosts](Canary-Hosts.md) · [System Updates](System-Updates.md)
