# Automatic rollback protection

Every time DROOL changes a host's firewall, the host is ready to **undo the change by itself**. If DROOL
can't prove the host is still reachable after the change, the host restores its previous rules on its
own. So even a policy that accidentally cuts DROOL off can't leave DROOL locked out of the host.

It protects **DROOL's own connection**, not everything else: see
[What it doesn't protect](#what-it-doesnt-protect).

## How an apply works

1. **DROOL checks** the host's firewall environment and **measures a brand-new management connection**
   (a new SSH connection through every jump host, or a new Shepherd connection).
2. **The host prepares to undo**: it writes a durable journal and arms a **rollback timer** *before*
   its rules change, then applies the new rules in one atomic step.
3. **DROOL commits** over a **new** connection. An old, already-open connection proves nothing,
   because firewalls keep existing connections alive; only a brand-new one shows the new rules let
   DROOL in.
4. **Without that commit, the host rolls back.** When the timer runs out, or at the next boot if it
   restarted, the host restores its previous policy on its own. This covers a policy that cuts the
   path, a controller that crashed or lost its network, a Shepherd that died, and a host that rebooted
   in the middle.

The transaction then shows **rolled back**, with the reason: *the host restored its previous policy:
…*.

## What it doesn't protect

Whether a change stays is decided by **DROOL's own connection** alone. A policy that blocks other access
but leaves DROOL's connection working **commits and stays**. That includes your own SSH from a laptop,
a web interface, backups or monitoring. Nothing rolls it back.

This matters most on hosts managed through a **Shepherd**. There, DROOL's connection is the Shepherd's
connection to the controller, not SSH. For example, on `media-01` with only `lan-to-jellyfin`, the
Shepherd keeps working while SSH from `admin-laptop` is dropped, so the apply commits. On an SSH host,
DROOL's own SSH is allowed ahead of every rule, but only from the address DROOL connects from.

Before applying:

- Read the host's **Policy preview**: *Everything not allowed below is dropped.* Anything not listed,
  your own SSH included, is dropped once the policy is in force.
- Look at **Observed connections** on the same page. Connections marked *would be denied* are the ones
  the policy would drop: allow what you need with a rule (*admin-ssh-to-media* in the examples).
  DROOL never allows observed traffic by itself.
- The review says so too: *Automatic rollback protects only DROOL's own connection*.

If it happened anyway, DROOL still manages the host. Add the missing rule and **Review and apply
changes…**, or **Remove DROOL policy…** on the host's firewall page
([Recovering From a Firewall Problem](Recovering-From-a-Firewall-Problem.md)).

## Transaction states

| State | Meaning |
|---|---|
| **preparing** / **pending** | The apply is under way, or waiting for its commit |
| **committed** | Confirmed over a new connection: the new policy stays |
| **rolled back** | Not confirmed: the host restored its previous policy |
| **failed** | Refused or failed **before** anything changed on the host |
| **unknown** | Not committed, and DROOL couldn't reach the host afterwards to confirm the rollback. *The host restores its previous policy on its own (timer or boot)*; DROOL checks again when it can reach it |

**Firewall → Transactions** lists every transaction; each host's firewall page has its own
**Transactions** tab.

## The rollback timeout

How long a host waits for the commit before restoring its previous rules. The default is **120
seconds**:

- **Firewall → Settings → Default rollback timeout (seconds)** for every host;
- **Rollback timeout per Pack** (**Change** on a Pack): a host uses the shortest of its Packs';
- a host's own **Rollback timeout (s)** on its firewall page wins over both.

A longer timeout gives slow hosts and slow links more time; a shorter one shortens how long a bad
policy stays in force. DROOL refuses a timeout shorter than **3 times** the connection time it
measured (a commit would hardly have time to arrive), unless an administrator ticks the audited
override *Accept a rollback timeout shorter than 3× the measured connection time*.

## Good to know

- The previous policy the host returns to is the one it had **before** this transaction: DROOL's last
  committed policy, or no DROOL rules at all.
- While a transaction is pending, DROOL won't reboot the host, update it or restart its network
  services.
- Removing DROOL's policy (**Remove DROOL policy**) is a transaction too, protected the same way.
- Even the [emergency override](Emergency-Override.md) is protected by automatic rollback.

## Related

[Applying the Firewall Policy](Applying-the-Firewall-Policy.md) · [Management Path Protection](Management-Path-Protection.md) ·
[Firewall Apply Rolled Back](Firewall-Apply-Rolled-Back.md)
