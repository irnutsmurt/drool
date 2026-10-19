# Topology view

**Firewall → Policy → Topology** draws the [firewall policy](Firewall-Policy.md) as a map: boxes for
hosts, Packs, networks and the internet, and lines for what may talk to what.

![The Topology view across the whole width (nothing is selected, so the side panel steps aside; Details and preview brings it back): boxes for home-lan, admin-laptop, the Internet, the DROOL Controller, the media-servers and web-servers Packs and database-01; blue lines for allowed services such as jellyfin (TCP 8096), ssh (TCP 22), http/https and postgres (TCP 5432); dashed green lines for DROOL's management paths; the toolbar (Configured, Observed, Configured + Observed, New rule…, Place on canvas…, a filter, Focus on…, Fit, Focus selection, Display options) and the legend](images/firewall-topology.png)

## Reading the map

- **Lines are allowed (or denied) communication, not cabling.** A **blue** line is allowed, a **red
  dashed** line denied, each labelled with the service and what it means on the wire, such as
  `ssh (TCP 22)` or `jellyfin (TCP 8096)`.
- **Dashed green lines** are DROOL's own **management paths** (*DROOL management (read-only)*): SSH
  from the **DROOL Controller** to each host, and Shepherds' connections to the controller (*mTLS
  TCP 9443*). You can't edit them; untick **DROOL management paths** (under **Display options**) to
  hide them.
- A **Pack** is one box and one line however many hosts it has. **Show members** expands it.
- Moving boxes only changes the picture (DROOL remembers your layout). **Auto layout** (under
  **Display options**) tidies it. **Fit** shows everything (with observed connections drawn too:
  **Fit policy** frames your rules' boxes, **Fit all** everything); **Focus on…** and **Focus
  selection** zoom in on one box or line; **+**, **−** and the corner button zoom.
- **Filter (name, CIDR)** finds a box; **All hosts** (under **Display options**) shows every host, not
  only those in rules.
- **Configured**, **Observed** and **Configured + Observed** switch between your rules, the
  connections DROOL has actually seen ([Network Activity](What-DROOL-Can-See.md)), or both.

## Adding a rule

- **Drag** from the right edge of one box to another box. A rule dialog opens with the source and
  destination filled in.
- Or click **New rule…**, or use **Place on canvas…** to add a host, Pack or network box first.

In the rule dialog, choose the **Action**, adjust the **Source** and **Destination** (a **Host**,
**Pack**, **Network**, **Address range (CIDR)**, **Internet**, **Internal networks** or **Anywhere**),
add **Services** (a built-in one, or **Custom (protocol and port)…**), and click **Add rule** (or
**Update rule**). *Changes stay in the draft until you save. Nothing reaches a host until you apply.*

## Details

Click a **line** or its label to see the rules behind it, the hosts it affects, and **Can it reach…?**
for that pair. Click a **box** to see what a host allows inbound and outbound, its denies and DROOL's
own access. The **Details** tab on the right shows what you selected; the **Preview** tab shows what
your draft would change.

While nothing is selected and nothing is unsaved, that side panel steps aside and the map gets the
whole width. Selecting a line or a box, or changing the draft, brings it back; **Details and
preview** opens it by hand and **Hide side panel** puts it away.

## Good to know

- Topology is a desktop view: on a phone, the policy opens in the [Rules view](Rules-View.md).
- **Known limitation:** the labels on the lines are small, and where lines run close together two
  labels can sit almost on top of each other, which makes them hard to hit with a finger or an
  unsteady pointer. A label only shows its line's details, so picking the wrong one changes nothing.
  The [Rules view](Rules-View.md) (**Rules**, above the canvas) lists every rule with full-size
  controls, and each label is also reachable with **Tab**.
- Very large expanded Packs (hundreds of hosts) are slow to draw: keep them collapsed or filter.
- Your layout is saved per browser, not per account.

## Related

[Rules View](Rules-View.md) · [YAML View](YAML-View.md) · [Firewall Policy](Firewall-Policy.md)
