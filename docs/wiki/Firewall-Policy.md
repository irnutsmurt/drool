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

**Saving changes no host.** *Saved. Nothing changes on hosts until you apply it (see Preview).* To
put the policy on enforced hosts, [apply it](Applying-the-Firewall-Policy.md).

## Before you start

- Everyone can look at the policy and use **Can it reach…?**. Changing it needs **firewall.write**
  (administrators): *You can explore and simulate; only administrators can change the firewall
  policy.*
- One person editing at a time works best. If someone else saved the policy since you opened it,
  saving is refused (*Someone else saved the firewall policy since you opened it…*): copy your changes
  from the YAML view, discard, and reapply them.

## Next

**Check it, then apply it.** Look at the **Preview** panel, and ask [Can A Reach B?](Can-A-Reach-B.md) about the connections that matter (*can web-01 reach database-01 on TCP 5432?*). Then [apply the policy](Applying-the-Firewall-Policy.md), one host first.

## Related

[Topology View](Topology-View.md) · [Rules View](Rules-View.md) · [YAML View](YAML-View.md) ·
[Can A Reach B?](Can-A-Reach-B.md) · [Applying the Firewall Policy](Applying-the-Firewall-Policy.md)
