# Install DROOL with Podman

This page sets DROOL up as a **rootless** Podman container (it runs as your ordinary user, without
root) that systemd starts for you, from nothing installed to DROOL's setup screen in your browser.

At the end you'll have:

- DROOL running in a Podman container that starts at boot;
- the address of DROOL's setup page, for example `https://10.10.0.10:8080/setup`;
- a one-time **setup code**, six digits you enter once to create the first administrator account;
- DROOL's **Set up DROOL** screen open in your browser.

The examples install DROOL on `drool.example.net`, address 10.10.0.10
([About the Examples](About-the-Examples.md)). Use your own machine's address instead.

## Before you start

- **A 64-bit Linux machine with systemd** (amd64 or arm64) that stays on. Requirements in more
  detail: [Installing DROOL](Installing-DROOL.md#before-you-start).
- **Podman 4.4 or newer.** Check with `podman --version`. Fedora, Rocky Linux and AlmaLinux 9 or
  newer, Ubuntu 24.04 and Debian 13 have a new enough one (`sudo dnf install podman` or
  `sudo apt install podman`). Debian 12 and Ubuntu 22.04 ship an older Podman that can't do this:
  use [Docker Compose](Install-With-Docker-Compose.md) or a [package](Install-From-a-Package.md)
  there.
- **Log in as the user who will run DROOL**: an ordinary user, not root, and logged in directly (on
  the console, the desktop or over SSH), not through `su` or `sudo -i`. Rootless Podman needs that
  user's own systemd session.
- **Internet access** from that machine, to download a small file from GitHub and DROOL's image
  (under 100 MB) from GitHub Container Registry (`ghcr.io`). No account is needed.
- **`curl`** (`curl --version` checks), or `wget`.
- **Ports 8080 and 9443 free**: 8080 for the web interface, 9443 for Shepherds (DROOL's optional
  agents).

This page installs DROOL's **development image**, `ghcr.io/irnutsmurt/drool:main`, because DROOL has
no official release yet. It is the newest code that passed DROOL's automated tests: fine for trying
DROOL, not a stable release ([the difference](Installing-DROOL.md#development-image-or-release)).

## Step 1: Download DROOL's unit file

Podman can run a container as a systemd service from a small description file (Podman calls this
*Quadlet*). DROOL ships one, `drool.container`. Podman looks for these files in
`~/.config/containers/systemd/`, so create that folder and download the file into it:

```sh
mkdir -p ~/.config/containers/systemd
cd ~/.config/containers/systemd
curl -fsSLO https://raw.githubusercontent.com/irnutsmurt/drool/main/packaging/podman/drool.container
```

`~` is your home folder. (With `wget` instead of `curl`:
`wget https://raw.githubusercontent.com/irnutsmurt/drool/main/packaging/podman/drool.container`.)

## Step 2: Point it at the development image

The file's `Image=` line names the image to run. As downloaded, it names `drool:local`, which is for
people who build DROOL themselves. Change it to the development image and check the result:

```sh
sed -i 's|^Image=.*|Image=ghcr.io/irnutsmurt/drool:main|' drool.container
grep '^Image=' drool.container
```

The second command must print:

```text
Image=ghcr.io/irnutsmurt/drool:main
```

(`sed -i` edits the file in place: it replaces the whole line that starts with `Image=`. You can
make the same change in a text editor instead, for example `nano drool.container`.)

## Step 3: Download DROOL

```sh
podman pull ghcr.io/irnutsmurt/drool:main
```

Podman downloads the image and shows its progress. The image exists for amd64 and arm64; Podman picks
the one for your machine.

## Step 4: Start DROOL

```sh
systemctl --user daemon-reload
systemctl --user start drool
```

- `systemctl --user` manages your own user's services (no `sudo`).
- `daemon-reload` makes systemd read the new file; Podman turns `drool.container` into a service
  called `drool`.
- `start drool` starts it. You don't need `systemctl --user enable`: the file itself says to start
  DROOL whenever your user's services start.

## Step 5: Keep it running when you log out

```sh
loginctl enable-linger "$USER"
```

Without this, systemd stops your user's services when you log out, and starts them only when you log
in. With it, DROOL starts at boot and keeps running. If it says *Access denied*, run it with `sudo`
in front.

## Step 6: Check that it's running

```sh
systemctl --user status drool --no-pager
podman ps
```

`systemctl` should say `Active: active (running)`. `podman ps` lists the container:

```text
CONTAINER ID  IMAGE                          COMMAND     CREATED         STATUS                   PORTS                                           NAMES
3f2a9c81b7d4  ghcr.io/irnutsmurt/drool:main  serve       40 seconds ago  Up 40 seconds (healthy)  0.0.0.0:8080->8080/tcp, 0.0.0.0:9443->9443/tcp  drool
```

Its STATUS starts with `Up`; within a minute it should add `(healthy)`. If `systemctl` says `failed`
or `podman ps` lists nothing, the log in the next step says why.

## Step 7: Find the address and the setup code

When DROOL starts, it prints a short **startup message** with the address to open and the setup
code. systemd collects what its services print in the *journal*, and `journalctl` shows it:

```sh
journalctl --user -u drool -n 50
```

- `--user` means your own services, `-u drool` only DROOL's messages, `-n 50` the newest 50 lines.
- If the output is longer than your screen, `journalctl` shows it in a viewer: scroll with the arrow
  keys, press `q` to leave.

Each line starts with the date, the time, the machine's name and the program's name. After that,
most lines start with `{`: DROOL's ordinary log, which you can ignore for now. **The startup message
is near the end**, framed by two rows of `=`. Podman may show an empty line after each line. Without
the date and names at the start of each line, the important part looks like this:

```text
 DROOL is ready.

 Open:
   https://<this-host's-address>:8080/setup

 Setup code:
   482913

 Single use. Expires 15:25 UTC (in 60 min).
 New code: restart, or run `drool setup code`.
```

- **`Open:`** is the address of the setup page. Inside a container, DROOL can't know the address your
  browser uses to reach this machine, so it prints the placeholder `<this-host's-address>`. Replace
  it, including the `<` and `>`, with this machine's IP address or name, for example
  `https://10.10.0.10:8080/setup`. `hostname -I` on this machine prints its IP addresses; the first
  is usually the one you want ([more help](Setup-Problems.md#i-dont-know-what-address-to-open)). If
  your browser runs on this machine, `https://localhost:8080/setup` works too.
- **`Setup code:`** is the six-digit code. **`482913` is only this page's example: use the code your
  own DROOL printed.** It works once, for 60 minutes.
- The expiry time is in UTC, the container's clock; `(in 60 min)` is what counts.

## Step 8: Open the setup page

In your browser, go to the address with your machine's address filled in, for example
`https://10.10.0.10:8080/setup`. The browser warns that the connection isn't private: that's expected
(DROOL made its own certificate). Continue to the site (Chrome: **Advanced → Proceed to …**; Firefox:
**Advanced → Accept the Risk and Continue**), and you see **Set up DROOL**:

![The "Set up DROOL" page: setup code, admin username, password and confirm password fields, and the Create administrator button](images/first-login-setup.png)

If the machine runs a firewall (Rocky Linux, AlmaLinux and Fedora Server turn one on by default),
open the ports first: [Open the ports, if the machine has a firewall](Install-From-a-Package.md#step-7-open-the-ports-if-the-machine-has-a-firewall).
Other connection problems: [Setup Problems](Setup-Problems.md#the-browser-cant-connect).

DROOL is installed. Continue with [First Login](First-Login.md#2-create-the-administrator).

## If you lost the code, or it expired

Logging out or closing the terminal doesn't stop DROOL. `journalctl --user -u drool -n 50` shows the
startup message again. If the code is more than 60 minutes old, already used, or you can't find it,
ask DROOL for a new one:

```sh
podman exec drool drool setup code
```

```text
Setup code: 615027
Single use. Expires 15:40 UTC. Any previously generated code is no longer valid.
```

(Your code differs.) `podman exec drool` runs a command inside the running container called `drool`.
**The previous code stops working**: DROOL never stores a setup code itself, so it can't show an old
one again, only make a new one. The new code appears only here, not in the journal.

## When DROOL makes a new code by itself

Until someone creates the first administrator, **every start of DROOL makes a new setup code and the
previous one stops working**, including one from `drool setup code`. DROOL starts again when you run
`systemctl --user restart drool`, after you change `drool.container` and restart it, when you update
the image, when the machine reboots, and when it stopped unexpectedly and systemd restarted it. So
always use the newest code: the last startup message in the journal, or the code from your latest
`drool setup code`.

## After the first administrator exists

Setup happens once. Afterwards the startup message shows no code (only
`Open: https://<this-host's-address>:8080`), `/setup` takes you to **Sign in to DROOL**, and
`podman exec drool drool setup code` refuses with *setup is already complete (an administrator
exists)*. Add other people under **Settings → Users** ([Users and Roles](Users-and-Roles.md)).
Locked out? See [First Login](First-Login.md#if-you-forget-your-password).

## Everyday commands

| To | Run |
|---|---|
| See whether DROOL is running | `systemctl --user status drool --no-pager` or `podman ps` |
| See DROOL's log | `journalctl --user -u drool -n 50`; keep watching: `journalctl --user -u drool -f` (Ctrl+C stops watching) |
| Stop, start, restart DROOL | `systemctl --user stop drool`, `… start drool`, `… restart drool` |
| Run a `drool` command | `podman exec drool drool <command>` ([Command Line](Command-Line.md)) |
| Update DROOL | [Updating DROOL](Updating-DROOL.md) |
| Back it up | [Podman Backup](Podman-Backup.md) |

## Where your data is

Everything DROOL stores is in the Podman volume **`drool-data`** (`podman volume inspect drool-data`
shows where on disk). Stopping, restarting, updating and rebooting keep it; `podman volume rm
drool-data` deletes it, with everything DROOL knows. Keep a copy of DROOL's
[master key](Master-Key.md) once you're set up
([Secure Your Installation](Secure-Your-Installation.md#a-copy-of-the-master-key-kept-apart-from-your-backups)).

## If you'll use Shepherds

Shepherds connect to DROOL on port 9443 at an address DROOL gives them; inside a container DROOL
can't find out its own address. (Without this setting, DROOL uses the address you opened it with in
your browser, which fails for `localhost` or an address other machines can't reach.)

1. Open `~/.config/containers/systemd/drool.container` in a text editor.
2. Find the line `#Environment=DROOL_AGENT_ADVERTISE=drool.example.net:9443`, delete the `#` at its
   start, and put your machine's name or address in place of `drool.example.net`:

   ```text
   Environment=DROOL_AGENT_ADVERTISE=10.10.0.10:9443
   ```

3. Apply it:

   ```sh
   systemctl --user daemon-reload
   systemctl --user restart drool
   ```

   Your data stays. Before the first administrator exists, the restart makes a new setup code.

## With a release instead

Once DROOL has an official release, download its `drool.container`, the image file
(`drool_1.0.0_image.oci.tar`, where `1.0.0` stands for the release's version) and `SHA256SUMS` from
the [Releases page](https://github.com/irnutsmurt/drool/releases) into one folder, and in that
folder:

```sh
sha256sum --check --ignore-missing SHA256SUMS
podman load -i drool_1.0.0_image.oci.tar
mkdir -p ~/.config/containers/systemd
cp drool.container ~/.config/containers/systemd/
```

The first command must print `OK` for each file ([checking the signature too](Installing-DROOL.md#checking-a-releases-files)).
The release's `drool.container` already names its
image, so skip steps 1 to 3 and continue with step 4. If you're already running the development
image, read the release notes before switching.

## Starting over

To throw away a test installation completely:

```sh
systemctl --user stop drool
podman volume rm drool-data
systemctl --user start drool
```

DROOL then starts as if new, with a new setup code.

## Something went wrong?

See [Setup Problems](Setup-Problems.md).

## Next

[First Login](First-Login.md): enter the setup code and create your administrator account.
