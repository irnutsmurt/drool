# About the examples in this wiki

Every example in this wiki uses the same small, **made-up** network, so you can follow one story
from page to page instead of decoding `<host1>` and `<host2>` placeholders. None of these names,
addresses, keys, codes or passwords are real. Don't copy them into your own installation: use your
own machines' names and addresses wherever an example uses these.

The screenshots come from a real DROOL installation running this example network on disposable
virtual machines. Values that would be secret on a real installation (setup codes, enrollment
tokens, a two-factor QR code) are masked, DROOL's own public key is replaced by the example key shown
below, and the lab machine's own folder and name are shown as `/var/lib/drool` and `drool`, as on a
normal installation. Everything else is what DROOL showed.

Three things in the screenshots are simulated, because no small lab can produce them:

- **`nas-01` and `gpu-01` are not real machines.** Their monitoring data (eight disks, a failing disk,
  a busy GPU) is simulated so the monitoring pages have something to show. That's why they have full
  monitoring data, but show **Needs host key** everywhere else (DROOL has never connected to them), and
  why runs that include them fail for them.
- **Most observed network connections are simulated**: the web traffic, Jellyfin players, package
  downloads and DNS on the Network Activity pages. Their internet addresses come from ranges reserved
  for documentation (203.0.113.x and 198.51.100.x), so they can't point at a real machine; DROOL shows
  those as *Special (documentation)* rather than external.
- **The applications are stand-ins.** `jellyfin.service` on `media-01` is a small placeholder service,
  so that it can fail and recover on cue for the alert examples.

## The example network

| Name | Address | What it is | How DROOL manages it |
|---|---|---|---|
| `drool.example.net` | 10.10.0.10 | The machine DROOL itself runs on (the *controller*). You open DROOL at `https://drool.example.net:8080`. | — |
| `media-01` | 10.10.20.15 | Debian 12, runs the Jellyfin media server | Through a [Shepherd](SSH-vs-Shepherds.md) (the DROOL agent) |
| `media-02` | 10.10.20.16 | Debian 12, a second Jellyfin server | Over SSH |
| `database-01` | 10.10.30.10 | Rocky Linux 9, runs PostgreSQL for the web apps | Over SSH |
| `web-01` | 10.10.40.21 | Ubuntu 24.04, runs nginx and the web apps | Over SSH |
| `nas-01` | 10.10.20.30 | A file server with eight disks (used in the monitoring examples) | Over SSH |
| `gpu-01` | 10.10.50.12 | An AI workstation with a graphics card (used in the GPU examples) | Over SSH |
| `admin-laptop` | 10.10.5.25 | The administrator's laptop. DROOL doesn't manage it; it appears in firewall examples as a place SSH comes from. | — |
| (mail relay) | 10.10.5.30 | The home mail relay the *Ops mailbox* destination sends through. Switched off in the screenshots, which is why its deliveries fail | — |
| (Home Assistant) | 10.10.5.40 | The living-room mini PC: it runs Home Assistant (which receives DROOL's webhook alerts) and plays videos from Jellyfin | — |
| (TV) | 10.10.5.41 | Plays videos from Jellyfin | — |
| (DNS server) | 10.10.0.53 | The home network's DNS server | — |

All of these addresses are in `10.10.0.0/16`, the example "home LAN". Internet addresses in the
examples (203.0.113.x, 198.51.100.x) are reserved for documentation and belong to no one.

## Packs (groups of hosts)

| Pack | Members |
|---|---|
| `media-servers` | media-01, media-02, nas-01 |
| `web-servers` | web-01 |

Pack names can't contain spaces, which is why the examples write `media-servers` rather than
"Media Servers". See [Packs](Packs.md).

## People and accounts

| Account | Where | What it is |
|---|---|---|
| `alex` | DROOL | The administrator who installed DROOL |
| `sam` | DROOL | An operator: runs jobs and handles alerts, but can't change security settings |
| `jordan` | DROOL | A viewer: can look, can't change anything |
| `drooladmin` | Each managed Linux host | The Linux account DROOL signs in as over SSH. It may use `sudo` without a password. |

Don't confuse the two kinds of account: `alex` signs in to the DROOL website; `drooladmin` is the
account DROOL itself uses to log in to your servers.

## Example values you'll see

- DROOL's public SSH key, as the examples show it:
  `ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIG7x8RkV4n3fTnYp5sM2rP9QeW1bLc6jK0dZxExample drool:controller-default`.
  It is deliberately broken (it contains the word *Example*) and can't be used for anything.
  Your own installation shows its own key: always copy the one DROOL shows you.
- The first-login setup code in the examples is `482913`. Yours is in your DROOL's startup message,
  and differs ([Where do I get the setup code?](First-Login.md#where-do-i-get-the-setup-code)).
- Fingerprints such as `SHA256:sU3rdL7J8ULPqUqvP4ImLFCOKmPP8tXogMjm7goRYos` come from the
  disposable lab machines; your hosts have their own.

## The story the examples follow

1. Alex installs DROOL on `drool.example.net` and signs in for the first time.
2. Alex adds `web-01` as the first host, then `media-01`, `media-02` and `database-01`.
3. `media-01` gets a Shepherd, so it is monitored continuously and can be reached even if SSH
   isn't available; the others stay on SSH.
4. Alex groups the media servers into the `media-servers` pack and the web server into
   `web-servers`.
5. Later examples deploy `/etc/jellyfin/network.xml` to both media servers, schedule weekly
   updates for `media-servers`, keep `/etc/motd` the same everywhere with Desired State, and write
   a firewall policy whose most important rule is *"web-servers may reach database-01 on
   PostgreSQL (TCP 5432)"*.
