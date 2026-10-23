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

1. Open **Packs**, then the Pack.
2. Under **What this Pack sets for its hosts** → **Desired State**, click **Change priority…**. (The
   page's **Where another Pack decides** lists the resources the Packs disagree about.)
3. Enter a **Priority** from -1000 to 1000 (default 0; higher wins). The **Impact** shows which hosts'
   desired state would change.
4. Click **Save**.

If the two Packs have the **same** priority and disagree, that's a **conflict**: DROOL doesn't pick
one ([Conflicts](Conflicts.md)).

## See where a host's desired state comes from

Open the host's **Desired State** section. **Where this host's desired state comes from** lists the
baselines that apply (*Server Defaults rev. 1, via everywhere*), the host's Packs with their
priorities, and the inheritance order. Open any resource to see **Where the desired value comes
from**, for example *Required by Global baseline Server Defaults* or *Inherited from pack:
media-servers (baseline Media Server)*.

![web-01's Desired State section: drift detected, 1 of 3 resources compliant; Server Defaults applies via everywhere, with the Packs' priorities and the inheritance chain; the managed resources, each opening for its details and actions: /etc/motd is missing and telnet is installed but should be absent (drift), openssh-server is compliant](images/host-desired.png)

## Next

**[Drift](Drift.md)**: check which hosts don't match their baselines, and why.

## Related

[Creating a Baseline](Creating-a-Baseline.md) · [Conflicts](Conflicts.md) · [Packs](Packs.md) ·
[Drift](Drift.md)
