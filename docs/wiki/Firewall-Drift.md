# Firewall drift

**Firewall drift** means DROOL's own rules on a host were changed **outside DROOL**: someone edited
the `inet drool` table with `nft`, or reloaded `nftables.service` with a configuration that flushes
the whole ruleset. DROOL notices at its next check and shows it, because the host no longer has the
policy DROOL committed.

## Where you see it

- **Firewall → Hosts**: the host's status is **drift**.
- The host's firewall page: *DROOL's rules were changed outside DROOL (drift)*, and in the
  **Enforcement** card *Drift: … (for example by nft or a reload of nftables.service). Apply again and
  choose to replace them.*

## What to do

1. Find out who changed the rules and why. A manual change may have been an emergency fix that
   someone still needs, or it may be the cause of a problem.
2. If the change should stay, put it into the [policy](Firewall-Policy.md) as a rule.
3. **Apply again**. A normal apply **refuses** to overwrite drifted rules. An administrator with
   **firewall.override** opens **Overrides (audited)** in the apply dialog, ticks **Replace DROOL
   rules that were changed outside DROOL (drift)**, enters a **reason**, and applies.

The replacement is recorded in the [audit log](Audit-Log.md) with the reason, and protected by
[automatic rollback](Automatic-Rollback-Protection.md) like every apply.

## Avoid it

- Don't edit DROOL's table (`inet drool`) by hand; change the policy in DROOL.
- On Ubuntu, `/etc/nftables.conf` starts with `flush ruleset`. Reloading `nftables.service` then
  removes DROOL's rules until the next boot or DROOL apply (DROOL's boot unit loads after it). DROOL
  shows this warning under **Firewall environment** on the host's firewall page.
- Rules in **other** nftables tables aren't drift: DROOL ignores them. But another firewall manager
  appearing (Docker, UFW, firewalld) makes DROOL refuse to enforce on that host
  ([What DROOL's Firewall Does](What-DROOLs-Firewall-Does.md#where-enforcement-works)).

## Related

[Applying the Firewall Policy](Applying-the-Firewall-Policy.md) · [Emergency Override](Emergency-Override.md) ·
[Recovering From a Firewall Problem](Recovering-From-a-Firewall-Problem.md)
