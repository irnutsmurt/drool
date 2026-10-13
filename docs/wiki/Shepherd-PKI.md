# Shepherd certificates and trust (PKI)

Shepherds and the controller recognise each other with **certificates**, issued by DROOL's own
certificate authority. You rarely need to think about it: certificates are issued at enrollment and
renewed automatically. This page explains what's there, and what to do when something goes wrong.

## The pieces

| Piece | What it does |
|---|---|
| **Agent Root CA** | The top of the chain. Its key signs the intermediate CA and the *trust manifest*. Valid for 20 years |
| **Intermediate CA** | Signs each Shepherd's certificate. Stored encrypted with the [master key](Master-Key.md) |
| **Trust manifest** | Tells Shepherds which intermediates to trust (and which are revoked). Shepherds only accept a **newer** manifest signed by the root, never an older one |
| **Shepherd certificate** | Each Shepherd's identity. Valid **30 days**, renewed automatically |

All of it is shown under **Shepherds → Trust (PKI)**: the root fingerprint and validity, the
intermediate, the trust manifest version, the agent endpoint (where Shepherds connect, and the port
DROOL listens on, 9443, separate from the web interface) and the controller epoch.

![Shepherds → Trust (PKI): a warning that the root private key is still on the controller; the root fingerprint, intermediate, trust manifest version 1, agent endpoint and controller epoch; "Take the root key offline" in three steps; Replace the intermediate CA (Planned rotation, or Compromise: rotate and revoke); Import a trust manifest; and what backups contain](images/shepherd-pki.png)

## Expired certificates

A Shepherd that was off for a while (a host in a drawer, a long holiday) has an expired certificate.
When it comes back, it recovers by proving it still holds its key, within **180 days**
(`agent.recovery_grace`). After that it must be [enrolled again](Installing-a-Shepherd.md).

## Revoking a Shepherd

If a host was compromised, sold, or re-imaged: open the host's page and click **Revoke Shepherd
identity** in its **Management** card, give a **Reason (recorded in the audit log)**, and confirm. It
takes effect immediately: the Shepherd can no longer connect. To manage the host with a Shepherd again,
reinstall the host and enroll it again.

## Cloned machines (identity conflict)

If two machines connect with the **same** Shepherd identity at once (usually a cloned VM that copied
the Shepherd's key), DROOL **quarantines** the identity: *Identity conflict… DROOL doesn't pick one:
management is paused until you resolve it.* The host's Management card lists both connections
(address, hostname, boot id, since when).

1. Shut down or re-image the clone. On the clone: stop `drool-agent` and remove `/var/lib/drool-agent`.
2. Click **Release quarantine**.

If you can't tell which machine is the real one, click **Revoke identity** and enroll each machine
again.

## Replacing the intermediate CA

**Shepherds → Trust (PKI) → Replace the intermediate CA** signs a new intermediate and a new trust
manifest with the root key. Shepherds pick it up the next time they connect.

- **Planned rotation**: routine replacement; the old intermediate stays valid while Shepherds move.
- **Compromise: rotate and revoke**: if the intermediate's key may be exposed. The old intermediate is
  revoked; Shepherds move over through certificate recovery, without re-enrolling.

Both need the **root key**. If you [took it offline](Taking-the-Agent-Root-CA-Offline.md), paste it
into **Root private key (PEM), used once and not stored**, or use the command line:
`drool agent-ca rotate --root-key drool-agent-root.key [--revoke-old]`.

## Good to know

- Everything here is in the [audit log](Audit-Log.md), and asks you to
  [confirm it's you](Re-authentication.md).
- A Shepherd's own view: `drool-agent status` on the host shows its identity, certificate, trust and
  endpoints.
- After restoring DROOL from a backup, see [What Happens After Restore](What-Happens-After-Restore.md).

## Related

[Taking the Agent Root CA Offline](Taking-the-Agent-Root-CA-Offline.md) · [Installing a Shepherd](Installing-a-Shepherd.md) ·
[Shepherd Offline](Shepherd-Offline.md)
