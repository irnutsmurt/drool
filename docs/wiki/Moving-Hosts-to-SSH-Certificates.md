# Moving hosts to SSH certificates

Once DROOL has its [SSH certificate authority](Short-Lived-SSH-Certificates.md), each host needs two
steps: **trust** the CA, then **use certificates**. DROOL checks each step with a brand-new connection
before relying on it, so a host is never stranded. In the examples, Alex moves `web-01`.

## Before you start

- You need **credentials.manage** (administrators); switching a host asks you to
  [confirm it's you](Re-authentication.md).
- The host must be reachable with its current credential, and sudo must work (the trust step changes
  sshd's configuration).
- If DROOL should sign in with [its own SSH account](Dedicated-SSH-Account.md), set that up first:
  the certificates then name that account. A host already in certificate mode goes back to its
  credential for the account's setup, then switches to certificates again.

## 1. Set up trust

For several hosts: **Credentials → SSH certificate authority → Set up trust on hosts…**, tick the
hosts, and start (*one host first; then 5 at a time, pausing after 2 failures*). For one host: in its
**Management** section, on the **SSH access** card, click **Set up CA trust**.

This is an ordinary run, *Trust DROOL's SSH certificate authority*. On each host it:

1. writes DROOL's CA public key(s) to `/etc/ssh/drool_user_ca.pub`;
2. points `TrustedUserCAKeys` at it in `/etc/ssh/sshd_config.d/40-drool-user-ca.conf`;
3. checks the configuration with `sshd -t` and `sshd -T`;
4. reloads sshd (open sessions stay up);
5. proves it works with a brand-new connection that offers **only** a certificate.

If sshd rejects the result, the run puts the previous files back. A host that already trusts **another**
CA file is left alone: add DROOL's key to that file yourself (**Trusting it by hand** in the card shows
the key and the line).

The host's **SSH access** card then shows **CA trust: accepted**.

## 2. Use certificates

1. In the host's **Management** section, on the **SSH access** card, click **Use certificates…**.
2. Leave **Principal** empty to use the host's SSH user (for example `drooladmin`). Set it only if the
   host maps certificates with `AuthorizedPrincipalsFile`.
3. Click **Check and switch**. DROOL connects once more with a fresh certificate, and switches only if
   that works.

![web-01's Management section: Connection (managed via SSH) and SSH access — drooladmin@10.10.40.21:22, credential controller-default (kept as a way back; DROOL signs in with certificates), signs in with a fresh certificate from DROOL's SSH CA for each connection, CA trust accepted, with Check CA trust again and Go back to the credential; beside them the Shepherd card with Install Shepherd (DROOL agent), and Membership](images/host-management-certificate.png)

From now on DROOL doesn't use the host's stored credential: the card says *kept as a way back; DROOL
signs in with certificates*.

## Going back

**Go back to the credential** on the host's **SSH access** card (under **Management**) switches back
to the stored credential. Keep that credential (and its key in the host's `authorized_keys`) as your
way back. If you remove it, DROOL warns that its CA is then the only way in.

## Jump hosts

A [jump host](Jump-Hosts.md) can use certificates too: trust and switch it like any host. Connections
through it then use a certificate for each hop.

## Related

[Short-Lived SSH Certificates](Short-Lived-SSH-Certificates.md) · [SSH CA Rotation](SSH-CA-Rotation.md) ·
[Authentication Failed](Authentication-Failed.md)
