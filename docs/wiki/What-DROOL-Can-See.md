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

![Network Activity: 51 connections (25 inbound, 26 outbound), 0 external, 25 not in policy, 1 denied or would be denied; how hosts are observed; quick filters and the filter fields; and the connections with their direction, host, process, local and remote address, protocol, network, first and last seen, number of connections, what each is and its policy status](images/network-overview.png)

At the top:

- **Connections (flows)**: how many distinct connections DROOL knows, inbound and outbound.
- **External**: connections to or from the internet, and how many **new destinations** appeared in
  the last 24 hours (**Show new destinations**).
- **Not in policy**: connections no firewall rule describes (**Show them**).
- **Denied or would be denied**: blocked by a deny rule, or by the default once the firewall is
  enforced (**Show**).
- **How hosts are observed**: how many hosts are watched continuously (*conntrack* or *socket
  sampling*), how many by SSH snapshot, and how many SSH hosts have no snapshot yet.

Below, quick filters (**External connections**, **Unknown traffic**, **Not in policy**, **New
external destinations (24 h)**, **Denied or would be denied**, **DROOL's own traffic**, **Inbound**)
and detailed ones: search, host, Pack, direction, remote address (internal, external, special),
protocol, port, process, service, country, network (ASN), what it is, policy status, last seen, new
external destinations, and more.

Each row is one **flow**: a host, a direction, a protocol, a local address, a service port and a
remote address, with how many connections DROOL saw and when it first and last saw them. Click a row
for its details ([Connection Details](Connection-Details.md)).

| Column | What it shows |
|---|---|
| **Direction** | ↘ **Inbound** (someone connected to the host) or ↗ **Outbound** (the host connected out), and whether the other end is **Internal**, **External** or **Special** ([Internal vs External](Internal-vs-External.md)) |
| **Process / service** | The program, such as `nginx` or `postgres`, or *Unknown*, and the service name for the port |
| **Local**, **Remote** | Both ends. A remote address that is a DROOL host, Pack member or named network says so (*database-01*, *network: home-lan*) |
| **Country · Network (ASN)** | For external addresses, when a database is installed ([GeoIP and ASN](GeoIP-and-ASN.md)) |
| **What it is · policy** | What the connection is: DROOL's own management traffic, a classification you made, a known service, or unknown ([Classifying Traffic](Classifying-Traffic.md)); and under it what the firewall policy says ([Policy Classification](Policy-Classification.md)) |

## Each host's view

A host's page → **View network activity** shows **How this host is observed**, what it **listens
on** (port, process, who can reach it, and what was seen arriving), and its connections. For an SSH
host, **Take snapshot** records what's open right now.

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
