# Rebooting hosts

DROOL reboots a host safely: it schedules the reboot, waits for the host to go away and come back,
and only reports success when the host answers again **with the SSH key you trusted and a new boot
id**. That last part matters: it proves the machine really restarted, and that it's the same machine.

## Before you start

- You need **jobs.execute** (administrators and operators).
- The host needs **systemd**, and sudo or a root login. Shepherd hosts need the **Managed** or
  **Full** preset (the reboot capability).
- **Everything running on the host stops** until it's back: services, users' sessions, other runs.
  For a host others depend on, consider [maintenance mode](Maintenance-Mode.md) first so nobody gets
  alerts about it, and check that its services start by themselves at boot.

## Steps

1. **Runs → New run → Reboot**, or **Actions → Reboot…** on a host's page.
2. **Wait for each host to come back** is ticked by default: the run succeeds only when the host
   answers with its trusted key and a new boot id. Within a run, the next batch of hosts starts only
   after this. Untick it only if you don't care whether the hosts come back.
3. Choose the **Targets**. For several hosts that serve the same purpose (`media-01` and `media-02`),
   make sure they never reboot at the same time: **Change rollout…** and set the **Batch size** to 1
   ([Batches and Failure Thresholds](Batches-and-Failure-Thresholds.md)).
4. Click **Review: reboot 2 hosts…**. DROOL's review, *Reboot 2 hosts?*, lists the hosts and how the
   reboots roll out, and says *Everything running on them stops until they come back.* Click
   **Reboot 2 hosts**.

## What DROOL does on each host

1. Notes the host's current boot id.
2. Schedules the reboot a couple of seconds ahead with systemd (`systemd-run … systemctl reboot`), so
   its own command finishes cleanly before the machine goes down.
3. Waits for the host to disappear, then polls until it's back (host state *rebooting*, then
   *waiting for host to return*, then *verifying*).
4. Checks the trusted host key and that the boot id is new.

The default time limit is 900 seconds (15 minutes).

## When it doesn't work out

| Result | Meaning | What to do |
|---|---|---|
| *Timeout* — the host *never went away* | The reboot didn't happen (the host kept the same boot id). DROOL doesn't count it as a change. | Look at the host: a stuck shutdown, an inhibitor, or a reboot that was refused |
| *Timeout* — the host didn't come back in time | It went down but didn't answer again within the limit | Check its console: boot problems, a disk check, a network change |
| *HostKeyError* | It came back with a **different** SSH key | Someone or something else answers at that address now, or the machine was reinstalled. See [SSH Host Key Changed](SSH-Host-Key-Changed.md) |
| Refused | A firewall change is waiting for confirmation on that host | Wait until the firewall transaction is committed or rolled back (a couple of minutes) |

## Related

[System Updates](System-Updates.md) (can reboot only when needed) · [Maintenance Mode](Maintenance-Mode.md) ·
[Understanding Run Results](Understanding-Run-Results.md)
