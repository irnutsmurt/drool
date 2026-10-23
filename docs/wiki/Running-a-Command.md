# Running a command

A **run** is anything DROOL does on hosts. The simplest is a command: type it once, pick the hosts,
and DROOL runs it on each of them and shows every host's output. In the examples, Alex checks the
free disk space on all media servers with `df -h /`.

## Before you start

- You need **jobs.execute** (administrators and operators).
- The hosts must be in DROOL and reachable ([Your First Host](Your-First-Host.md)). Hosts that aren't
  ready (for example *Needs host key*) are listed with a warning and will fail.
- On a host managed through a **Shepherd**, commands need a Shepherd whose ceiling allows the shell:
  the **Full** preset. With *Managed* or *Monitor*, the host refuses with a *CapabilityError*
  ([Shepherd Capability Presets](Shepherd-Capability-Presets.md)).

## Steps

1. Open **Runs → New run** (or **Run command** on a host's page, or **Run on this Pack** on the Packs
   page: the target is then already selected).
2. Under **What to do**, keep **Command** selected and type the command, for example `df -h /`.
3. Choose the **Targets** on the right: tick hosts, Packs (`media-servers`), or type tags. The box
   **Will run on 3 hosts** lists exactly which hosts that means, with a warning next to any host that
   will fail or be skipped.
4. Optionally:
   - **Run as root (sudo)** — run the command through the host's sudo setting. Without it, the
     command runs as the SSH user (`drooladmin`).
   - **Name** — shown in the Runs list (for example *Check disk space*); otherwise the command is.
   - Under **Advanced options** (closed, it still lists what's set there):
     - **Timeout (seconds)** — default 300. A command still running after that is stopped and the
       host reported as *timed out*.
     - **Only run on:** *Debian / Ubuntu* or *RHEL family / Fedora* — the command runs only on hosts
       of that family; the others are skipped. Leave both unticked for all hosts.
     - **Read-only** — tell DROOL the command doesn't change anything. Read-only runs may run on a
       host at the same time as other runs; other runs wait for each other.
5. Check the **Rollout** card ([Canary Hosts](Canary-Hosts.md),
   [Batches and Failure Thresholds](Batches-and-Failure-Thresholds.md)). For a change to two or more
   hosts, DROOL starts with its recommended rollout (*Recommended for 3 hosts: 1 canary host first,
   then batches of 5, up to 5 at a time, pause after 2 failures*): it stops a bad command early
   instead of on every host. **Change rollout…** sets another; a **Read-only** command needs none.
6. Click **Review: run on 3 hosts…** (the button counts the hosts). DROOL's review, *Start this run
   on 3 hosts?*, shows **What** (the command, and whether it runs as root), **On** (the hosts by
   name), the **Rollout**, and any warnings. Click **Start run on 3 hosts**: DROOL opens the run.

![New run with the command df -h /, the name "Check disk space" and Advanced options folded; the media-servers Pack ticked; the Rollout card with the recommended rollout for 3 hosts (media-01 first as the canary, then media-02 and nas-01) and Change rollout…; "Will run on 3 hosts" with media-02 (last connection test could not reach the host) and nas-01 (host key not approved yet; this host will fail); and Review: run on 3 hosts…, Schedule this run and Save as workflow](images/new-run-command.png)

![DROOL's review, Start this run on 3 hosts?: What, the command df -h /; On, media-01, media-02 and nas-01; Rollout, 1 canary host first, then batches of 5, up to 5 at a time, pause after 2 failures; that it runs as the SSH user; the warnings for media-02 and nas-01; Cancel and Start run on 3 hosts](images/new-run-review.png)

Instead of running it now you can **Schedule this run** ([Schedules](Schedules.md)) or **Save as
workflow** to reuse it ([Workflows](Workflows.md)).

## What DROOL does

- It connects to each host (over SSH, or through its Shepherd) and runs the command in a shell on
  the host, as the SSH user or, with **Run as root**, through sudo.
- Up to 10 hosts run at the same time by default (**Hosts at a time**; 5 with the recommended
  rollout), 20 across all runs on the controller.
- A host that's busy with another (non-read-only) run waits for it first (*waiting for host*).
- It streams the output live and keeps it with the run (up to 10 MB per host and step).
- If a sudo password is involved, it's sent on standard input and removed from the output.

## The result

![The result of "Check disk space": completed with failures, media-01 succeeded via agent, media-02 via SSH, nas-01 failed because its host key isn't approved; media-01's df output below](images/run-result.png)

Each host gets a state (*succeeded*, *failed*, *timed out*…), its exit code, the path DROOL used
(**Via**: *agent*, *SSH*, or *SSH fallback*), how long it took, and the problem if there was one.
Click a host's name to see its output. **Re-run unsuccessful hosts…** starts a new run for the hosts
that didn't succeed. [Understanding Run Results](Understanding-Run-Results.md) explains everything
on this page.

## Quoting

The command is passed to the host's shell exactly as typed, the same as typing it into a terminal
there. Quote as you would in a terminal. For example, to find large log files:

```sh
find /var/log -type f -size +100M -exec ls -lh {} +
```

and to search for a phrase with spaces, quote the phrase:

```sh
grep -r "connection refused" /var/log/nginx/
```

For anything longer than a line, or with `if`/`for`, use a [script](Running-a-Script.md).

## Next

**[Understanding Run Results](Understanding-Run-Results.md)**: what each host's state, exit code and output mean.

## Related

[Running a Script](Running-a-Script.md) · [Understanding Run Results](Understanding-Run-Results.md) ·
[Failed, Interrupted and Unknown Outcomes](Failed-Interrupted-and-Unknown-Outcomes.md)
