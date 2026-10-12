# Rules view

**Firewall → Policy → Rules** shows the [firewall policy](Firewall-Policy.md) as tables and forms. It
does everything the topology does, and works with the keyboard and on a phone.

![The Rules view: six rules (admin-ssh-to-media, admin-ssh-to-web, admin-ssh-to-database, lan-to-jellyfin, internet-to-web, web-to-database) with their action, source, destination and service; below, the Networks admin-laptop and home-lan, the Packs media-servers and web-servers, and the custom service jellyfin (TCP 8096)](images/firewall-rules.png)

## Rules

*Each rule lets a source reach a destination on services (allow), or stops it (deny). Denies always
win over allows; DROOL's own management path is always kept open ahead of every rule.*

- **New rule** opens the rule dialog: **Name**, **Action** (*Allow*, or *Deny (wins over allows)*),
  **Source**, **Destination**, **Service**, and an optional **Description**.
- **Edit** and **Delete** on each row; **↑** and **↓** reorder rules (the order is for you to read;
  it doesn't change what's allowed).

## Networks, Packs and Services

- **Networks**: *Named address ranges (IPv4 and IPv6) to use as sources or destinations.* **New
  network**, **Edit**, **Delete**. See [Networks and CIDRs](Networks-and-CIDRs.md).
- **Packs (host groups)**: DROOL's Packs, shared with runs and everything else. **Members** changes
  who is in a Pack (everywhere in DROOL); **New pack** creates one. *Existing packs are deleted on the
  Packs page, not here.* See [Packs in Firewall Rules](Packs-in-Firewall-Rules.md).
- **Services**: *Named protocol/port combinations.* **New service**, **Edit**, **Delete**, and **Show
  built-in services**. See [Services and Ports](Services-and-Ports.md).

As in every view, changes are a draft until **Save policy**, and the **Preview** shows what they'd do.

## Related

[Topology View](Topology-View.md) · [YAML View](YAML-View.md) · [Firewall Policy](Firewall-Policy.md)
