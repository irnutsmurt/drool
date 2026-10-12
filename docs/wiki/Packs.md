# Packs (host groups)

A **Pack** is a named group of hosts, what other tools call a *host group*. In the examples,
`media-servers` holds `media-01`, `media-02` and `nas-01`, and `web-servers` holds `web-01`.

Packs do two jobs:

1. **Targeting** — run something on "all media servers" instead of picking hosts one by one: runs,
   schedules, maintenance, file deployments, firewall rules.
2. **Shared settings** — everything you configure for a Pack applies to its members: monitoring
   adjustments, Desired State baselines, firewall rollback timeouts, notification routes.

A host can be in several Packs. Membership is looked up when it's used: a host you add to
`media-servers` next month is included in that Pack's weekly update from then on.

## Before you start

- Creating, changing and deleting Packs needs **hosts.write** (administrators).
- The hosts must already be in DROOL ([Adding a Host](Adding-a-Host.md)).

## Create a Pack

1. Open **Packs** in the sidebar.
2. Type a name under **New pack**, for example `media-servers`, and click **Create pack**.

   Names can contain letters, digits, `.`, `_` and `-`, and must start with a letter or digit: no
   spaces. (`media-servers` works, `Media Servers` doesn't.)
3. On the new Pack's card, click **Members** and tick the hosts that belong in it (`media-01`,
   `media-02`, `nas-01`). Changes are saved as you tick.

## What each Pack's card offers

| On the card | What it's for |
|---|---|
| **Customize monitoring** | Health-check adjustments for this Pack's hosts, e.g. no high-CPU warning for a render farm ([Health Overrides](Health-Overrides.md)) |
| **Members** | Add and remove hosts |
| **Delete** | Remove the Pack (see below) |
| **Desired State: … · priority 0 — Change priority** | Which baselines apply to the Pack, and its priority when two Packs disagree ([Conflicts](Conflicts.md)) |
| **Alerts** | Alerts of this Pack's hosts |
| **Run on this pack** | A new run with the Pack as its target ([Running a Command](Running-a-Command.md)) |
| **Network Activity** | Connections of this Pack's hosts |
| **Baselines** | Desired State baselines assigned to the Pack |

## Where Packs are used

- **Runs and schedules** — in the target picker, choose Packs as well as or instead of hosts.
- **Maintenance** — put a whole Pack in maintenance ([Maintenance Mode](Maintenance-Mode.md)).
- **Desired State** — assign a baseline to a Pack ([Applying a Baseline](Applying-a-Baseline.md)).
- **Monitoring** — Pack-level health adjustments, with a priority when Packs disagree.
- **Notifications** — route a Pack's alerts somewhere specific ([Notification Routing](Notification-Routing.md)).
- **Firewall** — a Pack is one box in the topology and one name in rules, e.g. *web-servers may
  reach database-01 on PostgreSQL* ([Packs in Firewall Rules](Packs-in-Firewall-Rules.md)). Changing
  a Pack's members in the firewall designer changes the Pack everywhere.

## Delete a Pack

**Delete** removes the Pack and the settings that belong to it: its memberships (the hosts stay),
its Desired State baseline assignments, its health adjustments and its firewall settings. DROOL
doesn't refuse and doesn't warn you about what still uses the Pack, so check first:

- **Firewall rules** that name the Pack keep pointing at it as a deleted group
  (`group:deleted-grp_…`). The firewall policy then shows the problem *unknown group "deleted-grp_…"*
  and can't be applied until you change or remove those rules in **Firewall → Policy**.
- **Schedules** that target the Pack can't start their runs anymore: DROOL reports that the group
  wasn't found. Check **Schedules** and change their targets.

## Packs and tags

[Tags](Tags.md) are lighter: free-form labels on each host, used for searching and for targeting
runs ("hosts with any of these tags"). Packs carry settings; tags don't. Use a Pack when hosts should
be *managed* together, a tag when you just want to *find* or *pick* them together.

## Related

[Tags](Tags.md) · [Packs in Firewall Rules](Packs-in-Firewall-Rules.md) · [Health Overrides](Health-Overrides.md) ·
[Applying a Baseline](Applying-a-Baseline.md)
