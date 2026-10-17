# Release keys

`allowed_signers` holds the public half of the **DROOL release key**, in OpenSSH's
allowed-signers format. It says which key may sign a release's `SHA256SUMS`, so that people who
download DROOL can tell an official release from anything else:

```sh
ssh-keygen -Y verify -f allowed_signers -I release@drool -n drool-release -s SHA256SUMS.sig < SHA256SUMS
```

The private half never enters this repository, CI or a DROOL controller. It lives offline with the
release manager, who signs each release by hand ([`RELEASING.md`](../../RELEASING.md)).

This key is not any of DROOL's runtime keys, and it doesn't replace them:

- The **DROOL Shepherd release key** signs official Shepherd updates. Its public half is in
  [`internal/release/official_keys.txt`](../../internal/release/official_keys.txt), built into DROOL and every
  Shepherd; its private half is offline with the release manager too. The DROOL release key only says that a
  download is what the DROOL project built; the Shepherd release key says which Shepherd binaries a host may
  update to by itself.
- **Your own Shepherd release-signing keys** (`drool release keygen`) are for Shepherd builds you sign
  yourself. Each host trusts them locally, and a host can refuse official releases altogether
  (`updates: {official: deny}` in its agent.yaml).
- The **Agent Root CA**, the **SSH user CA** and the **master key** are created by each controller
  and never leave it except into your own offline storage.
