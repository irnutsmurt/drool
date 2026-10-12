# Removing a host

**Remove host** takes a host out of DROOL. It doesn't log in to the host or change anything there,
which also means it doesn't clean anything up there. Read what stays behind before you click.

## Before you start

- You need to be an **administrator** (**hosts.write**).
- **A jump host can't be removed** while other hosts connect through it. DROOL refuses with
  *host is the jump host for: …* and names them. Change those hosts' **Connect through** first.

## What gets deleted in DROOL

Removing a host deletes, for that host:

- its connection settings, its trusted SSH host key and its Pack memberships;
- its inventory, monitoring history and **alert history**;
- its network activity history and its firewall state;
- its Desired State values and history, and its health-setting customizations;
- its Shepherd identity, if it has one.

**What stays:** the **run history** (past runs keep showing the host by name) and the **audit log**
(which records the removal itself).

## What stays on the host

Nothing on the host changes. In particular:

- **DROOL's SSH key or certificate trust** stays in `drooladmin`'s `~/.ssh/authorized_keys` (and, if
  you used SSH certificates, in `/etc/ssh/drool_user_ca.pub`). Remove them if DROOL should no longer
  be able to log in.
- **A Shepherd keeps running.** It will keep trying to connect to DROOL, which no longer knows it.
  Remove it first (below).
- **A firewall DROOL enforced keeps enforcing**, and keeps loading at boot. If DROOL enforces the
  host's firewall, click **Remove DROOL policy** on the host's firewall page *before* removing the
  host ([Recovering From a Firewall Problem](Recovering-From-a-Firewall-Problem.md)).
- **Files DROOL deployed** stay as they are.

## Steps

1. If the host has a Shepherd, uninstall it on the host (below), or at least **Revoke Shepherd
   identity** on the host's **Management** card.
2. If DROOL enforces the host's firewall, remove DROOL's policy from the host first.
3. On the host's page, scroll to **Danger zone** and click **Remove host**. Confirm *Remove media-02
   from DROOL? Nothing is changed on the host.*

## Removing a Shepherd from a machine

DROOL has no uninstall command for Shepherds. On the host, as root:

```sh
sudo systemctl disable --now drool-agent
sudo rm -f /etc/systemd/system/drool-agent.service
sudo systemctl daemon-reload
sudo rm -rf /usr/libexec/drool-agent /var/lib/drool-agent /var/log/drool-agent
sudo rm -f /etc/drool/agent.yaml
```

This stops the Shepherd, removes its service, its programs, its identity (the private key it used to
prove itself to DROOL), its log and its settings. On a host without systemd (a container), the
Shepherd runs under a small supervisor script instead; stop that process, then delete the same
folders.

If you only want to get rid of the Shepherd and keep managing the host over SSH, you don't need to
remove the host. On its **Management** card, choose **Manage over SSH only**, then **Revoke Shepherd
identity** (give a reason, such as *Moving back to SSH*), then remove the Shepherd from the machine as
above.

## Adding it back later

A removed host can be added again like any new host ([Adding a Host](Adding-a-Host.md)). DROOL
treats it as new: you trust its key again, and its old monitoring and alert history doesn't come
back.

## Related

[Host Details](Host-Details.md) · [Installing a Shepherd](Installing-a-Shepherd.md) ·
[Recovering From a Firewall Problem](Recovering-From-a-Firewall-Problem.md)
