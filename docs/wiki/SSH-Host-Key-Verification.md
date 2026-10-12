# SSH host-key verification

Every SSH server has a *host key*: a key pair created when the operating system was installed, which
proves "I am this machine". DROOL uses it to make sure it's talking to the right host before it
logs in or sends a password.

The rules are simple and strict:

- **DROOL connects to a host only after you trusted its key.** Until then the host shows
  **Needs host key**, and nothing (no test, no monitoring, no run) touches it.
- **DROOL checks the key on every connection**, including after reboots and through a jump host.
- **If the key ever changes, DROOL stops connecting** and shows **Host key changed** until an
  administrator looks at it. It never accepts a new key by itself.

This is the same protection your own `ssh` command gives you with `~/.ssh/known_hosts`, except that
DROOL never asks "are you sure?" mid-connection: it simply refuses.

## Before you start

Trusting a key needs the **hostkeys.approve** permission (administrators).

## Trusting the key of a new host

When you add a host, DROOL asks it for its keys and shows each one with its type (for example
`ssh-ed25519`) and a fingerprint such as `SHA256:sU3rdL7J8ULPqUqvP4ImLFCOKmPP8tXogMjm7goRYos`.

1. **On the host itself** (a terminal you already trust, or its console), list its fingerprints:

   ```sh
   for f in /etc/ssh/ssh_host_*_key.pub; do ssh-keygen -lf "$f"; done
   ```

   Each line shows a key size, the fingerprint, the file and the type, for example
   `256 SHA256:sU3rdL7J8ULPqUqvP4ImLFCOKmPP8tXogMjm7goRYos root@web-01 (ED25519)`.
2. In DROOL, paste the fingerprint of the matching type into **Paste the expected fingerprint to
   highlight the matching key**. The matching key gets a **matches expected** badge.
3. Click **Trust this key** next to it.

Why bother checking? If someone else answered at that address (a misconfigured DNS name, an address
reused by another machine, or an attacker), trusting its key would let DROOL log in to the wrong
machine. Checking once, here, is what makes every later connection safe.

You only need to trust one key per host; DROOL uses that type from then on. If the scan shows
nothing, DROOL couldn't reach the SSH port: check the address and port and click **Scan again**
(see [Host Won't Connect](Host-Wont-Connect.md)).

The same panel appears on the host's page while a host is waiting for its key, so you can come back
to it later.

## When a host's key changes

A key changes legitimately when a machine is **reinstalled**, when its SSH host keys are
**regenerated**, or when a **different machine** now answers at that address (for example a
replacement server with the same IP). It also changes when someone is impersonating the host. DROOL
can't tell these apart, so it stops and asks you.

The host then shows **Host key changed** with this explanation: *"The host presented a different SSH
key than the one you approved, so DROOL is not connecting to it. If the host was reinstalled or its
keys were rotated, check the new fingerprint on the host and approve it below. Otherwise treat this
as a possible attack."*

What to do:

1. Find out why. Did you or a colleague reinstall the host or regenerate its keys? Was a machine
   replaced?
2. If you know why, check the **new** fingerprint on the host itself (the command above) and compare
   it with the new key DROOL shows on the host's page.
3. If it matches, click **Trust this key** next to the new key. DROOL asks for your password again
   first (see [Re-authentication](Re-authentication.md)), because trusting a changed key is a
   security decision. The old key is kept as *retired*, and DROOL connects again.
4. If you can't explain the change, don't trust the new key. Look at the machine and the network
   first: DROOL keeps refusing to connect, which is the safe state.

Runs that target the host meanwhile fail for it with a host-key error; nothing is sent to it.

## Related

[Adding a Host](Adding-a-Host.md) · [SSH Host Key Changed](SSH-Host-Key-Changed.md) (troubleshooting) ·
[Testing a Connection](Testing-a-Connection.md) · [Jump Hosts](Jump-Hosts.md)
