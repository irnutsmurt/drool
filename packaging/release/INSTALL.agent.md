# The DROOL Shepherd (drool-agent)

`drool-agent` is the optional DROOL agent for one CPU architecture. You usually don't need
this archive: the controller installs the Shepherd itself (a host's page → *Install DROOL
agent*, or the install command under *Shepherds → Enrollment*), using the copy shipped with
the controller.

Use it to:

- **Install by hand:** copy `drool-agent` to the host, then
  `sudo ./drool-agent install --preset managed --token-file -` and paste an enrollment token
  from *Shepherds → Enrollment* (then Ctrl-D).
- **Update Shepherds:** Shepherds only install updates signed with a release-signing key you
  hold (it never lives on the controller). Check this download against the release's
  `SHA256SUMS` and its signature, then sign it with your key and upload both files under
  *Shepherds → Updates*:

  ```sh
  drool release sign --key drool-release.key drool-agent
  ```

  `drool release sign` reads the version and architecture from the binary itself.

`drool-agent version` prints the version; `drool-agent status` shows the Shepherd's identity,
certificate, trust, controller addresses and what it may do (`/etc/drool/agent.yaml`).
Third-party licenses: `THIRD_PARTY_NOTICES.txt`.
