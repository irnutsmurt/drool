# Removing a host

**Remove host…** takes a host out of DROOL. You choose how:

- **Clean up DROOL access and remove the host** (recommended when DROOL installed something there):
  DROOL first removes what it put on the host itself, its Shepherd and the SSH account it created,
  then forgets the host.
- **Remove from DROOL only**: DROOL forgets the host and changes nothing on it.

Read what stays behind either way before you click.

## Before you start

- You need to be an **administrator** (**hosts.write**). Cleaning up asks for your password again:
  DROOL removes its own way in.
- **A jump host can't be removed** while other hosts connect through it. DROOL refuses with
  *DROOL reaches … through …* (or *host is the jump host for: …*) and names them. Change those hosts'
  **Connect through** first.
- If DROOL enforces the host's firewall and you want that gone too, click **Remove DROOL policy** on
  the host's firewall page first ([Recovering From a Firewall Problem](Recovering-From-a-Firewall-Problem.md)):
  removing the host never touches the firewall.

## Steps

1. On the host's page, click **Actions → Remove host…** (or, in its **Management** section,
   **Remove host…** under **Danger zone**).
2. DROOL checks what it installed on the host and whether each part can be removed now:
   - **The Shepherd removes itself** (or *, removed over SSH*): see [Uninstalling a Shepherd](#uninstalling-a-shepherd);
   - **DROOL's SSH account drooladmin**: only an account DROOL can prove it created. The host must
     have DROOL's record of it, `/var/lib/drool/ssh-accounts/drooladmin`, written with DROOL's own
     SSH key and the account's user ID. The name alone proves nothing, and DROOL never removes an
     account you or anyone else created.
3. Pick **Clean up DROOL access and remove the host** or **Remove from DROOL only**, and click
   **Clean up and remove** (or **Remove from DROOL**).

## What the cleanup does on the host

For **DROOL's SSH account**, DROOL uses it one last time, as root through its own sudo. It checks the
account is DROOL's, then starts a small helper on the host that keeps running after DROOL's
connection ends (a temporary systemd unit, or a background process where there's no systemd). Once
DROOL's session is gone, the helper:

- removes DROOL's sudo rule (`/etc/sudoers.d/drool-account-drooladmin`, only DROOL's own file);
- removes the account, with its home folder (DROOL's key was in it) and mail spool;
- removes the account's group, only when it's the one created with the account and nobody else uses
  it;
- removes DROOL's record of the account last, then itself.

It checks the account is DROOL's again before it removes anything, and leaves anything else alone:
other accounts, groups, sudo rules and files.

The result says what DROOL could and couldn't see: *Cleanup initiated: … DROOL intentionally removed
its own management access and can't verify the final state through that account.* There's no way
left for DROOL to look. To be sure, check on the host later (`getent passwd drooladmin` prints nothing
once it's gone).

**If a part can't start** (the account isn't provably DROOL's, the host doesn't answer, it lacks
`userdel`), DROOL changes nothing, keeps the host and says why. You can still choose **Remove from
DROOL only**. **If a part fails after another one ran** (the Shepherd went, but the account's
cleanup didn't start), DROOL keeps the host and lists what was done and what wasn't: remove the host
again to finish, or remove it from DROOL only.

## What gets deleted in DROOL

Removing a host, either way, deletes for that host:

- its connection settings, its trusted SSH host key and its Pack memberships;
- its inventory, monitoring history and **alert history**;
- its network activity history and its firewall state;
- its Desired State values and history, and its health-setting customizations (monitored services
  included);
- network classifications made for this host alone;
- the **setup-only credentials** typed into **Add host** for it (the password and sudo password that
  set up DROOL's own account), unless another host, a jump host included, still uses them. Stored
  credentials you created yourself on **Credentials** are never deleted with a host;
- its Shepherd identity, if it has one.

It's also **taken out of the schedules and firewall policies that name it**. The dialog lists each one
before you click (*Either way, in DROOL: …*), and each change is in the [audit log](Audit-Log.md):

- **Schedules** keep running on their other hosts. A schedule that ran only on this host is
  **disabled** and says why ([Schedules](Schedules.md#when-a-host-it-runs-on-is-removed)).
- **Firewall policies** lose it from their sources and destinations. A policy whose only source or only
  destination was this host is **deleted**: it could never match again. Nothing allowed or blocked
  changes, because a removed host already matched nothing.
- **Notification routes** keep it in their filters on purpose: dropping it could widen a route to
  every host. It just matches nothing any more.

**What stays:** the **run history** (past runs keep showing the host by name) and the **audit log**
(which records the removal itself, and the cleanup's steps).

## What stays on the host

With **Remove from DROOL only**, nothing on the host changes:

- **DROOL's SSH key or certificate trust** stays in the account's `~/.ssh/authorized_keys` (and, if
  you used SSH certificates, in `/etc/ssh/drool_user_ca.pub`). Remove them if DROOL should no longer
  be able to log in.
- **DROOL's own account**, if DROOL created it, stays too, with its passwordless sudo
  ([removing it by hand](Dedicated-SSH-Account.md#removing-the-account)).
- **A Shepherd keeps running**, and keeps trying to connect to DROOL, which no longer knows it.

Either way:

- **A firewall DROOL enforced keeps enforcing**, and keeps loading at boot.
- **Files DROOL deployed**, packages it installed and services it changed stay as they are.

## Uninstalling a Shepherd

To stop using a Shepherd on a host, you don't need to remove the host. In its **Management**
section, on the **Shepherd (DROOL agent)** card, click **Uninstall Shepherd…** (**agents.manage** and
**hosts.write**: administrators; DROOL asks for your password again). The dialog says how it will
go and what it removes.

**When the Shepherd is connected, it removes itself.** No SSH is needed. DROOL asks it over its own
connection; it starts a helper that outlives it, and the helper stops the Shepherd (its service, or
its supervisor on a host without systemd) and removes its service file, its programs
(`/usr/libexec/drool-agent`), its settings (`/etc/drool/agent.yaml`, `/etc/drool/release-keys.d`), its
identity and state, the private key included (`/var/lib/drool-agent`), and its log
(`/var/log/drool-agent`). In DROOL, its identity is revoked (it can never connect again), enrollment
tokens that could still enroll one for this host are revoked, and its capability narrowing is
forgotten.

The result tells what DROOL saw, and what it couldn't:

| Result | Means |
|---|---|
| **Verified removed** | The Shepherd removed itself, and DROOL checked over SSH that nothing of it is left. Only possible where SSH works |
| **Self-removal initiated** | The Shepherd accepted and disconnected as expected. Without SSH, DROOL can't look at the host, so it says *initiated*, not *removed*. Check on the host if you need to be sure ([below](#by-hand-when-drool-cant-reach-the-host)) |
| **Partly removed** | DROOL's check over SSH still finds something: it's listed. Uninstall again (over SSH) to finish |
| **Outcome unknown** | The Shepherd didn't confirm. Nothing changed in DROOL. **Check again**: a Shepherd still there is asked again; one that's gone is removed over SSH, or forgotten |
| **Refused by the Shepherd** | It said why (for example, it couldn't start its helper). Nothing changed |

**Afterwards**, DROOL manages the host over SSH, if it has SSH access. If it doesn't (the Shepherd was
the only way in), the host stays in DROOL with no way to manage it, and its page says so: *DROOL has
no way to manage this host: it has no Shepherd and no SSH access is set up.* Set up SSH access
([Connection Details](Connection-Details.md)), install a Shepherd again, or remove the host.

**When the Shepherd can't be asked**, DROOL removes it over SSH, as root, as long as SSH works: the
Shepherd is offline, or it's older than this way of removing itself (*This Shepherd (version …) can't
remove itself: it's from before Shepherds could. Update it (Shepherds → Updates) and ask again, or
remove it over SSH or by hand*; [Updating Shepherds](Updating-Shepherds.md)).
DROOL reads what is on the host first, removes it, checks that nothing is left, and the result is
*The Shepherd is gone …* or what remains.

**When neither works** (the Shepherd offline and no working SSH), DROOL can't remove it now. Remove it
on the host by hand ([below](#by-hand-when-drool-cant-reach-the-host)), then click **Forget the
Shepherd** in the same dialog: DROOL revokes its identity and changes nothing on the host.

Files DROOL deployed, its firewall rules and DROOL's own SSH account stay (**Remove host…** can clean up
the account). The audit log records the uninstall (`agent.uninstall`, with how it was done), never a
secret. To use a Shepherd again later, install one as before ([Installing a Shepherd](Installing-a-Shepherd.md)).

### By hand, when DROOL can't reach the host

For a host DROOL can't reach (broken SSH and an offline Shepherd, a machine you're about to wipe),
remove the Shepherd on the host yourself, as root:

```sh
sudo systemctl disable --now drool-agent
sudo rm -f /etc/systemd/system/drool-agent.service
sudo systemctl daemon-reload
sudo rm -rf /usr/libexec/drool-agent /var/lib/drool-agent /var/log/drool-agent /etc/drool/release-keys.d
sudo rm -f /etc/drool/agent.yaml
```

This stops the Shepherd, removes its service, its programs, its identity (the private key it used to
prove itself to DROOL), its log and its settings. On a host without systemd (a container), the
Shepherd runs under a small supervisor script instead; stop that process, then delete the same
folders. Then **Forget the Shepherd** (or **Revoke Shepherd identity**) on the host's **Shepherd
(DROOL agent)** card, under **Management**, so it can't connect again even if a copy of its identity
survived.

The same commands check a self-removal that DROOL reported as *initiated*: once it's done, none of
these files exist (`ls /usr/libexec/drool-agent /var/lib/drool-agent` finds nothing).

## Adding it back later

A removed host can be added again like any new host ([Adding a Host](Adding-a-Host.md)). DROOL
treats it as new: you trust its key again, and its old monitoring and alert history doesn't come
back.

## Related

[Host Details](Host-Details.md) · [Installing a Shepherd](Installing-a-Shepherd.md) ·
[DROOL's Own SSH Account](Dedicated-SSH-Account.md) ·
[Recovering From a Firewall Problem](Recovering-From-a-Firewall-Problem.md)
