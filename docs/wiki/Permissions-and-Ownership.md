# Permissions and ownership

The file manager shows every file's owner and permissions in two notations at once, with a
plain-language description, and lets you change them with checkboxes instead of remembering octal
numbers.

## Reading permissions

Each entry shows, for example:

- **Owner** `root:root` — the owning user, then the owning group.
- **Permissions** `rw-r--r--` and `0644`, described as *owner: read, write · group & others: read*.
  Hover over the permissions for the full description, such as *Owner: read, write · Group: read ·
  Everyone else: read*.

For folders, *read* means listing the contents, *write* means creating or deleting files in it, and
*execute* means entering it, which is why the description of `.ssh` above reads *only the owner: list,
change, enter*.

## Before you start

- **files.write** (administrators and operators).
- Changing permissions works for the file's owner. Changing the **owner** almost always needs root:
  tick **Use administrator privileges** first on SSH hosts ([Administrator Privileges](Administrator-Privileges.md)).

## Change permissions

1. In the host's **Files**, click **Permissions** next to the file or folder.
2. Tick or untick read, write and execute for owner, group and everyone else, or type a **Numeric
   mode** in octal, such as `644` or `0755`. The description below updates as you go.
3. Save.

Symlinks have no **Permissions** action: their own permissions don't matter, and DROOL never changes
a file *through* a link.

## Change the owner

1. Click **Owner**.
2. Type **Owner (user)** and **Group**, for example `jellyfin` and `jellyfin`.
3. Save. (*Changing the owner usually needs administrator privileges.*)

## Common values

| Octal | Shown as | Typical for |
|---|---|---|
| `0644` | `rw-r--r--` | Ordinary configuration files |
| `0640` | `rw-r-----` | Config files with secrets, readable by a service's group |
| `0600` | `rw-------` | Private keys, files only the owner may read |
| `0755` | `rwxr-xr-x` | Folders, programs and scripts |
| `0700` | `rwx------` | Private folders such as `~/.ssh` |

## When deploying files

A [file deployment](Deploying-a-File-to-Multiple-Hosts.md) can set owner, group and permissions on
every host at once. Leave them empty to keep what each existing file has (new files then get `root`,
or the login user, and `0644`).

## Limits

DROOL shows and changes the standard owner, group and permission bits. It doesn't show or change
ACLs, SELinux labels or extended attributes.

## Related

[Browsing Files](Browsing-Files.md) · [Administrator Privileges](Administrator-Privileges.md)
