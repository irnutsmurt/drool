# Install DROOL with Docker Compose

This page takes you from a Linux machine with Docker to DROOL's setup screen in your browser. Every
command is explained, and you don't need to know anything about DROOL yet. It takes about ten
minutes.

At the end you'll have:

- DROOL running in a Docker container, which starts again by itself after a reboot;
- the address of DROOL's setup page, for example `https://10.10.0.10:8080/setup`;
- a one-time **setup code**: six digits that prove you're the person who installed DROOL, which you
  enter once to create the first administrator account;
- DROOL's **Set up DROOL** screen open in your browser.

The examples install DROOL on the machine `drool.example.net`, address 10.10.0.10
([About the Examples](About-the-Examples.md)). Wherever an example shows 10.10.0.10, use your own
machine's address.

On a Synology NAS, use [Install on a Synology NAS](Install-On-a-Synology-NAS.md) instead: the same
file, set up entirely in Container Manager, without a terminal.

## Before you start

- **A 64-bit Linux machine that stays on** (amd64, or arm64 such as a Raspberry Pi with a 64-bit
  system). DROOL runs there and manages your other machines from there. Requirements in more
  detail: [Installing DROOL](Installing-DROOL.md#before-you-start).
- **Docker, with Docker Compose.** In a terminal on that machine, run:

  ```sh
  docker compose version
  ```

  It should print a version, for example `Docker Compose version v2.29.7`. If it says
  `docker: command not found`, `unknown command` or `'compose' is not a docker command`, install
  Docker Engine and its Compose plugin first, with Docker's instructions for your distribution:
  [docs.docker.com/engine/install](https://docs.docker.com/engine/install/).
- **Permission to use Docker.** Run `docker ps`. A table heading (`CONTAINER ID   IMAGE …`), even with
  nothing under it, means you're set. If it says *permission denied* and mentions `docker.sock`,
  either type `sudo` in front of every `docker` command on this page, or add yourself to the
  `docker` group with `sudo usermod -aG docker "$USER"` and then log out and back in. (Anyone in the
  `docker` group can take over the whole machine through Docker, so add only people you'd trust
  with root.)
- **Internet access** from that machine, to download DROOL's `compose.yaml` file from GitHub and
  DROOL's container image (under 100 MB) from GitHub Container Registry (`ghcr.io`). No account is
  needed.
- **`curl`** to download the file (`curl --version` checks). `wget` works too.
- **Ports 8080 and 9443 free** on that machine: 8080 for DROOL's web interface, 9443 for Shepherds
  (DROOL's optional agents). Other ports work too: [Use other ports](#use-other-ports).

## Which DROOL this installs

DROOL has no official release yet, so this page installs the **development image**,
`ghcr.io/irnutsmurt/drool:main`. It is built automatically from the newest DROOL code whenever a
change passes DROOL's automated tests. It changes often, it isn't signed with a release key, and
`drool version` calls it a *development* build: good for trying DROOL and for a homelab you're
happy to experiment with, not for anything you can't afford to lose. Once there is a release,
[With a release instead](#with-a-release-instead) shows what changes.
[Installing DROOL](Installing-DROOL.md#development-image-or-release) explains the difference in
more detail.

## Step 1: Make a folder for DROOL

```sh
mkdir -p ~/drool
cd ~/drool
```

- `~` means your home folder, for example `/home/alex`, so `~/drool` is `/home/alex/drool`.
  `mkdir -p` creates the folder (and does nothing if it already exists); `cd` moves your terminal
  into it.
- This folder will hold DROOL's configuration file, `compose.yaml`. **Run every `docker compose` command
  for DROOL from this folder**, today and later: Docker Compose looks for its files in the folder
  you're in. In a new terminal, run `cd ~/drool` first.
- DROOL's data doesn't go into this folder: it's in a Docker volume, which keeps its name wherever
  the folder is ([Where your data is](#where-your-data-is)).

## Step 2: Download compose.yaml

`compose.yaml` tells Docker Compose how to run DROOL: which image, which ports, where its data goes,
and the safety restrictions it runs with. Download DROOL's copy from its GitHub repository into the
folder:

```sh
curl -fsSLO https://raw.githubusercontent.com/irnutsmurt/drool/main/compose.yaml
```

`-O` saves the file under its own name, `compose.yaml`, in the folder you're in. (With `wget`
instead: `wget https://raw.githubusercontent.com/irnutsmurt/drool/main/compose.yaml`.)

The same file is shown as a web page at
[github.com/irnutsmurt/drool/blob/main/compose.yaml](https://github.com/irnutsmurt/drool/blob/main/compose.yaml).
If you'd rather download it with your browser, use the **Download raw file** button on that page, not
*Save page as* (which saves the web page around the file), and then move it into the folder:
`mv ~/Downloads/compose.yaml ~/drool/`.

Check that the file is there and that it names DROOL's image on GitHub:

```sh
ls
grep '^ *image:' compose.yaml
```

`ls` lists `compose.yaml`, and `grep` prints the line that says which image Docker should run:

```text
    image: ${DROOL_IMAGE:-ghcr.io/irnutsmurt/drool:main}
```

That's DROOL's development image on GitHub Container Registry; the next step downloads it.
(`DROOL_IMAGE` lets people who build DROOL themselves run their own image instead; you don't need
it.) You don't need to change anything in the file.

## Step 3: Download DROOL

```sh
docker compose pull
```

Docker downloads the image `compose.yaml` names, DROOL's development image, from GitHub Container
Registry and shows its progress. It ends by saying that `ghcr.io/irnutsmurt/drool:main` was pulled
(the exact wording depends on your Docker version). The image exists for amd64 and arm64; Docker picks the one for your machine.

## Step 4: Start DROOL

```sh
docker compose up -d
```

Docker creates DROOL's data volume (`drool_drool-data`), a network (`drool_default`) and the container
(`drool-drool-1`), and starts it.

`-d` (*detached*) runs DROOL in the background: it keeps running when you close the terminal or log
out, and Docker starts it again after a reboot. (That needs the Docker service itself to start at
boot; `sudo systemctl enable docker` makes sure it does.) Without `-d`, DROOL would run inside your
terminal and stop as soon as you press Ctrl+C or close the terminal.

## Step 5: Check that it's running

```sh
docker compose ps
```

```text
NAME            IMAGE                           COMMAND                  SERVICE   CREATED          STATUS                    PORTS
drool-drool-1   ghcr.io/irnutsmurt/drool:main   "/usr/local/bin/droo…"   drool     20 seconds ago   Up 19 seconds (healthy)   0.0.0.0:8080->8080/tcp, [::]:8080->8080/tcp, 0.0.0.0:9443->9443/tcp, [::]:9443->9443/tcp
```

What to look for:

- **STATUS** starts with `Up`. Just after starting it says `(health: starting)`; within a minute it
  should say `(healthy)`, which means DROOL checked itself and is answering.
- **PORTS** includes `0.0.0.0:8080->8080/tcp`: connections to port 8080 of your machine reach DROOL.
- **No line at all**, or a STATUS of `Restarting` or `Exited`, means DROOL didn't start or keeps
  stopping. The last lines of `docker compose logs drool` (next step) say why; see also
  [Setup Problems](Setup-Problems.md#i-dont-see-a-setup-code).

## Step 6: Find the address and the setup code

When DROOL starts, it prints a short **startup message** with the address to open and the setup
code. Docker keeps everything DROOL prints (its *log*), and this command shows it:

```sh
docker compose logs drool
```

Every line starts with `drool-1  |`, the name of the container it came from. Most then continue with
`{`: those are DROOL's ordinary log entries, one per line, which you can ignore for now. **The startup
message is at the end**, framed by two rows of `=`. It looks like this:

```text
drool-1  | {"time":"2026-10-12T14:25:02.885846609Z","level":"INFO","msg":"applied migration","stream":"app","migration":"0001_init.sql"}
drool-1  | {"time":"2026-10-12T14:25:02.885991112Z","level":"INFO","msg":"applied migration","stream":"app","migration":"0002_setup_codes.sql"}
   ⋮
drool-1  | ================================================================
drool-1  |  DROOL is ready.
drool-1  |
drool-1  |  Open:
drool-1  |    https://<this-host's-address>:8080/setup
drool-1  |
drool-1  |  Setup code:
drool-1  |    482913
drool-1  |
drool-1  |  Single use. Expires 15:25 UTC (in 60 min).
drool-1  |  New code: restart, or run `drool setup code`.
drool-1  |
drool-1  |  Your browser will warn about the self-signed certificate; that is
drool-1  |  expected. You can install your own certificate later in the config.
drool-1  |  Certificate SHA-256: 5B:0E:93:2C:7A:D1:44:F8:16:AB:C2:3E:90:57:6D:E4:08:BF:21:7C:D5:39:A6:1F:4E:82:C7:0D:63:B9:F2:15
drool-1  | ================================================================
```

(The `⋮` stands for about 30 more lines like the two above it.)

- **`Open:`** is the address of the setup page. Inside a container, DROOL can't know the address your
  browser uses to reach this machine, so it prints the placeholder `<this-host's-address>`. Replace
  it, including the `<` and `>`, with this machine's IP address or name. In the example that gives
  `https://10.10.0.10:8080/setup`. To find the address, run `hostname -I` on this machine: it prints
  its IP addresses, and the first is usually the one you want. (Docker's own addresses, such as
  `172.17.0.1`, don't work from other computers. If `hostname` says *invalid option*, see
  [I don't know what address to open](Setup-Problems.md#i-dont-know-what-address-to-open).) If your
  browser runs on this same machine, `https://localhost:8080/setup` works too.
- **`Setup code:`** is the six-digit code. **`482913` is only this page's example: use the code your
  own DROOL printed.** It works once, for 60 minutes.
- **`Expires 15:25 UTC`**: the time is in UTC, the container's clock, which can differ from your
  local time. `(in 60 min)` is what counts.
- **`Certificate SHA-256`** lets you check that your browser is really talking to your DROOL
  ([First Login](First-Login.md#1-open-drool-in-your-browser)).

To see just the setup code lines:

```sh
docker compose logs drool | grep -A 1 'Setup code:'
```

If this shows more than one code, DROOL has been restarted, and **only the last one works**
([why](#when-drool-makes-a-new-code-by-itself)).

## Step 7: Open the setup page

In your browser, go to the address from step 6 with your machine's address filled in, for example
`https://10.10.0.10:8080/setup`. Type the `https://` and the `:8080`.

The browser warns that the connection isn't private. That's expected: DROOL made its own
certificate, which your browser has no way to check yet. Continue to the site (Chrome: **Advanced →
Proceed to …**; Firefox: **Advanced → Accept the Risk and Continue**).

You should now see **Set up DROOL**:

![The "Set up DROOL" page: setup code, admin username, password and confirm password fields, and the Create administrator button](images/first-login-setup.png)

DROOL is installed. Continue with [First Login](First-Login.md#2-create-the-administrator): enter
the code and create your administrator account.

If the browser can't connect or shows something else, see
[Setup Problems](Setup-Problems.md#the-browser-cant-connect).

## If you closed the terminal, lost the code, or it expired

Closing the terminal doesn't stop DROOL: it runs in the background. Open a new terminal and show the
startup message again:

```sh
cd ~/drool
docker compose logs drool
```

If the code there is more than 60 minutes old, was already used, or you can't find it, ask DROOL for
a new one:

```sh
cd ~/drool
docker compose exec drool drool setup code
```

It answers like this (with its own code):

```text
Setup code: 615027
Single use. Expires 15:40 UTC. Any previously generated code is no longer valid.
```

- `docker compose exec drool` runs a command inside the running `drool` container, and
  `drool setup code` is the command.
- The new code appears only here, not in `docker compose logs`.
- **The previous code stops working.** DROOL never stores a setup code itself, only a fingerprint of
  it, so it can't show you an old code again; it can only make a new one.
- This works only until the first administrator exists ([after that](#after-the-first-administrator-exists)).

## When DROOL makes a new code by itself

Until someone creates the first administrator, **every start of DROOL makes a new setup code, and
the previous one stops working**, including one from `drool setup code`. DROOL starts again when:

- you run `docker compose restart`, or `docker compose down` and then `docker compose up -d`;
- you run `docker compose up -d` after changing `compose.yaml` (Docker replaces the container);
- you update the image (`docker compose pull`, then `docker compose up -d`);
- the machine reboots, or Docker is restarted or updated;
- DROOL stopped unexpectedly and Docker restarted it.

So always use the newest code. After a restart, `docker compose logs drool` shows one startup message
for each start: use the last one. After the container was replaced, the log starts afresh with the
new message. When in doubt, run `docker compose exec drool drool setup code` and use that code
straight away.

## After the first administrator exists

Setup happens once. As soon as the first administrator account exists:

- DROOL deletes every setup code, and its startup message no longer shows one. It then says only
  `Open: https://<this-host's-address>:8080`.
- Going to `/setup` takes you to the **Sign in to DROOL** page.
- `docker compose exec drool drool setup code` refuses with *drool: setup is already complete (an
  administrator exists); use `drool user create` to add users*.

To give other people access, add them under **Settings → Users** ([Users and Roles](Users-and-Roles.md)).
If you're ever locked out, [First Login](First-Login.md#if-you-forget-your-password) shows how to
create a new administrator from the command line.

## Everyday commands

Run these in `~/drool` (`cd ~/drool` first):

| To | Run |
|---|---|
| See whether DROOL is running | `docker compose ps` |
| See DROOL's log | `docker compose logs drool`; only the newest 50 lines: `docker compose logs --tail 50 drool`; keep watching: `docker compose logs -f drool` (Ctrl+C stops watching, not DROOL) |
| Stop DROOL | `docker compose stop` |
| Start it again | `docker compose start` |
| Restart it | `docker compose restart` |
| Run a `drool` command | `docker compose exec drool drool <command>` ([Command Line](Command-Line.md)) |
| Update DROOL | [Updating DROOL](Updating-DROOL.md) |
| Back it up | [Docker Backup](Docker-Backup.md) |

## Where your data is

Everything DROOL stores (hosts, credentials, history, its keys) is in a Docker **volume** called
**`drool_drool-data`**: Docker's storage, not a file in `~/drool`. `compose.yaml` names it, so the
name stays the same if you move or rename the folder.

- `docker compose stop`, `restart`, `down`, updates and reboots all keep it.
- **`docker compose down -v` deletes it**, and with it everything DROOL knows. Don't add `-v` unless
  you mean to [start over](#starting-over).
- A second DROOL on the same machine needs a name of its own, and an installation made with a
  `compose.yaml` from before 2026-10-14 may have its data under another name: see
  [Compose Data Volume](Compose-Data-Volume.md).
- DROOL's [master key](Master-Key.md) is in the volume too. Once you're set up, keep a copy of it
  somewhere safe ([Secure Your Installation](Secure-Your-Installation.md#a-copy-of-the-master-key-kept-apart-from-your-backups)).

## What the container may do

DROOL's `compose.yaml` gives the container nothing it doesn't need. Keep it that way:

- **Two ports:** 8080 for the web interface (and DROOL's API), 9443 for Shepherds
  ([other ports](#use-other-ports)). If you'll never use Shepherds, `DROOL_AGENT_ENABLED: "false"`
  under `environment:` turns that endpoint off ([Configuration](Configuration.md)).
- **One writable place:** the data volume, at `/data` inside the container, plus a temporary `/tmp` in
  memory that only `drool backup verify` uses. The rest of the container is read-only.
- **No privileges:** an ordinary unprivileged user, no Linux capabilities and no way to gain any; no
  host network, no Docker socket, no devices and no folders from your machine.
- **Your machine's firewall is never touched.** DROOL manages the firewalls of the hosts it manages,
  over SSH or their Shepherd, never the firewall of the machine it runs on.
- **A health check** runs `drool healthcheck` inside the container: that's where `(healthy)` in
  `docker compose ps` comes from.
- **No shell inside.** The image contains DROOL and nothing else, so `docker compose exec drool sh`
  doesn't work; run DROOL's own commands with `docker compose exec drool drool <command>`
  ([Command Line](Command-Line.md)).
- **Rootless Docker** works the same way, with the same `compose.yaml`.

## Use other ports

`compose.yaml` starts, after its comments, with the two ports other computers use to reach DROOL:

```yaml
x-drool-ports:
  web: &web-port 8080
  shepherds: &shepherd-port ${DROOL_AGENT_PUBLIC_PORT:-9443}
```

- **`web`** is the port your browser uses: `https://10.10.0.10:8080`.
- **`shepherds`** is the port Shepherds connect to: the number after `:-`.

Inside its container DROOL always listens on 8080 and 9443 (the `target:` lines further down), and
Docker forwards this machine's two ports to them. DROOL can't see that forwarding from inside its
container, so the same two numbers are also handed to DROOL, under `environment:`. It then tells
Shepherds the right port, and its startup message shows the right web port. **Change the numbers at
the top and nothing else.**

For example, for Shepherds on port 2043 of this machine:

1. Open `compose.yaml` in a text editor, for example `nano compose.yaml` (in `~/drool`).
2. Change `9443` on the `shepherds:` line to `2043`:

   ```yaml
     shepherds: &shepherd-port ${DROOL_AGENT_PUBLIC_PORT:-2043}
   ```

   (In nano, Ctrl+O and Enter save, Ctrl+X quits.)
3. Apply it:

   ```sh
   docker compose up -d
   ```

   Docker replaces the container; your data stays. If you haven't created the administrator yet,
   DROOL now has a new setup code ([why](#when-drool-makes-a-new-code-by-itself)).

`docker compose ps` then shows `0.0.0.0:2043->9443/tcp`: this machine's port 2043 forwarded to
DROOL's 9443. DROOL keeps listening on 9443 inside the container and tells Shepherds to connect to
`10.10.0.10:2043`.

Instead of editing the file, you can set the Shepherd port in a file named `.env` next to
`compose.yaml`, with the line `DROOL_AGENT_PUBLIC_PORT=2043`: Docker Compose reads it by itself. You
don't need one otherwise.

**Change the Shepherd port before you install Shepherds.** A Shepherd that's already installed keeps
connecting to the port it was given at installation and goes offline when that port disappears.
Putting the old number back brings it back.

## If you'll use Shepherds

Shepherds (DROOL's optional agents, see [SSH vs Shepherds](SSH-vs-Shepherds.md)) connect to DROOL
at the address you open DROOL with in your browser, on the Shepherd port: 9443, or the one you chose
([Use other ports](#use-other-ports)). Before you install one, the install dialog shows exactly where
it will connect, for example **Shepherds will connect to: 10.10.0.10:9443**. Your managed hosts must
be able to reach that address and port.

So open DROOL by an address your managed hosts can reach too, not `localhost`. If they reach this
machine by another name or address (a DNS name, NAT, a VPN), tell DROOL that name:

1. Open `compose.yaml` in a text editor, for example `nano compose.yaml` (in `~/drool`).
2. Find this line, under `environment:`:

   ```yaml
         # DROOL_AGENT_ADVERTISE: drool.example.net
   ```

3. Delete the `#` and the space after it, and put your machine's name or IP address in place of
   `drool.example.net`:

   ```yaml
         DROOL_AGENT_ADVERTISE: drool.example.net
   ```

   Keep the spaces at the start of the line: it lines up with `DROOL_AGENT_PUBLIC_PORT` above it.
   DROOL adds the Shepherd port by itself; write `name:port` only if Shepherds must use yet another
   port.
4. Apply it: `docker compose up -d`.

Other settings work the same way: each is an environment variable under `environment:`
([Configuration](Configuration.md)).

## With a release instead

Once DROOL has an official release, you can install it instead of the development image. The steps
are the same except where the files come from:

1. In step 2, download `compose.yaml`, the image file (`drool_1.0.0_image.oci.tar`) and `SHA256SUMS`
   from the release on the [Releases page](https://github.com/irnutsmurt/drool/releases) into
   `~/drool`. (`1.0.0` stands for the release's version; the page shows the exact file names.) The
   release's `compose.yaml` names the release's own image instead of the development image. If your
   folder has a `.env` file that sets `DROOL_IMAGE` (an earlier version of this page made one),
   delete it (`rm .env`): it would keep Compose on the development image.
2. Instead of step 3, check the files and load the image into Docker:

   ```sh
   sha256sum --check --ignore-missing SHA256SUMS
   docker load -i drool_1.0.0_image.oci.tar
   ```

   The first command must print `OK` for each file you downloaded. If it says `FAILED`, download
   that file again and don't use it until it says `OK`. (Checking the release's signature too:
   [Checking a release's files](Installing-DROOL.md#checking-a-releases-files).)
3. Continue with step 4.

A release's `compose.yaml` names its own version (for example `drool:1.0.0`), so DROOL changes only
when you install the next release ([Updating DROOL](Updating-DROOL.md)). Already running the
development image? Read the release notes before switching: a release older than your development
build may refuse to open the database that build has upgraded.

## Starting over

To throw away a test installation completely, with all its hosts, credentials and history, remove
its volume:

```sh
cd ~/drool
docker compose down
docker volume rm drool_drool-data
docker compose up -d
```

DROOL then starts as if new, with a new setup code (step 6). (With `DROOL_INSTANCE` set, the volume
is `<DROOL_INSTANCE>_drool-data`: [Compose Data Volume](Compose-Data-Volume.md#starting-over).)

## Something went wrong?

[Setup Problems](Setup-Problems.md) covers: no setup code in the log, a code that expired or doesn't
work, *setup is already complete*, which address to open, a browser that can't connect, and the
Docker messages *no configuration file provided*, *pull access denied*, *permission denied* and
*port is already allocated*.

## Next

[First Login](First-Login.md): enter the setup code and create your administrator account.
