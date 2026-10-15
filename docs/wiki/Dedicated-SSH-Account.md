# DROOL's own SSH account

DROOL can sign in to a host with an account of its own, which it creates itself. You give it an
account you already have on the host (your own, with its password or key) just once: DROOL uses it to
create its account, proves that the new account works, switches to it, and forgets what you typed.
[Your First Host](Your-First-Host.md) walks through it; this page is the reference.

Why it's the recommended way:

- **Your own password or key isn't kept in DROOL.** The details you typed for the setup are deleted
  once DROOL signs in with its own account.
- **DROOL's actions are easy to tell apart** from yours in the host's logs (`journalctl`,
  `/var/log/auth.log`): they come from DROOL's account.
- **Your account and DROOL's are independent.** Changing your password, or removing your key,
  doesn't lock DROOL out, and the other way round.

## What DROOL creates on the host

In the examples the account is called `drooladmin`; DROOL suggests `drool`. Names are lower-case
letters, digits, `_` and `-` (at most 32, starting with a letter or `_`), and can't be `root`.

| What | Where | Details |
|---|---|---|
| The account | `/etc/passwd` | A system account with a home folder (`/home/drooladmin`) and the shell `/bin/bash` (`/bin/sh` if there's no bash). It has **no password** (`*`), so nobody can sign in as it with one. |
| DROOL's key | `~drooladmin/.ssh/authorized_keys` | DROOL's own SSH key (`controller-default`), the same one for every host. The folder and file belong to the account, modes 700 and 600. |
| The sudo rule | `/etc/sudoers.d/drool-account-drooladmin` | Passwordless sudo for everything (shown below), mode 440. The host's own `visudo` checks it before DROOL installs it. |
| DROOL's record | `/var/lib/drool/ssh-accounts/drooladmin` | Readable by root only: which DROOL key set the account up and the account's user ID. It's how DROOL knows an account is its own. |

The sudo rule, as DROOL writes it:

```
# Managed by DROOL: drooladmin is the account DROOL signs in with on this host.
# Full management: drooladmin may run any command as root without a password,
# which makes it root-equivalent. Delete this file to take that away.
drooladmin ALL=(ALL:ALL) NOPASSWD: ALL
```

That is the whole rule on almost every host. DROOL adds a line only when the host's sudo needs one:

- **If sudo requires a terminal for the account** (`requiretty` in the host's sudo configuration),
  DROOL adds `Defaults:drooladmin !requiretty` above the rule, because DROOL runs commands without a
  terminal. It finds out by trying: as the new account, without a terminal, it runs `sudo -n id -u`.
  It adds the line only if the host's own `visudo` accepts it.
- **If the host's sudo doesn't accept that line**, DROOL stops and installs nothing
  ([below](#when-the-setup-stops)). sudo-rs, Ubuntu's sudo since 25.10, has no `requiretty` at all,
  so it never needs the line, and DROOL never writes it there.

Before it installs anything, DROOL checks the exact file with the host's `visudo`. Afterwards the
whole sudo configuration must report no problem it didn't report before, and sudo must actually let
the account run a command as root without a password. If any of that fails, DROOL takes its file
out again (and puts back the one it replaced, on a second run).

**Full management gives this DROOL account passwordless sudo and therefore root-equivalent
administrative capability.** That is what DROOL needs to install packages, manage services, reboot
and edit system files. Anyone who controls DROOL (or its key) can do anything on the host as root.
Protect DROOL accordingly: [Secure Your Installation](Secure-Your-Installation.md).

What DROOL never does for this:

- **install software.** The host needs `sudo` (with `visudo`), `useradd`, `usermod`, `getent`,
  `runuser` and `setsid`, which every supported distribution has. If something is missing, DROOL
  stops and says what.
- **delete or change the account you use for the setup.** Its password, keys, groups and sudo
  rights stay exactly as they were.
- **take over an account it didn't create**, whatever its name ([below](#an-account-with-that-name-already-exists)).
- **create a separate key per host.** Every DROOL account uses DROOL's own key, the one under
  **Credentials** ([SSH Credentials](SSH-Credentials.md)).

## How the setup goes

1. You add the host and trust its host key ([SSH Host-Key Verification](SSH-Host-Key-Verification.md)).
2. DROOL tests the connection with the setup account: it must sign in and become root.
3. DROOL asks the host, read-only and as root, whether the name is free (or an account DROOL
   created before) and nothing it needs is missing. It shows you exactly what it will change.
4. You confirm, with your DROOL password if your sign-in isn't recent: it's a high-impact change
   ([Re-authentication](Re-authentication.md)).
5. DROOL runs the setup as a run of its own on the host, while no other run can change the host.
   As root, through the setup account, it creates the account (or continues with the one it created
   before), its key, its sudo rule and its record.
6. DROOL opens a **brand-new connection**: not the one it used for the setup, and nothing it kept
   from before. It dials every hop again (through the [jump host](Jump-Hosts.md), if any), checks each
   host key, signs in as the new account with DROOL's key alone, and runs sudo without a password.
   If the host is itself a jump host for others, the new account must also forward connections to
   each of them.
7. **Only if all of that worked** does DROOL switch the host to the new account. In the same step it
   forgets the setup credentials that nothing else uses ([below](#what-happens-to-the-setup-credentials)).
8. DROOL closes the connections it still had as the setup account, tests the connection as the new
   account and refreshes the host's inventory.

DROOL never changes how it signs in first and hopes the new account works afterwards: until step 7
the host keeps working exactly as before.

The run appears under **Runs** like any other (*Set up DROOL's own SSH account on web-01
(drooladmin)*), with what happened at each step. It can't be scheduled, put in a workflow or
re-run from there: to run it again, use the host's page.

## What happens to the setup credentials

| Credential | After the switch |
|---|---|
| A password, key or sudo password you **typed in Add host** (named like `setup-web-01-login`, marked **for setup only** under Credentials) | **Deleted**, unless another host uses it by then (a jump host included): then it's kept, and the result says which hosts still use it |
| A **stored credential** you chose for the setup (one you added under Credentials before) | Kept. Delete it under **Credentials** once nothing uses it |
| **DROOL's own key**, if the setup account signed in with it | Kept, of course: DROOL's account uses it. It also stays in the setup account's `authorized_keys` on the host; remove it there yourself if DROOL shouldn't be able to sign in as that account any more |

Secret values never appear in DROOL's logs, audit log, errors or run output. The audit log records
the setup (`host.ssh_account`): the host, the account, the setup account, whether DROOL switched,
and each credential's name and what happened to it. Each deleted credential gets its own
`credential.delete` record.

### If the setup is never finished

You added a host with **Create a dedicated DROOL SSH account**, and then left it: closed the page, the
setup failed and you haven't fixed it yet, or you changed your mind.

- **The setup credentials stay** while the host signs in with them, marked **for setup only**, so the
  setup can be finished later from the host's page. DROOL never deletes them because time passed.
- **Removing the host** (a half-added host you gave up on) deletes them with it.
- **Editing the host** to sign in another way (another credential, another account, a jump host's
  credential) deletes the setup credentials it no longer uses.
- Either way, a setup credential another host uses by then (a jump host included) is kept, and stored
  credentials you created yourself under **Credentials** are never deleted.
- When DROOL starts, it deletes setup-only credentials no host uses any more (left behind by an older
  version of DROOL).

Each deletion is recorded in the audit log as `credential.delete`, with the credential's name and why
(the host was removed, or signs in another way), never with its value; the host's own record
(`host.delete` or `host.update`) lists the setup credentials that went with it.

## An account with that name already exists

DROOL checks before it changes anything. An account with the name you chose counts as DROOL's own
only if DROOL's record for it is there, was written with DROOL's current key, and names the
account's actual user ID. Otherwise DROOL stops, changes nothing, and says why:

- *an account named drool already exists on this host, and DROOL didn't create it*: choose another
  name (for example `drool-mgmt`), or use that account with **Use this SSH account** instead. A
  common case: the host runs DROOL itself, whose package created a system account `drool`.
- *a group named … already exists*: DROOL creates its account with a group of the same name; choose
  another name.
- *the account … was set up by a DROOL controller with another SSH key*: another DROOL (or this one,
  before its key was replaced) created it. Choose another name, or, if it was this DROOL, remove the
  old account from the host first ([as below](#removing-the-account)) and run the setup again.

## When the setup stops

Whatever the reason, **nothing is switched**: the host keeps signing in as the setup account, and no
credential is deleted. The message says which step failed:

| Message | What to do |
|---|---|
| *DROOL couldn't sign in to … as … for the setup* | The setup account's password or key. Check it ([Authentication Failed](Authentication-Failed.md)), then run the setup again. |
| *… couldn't become root on …: sudo rejected the password* (or *requires a password*) | The sudo password. Edit the host and choose the right one ([Sudo Failed](Sudo-Failed.md)). |
| *this host doesn't have: sudo* | Install sudo on the host yourself, or keep signing in with this account. |
| */etc/sudoers doesn't read /etc/sudoers.d* | Add the line `@includedir /etc/sudoers.d` to `/etc/sudoers` with `sudo visudo` (older systems write `#includedir`). |
| *DROOL couldn't use drooladmin on a fresh connection: authentication as "drooladmin" failed* | The account was created, but the host's SSH server won't let it in. Usually `AllowUsers`, `AllowGroups`, `DenyUsers` or `DenyGroups` in `/etc/ssh/sshd_config` (the plan warns when they're set). Allow the account, then run the setup again: it continues with the account it created. |
| *… sudo without a password didn't work*, *sudo doesn't let drooladmin run commands as root without a password and without a terminal* | Another sudo rule takes the account's rights away, or sudo rules come from elsewhere (LDAP). Fix that, then run the setup again. |
| *this host's sudo requires a terminal for drooladmin (requiretty), and its visudo doesn't accept an exception for the account* | The host's sudo wants a terminal, and doesn't accept the line that would let DROOL work without one. DROOL installed nothing. Remove `requiretty` for that account (or for everyone) from the host's sudo configuration, then run the setup again. |
| *visudo rejected DROOL's sudo rule, so it wasn't installed* | The message quotes `visudo`. Nothing was installed. Check the host's sudo (`sudo visudo -c`), then run the setup again. |
| *the sudo configuration reports a new problem with DROOL's rule in it* | DROOL took its rule out again; the message quotes `visudo -c`. Check the host's sudo configuration, then run the setup again. |
| *the connection to … was lost while DROOL was setting up …, so DROOL doesn't know how far it got* | Run the setup again: it looks at what's on the host and continues. |
| *…'s connection settings were changed while DROOL was setting up …* | Someone edited the host meanwhile; that change wins. Run the setup again. |

A warning that doesn't stop the setup: *visudo -c reports problems that are already on this host*
(for example another file in `/etc/sudoers.d` with the wrong mode). DROOL doesn't fix other files
and doesn't count those problems against its rule: it checks that its own file is valid, that it
adds no new problem, and that sudo grants it.

To run the setup again: **Try again** where it stopped, or **Set up a dedicated DROOL SSH account**
on the host's page.

## Hosts you already added

On the host's page, the **Management** card offers **Set up a dedicated DROOL SSH account…** to
administrators, for hosts that sign in with a credential and don't use DROOL's own account yet. It
opens the same plan as step 4 of Add host, and runs the same setup.

It isn't offered for hosts that only serve as a jump host (DROOL doesn't manage them, so it doesn't
need an account with sudo there), nor for hosts that sign in with SSH certificates
([below](#ssh-certificates)).

## Jump hosts

- **A host behind a jump host:** the setup and the brand-new connection both go through the jump host,
  with its trusted key. The jump host's own account and credentials don't change, and a credential
  the jump host shares with the host is kept.
- **A jump host only:** gets no account, see above.
- **A managed host that other hosts connect through:** before switching, DROOL checks that the new
  account can forward connections to each of them. The hosts behind it then reach it as the new
  account too.

## SSH certificates

The account works with [Short-Lived SSH Certificates](Short-Lived-SSH-Certificates.md) like any
other: once the host signs in as DROOL's account, you can move it to certificates as usual
([Moving Hosts to SSH Certificates](Moving-Hosts-to-SSH-Certificates.md)), and its certificates then
name that account. DROOL doesn't create a certificate authority for this.

A host that already signs in with certificates: switch it back to its credential first, set up the
account, then switch to certificates again.

## Shepherds

A host with a [Shepherd](SSH-vs-Shepherds.md) can have DROOL's own SSH account too, as its SSH path
or SSH fallback. The Shepherd doesn't use or need the account, and you can still go Shepherd-only
later.

## Removing the account

DROOL never deletes its account by itself; removing the host from DROOL leaves it on the host. To
remove it, first make sure DROOL no longer signs in with it (remove the host from DROOL, or edit it to
use another account), then on the host:

```sh
sudo rm /etc/sudoers.d/drool-account-drooladmin
sudo userdel --remove drooladmin
sudo rm /var/lib/drool/ssh-accounts/drooladmin
```

The first line takes the root rights away, the second removes the account and its home folder, the
third DROOL's record.

## Related

[Your First Host](Your-First-Host.md) · [SSH Credentials](SSH-Credentials.md) ·
[Understanding SSH vs Shepherds](SSH-vs-Shepherds.md) · [Jump Hosts](Jump-Hosts.md) ·
[Removing a Host](Removing-a-Host.md) · [Audit Log](Audit-Log.md)
