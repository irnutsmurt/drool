# GeoIP and ASN

DROOL shows the **country** and the **network owner** (the ASN, for example *AS64500 Example
Hosting*) of external addresses in Network Activity. It works without setup: DROOL downloads the
free **DB-IP Lite** country and ASN databases by itself, in the background, and keeps them up to
date. Addresses are always looked up **locally**, on the controller: DROOL never sends addresses to
an online service, and never looks up private, loopback, link-local, multicast or documentation
addresses. Only the database files are downloaded.

> Country and network owner are **context, not a verdict**. They can be wrong or out of date, and
> DROOL never blocks, scores or flags traffic because of them.

## Before you start

- Seeing the enrichment status needs **network.read**; changing the settings and **Check now** need
  **network.settings** (administrators).
- Automatic downloads need the controller to reach `https://download.db-ip.com` (directly or through
  the proxy in `HTTPS_PROXY`). Without it everything else works; only country and network owner are
  missing.

## How it works

- **At startup**, in the background (it never delays DROOL or monitoring), DROOL looks at the
  databases it has. If the country or the ASN database is missing, it downloads it.
- **Every day** it checks for a newer monthly release. DB-IP publishes one on the first of each month;
  until then DROOL keeps the previous month's. An unchanged release isn't downloaded again.
- **Every download is checked** before it's used: the size is limited, the file must unpack
  completely (its checksum included), be a MaxMind-format database DROOL can read, and contain the
  kind of data expected. Only then does it replace the previous one, in one step. A download that
  fails or doesn't pass keeps the databases in use, and DROOL says so.
- **Your own databases win.** Files you put in DROOL's GeoIP directory are never changed or removed,
  and for the kinds of data they provide DROOL downloads nothing ([below](#your-own-databases)).
- DROOL keeps what it downloads in `<data_dir>/geoip/downloaded` (containers: `/data/geoip/downloaded`,
  in DROOL's data volume), so it survives restarts and upgrades without downloading again.

The data is DB-IP's, licensed under [CC BY 4.0](https://creativecommons.org/licenses/by/4.0/):
wherever it's shown, DROOL shows **IP Geolocation by DB-IP** with a link to [db-ip.com](https://db-ip.com).

## Check the status

Open **Network Activity → Collection and enrichment**. **Country and network owner (GeoIP/ASN)**
shows one state:

| State | What it means |
|---|---|
| **Preparing the databases…** | The first download is on its way (a minute or so). Network Activity shows a short note, not a warning |
| **Available** | Lookups work. Each kind says where its data comes from: *DB-IP Lite, release 2026-10*, or your own file |
| **Checking for an update…** | A daily check or **Check now** is running; the databases in use stay in use |
| **Update failed — the databases in use are kept** | The last check failed (DROOL says why); it tries again at the next daily check, or use **Check now** |
| **Temporarily unavailable** | Nothing could be downloaded yet (for example, the controller is offline). DROOL tries again every hour |
| **Lookups off** | **Show country and network owner…** is turned off |
| **Not set up** | Automatic downloads are off and there are no databases |

It also shows the last successful check, the next one, and the last error. **Check now** checks for a
newer release right away (it's recorded in the audit log). **Reload files** loads changed files from
the directories right away (DROOL also notices them by itself).

![Network Activity → Collection and enrichment: Country and network owner (GeoIP/ASN) Available, the country and the network owner (ASN) from DB-IP Lite, release 2026-10; Automatic downloads on (DB-IP Lite, CC BY 4.0, checked daily for a new monthly release) with the last successful check and the next, Only the database files are downloaded, IP Geolocation by DB-IP; Check now and Reload files; and the collection settings with Show country and network owner and Download and update the databases automatically ticked](images/network-settings.png)

## Turning it off

- **Show country and network owner for public addresses**, on the same page, turns lookups off. DROOL
  then downloads nothing either.
- **Download and update the databases automatically**, below it, turns downloads off. Databases already
  downloaded stay in use; nothing is downloaded or updated any more.
- To make sure the controller never contacts DB-IP — even at its very first start — set
  `network.geoip_download: false` in the [configuration](Configuration.md) (or
  `DROOL_NETWORK_GEOIP_DOWNLOAD=false`). For an installation without internet access, put your own
  databases in place instead.

## Your own databases

You can use other databases, for example MaxMind GeoLite2 kept current with `geoipupdate`. Put
MaxMind-format `.mmdb` files in DROOL's GeoIP directory; DROOL recognises them by their content:

| Installation | Directory |
|---|---|
| Native package or archive | `/var/lib/drool/geoip` (that is, `<data_dir>/geoip`) |
| Docker Compose | `/data/geoip` inside the container (the `geoip` folder in DROOL's data volume) |
| Podman | `/data/geoip` inside the container |

To use another directory, set `network.geoip_dir` in the [configuration](Configuration.md). The DROOL
service must be able to read the files; DROOL never writes there.

| Source | What you get |
|---|---|
| **MaxMind GeoLite2** Country and ASN | Free account; keep them current with MaxMind's `geoipupdate` tool (point its `DatabaseDirectory` at this directory) |
| **DB-IP Lite** country and ASN | Monthly downloads, CC BY 4.0 |
| **IPinfo Lite** | Country and ASN in one file |

For each kind of data — country, network owner — your file is used instead of DB-IP's, and DROOL
doesn't download that kind. Keeping your files current is then up to you. Remove them to let DROOL
download and update DB-IP Lite again. A half-written or broken replacement of a file is ignored, and
the previous copy stays in use.

## What Network Activity shows

DROOL tells these cases apart, for the country and the network owner separately:

| Case | A connection shows | Network Activity says |
|---|---|---|
| Lookups turned off | **—** (*country lookups are off*) | *Country and network-owner lookups are turned off. Collection and enrichment →* |
| The first download is on its way | **—** (*the country database is still being prepared*) | *Preparing country and network-owner data: DROOL is downloading its databases for the first time.* |
| The download failed and nothing was downloaded yet | **—** (*the country database couldn't be downloaded yet*) | *… temporarily unavailable: DROOL couldn't download its database (the reason). It tries again automatically.* |
| No database of that kind, and automatic downloads off | **—** (*no country database is configured*) | *… unavailable because GeoIP/ASN databases are not configured. Configure enrichment →* |
| A database is loaded, and has no answer for the address | *country unknown*, *network unknown* | nothing: the address is genuinely unknown |

**Unknown** therefore always means the database was asked and had no answer. The reason behind a
**—** is on the connection's details and in its tooltip. Private, internal and special addresses are
never looked up, whatever the setting: *internal*, or nothing.

![web-01's connection to 1.1.1.1 on TCP 853 (DNS over TLS), external: country Australia (AU) and network AS13335 Cloudflare, Inc., with the note that country and network come from this controller's GeoIP/ASN databases and are context, not a verdict, and the attribution IP Geolocation by DB-IP](images/network-flow-geoip.png)

## Related

[Internal vs External](Internal-vs-External.md) · [What DROOL Can See](What-DROOL-Can-See.md) ·
[Configuration](Configuration.md)
