# Workflow revisions

Every time you save a change to a [workflow](Workflows.md), DROOL keeps it as a new **revision**
(revision 1, 2, 3…). Revisions never change. This is what makes scheduled work predictable: a schedule
runs exactly what you reviewed, even if someone edits the workflow later.

## How it works

- **Saving a change** (**Save as a new revision**) stores a new revision. If nothing changed, nothing
  is saved (*Nothing changed, so no new revision was saved.*).
- **A schedule runs one particular revision**: the one it was saved with. The schedule's form says
  *Runs revision 3 exactly as it is now. Later edits to the workflow don't change this schedule until
  you choose to use them.*
- **Past runs** always show exactly the steps they ran, and which revision (*workflow Update media
  servers, revision 3*).
- **Run now** on the workflow's page runs its latest revision.

## Move a schedule to the newer revision

When the workflow changes, the schedule's page tells you: *A newer workflow revision is available:
this schedule runs revision 3 of Update media servers; the latest is 4. It keeps running the revision
it has until you choose the new one.*

1. Look at what changed: open the workflow (the link in that notice); its **Revisions** card has
   **View** for each revision.
2. On the schedule's page, click **Review and update**.
3. On the form, click **Use revision 4**.
4. Check the **Preview**, and click **Save changes**.

Until you do, the schedule keeps running revision 3. Nobody's edit to a workflow reaches a schedule
without someone deciding it should.

## Archived workflows

**Archive** on a workflow retires it: schedules that use it **keep running their revision** (the
schedule's page says *Its workflow is archived. The schedule keeps running the revision it has.*), but
new schedules can't choose it. Past runs keep showing what they ran.

## Related

[Workflows](Workflows.md) · [Schedules](Schedules.md)
