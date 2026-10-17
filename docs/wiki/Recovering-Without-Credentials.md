# Recovering without credentials (lost master key)

If the [master key](Master-Key.md) is lost (the controller's disk died, and no copy of the key was
kept), your backups can't give back the **stored credentials**: SSH keys, passwords and sudo passwords,
because those were encrypted with the lost key. Everything else can still come back.

DROOL refuses to start with a missing or wrong key, rather than silently making your credentials
unreadable.

## What you keep, and what you lose

| Comes back | Lost (must be set up again) |
|---|---|
| Hosts and their trusted host keys, Packs, users, run history, the audit log, firewall policies, schedules, Desired State, settings | Stored credentials (SSH keys, passwords, sudo passwords) |
| | DROOL's SSH certificate authority, the Shepherd intermediate CA |
| | Notification and audit-copy secrets (webhook URLs, SMTP passwords, headers): those destinations are disabled until you enter them again |
| | Two-factor authentication secrets: users sign in with their password and set up two-factor again |

## Steps

1. Install DROOL (if needed) and stop it.
2. Restore the latest backup **without** credentials:

   ```sh
   sudo systemctl stop drool
   sudo -u drool drool restore --discard-credentials - < drool-backup-20261012-214018.tar.gz
   sudo systemctl start drool
   ```

   With Docker Compose (in `~/drool`):

   ```sh
   docker compose stop drool
   docker compose run --rm -T drool restore --discard-credentials - < drool-backup-20261012-214018.tar.gz
   docker compose start drool
   ```

   With Podman:

   ```sh
   systemctl --user stop drool
   image=$(sed -n 's/^Image=//p' ~/.config/containers/systemd/drool.container)
   podman run --rm -i --read-only --tmpfs /tmp --cap-drop ALL --security-opt no-new-privileges \
     -v drool-data:/data "$image" restore --discard-credentials - < drool-backup-20261012-214018.tar.gz
   systemctl --user start drool
   ```

   The restore lists the hosts that need a credential assigned.
3. **Credentials**: create the credentials again. For an SSH key, DROOL creates a new one: put its public
   key on the hosts ([SSH Credentials](SSH-Credentials.md)), for example with your own SSH access or the
   host's console.
4. Assign the credentials to the hosts (on each host's page, **Actions → Edit host details**), and
   **Test connection**.
5. **Shepherds**: the intermediate CA is gone too. With the **offline root key**, issue a new
   intermediate (**Shepherds → Trust (PKI) → Replace the intermediate CA**, or
   `drool agent-ca rotate --root-key drool-agent-root.key`); Shepherds move to it by themselves. Without
   the root key, every Shepherd must be [enrolled again](Installing-a-Shepherd.md).
6. **Notifications**: edit each destination and enter its secret again.
7. Work through **Settings → System → Restore** ([What Happens After Restore](What-Happens-After-Restore.md)).
8. Copy the **new** master key somewhere safe this time ([Keeping a copy](Master-Key.md#keeping-a-copy)).

## Related

[The Master Key](Master-Key.md) · [Restoring a Controller](Restoring-a-Controller.md) ·
[Taking the Agent Root CA Offline](Taking-the-Agent-Root-CA-Offline.md)
