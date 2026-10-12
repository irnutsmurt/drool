# Applying a baseline

A baseline applies to the hosts you choose under **Applies to** when you
[create or edit it](Creating-a-Baseline.md):

- **Every host**: every host in DROOL, including hosts you add later. *Server Defaults* in the examples.
- **Packs and hosts I choose**: tick Packs and/or single hosts. A Pack's members are looked up live: a
  host that joins `media-servers` gets *Media Server*'s resources from then on, and a host that leaves
  stops getting them.

Applying a baseline changes nothing on the hosts by itself: DROOL starts **checking** them, and shows
each one as compliant or drifted. To make hosts match, [restore managed state](Restore-Managed-State.md).

## When several baselines say something about the same thing

A host often gets resources from several places: a baseline for every host, one for each of its
Packs, maybe one just for itself. When they disagree about the same resource (the same service,
package or file), **the most specific one wins**:

**this host's own value → baselines assigned to the host → baselines of its Packs → baselines for every host**

For example, *Server Defaults* (every host) says `/etc/motd` should say *Authorized use only*.
Suppose Alex also added `/etc/motd` with a friendlier text to *Media Server* (Pack `media-servers`):
on `media-01`, the Pack's version would win; on `web-01`, which isn't a media server, *Server
Defaults* would still apply.

A host's own value comes from [Accept Current State](Accept-Current-State.md) (*Host override*); it
always wins until you **Reset to inherited**.

## When two Packs disagree: Pack priority

If a host is in two Packs whose baselines say different things about the same resource, the Pack with
the higher **desired-state priority** wins. To set it:

1. Open **Packs**.
2. On the Pack's row, next to *Desired State: … · priority 0*, click **Change priority**.
3. Enter a **Priority** from -1000 to 1000 (default 0; higher wins). The **Impact** shows which hosts'
   desired state would change.
4. Click **Save**.

If the two Packs have the **same** priority and disagree, that's a **conflict**: DROOL doesn't pick
one ([Conflicts](Conflicts.md)).

## See where a host's desired state comes from

Open the host's **Desired State** page (from the host's page, **Desired State** card → **View desired
state**). **Where this host's desired state comes from** lists the baselines that apply (*Server
Defaults rev. 1, via everywhere*), the host's Packs with their priorities, and the inheritance order.
Open any resource to see **Where the desired value comes from**, for example *Required by Global
baseline Server Defaults* or *Inherited from pack: media-servers (baseline Media Server)*.

![web-01's desired state: drift detected, 1 of 3 resources compliant; Server Defaults applies via everywhere; /etc/motd is missing (drift) with Review difference, Restore managed state, Accept current state and Change baseline; telnet is installed but should be absent; openssh-server is compliant](images/host-desired.png)

## Related

[Creating a Baseline](Creating-a-Baseline.md) · [Conflicts](Conflicts.md) · [Packs](Packs.md) ·
[Drift](Drift.md)
