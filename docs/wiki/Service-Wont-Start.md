# Service won't start

A service run fails with *jellyfin.service failed to start (state: failed)*, or *… stopped right after
starting*, with the hint *The unit's recent log lines are in the output above*. Or monitoring raised
**Service failed** for a service.

## What it means

systemd tried to start the service, and the program exited with an error, or crashed, right away. DROOL
did what you asked; the service itself refuses to run. The reason is almost always in the service's own
log.

## What to do

1. **Read the run's output.** DROOL includes the service's last 15 log lines when a start fails: click the
   host's name on the run's page. Look for the first real error (*permission denied*, *address already in
   use*, *no such file*, a configuration file and a line number).
2. **More of the log**, if those lines aren't enough. Run a [command](Running-a-Command.md) with **Run as
   root (sudo)**:

   ```sh
   journalctl -u jellyfin.service -n 100 --no-pager
   ```

3. **Common causes:**
   - A configuration error, often after a file was changed: check the change, or [roll the file
     back](Rolling-Back-a-File.md). Many programs can check their own configuration (`nginx -t`,
     `sshd -t`); use that as a [validation command](Validation-Before-Deployment.md) next time.
   - **Address already in use**: another program uses the port (`ss -tlnp | grep 8096`).
   - A missing directory, wrong permissions, or a full disk (`df -h`).
4. Start it again when fixed: **Actions… → Start** on the service's row of the host's monitoring
   **Services** tab, or a service run ([Managing Services](Managing-Services.md)).

## Keeps coming back?

- If the service is in a **Desired State** baseline with **automatic restore**, DROOL tries to start it
  again after 15 minutes and after an hour, then pauses automatic restore for that service
  ([Automatic Restore](Automatic-Restore.md)).
- If you stop a service on purpose, unwatch it on the **Services** tab, or put the host in
  [maintenance](Maintenance-Mode.md) while you work on it.

## Related

[Managing Services](Managing-Services.md) · [Service Monitoring](Service-Monitoring.md) · [Desired State](What-Desired-State-Is.md)
