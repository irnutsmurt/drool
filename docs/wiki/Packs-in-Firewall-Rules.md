# Packs in firewall rules

A rule can name a whole [Pack](Packs.md) as its source or destination: *web-servers may reach
database-01 on postgres*. A Pack in a rule always means **its current members**: add `web-02` to
`web-servers` next month, and it can reach `database-01` without anyone touching the rule.

## The same Packs as everywhere else

Packs in the firewall are DROOL's ordinary Packs (written `group:` in [YAML](YAML-View.md)):

- **Changing a Pack's members in the firewall designer** (**Rules view → Packs → Members**, or the
  `members` list in YAML) **changes the Pack everywhere** in DROOL: runs, schedules, monitoring
  settings and Desired State included.
- A Pack can be created in the designer (**New Pack**), but **deleted only on its page** (**Actions →
  Delete Pack…**). Removing a group from the YAML doesn't delete the Pack.
- **A Pack that a rule names can't be deleted**: DROOL lists the rule (and anything else that uses
  the Pack) with a link to change it first ([Packs](Packs.md#delete-a-pack)). The Pack's page lists
  the rules that name it under **What this Pack sets for its hosts**.

## When addresses change

DROOL turns Packs and hosts into nftables **sets** of addresses on each enforced host. When a host's
address changes, DROOL updates just those set elements on every host that refers to it, in one atomic
update per host, without a new transaction. Until every host is up to date, the Firewall page shows
**Policy propagation incomplete**, and a host's firewall page shows *In force: revision 2, addresses
updating* (*newer addresses are being propagated*). There is nothing to apply.

## A tip for big Packs

On the topology, a Pack is one box and one line, however many hosts it has. **Show members** expands it;
very large expanded Packs (hundreds of hosts) are slow to draw.

## Related

[Packs](Packs.md) · [Firewall Policy](Firewall-Policy.md) · [Networks and CIDRs](Networks-and-CIDRs.md)
