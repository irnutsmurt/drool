# What "Unknown" means

DROOL uses **Unknown** (often with a **?**) whenever it can't tell. It never fills that gap with a
guess, and in particular it never shows something as healthy, compliant or allowed just because it
couldn't check. An Unknown is a question, not an answer.

## Where you'll see it, and what it means

| Where | What it means | Usually |
|---|---|---|
| A **Temperature** tile: *No sensors* | The host exposes no temperature sensors | Normal for VMs and containers. Nothing to do |
| **Storage devices**: *Health unknown* | At least one disk can't report its health | Virtual disks; missing `smartmontools`/`nvme-cli`; SSH hosts without passwordless sudo. See [Storage Health](Storage-Health.md) |
| A storage device: *Limited — No failure indicators detected* | The device (SD card, USB stick) reports nothing about itself | Normal. DROOL watches kernel errors instead; consider the write/read probe |
| A service state: *Unknown* | DROOL hasn't seen that unit's state | The host hasn't reported yet, or the data is too old |
| A host's **Health**: *No data yet* | Monitoring hasn't received anything from the host | A host just added (wait one poll, up to 5 minutes), or one DROOL can't reach |
| Desired State: **Unknown** | DROOL couldn't confirm the resource's state | The host is unreachable, the data is too old, or monitoring is off. See [Host Shows Unknown](Host-Shows-Unknown.md) |
| A run's host: **outcome unknown** / **interrupted** | DROOL lost contact mid-operation; it may or may not have happened | Check the host before repeating. See [Failed, Interrupted and Unknown Outcomes](Failed-Interrupted-and-Unknown-Outcomes.md) |
| Network Activity: process **Unknown** | The connection was gone before DROOL could see which program owned it | Common for very short outbound connections |
| Network Activity: country or network *unknown* | The GeoIP/ASN database has no answer for that address. Without a database (still being prepared, none, or lookups off), connections show **—** instead, and the page says why | See [GeoIP and ASN](GeoIP-and-ASN.md#what-network-activity-shows) |

## Unknown versus stale

Data that is **too old** is treated as unknown rather than trusted. On the fleet page, the
**Unreachable / stale** filter lists hosts whose data is out of date; the **Updated** column shows its
age. See [Monitoring Is Stale](Monitoring-Is-Stale.md).

## Related

[Host Shows Unknown](Host-Shows-Unknown.md) · [Fleet Monitoring](Fleet-Monitoring.md)
