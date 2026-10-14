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
- You need an account on the host that can sign in over SSH (with a password or a key) and use
  `sudo`, or `root`. By default DROOL uses it only to create an account of its own and then forgets
  what you typed ([DROOL's Own SSH Account](Dedicated-SSH-Account.md)); or it keeps using it, if you
  choose **Use this SSH account**.
- The controller must reach the host's SSH port, directly or through a [jump host](Jump-Hosts.md).
  A jump host must itself be added to DROOL first.
- With **Use this SSH account**, a password the host needs (to log in, or for sudo) must be stored
  under **Credentials** first ([SSH Credentials](SSH-Credentials.md)), so you can pick it in the
  form. For DROOL's own account you type them in the form instead.

## What happens when you add a host

**Hosts → Add host** walks through four steps (three with **Use this SSH account**):

1. **Connection** — the form (all fields are described in [Your First Host](Your-First-Host.md#step-1-add-the-host-in-drool)),
   including how DROOL signs in from now on: **Create a dedicated DROOL SSH account** (recommended)
   or **Use this SSH account**. Clicking **Continue** saves the host right away, then asks it for
   its SSH host keys.
2. **Host key** — you trust the key that belongs to the host, after checking its fingerprint
   ([SSH Host-Key Verification](SSH-Host-Key-Verification.md)). DROOL never connects before this.
3. **Test** — DROOL tests the connection, step by step ([Testing a Connection](Testing-a-Connection.md)).
4. **DROOL account** — DROOL shows what it will create, and after you confirm, creates its account,
   proves it on a brand-new connection, switches to it and forgets the setup credentials
   ([DROOL's Own SSH Account](Dedicated-SSH-Account.md)).

If you leave the page early, the host stays in DROOL with the status **Needs host key** (or
**Untested** after step 2). Open it from **Hosts** and finish there: its page shows what's missing,
including **Set up a dedicated DROOL SSH account** if you left before step 4.

Steps 1 to 3 change nothing on the host: DROOL only logs in, reads information, and logs out. Step 4
creates DROOL's account, and only once you've confirmed it.

## Common situations

### The account is root

For DROOL's own account, enter `root` as the **Existing SSH account**: DROOL sets itself up as root
directly, and its own account then uses sudo. With **Use this SSH account**, set **SSH user** to
`root` and **Privilege escalation** to `none (connect as root)`. Many distributions forbid root
logins over SSH by default (`PermitRootLogin` in `/etc/ssh/sshd_config`); an account with sudo is
the safer habit.

### sudo asks for a password

For DROOL's own account, type it in **Sudo password**: it's needed only for the setup, since DROOL's
account doesn't need one. With **Use this SSH account**, add a **Sudo password** credential under
**Credentials**, then pick it in **Sudo password**. Either way DROOL sends it to sudo on standard
input (never on the command line) and removes it from run output.

### sudo requires a terminal

Some systems configure sudo with `requiretty` (the test then fails at *Privilege Escalation* with
a message about a terminal). Open **Advanced** in the form and tick **Requires a terminal for sudo**.

### The host is only reachable through another machine

Add that other machine first, then choose it in **Connect through**. DROOL supports one jump host
per host, not chains. See [Jump Hosts](Jump-Hosts.md).

### A machine you only use as a jump host

Tick **Advanced → Jump host only**. DROOL uses it to reach other hosts but doesn't manage or monitor
it, and it doesn't count as a managed host. It keeps signing in with the account you enter: DROOL
doesn't create an account with sudo where it doesn't manage anything.

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
**Change…** next to **Packs** on the host's page, or the Pack's own page ([Packs](Packs.md#add-and-remove-hosts)).

## Many hosts

There's no bulk import in the interface. For each host, the steps are the same: add it with an
account you have there, trust its key, and let DROOL create its own account. Nothing needs to be
prepared on the hosts beforehand.

## Next

**[Running a Command](Running-a-Command.md)**: try something harmless on the new host, such as `df -h`.

## Related

[Your First Host](Your-First-Host.md) · [DROOL's Own SSH Account](Dedicated-SSH-Account.md) · [Host Details](Host-Details.md) ·
[Removing a Host](Removing-a-Host.md) · [Host Won't Connect](Host-Wont-Connect.md)
