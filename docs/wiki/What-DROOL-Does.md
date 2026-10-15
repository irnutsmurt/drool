# What DROOL does

DROOL is a web application for looking after a handful to a few dozen Linux machines from one
place: your homelab, a small office, a club's servers. You install it once, on one machine (the
**controller**), open it in a browser, and from there you can see how every machine is doing and
change things on them, safely and with a record of who did what.

DROOL stands for *Distributed Remote Operations & Observability Layer*, but you never need to know
that.

## How it reaches your machines

Every Linux machine DROOL looks after is called a **host**. DROOL can reach a host in two ways, and
you choose per host:

- **Over SSH**, the same way you log in yourself. No software is installed on the host. DROOL logs
  in as its own account, which it creates with an account you already have there (in this wiki's
  examples, `drooladmin`), and runs ordinary commands.
- **Through a Shepherd**, DROOL's optional agent. A small program on the host connects *out* to
  DROOL, so the host needs no open port, and it can report monitoring data continuously. The host
  itself decides what a Shepherd is allowed to do.

You can start with SSH everywhere and add Shepherds later. [SSH vs Shepherds](SSH-vs-Shepherds.md)
explains the difference in detail.

## What you can do with it

| Area | What it's for | Start here |
|---|---|---|
| **Hosts** | Add machines, check DROOL can reach them, see what they are (OS, disks, network, services) | [Your First Host](Your-First-Host.md) |
| **Monitoring** | CPU, memory, disks, temperatures, GPUs, services and disk health, with history | [Fleet Monitoring](Fleet-Monitoring.md) |
| **Alerts** | Be told when something is wrong (email, Discord, Slack, Telegram, webhook), and keep quiet during maintenance | [Understanding Alerts](Understanding-Alerts.md) |
| **Runs** | Run a command or script on many machines, install and remove packages, install updates, restart services, reboot | [Running a Command](Running-a-Command.md) |
| **Files** | Browse, edit, upload and download files; send one file to many machines with a preview and a rollback | [Browsing Files](Browsing-Files.md) |
| **Automation** | Schedules ("every Sunday at 3:00") and workflows ("update, reboot if needed, check the service is running") | [Schedules](Schedules.md) |
| **Desired State** | Say what a machine should look like (these services running, these packages installed, this file with this content) and see when it drifts | [What Desired State Is](What-Desired-State-Is.md) |
| **Firewall** | Describe who may talk to whom ("the web servers may reach the database on PostgreSQL"); DROOL turns it into firewall rules and applies them without locking you out | [What DROOL's Firewall Does](What-DROOLs-Firewall-Does.md) |
| **Network Activity** | See what your machines actually talk to | [What DROOL Can See](What-DROOL-Can-See.md) |
| **Security** | Accounts and roles, two-factor sign-in, an audit log of every change, encrypted credentials, short-lived SSH certificates | [Secure Your Installation](Secure-Your-Installation.md) |

## How DROOL behaves (the short version)

A few habits run through the whole product. Knowing them up front saves surprises:

- **It shows you before it changes things.** Package changes are simulated first. File deployments
  show a per-host preview and diff. Firewall policies show which hosts would change. Desired State
  shows exactly what a correction would do before it runs.
- **It never guesses when it can't know.** If a connection drops while a command runs, DROOL
  reports the outcome as **unknown** and doesn't run it again on its own. See
  [Failed, Interrupted and Unknown Outcomes](Failed-Interrupted-and-Unknown-Outcomes.md).
- **It trusts machines by their keys, not their addresses.** DROOL only connects to a host after you
  approved its SSH host key, and it refuses to connect if that key ever changes until you look at it.
- **It keeps a record.** Every change, by a person or a schedule, is in the
  [Audit Log](Audit-Log.md).
- **Your secrets are encrypted.** Stored passwords and keys are encrypted with a *master key* that
  lives next to DROOL's data. Keep a copy of it somewhere safe: see
  [The Master Key](Master-Key.md).

## Words DROOL uses

Three names in the interface are DROOL's own. The rest of this wiki uses them, so here they are once:

- A **Pack** is a *group of hosts* (for example `media-servers`).
- A **Shepherd** is the *DROOL agent* installed on a host (the program is called `drool-agent`).
- A **Run** is a *job*: anything DROOL does on hosts, from a one-line command to a scheduled update.

The [Glossary](Glossary.md) has the rest.

## What DROOL doesn't do (yet)

So you don't go looking for it:

- **Platforms:** Linux only. Windows is a future goal; macOS and BSD aren't planned.
- **Scale:** one controller with one database, no high availability; tested with tens of hosts, not
  thousands ([Supported Platforms](Supported-Platforms.md)).
- **Accounts:** local accounts only, no single sign-on. Two-factor sign-in uses authenticator apps; no
  WebAuthn or passkeys yet.
- **Firewall:** plain-nftables hosts only: not hosts that run Docker or other container networks,
  firewalld, UFW or Tailscale. No WireGuard identity networking.
- **Network Activity:** it shows traffic; it doesn't block addresses or countries, score reputation,
  capture packets, detect intrusions or alert on network activity.
- **Desired State:** services, packages and files only (no users, cron, sysctl or containers, no
  package versions); no compliance benchmarks (CIS, STIG), policy expressions or remediation scripts.
- **Files:** no templated files or directory sync.
- **Automation:** workflows have no branches, conditions or approval steps, and run results aren't sent
  as notifications.
- **Alerts:** no on-call rotations, escalation or paging, incident grouping, alert rule expressions,
  custom dashboards or log collection. No SMS or push apps: a [webhook](Webhooks.md) can feed ntfy,
  Gotify or Home Assistant.
- **Extensions:** no plugins yet.
- **DROOL itself:** no package repository for installing it, it doesn't update itself, and backups are
  made from the command line.

Each page mentions the limits that matter for that task.

## Next

- [Installing DROOL](Installing-DROOL.md)
- Already installed? [First Login](First-Login.md)
