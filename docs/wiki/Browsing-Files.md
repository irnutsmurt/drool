# Browsing files

Every host has a file manager: browse its folders, open and edit text files, upload, download,
rename, move, delete, and change permissions and owners, all from the browser and all recorded in the
audit log.

## Before you start

- You need **files.read** to browse and download, **files.write** to change anything, and
  **files.elevate** to work as root. Administrators and operators have all of them; auditors and
  viewers have **no** file access at all, because file contents (keys, configuration) are often
  secrets.
- On an **SSH** host, DROOL needs SFTP, which almost every OpenSSH server offers by default (the
  `Subsystem sftp` line in `sshd_config`).
- On a **Shepherd** host, the Shepherd needs the files capability: the **Full** preset, or files
  allowed for certain paths ([Shepherd Capability Presets](Shepherd-Capability-Presets.md)).

## Open it

Open the host's **Files** section (in the examples: **Hosts → media-02 → Files**). The breadcrumb
above it says *Hosts › media-02 › Files*.

![web-01's Files section in /etc/ssh with administrator privileges on: the warning banner, Up and the path, Go to path, Refresh, New folder and Upload…, and the entries with their size, owner root:root, permissions (rw-r--r-- 0644, described), modification time, Download for files and a ⋯ menu for the other actions](images/file-manager.png)

- **The current path** is always shown (`/etc / jellyfin`); click a part of it to go there, or type a
  path into **Go to path…** and press Enter. **↑ Up** goes one level up.
- **Each entry** shows its name, size, owner (`root:root`), permissions twice (`rw-r--r--` and
  `0644`) with a plain description below (*owner: read, write · group & others: read*), and when it
  was last modified.
- **Click a folder** to open it; **click a file** to view or edit it ([Editing Files](Editing-Files.md)).
- **Actions** on each entry: **Download** (files), and the entry's **⋯** menu with **Rename or
  move…**, **Permissions…**, **Owner…** and **Delete…**. At the top: **Refresh**, **New folder**,
  **Upload…**.

## Who you are on the host

This is the most important thing to understand about the file manager:

- **On an SSH host**, you act as the host's **login user** (`drooladmin` in the examples), with that
  user's permissions, until you tick **Use administrator privileges** (top right). Then every action
  runs as **root** through the host's sudo setting, and a yellow banner says so. DROOL never switches
  to root on its own. See [Administrator Privileges](Administrator-Privileges.md).
- **On a Shepherd host**, files go through the Shepherd, which acts as **root** — on the whole
  filesystem with the Full preset, or only within the paths the host allows. A banner explains which.
  There's no separate switch.
- **If the host's login user is root**, everything is done as root anyway, and a banner says
  *This host's login account is root: every change is made as root.*

## Symbolic links

Symlinks are shown with their target and opened only when you click them (*Go to …*). DROOL never
writes, changes permissions on or replaces anything *through* a symlink, and deleting a symlink
removes only the link, never what it points to.

## Limits

- No recursive upload or download of folders.
- With administrator privileges on an SSH host, listings and reads use commands through sudo (`find`,
  `cat`) instead of SFTP, so they're slower on large folders.
- Every change is in the audit log with the host, path, action, result, checksums before and after,
  owner, group and permissions. **File contents never go into the audit log.** Reads with
  administrator privileges are audited too.

## Related

[Uploading and Downloading](Uploading-and-Downloading.md) · [Editing Files](Editing-Files.md) ·
[Permissions and Ownership](Permissions-and-Ownership.md) ·
[Deploying a File to Multiple Hosts](Deploying-a-File-to-Multiple-Hosts.md)
