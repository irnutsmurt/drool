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
- A Pack can be created before its hosts exist, and it can stay empty. To add hosts to it, they
  must be in DROOL ([Adding a Host](Adding-a-Host.md)).

## Create a Pack

1. Open **Packs** in the sidebar and click **Create Pack**.
2. Type a name, for example `media-servers`, and if you like a description. Click **Create**.

   Names can contain letters, digits, `.`, `_` and `-`, and must start with a letter or digit: no
   spaces. (`media-servers` works, `Media Servers` doesn't.)

   ![The "Create a Pack" dialog: name media-servers, description "Jellyfin servers and the file server", a note that hosts are added on the Pack's page, now or later, and the Create button](images/pack-create.png)

3. DROOL opens the new Pack's page. It has no hosts yet:

   ![The media-servers pack's page: no hosts in it yet, Add hosts… in the empty Hosts card, Actions in the header, what the Pack sets for its hosts, and the Monitored services card](images/pack-empty.png)

An empty Pack is a Pack like any other: schedules, firewall rules and baselines can already name it,
and its hosts are included from the moment you add them. A run started on it while it's empty is
refused (*the selected targets match no hosts*), and a schedule skips that time.

## Add and remove hosts

On the Pack's page:

1. Click **Add hosts…** (in the **Hosts** card).
2. Tick the hosts that belong in the Pack (`media-01`, `media-02`, `nas-01`) and click **Add 3 hosts**.
   With many hosts, a search box narrows the list by name, address or tag. Hosts already in the Pack
   aren't listed. Under the list, **What they get from media-servers** says what joining means: the
   health checks the Pack sets, its monitored services, its Desired State baselines, the firewall
   rules that name it, and the schedules, maintenance and routes that include its hosts.

   ![The "Add hosts to media-servers" dialog with backup-01, database-01, gpu-01, media-01, media-02, nas-01 and web-01; media-01, media-02 and nas-01 ticked; what they get from media-servers (no health checks of its own, its two baselines, two firewall rules naming it, and the schedules, notification route and classification that include it); and the Add 3 hosts button](images/pack-add-hosts.png)

The Pack's page then lists its hosts:

![The media-servers pack with media-01, media-02 and nas-01: their health, connection, pending updates and tags with a Remove… button on each; where another Pack decides; what the Pack sets for its hosts; and the Monitored services card](images/pack-hosts.png)

- Above the list: how its hosts are (*2 healthy, 1 unhealthy · 6 updates pending*), from monitoring.
  A host that isn't reporting counts as *not reporting*, never as healthy.
- **Remove…** takes a host out of the Pack, after DROOL asks *Remove media-01 from media-servers?*
  (**Remove from the Pack**). The host stays in DROOL, and the Pack stays too, even when its last host
  is gone; the Pack's settings stop applying to that host, and runs or schedules that target the Pack
  no longer include it.
- **From a host's page:** in its **Management** section, on the **Membership** card, **Change…** next
  to **Packs** lists every Pack with a tick for the ones the host is in. Ticking adds the host,
  unticking removes it.
- Each change is saved on its own, so two people adding hosts at the same time both keep theirs.

## The Pack's page

The **Packs** list shows each Pack's hosts and how they are; everything about one Pack is on its page,
in this order:

| Where | What it's for |
|---|---|
| **Run on this Pack**, **Install updates…** (header) | A new run with the Pack as its target ([Running a Command](Running-a-Command.md)); the review of updating its hosts (each one's dry run, the recommended rollout for two or more), then the run, followed in place ([System Updates](System-Updates.md#install-updates-where-you-see-them)) |
| **Actions** (header) | **Start maintenance…** for the Pack's hosts ([Maintenance Mode](Maintenance-Mode.md)), and last, **Delete Pack…** (see below) |
| **Hosts** | Its hosts with their health, connection and pending updates; **Add hosts…**, **Remove…** |
| **Where another Pack decides** | Hosts where this Pack's setting doesn't apply because another of their Packs disagrees (see below) |
| **What this Pack sets for its hosts** | Its **Health checks…** ([Health Overrides](Health-Overrides.md)), its **monitoring priority** and **Desired State priority** (**Change…**, with what changes before saving), the baselines assigned to it, the firewall rules that name it and its rollback timeout, standing monitoring-helper approvals, and the schedules, maintenance, notification routes, enrollment tokens and classifications that target it, each linking to where it's changed |
| **Monitored services** | Services monitored on its hosts, and the state each should be in: running, stopped or ignored, e.g. `plexmediaserver.service` stopped on every media server ([Service Monitoring](Service-Monitoring.md#customize-monitoring-the-expected-state)) |
| **Alerts for this Pack**, **Network Activity** | Its hosts' alerts and connections |

## When Packs disagree

A host in two Packs can get two different values for the same thing. The **monitoring priority** (for
health checks) and the **Desired State priority** (for baselines) decide: the higher wins. With equal
priorities DROOL picks neither ([Conflicts](Conflicts.md)).

**Where another Pack decides** on the Pack's page lists every such place for its hosts, in words, for
example *media-01 · Disk space, warning at: This Pack (90%) and backup-sources (85%) disagree at equal
priority (0): 80% from every host's setting applies.* When a higher-priority Pack wins, it says *…
outranks it*. For a health check, **Let media-servers win…** raises this Pack's monitoring priority
just above the other's, after showing what changes on every host in both. A Desired State conflict
links to the host's Desired State. When there are none: *None: this Pack's settings apply on every host
in it, unless the host sets its own.*

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

**Actions → Delete Pack…** (on the Pack's page) first asks every part of DROOL what still uses the Pack,
and shows the answer.

**While anything depends on it, DROOL won't delete it.** The dialog lists each thing, with what to
do and a link to it:

| Uses the Pack | What to do |
|---|---|
| A **firewall policy** that names it | Take the Pack out of the policy (or delete the policy) in **Firewall → Policy** |
| A **schedule** that targets it | Change the schedule's targets, or archive it |
| A **notification route** whose filter names it | Take the Pack out of the route's filter, or delete the route |
| A **maintenance** window that covers it | End the maintenance |
| A **Desired State baseline** assigned to it | Unassign the baseline from the Pack |
| A **monitoring setting** customized for it (monitored services included) | Reset it to inherited on the Pack's page (**Health checks…**, **Monitored services**) |
| A **Shepherd enrollment token** that adds new hosts to it | Revoke the token, or let it expire |
| A **network classification** that matches it | Change it to other hosts, or delete it ([Classifying Traffic](Classifying-Traffic.md)) |

Once nothing depends on it, the dialog says exactly what deleting it removes, and nothing more:

- its memberships: the hosts leave the Pack and **stay in DROOL**, with everything else they have.
  Removing a host from a Pack never deletes the host;
- the Pack's own monitoring and Desired State priorities, its firewall rollback timeout, and a
  standing approval to install monitoring helpers on its hosts, which only ever applied to the Pack;
- the Pack's name on used, revoked or expired enrollment tokens (their history stays).

Click **Delete Pack** to confirm. If something started depending on the Pack in the meantime, DROOL
still refuses and shows the new list. The deletion is audited with what it removed.

## Packs and tags

[Tags](Tags.md) are lighter: free-form labels on each host, used for searching and for targeting
runs ("hosts with any of these tags"). Packs carry settings; tags don't. Use a Pack when hosts should
be *managed* together, a tag when you just want to *find* or *pick* them together.

## Related

[Tags](Tags.md) · [Packs in Firewall Rules](Packs-in-Firewall-Rules.md) · [Health Overrides](Health-Overrides.md) ·
[Applying a Baseline](Applying-a-Baseline.md)
