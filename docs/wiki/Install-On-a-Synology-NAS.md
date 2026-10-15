# Install DROOL on a Synology NAS

This page installs DROOL on a Synology NAS with **Container Manager**, entirely in DSM's web
interface: no SSH, no terminal and no commands. You copy one file from GitHub into Container Manager,
check two port numbers, and click through. It takes about ten minutes.

At the end you'll have:

- DROOL running as a Container Manager project, which starts again by itself after the NAS restarts;
- a one-time **setup code** (six digits) from DROOL's log in Container Manager;
- DROOL's **Set up DROOL** screen open in your browser.

On any other Linux machine with Docker, use [Docker Compose](Install-With-Docker-Compose.md)
instead: it's the same file, run from the command line.

The examples use a NAS at 10.10.0.10 ([About the Examples](About-the-Examples.md)). Wherever you see
10.10.0.10, use your NAS's address: the one you open DSM with.

## Before you start

- **DSM 7.2 or newer, with Container Manager.** If it isn't installed yet, install **Container
  Manager** from **Package Center**. If Package Center doesn't offer it, your model can't run
  containers. DROOL's image exists for Intel and AMD (amd64) and 64-bit ARM (arm64) models; Container
  Manager picks the right one.
- **A DSM account in the administrators group.**
- **Internet access** from the NAS: Container Manager downloads DROOL's image (under 100 MB) from
  GitHub Container Registry (`ghcr.io`). No account is needed.
- **Two free ports on the NAS:** one for DROOL's web interface (8080 unless you choose another) and
  one for Shepherds, DROOL's optional agents (9443 unless you choose another). DSM itself uses 5000
  and 5001, and Web Station 80 and 443, so the defaults are usually free.

This installs DROOL's **development image**, `ghcr.io/irnutsmurt/drool:main`: the newest DROOL code
that passed DROOL's automated tests, not a stable release, because DROOL has none yet
([the difference](Installing-DROOL.md#development-image-or-release)). Keep backups.

## Step 1: Create the project

1. Open **Container Manager** from DSM's main menu.
2. Click **Project** on the left, then **Create**.

A window opens to describe the new project. Keep it open for steps 2 to 4.

## Step 2: Name the project and choose its folder

- **Project name:** `drool`. Container Manager names DROOL's container after the project
  (`drool-drool-1`).
- **Path:** the folder where Container Manager keeps the project's YAML file. Choose a new, empty
  folder named `drool` inside the **docker** shared folder (Container Manager created **docker** when
  it was installed). If `drool` doesn't exist yet, create it in **File Station** (open **docker**,
  then **Create → Create folder**) while the project window stays open. Then select
  **docker → drool** as the path.

DROOL's own data (its database, keys and settings) doesn't go into that folder: Container Manager
keeps it in a Docker volume named `drool_drool-data`, which stays when the container is stopped,
rebuilt or updated. DROOL's YAML gives the volume that name whatever the project is called
([Compose Data Volume](Compose-Data-Volume.md)).

## Step 3: Paste DROOL's YAML

1. Set **Source** to **Create docker-compose.yml** (newer versions of Container Manager say
   **Create compose.yml**). A text box appears.
2. In a new browser tab, open DROOL's `compose.yaml`:
   [raw.githubusercontent.com/irnutsmurt/drool/main/compose.yaml](https://raw.githubusercontent.com/irnutsmurt/drool/main/compose.yaml).
   It shows as plain text, starting with `# DROOL with Docker Compose.`
3. Select all of it (Ctrl+A, or Cmd+A on a Mac), copy it, and paste it into Container Manager's text
   box.

The YAML names DROOL's image on GitHub, `ghcr.io/irnutsmurt/drool:main`, so Container Manager knows
what to download. You don't need a `.env` file or any other file.

(Or download the file with your browser instead, choose **Upload docker-compose.yml**, and pick it.)

## Step 4: Check the ports

The YAML starts, after its comments, with the only two numbers you might change:

```yaml
x-drool-ports:
  web: &web-port 8080
  shepherds: &shepherd-port ${DROOL_AGENT_PUBLIC_PORT:-9443}
```

| | Where | What it is |
|---|---|---|
| **Web port** | `8080` on the `web:` line | The port on your NAS where your browser opens DROOL: `https://10.10.0.10:8080` |
| **Shepherd port** | `9443` on the `shepherds:` line (the number after `:-`) | The port on your NAS that Shepherds, on your other machines, connect to. DROOL tells every Shepherd this port |
| **Container ports** | `target: 8080` and `target: 9443`, further down | Where DROOL listens *inside* its container. They never change: Container Manager forwards the NAS's two ports to them |

If the defaults are free on your NAS, change nothing and go to step 5.

**To use another port,** change only the number on its line at the top. For example, to make
Shepherds connect to port 2043 on the NAS, change `9443` on the `shepherds:` line to `2043`:

```yaml
  shepherds: &shepherd-port ${DROOL_AGENT_PUBLIC_PORT:-2043}
```

That one number does both jobs: the NAS's port 2043 is forwarded to DROOL's 9443 inside the
container (`2043:9443`), and DROOL tells Shepherds to connect to `10.10.0.10:2043`. Inside the
container DROOL still listens on 9443. Leave the `ports:` and `environment:` lines further down as
they are: they take their numbers from these two lines. The web port works the same way: `8443`
instead of `8080` on the `web:` line, and you open `https://10.10.0.10:8443`.

## Step 5: Deploy

1. Click **Next**.
2. On **Web portal settings**, don't set up a web portal through Web Station: DROOL serves its own
   web interface over HTTPS. Click **Next**.
3. Check the summary and click **Done**.

Container Manager downloads DROOL's image, creates the container and starts it, and shows its
progress. It should end with **Exit Code: 0**. The project **drool** is then listed under
**Project** as running, with one container, `drool-drool-1`.

If it stops with *port is already allocated* or *address already in use*, another program on the
NAS already uses that port: choose another web or Shepherd port
([change a port](#change-a-port-later)).

## Step 6: Find the setup code

When DROOL starts, it writes a short **startup message** to its log, with the one-time setup code.

1. In Container Manager, click **Container** on the left.
2. Select **drool-drool-1** and click **Details**.
3. Open the **Log** tab.

Most lines start with `{`: those are DROOL's ordinary log entries, which you can ignore. Look for the
startup message, between two rows of `=`:

```text
================================================================
 DROOL is ready.

 Open:
   https://<this-host's-address>:8080/setup

 Setup code:
   482913

 Single use. Expires 15:25 UTC (in 60 min).
 New code: restart, or run `drool setup code`.
================================================================
```

- **`Setup code:`** is followed by the six digits. **`482913` is only this page's example: use the
  code your own DROOL wrote.** It works once, for 60 minutes; the time is in UTC.
- **`Open:`** is the setup page's address. DROOL can't see your NAS's address from inside its
  container, so it writes `<this-host's-address>`: you'll use your NAS's address in step 7. It shows
  your web port (`:8080`, or the one you chose).
- **The code expired, or you can't find it?** In **Container**, select **drool-drool-1** and restart
  it (**Action → Restart**). Every start before the first administrator exists writes a new code, and
  the previous one stops working, so always use the newest. It appears as a new startup message in
  the **Log** tab.

## Step 7: Open DROOL

In your browser, go to `https://`, your NAS's address, `:`, the web port and `/setup`. If you open DSM
at `http://10.10.0.10:5000`, that's:

```text
https://10.10.0.10:8080/setup
```

Type the `https://` and the port. The browser warns that the connection isn't private. That's
expected: DROOL made its own certificate, which your browser can't check yet. Continue to the site
(Chrome: **Advanced → Proceed to …**; Firefox: **Advanced → Accept the Risk and Continue**).

You should now see **Set up DROOL**. Continue with [First Login](First-Login.md#2-create-the-administrator):
enter the setup code and create your administrator account.

If the browser can't connect: check the address and the port, and that the project is running. If
you turned on DSM's firewall (**Control Panel → Security → Firewall**), allow the web port and the
Shepherd port there. More: [Setup Problems](Setup-Problems.md#the-browser-cant-connect).

## Shepherds

Shepherds connect to the address you open DROOL with in your browser, on the Shepherd port from
step 4. Before you install one, the install dialog shows exactly where it will connect, for example:

> Shepherds will connect to: 10.10.0.10:2043

Your other machines must be able to reach that address and port. If they reach the NAS by another
name or address (a DNS name, a VPN), tell DROOL that name: in the project's YAML
([how to edit it](#change-a-port-later)), under `environment:`, change

```yaml
      # DROOL_AGENT_ADVERTISE: drool.example.net
```

to your name, without the `#`:

```yaml
      DROOL_AGENT_ADVERTISE: nas.example.net
```

DROOL adds the Shepherd port by itself. Keep the spaces at the start of the line: it lines up with
`DROOL_AGENT_PUBLIC_PORT` above it.

## Change a port later

For example, to make Shepherds use port 2043 on a DROOL that's already running:

1. In Container Manager, click **Project**, select **drool**, and choose **Action → Stop**.
2. Open the project and its **YAML Configurations** tab.
3. Change the number on the `web:` or `shepherds:` line at the top, as in [step 4](#step-4-check-the-ports),
   and save.

   If your YAML doesn't start with `x-drool-ports:` (after its comments), it's from before
   2026-10-13. Replace all of it with the current file ([step 3](#step-3-paste-drools-yaml)) first,
   then change the number. Copy any other changes you made, such as `DROOL_AGENT_ADVERTISE`, into
   the new one. If your project isn't called `drool`, read [Compose Data Volume](Compose-Data-Volume.md#synology-container-manager)
   before you build it: the current YAML would otherwise start DROOL with a new, empty volume.
4. Choose **Action → Build**. Container Manager creates DROOL's container again with the new ports.
   If the project doesn't show as running afterwards, choose **Action → Start**.

DROOL's data stays, and so does its setup: if you already created your administrator account, sign
in as before.

**Change the Shepherd port before you install Shepherds.** A Shepherd that's already installed keeps
connecting to the port it was given at installation and goes offline when that port disappears.
Putting the old number back brings it back.

**Never choose Action → Clean for DROOL's project.** Synology describes it as removing the project's
volumes too, and all of DROOL's data is in one.

## Update DROOL

**Action → Build** doesn't download a newer image by itself, and Container Manager doesn't offer
updates for an image named `:main`. To update DROOL to the newest development image:

1. **Project:** select **drool**, then **Action → Stop**.
2. **Container:** select **drool-drool-1**, then **Action → Delete**. DROOL's data isn't in the
   container: it stays in the `drool_drool-data` volume.
3. **Image:** select **ghcr.io/irnutsmurt/drool**, then **Delete**.
4. **Project:** select **drool**, then **Action → Build**. Container Manager downloads the newest image
   and creates DROOL's container again. If the project doesn't show as running afterwards, choose
   **Action → Start**.

Before a new version changes DROOL's database, DROOL backs it up by itself.
[Updating DROOL](Updating-DROOL.md) explains what an update does and how to go back.

## Something went wrong?

- [Setup Problems](Setup-Problems.md): no setup code, an expired code, which address to open, a
  browser that can't connect.
- Installing a Shepherd fails with *connection refused* on port 9443, after you changed the
  Shepherd port: [Installing a Shepherd](Installing-a-Shepherd.md#shepherd-installation-says-connection-refused-on-port-9443-after-changing-the-docker-port).

## Next

[First Login](First-Login.md): enter the setup code and create your administrator account.
