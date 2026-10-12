# Host shows Unknown

Something about a host says **Unknown**: its health, a service's state, a Desired State resource, a
storage device. **Unknown means DROOL couldn't check, not that something is wrong**, and DROOL never
treats it as fine either. The list of every place it appears: [What "Unknown" Means](What-Unknown-Means.md).

## Find the reason

DROOL usually says why next to the Unknown, for example *couldn't check: host nas-01 has no approved
host key*. The most common reasons:

| Reason | What to do |
|---|---|
| **The host isn't reachable** (status *Unreachable*, *Host key changed*, *Access denied*, *Needs host key*) | Fix the connection first: [Troubleshooting](Troubleshooting.md#hosts-and-connections) |
| **The data is too old** | [Monitoring Is Stale](Monitoring-Is-Stale.md) |
| **Monitoring is off** (Desired State services and workflow checks read monitoring) | **Monitoring → Health checks and settings → Monitor hosts** ([Monitoring Settings](Monitoring-Settings.md#collection)) |
| **The host was just added** | Wait for the first check (up to 5 minutes for SSH hosts; Desired State checks packages and files every 15 minutes, or click **Check now**) |
| **The host can't report it** (a virtual disk's health, no sensors in a VM, an SSH host without passwordless sudo for SMART data) | Often normal: see [Storage Health](Storage-Health.md) and [Temperatures](Temperatures.md) |
| **The host is in maintenance** | Maintenance doesn't stop monitoring, but the reason it's unknown is usually one of the above |

## Desired State: Unknown

On **Desired State → Overview**, the **Unknown** filter lists hosts DROOL couldn't verify, with the
reason under **What differs**. Unknown resources aren't restored automatically, and can't be accepted:
DROOL has to see the state first. **Check now** on the host's Desired State page tries again right away.

## Related

[What "Unknown" Means](What-Unknown-Means.md) · [Monitoring Is Stale](Monitoring-Is-Stale.md) · [Host Won't Connect](Host-Wont-Connect.md)
