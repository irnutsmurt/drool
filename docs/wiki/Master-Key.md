# The master key

DROOL encrypts its secrets with one key file, `master.key`: stored credentials (SSH keys, passwords,
sudo passwords), its certificate authorities, notification and audit-copy secrets, and two-factor
secrets. DROOL creates it on first start.

**The most important thing to know:** a backup does **not** contain the master key, unless you ask for
it. Without the key, a backup restores everything *except* those secrets. So keep a copy of the key,
**apart from your backups**.

## Where it is

| Installation | Where | Copy it out |
|---|---|---|
| Native package or archive | `/var/lib/drool/master.key` | `sudo install -m 0600 -o "$USER" /var/lib/drool/master.key ./master.key` |
| Docker Compose | `/data/master.key` in the `drool-data` volume | `docker compose cp drool:/data/master.key ./master.key` |
| Podman | `/data/master.key` in the `drool-data` volume | `podman cp drool:/data/master.key ./master.key` |

The copy commands put a copy, readable only by you, in the current folder.

## Keeping a copy

1. Copy the key out with the command for your installation (above).
2. Store the copy somewhere safe **and separate from your backups**: a password manager (as an attached
   file), an encrypted USB stick, or another machine you trust.
3. Delete the copy from the folder you copied it to.
4. Do this again after every [key rotation](Key-Rotation.md).

Why separate? Whoever has both a backup and the key has your credentials; whoever has only one of them
has none. [Secure Your Installation](Secure-Your-Installation.md) reminds you, because DROOL can't check
where your copy is.

## Backups with the key included

`drool backup --include-master-key` makes one self-contained file that restores everything, including
credentials. Anyone who has that file can decrypt your credentials: store it encrypted, or not at all.
(**Shepherds → Trust (PKI) → Backups** shows this command, because Shepherd trust depends on the master
key too.)

## If you lose it

DROOL refuses to start with a missing or wrong key, rather than silently making your credentials
unreadable. Restore your latest backup **without** credentials, and give hosts a credential again:
[Recovering Without Credentials](Recovering-Without-Credentials.md).

## Advanced: keeping the key outside the data directory

The `secrets.master_key_file` setting (environment variable `DROOL_SECRETS_MASTER_KEY_FILE`) puts the
key somewhere else, so that a copy of the data directory or volume never contains it. DROOL must be
able to write there on first start and when rotating. See [Configuration](Configuration.md).

## Related

[Key Rotation](Key-Rotation.md) · [Backup and Recovery](Backup-and-Recovery.md) ·
[Restoring a Controller](Restoring-a-Controller.md) · [Taking the Agent Root CA Offline](Taking-the-Agent-Root-CA-Offline.md)
