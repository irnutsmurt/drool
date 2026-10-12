# Administrator privileges (working as root)

By default, everything you do in a host's file manager happens as the host's **login user**: on an
SSH host, the account DROOL signs in as (`drooladmin` in the examples), with exactly that user's
permissions. To read or change files that user can't, you switch on **Use administrator privileges**,
and DROOL does it as **root** through the host's sudo setting.

DROOL never switches to root on its own. You always decide, per visit, and you always see that you did.

## Before you start

- You need the **files.elevate** permission (administrators and operators).
- The host must allow DROOL to become root: sudo (passwordless, or with a stored *Sudo password*
  credential) or a root login. See [Sudo Failed](Sudo-Failed.md) if it doesn't work.
- This applies to **SSH hosts**. On **Shepherd** hosts the file manager always goes through the
  Shepherd, which acts as root within the limits the host allows; there's no switch.

## Switching it on

On the host's **Files** page, tick **Use administrator privileges** (*Act as root through this host's
sudo setting.*). A yellow banner appears: *Administrator privileges are on: you are reading and
changing files as root. Every change is recorded in the audit log.* Untick it when you're done.

The same choice appears in two other places:

- **Saving in the editor**, when the login user may not write the file: **Save with administrator
  privileges**.
- **File deployments**: **Use administrator privileges** in the deployment form, needed for most
  system files ([Deploying a File to Multiple Hosts](Deploying-a-File-to-Multiple-Hosts.md)).

## What changes as root

- **Everything is possible**, including mistakes. Deleting the wrong folder in `/etc` breaks a host.
  DROOL refuses a recursive delete of top-level folders (`/etc`, `/home`, …), but not of everything
  below them.
- **New files belong to root** (upload, new folder, editor save as root).
- **DROOL refuses to write into a folder other users could modify**, such as `/tmp` or any
  world-writable folder: while root is writing there, another user could swap the file. Write
  somewhere else, or use a non-root session.
- **It's slower on large folders:** listings and reads go through sudo with ordinary commands
  (`find`, `cat`) instead of SFTP.
- **It's audited**: every change, and every *read* made with administrator privileges.

## Related

[Browsing Files](Browsing-Files.md) · [Permissions and Ownership](Permissions-and-Ownership.md) ·
[Sudo Failed](Sudo-Failed.md)
