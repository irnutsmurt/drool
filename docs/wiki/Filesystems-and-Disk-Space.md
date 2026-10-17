# Filesystems and disk space

DROOL watches how full every filesystem on a host is, and whether it runs out of inodes or suddenly
becomes read-only. This is about **space**; the physical health of the disks underneath is a
separate topic ([Storage Health](Storage-Health.md)).

## Where to look

- **Monitoring** (the fleet page): the **Disk** column shows each host's fullest filesystem, for
  example *96% /srv/media*, and the **Low disk space** filter lists the hosts that are short.
- A host's **Storage** tile, on its **Overview** and monitoring **Summary**, shows its fullest
  filesystem.
- A host's **Metrics** tab has a **Filesystems** card: a chart of space used per mount point, and a
  table with **Mount**, **Device**, **Used**, **Free**, **Inodes** and **State** (*Read/write* or
  *Read-only*), each with **Customize health check**.

## When does DROOL call it a problem?

| Check | Recommended setting |
|---|---|
| **Filesystem space** | Warning at 85% full, critical at 95%. Two readings in a row decide it, so a short burst of temporary files doesn't count |
| **Filesystem inodes** | Warning at 90%, critical at 97% of inodes used (a filesystem out of inodes can't create files even with free space) |
| **Filesystem turned read-only** | A filesystem that was writable earlier in this boot became read-only, usually because the kernel hit errors on the disk |

For example, in the examples `nas-01` has a critical issue *"/srv/media is over 95% full — 327.7 GiB
free of 8.0 TiB"*.

## Different thresholds for different filesystems

A media library that's meant to be nearly full shouldn't warn at 85%, but the root filesystem should.
Click **Customize health check** next to that filesystem (on **Metrics**, or on the **Health settings**
tab) and set, for example, warning 92% and critical 98% for `/srv/media` only, while `/` keeps 85% and
95%. DROOL recognises the filesystem by its UUID, so the setting stays with it even if it's mounted
elsewhere later. See [Health Overrides](Health-Overrides.md).

Raising a space threshold never weakens the checks on the disk underneath: SMART, NVMe, media-error
and kernel-error detection stay as they were.

## Making space

DROOL doesn't clean up disks for you. To see what's using the space, [run a command](Running-a-Command.md)
such as:

```sh
du -xh --max-depth=2 /srv/media 2>/dev/null | sort -h | tail -20
```

(`-x` stays on that filesystem; `2>/dev/null` hides "permission denied" for folders the user can't
read. Tick **Run as root (sudo)** to see everything.)

## Related

[Storage Health](Storage-Health.md) · [Health Overrides](Health-Overrides.md) · [Host Monitoring](Host-Monitoring.md)
