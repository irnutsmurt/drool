# The DROOL Shepherd (drool-agent)

`drool-agent` is the optional DROOL agent for one CPU architecture. You usually don't need
this archive: the controller installs the Shepherd itself (a host's page → *Install DROOL
agent*, or the install command under *Shepherds → Enrollment*), using the copy shipped with
the controller.

Use it to:

- **Install by hand:** copy `drool-agent` to the host, then
  `sudo ./drool-agent install --preset managed --token-file -` and paste an enrollment token
  from *Shepherds → Enrollment* (then Ctrl-D).
- **Not for updates:** Shepherds update themselves when DROOL is updated, to the release's own
  Shepherd, signed by the DROOL project (*Shepherds → Updates*). Only for a build you sign
  yourself: `drool release sign --key my-release.key drool-agent`, then upload both files
  under *Shepherds → Updates → Advanced settings → Custom releases*.

`drool-agent version` prints the version; `drool-agent status` shows the Shepherd's identity,
certificate, trust, controller addresses and what it may do (`/etc/drool/agent.yaml`).
DROOL's license (GNU GPL version 3): `LICENSE`. Third-party licenses: `THIRD_PARTY_NOTICES.txt`.
