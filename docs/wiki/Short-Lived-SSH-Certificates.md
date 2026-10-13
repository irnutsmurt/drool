# Short-lived SSH certificates

Normally DROOL signs in to a host with a stored SSH key ([SSH Credentials](SSH-Credentials.md)): a key
that works until someone removes it from `authorized_keys`. With **short-lived SSH certificates**, DROOL
instead creates a **fresh key and certificate for every connection**, valid for only a few minutes, so
it no longer needs a long-lived login key for each host. What stays long-lived is DROOL's SSH
certificate authority: its private key is on the controller, encrypted with the
[master key](Master-Key.md), and whoever had both could sign certificates your hosts accept. Protect
the controller and the master key accordingly.

## How it works

- DROOL has its own **SSH certificate authority** (CA). Each host is told, once, to trust certificates
  signed by it.
- For every connection to a host in *certificate mode*, DROOL creates a new Ed25519 key in memory, signs
  a certificate for the host's login user only (or a *principal* you set), valid from 5 minutes before to
  10 minutes after it's issued, uses it to sign in, and forgets it.
- The host's OpenSSH checks the signature and enforces the expiry. Certificates and their keys are never
  stored or logged.
- Because sshd checks a certificate only when a connection signs in, DROOL reuses such a connection only
  until 30 seconds before its certificate expires (about 9½ minutes), then connects again with a new one.
  An operation already running finishes on its connection.
- Every certificate issued is recorded in the [audit log](Audit-Log.md): host, principal, serial,
  validity and CA fingerprint. The host's sshd log shows the same `ID drool:…`.
- The CA's private key is stored encrypted with the [master key](Master-Key.md).

## Before you start

- You need **credentials.manage** (administrators), and DROOL asks you to
  [confirm it's you](Re-authentication.md).
- The hosts need OpenSSH with a `sshd_config.d` drop-in directory (current Debian, Ubuntu, RHEL-family
  and Fedora releases have it).
- Keep each host's current credential until certificates work: it's your way back.

## Create DROOL's SSH certificate authority

1. Open **Credentials**.
2. In the **SSH certificate authority** card, click **Create certificate authority…**.
3. Give it a **Name** (*shown in certificates' key ids and in the public key's comment*), for example
   `drool-ssh-ca`, and create it.

The card then shows the CA (**issuing**), its fingerprint, how many certificates it has issued,
**Copy public key**, and how many hosts sign in with certificates.

![Credentials → SSH certificate authority: drool-ssh-ca, issuing, with its fingerprint and 3 certificates issued; Copy public key; "1 host signs in with certificates. Certificates are valid from 5 minutes before until 10 minutes after they are issued."; Set up trust on hosts…, Rotate… and Trusting it by hand](images/credentials-ssh-ca.png)

Next: [Moving Hosts to SSH Certificates](Moving-Hosts-to-SSH-Certificates.md).

## Related

[Moving Hosts to SSH Certificates](Moving-Hosts-to-SSH-Certificates.md) · [SSH CA Rotation](SSH-CA-Rotation.md) ·
[SSH Credentials](SSH-Credentials.md)
