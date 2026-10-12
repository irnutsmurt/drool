# Policy classification

Every observed connection shows what DROOL's firewall policy says about it, its **policy status**.
This is how you find the connections your rules don't cover yet, **before** you switch a host to
Enforce, and the ones that are already blocked.

| Status | Means |
|---|---|
| ✓ **Covered by policy** | *A configured firewall policy allows this connection.* The details name the rule |
| ⛨ **DROOL management** | *A protected DROOL management path (SSH from the controller or a jump host, or the agent's connection).* Always allowed ([Management Path Protection](Management-Path-Protection.md)) |
| ? **Unclassified** | *No firewall policy says anything about this connection.* |
| ✕ **Denied** | *A deny policy, or the default drop on an enforced host, blocks this connection.* |
| ✕ **would be denied** (extra badge) | Not blocked today, but **would be** once the host is enforced, or the current policy is applied |
| ↻ **not applied yet** (extra badge) | The host still runs an older policy that treats this connection differently |

The status is always shown with a word and a symbol, never by colour alone.

## Reading it before you enforce

On a host in **Monitor** or **Guided** mode, nothing is blocked, but the statuses show what enforcement
would do:

- **Unclassified + would be denied** on an **inbound** connection means: *this connection will stop
  working when you enforce.* Either it's wanted, and you need a rule for it, or it's unwanted, and
  enforcing will stop it. In the examples, an SSH connection from `10.10.0.10` to `media-01` would be
  denied: `media-01` is managed through its Shepherd, so plain SSH from the controller isn't its
  management path.
- **Unclassified** on an **outbound** connection is allowed by default (outbound traffic is allowed
  unless the host's outbound mode is *restrict*), but DROOL still tells you no rule mentions it.

Filter **Policy status** on the Network Activity page, or use the **Unclassified** and **Denied or
would be denied** quick filters.

## Turning connections into rules

From a connection's details, **Create policy from observed connection…** starts a draft rule; on a
host in **Guided** mode, **Suggested policies** on its firewall page offers rules for what was seen.
See [Creating a Policy From Observed Traffic](Creating-a-Policy-From-Observed-Traffic.md).

## Related

[What DROOL Can See](What-DROOL-Can-See.md) · [Firewall Modes](Firewall-Modes.md) · [Can A Reach B?](Can-A-Reach-B.md)
