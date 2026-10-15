# Managing services

DROOL starts, stops, restarts and reloads systemd services, and switches whether they start at boot.
After each change it checks on the host that the service really is in the state you asked for, so
"started" means *running*, not "the start command returned". There are two ways: from a host's
**Services** tab, for one service, or as a run on many hosts.

## Before you start

- You need **jobs.execute** (administrators and operators).
- The hosts need **systemd**, and sudo or a root login. Shepherd hosts need the **Managed** or
  **Full** preset; an administrator may also have [narrowed](Shepherd-Capability-Presets.md#narrowing-from-drool)
  a Shepherd to certain services only.
- You need the service's **unit name**, for example `jellyfin`, `nginx`, `ssh` (Debian and Ubuntu) or
  `sshd` (RHEL family). DROOL adds `.service` if you leave it out. A host's services are listed on its
  monitoring page ([Service Monitoring](Service-Monitoring.md)).

## From a host's Services tab

On the host's monitoring page, **Services** tab, click **Actions…** on a service's row. The dialog
shows the service as it is (**Now**: running or not; **At boot**: its unit-file state; **Monitored**:
whether DROOL monitors it, and what it expects) and offers only the actions that make sense for it:

| The service is | Actions offered |
|---|---|
| running, enabled at boot | **Stop**, **Restart**, **Disable**, **Disable & Stop** |
| stopped, disabled | **Start**, **Enable**, **Enable & Start** |
| running, disabled | **Stop**, **Restart**, **Enable** |
| stopped (or failed), enabled | **Start**, **Disable** |
| `static`, `indirect`, `generated` or an alias | **Start** or **Stop**/**Restart**, never Enable or Disable, and the dialog says why (*Static: started by other units, sockets or timers, not at boot by itself, so it can't be enabled or disabled.*) |
| `masked` | nothing that starts or enables it (only **Stop**, if it runs): *Masked: systemd won't start or enable it until it's unmasked on the host (systemctl unmask); DROOL doesn't unmask units.* |
| transient (made at runtime) | only **Stop**, if it runs |

**Start**/**Stop** change it now, and leave the boot setting alone; **Enable**/**Disable** change
the boot setting, and leave it running or stopped as it is; **Enable & Start** and **Disable & Stop**
do both.

Pick one, read what it does, and confirm. It runs as an ordinary run (on the **Runs** page, audited),
over SSH or through the Shepherd. When it's done, the dialog shows what DROOL found on the host
afterwards, for example *Stop: done. DROOL checked on media-01: jellyfin.service is now inactive
(dead), enabled at boot.*, and the Services tab refreshes.

### The expected state stays as it is

Stopping, disabling or starting a service never changes whether DROOL [monitors](Service-Monitoring.md#monitor) it or what it
[expects](Service-Monitoring.md#customize-the-expected-state) of it. Running and expectation are separate on purpose: a service you
stop for a minute shouldn't quietly stop being monitored.

- Before **Stop**, **Disable** or **Disable & Stop** on a service expected to run, the confirmation
  warns: *jellyfin.service is expected to run (set on this host). Stopping it raises a health issue,
  and an alert, until it runs again or you change its expected state. DROOL doesn't change that for
  you.* The same for starting a service expected to be stopped.
- Afterwards, the dialog offers the change as a separate step: **Mark it expected stopped on this
  host** (or *running*). Nothing changes unless you click it.

## As a run on many hosts

1. **Runs → New run → Service**.
2. **Service:** the unit name. DROOL suggests names it has seen on your hosts as you type.
3. **Action:**

   | Action | What happens |
   |---|---|
   | **Start** | Starts the service; nothing if it's already running |
   | **Stop** | Stops it; nothing if it's already stopped |
   | **Restart** | Stops and starts it, even if it was running |
   | **Reload configuration** | Asks the service to reread its configuration without stopping (only for services that support it, such as nginx) |
   | **Enable at boot** / **Disable at boot** | Changes whether it starts at boot; doesn't start or stop it now |
   | **Enable at boot and start** / **Disable at boot and stop** | Both at once |

4. Choose the **Targets**, then **Run on …** and confirm.

If the change contradicts [Desired State](What-Desired-State-Is.md) (for example stopping a service a
baseline says must run), New run warns you before you start, for example *On media-01:
jellyfin.service is managed to keep running … Stopping it will create desired-state drift*. If
automatic restore is on for that baseline, DROOL may start it again later; use
[maintenance](Maintenance-Mode.md) or change the baseline if the change is intentional.

## How DROOL checks the result

- After **Start** or **Restart**, the service must be running **and still running two seconds
  later**. A service that crashes right after starting is reported as failed (*ServiceError*), with
  its state and the last lines of its log, instead of looking like a success.
- After **Stop**, it must be stopped; after **Enable**/**Disable**, the boot setting must have
  changed.
- Actions that find the service already in the requested state change nothing and report **no
  change**.

## When it fails

| Message | Meaning |
|---|---|
| *service cron.service not found* — *Check the name; the package that provides it may not be installed.* | No such unit on that host. Different distributions name some services differently (`ssh` vs `sshd`) |
| *jellyfin.service failed to start (state: failed)* — *The unit's recent log lines are in the output above.* | The service didn't start, or started and exited again (the output then also says it *stopped right after starting*). Its last 15 log lines are in the run output, and usually say why (often a configuration error) |
| *app.service cannot be enabled (unit file state: static)* | The unit has no boot setting of its own; it's started by another unit, a socket or a timer |
| *app.service is masked* — *Unmask it on the host first (systemctl unmask) if it should run.* | systemd refuses to start or enable a masked unit; DROOL doesn't unmask units |
| *app.service is still running after stop* / *unit file state is enabled after disable* | systemctl said yes, but the host says otherwise: DROOL reports what it found |
| *CapabilityError* | The host's Shepherd doesn't allow service changes, or not for that service |
| *Unsupported* host | No systemd on that host (most containers) |

See also [Service Won't Start](Service-Wont-Start.md).

## Related

[Service Monitoring](Service-Monitoring.md) · [Reloading or Restarting a Service](Reloading-or-Restarting-a-Service.md)
(after deploying a file) · [Workflows](Workflows.md) (check services after an update)
