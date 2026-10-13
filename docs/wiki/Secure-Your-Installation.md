# Secure your installation

DROOL works from the first sign-in, and nothing on this page is required before you start adding
hosts. But DROOL holds the keys to your machines, so it's worth an hour, soon after installing, to
go through its own security checklist.

You'll find the checklist at **Settings → System → Secure your installation**. DROOL fills it in
from what it can see about itself, sorted into four kinds:

- **Problem** (✕) — something is actively wrong. Fix it.
- **Recommended** (⚠) — strongly advised hardening.
- **Check yourself** (?) — things only you can check, because DROOL can't see them.
- **Optional** (ℹ) — good ideas that depend on your situation.

Items you've completed move to **Done** (✓). Problems (and a few recommendations) also show in the
security indicator at the top of every page and on the Overview until they're handled.

![Settings → System → Secure your installation for the example network: 3 things to look at; recommended: two-factor sign-in for every administrator, the Agent Root CA key offline, a remote copy of the audit history; check yourself: a copy of the master key kept apart from the backups, and backups kept off this machine; optional: your own TLS certificate, and short-lived SSH certificates (5 hosts still use a stored key or password, 1 uses certificates); done: encrypted connections](images/secure-your-installation.png)

## Problems

### Plaintext HTTP on a network address

DROOL was configured to serve plain, unencrypted HTTP to the network, so passwords and session
cookies cross the network readable by anyone in between. DROOL only allows this when someone
explicitly turned on `server.allow_insecure_plaintext`. Turn TLS back on (the default), or put an
HTTPS reverse proxy in front of DROOL and bind DROOL to the loopback address
([Configuration](Configuration.md#behind-a-reverse-proxy)).

If you run DROOL behind a reverse proxy correctly (TLS off, listening only on `127.0.0.1`), the
checklist shows **HTTPS through your reverse proxy** instead, as something to check yourself: DROOL
can't see whether the proxy uses HTTPS.

### Test switches are set

Environment variables meant only for DROOL's own automated tests are set (their names start with
`DROOL_TEST_`). They make DROOL report invented data. Remove them from the container or service
environment and restart DROOL.

### Audit history verification failed

DROOL found audit records that were changed, removed, added or reordered outside DROOL. Someone with
access to DROOL's database may have edited it. See [Audit Log](Audit-Log.md#if-verification-fails).

## Recommended

### Two-factor sign-in for every administrator

Without it, a stolen password is enough to sign in as an administrator. The checklist names the
administrators who haven't turned it on. Turn yours on under **Settings → Account → Set up
two-factor authentication** ([Two-Factor Authentication](Two-Factor-Authentication.md)); ask the
others to do the same.

### Agent Root CA key offline

When DROOL started for the first time, it created a certificate authority (the *Agent Root CA*)
that Shepherds trust. Its private key is still on the controller. If someone copied it, they could
create certificates your Shepherds would believe. Export it to offline storage and remove it from
the controller under **Shepherds → Trust (PKI)** ([Taking the Agent Root CA Offline](Taking-the-Agent-Root-CA-Offline.md)).
You only need it again to replace the intermediate certificate authority, which is rare.

This applies even if you don't use Shepherds yet.

### Remote copy of the audit history

If the controller were ever compromised, an attacker could rewrite its audit log consistently. A
copy sent, as it's written, to another machine is what still tells the truth. Set one up under
**Audit Log → Remote copy** ([Remote Audit Copy](Remote-Audit-Copy.md)). You need a small HTTPS
receiver on another machine to send it to.

## Check yourself

### A copy of the master key, kept apart from your backups

Every stored password, SSH key and other secret is encrypted with the file `master.key` in DROOL's
data folder. **Backups don't contain it** (unless you ask), so a backup alone can't give your stored
credentials back. DROOL can't see whether you keep a copy, so it always lists this item.

Copy the key now, and again after every rotation, to somewhere safe that isn't where your backups
are (a password manager, an encrypted USB stick):

| Installation | Command |
|---|---|
| Package or archive | `sudo install -m 0600 -o "$USER" /var/lib/drool/master.key ./master.key` |
| Docker Compose | In `~/drool`: `docker compose cp drool:/data/master.key ./master.key` |
| Podman | `podman cp drool:/data/master.key ./master.key` |

Each command puts `master.key` in your current folder; move it to its safe place from there and
delete the local copy. [The Master Key](Master-Key.md) explains why it matters and what happens
without it.

### Backups, kept off this machine

DROOL shows the newest backup in its own data folder, if any. Copies you keep elsewhere are invisible
to it, so check yourself that you have a recent backup on another machine. See
[Backup and Recovery](Backup-and-Recovery.md); `drool backup verify` checks a backup without
restoring it.

## Optional

### Your own TLS certificate

DROOL uses a certificate it created itself, so browsers warn on the first visit. That's fine at
home. If you have a certificate for the controller's name (from your own certificate authority, or
a public one), configure it: [Configuration](Configuration.md#your-own-tls-certificate).

### Short-lived SSH certificates instead of stored keys

The checklist counts the hosts DROOL still signs in to with a stored key or password. Instead, DROOL
can sign in with a fresh SSH certificate for each connection, valid for minutes, from its own SSH
certificate authority. Nothing long-lived is left on the controller to steal.
See [Short-Lived SSH Certificates](Short-Lived-SSH-Certificates.md).

## Also worth doing

These aren't on the checklist, but belong to a well-kept installation:

- **Give people their own accounts** with the least role they need (viewer, auditor, operator,
  admin), rather than sharing one administrator. See [Users and Roles](Users-and-Roles.md).
- **Keep DROOL updated** ([Updating DROOL](Updating-DROOL.md)).
- **Look at the Audit Log now and then**, especially after someone leaves.

## Related

[Backup and Recovery](Backup-and-Recovery.md) · [The Master Key](Master-Key.md) ·
[Re-authentication](Re-authentication.md) · [Shepherd PKI](Shepherd-PKI.md)
