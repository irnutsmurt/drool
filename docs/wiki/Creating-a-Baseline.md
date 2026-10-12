# Creating a baseline

A **baseline** is a named list of what machines should have: services, packages and files
([What Desired State Is](What-Desired-State-Is.md)). In the examples, Alex creates *Server Defaults*
for every host, and *Media Server* for the `media-servers` Pack.

## Before you start

- You need **desired.manage** (administrators).
- For a **file**, have its exact content ready as a file on your computer: you upload it. A good way
  is to download the file from a host that's already right ([Uploading and Downloading](Uploading-and-Downloading.md)).
- For a **service**, monitoring must be on (DROOL reads services from it).
- Know which Packs should get the baseline ([Packs](Packs.md)).

## Steps

1. Open **Desired State → Baselines** and click **Create baseline**.
2. Under **Baseline**, enter a **Name** (*Server Defaults*) and optionally a **Description** (*What
   every server should have*).
3. Under **Applies to**, choose **Every host**, or **Packs and hosts I choose** and tick them. See
   [Applying a Baseline](Applying-a-Baseline.md) for what happens when baselines disagree.
4. Under **Resources**, add what the hosts should have:
   - **Add service**: the **Service** name (for example `jellyfin.service`), **Running** (*Yes,
     running*, *No, stopped* or *Leave alone*) and **Enabled at boot** (*Yes*, *No* or *Leave alone*).
   - **Add package**: the **Package** name (for example `unattended-upgrades`) and whether it **Should
     be** *Installed* or *Not installed (prohibited)*.
   - **Add file**: the **File path on the host** (`/etc/motd`), **Upload content**, and optionally:
     - **Owner**, **Group** and **Permissions** (for example `root`, `root`, `0644`). Empty means DROOL
       doesn't manage that part.
     - **Read and write it as root (sudo) on SSH hosts**: needed for most files under `/etc`.
     - **Validation and service action**: a **Validation command** that checks the new file before it
       replaces the live one (`{path}` is the new, staged file; *a failure leaves the live file
       untouched*), and a service to **reload** or **restart** afterwards. For example, for
       `/etc/caddy/Caddyfile`: `caddy validate --config {path}`, then reload `caddy.service`. See
       [Validation Before Deployment](Validation-Before-Deployment.md).
5. Check **Impact before saving** on the right: how many hosts are affected, what changes, and which
   hosts would start to differ.
6. Click **Create baseline** (when changing one: **Save revision 2**).

![Changing the baseline Server Defaults: Applies to Every host; its resources /etc/motd (root, root, 0644, read and written as root), the packages openssh-server (Installed) and telnet (Not installed), and a new package unattended-upgrades being added; the impact preview says 6 hosts are affected and will be checked after saving](images/baseline-editor.png)

**Saving changes the desired state only.** Nothing on the hosts changes until you (or
[automatic restore](Automatic-Restore.md)) [restore managed state](Restore-Managed-State.md). After
saving, DROOL checks the affected hosts, and the Overview shows which ones comply.

## Changing a baseline

Open the baseline and click **Edit** (or **Change baseline** on a host's resource). Every saved change
is a new **revision**; earlier revisions are kept unchanged and listed on the baseline's page with
**View**.

## Archiving a baseline

**Archive** on the baseline's page stops it applying to hosts; its revisions and history are kept.
Archiving doesn't change anything on the hosts: files, packages and services simply stop being
checked. **Show archived baselines** on the Baselines page lists them.

## Good to know

- A baseline holds at most **200 resources**.
- A managed file's content is kept in DROOL's file store and is part of [backups](What-a-Backup-Contains.md).
- Only `.service` units can be managed, and not the boot setting of masked or static units.
- DROOL never follows or replaces a symbolic link where a managed file should be: such a host shows
  the reason instead of being changed.

## Related

[Applying a Baseline](Applying-a-Baseline.md) · [Drift](Drift.md) · [Restore Managed State](Restore-Managed-State.md) ·
[Packs](Packs.md)
