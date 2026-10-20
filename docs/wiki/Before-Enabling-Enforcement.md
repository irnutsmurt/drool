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
2. **Check the host's firewall environment.** Open the host's **Firewall** section → **Check**
   under **Firewall environment**. **Check** only looks at the host; it never installs or changes
   anything. You want *Enforcement is supported here: plain nftables, no other firewall manager.*
   Otherwise, read why:
   - *nftables (nft) is not installed*: choose **Install nftables…** beside the result. DROOL asks the
     host's package manager what that involves (a dry run) and shows you, then installs it as a normal
     package run once you confirm, shows its progress, and checks again by itself. It doesn't change the
     mode or enforce anything. In **Monitor** or **Guided** mode this is only information: monitoring,
     simulation and explanation work without nftables. Where DROOL can't install it (you need permission
     to run jobs and to manage the firewall; the host needs apt or dnf; DROOL won't remove other packages
     for it), it says why: install it yourself ([Installing Packages](Installing-Packages.md)), then
     **Check** again.
   - Docker, firewalld, UFW, Tailscale, or other iptables/nftables rules: DROOL won't share the
     firewall with another manager. Enforce on other hosts, or remove the other manager first.
     After `ufw disable`, UFW's rules stay loaded until the next reboot.
3. **Read the warnings under the check.** For example, on Ubuntu: */etc/nftables.conf starts with
   "flush ruleset": reloading nftables.service removes DROOL's rules until the next boot or DROOL
   apply*. Avoid `systemctl reload nftables` on enforced hosts, or remove that line.
4. **Watch first.** Put the host in **Monitor** (or **Guided**) mode for a while
   ([Firewall Modes](Firewall-Modes.md)) and look at its **Observed connections**, on its firewall
   page: what reaches it, and from where. Every connection you need must be allowed by a rule:
   **Create policy from observed connection…** in a connection's details starts one.
5. **Write the rules, then check them.** The host's **Policy preview** lists everything that would be
   allowed and denied, and why. Ask [Can A reach B?](Can-A-Reach-B.md) for the connections
   that matter (*can web-01 reach database-01 on TCP 5432?*).
6. **Think about what isn't in the list.** Inbound traffic that no rule allows will be **denied**:
   backups pulled from another machine, monitoring agents, file shares, the web interface of something
   you rarely use, and **your own SSH**. Automatic rollback won't bring those back: it protects DROOL's
   connection only, and on a Shepherd host that isn't SSH
   ([What it doesn't protect](Automatic-Rollback-Protection.md#what-it-doesnt-protect)).
7. **Choose a quiet moment.** Enforce one host first, check its services, then the rest.

## What DROOL already allows

You don't need rules for:

- DROOL's own management path (SSH from the controller or jump host; a Shepherd's connection to the
  controller);
- replies to connections that were allowed;
- loopback traffic, ICMP errors (path MTU, unreachable), IPv6 neighbour discovery, and DHCP replies.

Tick **Show base rules (replies, loopback, ICMP, DHCP)** under the host's **Policy preview** (or
**Applied policy**) to see them.
Note that **ping** is not among them: allow the `ping` service if you want hosts to answer pings.

## Next

**[Firewall Modes](Firewall-Modes.md)**: put the host in **Monitor** first, and watch what reaches it before you write rules.

## Related

[Firewall Modes](Firewall-Modes.md) · [Applying the Firewall Policy](Applying-the-Firewall-Policy.md) ·
[What DROOL's Firewall Does](What-DROOLs-Firewall-Does.md)
