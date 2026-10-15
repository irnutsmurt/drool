# DROOL User Guide

<!-- docs-version: when a stable release is published, say which one this guide describes, here and
at the top of _Sidebar.md (RELEASING.md lists this step). -->
> **This guide describes DROOL's development version**: the `main` branch, which the development image
> `ghcr.io/irnutsmurt/drool:main` runs. DROOL has no stable release yet.
> [Development image or release?](Installing-DROOL.md#development-image-or-release)

**DROOL** looks after a handful to a few dozen Linux machines from one web page: your homelab, a small
office, a club's servers. You install it once, open it in a browser, and from there you can see how
every machine is doing and change things on them, safely and with a record of who did what.

![DROOL's Overview for the example network: 6 hosts, 2 open alerts (1 critical, 1 warning, acknowledged), 4 hosts drifted from their Desired State, the firewall in sync; what needs attention, the latest alerts and runs, updates and reboots (a failed systemd unit as a fact: only monitored services alert), security advice and recent events](images/overview-fleet.png)

## What it can do

- **See every machine's health**: CPU, memory, disks and their health, temperatures, GPUs, services,
  with history and plain-English problems ([Fleet Monitoring](Fleet-Monitoring.md)).
- **Tell you when something's wrong**, by email, Discord, Slack, Telegram or webhook, and when it's fixed
  ([Understanding Alerts](Understanding-Alerts.md)).
- **Run work on many machines at once**: commands, scripts, packages, updates, reboots, services, with
  each machine's output and careful rollouts ([Running a Command](Running-a-Command.md)).
- **Manage files**: browse, edit, upload, and deploy a file to many machines with validation and
  rollback ([Browsing Files](Browsing-Files.md)).
- **Automate**: schedules, reusable workflows, maintenance windows ([Schedules](Schedules.md)).
- **Keep machines the way they should be**: services, packages and files, with drift detection and safe
  restore ([What Desired State Is](What-Desired-State-Is.md)).
- **Manage firewalls from intent** ("web-servers may reach database-01 on PostgreSQL"), applied so that
  a mistake can't lock you out ([What DROOL's Firewall Does](What-DROOLs-Firewall-Does.md)).
- **See network activity**: what talks to what, and what your firewall says about it
  ([What DROOL Can See](What-DROOL-Can-See.md)).

More: [What DROOL Does](What-DROOL-Does.md).

## New here? Start with these

1. [Installing DROOL](Installing-DROOL.md): today the quickest way is
   [Docker Compose](Install-With-Docker-Compose.md), step by step from nothing installed (on a
   Synology NAS: [Install on a Synology NAS](Install-On-a-Synology-NAS.md), without a terminal)
2. [First Login](First-Login.md)
3. [Your First Host](Your-First-Host.md): connect a machine, run your first command on it and see it
   in Monitoring
4. [Secure Your Installation](Secure-Your-Installation.md)
5. [Backup and Recovery](Backup-and-Recovery.md): set up backups before you rely on DROOL

All examples follow one small, made-up network (`drool.example.net`, `web-01`, `media-01`, …): see
[About the Examples](About-the-Examples.md).

## Something isn't working?

Go to [Troubleshooting](Troubleshooting.md): find what you're seeing, and follow the link. The most
common ones:

- Installing, the setup code, or the first visit: [Setup Problems](Setup-Problems.md)
- A host is unreachable: [Host Won't Connect](Host-Wont-Connect.md)
- *Host key changed*: [SSH Host Key Changed](SSH-Host-Key-Changed.md)
- A run says *outcome unknown*: [Run Outcome Unknown](Run-Outcome-Unknown.md) (it does **not** mean
  failed)
- Anything says *Unknown*: [What "Unknown" Means](What-Unknown-Means.md)
- A firewall change was rolled back: [Firewall Apply Rolled Back](Firewall-Apply-Rolled-Back.md)

## Find what you need

| I want to… | Start here |
|---|---|
| Add machines, and group them | [Adding a Host](Adding-a-Host.md) · [Packs](Packs.md) |
| See how my machines are doing | [Fleet Monitoring](Fleet-Monitoring.md) |
| Be told when something breaks | [Understanding Alerts](Understanding-Alerts.md) · [Email Notifications](Email-Notifications.md) |
| Run commands, install updates, reboot | [Running a Command](Running-a-Command.md) · [System Updates](System-Updates.md) |
| Look at or change files on my machines | [Browsing Files](Browsing-Files.md) · [Deploying a File to Multiple Hosts](Deploying-a-File-to-Multiple-Hosts.md) |
| Do routine work automatically | [Schedules](Schedules.md) · [Workflows](Workflows.md) |
| Keep machines the way they should be | [What Desired State Is](What-Desired-State-Is.md) |
| Control which machines may talk to each other | [What DROOL's Firewall Does](What-DROOLs-Firewall-Does.md) · [What DROOL Can See](What-DROOL-Can-See.md) |
| Keep DROOL itself safe, backed up and up to date | [Secure Your Installation](Secure-Your-Installation.md) · [Backup and Recovery](Backup-and-Recovery.md) · [Updating DROOL](Updating-DROOL.md) |
| Look up a word, a status or a command | [Glossary](Glossary.md) · [Status Meanings](Status-Meanings.md) · [Command Line](Command-Line.md) |

Every page is listed in the sidebar, by topic.

This guide describes DROOL as it is today, including its limits. For the design, the implementation
record and how releases are made, see the project's [README](https://github.com/irnutsmurt/drool#readme),
which links to all three.
