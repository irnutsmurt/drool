# dpkg was interrupted

On a Debian or Ubuntu host, a package operation fails with *dpkg was interrupted during an earlier
operation*, with the hint *Log in to the host and run: sudo dpkg --configure -a (DROOL does not repair
this automatically).*

## What it means

An earlier package installation or update stopped in the middle: a power cut, a reboot, a killed
process, a full disk. Some packages are unpacked but not configured, and APT refuses to do anything else
until that's finished.

DROOL doesn't repair this by itself on purpose: finishing an interrupted installation can ask questions
(for example, whether to keep a configuration file you changed), and the right answer depends on the
machine.

## What to do

1. Make sure nothing else is installing packages right now ([Package Manager Busy](Package-Manager-Busy.md)),
   and that the disk isn't full (`df -h /`).
2. On the host, in a terminal, finish the interrupted work and answer any questions:

   ```sh
   sudo dpkg --configure -a
   ```

3. Then fix anything still broken: `sudo apt-get -f install`.
4. Run the DROOL operation again (**Re-run unsuccessful hosts**).

Without a terminal on the host, you can do step 2 as a DROOL [command](Running-a-Command.md) with
**Run as root (sudo)**. A run can't answer questions, so tell dpkg to keep your configuration files
where they were changed:

```sh
DEBIAN_FRONTEND=noninteractive dpkg --force-confdef --force-confold --configure -a
```

## Related

[Package Manager Busy](Package-Manager-Busy.md) · [System Updates](System-Updates.md) ·
[Failed, Interrupted and Unknown Outcomes](Failed-Interrupted-and-Unknown-Outcomes.md)
