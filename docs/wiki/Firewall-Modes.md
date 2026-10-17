# Firewall modes

Each host has a firewall **mode**, chosen in its **Firewall** section (**Mode and environment**). New
hosts start in **Off**.

| Mode | What DROOL does |
|---|---|
| **Off** | *DROOL doesn't observe or change this host's firewall.* |
| **Monitor** | *Observe connections and explain what a policy would do. Nothing is changed.* |
| **Guided** | *Observe, and suggest policies from what is observed for you to approve. Nothing is changed.* |
| **Enforce** | *Apply the policy with automatic rollback protection — the host reverts a change by itself unless a fresh management connection verifies it — and watch for drift.* |

Choosing **Enforce** doesn't change the host yet: you still have to **Apply policy**
([Applying the Firewall Policy](Applying-the-Firewall-Policy.md)).

## Before you start

You need **firewall.write** (administrators). Read [Before Enabling Enforcement](Before-Enabling-Enforcement.md)
before choosing Enforce.

## Change a host's mode

1. Open the host's **Firewall** section.
2. Under **Mode and environment**, choose the **Mode**.
3. Optionally change:
   - **Outbound traffic**: **allow** (*Not filtered (denies still apply)*, the default) or **restrict**
     (*Only what policies allow (plus the management path)*). With **restrict**, the host can only
     start connections your rules allow: DNS, NTP, package downloads and updates all need rules.
   - **Rollback timeout (s)**: how long the host waits for DROOL to confirm a change before restoring
     its previous rules. Empty: inherited (*now 120s (default)*). See
     [Automatic Rollback Protection](Automatic-Rollback-Protection.md#the-rollback-timeout).
4. Click **Save**.

![web-01's firewall page in Enforce mode: outbound allow, rollback timeout 120 s (default), the supported environment with the nftables.conf warning, Enforcement committed at revision 1 over SSH, and the effective policy: SSH from the controller (management path), SSH from admin-laptop, HTTP and HTTPS from the internet, everything else inbound denied](images/firewall-host-enforced.png)

**Learning** under the mode says how DROOL sees this host's connections: *continuous (Shepherd)* or
*connection snapshots only (SSH host)* ([Agent vs SSH Visibility](Agent-vs-SSH-Visibility.md)).

## Stop enforcing

Switching an enforced host to **Monitor** or **Off** doesn't remove its rules: the last applied policy
stays in force. To take DROOL's rules off the host, click **Remove DROOL policy** in the **Enforcement**
card. Removal is a transaction too, protected the same way as an apply, and it works even if another
firewall manager appeared on the host since.

## Next

**[Firewall Policy](Firewall-Policy.md)**: write the rules your hosts need. In Monitor mode, the host's firewall page shows what they would do.

## Related

[Before Enabling Enforcement](Before-Enabling-Enforcement.md) · [Applying the Firewall Policy](Applying-the-Firewall-Policy.md) ·
[Firewall Policy](Firewall-Policy.md)
