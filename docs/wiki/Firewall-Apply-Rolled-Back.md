# Firewall apply rolled back

A firewall apply ended as **rolled back**: *the new policy was not confirmed over a new connection and
was rolled back*, with the hint *The host kept its previous policy. Check the management path in the
preview and the transaction timeline.*

## What it means

[Automatic rollback protection](Automatic-Rollback-Protection.md) worked: after applying the new rules,
the host waited for DROOL to confirm over a **brand-new** connection, and that confirmation didn't arrive
before the rollback timeout. So the host restored its previous policy by itself. **Nothing is broken**:
the host has exactly the rules it had before.

## Why the confirmation didn't arrive

| Cause | How to tell | What to do |
|---|---|---|
| **The new policy blocked DROOL's new connection** | The policy changes something on DROOL's path: a deny, a changed Pack or network, the controller's or jump host's address changed since the last transaction | Check **Management path** with **Check** on the host's firewall page; read the preview's *Transaction safety blockers*; fix the policy ([Management Path Protection](Management-Path-Protection.md)) |
| **The host or the network was too slow** | The transaction timeline shows a long *measured* connection time; slow hosts (a Raspberry Pi under load) or slow links | Give the host (or its Pack) a longer **Rollback timeout** ([The rollback timeout](Automatic-Rollback-Protection.md#the-rollback-timeout)) |
| **The host rebooted** during the transaction | The reason says the host rebooted | Apply again when it's up |
| **The controller lost the host** (network outage, controller restart) | The reason mentions the connection | Apply again |

## What to do

1. Open the host's firewall page → **Transactions**, and read the transaction's timeline and reason.
2. Fix the cause (table above).
3. **Apply** again.

## If the transaction is "unknown" instead

DROOL couldn't reach the host afterwards to confirm the rollback. The host still restores its previous
policy on its own; DROOL checks again when it can reach it
([Recovering From a Firewall Problem](Recovering-From-a-Firewall-Problem.md#a-transaction-is-unknown)).

## Related

[Automatic Rollback Protection](Automatic-Rollback-Protection.md) · [Applying the Firewall Policy](Applying-the-Firewall-Policy.md) ·
[Recovering From a Firewall Problem](Recovering-From-a-Firewall-Problem.md)
