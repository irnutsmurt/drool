# Confirming it's you (re-authentication)

Some actions change who or what DROOL trusts, or could lock DROOL out of your hosts. Before those, DROOL
asks for your password again (and a code, if you use [two-factor authentication](Two-Factor-Authentication.md)),
unless you confirmed in the last **15 minutes**. Signing in counts as confirming. This protects you
when someone else gets hold of a browser where you're signed in.

![Confirm it's you: "You're changing how your own account signs in or acts. Confirm your identity to continue.", "DROOL asks for your password again. One confirmation lasts 15 minutes: other high-impact actions in that time don't ask again.", the Password field, Cancel and Confirm](images/reauth-dialog.png)

The dialog says why it asks, for example *You're changing how DROOL gets administrative access to
this host. Confirm your identity to continue.* It always opens on top of whatever you were doing, and
after you confirm, DROOL finishes the action you started. **Cancel** leaves everything as it was.

## When DROOL asks

Only for actions that change trust, access or privilege, or destroy something you can't get back:

- **Your own sign-in**: turning [two-factor authentication](Two-Factor-Authentication.md) on or off,
  new recovery codes, new API tokens.
- **Who may sign in, and what they may do**: adding and removing users, changing a user's role or
  disabling them, resetting someone's two-factor authentication, and setting another user's password
  (they're signed out everywhere).
- **How DROOL gets into a host**: setting up and switching to [DROOL's own SSH account](Dedicated-SSH-Account.md),
  switching a host between a credential and certificates, switching an established host (one whose
  host key DROOL already trusts) to another account or credential, replacing a credential hosts use,
  and accepting a changed SSH host key.
- **DROOL's SSH certificate authority**: creating, rotating, activating or retiring it.
- **Shepherds**: installing one (on one host or many) and uninstalling one; revoking a Shepherd;
  the Shepherd certificate authorities (exporting or removing the root key, rotating), release-signing
  keys and manifests, and DROOL's addresses for Shepherds.
- **Firewall overrides**: a firewall change that turns off the safeguard keeping DROOL connected to
  the host. An ordinary apply doesn't ask: it keeps the safeguard and rolls back by itself if DROOL
  loses the host.
- **The audit trail and recovery**: the remote audit copy, acknowledging an audit history break, and
  decisions after a restore.

Everyday work never asks: renaming hosts, tags, Packs, runs and schedules, monitoring settings and
expected service states, alerts and maintenance, network classifications, notification destinations,
files and Desired State.

## What to do

Enter your password (and the 6-digit code from your authenticator, or a recovery code via **Use a
recovery code instead**) and confirm. DROOL then does what you asked. For the next 15 minutes, every
action in the list goes through without asking again.

## Good to know

- The check is made by the server, not the page: the page keeps no timer of its own. When the window
  has passed, the server says so on the next high-impact action, and the dialog appears.
- **API tokens can never do these actions**, even with every permission: they need a person at a
  browser ([API Tokens](API-Tokens.md)).
- Wrong passwords count towards the sign-in limit, so guessing is limited.
- An administrator can change the 15-minute window with the `auth.reauth_window` setting
  ([Configuration](Configuration.md)).

## Related

[Two-Factor Authentication](Two-Factor-Authentication.md) · [Users and Roles](Users-and-Roles.md)
