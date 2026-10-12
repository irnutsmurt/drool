# Services and ports

In a firewall rule, a **service** says *what kind of traffic*: a protocol and ports. DROOL comes with
the common ones, and you can add your own.

## Built-in services

| Service | Means |
|---|---|
| `ssh` | TCP 22 |
| `dns` | UDP **and** TCP 53 |
| `http` | TCP 80 |
| `https` | TCP 443 |
| `ntp` | UDP 123 |
| `postgres` | PostgreSQL, TCP 5432 |
| `mysql` | MySQL / MariaDB, TCP 3306 |
| `redis` | TCP 6379 |
| `smtp` | Mail transfer, TCP 25 |
| `submission` | Mail submission, TCP 587 and 465 |
| `imap` | IMAP and IMAPS, TCP 143 and 993 |
| `smb` | SMB / CIFS file sharing, TCP 445 and 139 |
| `nfs` | NFS with rpcbind (for NFSv3), TCP and UDP 2049 and 111 |
| `ping` | ICMP echo (ping), IPv4 and IPv6 |
| `mdns` | Multicast DNS, UDP 5353 |
| `mqtt` | MQTT and MQTT over TLS, TCP 1883 and 8883 |
| `wireguard` | UDP 51820 |

**Rules view → Services → Show built-in services** lists them in DROOL. The topology always labels a
line with what it means on the wire, such as `postgres (TCP 5432)`.

## Your own services

In the examples, Jellyfin listens on TCP 8096, so Alex adds a `jellyfin` service:

- **Rules view → Services → New service**: a name (lowercase letters, digits, `-` and `_`), the
  protocol (**TCP** or **UDP**) and the port.
- Or, while adding a rule, choose **Custom (protocol and port)…** in **Service**, enter the protocol
  and **Port**, and optionally a **Name**; DROOL adds the service for you.
- Or in YAML:

  ```yaml
  services:
    jellyfin:
      protocol: tcp
      port: 8096
  ```

**Ports** can be one port (`8096`), a range (`8000-8010`), or a list (`80,443`): *Port must be 1–65535,
a range such as 8000-8010, or a list such as 80,443.*

## Good to know

- A built-in service's meaning can't be changed. Changing your own service changes every rule that uses
  it: the preview shows which hosts that affects, and warns if it would block DROOL's management path.
- A service is about the **destination** port, the one the receiving program listens on.

## Related

[Firewall Policy](Firewall-Policy.md) · [Networks and CIDRs](Networks-and-CIDRs.md) · [YAML View](YAML-View.md)
