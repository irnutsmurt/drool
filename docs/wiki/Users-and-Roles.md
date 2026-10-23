# Users and roles

Everyone who uses DROOL should have their own account, so the [audit log](Audit-Log.md) shows who did
what. Each account has one **role**. In the examples, Alex is an administrator, Sam an operator, and
Jordan a viewer.

| Role | For | Can |
|---|---|---|
| **Administrator** (`admin`) | The people who run DROOL | Everything: hosts and their trust, credentials, users, settings, firewall policy and modes, notification destinations, baselines |
| **Operator** (`operator`) | People who look after the machines day to day | Run commands, scripts, packages, updates, services and reboots; files; schedules and workflows; acknowledge alerts and start maintenance; send test notifications; monitor services (and set their expected state to running or stopped) and make health checks stricter; restore desired state; take network snapshots and classify network traffic. **Can't** add or remove hosts, approve host keys, manage credentials, users or settings, change the firewall, or change notification destinations |
| **Auditor** (`auditor`) | Reviewing what happened | Look at everything (including the audit log, settings and network activity), change nothing |
| **Viewer** (`viewer`) | Keeping an eye on things | Look at hosts, runs, monitoring, alerts, firewall policy, schedules, workflows and desired state. Not the audit log, settings, notification destinations or network activity |

Pages say so when your role can see but not change something (*you can see … ; changing them needs …*).

## Before you start

Managing users needs **users.manage** (administrators), and you'll be asked to
[confirm it's you](Re-authentication.md).

## Add a user

1. Open **Settings → Users**.
2. Under **Add user**, enter a **Username** (for example `sam`), an **Initial password** (at least 12
   characters) and a **Role**. The line under the form says what the chosen role may do.
3. Click **Add**.

Tell them the initial password through a safe channel. They can change it under **Settings → Account →
Change password**, and should turn on [two-factor authentication](Two-Factor-Authentication.md).

![Settings → Users: the Add user form with the role's description under it, and the users alex (Administrator, you), jordan (Viewer) and sam (Operator) with their role, status, two-factor state and last login; Change role… and More for the others, Your account on your own row, and where to change your own password or recover a locked-out installation](images/users.png)

## Change someone's account

Each row other than yours has **Change role…** and a **More** menu. Your own row links to **Your
account** instead: DROOL never lets you disable, demote or delete yourself.

- **Change role…** shows the four roles side by side, what each may do, and which one they have now.
  Choose one and click **Make sam an operator** (for example). It applies at once, to their open
  sessions and API tokens.
- **More → Set password…**: see [below](#setting-someones-password).
- **More → Reset two-factor…**: for someone who lost their authenticator and recovery codes. They are
  signed out and sign in with their password only, until they set it up again.
- **More → Disable…** / **Enable**: a disabled user is signed out at once and can't sign in or use
  their API tokens; their history stays. Use this when someone leaves, rather than deleting them.
- **More → Delete user…**: removes the account, after you confirm.

**Schedules** run with their owner's permissions: if you disable or delete someone who owns schedules,
or remove a permission the steps need, those schedules stop and are marked *needs attention* until an
administrator gives them a new owner ([Schedules](Schedules.md#who-a-scheduled-run-runs-as)).

## Setting someone's password

For someone who forgot their password, or a colleague's first one:

1. Open **Settings → Users** and choose **More → Set password…** on their row. (Your own password is on
   **Settings → Account**.)
2. Type the new password twice (at least 12 characters) and click **Set password**.
3. DROOL asks you to [confirm it's you](Re-authentication.md) with your own password, unless you did
   so in the last 15 minutes.

They are **signed out of every session** at once and sign in with the new password; their
two-factor authentication stays as it is. Give them the password through a channel you trust: they
can change it under **Settings → Account**. The audit log records who set whose password, never the
password itself.

## If someone forgets their password

An administrator sets a new one ([above](#setting-someones-password)). If no administrator can sign
in (the **only administrator** forgot their password), set it on the controller:

```sh
# Native installation (prompts for the password twice)
sudo -u drool drool user set-password --username alex
# Docker Compose (run cd ~/drool first)
docker compose exec drool drool user set-password --username alex
# Podman
podman exec -it drool drool user set-password --username alex
```

It signs them out everywhere, keeps their two-factor authentication (if they lost that too, also run
`drool user mfa-reset --username alex`, see
[Two-Factor Authentication](Two-Factor-Authentication.md#if-you-lose-your-authenticator)) and is
recorded in the audit log as done from the command line. `drool user create --username alex2 --role
admin` still makes a new administrator instead.

## Good to know

- Accounts are local to DROOL: there's no single sign-on (LDAP, OIDC) in this version.
- Changing your own password signs out your other sessions.

## Related

[Two-Factor Authentication](Two-Factor-Authentication.md) · [API Tokens](API-Tokens.md) · [Audit Log](Audit-Log.md)
