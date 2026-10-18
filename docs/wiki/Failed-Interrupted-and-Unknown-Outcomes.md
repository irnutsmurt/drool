# Failed, interrupted and unknown outcomes

DROOL uses a handful of words for things that didn't simply succeed. They mean different things, and
the difference decides whether it's safe to try again. This page explains each one, and especially
the one people misread most: **outcome unknown**.

## Outcome unknown: not failed, not succeeded

> **Outcome unknown means DROOL doesn't know whether the operation happened.** It does *not* mean it
> failed, and it does *not* mean it succeeded. The change may have been made completely, partly, or
> not at all, and a command may even still be running on the host.

It happens when DROOL loses contact with a host **after** it started an operation there and before it
learned the result:

- the network connection dropped in the middle of a command (*ConnectionError: the connection to the
  host was lost while the command was running; it may have finished, failed, or still be running*);
- the controller itself stopped (a restart, a crash, an upgrade, or a restore from backup) while the
  host was running something (*interrupted*);
- a Shepherd disappeared in the middle of a step, and couldn't say afterwards how it ended.

**Why DROOL doesn't just try again:** many operations aren't safe to repeat. Running *"append this
line to a file"* twice appends it twice; *"move these files"* may fail the second time because the
files are gone; a database migration may run halfway twice. So for commands, scripts and file
operations, DROOL stops and leaves the decision to you. Only operations that are naturally safe to
repeat (installing or removing a package, a system update, putting a service in a state) are retried
by themselves after a dropped connection.

**What to do:**

1. Look at the host: did the change happen? (Run a read-only command, open the file manager, look
   at the service.)
2. If the operation is still running there (a long script), wait for it.
3. Then decide: run it again (for example with **Re-run unsuccessful hosts…**), finish it by hand, or
   leave it.

## All the outcome words

| Word | Means | Did anything change? | Safe to retry? |
|---|---|---|---|
| **Succeeded** | Done, as asked | Yes, or nothing needed to (*no change*) | — |
| **Failed** | It ran (or tried to) and DROOL knows it didn't work. The *Problem* says why | Depends: a failed command may have done half its work; refused operations changed nothing | After fixing the cause, usually yes |
| **Timed out** | Didn't finish within its time limit; DROOL stopped it | What had been done stays done | For commands and scripts, check first; package, update and service operations are retried automatically |
| **Interrupted** | DROOL stopped while the host was running it | **Unknown** | Check first (see above) |
| **Outcome unknown** | DROOL lost the connection mid-operation | **Unknown** | Check first (see above) |
| **Skipped** | Not run on purpose: no matching per-OS script, a scheduled run on a host in maintenance, a host DROOL must not change right now. The reason is shown | Nothing | Yes, once the reason is gone |
| **Cancelled** | Someone cancelled the run | Hosts that hadn't started: nothing. Running commands were terminated: partly, possibly | Check the hosts that were running |
| **Unsupported** | The host can't do this kind of operation at all: no systemd (services, reboots), no APT or DNF (packages), not Linux | Nothing | No: the host itself would have to change |
| **Incompatible** | A Shepherd too old (or too new) for this controller, or for this operation. DROOL doesn't send it operations it can't do | Nothing | After updating the Shepherd ([Shepherd Incompatible](Shepherd-Incompatible.md)) |
| **Drift** (Desired State) | A host no longer matches what a baseline says | — (it's a finding, not a run result) | [Drift](Drift.md) |
| **Conflict** (Desired State) | Two Packs say different things about the same resource on a host, with equal priority. DROOL checks against neither and restores neither | — | Resolve it first ([Conflicts](Conflicts.md)) |

## What DROOL retries by itself

Within a run, DROOL tries up to 3 times in total, waiting 5 seconds before the first retry and up to 2
minutes between later ones:

| Problem | Retried? |
|---|---|
| Couldn't connect **before** the operation started | Yes |
| Connection lost **during** the operation | Only for operations that are safe to repeat (packages, updates, service states); otherwise *outcome unknown* |
| Package manager busy, a download failed, package lists missing | Yes (APT first waits up to 2 minutes for its lock) |
| Timed out | Only for operations that are safe to repeat |
| Wrong host key, rejected credential, sudo refused | **Never**: retrying can't fix it, and repeated login failures could lock the account |
| The command exited with an error, a validation failed, a service didn't start, something is missing on the host | Never |

And across hosts: if 5 hosts in a row fail with the same credential, host-key or sudo problem, DROOL
**pauses** the run, since that usually means one shared problem (*likely a shared credential problem*)
rather than many broken hosts.

## Failure classes

The **Problem** column, and many messages, start with a class name. In plain words:

| Class | Meaning |
|---|---|
| *ConnectionError* | DROOL could not reach the host (SSH or Shepherd connection) |
| *AuthenticationError* | The host rejected DROOL's credential |
| *HostKeyError* | The host's SSH key does not match the trusted key, so DROOL did not connect |
| *AuthorizationError* | Not allowed: sudo, file permissions or the host's management state refused this |
| *TimeoutError* | The operation did not finish within its timeout |
| *ExecutionError* | The command ran but exited with an error |
| *ConfigurationError* | The host is missing something this operation needs |
| *ValidationError* | DROOL refused to make this change (for example, unapproved package removals) |
| *PackageManagerError* | The package manager reported a problem |
| *ServiceError* | The service could not be put in the requested state |
| *CancelledError* | The job was cancelled |
| *LockTimeoutError* | Another job kept this host busy for too long |
| *InterruptedError* | The controller stopped (a restart, or a restore from a backup) while this host was running; the outcome is unknown |
| *InternalError* | Something went wrong inside DROOL. The controller log has details |
| *CapabilityError* | Refused by the host's Shepherd (DROOL agent): outside what the host allows |
| *VerificationError* | A check after the change didn't pass (the host, its services or its health) |

## Related

[Understanding Run Results](Understanding-Run-Results.md) · [Run Outcome Unknown](Run-Outcome-Unknown.md)
(troubleshooting) · [SSH Fallback](SSH-Fallback.md)
