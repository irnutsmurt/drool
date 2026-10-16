# Internal vs external

Network Activity sorts the other end of every connection into one of three kinds:

| Kind | Means | Shown as |
|---|---|---|
| ⌂ **Internal** | An address in your **internal ranges** | The DROOL host it belongs to (*database-01*), its Pack, or the named network (*network: home-lan*) |
| ◎ **External** | A public internet address | With its country and network owner ([GeoIP and ASN](GeoIP-and-ASN.md)) |
| ◌ **Special** | Neither: an address that isn't on the public internet and isn't in your internal ranges, such as multicast, reserved, benchmarking or documentation addresses. Special addresses are never looked up in a GeoIP database | With the kind of range, for example *Special (documentation)* |

Your internal ranges always win: with the default list, loopback and link-local addresses count as
internal.

## Internal ranges

**Internal** means *in the firewall's internal ranges*, listed under **Firewall → Settings → Internal
networks**: by default the private IPv4 ranges (10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16), CGNAT and
Tailscale (100.64.0.0/10), link-local, loopback and IPv6 unique local addresses
([Networks and CIDRs](Networks-and-CIDRs.md#internet-internal-networks-anywhere)). If you use other
ranges internally (routed LANs, VLANs, a VPN), add them there, or their traffic counts as external.

The same list decides what the firewall's **Internet** target means: everything outside these ranges.

## New external destinations

**New external destinations** are external addresses that a host, or any host, contacted for the
first time recently. Filter **New external destinations** for the last **24 hours** or **7 days**, per
host or across the fleet. "New" is relative to what DROOL remembers (30 days by default): an address
not seen for longer than that counts as new again.

## A note on this wiki's examples

The example network's internet addresses (203.0.113.x and 198.51.100.x) come from ranges reserved
for documentation, so they can never point at a real machine. DROOL recognises those ranges, so in the
screenshots they appear as **Special (documentation)** instead of external, and the **External** count
is 0. Your hosts' real internet traffic appears as **External**.

## Related

[What DROOL Can See](What-DROOL-Can-See.md) · [GeoIP and ASN](GeoIP-and-ASN.md) · [Networks and CIDRs](Networks-and-CIDRs.md)
