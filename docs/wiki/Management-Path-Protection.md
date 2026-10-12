# Management path protection

DROOL needs a way into each host to manage it: its **management path**. For an SSH host, that's SSH
from the controller (through the jump host, if there is one) to the host; for a Shepherd host, the
Shepherd's own connection to the controller. DROOL keeps that path open on purpose, and won't let a
firewall policy close it by accident.

## What DROOL does

- **It allows its own path ahead of your rules.** On every enforced host, the management path is
  allowed before any of your rules (*management path: SSH management from the controller (seen as
  10.10.0.10, port 22)* on `web-01`), so deleting or changing an allow rule can never cut it. Only a
  **deny** that covers the path can, and that's what the checks below catch.
- **It warns while you edit.** If a draft would cut a path (a deny, a change to a Pack or network, a
  redefined service), a red **Management path blocked** banner names the host, the **Required path**
  and the **Blocked segment**, for example *DROOL Controller → web-01 : TCP 22 by rule no-ssh-to-web*.
  **Show on the topology** highlights it.
- **It asks before you save** such a draft (*Save a policy that blocks DROOL's management path?*), and
  lists it under **Transaction safety blockers** in the preview.
- **It refuses to apply it.** The host shows **blocked: management path**, and applying is refused
  (*Refused: a policy would cut DROOL off.*) until the policy is fixed.

![A draft rule "no-ssh-to-web" in the YAML view: the Management path blocked banner says the change would prevent DROOL from managing web-01, with the required path DROOL Controller → web-01 TCP 22 and the blocked segment; the preview lists it under Transaction safety blockers](images/firewall-mgmt-blocked.png)

## Where the paths are shown

On the [topology](Topology-View.md), as dashed green lines from the **DROOL Controller** (*Protected
DROOL management path*), and for Shepherds to the controller (*mTLS TCP 9443*). On a host's firewall
page, **Enforcement → Management path** says which one DROOL used (*ssh* or *agent*).

**Check** on the host's firewall page measures the path DROOL actually uses, including the address the
host sees it coming from. *Not measured yet: click Check on the host* means DROOL doesn't know it yet.

## Good to know

- The path is allowed from the address the host saw at the **last transaction**. If the controller or
  the jump host changes address, apply again before relying on the new one.
- Only DROOL's own path is protected. Your own SSH access from your laptop is an ordinary rule
  (`admin-ssh-to-web` in the examples): DROOL can't know you need it.
- If you really must apply such a policy (to recover from a mistake), an administrator can use the
  [emergency override](Emergency-Override.md). Automatic rollback protection still applies.

## Related

[Automatic Rollback Protection](Automatic-Rollback-Protection.md) · [Emergency Override](Emergency-Override.md) ·
[Firewall Policy](Firewall-Policy.md)
