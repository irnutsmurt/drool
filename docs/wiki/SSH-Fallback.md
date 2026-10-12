# SSH fallback

A host with a Shepherd can also keep its SSH settings. DROOL then uses the Shepherd first and SSH
only as a **fallback**, with strict rules so that the fallback never becomes a way around the
Shepherd's limits or a way to run something twice.

## Before you start

- The host has a Shepherd ([Installing a Shepherd](Installing-a-Shepherd.md)) *and* working SSH
  settings (added over SSH, key trusted).
- Changing a host's management settings needs **hosts.write** (administrators).

## The settings

On the host's **Management** card:

| Line | Meaning |
|---|---|
| **Managed via** | *Shepherd (DROOL agent)* or *SSH*: what DROOL prefers |
| **Fallback** | *SSH (only while the agent is unreachable, never to work around a refusal)*, or *none* |
| **Operations now** | What DROOL would use right now: *through the Shepherd*, *over SSH*, or *blocked* |

Buttons on the same card change them:

- **Turn off SSH fallback** — the Shepherd only. If it's offline, operations on the host wait (fail)
  instead of going over SSH.
- **Use SSH as fallback** — turns it back on.
- **Manage over SSH only** — stop using the Shepherd and use SSH for everything.
- **Prefer the Shepherd** — go back to the Shepherd first (with SSH as fallback, if SSH is set up).

A host installed from its page starts with **Shepherd first, SSH as fallback**.

## When DROOL falls back, and when it doesn't

- **The Shepherd is offline** (or too old for the operation) **before a step is sent** → DROOL uses
  SSH for that step and says so in the run result (*Via: SSH*, with the reason).
- **A step was already sent to the Shepherd** and the Shepherd vanished → DROOL **never** sends the
  same step over SSH. When the Shepherd is back, DROOL asks it how the step ended. If that can't be
  known, the result is *outcome unknown* ([why](Failed-Interrupted-and-Unknown-Outcomes.md)).
- **The Shepherd refused** (its ceiling doesn't allow it, a *CapabilityError*) → no fallback. A
  refusal is the host's decision, not a connection problem.
- **The Shepherd's identity was revoked, or is quarantined** (two machines claimed it) → the host is
  **blocked**: no Shepherd, no SSH. Someone has to look at it first. See [Shepherd PKI](Shepherd-PKI.md).

## Should you keep the fallback?

**Keep it** (the default) if you want the host manageable while its Shepherd is down, for example
during a Shepherd update gone wrong.

**Turn it off** if the Shepherd's ceiling must be the only limit. SSH is bounded only by what the
SSH account may do: with passwordless sudo, everything. The Management card warns about this when the
fallback logs in with root access: *SSH fallback logs in with root access, which this ceiling does
not limit.* For the ceiling to be absolute, turn the fallback off, and also remove DROOL's key from
the host (or restrict the account's sudo rights).

## Related

[SSH vs Shepherds](SSH-vs-Shepherds.md) · [Shepherd Capability Presets](Shepherd-Capability-Presets.md) ·
[Shepherd Offline](Shepherd-Offline.md)
