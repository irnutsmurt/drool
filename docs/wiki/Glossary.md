# Glossary

The words DROOL uses, in plain English.

| Word | Meaning |
|---|---|
| **Acknowledge** | Say "I'm on it" about an alert. Stops reminders; doesn't close it ([Acknowledging an Alert](Acknowledging-an-Alert.md)) |
| **Alert** | DROOL telling you about a Warning or Critical health issue ([Understanding Alerts](Understanding-Alerts.md)) |
| **Baseline** | A named list of what hosts should have: services, packages, files ([Creating a Baseline](Creating-a-Baseline.md)) |
| **Batch** | A group of hosts that run together in a rollout, after the canaries ([Batches and Failure Thresholds](Batches-and-Failure-Thresholds.md)) |
| **Canary** | A host that tries a change first, alone ([Canary Hosts](Canary-Hosts.md)) |
| **Certificate mode** | A host DROOL signs in to with short-lived SSH certificates instead of a stored key ([Short-Lived SSH Certificates](Short-Lived-SSH-Certificates.md)) |
| **Controller** | The machine (or container) DROOL itself runs on: `drool.example.net` in the examples |
| **Controller epoch** | An identity for "this controller since its last restore". Hosts only take commands from the current epoch once reconciled ([What Happens After Restore](What-Happens-After-Restore.md)) |
| **Credential** | Something DROOL signs in with: an SSH key, a password, or a sudo password ([SSH Credentials](SSH-Credentials.md)) |
| **Desired State** | DROOL's feature for keeping hosts the way baselines say ([What Desired State Is](What-Desired-State-Is.md)) |
| **Destination** | Where notifications go: email, Discord, Slack, Telegram, a webhook ([Notification Routing](Notification-Routing.md)) |
| **Development image** | The container image `ghcr.io/irnutsmurt/drool:main`, built automatically from the newest DROOL code that passed its tests. Not a release ([Installing DROOL](Installing-DROOL.md#development-image-or-release)) |
| **Drift** | A host no longer matches its baselines ([Drift](Drift.md)), or DROOL's firewall rules were changed outside DROOL ([Firewall Drift](Firewall-Drift.md)) |
| **DROOL's own SSH account** | The account DROOL creates on a host (named `drool` unless you choose another name; `drooladmin` in the examples), signing in with DROOL's key and using passwordless sudo, so root-equivalent ([DROOL's Own SSH Account](Dedicated-SSH-Account.md)) |
| **Flow** | One kind of connection in Network Activity: a host, direction, protocol, local address, service port and remote address ([What DROOL Can See](What-DROOL-Can-See.md)) |
| **Health issue** | A problem monitoring found: Info, Warning or Critical ([Host Monitoring](Host-Monitoring.md)) |
| **Host** | A Linux machine DROOL manages |
| **Host key** | A host's SSH identity. DROOL connects only to hosts whose key you approved ([SSH Host Key Verification](SSH-Host-Key-Verification.md)) |
| **Journal** | Where systemd keeps what services print, including DROOL's startup message on a native installation. `journalctl` shows it ([Install From a Package](Install-From-a-Package.md#step-6-find-the-address-and-the-setup-code)) |
| **Jump host** | A host DROOL connects through to reach others ([Jump Hosts](Jump-Hosts.md)) |
| **Maintenance** | A period during which DROOL sends no notifications for some hosts ([Maintenance Mode](Maintenance-Mode.md)) |
| **Management path** | DROOL's own way into a host (SSH from the controller, or the Shepherd's connection), protected by the firewall ([Management Path Protection](Management-Path-Protection.md)) |
| **Master key** | The file that encrypts DROOL's secrets ([The Master Key](Master-Key.md)) |
| **Operator** | A user role: runs things, can't change security settings ([Users and Roles](Users-and-Roles.md)) |
| **Pack** | A named group of hosts, such as `media-servers` ([Packs](Packs.md)). Called *groups* in the API and `group:` in firewall YAML |
| **Policy** (firewall) | The rules for who may reach whom ([Firewall Policy](Firewall-Policy.md)) |
| **Preset** | What a Shepherd may do on its host: Monitor, Managed or Full ([Shepherd Capability Presets](Shepherd-Capability-Presets.md)) |
| **Reconciliation** | Comparing each host with a restored backup before managing it again ([Host Reconciliation](Host-Reconciliation.md)) |
| **Release** | A fixed, published version of DROOL, with files that never change once published ([Installing DROOL](Installing-DROOL.md#development-image-or-release)) |
| **Revision** | A saved version of a workflow, baseline or firewall policy. Revisions never change |
| **Rollback timeout** | How long a host waits for DROOL to confirm a firewall change before undoing it ([Automatic Rollback Protection](Automatic-Rollback-Protection.md)) |
| **Route** | A rule that decides which alerts go to which destinations ([Notification Routing](Notification-Routing.md)) |
| **Run** | Anything DROOL does on hosts: a command, script, package operation, update, reboot, file deployment… Called a *job* in the API ([Running a Command](Running-a-Command.md)) |
| **Schedule** | A run, workflow or maintenance window that happens at set times ([Schedules](Schedules.md)) |
| **Setup code** | The one-time six-digit code DROOL prints when it starts, until the first administrator exists. You enter it once, to create that administrator ([Where do I get the setup code?](First-Login.md#where-do-i-get-the-setup-code)) |
| **Setup credential** | A password, key or sudo password typed in Add host only so DROOL can create its own account; marked *for setup only* and deleted once DROOL signs in with that account, unless another host uses it ([SSH Credentials](SSH-Credentials.md#credentials-for-the-setup-only)) |
| **Shepherd** | The DROOL agent (`drool-agent`), installed on a host for continuous monitoring and management ([SSH vs Shepherds](SSH-vs-Shepherds.md)). Called an *agent* in the API (`/api/v1/agents`) and on the host |
| **SSH fallback** | Using SSH when a host's Shepherd isn't available ([SSH Fallback](SSH-Fallback.md)) |
| **Startup message** | The short message DROOL prints each time it starts, between two rows of `=`: the address to open and, until setup is done, the setup code ([Where do I get the setup code?](First-Login.md#where-do-i-get-the-setup-code)) |
| **Tag** | A label on a host, such as `role:web`, used to pick targets ([Tags](Tags.md)) |
| **Transaction** (firewall) | One protected firewall change on one host ([Automatic Rollback Protection](Automatic-Rollback-Protection.md)) |
| **Volume** | Where a Docker or Podman container keeps data that outlives the container. DROOL's is `drool_drool-data` with Docker Compose ([Compose Data Volume](Compose-Data-Volume.md)), `drool-data` with Podman |
| **Workflow** | A saved list of steps that runs in order on each host ([Workflows](Workflows.md)) |

## Supported distributions

Debian 12 and 13, Ubuntu 22.04, 24.04 and 26.04, Rocky Linux 9 and 10, AlmaLinux 9 and 10, and Fedora 44
are tested; RHEL and other Debian- or RHEL-based distributions are expected to work. The full lists —
where the controller runs, which hosts it manages, CPU architectures and Shepherd versions — are on
[Supported Platforms](Supported-Platforms.md).
