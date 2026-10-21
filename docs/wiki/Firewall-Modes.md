# Firewall modes

Each host has a firewall **mode**, chosen in its **Firewall** section (**Mode and environment**). New
hosts start in **Off**.

| Mode | What DROOL does |
|---|---|
| **Off** | *DROOL doesn't observe or change this host's firewall.* |
| **Monitor** | *Observe connections and explain what a policy would do. Nothing is changed.* |
| **Guided** | *Observe, and suggest policies from what is observed for you to approve. Nothing is changed.* |
| **Enforce** | *Apply the policy with automatic rollback protection — the host reverts a change by itself unless a fresh management connection verifies it — and watch for drift.* |

Choosing **Enforce** doesn't change the host by itself: DROOL's rules take effect when the policy is
applied. When you choose it, the page says so and offers **Review and apply policy…**, which shows what
changes, then switches the host to Enforce and applies the policy in one confirmed step; **Save mode
only** sets the mode and leaves applying for later ([Applying the Firewall Policy](Applying-the-Firewall-Policy.md)).
**Policy on this host**, at the top of the page, always says what is in force there, whatever the mode.

You can also choose the mode in **review and apply**, for example right after saving the policy: for a
host that isn't in Enforce mode, the review asks which mode it should have, starting at **Keep current
mode**. Only **Enforce**, chosen there, applies the policy; **Off**, **Monitor** and **Guided** change
only the mode ([A host that isn't in Enforce mode](Applying-the-Firewall-Policy.md#a-host-that-isnt-in-enforce-mode)).

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
4. Click **Save** (or, when you chose **Enforce**, **Review and apply policy…** or **Save mode only**).

![web-01's firewall page in Enforce mode: Policy on this host says In force: revision 10, the same as the saved policy; the Applied policy lists SSH from the controller (DROOL management), ssh from admin-laptop, http and https from the internet and from home-lan, everything else inbound dropped; Mode and environment: Enforce, outbound allow, the supported environment; the observed connections](images/firewall-host-enforced.png)

**Learning** under the mode says how DROOL sees this host's connections: *continuous (Shepherd)* or
*connection snapshots only (SSH host)* ([Agent vs SSH Visibility](Agent-vs-SSH-Visibility.md)).

## Stop enforcing

Switching an enforced host to **Monitor** or **Off** doesn't remove its rules: the last applied policy
stays in force, and the page says so (*Changing the mode doesn't remove DROOL's rules*). To take DROOL's
rules off the host, click **Remove DROOL policy…** under **Policy on this host**. Removal is a transaction too, protected the same way as an apply, and it works even if another
firewall manager appeared on the host since.

## Next

**[Firewall Policy](Firewall-Policy.md)**: write the rules your hosts need. In Monitor mode, the host's firewall page shows what they would do.

## Related

[Before Enabling Enforcement](Before-Enabling-Enforcement.md) · [Applying the Firewall Policy](Applying-the-Firewall-Policy.md) ·
[Firewall Policy](Firewall-Policy.md)
