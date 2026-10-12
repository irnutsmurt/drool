# Editing files

You can open and edit text files on a host right in the browser: a quick fix to a config file without
opening a terminal. DROOL protects you from the classic mistake of overwriting a change someone else
made in the meantime.

## Before you start

- **files.write** to save (administrators and operators); **files.read** to view.
- The file must be **UTF-8 text** and at most **1 MiB**. Larger or binary files can't be edited here;
  DROOL offers to download them instead.
- System files (under `/etc`, for example) usually need **Use administrator privileges**
  ([Administrator Privileges](Administrator-Privileges.md)), unless the host has a Shepherd.

## Steps

1. Open the host's **Files**, find the file and click its name, for example
   `/etc/jellyfin/network.xml` on `media-02`.
2. Edit the text. *Unsaved changes* appears next to the buttons; **Undo changes** goes back to the
   version you opened.
3. Click **Save**. DROOL writes the new content the same safe way as an upload (to a temporary file
   next to it, then put in place in one step) and keeps the file's owner and permissions.

## If someone changed the file while you were editing

When you save, DROOL checks that the file on the host is still the version you opened. If it isn't,
the save is refused:

*This file changed on the host after you opened it. Reload it or explicitly overwrite the newer
version.*

- **Reload (discard my changes)** loads the current version, so you can see the other change. It's
  almost always the right choice; DROOL asks *Discard your changes and load the current version?*
  first, so copy your edits somewhere if you'll want them.
- **Overwrite the newer version** saves your text anyway, replacing the other change. Use it only if
  you know what that change was.

## If you don't have permission

If the login user isn't allowed to write the file, the save fails and DROOL offers **Save with
administrator privileges** (*Writes the file as root through this host's sudo setting*), if you have
the **files.elevate** permission. While you edit with administrator privileges, a banner says
*Editing as root (administrator privileges). The save is audited.*

## Better ways for some edits

- **The same file on many hosts:** use a [file deployment](Deploying-a-File-to-Multiple-Hosts.md): it
  shows a diff per host, can validate the file first (for example `nginx -t`) and can be rolled back.
  A manual edit keeps no previous version.
- **A file that must stay exactly like this:** put it under [Desired State](What-Desired-State-Is.md);
  DROOL then tells you whenever it drifts.
- **Configuration a service reads at start:** remember to reload or restart the service afterwards
  ([Managing Services](Managing-Services.md)).

## Related

[Browsing Files](Browsing-Files.md) · [Permissions and Ownership](Permissions-and-Ownership.md)
