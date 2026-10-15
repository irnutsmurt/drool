# Connection details

Click any connection on a **Network Activity** page to see everything DROOL knows about it. In the
examples, Alex notices that `media-02` connected to `198.51.100.77` on port 8443, an address nobody
recognises, half an hour ago.

![Observed connection: outbound from media-02 (10.10.20.16) to 198.51.100.77 on TCP 8443; process Unknown; first and last seen; 21 connections; address first seen by media-02 and by any host; observed by SSH snapshot; What it is: Unknown, nothing says what this is yet, with Classify…; Firewall: Not in policy, no firewall policy says anything about this connection, with a suggested rule and "Create policy from observed connection…"](images/network-flow-details.png)

## What each line means

| Line | Meaning |
|---|---|
| **Direction** | Inbound or outbound, and whether the other end is internal, external or special ([Internal vs External](Internal-vs-External.md)) |
| **Source**, **Destination** | Who started the connection, and who received it, with addresses and ports |
| **Remote host**, **Network object**, **Internal range** | If the other end is a DROOL host, belongs to a named network, or to an internal range |
| **Country**, **Network** | For external addresses, from the local GeoIP/ASN databases ([GeoIP and ASN](GeoIP-and-ASN.md)) |
| **Protocol**, **Port** | TCP or UDP, and the service port |
| **Process** | The program, or *Unknown — DROOL couldn't tie this connection to a process*. DROOL never guesses: very short outbound connections are often gone before DROOL can see their owner |
| **First seen**, **Last seen**, **Connections** | When DROOL first and last saw this flow, and how many times |
| **Address first seen** | When this remote address was first seen by this host, and by any host: *Today 01:51 PM by media-02* means it's new |
| **Observed by** | How DROOL saw it: *Continuous: conntrack*, *Continuous: socket sampling*, or *Snapshot only: SSH* |

## What it is

What the connection is and how DROOL knows: **⛨ DROOL management** with the evidence (*DROOL's own SSH
connections to media-02 come from 10.10.0.10 …*), a classification someone made (**✓ Expected** or
**◆ Classified**, with its label), **≡ Known service** (named after its port only), or **? Unknown**.
**Classify…** labels it, for this connection or for similar traffic ([Classifying Traffic](Classifying-Traffic.md)).

## The firewall section

What the firewall policy says about this connection ([Policy Classification](Policy-Classification.md)),
for example **? Not in policy** — *No firewall policy says anything about this connection* — and the
state of the host's firewall (*DROOL does not manage this host's firewall*, *Monitor mode (not
enforced)*, *Enforce mode, policy not applied yet*).

For a connection no policy covers, DROOL suggests a rule (*Suggested: Allow media-02 → 198.51.100.77
tcp-8443*). **Create policy from observed connection…** opens it as a draft in the firewall designer:
*Nothing is saved or applied until you review it, save it, and apply it.* See
[Creating a Policy From Observed Traffic](Creating-a-Policy-From-Observed-Traffic.md). Only create a
rule for a connection you understand and want.

**Other connections with this address** lists the other flows to or from the same address, on any host.

## Investigating something unexpected

1. Is the process known? *Unknown* doesn't mean malicious: short connections often end before DROOL
   can see their owner. On a Shepherd host with conntrack, every new connection is seen.
2. When did it start? **Address first seen** tells you if it's new for this host, or new for the whole
   fleet.
3. Look on the host: [run a read-only command](Running-a-Command.md) such as
   `sudo ss -tnp | grep 198.51.100.77` while the connection is open.
4. Country and network owner are context, never a verdict: they can be wrong or out of date.

## Related

[What DROOL Can See](What-DROOL-Can-See.md) · [Policy Classification](Policy-Classification.md) ·
[Creating a Policy From Observed Traffic](Creating-a-Policy-From-Observed-Traffic.md)
