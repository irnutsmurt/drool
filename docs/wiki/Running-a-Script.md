# Running a script

When one line isn't enough (several commands, `if`, loops, variables), run a script. DROOL sends the
script to every target host, runs it there with the interpreter you choose, and shows each host's
output, exactly like a [command](Running-a-Command.md). It can even send a different script to
Debian-family and RHEL-family hosts in the same run.

## Before you start

The same as for commands: **jobs.execute** (administrators and operators), reachable hosts, and on
Shepherd hosts the **Full** preset (the shell capability).

## Steps

1. **Runs → New run**, then **Script** under **What to do**.
2. Choose the **Interpreter**: `sh` (the default; works everywhere) or `bash`.
3. Write the script in **Script**. For example, to see how much space Jellyfin's data uses on each
   media server:

   ```sh
   set -e
   if [ -d /var/lib/jellyfin ]; then
     du -sh /var/lib/jellyfin
   else
     echo "No Jellyfin data on $(hostname)"
   fi
   ```

   You don't need a `#!` line: the interpreter you chose runs the script.
4. Pick the **Targets** (for example the `media-servers` Pack), and set **Run as root (sudo)**,
   **Read-only**, **Name** and **Timeout (seconds)** as for a command. Scripts default to a 600-second
   timeout.
5. Click **Run on 3 hosts** and confirm *Run this script on 3 hosts?*.

## A different script per operating system

Tick **Different script per OS family** to write up to three variants:

- **Debian / Ubuntu**
- **RHEL / Rocky / Alma / Fedora**
- **Default (any other OS, optional)**

Each host runs the variant for the OS DROOL discovered on it. A host without a matching variant uses
the default, or is skipped if there's no default. For example, to see whether another firewall tool
is active (useful before [enabling DROOL's firewall](Before-Enabling-Enforcement.md)):

- Debian / Ubuntu: `ufw status || true`
- RHEL / Rocky / Alma / Fedora: `systemctl is-active firewalld || true`

The `|| true` keeps the script from being reported as failed just because the tool isn't installed.

## How the result is judged

A script **succeeds** when it exits with code 0 and **fails** with any other exit code
(*ExecutionError: The command ran but exited with an error*). Write `set -e` at the top so that the
script stops, and fails, at the first command that fails, instead of carrying on.

Anything the script prints to standard error is highlighted in the output.

## Things to keep in mind

- **Scripts can do anything** the account (or root, with sudo) can. DROOL can't preview what a
  script will change. For a change to many hosts, use a [rollout](Canary-Hosts.md): one host first.
- **Interactive programs won't work**: there's no keyboard on the other end. Use non-interactive
  options (`apt-get -y`, `--yes`), or better, the dedicated [package](Installing-Packages.md) and
  [service](Managing-Services.md) operations, which DROOL simulates and verifies.
- **A dropped connection** while a script runs leaves its outcome unknown, and DROOL doesn't run it
  again by itself ([why](Failed-Interrupted-and-Unknown-Outcomes.md)).

## Related

[Running a Command](Running-a-Command.md) · [Workflows](Workflows.md) ·
[Understanding Run Results](Understanding-Run-Results.md)
