# Slack notifications

DROOL can post alerts to a Slack channel through an **incoming webhook**: a private address that a
small Slack app gives you, which lets DROOL post into one channel.

## Before you start

- You need **notifications.manage** in DROOL (administrators).
- In Slack, you need permission to add apps to the workspace. In some workspaces an admin has to
  approve the app first.
- The DROOL controller must be able to reach `hooks.slack.com` on the internet (HTTPS, port 443).

## 1. Create the webhook in Slack

Slack's pages change their wording from time to time; the steps are the same.

1. Go to Slack's app page for developers (**api.slack.com/apps**) and click **Create New App →
   From scratch**. Name it *DROOL* and pick your workspace.
2. In the app's settings, open **Incoming Webhooks** and switch **Activate Incoming Webhooks** on.
3. Click **Add New Webhook** (*Add New Webhook to Workspace*), choose the channel, for example
   `#homelab-alerts`, and click **Allow**.
4. Copy the **Webhook URL**. It looks like this (this one is made up):

   ```text
   https://hooks.slack.com/services/T0EXAMPLE1/B0EXAMPLE2/example-only-not-a-real-token
   ```

Treat this URL like a password: anyone who has it can post into your channel. If it leaks, remove
the webhook in the app's **Incoming Webhooks** settings and add a new one.

## 2. Add it to DROOL

1. Open **Settings → Notifications** (or **Alerts → Notification settings**).
2. Click **Add destination** and choose **Slack**.
3. **Name**: for example *Homelab Slack* (empty: *Slack*).
4. **Webhook URL**: paste the URL. DROOL accepts only Slack incoming-webhook addresses here
   (`https://hooks.slack.com/services/…`).
5. **Notify for**: *Recommended: Warnings and Critical, plus recoveries*, or *Only what advanced
   routes send here* ([Notification Routing](Notification-Routing.md)).
6. Click **Send test**. A message **TEST: DROOL notification test** appears in the channel.
7. Click **Save**.

DROOL stores the URL encrypted with its [master key](Master-Key.md) and never shows it again: the list
shows only `https://hooks.slack.com/…•••••••`. To change it, **Edit** the destination and paste the
new URL; leave the field empty to keep the current one.

## What the messages look like

Each alert is one message: a bold first line such as **CRITICAL: nas-01**, then the same lines as an
email ([example](Email-Notifications.md#what-the-emails-look-like)), ending with the *Open in DROOL*
link. Slack doesn't unfold link previews for them.

## Good to know

- DROOL sends at most **30 messages a minute** to one Slack destination. During a big outage the rest
  wait their turn rather than being lost.
- To pause a destination without deleting it, **Edit** it and untick **Enabled**.

## If the test fails

| Message | What to do |
|---|---|
| *paste the Slack incoming webhook URL (https://hooks.slack.com/services/…)* | The address isn't an incoming-webhook URL. Copy it again from the app's **Incoming Webhooks** page |
| *the receiver rejected the notification (HTTP 404)* or *(HTTP 410)* | The webhook or the app was removed, or the channel was archived. Add a new webhook |
| *the receiver refused the credentials (HTTP 403)* | The webhook isn't allowed to post (for example, it was revoked). Add a new webhook |
| *host name not found* / *could not connect* / *timed out* | The controller can't reach hooks.slack.com. Check its internet access, DNS and any proxy |

More: [Notification Failed](Notification-Failed.md).

## Related

[Notification Routing](Notification-Routing.md) · [Understanding Alerts](Understanding-Alerts.md) ·
[Email](Email-Notifications.md) · [Discord](Discord-Notifications.md) · [Webhooks](Webhooks.md)
