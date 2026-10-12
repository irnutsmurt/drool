# Sudo failed

DROOL logged in, but couldn't become root with `sudo`. Package, update, service and reboot operations
always need root, and so do commands with **Run as root (sudo)**.

## What the message says

| Message | Means | Fix |
|---|---|---|
| *sudo requires a password* | The user's sudo rule asks for a password, and DROOL has none for this host | *Set a sudo password credential for this host, or configure NOPASSWD for the SSH user.* |
| *sudo rejected the password* | The sudo password credential is wrong | *Check the host's sudo password credential.* |
| *the SSH user may not use sudo* | The user isn't in sudoers, or not for this command | *Add the user to sudoers or choose a different user.* |
| *sudo requires a terminal (requiretty)* | sudo is configured to work only in a terminal | *Edit the host and enable "Requires a terminal for sudo".* |
| *sudo is not installed* | No `sudo` on the host | *Install sudo or log in as root.* |
| *command timed out or was cancelled* (at **Privilege Escalation**) | sudo didn't answer in time, often on a very busy or just-booted host | Test again in a few minutes ([example](Testing-a-Connection.md#reading-a-failed-test)) |

## Passwordless sudo for DROOL's user

The simplest setup: let the user DROOL logs in as use sudo without a password (the same commands as in
[Your First Host](Your-First-Host.md#step-1-create-an-account-on-the-host-for-drool)). On the host, logged
in as yourself:

```sh
echo 'drooladmin ALL=(ALL) NOPASSWD:ALL' | sudo tee /etc/sudoers.d/drooladmin
sudo chmod 0440 /etc/sudoers.d/drooladmin
sudo visudo -cf /etc/sudoers.d/drooladmin
```

The last command checks the file and should print `parsed OK`; fix any error it reports before logging
out (a broken sudoers file can stop `sudo` working for everyone). Replace `drooladmin` with your SSH
user.

## Or: a sudo password credential

Under **Credentials**, add a *sudo password* credential, and choose it on the host's page (**Edit**).
DROOL sends the password to sudo on standard input, never on the command line, and removes it from run
output ([SSH Credentials](SSH-Credentials.md)).

## Related

[Administrator Privileges](Administrator-Privileges.md) · [Testing a Connection](Testing-a-Connection.md) ·
[Authentication Failed](Authentication-Failed.md)
