# Key rotation

DROOL has several keys you might want to replace. This page covers the **master key**; the others have
their own pages:

| Key | Rotate when | How |
|---|---|---|
| **Master key** (`master.key`) | A copy may have leaked, or as routine | Below |
| **SSH certificate authority** | Its key may be exposed, or as routine | [SSH CA Rotation](SSH-CA-Rotation.md) |
| **Shepherd intermediate CA** | Its key may be exposed, or as routine | [Shepherd PKI](Shepherd-PKI.md#replacing-the-intermediate-ca) |
| **A stored SSH key** (a credential) | It may be exposed | Create a new credential, put its public key on the hosts, assign it, then remove the old key from `authorized_keys` ([SSH Credentials](SSH-Credentials.md)) |

## Rotating the master key

Rotation creates a new key version, re-encrypts every stored secret with it in one transaction, checks
that the new key alone opens all of them, and only then removes the old one. A crash at any point
leaves a key file that still opens everything.

### Before you start

- **DROOL must be stopped** while it rotates (nothing else may write meanwhile). Plan a few minutes of
  downtime.
- Make a [backup](Backup-and-Recovery.md) first.
- You need administrator access to the controller machine.

### Steps

Native installation:

```sh
sudo systemctl stop drool && sudo -u drool drool secrets rotate-key && sudo systemctl start drool
```

Docker Compose (in the directory with `compose.yaml`):

```sh
docker compose stop drool && docker compose run --rm drool secrets rotate-key && docker compose start drool
```

Podman:

```sh
systemctl --user stop drool
podman run --rm -i --read-only --tmpfs /tmp --cap-drop ALL --security-opt no-new-privileges \
  -v drool-data:/data docker.io/library/drool:v0.1.0-alpha secrets rotate-key
systemctl --user start drool
```

Use the image you actually run instead of `docker.io/library/drool:v0.1.0-alpha`: it's in your
`drool.container` file (`Image=`), and `podman images` lists it.

### Afterwards

1. **Copy the new key** out and store it as before ([Keeping a copy](Master-Key.md#keeping-a-copy)).
2. The old key is saved next to the new one as `master.key.previous-<time>`. **Backups made before the
   rotation need it** (`drool restore --master-key …`). Move it off the controller and keep it with those
   older backups; [Secure Your Installation](Secure-Your-Installation.md) tells you while it's still
   there.
3. Make a new backup.

## Related

[The Master Key](Master-Key.md) · [SSH CA Rotation](SSH-CA-Rotation.md) · [Shepherd PKI](Shepherd-PKI.md)
