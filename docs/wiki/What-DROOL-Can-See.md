# What DROOL can see (Network Activity)

**Network Activity** shows what your hosts actually talk to: every connection DROOL has observed,
which program made it, and what your firewall policy says about it. It answers questions like *what is
connecting to my database?*, *why is media-02 talking to an address I don't know?*, or *what would
break if I enforced the firewall?*

It's **monitoring only**: nothing here blocks traffic, scores addresses or raises alarms.

## Before you start

- You need **network.read**: administrators, operators and auditors. Viewers can't see Network
  Activity, because connection details are sensitive.
- What DROOL can see depends on how the host is managed: continuously through a Shepherd, or only in
  snapshots over SSH ([Agent vs SSH Visibility](Agent-vs-SSH-Visibility.md)).

## The Network Activity page

Open **Network Activity** in the menu.

![Network Activity: 58 connections (27 inbound, 31 outbound), 2 external, 29 not in policy, 2 denied or would be denied; how hosts are observed (continuous conntrack, socket sampling, SSH snapshots); Search, Host and Direction with the filters External, Unknown traffic, Not in policy and New external destinations (24 h), and More filters; and the connections with their direction, host, process and local address, remote address and service, country and network owner, when last seen, the number of connections, and what each is and its policy status](images/network-overview.png)

At the top:

- **Connections (flows)**: how many distinct connections DROOL knows, inbound and outbound.
- **External**: connections to or from the internet, and how many **new destinations** appeared in
  the last 24 hours (**Show new destinations**).
- **Not in policy**: connections no firewall rule describes (**Show them**).
- **Denied or would be denied**: what the policy drops, or would drop on hosts where it isn't in force
  (**Show**). Only on a host that enforces DROOL's policy does *denied* mean the firewall blocks it
  ([Policy Classification](Policy-Classification.md)).
- **How hosts are observed**: how many hosts are watched continuously (*conntrack* or *socket
  sampling*), how many by SSH snapshot, and how many SSH hosts have no snapshot yet.
- While DROOL prepares its GeoIP/ASN databases (the first download), one quiet line above all this
  says so; if they can't be had, it says why ([GeoIP and ASN](GeoIP-and-ASN.md#what-network-activity-shows)).

Below, the filters. **Search**, **Host**, **Direction** and four quick filters (**External**,
**Unknown traffic**, **Not in policy**, **New external destinations (24 h)**) are always there.
**More filters** has the rest: the quick filters **Denied or would be denied**, **DROOL's own
traffic** and **Inbound**, and Pack, remote address (internal, external, special), protocol, port,
process, service, country, network (ASN), what it is, policy status, last seen, new external
destinations, and more. While it's closed, the filters in effect there show as chips you can remove,
and the button counts them (*More filters (2 in effect)*).

Each row is one **flow**: a host, a direction, a protocol, a local address, a service port and a
remote address, with how many connections DROOL saw and when it first and last saw them. Click a row
to open the connection in a panel beside the list ([Connection Details](Connection-Details.md)). On
a phone, each connection is a compact entry, 25 to a page.

| Column | What it shows |
|---|---|
| **Direction** | ↘ **Inbound** (someone connected to the host) or ↗ **Outbound** (the host connected out), and whether the other end is **Internal**, **External** or **Special** ([Internal vs External](Internal-vs-External.md)) |
| **Process · local** | The program, such as `nginx` or `postgres`, or *Unknown*, the service name for the port, and the host's own address and port |
| **Remote · service** | The other end, with the protocol and port. A remote address that is a DROOL host, Pack member or named network says so (*database-01*, *network: home-lan*) |
| **Country · network (ASN)** | For external addresses, from the local databases. **—** when nothing was looked up (the database still being prepared, none of that kind, or lookups off: the page says which, once, at the top); *unknown* only when a database had no answer ([GeoIP and ASN](GeoIP-and-ASN.md#what-network-activity-shows)) |
| **What it is · policy** | What the connection is: DROOL's own management traffic, a classification you made, a known service, or unknown ([Classifying Traffic](Classifying-Traffic.md)); and under it what the firewall policy says ([Policy Classification](Policy-Classification.md)) |

## Each host's view

A host's **Network** section shows **How this host is observed**, what it **listens on** (port,
process, who can reach it, and what was seen arriving), and its connections. For an SSH host, **Take
snapshot** records what's open right now.

## What DROOL never sees

Packet contents, DNS queries and byte counts. Connections routed **through** a host (containers on a
bridge, a router) are counted but not shown.

## How long DROOL keeps it

Connections not seen for **30 days** are removed, and each host keeps at most **5,000** flows (the
least recently seen go first). Administrators can change both under **Network Activity → Collection
and enrichment**. A flow takes about 250 bytes.

## Related

[Agent vs SSH Visibility](Agent-vs-SSH-Visibility.md) · [Connection Details](Connection-Details.md) ·
[Policy Classification](Policy-Classification.md) · [Creating a Policy From Observed Traffic](Creating-a-Policy-From-Observed-Traffic.md)
