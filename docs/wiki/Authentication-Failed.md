# Authentication failed

The host shows **Access denied**, or the connection test fails at **Authentication**: DROOL reached the
host and the host key was right, but the host didn't accept DROOL's login.

## What the message says

| Message | Means |
|---|---|
| *authentication as "drooladmin" failed on web-01* | The host refused the credential. *Check the SSH user, and that the credential's public key is in that user's ~/.ssh/authorized_keys on the host (Credentials shows a one-line install command for DROOL's own key).* |
| *web-01 refused DROOL's SSH certificate for "drooladmin"* | The host is in certificate mode, but doesn't accept the certificate. *The host must trust DROOL's certificate authority (TrustedUserCAKeys) and accept the certificate for this user.* |
| *DROOL couldn't issue an SSH certificate for web-01: …* | DROOL's own certificate authority has a problem. *Check the SSH certificate authority under Credentials.* |

## Steps for a key credential

1. Check the **SSH user** on the host's page (**Actions → Edit host details**). It's the Linux
   account DROOL logs in as, for example `drooladmin`, not your DROOL username.
2. Check that the credential's **public** key is in that user's `~/.ssh/authorized_keys` on the host.
   For DROOL's own key, **Credentials** shows a one-line command to install it ([SSH Credentials](SSH-Credentials.md)).
3. Check the file permissions sshd insists on: the user's home directory and `~/.ssh` not writable by
   others, `~/.ssh` mode `700`, `authorized_keys` mode `600`, owned by the user.
4. Check that sshd allows key logins for that user (`PubkeyAuthentication yes`; no `AllowUsers`/
   `DenyUsers` rule that excludes it). The host's SSH log says why it refused:
   `sudo journalctl -u ssh -n 30` (Debian/Ubuntu) or `sudo journalctl -u sshd -n 30` (RHEL family), on
   the host.
5. **Test connection** on the host's page.

## Steps for a password credential

Check the password (log in by hand with it), and that sshd allows password logins
(`PasswordAuthentication yes`). Keys are safer: consider switching ([SSH Credentials](SSH-Credentials.md)).

## Steps for certificate mode

1. In the host's **Management** section, click **Check CA trust again** on the **SSH access** card.
2. If trust isn't set up (or the host trusts another CA file), see
   [Moving Hosts to SSH Certificates](Moving-Hosts-to-SSH-Certificates.md#1-set-up-trust).
3. If the host maps certificates with `AuthorizedPrincipalsFile`, the **principal** must be listed
   there.

A host in certificate mode is never reached with another key automatically. **Go back to the
credential** switches it back, if its stored credential still works.

## Many hosts at once

If five hosts in a row fail with an authentication error during one run, DROOL pauses the run: *likely
a shared credential or configuration problem* ([Batches and Failure Thresholds](Batches-and-Failure-Thresholds.md#a-safety-stop-you-dont-have-to-set)).
Check the credential they share before resuming.

## Related

[SSH Credentials](SSH-Credentials.md) · [Testing a Connection](Testing-a-Connection.md) · [Sudo Failed](Sudo-Failed.md)
