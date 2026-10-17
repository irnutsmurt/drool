# What a backup does not contain

Some things are deliberately **not** in a DROOL backup. Keep them separately, or know how to get them
back.

| Not in the backup | Why | What to do |
|---|---|---|
| **The master key** (`master.key`) | Whoever has a backup *and* the key has your credentials; keeping them apart protects you if a backup leaks | Keep a copy of the key apart from your backups ([Keeping a copy](Master-Key.md#keeping-a-copy)). Or, deliberately, `drool backup --include-master-key` and store that file encrypted |
| **The Agent Root CA key**, once you took it offline | So a backup can never be used to mint Shepherd trust | Keep your two offline copies ([Taking the Agent Root CA Offline](Taking-the-Agent-Root-CA-Offline.md)) |
| **Your own release-signing key**, for custom Shepherd releases | It must never be on the controller | Keep it offline ([Updating Shepherds](Updating-Shepherds.md#custom-releases)) |
| **Your own TLS certificate and key** (`server.tls.mode: provided`) | They're your files, not DROOL's | Back them up with the rest of the machine, or reissue them |
| **GeoIP and ASN databases** | They're downloads | DROOL downloads its own again by itself; put your own back if you had any ([GeoIP and ASN](GeoIP-and-ASN.md)) |
| **DROOL's log files** | Not needed to restore | Your normal log handling |
| **Older backups and the `pre-restore-…` folders** in the data directory | A backup doesn't contain backups | Copy backups off the controller yourself |
| **The managed hosts themselves** | DROOL backs up the controller, not your servers | Back up your servers with your usual tools |

Also: a **native** backup is written to the controller's own disk (`/var/lib/drool/backups/`). Until you
copy it to another machine, it's lost along with the controller.

## Related

[What a Backup Contains](What-a-Backup-Contains.md) · [The Master Key](Master-Key.md) · [Backup and Recovery](Backup-and-Recovery.md)
