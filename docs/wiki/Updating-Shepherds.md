# Updating Shepherds

**Shepherds update themselves.** When you update DROOL, the Shepherds on your hosts follow
automatically: one host first, then the others in batches, each one checking the update's signature
before it installs it and going back to its previous version on its own if the new one doesn't work.
There is nothing to set up, no key to create and nothing to upload.

This page explains what you see under **Shepherds → Updates**, what DROOL does, and what to do when
something needs you. Custom builds, for those who make their own, come
[at the end](#custom-releases).

## The short version

1. [Update DROOL](Updating-DROOL.md) the way you installed it.
2. That's all. Within a minute or so of DROOL starting, its Shepherds begin to update.
   **Shepherds → Updates** shows how far it got.

## Shepherds → Updates

![Shepherds → Updates: Up to date, all 4 Shepherds run v1.5.0; DROOL v1.6.0 is available; automatic updates on; the Stable channel; last checked just now, with Check now; Shepherd v1.5.0 signed by the DROOL project and verified; 4 Shepherds up to date; Recent updates: the update to v1.5.0, completed, 4 of 4 updated, automatic; Advanced settings below](images/shepherd-updates.png)

At the top is how updates stand, in a word and a sentence:

| Status | Means |
|---|---|
| **Up to date** | Every Shepherd runs DROOL's version (or the newest one in your channel) |
| **Updating** | An update is running; the card **Updating now** shows how far. Offline Shepherds update when they connect again |
| **Update available** | Shepherds can be updated, and automatic updates are off: **Update N Shepherds to …** starts it |
| **Needs attention** | An update was paused, failed or rolled back, or a release failed its check. See [below](#when-something-needs-attention) |
| **Can't update** | Some Shepherds run an older version, and nothing can update them now; the reason is next to each one |
| **No Shepherds yet** | None installed. Shepherds you install get DROOL's own version |

Below it:

- **Automatic updates**: on unless someone turned them off. Off means Shepherds update only when
  someone starts an update. Turning them on or off takes effect at once; nothing else changes.
- **Update channel**: **Stable** (recommended): releases only. **Release candidates**: also the test
  versions published before a release, for trying DROOL before others do.
- **Last checked**: when DROOL last asked its release page about updates, and **Check now**. DROOL
  checks shortly after it starts and then every six hours (hourly while its own update isn't published
  yet).
- **Shepherds**: how many are up to date, updating, waiting or can't update.
- **DROOL x.y.z is available**, when a newer DROOL is out: update DROOL, and its Shepherds follow.
- **Recent updates**: each update, how many Shepherds it updated, and whether DROOL started it
  automatically or a person did. **Show hosts** lists each host: first (the canary), batch, updated,
  skipped and why.

Everyone who can see Shepherds can see this page; changing it takes the **agents.manage** permission
(administrators). Others see a *Read-only* note.

## What DROOL does

- **Shepherds follow DROOL.** The update for your Shepherds is the Shepherd that came with your
  DROOL. DROOL never updates them past its own version: update DROOL first.
- **Only signed, official releases go out by themselves.** The DROOL project signs each release's
  Shepherds with its own key, offline. Every DROOL and every Shepherd has that key's public half
  built in. DROOL fetches only the signed *description* of the Shepherd it ships (from DROOL's
  release page) and checks it against that key and against the program it came with; nothing it can
  run is downloaded. Each Shepherd then checks the signature again with *its* built-in key before it
  installs anything. Someone who took over DROOL's release page couldn't make your hosts run their
  code: DROOL takes only signed descriptions from it, of the program it already has. Someone who took
  over your DROOL couldn't either, without the DROOL project's private key, which never leaves the
  release manager's offline machine.
- **Carefully:** one Shepherd first. Only when it's healthy do the others follow, five at a time. If
  one fails, the update **pauses** and waits for you.
- **On each host** the Shepherd downloads the new program from DROOL (resuming if interrupted),
  checks it against the signed description, keeps the old version next to it, switches, and restarts.
  It keeps the new version only if it starts and reconnects to DROOL within two minutes; otherwise it
  switches back by itself, without relying on the new version. A power cut leaves either version
  complete.
- **Offline hosts update when they come back.** A host that's off during an update is updated the
  next time its Shepherd connects.
- **Hosts that can't take it are skipped, not failed**: one that refuses updates or doesn't trust the
  key, one that needs another version first. That never pauses the update for the others, and the
  page says why for each.
- **No downgrades**, except a Shepherd going back to its previous version after a failed update.

## When something needs attention

| You see | What happened | What to do |
|---|---|---|
| **The update to x.y.z is paused** | A Shepherd failed or rolled back, so the others weren't updated | Look at the failure below it. When it's fixed, **Resume**. **Cancel…** stops the update: Shepherds that already updated keep it, the others stay on their version, and DROOL won't start that version again by itself |
| **The update of host to x.y.z was rolled back** | The new version didn't come back healthy, so the Shepherd went back to its previous version | Check the host (disk full? the network?) and the detail shown. **Try again…** when the cause is fixed. A Shepherd never tries a version that failed on it again unless you ask |
| **…'s Shepherd release failed verification** | DROOL found a description of its Shepherd that isn't signed correctly, or doesn't match the program it shipped with | Nothing was installed. Make sure you installed DROOL from its official release. If it persists, report it |
| **this host trusts only its own release keys** | The host's `/etc/drool/agent.yaml` says `updates: {official: deny}` | Its administrator wants their own builds only: use a [custom release](#custom-releases), or remove that line |
| **updates are turned off on this host** | The host's `/etc/drool/agent.yaml` says `updates: {policy: deny}` | Intended: the host keeps its version. Remove the line to let it update |
| **this Shepherd doesn't have the DROOL release key** | It was built before DROOL had official releases (a development or pre-1.0 build) | Update it once with a [custom release](#custom-releases), or reinstall the Shepherd from its host's page |

If official updates can't work at all, the page says so in a box under the status, for example:

- **This DROOL build has no DROOL release key built in**: a development build of DROOL can't verify
  official releases. Install an official DROOL release, or use custom releases.
- **This DROOL is a development build**: there are no official Shepherd releases for development
  builds (such as the `:main` container image).
- **The signed Shepherd release of DROOL x.y.z isn't published yet**: DROOL checks again every
  hour. Shepherds keep their version until then.
- **DROOL couldn't reach its release source**: DROOL checks `github.com`. Without internet access,
  see [Without internet access](#without-internet-access).

## Turning automatic updates off

Clear **Update Shepherds automatically**. DROOL then tells you when an update is available, and
**Update N Shepherds to …** starts one, with the same care (one first, then batches, pausing on a
failure). For a single host, its **Management** section has **Update Shepherd to …**.

A choice you saved is kept when you update DROOL. An installation from before automatic updates that
had already rolled out releases of its own keeps updating by hand.

A host can also refuse updates whatever DROOL says: `updates: {policy: deny}` in its
`/etc/drool/agent.yaml` keeps it on its installed version.

## Advanced settings

**▸ Advanced settings** at the bottom of the page holds what most people never need:

- **How updates roll out**: how many hosts go first (the canary), the batch size, after how many
  failures the update pauses, and the **health window**: how long a new version has to reconnect
  before it's rolled back. **Exactly this version** keeps every Shepherd on one version instead of
  the newest in the channel.
- **Custom releases** and your own release-signing keys ([below](#custom-releases)).
- **Official releases**: where DROOL looks for them (the release source) and which DROOL project keys
  it has built in. The source is the setting `agent.update_source` ([Configuration](Configuration.md)):
  a mirror's address, or `off` to never look.

## Without internet access

DROOL needs only small files from its release page, never programs. Without internet access:

1. On a machine with access, download `drool-agent_<version>_linux_amd64.release.json` (and the
   `arm64` one if you have arm64 hosts) from the release page of the DROOL version you installed.
2. Under **Shepherds → Updates → Advanced settings → Custom releases**, choose the file as the
   **Signed description (.release.json)** and click **Upload release**. Leave **Shepherd program**
   empty: DROOL already has it.
3. DROOL checks the signature and that it describes its own Shepherd program; the update then runs as
   usual.

To stop DROOL from contacting the release page at all, set `agent.update_source: off`
([Configuration](Configuration.md)).

## Custom releases

For Shepherd builds you make or sign yourself, for example a patched Shepherd. You need **agents.manage**,
a machine other than the controller to keep your key on, and the `drool` program there.

1. **Create a release-signing key**, on that machine: `drool release keygen -o my-release`. Keep
   `my-release.key` offline, encrypted, with a backup.
2. **Make each host trust it.** DROOL can't do this for you: trusting a key decides what runs as root
   on the host. When you install the Shepherd, open **Advanced: also trust your own release-signing
   key** and paste `my-release.pub`; or, on a host that already has one, copy the file there and run
   `sudo drool-agent trust-release-key my-release.pub`.
3. **Register the key in DROOL**, so it can check uploads early: **Advanced settings → Custom releases
   → Your release-signing keys**, paste the line from `my-release.pub`, **Add key**.
4. **Sign the program**: `drool release sign --key my-release.key path/to/drool-agent`. This writes
   `drool-agent.release.json`, a signed description of exactly that file. `drool release sign` reads
   the version and architecture from the program itself; for builds without a release version, give
   it with `--version`, exactly as `drool-agent version` prints it. Use `--channel testing` (or any
   other name) for builds you don't want automatic updates to roll out.
5. **Upload it**: **Advanced settings → Custom releases**, choose the **Signed description** and the
   **Shepherd program**, **Upload release**.
6. **Roll it out**: **Update N Shepherds to …** next to it. With automatic updates on, a custom
   release in your update channel (**Stable** unless you chose another) is rolled out like an
   official one.

Hosts that don't trust your key are skipped, and the page says so. To make a host take only your
builds and never official ones, add `updates: {official: deny}` to its `/etc/drool/agent.yaml`.

## Related

[Updating DROOL](Updating-DROOL.md) · [Installing a Shepherd](Installing-a-Shepherd.md) ·
[Shepherd Incompatible](Shepherd-Incompatible.md) · [Configuration](Configuration.md) ·
[Shepherd PKI](Shepherd-PKI.md)
