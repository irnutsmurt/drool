# Jump hosts

Sometimes the controller can't reach a host directly: it's on another VLAN, behind a firewall, or in
a network you only enter through a *bastion* machine. DROOL can then connect **through** another host
you've added, the *jump host*, the same way `ssh -J` does.

For example: if `database-01` (10.10.30.10) were only reachable from inside its network, and a
machine called `bastion-01` (10.10.1.10) were reachable from the controller and could reach
`database-01`, you'd add `bastion-01` first and then set `database-01` to connect through it.

## Before you start

- **The jump host must be in DROOL first**, with its SSH key trusted and **Test connection**
  passing. Add it like any host ([Adding a Host](Adding-a-Host.md)).
- The jump host must allow SSH forwarding (`AllowTcpForwarding`, on by default in OpenSSH) and be
  able to reach the target host's SSH port.
- **One jump host per host.** DROOL doesn't chain jump hosts (A → B → C).
- Changing hosts needs **hosts.write** (administrators).

## Set it up

1. Add the jump host (`bastion-01`). If DROOL should only use it to reach others, and not manage or
   monitor it itself, open **Advanced** in the form and tick **Jump host only**.
2. Add (or **Edit**) the target host (`database-01`) and choose `bastion-01` under **Connect through**.
3. Trust the target's own host key and test the connection. The test's first step, **Reachable**,
   now means "reachable through `bastion-01`".

## How DROOL uses it

- **Both keys are checked.** DROOL verifies the jump host's trusted key *and* the target's, on every
  connection, as with direct connections. A changed key on either side stops the connection.
- **The credentials can differ.** The jump host logs DROOL in with its own SSH user and credential;
  the target with its own.
- **Busy jump hosts are protected:** DROOL opens at most 10 connections through one jump host at a
  time (`engine.per_jump_host_concurrency`); further hosts in a run wait their turn.
- **The firewall knows about it.** When DROOL enforces a firewall on the target, the path *from the
  jump host* is DROOL's protected management path. See [Management Path Protection](Management-Path-Protection.md).
- **Short-lived SSH certificates work through jump hosts** too: each hop signs in the way its own
  host is set up (a stored credential, or a certificate).
- **DROOL's own account works through them.** Setting up [DROOL's own SSH account](Dedicated-SSH-Account.md)
  on the target goes through the jump host, and so does the brand-new connection that proves it;
  the jump host's own account and credentials don't change. A *jump host only* gets no DROOL account.

## Removing a jump host

DROOL refuses to remove a host that others connect through, and names them (*host is the jump host
for: database-01*). First change those hosts' **Connect through**, then remove it.

## When a jump host isn't enough

If a host can only be reached through more than one hop, or not at all from the controller's side,
use a [Shepherd](Installing-a-Shepherd.md#a-host-that-drool-cant-reach-over-ssh) instead: it
connects out to DROOL. Shepherds connect directly to the controller; they don't relay through other
hosts.

## Related

[Adding a Host](Adding-a-Host.md) · [Host Won't Connect](Host-Wont-Connect.md) ·
[Management Path Protection](Management-Path-Protection.md)
