# Review difference

**Review difference** shows a managed resource's desired and current state side by side, so you can
see what happened before you decide to restore or accept it. *Read-only: reviewing never changes the
host.*

## Before you start

- Everyone who can see Desired State can review differences.
- Seeing a file's **content** (the line-by-line diff) needs **files.read** (administrators and
  operators). Without it, you see the checksums, size, owner and permissions, but not the content.

## Steps

1. Open the host's **Desired State** section, or click the host on **Desired State → Overview**.
2. Click the resource that differs, for example `/etc/motd` (each row opens; the arrow at its end
   says so).
3. Click **Review difference**. DROOL fetches the current state (*Comparing…*) and shows it.

![Review difference for /etc/motd on web-01: drift detected; desired is the managed content (sha256 f427ae57deec…), owner root:root, mode 0644, and on the host the file is missing; the SHA-256, size, owner and permissions table, and the diff adding the two lines "Managed by DROOL." and "Authorized use only."](images/review-difference.png)

## What you see

- **Desired** and **Current**, and when the drift was first observed.
- For a **service**: running or not, enabled at boot or not.
- For a **package**: installed or not, and the **installed version**.
- For a **file**: one of *The file is missing on the host; restoring creates it*, *The content
  differs*, *The content matches; owner, group or permissions differ*; then the SHA-256 checksum,
  size, owner and permissions, desired and **on the host**; and for text files up to 1 MiB a
  line-by-line diff (`---` the file on the host, `+++` the managed content, lines DROOL would add in
  green, lines it would remove in red). *Binary or large file: compared by hash only.*

Reviewing a file's content is recorded in the [audit log](Audit-Log.md), because it may contain
secrets.

## Then what?

Close the dialog and choose: [Restore Managed State](Restore-Managed-State.md),
[Accept Current State](Accept-Current-State.md) or **Change baseline** ([Drift](Drift.md#what-to-do-about-it)).

## Related

[Drift](Drift.md) · [Browsing Files](Browsing-Files.md) · [Editing Files](Editing-Files.md)
