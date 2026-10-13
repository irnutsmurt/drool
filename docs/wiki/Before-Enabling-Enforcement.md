# Before enabling enforcement

A firewall that blocks the wrong thing can take a service offline, or lock everyone out of a machine.
DROOL protects its **own** access ([Management Path Protection](Management-Path-Protection.md)) and
rolls back changes it can't confirm ([Automatic Rollback Protection](Automatic-Rollback-Protection.md)),
but it can't know which of **your** connections matter. Go through this list before you switch a host
to **Enforce**.

## The checklist

1. **Make sure you can reach the host another way.** A console (a hypervisor's console, a monitor and
   keyboard, an IPMI or cloud console) lets you [recover by hand](Recovering-From-a-Firewall-Problem.md#locked-out-anyway)
   if you ever need to.
2. **Check the host's firewall environment.** Open the host's page → **View firewall** → **Check**
   under **Firewall environment**. You want *Enforcement is supported here: plain nftables, no other
   firewall manager.* If it says *refused*, read why:
   - *nftables (nft) is not installed*: install the `nftables` package
     ([Installing Packages](Installing-Packages.md)), then **Check** again.
   - Docker, firewalld, UFW, Tailscale, or other iptables/nftables rules: DROOL won't share the
     firewall with another manager. Enforce on other hosts, or remove the other manager first.
     After `ufw disable`, UFW's rules stay loaded until the next reboot.
3. **Read the warnings under the check.** For example, on Ubuntu: */etc/nftables.conf starts with
   "flush ruleset": reloading nftables.service removes DROOL's rules until the next boot or DROOL
   apply*. Avoid `systemctl reload nftables` on enforced hosts, or remove that line.
4. **Watch first.** Put the host in **Monitor** (or **Guided**) mode for a while
   ([Firewall Modes](Firewall-Modes.md)) and look at its **Observed connections**: what reaches it,
   and from where. Every connection you need must be allowed by a rule.
5. **Write the rules, then check them.** The host's **Effective policy** tab lists everything that
   would be allowed and denied, and why. Ask [Can A reach B?](Can-A-Reach-B.md) for the connections
   that matter (*can web-01 reach database-01 on TCP 5432?*).
6. **Think about what isn't in the list.** Inbound traffic that no rule allows will be **denied**:
   backups pulled from another machine, monitoring agents, file shares, the web interface of something
   you rarely use.
7. **Choose a quiet moment.** Enforce one host first, check its services, then the rest.

## What DROOL already allows

You don't need rules for:

- DROOL's own management path (SSH from the controller or jump host; a Shepherd's connection to the
  controller);
- replies to connections that were allowed;
- loopback traffic, ICMP errors (path MTU, unreachable), IPv6 neighbour discovery, and DHCP replies.

Tick **Show base rules (replies, loopback, ICMP, DHCP)** on the **Effective policy** tab to see them.
Note that **ping** is not among them: allow the `ping` service if you want hosts to answer pings.

## Next

**[Firewall Modes](Firewall-Modes.md)**: put the host in **Monitor** first, and watch what reaches it before you write rules.

## Related

[Firewall Modes](Firewall-Modes.md) · [Applying the Firewall Policy](Applying-the-Firewall-Policy.md) ·
[What DROOL's Firewall Does](What-DROOLs-Firewall-Does.md)
