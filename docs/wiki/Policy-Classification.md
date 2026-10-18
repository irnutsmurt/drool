# Policy classification

Every observed connection shows what DROOL's firewall policy says about it, its **policy status**.
This is how you find the connections your rules don't cover yet, **before** you switch a host to
Enforce, and the ones that are already blocked.

A status is the policy's verdict on connections like this one. **It says what happened to the
connection only on a host where DROOL's policy is in force.** Everywhere else — a host in Off,
Monitor or Guided mode, or one whose policy isn't applied yet — it's what the policy *would* do, and
the connection says **not enforced**. A host's Network page says at the top which it is
(*Firewall: …*).

| Status | Means |
|---|---|
| ✓ **Covered by policy** | *The firewall policy allows connections like this one.* The details name the rule |
| ⛨ **Management path** | *A protected DROOL management path in the firewall policy (SSH from the controller or a jump host, or the Shepherd's connection).* Always allowed ([Management Path Protection](Management-Path-Protection.md)) |
| ? **Not in policy** | *No firewall policy says anything about connections like this one.* |
| ✕ **Denied** | *The policy enforced on this host drops connections like this one.* An observed one got through when it was seen: before the policy took effect, or around DROOL's rules |
| ✕ **Would be denied** | A deny policy names it, but the policy isn't in force on this host: nothing was blocked |
| ✕ **would be denied** (extra badge) | Not blocked today, but **would be** once the host is enforced, or the current policy is applied |
| ◌ **not enforced** (extra badge) | DROOL's policy isn't in force on this host: the status is what it would do, not what happened |
| ↻ **not applied yet** (extra badge) | The host still runs an older policy that treats this connection differently |

The status is always shown with a word and a symbol, never by colour alone. A connection's details
say the same in words (*Allowed by policy …* where it's in force, *Would be allowed by policy …*
where it isn't) and name the host firewall's state.

A host taken out of Enforce keeps its last applied policy until someone removes it: DROOL then still
counts that policy as in force, and says so. A host whose DROOL rules were removed outside DROOL
(*drift*, rules missing) counts as not filtered.

## Reading it before you enforce

On a host in **Monitor** or **Guided** mode, nothing is blocked, but the statuses show what enforcement
would do:

- **Not in policy + would be denied** on an **inbound** connection means: *this connection will stop
  working when you enforce.* Either it's wanted, and you need a rule for it, or it's unwanted, and
  enforcing will stop it. In the examples, an SSH connection from `10.10.0.10` to `media-01` would be
  denied: `media-01` is managed through its Shepherd, so plain SSH from the controller isn't its
  management path.
- **Not in policy** on an **outbound** connection is allowed by default (outbound traffic is allowed
  unless the host's outbound mode is *restrict*), but DROOL still tells you no rule mentions it.

Filter **Policy status** on the Network Activity page (under **More filters**), or use the quick
filters **Not in policy** and, under **More filters**, **Denied or would be denied**.

## Turning connections into rules

From a connection's details, **Create policy from observed connection…** starts a draft rule; on a
host in **Guided** mode, **Suggested policies** on its firewall page offers rules for what was seen.
See [Creating a Policy From Observed Traffic](Creating-a-Policy-From-Observed-Traffic.md).

## Related

[What DROOL Can See](What-DROOL-Can-See.md) · [Classifying Traffic](Classifying-Traffic.md) · [Firewall Modes](Firewall-Modes.md) · [Can A Reach B?](Can-A-Reach-B.md)
