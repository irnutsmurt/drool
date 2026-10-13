# Install DROOL from a package (.deb or .rpm)

This page installs DROOL as a normal system service from its `.deb` or `.rpm` package, from nothing
installed to DROOL's setup screen in your browser.

> **Packages come with releases, and DROOL has no official release yet.** Until the first one
> appears on the [Releases page](https://github.com/irnutsmurt/drool/releases), install with
> [Docker Compose](Install-With-Docker-Compose.md) or [Podman](Install-With-Podman.md), or build the
> packages yourself from the source code with `make release` (see the README's
> [Build and run from source](https://github.com/irnutsmurt/drool#build-and-run-from-source)). This
> page describes the steps for a release; `1.0.0` stands for its version.

At the end you'll have:

- DROOL running as the `drool` system service, started at boot;
- the address of DROOL's setup page, for example `https://10.10.0.10:8080/setup`;
- a one-time **setup code**, six digits you enter once to create the first administrator account;
- DROOL's **Set up DROOL** screen open in your browser.

The examples install DROOL on `drool.example.net`, address 10.10.0.10
([About the Examples](About-the-Examples.md)). Use your own machine's address instead.

## Before you start

- **A supported system**: Debian 12 or 13; Ubuntu 22.04, 24.04 or 26.04 (`.deb`); Rocky Linux 9 or
  10, AlmaLinux 9 or 10, Fedora 44, or RHEL 9 or 10 (`.rpm`); 64-bit, on amd64 or arm64. Other
  systemd-based distributions: [Install From the Release Archive](Install-From-the-Release-Archive.md).
  Requirements in more detail: [Installing DROOL](Installing-DROOL.md#before-you-start).
- **`sudo`**: installing a package and starting a system service need administrator rights.
- **Your machine's CPU type**, to pick the right file. Run `uname -m`: `x86_64` means amd64, `aarch64`
  means arm64.

## Step 1: Download the package

Make a folder for the downloads and go into it:

```sh
mkdir -p ~/drool-release
cd ~/drool-release
```

From the release on the [Releases page](https://github.com/irnutsmurt/drool/releases), download
**two files** into this folder: the package for your system and CPU, and `SHA256SUMS` (the list of
the release's checksums).

| System | CPU | Package |
|---|---|---|
| Debian, Ubuntu | amd64 (`x86_64`) | `drool_1.0.0-1_amd64.deb` |
| Debian, Ubuntu | arm64 (`aarch64`) | `drool_1.0.0-1_arm64.deb` |
| Rocky Linux, AlmaLinux, RHEL, Fedora | amd64 (`x86_64`) | `drool-1.0.0-1.x86_64.rpm` |
| Rocky Linux, AlmaLinux, RHEL, Fedora | arm64 (`aarch64`) | `drool-1.0.0-1.aarch64.rpm` |

You can click them on the release page, or download them in the terminal; for example, for the first
row:

```sh
curl -fLO https://github.com/irnutsmurt/drool/releases/download/v1.0.0/drool_1.0.0-1_amd64.deb
curl -fLO https://github.com/irnutsmurt/drool/releases/download/v1.0.0/SHA256SUMS
```

Files downloaded with a browser usually land in `~/Downloads`; move them into the folder
(`mv ~/Downloads/drool_1.0.0-1_amd64.deb ~/Downloads/SHA256SUMS ~/drool-release/`).

## Step 2: Check the download

```sh
sha256sum --check --ignore-missing SHA256SUMS
```

It must print `OK` after the package's name, for example `drool_1.0.0-1_amd64.deb: OK`. If it says
`FAILED`, the file is damaged or isn't the one the release published: download it again, and don't
install it until it says `OK`. Official releases also sign `SHA256SUMS`:
[Checking a release's files](Installing-DROOL.md#checking-a-releases-files) shows how to check that
signature.

## Step 3: Install it

Run the line for your system only:

```sh
sudo apt install ./drool_1.0.0-1_amd64.deb          # Debian, Ubuntu
sudo dnf install ./drool-1.0.0-1.x86_64.rpm         # Rocky Linux, AlmaLinux, RHEL, Fedora
```

The `./` matters: it tells `apt` and `dnf` to install this file rather than look for a package called
`drool` online. At the end, the package prints:

```text
DROOL is installed. Start it:

    sudo systemctl enable --now drool

then open the address and use the one-time setup code it prints:

    sudo journalctl -u drool -n 30
```

followed by a reminder about the master key. The next steps do exactly that.

The package creates a `drool` system user, which DROOL runs as (never root), and puts DROOL's data in
`/var/lib/drool`. The `drool` service runs in a strict systemd sandbox: no capabilities, a read-only
system, a private `/tmp`.

## Step 4: Start DROOL

```sh
sudo systemctl enable --now drool
```

`enable` makes DROOL start at every boot; `--now` also starts it right away.

## Step 5: Check that it's running

```sh
systemctl status drool --no-pager
```

It should say `Active: active (running)`. If it says `failed` or `activating (auto-restart)`, DROOL
can't start; the log in the next step says why.

## Step 6: Find the address and the setup code

A system service doesn't print to your terminal. What it prints goes to the system **journal**, a log
that systemd keeps for every service, and `journalctl` shows it:

```sh
sudo journalctl -u drool -n 50
```

- `-u drool` shows only DROOL's messages, `-n 50` the newest 50 lines.
- `sudo` is needed because the system journal is for administrators: without it you may see
  `-- No entries --` or a hint that you're *not seeing messages from other users and the system*.
- If the output is longer than your screen, `journalctl` shows it in a viewer: scroll with the arrow
  keys, press `q` to leave.

DROOL's startup message is at the end:

```text
Oct 12 14:25:03 drool drool[812]: ================================================================
Oct 12 14:25:03 drool drool[812]:  DROOL is ready.
Oct 12 14:25:03 drool drool[812]:
Oct 12 14:25:03 drool drool[812]:  Open:
Oct 12 14:25:03 drool drool[812]:    https://10.10.0.10:8080/setup
Oct 12 14:25:03 drool drool[812]:
Oct 12 14:25:03 drool drool[812]:  Setup code:
Oct 12 14:25:03 drool drool[812]:    482913
Oct 12 14:25:03 drool drool[812]:
Oct 12 14:25:03 drool drool[812]:  Single use. Expires 15:25 UTC (in 60 min).
Oct 12 14:25:03 drool drool[812]:  New code: restart, or run `drool setup code`.
Oct 12 14:25:03 drool drool[812]:
Oct 12 14:25:03 drool drool[812]:  Your browser will warn about the self-signed certificate; that is
Oct 12 14:25:03 drool drool[812]:  expected. You can install your own certificate later in the config.
Oct 12 14:25:03 drool drool[812]:  Certificate SHA-256: 5B:0E:93:2C:7A:D1:44:F8:16:AB:C2:3E:90:57:6D:E4:08:BF:21:7C:D5:39:A6:1F:4E:82:C7:0D:63:B9:F2:15
Oct 12 14:25:03 drool drool[812]: ================================================================
```

Each line starts with the date and time, the machine's name (`drool` here) and the program with its
process number (`drool[812]`).

- **`Open:`** lists this machine's own addresses with the setup page, here
  `https://10.10.0.10:8080/setup`. If it lists several, use the one on the same network as the
  computer your browser runs on.
- **`Setup code:`** is the six-digit code. **`482913` is only this page's example: use the code your
  own DROOL printed.** It works once, for 60 minutes; the expiry time is in this machine's time zone.

## Step 7: Open the ports, if the machine has a firewall

DROOL never changes the firewall of the machine it runs on. If that machine runs one, open DROOL's
ports yourself: 8080 for the web interface, and 9443 if you'll use Shepherds. No firewall? Go on to
step 8.

- **firewalld** (on by default on Rocky Linux, AlmaLinux, RHEL and Fedora Server). If
  `sudo firewall-cmd --state` says `running`:

  ```sh
  sudo firewall-cmd --permanent --add-port=8080/tcp --add-port=9443/tcp
  sudo firewall-cmd --reload
  ```

- **ufw** (Ubuntu, when it's been turned on). If `sudo ufw status` says `Status: active`:

  ```sh
  sudo ufw allow 8080/tcp
  sudo ufw allow 9443/tcp
  ```

## Step 8: Open the setup page

In your browser, go to the address from the startup message, for example
`https://10.10.0.10:8080/setup`. The browser warns that the connection isn't private: that's expected
(DROOL made its own certificate). Continue to the site (Chrome: **Advanced → Proceed to …**; Firefox:
**Advanced → Accept the Risk and Continue**), and you see **Set up DROOL**:

![The "Set up DROOL" page: setup code, admin username, password and confirm password fields, and the Create administrator button](images/first-login-setup.png)

DROOL is installed. Continue with [First Login](First-Login.md#2-create-the-administrator).
Can't connect? [Setup Problems](Setup-Problems.md#the-browser-cant-connect).

## If you lost the code, or it expired

Closing the terminal doesn't affect DROOL: it's a service. `sudo journalctl -u drool -n 50` shows the
startup message again. If the code is more than 60 minutes old, already used, or you can't find it,
ask DROOL for a new one:

```sh
sudo -u drool drool setup code
```

```text
Setup code: 615027
Single use. Expires 15:40 UTC. Any previously generated code is no longer valid.
```

(Your code differs.) `sudo -u drool` runs the command as DROOL's own user, which owns its data; run
as root, DROOL's commands refuse to touch it. **The previous code stops working**: DROOL never stores a setup code
itself, so it can't show you an old one again, only make a new one. The new code appears only here,
not in the journal.

## When DROOL makes a new code by itself

Until someone creates the first administrator, **every start of DROOL makes a new setup code and the
previous one stops working**, including one from `drool setup code`. DROOL starts again after
`sudo systemctl restart drool`, a reboot, a package upgrade (which restarts a running DROOL), and when
it stopped unexpectedly and systemd restarted it. So always use the newest code: the last startup
message in the journal, or the code from your latest `drool setup code`.

## After the first administrator exists

Setup happens once. Afterwards the startup message shows no code and `Open:` has no `/setup`;
`/setup` takes you to **Sign in to DROOL**; and `sudo -u drool drool setup code` refuses with
*setup is already complete (an administrator exists)*. Add other people under **Settings → Users**
([Users and Roles](Users-and-Roles.md)). Locked out? See
[First Login](First-Login.md#if-you-forget-your-password).

## Everyday commands

| To | Run |
|---|---|
| See whether DROOL is running | `systemctl status drool --no-pager` |
| See its startup messages | `sudo journalctl -u drool -n 50` |
| See its detailed log | the files in `/var/lib/drool/logs/` (`sudo ls /var/lib/drool/logs`) |
| Stop, start, restart DROOL | `sudo systemctl stop drool`, `… start drool`, `… restart drool` |
| Run a `drool` command | `sudo -u drool drool <command>` ([Command Line](Command-Line.md)) |
| Change a setting | `/etc/drool/config.yaml` ([Configuration](Configuration.md)) |
| Update DROOL | [Updating DROOL](Updating-DROOL.md) |
| Back it up | [Native Installation Backup](Native-Installation-Backup.md) |

## Where your data is

| What | Where |
|---|---|
| Everything DROOL stores (database, [master key](Master-Key.md), certificate, backups, logs) | `/var/lib/drool` |
| The configuration file, if you make one | `/etc/drool/config.yaml` |
| The program | `/usr/bin/drool` |

**Removing the package keeps your data** (`apt remove`, `apt purge` and `dnf remove` alike):
installing DROOL again later picks up `/var/lib/drool` as it was, with its accounts, so it won't ask
for setup again. To delete the data for good, once you're sure you don't need it:
`sudo rm -rf /var/lib/drool /etc/drool && sudo userdel drool`.

## If you'll use Shepherds

Shepherds connect to DROOL on port 9443 at an address DROOL gives them. By default that's the address
you opened DROOL with in your browser. If that was `localhost`, or a name your other machines can't
reach, set `agent.advertise` in `/etc/drool/config.yaml`
([Configuration](Configuration.md#change-a-setting-native-installation)), for example:

```yaml
agent:
  advertise: [drool.example.net:9443]
```

## Something went wrong?

See [Setup Problems](Setup-Problems.md).

## Next

[First Login](First-Login.md): enter the setup code and create your administrator account.
