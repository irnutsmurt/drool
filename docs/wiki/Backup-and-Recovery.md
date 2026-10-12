# Backup and recovery

DROOL keeps everything it knows (your hosts, their trusted keys, Packs, users, encrypted
credentials, run history, the audit log, firewall policies, schedules, Desired State, settings) in
one small database in its data folder. A backup is one file containing that database, DROOL's TLS
certificate and its configuration file, if you have one.

Two facts matter more than anything else on this topic:

1. **DROOL doesn't make backups on a schedule or copy them anywhere.** You run `drool backup`
   yourself, or from cron, a systemd timer or your backup tool, and you keep the files somewhere
   other than the controller.
2. **A backup doesn't contain the master key** (`master.key`), which encrypts your stored
   passwords and SSH keys. Without the key, a backup restores everything *except* those secrets.
   Keep a copy of the key, apart from the backups. See [The Master Key](Master-Key.md).

## The pages in this section

| If you want to… | Read |
|---|---|
| Know what is and isn't in a backup | [What a Backup Contains](What-a-Backup-Contains.md) · [What a Backup Does Not Contain](What-a-Backup-Does-Not-Contain.md) |
| Understand the master key | [The Master Key](Master-Key.md) |
| Make a backup | [Docker Backup](Docker-Backup.md) · [Native Installation Backup](Native-Installation-Backup.md) · [Podman Backup](Podman-Backup.md) |
| Check a backup is good, without restoring it | [Verifying a Backup](Verifying-a-Backup.md) |
| Restore after a disaster, or move DROOL to another machine | [Restoring a Controller](Restoring-a-Controller.md) |
| Understand what DROOL does right after a restore | [What Happens After Restore](What-Happens-After-Restore.md) · [Host Reconciliation](Host-Reconciliation.md) |
| Recover when the master key is lost | [Recovering Without Credentials](Recovering-Without-Credentials.md) |

## A sensible routine

1. **Once, now:** copy `master.key` to a password manager or an encrypted USB stick
   ([how](Master-Key.md#keeping-a-copy)).
2. **Every night:** make a backup and copy it off the controller. For example, on a native
   installation, a root crontab line (`sudo crontab -e`):

   ```text
   15 3 * * * sudo -u drool drool backup && rsync -a /var/lib/drool/backups/ backup-host.example.net:drool-backups/
   ```

   `backup-host.example.net` stands for your backup machine. Backups are safe to make while DROOL
   runs. Delete old ones from `/var/lib/drool/backups/` from time to time; DROOL doesn't.
3. **Now and then:** check a backup with `drool backup verify` ([Verifying a Backup](Verifying-a-Backup.md)).
4. **Before every update:** make a backup ([Updating DROOL](Updating-DROOL.md)).

**Settings → System** shows the backup commands for your kind of installation and, in
[Secure Your Installation](Secure-Your-Installation.md), when the newest backup in DROOL's own data
folder was made.

![Settings → System → Backup and restore: what a backup contains, the commands for Docker, Podman and a native installation, the reminder about the master key, and drool backup verify](images/system-backup-commands.png)

## Other things worth keeping

- **The Agent Root CA key**, once you've exported it from the controller (a different secret, for a
  different job): see [Taking the Agent Root CA Offline](Taking-the-Agent-Root-CA-Offline.md).
- **Your release-signing key** for Shepherd updates, if you use one ([Updating Shepherds](Updating-Shepherds.md)).

Both should be kept offline, and separately from the backups.
