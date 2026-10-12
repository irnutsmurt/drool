# Command line

Most of DROOL is used in the browser. A few jobs happen on the controller's command line: backups and
restores, recovering access, and keeping keys offline.

## How to run `drool` commands

| Installation | Run commands as |
|---|---|
| Native package or archive | `sudo -u drool drool <command>`. DROOL refuses to run as root on a data directory that belongs to the `drool` user, so files it writes stay readable to the service |
| Docker Compose | `docker compose exec drool drool <command>` (add `-T` when piping a file in or out), in the directory with `compose.yaml` |
| Podman | `podman exec drool drool <command>` (add `-i` when piping a file in) |

Commands that need DROOL **stopped** (restore, master key rotation) run in a one-off container instead:
see [Restoring a Controller](Restoring-a-Controller.md) and [Key Rotation](Key-Rotation.md).

## Commands

| Command | What it does | More |
|---|---|---|
| `drool backup [-o FILE] [--include-master-key]` | Back up the controller (safe while it runs). `-o -` writes to standard output | [Backup and Recovery](Backup-and-Recovery.md) |
| `drool backup verify FILE` | Check a backup can be restored, without restoring it. Exit status 0, 2 or 1 | [Verifying a Backup](Verifying-a-Backup.md) |
| `drool restore FILE` | Restore a backup (DROOL stopped). `-` reads standard input; `--master-key FILE`; `--discard-credentials` | [Restoring a Controller](Restoring-a-Controller.md) |
| `drool restore --recover` | Put the previous installation back after an interrupted restore | [Restore Needs Attention](Restore-Needs-Attention.md#drool-wont-start) |
| `drool setup code` | Issue a new one-time setup code for creating the first administrator | [First Login](First-Login.md) |
| `drool user create --username NAME [--role admin]` | Create a user (asks for the password): the way back in if every administrator is locked out | [Users and Roles](Users-and-Roles.md) |
| `drool user mfa-reset --username NAME` | Turn off a user's two-factor authentication | [Two-Factor Authentication](Two-Factor-Authentication.md#if-you-lose-your-authenticator) |
| `drool secrets rotate-key` | Replace the master key (DROOL stopped) | [Key Rotation](Key-Rotation.md) |
| `drool agent-ca status` | The Shepherd PKI: root (online or offline), intermediate, trust manifest | [Shepherd PKI](Shepherd-PKI.md) |
| `drool agent-ca export-root -o NAME` | Write the Agent Root CA key and certificate for offline storage | [Taking the Agent Root CA Offline](Taking-the-Agent-Root-CA-Offline.md) |
| `drool agent-ca remove-root --confirm FINGERPRINT` | Remove the root key from the controller | [Taking the Agent Root CA Offline](Taking-the-Agent-Root-CA-Offline.md) |
| `drool agent-ca rotate --root-key FILE [--revoke-old]` | New intermediate CA and trust manifest | [Shepherd PKI](Shepherd-PKI.md#replacing-the-intermediate-ca) |
| `drool agent-ca sign-manifest` / `import-manifest` | Sign a trust manifest on an offline machine / import it | [Shepherd PKI](Shepherd-PKI.md) |
| `drool release keygen -o NAME` | Create a release-signing key pair for Shepherd updates (on a machine other than the controller) | [Updating Shepherds](Updating-Shepherds.md) |
| `drool release sign --key KEY BINARY` | Sign a Shepherd binary | [Updating Shepherds](Updating-Shepherds.md) |
| `drool release verify` | Check a signed release against a public key | [Updating Shepherds](Updating-Shepherds.md) |
| `drool version [--json]` | Version, release channel, commit, database schema and Shepherd protocol | [Updating DROOL](Updating-DROOL.md) |
| `drool serve` | Run the controller (the service does this for you) | |

`drool --config FILE <command>` uses another configuration file ([Configuration](Configuration.md)).

## On a host with a Shepherd

| Command | What it does |
|---|---|
| `sudo drool-agent status` | Identity, certificate, trust, endpoints and capabilities |
| `sudo drool-agent trust-release-key FILE` | Trust a release-signing key for Shepherd updates ([Updating Shepherds](Updating-Shepherds.md)) |
| `journalctl -u drool-agent` | The Shepherd's log |

## Related

[Ports and Files](Ports-and-Files.md) · [Configuration](Configuration.md)
