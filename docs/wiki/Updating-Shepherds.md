# Updating Shepherds

Shepherds update themselves, but only to versions **you** have signed with a release-signing key
that you keep away from DROOL. That way, even someone who controlled your DROOL controller couldn't
make your Shepherds run code you didn't approve: they could offer an update, but the Shepherds
would refuse it.

This page sets up that trust once, then shows how to roll out a new version.

## Before you start

- **Update the controller first** ([Updating DROOL](Updating-DROOL.md)). A controller works with
  older Shepherds that speak the same protocol; the reverse isn't guaranteed.
- You need **agents.manage** (administrators) for keys, releases and rollouts.
- You need a machine **other than the controller** to keep the signing key on (your workstation, or
  better, an offline machine), with the `drool` program on it (from the same release).
- Each host must **trust your key**. That's the part people miss: registering the key in DROOL is not
  enough, because trusting a key is security-sensitive and DROOL can't do it for a host remotely.

## One-time setup: a release-signing key

1. **Create the key**, on your workstation (not the controller):

   ```sh
   drool release keygen -o drool-release
   ```

   This writes `drool-release.key` (the private key: keep it offline, encrypted, with a backup copy)
   and `drool-release.pub` (the public key you'll hand out).
2. **Register the public key in DROOL:** **Shepherds → Updates → Release-signing keys**, paste the
   contents of `drool-release.pub`, click **Add**.
3. **Make each host trust it**, in one of three ways:
   - when installing a Shepherd from a host's page, paste the public key into **Also trust a
     release-signing key (optional)**;
   - when installing with the installer command, add `--trust-release-key FILE`;
   - on a host that already has a Shepherd, copy `drool-release.pub` there and run:

     ```sh
     sudo drool-agent trust-release-key drool-release.pub
     ```

Hosts that don't trust your key simply won't install your releases, and say so.

## Rolling out a new version

Every DROOL release includes the Shepherd programs (`drool-agent_<version>_linux_amd64.tar.gz` and
the arm64 twin). For each architecture your hosts use:

1. **Check and unpack it**, on your workstation:

   ```sh
   sha256sum --check --ignore-missing SHA256SUMS
   tar xzf drool-agent_1.0.1_linux_amd64.tar.gz
   ```

2. **Sign it:**

   ```sh
   drool release sign --key drool-release.key drool-agent_1.0.1_linux_amd64/drool-agent
   ```

   This writes `drool-agent.release.json` next to the binary: a signed description of exactly that
   file. `drool release sign` reads the architecture and version from the binary itself, and refuses
   anything a Shepherd would reject later (another program, a build from modified source). For your
   own development builds, give the version with `--version`, exactly as `drool-agent version`
   prints it.
3. **Upload it:** **Shepherds → Updates → Releases** — choose the **Agent binary** and the **Signed
   manifest (.release.json)**, click **Upload release**.
4. **Roll it out:** click **Roll out** next to the release and confirm. Or set the **Update policy** to
   automatic (below).

## What a rollout does

- **One canary first.** One Shepherd updates; only when it's healthy do the others follow, in
  batches.
- **It pauses after failures** (the number under **Pause after failures**) and waits for you.
- **Offline hosts update when they come back.**
- **On each host:** the Shepherd checks the signature against the keys *it* trusts, stages the new
  program next to the current one, switches over and restarts. It keeps the new version only if it
  starts and completes a session with DROOL within the **Health window**; otherwise it **rolls back
  to the previous version on its own**, without needing the new version to work. A power cut leaves
  either the old or the new version complete.
- **No downgrades**, except that automatic rollback.

For a single host, the **Management** card shows **Update Shepherd to …** when a newer release is
available.

## The update policy

**Shepherds → Updates → Update policy**:

| Setting | Meaning |
|---|---|
| **Policy** | *Manual*: releases roll out only when someone starts a rollout. *Automatic*: new releases roll out by themselves, canary first, then batches. |
| **Channel** / **Or exactly this version** | With automatic updates: the newest release in a channel, or one exact version |
| **Canary hosts**, **Then batches of**, **Pause after failures** | How careful a rollout is |
| **Health window (s)** | How long a new version has to connect before it's rolled back |

Click **Save policy**.

A host can refuse updates altogether: `updates: {policy: deny}` in its `/etc/drool/agent.yaml`
keeps it on its installed version whatever DROOL asks.

## Related

[Installing a Shepherd](Installing-a-Shepherd.md) · [Shepherd Incompatible](Shepherd-Incompatible.md) ·
[Shepherd PKI](Shepherd-PKI.md) · [Updating DROOL](Updating-DROOL.md)
