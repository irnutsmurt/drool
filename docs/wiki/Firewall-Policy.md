# Firewall policy

The **firewall policy** is the list of rules for all your hosts: who may reach whom, over which
services. You edit it in **Firewall → Policy**, which shows the same policy three ways. Use whichever
suits you; the other two follow immediately.

| View | Best for |
|---|---|
| [Topology](Topology-View.md) | Seeing the whole picture, and drawing rules with the mouse |
| [Rules](Rules-View.md) | Forms and tables, in order; works with the keyboard and on a phone |
| [YAML](YAML-View.md) | Writing or pasting the whole policy as text |

## What a rule says

Each rule has:

- a **Name**, for example `web-to-database`;
- an **Action**: **Allow**, or **Deny (wins over allows)**;
- a **Source** (*who starts the connection*) and a **Destination** (*who receives it*), each one or
  more of: a host, a Pack, a named network, an address or range, **Internet** (*everything outside the
  internal ranges*), **Internal networks** or **Anywhere**
  ([Networks and CIDRs](Networks-and-CIDRs.md), [Packs in Firewall Rules](Packs-in-Firewall-Rules.md));
- one or more **Services**: built-in ones such as `ssh` or `https`, or your own
  ([Services and Ports](Services-and-Ports.md));
- an optional **Description**.

A rule `web-servers → database-01: postgres` means: hosts in `web-servers` may open connections to
`database-01` on TCP 5432. DROOL turns that into an **inbound** allow on `database-01` and, if
`web-01`'s outbound traffic is restricted, an **outbound** allow on `web-01`.

## The destination says which hosts get the rule

DROOL puts a rule on the hosts its **destination** names: a host, a Pack, or **Anywhere** (every host
DROOL manages). A destination written as addresses (**Internal networks**, **Internet**, a named
network, an address or range) names no host, **even one whose address is inside it**. As a destination,
addresses only limit what the *source* hosts may connect to (outbound).

So *Source Internal networks → Destination Internal networks: ssh* is on no host's firewall, and
neither is *Destination 192.168.3.50* for the host at that address. DROOL says so wherever such a rule
shows up:

- the **Preview**, and the summary after saving: *Rule ssh-internal applies to no host. Its
  destination, Internal networks, is a set of addresses, not a DROOL host… PiZeroWopenwx's address is in
  Internal networks: to allow ssh to it, make it the destination.*;
- the **Rules** view marks the rule *⚠ applies to no host*, and the rule's editor says *No host gets
  this rule…*;
- the firewall page of a host whose address the destination contains: *Rule ssh-internal isn't part
  of this host's policy…*.

To let your LAN reach SSH on a host, make the **host** the destination (*Internal networks →
PiZeroWopenwx: ssh*); on every host, use **Anywhere** or a Pack.

## How a host's rules are evaluated

From top to bottom: replies to allowed connections, DROOL's management path, your **denies**, then
your **allows**; anything inbound that's left is denied, anything outbound that's left is allowed (or
denied, with *restrict*). So a deny always wins over an allow, and DROOL's own access always comes
first. The order of your allow rules doesn't change the result.

## Save, then apply

Changes in any view are a **draft** (*Unsaved changes*) until you click **Save policy**. **Undo**
takes back the last change; **Discard changes** throws the draft away.

The **Preview** panel on the right lists, before you save, every host that would change (*+ allow
inbound TCP 5432 from group web-servers (policy web-to-database)*), hosts where enforcement is refused,
problems, and anything that would block applying, such as a
[blocked management path](Management-Path-Protection.md).

**Saving changes no host, and no host's mode.** *Saved. Nothing has changed on any host yet.* DROOL then
lists the hosts the change affects, where each one stands (*Policy saved — not yet enforced*, *Not
applied yet: the host runs revision 1*), and asks *Would you like to review and apply these changes
now?* for those it can apply to now. **Save and review application…** saves and goes straight to that
review, which asks, for each host not in Enforce mode, which mode it should have. See
[Applying the Firewall Policy](Applying-the-Firewall-Policy.md#right-after-saving-the-policy). Each
host's **Firewall** section shows the saved policy as it applies to that host, and whether it is in
force.

## Before you start

- Everyone can look at the policy and use **Can it reach…?**. Changing it needs **firewall.write**
  (administrators): *You can explore and simulate; only administrators can change the firewall
  policy.*
- One person editing at a time works best. If someone else saved the policy since you opened it,
  saving is refused (*Someone else saved the firewall policy since you opened it…*): copy your changes
  from the YAML view, discard, and reapply them.

## Next

**Check it, then apply it.** Look at the **Preview** panel, and ask [Can A Reach B?](Can-A-Reach-B.md) about the connections that matter (*can web-01 reach database-01 on TCP 5432?*). Then save, and [review and apply the policy](Applying-the-Firewall-Policy.md), one host first.

## Related

[Topology View](Topology-View.md) · [Rules View](Rules-View.md) · [YAML View](YAML-View.md) ·
[Can A Reach B?](Can-A-Reach-B.md) · [Applying the Firewall Policy](Applying-the-Firewall-Policy.md)
