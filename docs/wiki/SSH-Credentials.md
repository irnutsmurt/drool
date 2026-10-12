# SSH credentials

A *credential* is how DROOL proves who it is when it logs in to a host (an SSH key or a password),
or what it types when sudo asks for a password. Credentials live under **Credentials** in the
sidebar. Their secret part is encrypted as soon as you save it and is never shown again, not even to
administrators.

## The kinds of credential

| Type (in the form) | What it is | When to use it |
|---|---|---|
| *DROOL's own key* (`controller-default`) | An SSH key DROOL created at installation. Every new host uses it unless you choose another. | Most hosts. You install its public half on each host ([Your First Host](Your-First-Host.md#step-2-let-drools-key-log-in)). |
| **Generate new SSH key** | A new key pair made by DROOL, for example one key per group of machines. Only the public half can be copied out. | When you want separate keys you can revoke independently. |
| **Import existing SSH private key** | A key you already have. Paste the private key; if it's encrypted, give the passphrase once (it's used to decrypt and isn't stored). | When hosts already trust a key of yours. |
| **SSH password** | A login password. | Only where keys aren't possible. |
| **Sudo password** | What DROOL types when sudo asks for a password. Not used to log in. | Hosts where `sudo` isn't passwordless. |

Better than any stored key: short-lived **SSH certificates** from DROOL's own certificate authority,
valid for minutes. See [Short-Lived SSH Certificates](Short-Lived-SSH-Certificates.md).

## Before you start

- To **see** credentials (names, types, which hosts use them) and pick them for hosts, you need
  **credentials.use** (administrators and operators).
- To **add** or **delete** them, you need **credentials.manage** (administrators).

## Add a credential

1. Open **Credentials**. Under **Add credential**, choose the **Type**.
2. Give it a **Name** you'll recognise in the host form, for example `media-key` or
   `database-01-sudo`.
3. Depending on the type: nothing more (generate), paste the **Private key** (import), or type the
   **Password**.
4. Click **Add**.

For a generated or imported key, use **Copy public key** in the list and add that line to
`~/.ssh/authorized_keys` of the SSH user on each host that should accept it, the same way as for
DROOL's own key.

Then open each host that should use it, click **Edit**, choose it under **Credential** (or **Sudo
password**), and **Save**. Run **Test connection** to check.

## What DROOL does with secrets

- They're encrypted in DROOL's database with the [master key](Master-Key.md). A backup contains them
  only in that encrypted form.
- Private keys and passwords never appear in the interface, the API, logs or the audit log after you
  save them. The audit log records *that* a credential was added or deleted, and by whom.
- Sudo passwords are sent on sudo's standard input, never as part of a command line (where other
  users of the host could see them in the process list), and are removed from run output.

## Change or remove a credential

The list shows each credential's **Used by** column. A credential that hosts still use can't be
deleted (its **Delete** button is greyed out), and `controller-default` can't be deleted at all.

There's no "edit" for a credential's secret in the interface. To change a password or key, do it
the safe way, one host at a time:

1. Add the new value as a **new** credential.
2. On one host, **Edit** → choose the new credential → **Save** → **Test connection**.
3. When it works, switch the remaining hosts.
4. When **Used by** is empty, **Delete** the old credential.

That way a typo never locks DROOL out of every host at once.

## Related

[Adding a Host](Adding-a-Host.md) · [Authentication Failed](Authentication-Failed.md) ·
[Sudo Failed](Sudo-Failed.md) · [Short-Lived SSH Certificates](Short-Lived-SSH-Certificates.md) ·
[The Master Key](Master-Key.md)
