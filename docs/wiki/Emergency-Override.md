# Emergency override

DROOL refuses to apply a firewall policy that would cut its own management path
([Management Path Protection](Management-Path-Protection.md)). The **emergency override** lets an
administrator apply it anyway. It exists for one situation: **recovering from a mistake**, when the
only way forward is a policy DROOL's checks object to (for example, a management path that changed and
DROOL's measurement is out of date).

> **Read this first.** DROOL may lose contact with the host as soon as the rules are applied. If the
> new policy keeps a path DROOL didn't know about, the override can *succeed* and leave DROOL depending
> on that path. In almost every case, fixing the policy is the better choice.

## What still protects you

**Automatic rollback protection still applies**: if DROOL can't commit over a brand-new connection, the
host restores its previous policy on its own when its rollback timeout runs out
([Automatic Rollback Protection](Automatic-Rollback-Protection.md)). The override, your reason and the
outcome are recorded in the [audit log](Audit-Log.md).

## Before you start

- You need **firewall.override** (administrators).
- Have console access to the host ready, in case you need to [recover by hand](Recovering-From-a-Firewall-Problem.md#locked-out-anyway).

## Steps

1. Click **Apply policy** (or **Apply changes**) on the host's firewall page.
2. Open **Overrides (audited)** in the dialog.
3. Tick **Emergency override: apply even if a deny removes a management path**. A red box explains the
   consequences.
4. Type `OVERRIDE` in **Type OVERRIDE to confirm**.
5. Enter a **Reason (recorded in the audit log)**, at least 5 characters, for example *SSH moved to
   the new jump host; DROOL's measurement is from yesterday*.
6. Click **Apply with emergency override**.

The host's firewall page then says *Applied with the emergency override.*

## The other overrides

The same **Overrides (audited)** section has two more, each also needing a reason:

- **Accept a rollback timeout shorter than 3× the measured connection time**
  ([The rollback timeout](Automatic-Rollback-Protection.md#the-rollback-timeout)).
- **Replace DROOL rules that were changed outside DROOL (drift)** ([Firewall Drift](Firewall-Drift.md)).

## Related

[Management Path Protection](Management-Path-Protection.md) · [Recovering From a Firewall Problem](Recovering-From-a-Firewall-Problem.md)
