# Recovering from a firewall problem

Something stopped working after you applied a firewall policy, or a host isn't reachable. Start at the
top: most problems are fixed from DROOL's web interface, and the console steps at the end are a last
resort.

## A service stopped working after an apply

DROOL kept its own access, but something you need is now blocked: Jellyfin from the TV, backups from
the NAS, a monitoring system.

1. Open the host's firewall page → **Effective policy**. Inbound traffic not listed as **allow** is
   denied (*default for inbound traffic*).
2. Ask [Can A reach B?](Can-A-Reach-B.md) with the exact source, protocol and port. The answer names
   the rule (or the default) that decides.
3. Look at **Observed connections** (or [Network Activity](What-DROOL-Can-See.md)) for what's actually
   arriving.
4. Add the missing rule in **Firewall → Policy**, **Save policy**, then **Apply changes**.

**Quick way back:** click **Remove DROOL policy** in the host's **Enforcement** card. DROOL takes its
rules off the host (a protected transaction too), and the host is unfiltered by DROOL again while you
fix the policy. Switching the mode to Monitor or Off is **not** enough: the last committed policy
stays in force until you remove it.

## The apply was rolled back

The host couldn't be confirmed over a new connection, so it restored its previous policy by itself.
Nothing is broken; see [Firewall Apply Rolled Back](Firewall-Apply-Rolled-Back.md) for why it happens
and what to change.

## A transaction is "unknown"

*Not committed, and the host could not be reached to confirm the rollback.* The host restores its
previous policy on its own (by its timer, or at boot). When DROOL can reach the host again, it checks
and updates the transaction. If the host stays unreachable, check its power and network first, then
use the console steps below.

## DROOL's rules disappeared

If `nftables.service` was reloaded with a configuration that flushes the ruleset (Ubuntu's default
`/etc/nftables.conf` does), DROOL's rules are gone until the next boot or apply, and the host shows
**drift**. **Apply again** (choosing to replace drifted rules), see [Firewall Drift](Firewall-Drift.md).

## Locked out anyway?

If you can't reach a host at all, and DROOL can't either, use the host's **console** (hypervisor
console, monitor and keyboard, IPMI or cloud console). These commands only touch DROOL's own rules:

```sh
# If a transaction is pending: restore the last committed policy now
sudo /usr/libexec/drool-firewall/drool-fw rollback

# Remove DROOL's rules entirely (the host is then unfiltered by DROOL)
sudo nft delete table inet drool

# Stop DROOL's rules from being loaded again at the next boot
sudo systemctl disable drool-firewall.service
```

`sudo /usr/libexec/drool-firewall/drool-fw status` shows the state of DROOL's firewall on the host.

Afterwards, fix the policy in DROOL. The host shows **drift** (its rules no longer match what DROOL
committed); when the policy is right, apply again with **Replace DROOL rules that were changed outside
DROOL (drift)**, and re-enable the boot unit with `sudo systemctl enable drool-firewall.service`, or
let a successful apply do it.

## After restoring DROOL from a backup

A restored controller doesn't change any host's firewall until that host is reconciled: hosts keep the
rules they have. See [Host Reconciliation](Host-Reconciliation.md).

## Related

[Automatic Rollback Protection](Automatic-Rollback-Protection.md) · [Firewall Apply Rolled Back](Firewall-Apply-Rolled-Back.md) ·
[Firewall Drift](Firewall-Drift.md) · [Emergency Override](Emergency-Override.md)
