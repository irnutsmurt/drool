# Uploading and downloading files

The file manager moves files between your computer and a host. Transfers stream straight through:
DROOL doesn't hold the file in memory, so large files work.

## Before you start

- Uploading needs **files.write**; downloading needs **files.read** (administrators and operators).
- To upload into a folder the login user can't write (anything under `/etc`, for example), tick **Use
  administrator privileges** first ([Administrator Privileges](Administrator-Privileges.md)).
  On Shepherd hosts this isn't needed.

## Upload

1. Open the folder on the host, for example **Hosts → media-02 → Files**, then `/etc/jellyfin`.
2. Click **Upload…** and choose one or more files on your computer.
3. If a file with that name already exists, DROOL asks: *network.xml already exists in /etc/jellyfin.
   Replace it?*

**What DROOL does:** it writes the upload to a temporary file **next to** the destination, checks its
checksum when the whole file has arrived, and only then puts it in place, in one step. If you cancel
(the **Cancel** button during the upload) or the connection drops, the destination is left exactly as
it was: you never end up with half a file.

A new file belongs to whoever wrote it: the login user, or `root` with administrator privileges.
Change it afterwards with **Owner** and **Permissions** if needed
([Permissions and Ownership](Permissions-and-Ownership.md)).

To put the same file on many hosts, don't upload it host by host: use a
[file deployment](Deploying-a-File-to-Multiple-Hosts.md), which previews, validates and can roll back.

## Download

Click **Download** next to a file. Your browser saves it. To read a file the login user can't, tick
**Use administrator privileges** first; such reads are recorded in the audit log.

Text files that are too large (over 1 MiB) or binary can't be opened in the editor; the editor then
offers **Download it** instead.

## Related

[Browsing Files](Browsing-Files.md) · [Editing Files](Editing-Files.md)
