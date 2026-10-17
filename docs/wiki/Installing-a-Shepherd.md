# Installing a Shepherd

A Shepherd is DROOL's optional agent (the program `drool-agent`). Once installed, it connects *out*
to DROOL, reports monitoring data continuously and runs work within limits the host itself sets.
Not sure you need one? Read [Understanding SSH vs Shepherds](SSH-vs-Shepherds.md) first.

There are three ways to install one:

| Way | Use it when | Needs SSH from DROOL |
|---|---|---|
| [From the host's page](#from-the-hosts-page-easiest) | The host is already in DROOL over SSH | Yes, once |
| [With a one-line installer](#a-host-that-drool-cant-reach-over-ssh) | DROOL can't reach the host over SSH at all | No |
| [Convert to Shepherd](#many-ssh-hosts-at-once) on the Monitoring page | Many SSH hosts at once | Yes, once |

## Before you start

- **The host can reach DROOL's Shepherd port.** Shepherds connect to the address DROOL *advertises*:
  the address you open DROOL with in your browser, on the Shepherd port, **9443** unless you chose
  another. Both install dialogs show it before anything changes, as **Shepherds will connect to: …**.
  In the examples that's `drool.example.net:9443`. On the host, `nc -vz drool.example.net 9443` must
  succeed.
  - In a container, DROOL listens on 9443 inside the container. If the machine publishes it as another
    port (`2043:9443`), DROOL must know that port too: DROOL's `compose.yaml` takes care of it from one
    number ([Docker Compose](Install-With-Docker-Compose.md#use-other-ports),
    [Synology](Install-On-a-Synology-NAS.md#step-4-check-the-ports),
    [Podman](Install-With-Podman.md#if-youll-use-shepherds)).
  - If your hosts reach DROOL by another name or address than the one in your browser (or you opened
    it as `localhost`), set `DROOL_AGENT_ADVERTISE` first
    ([Docker Compose](Install-With-Docker-Compose.md#if-youll-use-shepherds),
    [Podman](Install-With-Podman.md#if-youll-use-shepherds), [Configuration](Configuration.md)).
  - If the controller's machine has a firewall, the Shepherd port must be open there.
- **The host is Linux on amd64 or arm64.** (Other architectures stay on SSH.)
- **You're an administrator** (installing needs **agents.enroll**).
- **Choose a preset**: how much DROOL may do through the Shepherd. The host keeps this limit even if
  DROOL asks for more. See [Shepherd Capability Presets](Shepherd-Capability-Presets.md).
- For the first way (from the host's page) additionally: the host is in DROOL, its SSH key is
  trusted, **Test connection** passes, and the SSH account can use sudo, because installing writes
  to `/etc` and creates a system service ([Your First Host](Your-First-Host.md)).

## From the host's page (easiest)

1. Open the host (in the examples, `media-02`) and its **Management** section.
2. On the **Shepherd (DROOL agent)** card, click **Install Shepherd (DROOL agent)**. The dialog says
   where the Shepherd will connect, for example **Shepherds will connect to: drool.example.net:9443**.
   If the host can't reach that address and port, cancel and fix it first
   ([Before you start](#before-you-start)).

   ![The "Install a Shepherd (DROOL agent) on web-01" dialog: how the install works, the address Shepherds connect to, the Monitor, Managed and Full presets and what each allows (Full marked root-equivalent), the optional release-signing key, and Cancel and Install with the managed preset in its footer](images/shepherd-install-dialog.png)

3. Choose a preset. **Managed** is selected by default: services, reboots and packages from the
   repositories already configured on the host, but no shell commands and no file access. Pick
   **Full** if you want to run commands and scripts and use the file manager through the Shepherd;
   **Monitor** if it should only report.
4. Leave **Also trust a release-signing key** empty unless you sign your own Shepherd builds
   ([Updating Shepherds](Updating-Shepherds.md)).
5. Click **Install with the managed preset** (the button names the preset you chose). It can take a
   few minutes.

**What DROOL does:**

1. Copies the `drool-agent` program to the host over the SSH connection whose key you trusted.
2. Writes the preset's limits to `/etc/drool/agent.yaml`. If that file already exists, DROOL keeps
   it: the host's own limits always win.
3. Creates a single-use enrollment token and enrolls the Shepherd with it. The Shepherd creates its
   own private key on the host; the key never leaves the host.
4. Starts the `drool-agent` service, which starts again at boot.
5. Switches the host to **Managed via: Shepherd** with **Fallback: SSH**.

The dialog then says **The agent is installed and connected.** with the installer's output; click
**Done**. If it failed, the dialog shows the error and nothing is switched over: the host stays on
SSH.

Afterwards the host's **Management** section looks like this:

![media-01's Management section: Connection (managed via Shepherd, connected, SSH as fallback only while the agent is unreachable, operations through the Shepherd; Test connection, Turn off SSH fallback, Manage over SSH only); the Shepherd card (its version, compatible, connected, the certificate's expiry, Shepherd details, Uninstall Shepherd… and Revoke Shepherd identity); SSH access; Membership; and the Danger zone](images/host-management-shepherd.png)

## A host that DROOL can't reach over SSH

For a host behind NAT, in another network, or a laptop that moves around, use the one-line installer.
The host needs only outgoing access to DROOL: the web port (8080) to download, the Shepherd port (9443)
to connect, or the ports you chose instead.

1. **Open DROOL by the address your hosts can reach** (for example `https://drool.example.net:8080`,
   not `https://localhost:8080`): the installer command uses the address in your browser.
2. Go to **Shepherds → Add a host with a Shepherd** (or, for a host that's already in DROOL,
   **Manual install…** on the **Shepherd (DROOL agent)** card of its **Management** section).
3. Choose a preset and click **Create token and installer**. The dialog says where the Shepherd will
   connect (**Shepherds will connect to: …**): the host must be able to reach it.
4. Copy the command. It is shown **only once** and expires after one hour. It looks like this:

   ```sh
   curl -fsSL -k --pinnedpubkey 'sha256//RXhhbXBsZS1vbmx5Lm5vdC1hLXJlYWwta2V5LWhhc2g=' 'https://drool.example.net:8080/agent/install.sh' | sudo sh -s -- --token 'drool-enroll-1.eyJleGFtcGxlIjp0cnVlfQ.ExampleOnly' --preset managed
   ```

5. Run it on the host, as a user who can use `sudo`. Paste it whole, quotes included.

About that command: `-k` tells `curl` not to check DROOL's certificate against public authorities
(it's self-signed), and `--pinnedpubkey` instead requires *exactly* DROOL's key. So the download
can't come from anyone else, which is stricter than normal certificate checking. The token carries
the fingerprint of DROOL's Agent Root CA, so the Shepherd trusts only your DROOL.

A Shepherd enrolled this way becomes a **new host** in DROOL, named after the machine's hostname
(with `-2`, `-3`… added if that name is already taken), unless the token was created for a host that
already exists (**Manual install…** in that host's **Management** section). DROOL never decides that
two hosts are the same because their names or addresses match.

**Without `curl | sudo sh`:** under the command's **Details**, DROOL shows the token on its own. Copy
`drool-agent-linux-amd64` (or `-arm64`) to the host and run, as root:

```sh
sudo ./drool-agent-linux-amd64 install --preset managed --token-file -
```

Paste the token, press Enter, then **Ctrl-D**.

Unused tokens are listed under **Shepherds → Enrollment → Enrollment tokens**, where you can
**Revoke** them.

## Many SSH hosts at once

On **Monitoring**, select the hosts and choose **Convert to Shepherd**. DROOL first shows a preview
counting the hosts that can be converted, are already converted, aren't supported or aren't reachable.
The conversion then runs like a careful rollout: one host first, then batches, pausing at the first
failure. A host where anything fails stays exactly as it was, on SSH.

## If installing fails

| What you see | Likely cause | What to do |
|---|---|---|
| The install times out or says *connection refused*, or the Shepherd never shows as connected | The host can't reach the address in **Shepherds will connect to** | On the host: `nc -vz drool.example.net 9443` (with that address and port). Fix the name (`DROOL_AGENT_ADVERTISE`), DNS, or a firewall in between |
| *connection refused* on port 9443, after you published DROOL's Shepherd port as another port | DROOL gave the host the port inside its container | [Below](#shepherd-installation-says-connection-refused-on-port-9443-after-changing-the-docker-port) |
| An error about sudo | The SSH account can't become root | [Sudo Failed](Sudo-Failed.md) |
| *token expired* or *already used* | Tokens last one hour and work once | Create a new one |
| An architecture error | The host isn't amd64 or arm64 | Keep the host on SSH |

Whatever fails, the host stays as it was: DROOL stops the Shepherd it installed and the host remains
managed over SSH (*Rolled back: the agent service was stopped; the host remains managed over SSH.*).
Fix the cause and install again.

See also [Shepherd Offline](Shepherd-Offline.md).

### Shepherd installation says connection refused on port 9443 after changing the Docker port

You published DROOL's Shepherd port as another port on the machine, for example `2043:9443` in
`compose.yaml` or in Synology's Container Manager, and installing a Shepherd fails like this:

```text
Installing and enrolling as root (agent endpoint 10.10.0.10:9443)...
… dial tcp 10.10.0.10:9443: connect: connection refused
Rolled back: the agent service was stopped; the host remains managed over SSH.
```

**Why:** inside its container DROOL listens on 9443, and Docker forwards the machine's port (here
2043) to it. DROOL can't see that forwarding from inside its container. DROOL builds before
2026-10-13 gave Shepherds the port DROOL listens on, 9443, which nothing on the machine answers. The
installation then rolled back as it should: the Shepherd service was stopped and the host is still
managed over SSH. There is nothing to clean up on the host.

**Fix:** use a current DROOL image with the current `compose.yaml`, and set the Shepherd port there,
once:

- **Synology Container Manager:** replace the project's YAML with the current file, set your port on
  the `shepherds:` line, and update DROOL's image
  ([Change a port later](Install-On-a-Synology-NAS.md#change-a-port-later),
  [Update DROOL](Install-On-a-Synology-NAS.md#update-drool)).
- **Docker Compose:** in `~/drool`, download `compose.yaml` again
  ([step 2](Install-With-Docker-Compose.md#step-2-download-composeyaml)), set your port on its
  `shepherds:` line ([Use other ports](Install-With-Docker-Compose.md#use-other-ports)), then
  `docker compose pull` and `docker compose up -d`. Copy any other changes you made, such as
  `DROOL_AGENT_ADVERTISE`, into the new file.
- **Your own compose file, or Podman:** publish the port as before and add the same port as
  `DROOL_AGENT_PUBLIC_PORT`, for example `DROOL_AGENT_PUBLIC_PORT: "2043"` under `environment:`
  (Podman: `Environment=DROOL_AGENT_PUBLIC_PORT=2043`). Don't change `DROOL_AGENT_PORT`: it's where
  DROOL listens inside the container, and stays 9443.

Then open the host's **Management** section and click **Install Shepherd (DROOL agent)** again. The
dialog should now say **Shepherds will connect to: 10.10.0.10:2043**, and the installer
`(agent endpoint 10.10.0.10:2043)`. If the dialog has no **Shepherds will connect to** line, DROOL's
image is still an old one.

## Next

**[Host Monitoring](Host-Monitoring.md)**: open the host's monitoring page. It now says *Continuous: the Shepherd samples every 10 s* instead of polling over SSH every 5 minutes.

## Related

[Shepherd Capability Presets](Shepherd-Capability-Presets.md) · [SSH Fallback](SSH-Fallback.md) ·
[Updating Shepherds](Updating-Shepherds.md) · [Shepherd PKI](Shepherd-PKI.md) ·
[Uninstalling a Shepherd](Removing-a-Host.md#uninstalling-a-shepherd)
