# Install DROOL from the release archive

The release archive is DROOL as plain files, for 64-bit Linux distributions with systemd that DROOL
has no package for. This page installs it as a system service, from nothing installed to DROOL's
setup screen in your browser. On Debian, Ubuntu, Rocky Linux, AlmaLinux, RHEL or Fedora,
[the package](Install-From-a-Package.md) is simpler.

> **The archive comes with releases, and DROOL has no official release yet.** Until the first one
> appears on the [Releases page](https://github.com/irnutsmurt/drool/releases), install with
> [Docker Compose](Install-With-Docker-Compose.md) or [Podman](Install-With-Podman.md), or build the
> archive yourself from the source code with `make release` (see the README's
> [Build and run from source](https://github.com/irnutsmurt/drool#build-and-run-from-source)). This
> page describes the steps for a release; `1.0.0` stands for its version.

At the end you'll have DROOL running as the `drool` system service, started at boot; the address of
its setup page, for example `https://10.10.0.10:8080/setup`; a one-time **setup code** (six digits
you enter once to create the first administrator account); and the **Set up DROOL** screen open in
your browser. The examples install DROOL on `drool.example.net`, address 10.10.0.10
([About the Examples](About-the-Examples.md)).

## Before you start

- **A 64-bit Linux machine with systemd**, amd64 or arm64. Run `uname -m`: `x86_64` means amd64,
  `aarch64` means arm64. Requirements in more detail:
  [Installing DROOL](Installing-DROOL.md#before-you-start).
- **`sudo`**: installing programs and a system service need administrator rights.

## Step 1: Download and check the archive

```sh
mkdir -p ~/drool-release
cd ~/drool-release
```

From the release on the [Releases page](https://github.com/irnutsmurt/drool/releases), download into
this folder the archive for your CPU, `drool_1.0.0_linux_amd64.tar.gz` or
`drool_1.0.0_linux_arm64.tar.gz`, and `SHA256SUMS`. In the terminal, for amd64:

```sh
curl -fLO https://github.com/irnutsmurt/drool/releases/download/v1.0.0/drool_1.0.0_linux_amd64.tar.gz
curl -fLO https://github.com/irnutsmurt/drool/releases/download/v1.0.0/SHA256SUMS
sha256sum --check --ignore-missing SHA256SUMS
```

The last command must print `drool_1.0.0_linux_amd64.tar.gz: OK`. If it says `FAILED`, download the
archive again and don't use it until it says `OK`. Official releases also sign `SHA256SUMS`:
[Checking a release's files](Installing-DROOL.md#checking-a-releases-files).

(Don't confuse it with `drool-agent_1.0.0_linux_amd64.tar.gz`: that one is the Shepherd alone, for the
machines DROOL manages.)

## Step 2: Unpack it

```sh
tar -xzf drool_1.0.0_linux_amd64.tar.gz
cd drool_1.0.0_linux_amd64
ls
```

`ls` shows the program `drool`, the two Shepherd programs DROOL hands to the hosts it manages
(`drool-agent-linux-amd64`, `drool-agent-linux-arm64`), the service file `drool.service`, an example
configuration and the documentation, including `INSTALL.md` with the commands of the next step.

## Step 3: Install it

Run these one after another, in the unpacked folder:

```sh
sudo install -m 0755 drool /usr/local/bin/drool
sudo install -d /usr/local/lib/drool
sudo install -m 0755 drool-agent-linux-amd64 drool-agent-linux-arm64 /usr/local/lib/drool/
sudo useradd --system --user-group --home-dir /var/lib/drool --no-create-home --shell /usr/sbin/nologin drool
sudo install -m 0644 drool.service /etc/systemd/system/drool.service
sudo systemctl daemon-reload
```

They copy the program to `/usr/local/bin`, the Shepherd programs to `/usr/local/lib/drool`, create a
`drool` system user that DROOL runs as (never root, and nobody can log in as it), and install the
service. If `useradd` says the user already exists, DROOL was installed here before: that's fine, go
on (but see [Where your data is](#where-your-data-is)).

## Step 4: Start DROOL

```sh
sudo systemctl enable --now drool
systemctl status drool --no-pager
```

`enable` makes DROOL start at every boot, `--now` starts it right away. The status should say
`Active: active (running)`; systemd creates `/var/lib/drool` for DROOL's data on the first start. If
it says `failed` or `activating (auto-restart)`, the log in the next step says why.

## Step 5: Find the address and the setup code

A system service doesn't print to your terminal. What it prints goes to the system **journal**, a log
that systemd keeps for every service, and `journalctl` shows it:

```sh
sudo journalctl -u drool -n 50
```

`-u drool` shows only DROOL's messages and `-n 50` the newest 50 lines. `sudo` is needed: the system
journal is for administrators. If the output is longer than your screen, scroll with the arrow keys
and press `q` to leave. The startup message is at the end; without the date and names at the start
of each line, it says:

```text
================================================================
 DROOL is ready.

 Open:
   https://10.10.0.10:8080/setup

 Setup code:
   482913

 Single use. Expires 15:25 UTC (in 60 min).
 New code: restart, or run `drool setup code`.
 …
================================================================
```

(The `…` stands for three lines about the certificate.)

- **`Open:`** lists this machine's own addresses with the setup page. If it lists several, use the one
  on the same network as the computer your browser runs on.
- **`Setup code:`** is the six-digit code. **`482913` is only this page's example: use the code your
  own DROOL printed.** It works once, for 60 minutes; the expiry time is in this machine's time zone.

[Install From a Package](Install-From-a-Package.md#step-6-find-the-address-and-the-setup-code) shows
the whole message as `journalctl` prints it.

## Step 6: Open the ports, if the machine has a firewall

DROOL never changes the firewall of the machine it runs on. If that machine runs one, open port 8080
(and 9443 if you'll use Shepherds); [Install From a Package](Install-From-a-Package.md#step-7-open-the-ports-if-the-machine-has-a-firewall)
shows how for firewalld and ufw.

## Step 7: Open the setup page

In your browser, go to the address from the startup message, for example
`https://10.10.0.10:8080/setup`. The browser warns that the connection isn't private: that's expected
(DROOL made its own certificate). Continue to the site (Chrome: **Advanced → Proceed to …**; Firefox:
**Advanced → Accept the Risk and Continue**), and you see **Set up DROOL**:

![The "Set up DROOL" page: setup code, admin username, password and confirm password fields, and the Create administrator button](images/first-login-setup.png)

DROOL is installed. Continue with [First Login](First-Login.md#2-create-the-administrator).
Can't connect? [Setup Problems](Setup-Problems.md#the-browser-cant-connect).

## If you lost the code, or it expired

`sudo journalctl -u drool -n 50` shows the startup message again. If the code is more than 60 minutes
old, already used, or you can't find it, ask DROOL for a new one:

```sh
sudo -u drool /usr/local/bin/drool setup code
```

```text
Setup code: 615027
Single use. Expires 15:40 UTC. Any previously generated code is no longer valid.
```

(Your code differs.) `sudo -u drool` runs the command as DROOL's own user. The full path
`/usr/local/bin/drool` matters on systems whose `sudo` doesn't look in `/usr/local/bin` (it then says
`command not found`). **The previous code stops working**: DROOL never stores a setup code itself,
so it can't show an old one again, only make a new one. The new code appears only here, not in the
journal.

Until someone creates the first administrator, **every start of DROOL also makes a new code** and the
previous one stops working: after `sudo systemctl restart drool`, a reboot, or when systemd restarted
DROOL after it stopped unexpectedly. Always use the newest code.

## After the first administrator exists

Setup happens once. Afterwards the startup message shows no code and `Open:` has no `/setup`;
`/setup` takes you to **Sign in to DROOL**; and `drool setup code` refuses with *setup is already
complete (an administrator exists)*. Add other people under **Settings → Users**
([Users and Roles](Users-and-Roles.md)). Locked out? See
[First Login](First-Login.md#if-you-forget-your-password).

## Everyday commands

| To | Run |
|---|---|
| See whether DROOL is running | `systemctl status drool --no-pager` |
| See its startup messages | `sudo journalctl -u drool -n 50` |
| Stop, start, restart DROOL | `sudo systemctl stop drool`, `… start drool`, `… restart drool` |
| Run a `drool` command | `sudo -u drool /usr/local/bin/drool <command>` ([Command Line](Command-Line.md)) |
| Change a setting | `/etc/drool/config.yaml` ([Configuration](Configuration.md)) |
| Update DROOL | [Updating DROOL](Updating-DROOL.md) |
| Back it up | [Native Installation Backup](Native-Installation-Backup.md) |

## Where your data is

Everything DROOL stores (database, [master key](Master-Key.md), certificate, backups, logs) is in
`/var/lib/drool`. If DROOL was installed on this machine before, that folder may already hold an
earlier installation's data, accounts included: DROOL then uses it and doesn't ask for setup
([Setup Problems](Setup-Problems.md#drool-says-setup-is-already-complete)).

**Removing DROOL:** `sudo systemctl disable --now drool`, then delete `/usr/local/bin/drool`,
`/usr/local/lib/drool` and `/etc/systemd/system/drool.service`. Your data in `/var/lib/drool` stays
until you delete it yourself.

## Something went wrong?

See [Setup Problems](Setup-Problems.md).

## Next

[First Login](First-Login.md): enter the setup code and create your administrator account.
