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
- its Desired State values and history, and its health-setting customizations (expected service
  states included);
- network classifications made for this host alone;
- the **setup-only credentials** typed into **Add host** for it (the password and sudo password that
  set up DROOL's own account), unless another host, a jump host included, still uses them. Stored
  credentials you created yourself on **Credentials** are never deleted with a host;
- its Shepherd identity, if it has one.

**What stays:** the **run history** (past runs keep showing the host by name) and the **audit log**
(which records the removal itself).

## What stays on the host

Nothing on the host changes. In particular:

- **DROOL's SSH key or certificate trust** stays in `drooladmin`'s `~/.ssh/authorized_keys` (and, if
  you used SSH certificates, in `/etc/ssh/drool_user_ca.pub`). Remove them if DROOL should no longer
  be able to log in.
- **DROOL's own account**, if DROOL created it, stays too, with its passwordless sudo: remove it as
  shown in [DROOL's Own SSH Account](Dedicated-SSH-Account.md#removing-the-account).
- **A Shepherd keeps running.** It will keep trying to connect to DROOL, which no longer knows it.
  Uninstall it first ([below](#uninstalling-a-shepherd)).
- **A firewall DROOL enforced keeps enforcing**, and keeps loading at boot. If DROOL enforces the
  host's firewall, click **Remove DROOL policy** on the host's firewall page *before* removing the
  host ([Recovering From a Firewall Problem](Recovering-From-a-Firewall-Problem.md)).
- **Files DROOL deployed** stay as they are.

## Steps

1. If the host has a Shepherd, uninstall it first: **Uninstall Shepherd…** on the host's
   **Management** card ([below](#uninstalling-a-shepherd)).
2. If DROOL enforces the host's firewall, remove DROOL's policy from the host first.
3. On the host's page, scroll to **Danger zone** and click **Remove host**. Confirm *Remove media-02
   from DROOL? Nothing is changed on the host.*

## Uninstalling a Shepherd

To stop using a Shepherd on a host and manage the host over SSH again, you don't need to remove the
host. On its **Management** card, click **Uninstall Shepherd…** (**agents.manage** and
**hosts.write**: administrators; DROOL asks for your password again).

DROOL first signs in to the host over SSH and reads what is there. The dialog lists exactly what it
will do:

- **on the host**, as root: stop the Shepherd and disable its service (or, on a host without systemd,
  stop its supervisor), and remove its service file, its programs (`/usr/libexec/drool-agent`), its
  settings (`/etc/drool/agent.yaml`, `/etc/drool/release-keys.d`), its identity and state, the
  private key included (`/var/lib/drool-agent`), and its log (`/var/log/drool-agent`). Each line
  says whether that item is there;
- **in DROOL**: revoke the Shepherd's identity (it can never connect again), revoke enrollment tokens
  that could still enroll one for this host, forget its capability narrowing, and manage the host
  over SSH with the sign-in DROOL just checked.

Click **Uninstall Shepherd**. DROOL removes everything, checks on the host that nothing of it is left
and that no Shepherd process runs, and waits until the connection is gone. The result says what it
could confirm:

- *The Shepherd is gone from media-02: DROOL checked that nothing of it is left on the host and no
  connection remains.*
- *Not everything could be confirmed …*, with what remains: the identity is revoked and the host is
  managed over SSH all the same; click **Try again** to finish.

DROOL refuses rather than leave a host it can't reach:

- **No SSH access at all** (the Shepherd is the only way in): *DROOL manages media-02 only through its
  Shepherd: there's no SSH access to fall back on …*. Set up SSH first ([Connection Details](Connection-Details.md)),
  or remove the Shepherd by hand ([below](#by-hand-when-drool-cant-reach-the-host)).
- **SSH doesn't work right now**: *DROOL can't run commands as root on media-02 over SSH (…)*. Fix
  SSH first (**Test connection** on the host's page), or remove the Shepherd by hand.
- **Nothing left to remove**: the dialog says so, and nothing changes.

Files DROOL deployed, its firewall rules and DROOL's own SSH account stay. The audit log records the
uninstall (`agent.uninstall`), never a secret. To use a Shepherd again later, install one as before
([Installing a Shepherd](Installing-a-Shepherd.md)).

### By hand, when DROOL can't reach the host

For a host DROOL can't sign in to (broken SSH, a machine you're about to wipe), **Revoke Shepherd
identity** on the **Management** card, then remove the Shepherd on the host yourself, as root:

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

Revoking first means the Shepherd can't connect again even if a copy of its identity survived.

## Adding it back later

A removed host can be added again like any new host ([Adding a Host](Adding-a-Host.md)). DROOL
treats it as new: you trust its key again, and its old monitoring and alert history doesn't come
back.

## Related

[Host Details](Host-Details.md) · [Installing a Shepherd](Installing-a-Shepherd.md) ·
[Recovering From a Firewall Problem](Recovering-From-a-Firewall-Problem.md)
