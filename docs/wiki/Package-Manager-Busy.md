# Package manager busy

A package install, removal or system update fails with *the package manager is locked by
unattended-upgr (pid 1342)* or *the package manager is locked by another process*.

## What it means

Only one program at a time may change a host's packages. Another one is doing it right now, most often
**unattended-upgrades** (Debian and Ubuntu's automatic updates, which often run shortly after boot and
once a day), or someone running `apt` or `dnf` by hand.

DROOL already handles the usual case: it **waits up to two minutes** for the lock, and retries the
operation by itself (package operations are safe to retry). You only see this message when the other
program kept the lock for longer than that.

## What to do

1. **Wait** a few minutes, then run it again, for example with **Re-run unsuccessful hosts…** on the
   run's page. Automatic updates can take a while on a host that hasn't been updated for some time.
2. To see what holds the lock, on the host:

   ```sh
   ps -o pid,etime,cmd -p 1342            # the pid from the message
   systemctl status unattended-upgrades apt-daily-upgrade.service
   ```

3. If a process is genuinely stuck (it has been running for hours without progress), stop it on the
   host, then check for an interrupted dpkg ([dpkg Was Interrupted](dpkg-Was-Interrupted.md)).

Don't delete lock files by hand: if the other program is still running, two programs then change the
package database at the same time.

## Related

[Installing Packages](Installing-Packages.md) · [System Updates](System-Updates.md) ·
[dpkg Was Interrupted](dpkg-Was-Interrupted.md)
