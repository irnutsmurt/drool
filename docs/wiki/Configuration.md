# Configuration

DROOL runs without any configuration file. You only need one to change a default: the port, the
certificate, where data lives, and similar.

## Where settings come from

1. Built-in defaults,
2. then the configuration file, if there is one: `/etc/drool/config.yaml` on a native installation (or the
   file given with `--config`),
3. then environment variables starting with `DROOL_`, which win over everything.

An environment variable's name is the setting's path in capitals with `_`, for example `server.web_port` →
`DROOL_SERVER_WEB_PORT`. In containers, use environment variables: under `environment:` in
`compose.yaml` ([how](Install-With-Docker-Compose.md#if-youll-use-shepherds)), or as `Environment=` lines
in `drool.container` for Podman ([how](Install-With-Podman.md#if-youll-use-shepherds)).

**Settings → System → Effective configuration** shows every setting's current value and where it came
from. The release installs a commented example as `/usr/share/drool/config.example.yaml`.

## Change a setting (native installation)

1. Write the settings you need in a file, for example `config.yaml`:

   ```yaml
   server:
     web_port: 8443
   ```

2. Install it so the service can read it:

   ```sh
   sudo install -m 0640 -g drool config.yaml /etc/drool/config.yaml
   ```

3. Restart DROOL: `sudo systemctl restart drool`.

## Settings you're most likely to change

| Setting | Default | What it does |
|---|---|---|
| `server.bind` | `0.0.0.0` | The address the web interface listens on |
| `server.web_port` | `8080` | The web interface's port (HTTPS) |
| `server.public_port` | `server.web_port` | The port browsers use, when a container publishes the web interface under another one (`8443:8080`). It only changes the address DROOL prints when it starts; DROOL still listens on `server.web_port`. DROOL's `compose.yaml` sets it from its web port |
| `server.tls.mode` | `self_signed` | `self_signed`, `provided` (your own certificate) or `off` (only behind a reverse proxy on the same machine) |
| `server.trusted_proxies` | none | Reverse proxies whose `X-Forwarded-For` header DROOL believes |
| `agent.enabled` | `true` | The Shepherd endpoint |
| `agent.port` | `9443` | The port the Shepherd endpoint listens on. It must differ from `server.web_port`: DROOL refuses to start with both on one port. In a container this is the port inside the container: leave it, and set `agent.public_port` |
| `agent.public_port` | `agent.port` | The port Shepherds connect to, when it isn't `agent.port`: a container publishing 9443 as another port (`2043:9443`), or port forwarding. It never changes where DROOL listens. DROOL's `compose.yaml` sets it from its Shepherd port |
| `agent.bind` | same as `server.bind` | The address the Shepherd endpoint listens on |
| `agent.advertise` | the address you open DROOL with, on `agent.public_port` (else `agent.port`) | The addresses Shepherds connect to (put into enrollment tokens and given to Shepherds when they're installed). Names or addresses, for example `drool.example.net`; one without a port gets `agent.public_port` (else `agent.port`), and `name:port` is used exactly as written. Set it when your hosts reach DROOL by another name than the one you open it with |
| `agent.update_source` | DROOL's releases on GitHub | Where DROOL looks for its Shepherds' signed update descriptions and for newer releases ([Updating Shepherds](Updating-Shepherds.md)): a mirror serving the same paths, or `off` to never look. Only small signed files are fetched, never programs; what doesn't verify against the DROOL release key built into DROOL and its Shepherds is ignored (`DROOL_AGENT_UPDATE_SOURCE`) |
| `data_dir` | `/var/lib/drool` (containers: `/data`) | Where DROOL keeps its database and files |
| `data_volume` | empty (`compose.yaml`: `drool_drool-data`) | Only for messages: the container volume `data_dir` is, which a DROOL starting without data names ([Compose Data Volume](Compose-Data-Volume.md)) |
| `secrets.master_key_file` | `<data_dir>/master.key` | Where the [master key](Master-Key.md) is |
| `network.geoip_dir` | `<data_dir>/geoip` | Where your own [GeoIP and ASN](GeoIP-and-ASN.md) databases are (DROOL only reads them) |
| `network.geoip_download` | `true` | Download the [GeoIP and ASN](GeoIP-and-ASN.md) databases you don't provide (DB-IP Lite) into `<data_dir>/geoip/downloaded` and keep them up to date. `false`: never contact DB-IP. Addresses are always looked up locally |
| `auth.session_idle_timeout` | `12h` | Sign out after this long without activity |
| `auth.session_absolute_timeout` | `168h` (7 days) | Sign out after this long in any case |
| `auth.reauth_window` | `15m` | How long [confirming it's you](Re-authentication.md) lasts |
| `ssh_ca.cert_ttl`, `ssh_ca.clock_skew` | `10m`, `5m` | [Short-lived SSH certificates](Short-Lived-SSH-Certificates.md)' validity |
| `poller.status_interval` | `5m` | How often DROOL checks every host's status |
| `poller.updates_interval` | `6h` | How often DROOL checks for available updates |
| `retention.jobs_days` | `30` | How long run history and output are kept |
| `backup.before_upgrade` | `true` | Back up the database before a newer DROOL upgrades it |
| `logging.level`, `logging.output` | `info`, `file` | Log detail; `file` (`<data_dir>/logs`) or `stdout` (containers) |
| `setup.code_ttl` | `1h` | How long a generated [setup code](First-Login.md#where-do-i-get-the-setup-code) stays valid (1 minute to 24 hours) |
| `setup.secret_file`, `setup.secret` | none | [Your own setup code](#your-own-setup-code), instead of a generated one |

## Your own setup code

Until the first administrator exists, DROOL normally makes a six-digit setup code each time it starts
and prints it in its startup message ([First Login](First-Login.md#where-do-i-get-the-setup-code)). For
automated installations you can choose the code yourself instead:

- `DROOL_SETUP_SECRET_FILE` (`setup.secret_file`): a file whose content is the code. Prefer this, for
  example with a Docker secret; `compose.yaml` has a commented-out example.
- `DROOL_SETUP_SECRET` (`setup.secret`): the code itself. Environment variables are easier to leak
  than a file.

It must be at least 12 characters, or DROOL refuses to start (*provided setup secret must be at least
12 characters*). It doesn't expire and doesn't change when DROOL restarts. DROOL never prints it: the
startup message says *Setup code: the setup secret you provided*. Like every setup code, it stops
working once the first administrator exists, and backups never contain it. A code from
`drool setup code` works alongside it.

## Your own TLS certificate

By default DROOL uses a self-signed certificate, and browsers warn about it. To use your own (from your
company CA, or Let's Encrypt via another tool):

```yaml
server:
  tls:
    mode: provided
    cert_file: /etc/drool/tls/fullchain.pem
    key_file: /etc/drool/tls/privkey.pem
```

The `drool` user must be able to read both files. The certificate must match the name you open DROOL
with (`drool.example.net`). Restart DROOL afterwards. Renewing the certificate means replacing the
files and restarting DROOL. (The Shepherd endpoint on port 9443 always uses DROOL's own certificates;
this setting doesn't affect it.)

## Behind a reverse proxy

If a reverse proxy on the **same machine** (Caddy, nginx, Traefik) handles HTTPS for DROOL:

```yaml
server:
  bind: 127.0.0.1
  tls:
    mode: off
  trusted_proxies: [127.0.0.1]
agent:
  bind: 0.0.0.0
```

- `tls.mode: off` is only accepted with a loopback `bind`, so DROOL never serves plain HTTP to the
  network by accident.
- `trusted_proxies` lets DROOL record the real client address (from `X-Forwarded-For`) in the audit log.
- **`agent.bind: 0.0.0.0` matters if you use Shepherds**: otherwise the Shepherd endpoint would listen on
  127.0.0.1 too, and no host could reach it. Shepherds connect to port 9443 **directly**, never through
  the proxy (they use their own certificates).
- The proxy must not buffer responses: live run output is streamed (server-sent events).

A minimal Caddy example:

```text
drool.example.net {
	reverse_proxy 127.0.0.1:8080
}
```

## Related

[Ports and Files](Ports-and-Files.md) · [Secure Your Installation](Secure-Your-Installation.md) ·
[Command Line](Command-Line.md)
