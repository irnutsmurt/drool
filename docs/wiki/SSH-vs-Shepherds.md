# Understanding SSH vs Shepherds

DROOL can manage a host in two ways. **SSH** needs nothing on the host but an account. A
**Shepherd** is DROOL's own small agent program, installed on the host. Both can do the same
operations; they differ in how they connect, what they can see, and who decides what DROOL may do.

You don't have to choose once and for all. Every host starts on SSH in this wiki's examples, and you
can add a Shepherd to any of them later (or remove it again).

## The short answer

- **Start with SSH.** It works on any reachable Linux machine, installs nothing, and is how you add
  a host in the first place: with an account you already have there, which DROOL uses to create its
  own account ([DROOL's Own SSH Account](Dedicated-SSH-Account.md)).
- **Add a Shepherd** to a host when any of these matters:
  - DROOL **can't reach the host** (it's behind NAT, in another network, on a laptop that moves):
    a Shepherd connects *out* to DROOL, so the host needs no open port.
  - You want **continuous monitoring** (every 10 seconds, with history kept even while DROOL is
    unreachable) instead of a snapshot every 5 minutes.
  - You want **limits set on the host itself** that DROOL can narrow but never widen, even if DROOL
    were compromised.
  - You want to **see network connections continuously** instead of by snapshot.

In the examples, `media-01` has a Shepherd; `media-02`, `database-01` and `web-01` stay on SSH.

## Side by side

| | SSH | Shepherd |
|---|---|---|
| **What's on the host** | Nothing but DROOL's account (`drooladmin`, which DROOL creates itself: [DROOL's Own SSH Account](Dedicated-SSH-Account.md)) and DROOL's key | The `drool-agent` program and service, plus its settings in `/etc/drool/agent.yaml` |
| **Who connects to whom** | DROOL connects *to* the host's SSH port (directly or through one [jump host](Jump-Hosts.md)) | The Shepherd connects *out* to DROOL on its Shepherd port (**9443** by default); the host needs no open port |
| **How trust works** | You approve the host's SSH key once; DROOL refuses to connect if it changes ([details](SSH-Host-Key-Verification.md)) | Both sides prove themselves with certificates DROOL issues at enrollment; the Shepherd's private key never leaves the host ([details](Shepherd-PKI.md)) |
| **What DROOL may do** | Whatever the SSH account may do (with sudo: everything) | At most what the host's own `/etc/drool/agent.yaml` allows ([presets](Shepherd-Capability-Presets.md)) |
| **Monitoring** | A snapshot every 5 minutes; disk health hourly (only with passwordless sudo) | Continuous: samples every 10 seconds, services every 30 seconds, disk health every 30 minutes; kept on the host while DROOL is unreachable and delivered later |
| **Network Activity** | A snapshot when you click **Take snapshot** | Continuous |
| **Updates of the agent itself** | — | Automatic when DROOL is updated, only to releases signed by the DROOL project or with your own key; the host can refuse them ([Updating Shepherds](Updating-Shepherds.md)) |
| **CPU architectures** | Any (amd64, arm64 tested; others expected to work) | amd64 and arm64 only |
| **Needs systemd** | For services and reboots | For services, reboots and its resource limits |

## What a host with a Shepherd looks like

In the host's **Management** section, the **Connection** card says how DROOL reaches the host (the
header's *Managed via* says it in short):

- **Managed via** — *SSH* or *Shepherd (DROOL agent)*.
- **Fallback** — for Shepherd hosts: *SSH (only while the agent is unreachable, never to work around
  a refusal)* or *none*.
- **Operations now** — what DROOL would use this very moment: *through the Shepherd*, *over SSH*, or
  *blocked*.

The Hosts list shows the same in its **Managed via** column, and every run result says which path
each host used.

## How SSH fallback works

When a host has a Shepherd *and* SSH settings, SSH can serve as a fallback. The rules are strict on
purpose:

- **Before** a step is sent: if the Shepherd isn't connected (or can't do that kind of operation
  because it's too old), DROOL uses SSH instead and says so in the run.
- **After** a step was sent to the Shepherd, DROOL never switches to SSH for that step. If the
  Shepherd disappears mid-step, DROOL asks it what happened once it's back; if that isn't possible,
  the result is [outcome unknown](Failed-Interrupted-and-Unknown-Outcomes.md).
- **A refusal is final.** If the host's own limits forbid something (a *CapabilityError*), DROOL
  does not try again over SSH.
- **A revoked or quarantined Shepherd blocks the host** entirely. DROOL doesn't quietly fall back to
  SSH: someone has to look at it first.

Keep in mind that SSH, when configured, is limited only by the SSH account's rights. If the
Shepherd's limits must be absolute, turn the fallback off. See [SSH Fallback](SSH-Fallback.md).

DROOL's own SSH account (passwordless sudo, so root-equivalent) can be a Shepherd host's SSH path or
fallback like any other account. The Shepherd itself doesn't use or need it: a Shepherd-only host
needs no SSH account at all.

## What installing a Shepherd changes on a host

- the program, in `/usr/libexec/drool-agent/`;
- its limits, in `/etc/drool/agent.yaml` (owned by root, readable only by root);
- its identity and state, in `/var/lib/drool-agent/`;
- a log of every operation it received (no command text, file contents or secrets), in
  `/var/log/drool-agent/operations.log`;
- the systemd service `drool-agent.service`, which starts at boot.

It runs within a small resource budget enforced by systemd (at most a quarter of one CPU core and
160 MB of memory by default), and backs off further when the host is busy. Jobs you run through it,
such as package updates, aren't limited by that budget.

## Next

**[Installing a Shepherd](Installing-a-Shepherd.md)**, on a host where one of the reasons above
applies. Otherwise there's nothing to do: SSH hosts need nothing more.

## Related

[Shepherd Capability Presets](Shepherd-Capability-Presets.md) ·
[Monitoring Through SSH vs Shepherd](Monitoring-Through-SSH-vs-Shepherd.md)
