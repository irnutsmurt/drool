# SSH host key changed

The host shows **Host key changed**, and runs fail with *host key for web-01 changed: expected
SHA256:…, got SHA256:…*. The machine at that address presented a **different SSH host key** from the one
you approved, so DROOL refuses to connect.

**This is DROOL protecting you.** A key changes for harmless reasons (the machine was reinstalled, its
keys were regenerated, a replacement machine took over the address), but also when something is
impersonating your host. DROOL can't tell which, so it stops and asks.

## What to do

1. **Find out why.** Did someone reinstall the host, restore it from an image, or regenerate its SSH
   keys? Was the machine replaced? Did its IP address move to another machine?
2. **Check the new key on the host itself**, from its console or an SSH session you trust:

   ```sh
   ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub
   ```

3. Compare that fingerprint with the new key DROOL shows on the host's **Overview**.
4. **If they match and you know why it changed**, click **Trust this key** next to the new key. DROOL
   asks you to [confirm it's you](Re-authentication.md). The old key is kept as *retired*.
5. **If you can't explain it, don't trust the new key.** Look at the machine and the network first.
   DROOL keeps refusing to connect, which is the safe state.

The full explanation: [SSH Host Key Verification](SSH-Host-Key-Verification.md#when-a-hosts-key-changes).

## After restoring DROOL from a backup

A host whose key changed after the backup shows *Blocked for safety* on **Settings → System →
Restore** with the same check to make ([What Happens After Restore](What-Happens-After-Restore.md)).

## Related

[SSH Host Key Verification](SSH-Host-Key-Verification.md) · [Host Won't Connect](Host-Wont-Connect.md)
