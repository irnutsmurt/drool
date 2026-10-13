**[DROOL User Guide](Home.md)**

<!-- docs-version: see the note at the top of Home.md. -->
*For the development version (`main`); no stable release yet.*

**[Troubleshooting](Troubleshooting.md)** · **[Glossary](Glossary.md)**

<details open>
<summary><b>Getting started</b></summary>

- [What DROOL Does](What-DROOL-Does.md)
- [Installing DROOL](Installing-DROOL.md)
  - [Docker Compose](Install-With-Docker-Compose.md)
  - [Podman](Install-With-Podman.md)
  - [Package (.deb, .rpm)](Install-From-a-Package.md)
  - [Release Archive](Install-From-the-Release-Archive.md)
- [First Login](First-Login.md)
- [Your First Host](Your-First-Host.md)
- [SSH vs Shepherds](SSH-vs-Shepherds.md)
- [Secure Your Installation](Secure-Your-Installation.md)
- [Updating DROOL](Updating-DROOL.md)
- [About the Examples](About-the-Examples.md)

</details>

<details>
<summary><b>Managing hosts</b></summary>

- [Adding a Host](Adding-a-Host.md)
- [DROOL's Own SSH Account](Dedicated-SSH-Account.md)
- [SSH Credentials](SSH-Credentials.md)
- [SSH Host Key Verification](SSH-Host-Key-Verification.md)
- [Testing a Connection](Testing-a-Connection.md)
- [Host Details](Host-Details.md)
- [Packs](Packs.md)
- [Tags](Tags.md)
- [Jump Hosts](Jump-Hosts.md)
- [Installing a Shepherd](Installing-a-Shepherd.md)
- [Shepherd Capability Presets](Shepherd-Capability-Presets.md)
- [SSH Fallback](SSH-Fallback.md)
- [Updating Shepherds](Updating-Shepherds.md)
- [Removing a Host](Removing-a-Host.md)

</details>

<details>
<summary><b>Monitoring</b></summary>

- [Fleet Monitoring](Fleet-Monitoring.md)
- [Host Monitoring](Host-Monitoring.md)
- [CPU, Memory and Load](CPU-Memory-and-Load.md)
- [Filesystems and Disk Space](Filesystems-and-Disk-Space.md)
- [Storage Health](Storage-Health.md)
- [Temperatures](Temperatures.md)
- [GPUs](GPUs.md)
- [Service Monitoring](Service-Monitoring.md)
- [Monitoring Settings](Monitoring-Settings.md)
- [Health Overrides](Health-Overrides.md)
- [SSH vs Shepherd Monitoring](Monitoring-Through-SSH-vs-Shepherd.md)
- [What "Unknown" Means](What-Unknown-Means.md)

</details>

<details>
<summary><b>Alerts</b></summary>

- [Understanding Alerts](Understanding-Alerts.md)
- [Acknowledging an Alert](Acknowledging-an-Alert.md)
- [Maintenance Mode](Maintenance-Mode.md)
- [Email Notifications](Email-Notifications.md)
- [Discord Notifications](Discord-Notifications.md)
- [Slack Notifications](Slack-Notifications.md)
- [Webhooks](Webhooks.md)
- [Notification Routing](Notification-Routing.md)
- [Recovered vs Cleared](Recovered-vs-Cleared-by-Configuration.md)

</details>

<details>
<summary><b>Running work</b></summary>

- [Running a Command](Running-a-Command.md)
- [Running a Script](Running-a-Script.md)
- [Installing Packages](Installing-Packages.md)
- [Removing Packages](Removing-Packages.md)
- [Updating Packages](Updating-Packages.md)
- [System Updates](System-Updates.md)
- [Rebooting Hosts](Rebooting-Hosts.md)
- [Managing Services](Managing-Services.md)
- [Understanding Run Results](Understanding-Run-Results.md)
- [Failed, Interrupted and Unknown](Failed-Interrupted-and-Unknown-Outcomes.md)

</details>

<details>
<summary><b>Files</b></summary>

- [Browsing Files](Browsing-Files.md)
- [Uploading and Downloading](Uploading-and-Downloading.md)
- [Editing Files](Editing-Files.md)
- [Administrator Privileges](Administrator-Privileges.md)
- [Permissions and Ownership](Permissions-and-Ownership.md)
- [Deploying a File to Many Hosts](Deploying-a-File-to-Multiple-Hosts.md)
- [Validation Before Deployment](Validation-Before-Deployment.md)
- [Reloading or Restarting a Service](Reloading-or-Restarting-a-Service.md)
- [Rolling Back a File](Rolling-Back-a-File.md)

</details>

<details>
<summary><b>Automation</b></summary>

- [Schedules](Schedules.md)
- [Workflows](Workflows.md)
- [Canary Hosts](Canary-Hosts.md)
- [Batches and Failure Thresholds](Batches-and-Failure-Thresholds.md)
- [Maintenance Windows](Maintenance-Windows.md)
- [Missed Runs](Missed-Runs.md)
- [Workflow Revisions](Workflow-Revisions.md)

</details>

<details>
<summary><b>Desired State</b></summary>

- [What Desired State Is](What-Desired-State-Is.md)
- [Creating a Baseline](Creating-a-Baseline.md)
- [Applying a Baseline](Applying-a-Baseline.md)
- [Drift](Drift.md)
- [Conflicts](Conflicts.md)
- [Review Difference](Review-Difference.md)
- [Accept Current State](Accept-Current-State.md)
- [Restore Managed State](Restore-Managed-State.md)
- [Automatic Restore](Automatic-Restore.md)

</details>

<details>
<summary><b>Firewall</b></summary>

- [What DROOL's Firewall Does](What-DROOLs-Firewall-Does.md)
- [Before Enabling Enforcement](Before-Enabling-Enforcement.md)
- [Firewall Modes](Firewall-Modes.md)
- [Firewall Policy](Firewall-Policy.md)
- [Topology View](Topology-View.md)
- [Rules View](Rules-View.md)
- [YAML View](YAML-View.md)
- [Networks and CIDRs](Networks-and-CIDRs.md)
- [Services and Ports](Services-and-Ports.md)
- [Packs in Firewall Rules](Packs-in-Firewall-Rules.md)
- [Applying the Policy](Applying-the-Firewall-Policy.md)
- [Management Path Protection](Management-Path-Protection.md)
- [Automatic Rollback Protection](Automatic-Rollback-Protection.md)
- [Can A Reach B?](Can-A-Reach-B.md)
- [Firewall Drift](Firewall-Drift.md)
- [Emergency Override](Emergency-Override.md)
- [Recovering From a Problem](Recovering-From-a-Firewall-Problem.md)

</details>

<details>
<summary><b>Network Activity</b></summary>

- [What DROOL Can See](What-DROOL-Can-See.md)
- [Shepherd vs SSH Visibility](Agent-vs-SSH-Visibility.md)
- [Connection Details](Connection-Details.md)
- [Internal vs External](Internal-vs-External.md)
- [GeoIP and ASN](GeoIP-and-ASN.md)
- [Policy Classification](Policy-Classification.md)
- [Policy From Observed Traffic](Creating-a-Policy-From-Observed-Traffic.md)

</details>

<details>
<summary><b>Security</b></summary>

- [Two-Factor Authentication](Two-Factor-Authentication.md)
- [Re-authentication](Re-authentication.md)
- [Users and Roles](Users-and-Roles.md)
- [API Tokens](API-Tokens.md)
- [Short-Lived SSH Certificates](Short-Lived-SSH-Certificates.md)
- [Moving Hosts to SSH Certificates](Moving-Hosts-to-SSH-Certificates.md)
- [SSH CA Rotation](SSH-CA-Rotation.md)
- [Shepherd PKI](Shepherd-PKI.md)
- [Agent Root CA Offline](Taking-the-Agent-Root-CA-Offline.md)
- [Audit Log](Audit-Log.md)
- [Remote Audit Copy](Remote-Audit-Copy.md)
- [The Master Key](Master-Key.md)
- [Key Rotation](Key-Rotation.md)

</details>

<details>
<summary><b>Backup and recovery</b></summary>

- [Backup and Recovery](Backup-and-Recovery.md)
- [What a Backup Contains](What-a-Backup-Contains.md)
- [What It Doesn't Contain](What-a-Backup-Does-Not-Contain.md)
- [Docker Backup](Docker-Backup.md)
- [Native Installation Backup](Native-Installation-Backup.md)
- [Podman Backup](Podman-Backup.md)
- [Verifying a Backup](Verifying-a-Backup.md)
- [Restoring a Controller](Restoring-a-Controller.md)
- [What Happens After Restore](What-Happens-After-Restore.md)
- [Host Reconciliation](Host-Reconciliation.md)
- [Recovering Without Credentials](Recovering-Without-Credentials.md)

</details>

<details>
<summary><b>Troubleshooting</b></summary>

- [Troubleshooting (start here)](Troubleshooting.md)
- [Setup Problems](Setup-Problems.md)
- [Host Won't Connect](Host-Wont-Connect.md)
- [SSH Host Key Changed](SSH-Host-Key-Changed.md)
- [Authentication Failed](Authentication-Failed.md)
- [Sudo Failed](Sudo-Failed.md)
- [Shepherd Offline](Shepherd-Offline.md)
- [Shepherd Incompatible](Shepherd-Incompatible.md)
- [Package Manager Busy](Package-Manager-Busy.md)
- [dpkg Was Interrupted](dpkg-Was-Interrupted.md)
- [Service Won't Start](Service-Wont-Start.md)
- [Firewall Apply Rolled Back](Firewall-Apply-Rolled-Back.md)
- [Host Shows Unknown](Host-Shows-Unknown.md)
- [Monitoring Is Stale](Monitoring-Is-Stale.md)
- [Notification Failed](Notification-Failed.md)
- [Run Outcome Unknown](Run-Outcome-Unknown.md)
- [Restore Needs Attention](Restore-Needs-Attention.md)

</details>

<details>
<summary><b>Reference</b></summary>

- [Glossary](Glossary.md)
- [Supported Platforms](Supported-Platforms.md)
- [Status Meanings](Status-Meanings.md)
- [Ports and Files](Ports-and-Files.md)
- [Configuration](Configuration.md)
- [Command Line](Command-Line.md)
- [Webhook Reference](Webhook-Reference.md)
- [Remote Audit Copy Reference](Remote-Audit-Copy-Reference.md)

</details>
