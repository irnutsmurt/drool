# Rotating the SSH certificate authority

**Rotation** replaces DROOL's [SSH certificate authority](Short-Lived-SSH-Certificates.md) with a new
one, without any host losing access at any point. Rotate if the CA's private key may have been exposed
(for example, a copy of DROOL's database together with its master key went missing), or as routine.

## Before you start

- You need **credentials.manage** (administrators); every CA change asks you to
  [confirm it's you](Re-authentication.md).
- Plan for running the trust step on every host in certificate mode, twice.

## Steps

1. **Introduce the new CA.** **Credentials → SSH certificate authority → Rotate…**, name it (for example
   `drool-ssh-ca-2027`), and confirm. The old CA keeps issuing certificates meanwhile.
2. **Set up trust again** on your hosts (**Set up trust on hosts…**). Hosts now trust **both** CAs.
3. **Start issuing with the new CA.** The button **Start issuing with it** works only once *every host in
   certificate mode has accepted it*.
4. **Retire the old CA.** DROOL asks *Retire …?*: *Its private key is destroyed. Run the trust step on
   hosts afterwards, so they stop trusting it too.* Click **Retire**.
5. **Set up trust once more**, so hosts drop the old CA.

Changed your mind before step 3? **Abandon** the rotation (**Abandon rotation** in DROOL's dialog):
the new CA's private key is destroyed, and the current CA stays in use.

## Good to know

- The CA that is issuing can never be retired: there's always one that works.
- Each step is recorded in the [audit log](Audit-Log.md).

## Related

[Short-Lived SSH Certificates](Short-Lived-SSH-Certificates.md) · [Moving Hosts to SSH Certificates](Moving-Hosts-to-SSH-Certificates.md) ·
[Key Rotation](Key-Rotation.md)
