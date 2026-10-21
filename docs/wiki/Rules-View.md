# Rules view

**Firewall → Policy → Rules** shows the [firewall policy](Firewall-Policy.md) as tables and forms. It
does everything the topology does, and works with the keyboard and on a phone.

![The Rules view: six rules (admin-ssh-to-media, admin-ssh-to-web, admin-ssh-to-database, lan-to-jellyfin, internet-to-web, web-to-database) with their action, source, destination and service; below, the Networks admin-laptop and home-lan, the Packs media-servers and web-servers, and the custom service jellyfin (TCP 8096)](images/firewall-rules.png)

## Rules

*Each rule lets a source reach a destination on services (allow), or stops it (deny). Denies always
win over allows; DROOL's own management path is always kept open ahead of every rule.*

- **New rule** opens the rule builder. Choose **Allow** or **Deny** (*Blocks the connection, even
  where another rule allows it*), then the **Source**, **Destination** and **Service**: each is one
  search. Type a few letters of a host, a Pack, a network, *Internal networks*, *Anywhere* or
  *Internet* and press Enter (or click the choice), or type an address (*192.168.1.0/24*) or a port
  (*8812*, *udp 53*, *8000-8010*). Add more than one if you need to; **×** removes one.
  - For the destination, hosts and Packs come first: DROOL puts a rule on the hosts its destination
    names. Addresses are listed as *outbound only*.
  - The builder says the rule in words (*Allows ssh (TCP 22) from Internal networks to web-01.*) and
    whose firewall it changes. When no host would get it, it says so, and its buttons read **Add to
    draft anyway** and **Save anyway**.
  - The name follows from your choices (*allow-ssh-internal-to-web-01*). Change it, add a
    description, or switch the rule off under **Name, description and status**.
  - **Save and review…** saves the policy and opens the [review](Applying-the-Firewall-Policy.md#review-and-apply)
    of the hosts it affects. **Add to draft** keeps the rule with your other unsaved changes.
- A host's **Firewall** section has **New rule…** too: the builder opens with that host as the
  destination.
- **Edit** and **Delete** on each row; **↑** and **↓** reorder rules (the order is for you to read;
  it doesn't change what's allowed).
- A rule marked **⚠ applies to no host** is on no host's firewall: its destination is addresses
  (*Internal networks*, a network, a range), not a host. The Preview says why, and what was probably
  meant ([The destination says which hosts get the rule](Firewall-Policy.md#the-destination-says-which-hosts-get-the-rule)).

## Networks, Packs and Services

- **Networks**: *Named address ranges (IPv4 and IPv6) to use as sources or destinations.* **New
  network**, **Edit**, **Delete**. See [Networks and CIDRs](Networks-and-CIDRs.md).
- **Packs (host groups)**: DROOL's Packs, shared with runs and everything else. **Members** changes
  who is in a Pack (everywhere in DROOL); **New pack** creates one. *Existing packs are deleted on the
  Packs page, not here.* See [Packs in Firewall Rules](Packs-in-Firewall-Rules.md).
- **Services**: *Named protocol/port combinations.* **New service**, **Edit**, **Delete**, and **Show
  built-in services**. See [Services and Ports](Services-and-Ports.md).

As in every view, changes are a draft until **Save policy** (or **Save and review application…**),
and the **Preview** shows what they'd do.

## Related

[Topology View](Topology-View.md) · [YAML View](YAML-View.md) · [Firewall Policy](Firewall-Policy.md)
