# Batches and failure thresholds

When a run reaches many hosts, three settings decide how fast it goes and when it stops to let you
look. They're in the **Rollout** card of **Runs → New run**, under **Advanced** on a
[schedule](Schedules.md#advanced-options), and in a [workflow's](Workflows.md) **Default rollout**.

| Setting | What it does | Default |
|---|---|---|
| **Batch size** | After the [canaries](Canary-Hosts.md), hosts run in **batches** (waves) of this many. A batch starts only when the previous one has **finished** | Empty: all hosts at once |
| **Hosts at a time** | How many hosts run in parallel **within** a batch (1–100) | 10 |
| **Pause after failures** | After this many hosts fail, DROOL starts no more hosts, lets the running ones finish, and **pauses** | Empty: never pause for failures |

DROOL's **recommended rollout** for changes to two or more hosts is one canary, then batches of 5, 5
at a time, pausing after 2 failures. **Use recommended rollout** sets it in one click.

## An example

Alex updates 13 hosts with: 1 canary, batch size 5, 5 at a time, pause after 2 failures.

1. The canary runs alone and succeeds.
2. Batch 1 (5 hosts) runs, all 5 at once. One fails.
3. Batch 2 starts after batch 1 has finished. A second host fails: that's 2 failures. DROOL starts no
   further hosts, lets the hosts already running finish, and pauses:

   > Paused after 2 failures (the failure threshold is 2). 4 hosts have not started; resume to
   > continue, or cancel.

4. Alex looks at the two failed hosts' output, sees a full disk on both, and clicks **Resume** for the
   rest. Failures are counted afresh after a resume: the run would pause again after two more.

## Resume or cancel

- **Resume** continues with the hosts that haven't started. **Hosts that failed stay failed**; a
  resume doesn't retry them.
- **Cancel** stops the run: hosts that haven't started never do, and commands still running are
  terminated (*Cancel this run? Running commands will be terminated.*).

To try the failed hosts again later, open the run and click **Re-run unsuccessful hosts**.

## What counts as a failure

A host counts as a failure when one of its steps **fails** or **times out**, including when its
**outcome is unknown** because the connection dropped mid-command: check such hosts before you resume
([Failed, Interrupted and Unknown Outcomes](Failed-Interrupted-and-Unknown-Outcomes.md)). Hosts that
are **skipped** (for example *Only run on: Debian / Ubuntu*, or in maintenance) or **cancelled** don't
count.

## A safety stop you don't have to set

Even without **Pause after failures**, DROOL pauses a run when **5 hosts in a row** fail for the same
reason that usually means a shared problem rather than five broken hosts: an **authentication** failure
(a wrong key or password), a **host key** problem, or a **sudo/authorization** failure. The pause
reason says so, for example *5 consecutive AuthenticationError failures; likely a shared credential or
configuration problem*. Fix the credential ([SSH Credentials](SSH-Credentials.md),
[Sudo Failed](Sudo-Failed.md)), then resume or cancel.

## Good to know

- The order of hosts and batches is fixed when the run starts and shown on the run's page under
  **Rollout progress**, with each wave's result.
- However many hosts a run allows at a time, the controller runs at most 20 hosts at once across all
  runs; the rest wait their turn.
- A host busy with another (non-read-only) run waits for it to finish before starting.

## Related

[Canary Hosts](Canary-Hosts.md) · [Schedules](Schedules.md) · [Workflows](Workflows.md) ·
[Understanding Run Results](Understanding-Run-Results.md)
