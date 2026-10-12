# DROOL User Guide

**DROOL** looks after a handful to a few dozen Linux machines from one web page: your homelab, a small
office, a club's servers. You install it once, open it in a browser, and from there you can see how
every machine is doing and change things on them, safely and with a record of who did what.

![DROOL's Overview for the example network: 6 hosts, 6 open alerts, 2 hosts drifted from their Desired State, the firewall in sync; what needs attention, the latest alerts and runs, updates, security advice and recent events](images/overview-fleet.png)

## What it can do

- **See every machine's health**: CPU, memory, disks and their health, temperatures, GPUs, services,
  with history and plain-English problems ([Fleet Monitoring](Fleet-Monitoring.md)).
- **Tell you when something's wrong**, by email, Discord, Slack or webhook, and when it's fixed
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

1. [Installing DROOL](Installing-DROOL.md)
2. [First Login](First-Login.md)
3. [Your First Host](Your-First-Host.md)
4. [Secure Your Installation](Secure-Your-Installation.md)
5. [Backup and Recovery](Backup-and-Recovery.md): set up backups before you rely on DROOL

All examples follow one small, made-up network (`drool.example.net`, `web-01`, `media-01`, …): see
[About the Examples](About-the-Examples.md).

## Something isn't working?

Go to [Troubleshooting](Troubleshooting.md): find what you're seeing, and follow the link. The most
common ones:

- A host is unreachable: [Host Won't Connect](Host-Wont-Connect.md)
- *Host key changed*: [SSH Host Key Changed](SSH-Host-Key-Changed.md)
- A run says *outcome unknown*: [Run Outcome Unknown](Run-Outcome-Unknown.md) (it does **not** mean
  failed)
- Anything says *Unknown*: [What "Unknown" Means](What-Unknown-Means.md)
- A firewall change was rolled back: [Firewall Apply Rolled Back](Firewall-Apply-Rolled-Back.md)

## The guide

| Section | Pages |
|---|---|
| **Getting started** | [What DROOL Does](What-DROOL-Does.md) · [Installing DROOL](Installing-DROOL.md) · [First Login](First-Login.md) · [Your First Host](Your-First-Host.md) · [SSH vs Shepherds](SSH-vs-Shepherds.md) · [Secure Your Installation](Secure-Your-Installation.md) · [Updating DROOL](Updating-DROOL.md) · [About the Examples](About-the-Examples.md) |
| **Managing hosts** | [Adding a Host](Adding-a-Host.md) · [SSH Credentials](SSH-Credentials.md) · [SSH Host Key Verification](SSH-Host-Key-Verification.md) · [Testing a Connection](Testing-a-Connection.md) · [Host Details](Host-Details.md) · [Packs](Packs.md) · [Tags](Tags.md) · [Jump Hosts](Jump-Hosts.md) · [Installing a Shepherd](Installing-a-Shepherd.md) · [Shepherd Capability Presets](Shepherd-Capability-Presets.md) · [SSH Fallback](SSH-Fallback.md) · [Updating Shepherds](Updating-Shepherds.md) · [Removing a Host](Removing-a-Host.md) |
| **Monitoring** | [Fleet Monitoring](Fleet-Monitoring.md) · [Host Monitoring](Host-Monitoring.md) · [CPU, Memory and Load](CPU-Memory-and-Load.md) · [Filesystems and Disk Space](Filesystems-and-Disk-Space.md) · [Storage Health](Storage-Health.md) · [Temperatures](Temperatures.md) · [GPUs](GPUs.md) · [Service Monitoring](Service-Monitoring.md) · [Monitoring Settings](Monitoring-Settings.md) · [Health Overrides](Health-Overrides.md) · [SSH vs Shepherd Monitoring](Monitoring-Through-SSH-vs-Shepherd.md) · [What "Unknown" Means](What-Unknown-Means.md) |
| **Alerts** | [Understanding Alerts](Understanding-Alerts.md) · [Acknowledging an Alert](Acknowledging-an-Alert.md) · [Maintenance Mode](Maintenance-Mode.md) · [Email](Email-Notifications.md) · [Discord](Discord-Notifications.md) · [Slack](Slack-Notifications.md) · [Webhooks](Webhooks.md) · [Notification Routing](Notification-Routing.md) · [Recovered vs Cleared by Configuration](Recovered-vs-Cleared-by-Configuration.md) |
| **Running work** | [Running a Command](Running-a-Command.md) · [Running a Script](Running-a-Script.md) · [Installing Packages](Installing-Packages.md) · [Removing Packages](Removing-Packages.md) · [Updating Packages](Updating-Packages.md) · [System Updates](System-Updates.md) · [Rebooting Hosts](Rebooting-Hosts.md) · [Managing Services](Managing-Services.md) · [Understanding Run Results](Understanding-Run-Results.md) · [Failed, Interrupted and Unknown Outcomes](Failed-Interrupted-and-Unknown-Outcomes.md) |
| **Files** | [Browsing Files](Browsing-Files.md) · [Uploading and Downloading](Uploading-and-Downloading.md) · [Editing Files](Editing-Files.md) · [Administrator Privileges](Administrator-Privileges.md) · [Permissions and Ownership](Permissions-and-Ownership.md) · [Deploying a File to Multiple Hosts](Deploying-a-File-to-Multiple-Hosts.md) · [Validation Before Deployment](Validation-Before-Deployment.md) · [Reloading or Restarting a Service](Reloading-or-Restarting-a-Service.md) · [Rolling Back a File](Rolling-Back-a-File.md) |
| **Automation** | [Schedules](Schedules.md) · [Workflows](Workflows.md) · [Canary Hosts](Canary-Hosts.md) · [Batches and Failure Thresholds](Batches-and-Failure-Thresholds.md) · [Maintenance Windows](Maintenance-Windows.md) · [Missed Runs](Missed-Runs.md) · [Workflow Revisions](Workflow-Revisions.md) |
| **Desired State** | [What Desired State Is](What-Desired-State-Is.md) · [Creating a Baseline](Creating-a-Baseline.md) · [Applying a Baseline](Applying-a-Baseline.md) · [Drift](Drift.md) · [Conflicts](Conflicts.md) · [Review Difference](Review-Difference.md) · [Accept Current State](Accept-Current-State.md) · [Restore Managed State](Restore-Managed-State.md) · [Automatic Restore](Automatic-Restore.md) |
| **Firewall** | [What DROOL's Firewall Does](What-DROOLs-Firewall-Does.md) · [Before Enabling Enforcement](Before-Enabling-Enforcement.md) · [Firewall Modes](Firewall-Modes.md) · [Firewall Policy](Firewall-Policy.md) · [Topology View](Topology-View.md) · [Rules View](Rules-View.md) · [YAML View](YAML-View.md) · [Networks and CIDRs](Networks-and-CIDRs.md) · [Services and Ports](Services-and-Ports.md) · [Packs in Firewall Rules](Packs-in-Firewall-Rules.md) · [Applying the Firewall Policy](Applying-the-Firewall-Policy.md) · [Management Path Protection](Management-Path-Protection.md) · [Automatic Rollback Protection](Automatic-Rollback-Protection.md) · [Can A Reach B?](Can-A-Reach-B.md) · [Firewall Drift](Firewall-Drift.md) · [Emergency Override](Emergency-Override.md) · [Recovering From a Firewall Problem](Recovering-From-a-Firewall-Problem.md) |
| **Network Activity** | [What DROOL Can See](What-DROOL-Can-See.md) · [Shepherd vs SSH Visibility](Agent-vs-SSH-Visibility.md) · [Connection Details](Connection-Details.md) · [Internal vs External](Internal-vs-External.md) · [GeoIP and ASN](GeoIP-and-ASN.md) · [Policy Classification](Policy-Classification.md) · [Creating a Policy From Observed Traffic](Creating-a-Policy-From-Observed-Traffic.md) |
| **Security** | [Two-Factor Authentication](Two-Factor-Authentication.md) · [Re-authentication](Re-authentication.md) · [Users and Roles](Users-and-Roles.md) · [API Tokens](API-Tokens.md) · [Short-Lived SSH Certificates](Short-Lived-SSH-Certificates.md) · [Moving Hosts to SSH Certificates](Moving-Hosts-to-SSH-Certificates.md) · [SSH CA Rotation](SSH-CA-Rotation.md) · [Shepherd PKI](Shepherd-PKI.md) · [Taking the Agent Root CA Offline](Taking-the-Agent-Root-CA-Offline.md) · [Audit Log](Audit-Log.md) · [Remote Audit Copy](Remote-Audit-Copy.md) · [The Master Key](Master-Key.md) · [Key Rotation](Key-Rotation.md) |
| **Backup and recovery** | [Backup and Recovery](Backup-and-Recovery.md) · [What a Backup Contains](What-a-Backup-Contains.md) · [What a Backup Does Not Contain](What-a-Backup-Does-Not-Contain.md) · [Docker](Docker-Backup.md) · [Native Installation](Native-Installation-Backup.md) · [Podman](Podman-Backup.md) · [Verifying a Backup](Verifying-a-Backup.md) · [Restoring a Controller](Restoring-a-Controller.md) · [What Happens After Restore](What-Happens-After-Restore.md) · [Host Reconciliation](Host-Reconciliation.md) · [Recovering Without Credentials](Recovering-Without-Credentials.md) |
| **Troubleshooting** | [Troubleshooting](Troubleshooting.md) (start here) · [Host Won't Connect](Host-Wont-Connect.md) · [SSH Host Key Changed](SSH-Host-Key-Changed.md) · [Authentication Failed](Authentication-Failed.md) · [Sudo Failed](Sudo-Failed.md) · [Shepherd Offline](Shepherd-Offline.md) · [Shepherd Incompatible](Shepherd-Incompatible.md) · [Package Manager Busy](Package-Manager-Busy.md) · [dpkg Was Interrupted](dpkg-Was-Interrupted.md) · [Service Won't Start](Service-Wont-Start.md) · [Firewall Apply Rolled Back](Firewall-Apply-Rolled-Back.md) · [Host Shows Unknown](Host-Shows-Unknown.md) · [Monitoring Is Stale](Monitoring-Is-Stale.md) · [Notification Failed](Notification-Failed.md) · [Run Outcome Unknown](Run-Outcome-Unknown.md) · [Restore Needs Attention](Restore-Needs-Attention.md) |
| **Reference** | [Glossary](Glossary.md) · [Status Meanings](Status-Meanings.md) · [Ports and Files](Ports-and-Files.md) · [Configuration](Configuration.md) · [Command Line](Command-Line.md) |

This guide describes DROOL as it is today, including its limits. For developers and the full technical
detail, see the project's [README](https://github.com/irnutsmurt/drool#readme).
