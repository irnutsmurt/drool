# Shepherd incompatible

The **Shepherds** page and each host's Shepherd card show how well that Shepherd fits this DROOL:

| Compatibility | Means | What to do |
|---|---|---|
| **compatible** | All good | Nothing |
| **update available** | A newer Shepherd is available | [Update it](Updating-Shepherds.md) when convenient |
| **reduced capabilities** | It works, but lacks an optional capability (firewall management, network activity, system monitoring, Desired State checks, …) | Update it to get them |
| **update required** | It lacks something this host's configuration needs, for example firewall management on a host in Enforce mode | Update it |
| **incompatible** | It speaks a protocol version this DROOL doesn't | Update it. Meanwhile DROOL doesn't use it, and hosts with [SSH fallback](SSH-Fallback.md) keep working over SSH |
| **identity / trust problem** | Revoked, in an identity conflict, or its certificate expired too long ago to renew | [Shepherd PKI](Shepherd-PKI.md) |

DROOL never sends a Shepherd an operation it can't do: it uses SSH where that's allowed, or refuses and
says to update the Shepherd.

![Shepherds → Shepherds: media-01, connected and compatible, running v0.1.0-alpha-73-ge3c0c58 (protocol 1), update current, its certificate valid until Nov 11, 2026, and root-equivalent capabilities](images/shepherds-list.png)

## Updating

Shepherd updates use binaries **you sign** with your own release-signing key, so a compromised DROOL
can't make Shepherds run unsigned code. The one-time setup and the rollouts are explained in
[Updating Shepherds](Updating-Shepherds.md). Without a signing key that the host trusts, a Shepherd
can't be updated by DROOL: set the key up first.

## After restoring DROOL from a backup

A Shepherd too old to move to the restored controller's epoch waits as **update required** on
**Settings → System → Restore**; once updated, it's compared again when it reconnects
([What Happens After Restore](What-Happens-After-Restore.md)).

## Related

[Updating Shepherds](Updating-Shepherds.md) · [Shepherd Offline](Shepherd-Offline.md) · [Shepherd PKI](Shepherd-PKI.md)
