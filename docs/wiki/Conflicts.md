# Conflicts

A **conflict** happens when a host is in two Packs whose baselines say **different** things about the
**same** resource, and both Packs have the **same priority**. For example, `media-01` is in
`media-servers` and in a `backups` Pack; *Media Server* says `rsync` should be installed and *Backup
Clients* says it must not be. Both Packs have priority 0.

**DROOL never picks one.** A guess could undo someone's deliberate decision, so instead:

- the resource is shown as **Conflict**, with both values and where each comes from (*Equal priorities
  that disagree: DROOL doesn't pick one.*);
- it is **not checked** against either value;
- it **can't be restored**, by hand or automatically, until the conflict is resolved.

The Desired State **Overview** counts *conflicts to resolve*, and the **Conflicts** filter lists the
hosts.

## Resolve a conflict

Choose whichever matches what you meant:

| Option | Effect | How |
|---|---|---|
| **Give one Pack a higher priority** | That Pack's value wins on every host in both Packs | **Packs** → **Change priority** on one of them ([Applying a Baseline](Applying-a-Baseline.md#when-two-packs-disagree-pack-priority)) |
| **Change a baseline** | Make the two baselines agree, or remove the resource from one | Open the baseline → **Edit** ([Creating a Baseline](Creating-a-Baseline.md)) |
| **Accept the current state for this host** | Whatever the host has now becomes its own desired state; both Packs are overruled on this host only | The resource → **Accept current state** ([Accept Current State](Accept-Current-State.md)) |

The impact preview of a priority or baseline change shows which hosts it resolves. Once resolved,
the history records *Conflict resolved* and DROOL checks the resource again.

## Good to know

- Conflicts can only happen between **Packs** with equal priority. A host's own value, a baseline
  assigned to the host, and baselines for every host are never in conflict: the most specific one wins.
- Health checks have their own Pack priorities ([Health Overrides](Health-Overrides.md)), separate
  from the desired-state priority.

## Related

[Applying a Baseline](Applying-a-Baseline.md) · [Drift](Drift.md) · [Packs](Packs.md)
