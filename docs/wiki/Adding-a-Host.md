# Adding a host

A *host* is a Linux machine DROOL manages. This page is the reference for adding one and changing
it later. If this is your first host, follow the guided walkthrough instead:
[Your First Host](Your-First-Host.md).

There are two ways to add a host:

- **Over SSH** — **Hosts → Add host**. DROOL connects to the host. This is the usual way, and the
  rest of this page is about it.
- **With a Shepherd from the start** — **Shepherds → Add a host with a Shepherd**. You run a one-line
  installer on the host, and it connects *out* to DROOL. Use this when DROOL can't reach the host
  over SSH at all. See [Installing a Shepherd](Installing-a-Shepherd.md#a-host-that-drool-cant-reach-over-ssh).

## Before you start

- You need to be an **administrator**: adding hosts (**hosts.write**) and trusting their keys
  (**hostkeys.approve**) are administrator permissions. Operators can use hosts once they exist.
- The host needs an account DROOL can log in as, with DROOL's key (or a password) and, for anything
  that changes the system, `sudo` or a root login. [Your First Host](Your-First-Host.md#step-1-create-an-account-on-the-host-for-drool)
  shows how to create it.
- The controller must reach the host's SSH port, directly or through a [jump host](Jump-Hosts.md).
  A jump host must itself be added to DROOL first.
- If the host needs a password (to log in, or for sudo), add it under **Credentials** first
  ([SSH Credentials](SSH-Credentials.md)), so you can pick it in the form.

## What happens when you add a host

**Hosts → Add host** walks through three steps:

1. **Connection** — the form (all fields are described in [Your First Host](Your-First-Host.md#step-3-add-the-host-in-drool)).
   Clicking **Continue** saves the host right away, then asks it for its SSH host keys.
2. **Host key** — you trust the key that belongs to the host, after checking its fingerprint
   ([SSH Host-Key Verification](SSH-Host-Key-Verification.md)). DROOL never connects before this.
3. **Test** — DROOL tests the connection, step by step ([Testing a Connection](Testing-a-Connection.md)).

If you leave the page after step 1, the host stays in DROOL with the status **Needs host key** (or
**Untested** after step 2). Open it from **Hosts** and finish there: its page shows what's missing.

Adding a host changes nothing on the host. DROOL only logs in, reads information, and logs out.

## Common situations

### The account is root

Set **SSH user** to `root` and **Privilege escalation** to `none (connect as root)`. DROOL then runs
everything as root directly. Many distributions forbid root logins over SSH by default
(`PermitRootLogin` in `/etc/ssh/sshd_config`); a separate account with sudo is the safer habit.

### sudo asks for a password

Add a **Sudo password** credential under **Credentials**, then pick it in **Sudo password**. DROOL
sends it to sudo on standard input (never on the command line) and removes it from run output.

### sudo requires a terminal

Some systems configure sudo with `requiretty` (the test then fails at *Privilege Escalation* with
a message about a terminal). Open **Advanced** in the form and tick **Requires a terminal for sudo**.

### The host is only reachable through another machine

Add that other machine first, then choose it in **Connect through**. DROOL supports one jump host
per host, not chains. See [Jump Hosts](Jump-Hosts.md).

### A machine you only use as a jump host

Tick **Advanced → Jump host only**. DROOL uses it to reach other hosts but doesn't manage or monitor
it, and it doesn't count as a managed host.

### The host has no systemd (most containers)

The connection test reports **Init System** as failed and DROOL shows the host as **Unsupported**:
commands, scripts, packages and updates work; service management and reboots don't.

### The address may change

Use a DNS name in **Address** if your network has one for the host, or give the host a fixed address
(a DHCP reservation). If the host moves to a new address, edit it in DROOL. DROOL keeps trusting
the same host *key*, so pointing the address at a different machine can't fool it.

## Changing a host later

Open the host and click **Edit**. The same form opens with every field; click **Save**. Changes
take effect on DROOL's next connection to the host. Run **Test connection** afterwards to check.

Tags and the description can also be changed there. To change which Packs a host belongs to, use
the [Packs](Packs.md) page.

## Many hosts

There's no bulk import in the interface. For each host, the steps are the same: create the account
on the host (the same commands everywhere), install DROOL's key, add it in DROOL, trust its key.
The key-install command is identical for every host that uses the same SSH user.

## Related

[Your First Host](Your-First-Host.md) · [Host Details](Host-Details.md) · [Removing a Host](Removing-a-Host.md) ·
[Host Won't Connect](Host-Wont-Connect.md)
