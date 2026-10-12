# What a backup contains

A DROOL backup is **one file** (a compressed `.tar.gz` archive) with everything DROOL needs to come back:

| In the backup | Which means |
|---|---|
| **The database** | Hosts and their trusted SSH host keys, Packs, users (with two-factor settings), API tokens, **stored credentials in encrypted form**, run history and output, the audit log, monitoring settings and history, alerts and notification settings, firewall policies and transactions, observed connections, schedules and workflows, Desired State, Shepherd identities and trust manifests, the SSH certificate authority, and all settings |
| **Stored files** | The content of files you deployed, the previous versions kept for rollback, and Desired State's managed file contents |
| **The TLS certificate** | DROOL's self-signed certificate, if it uses one (so browsers that trusted it still do) |
| **The configuration file** | If you use one (the setup code is removed from it) |
| **A manifest** | What the backup is, which DROOL made it, and checksums of every part |

The database is copied consistently while DROOL runs, so **backups are safe to make at any time**. The
file is written under a temporary name and renamed only when complete: a full disk or a crash never
leaves a half-written file under the backup's name.

Secrets in the database (credentials, the certificate authorities' keys, notification and audit-copy
secrets, two-factor secrets) are encrypted with the [master key](Master-Key.md), which the backup
**doesn't** contain unless you ask: see [What a Backup Does Not Contain](What-a-Backup-Does-Not-Contain.md).

## What `drool backup` tells you

After writing a backup, DROOL prints what's in it and anything you should know. This is what it printed
for the example network:

```text
Backup written to /var/lib/drool/backups/drool-backup-20261012-213603.tar.gz
  DROOL v0.1.0-alpha-73-ge3c0c58, schema 20, 3 users, 6 hosts, 1 credentials, 17 jobs, 186 audit events

This backup does NOT include the master key, so it is NOT a complete
disaster-recovery backup on its own. Its 3 stored credential(s) can only be
restored together with the master key file (key id 12a1738bb919c12b):
    /var/lib/drool/master.key
Keep a copy of that file somewhere safe and separate from this backup, or run
"drool backup --include-master-key" for a self-contained backup.

The Agent Root CA private key is still on this controller, so it is in this backup (sealed with the
master key). Export it to offline storage and remove it from the controller (Agents → Trust in the web UI).

The backup is inside the data directory; copy it to another machine for it to be useful after a disk failure.
```

Your version, counts and key id will differ. Two things to know when reading it:

- The first line counts credentials as you see them under **Credentials**; the master-key warning
  counts every **stored secret** the key protects (credentials, but also the SSH certificate
  authority's key and similar), so the two numbers can differ.
- *Agents → Trust* is the page called **Shepherds → Trust (PKI)** in the menu
  ([Taking the Agent Root CA Offline](Taking-the-Agent-Root-CA-Offline.md)).

## Related

[What a Backup Does Not Contain](What-a-Backup-Does-Not-Contain.md) · [Backup and Recovery](Backup-and-Recovery.md) ·
[Verifying a Backup](Verifying-a-Backup.md)
