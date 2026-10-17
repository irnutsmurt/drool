# Shepherd offline

A host's Shepherd shows **offline** (on **Shepherds**, and in the host's **Management** section):
DROOL hasn't heard from the `drool-agent` service for a while. Monitoring for that host stops being
continuous, and operations go over SSH if the host allows [SSH fallback](SSH-Fallback.md), or wait.

## Usual causes

1. **The host is off or unreachable.** Check its console or power.
2. **The `drool-agent` service stopped.**
3. **The Shepherd can't reach the controller** on the agent port (**9443** by default): a firewall
   between them, or DROOL's address changed.
4. **Its certificate expired long ago.** A Shepherd that was off for more than 180 days must be enrolled
   again ([Shepherd PKI](Shepherd-PKI.md#expired-certificates)).
5. **It was revoked**, or is in an **identity conflict** (a cloned machine): see
   [Shepherd PKI](Shepherd-PKI.md#cloned-machines-identity-conflict).

## Check on the host

From the host's console or over SSH:

```sh
systemctl status drool-agent          # is it running?
sudo drool-agent status               # identity, certificate, trust, and the controller address it uses
sudo journalctl -u drool-agent -n 50  # what it says about connecting
```

Then, depending on what you find:

- **Stopped:** `sudo systemctl start drool-agent`, and look at the log if it stops again.
- **It can't connect to the controller address it shows:** check that the address resolves and is
  reachable from the host (`nc -vz drool.example.net 9443`). If DROOL's address changed, set the new
  one under **Shepherds → Controller address**: connected Shepherds test it before you commit, and keep
  the old address as a fallback. A Shepherd that's already cut off needs the old address to work again
  (for example, a DNS name pointing at the new controller).
- **Certificate or trust errors:** see [Shepherd PKI](Shepherd-PKI.md).

## In Docker

DROOL listens on 9443 inside its container; the machine publishes that port, as 9443 or another port.
Shepherds must be given the published port and an address they can reach:

- **The port:** set it once, at the top of DROOL's `compose.yaml` (the `shepherds:` line): it both
  publishes the port and tells DROOL ([how](Install-With-Docker-Compose.md#use-other-ports);
  [Synology](Install-On-a-Synology-NAS.md#change-a-port-later)). A Shepherd that was given another
  port than the published one can't connect.
- **The address:** Shepherds use the address you opened DROOL with. If they reach the machine by
  another name, set `DROOL_AGENT_ADVERTISE`
  ([how](Install-With-Docker-Compose.md#if-youll-use-shepherds)). `localhost` or a container-internal
  name doesn't work for other machines.

**Shepherds → Trust (PKI)** shows both: where Shepherds connect now, and the port DROOL listens on.
Shepherds that were installed before you changed the published port keep using the old one: put the
old number back.

## Related

[Shepherd Incompatible](Shepherd-Incompatible.md) · [SSH Fallback](SSH-Fallback.md) · [Installing a Shepherd](Installing-a-Shepherd.md)
