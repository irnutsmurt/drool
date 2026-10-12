# Taking the Agent Root CA offline

When DROOL starts for the first time, it creates an **Agent Root CA** ([Shepherd PKI](Shepherd-PKI.md)),
and its private key stays on the controller until you move it. While it's there, anyone who
compromises the controller (or gets a backup together with the master key) could use it to mint a
trust manifest that Shepherds would accept. DROOL warns you until you take it offline.

You only need the root key for two rare jobs: **replacing the intermediate CA**, and **signing a new
trust manifest**. Everything else works without it.

## Before you start

- You need **agents.pki** (administrators), and DROOL asks you to [confirm it's you](Re-authentication.md).
- Have two safe offline places ready, for example two encrypted USB sticks, or a password manager plus
  an encrypted USB stick. Not the controller, and not next to your backups.

## In the browser

1. Open **Shepherds → Trust (PKI)**.
2. Under **Take the root key offline**, click **1. Export the root key…**. *This is the root private key.
   Save both files somewhere offline (and a second copy), then remove the key from the controller.*
   Click **Save drool-agent-root.key** and **Save drool-agent-root.crt**.
3. Store both files offline, in two places. Delete them from your Downloads folder.
4. **2. Type the root fingerprint to confirm you saved the key**: copy it from the line below the field
   (it starts with `sha256:`).
5. Click **3. Remove the root key from the controller**.

## On the command line

On the controller (native installation shown; in a container, run `drool` through `docker compose exec`
or `podman exec`, and copy the files out of the container afterwards):

```sh
sudo -u drool drool agent-ca export-root -o /var/lib/drool/drool-agent-root
```

This writes `drool-agent-root.key` and `drool-agent-root.crt` in `/var/lib/drool`, and prints the exact
command to run next, with your root's fingerprint. Move both files offline (two places), delete them
from the controller, then run the printed command, which looks like this:

```sh
sudo -u drool drool agent-ca remove-root --confirm sha256:9a839906d7b2374d052e522c6e877dfa2882bb44974f0240d6151c29e579e659
```

The fingerprint must be your own (the one above is from the example network); `drool agent-ca status`
shows it too.

## Afterwards

- **Backups never contain a root key you removed.** Your offline copies are the only ones: you need one
  to recover from a lost master key, or a compromised intermediate.
- To replace the intermediate later, paste the key into **Replace the intermediate CA**, or run
  `drool agent-ca rotate --root-key drool-agent-root.key` ([Shepherd PKI](Shepherd-PKI.md#replacing-the-intermediate-ca)).
- To sign a trust manifest on an offline machine, use `drool agent-ca sign-manifest`, then **Import a
  trust manifest** on the Trust (PKI) page.

## Related

[Shepherd PKI](Shepherd-PKI.md) · [Master Key](Master-Key.md) · [Backup and Recovery](Backup-and-Recovery.md)
