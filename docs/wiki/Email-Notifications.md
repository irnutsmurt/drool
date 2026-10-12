# Email notifications

DROOL can email you when an alert opens and when it's fixed. It sends through an ordinary mail
server (SMTP): your email provider's, or a mail relay on your own network.

## Before you start

- You need **notifications.manage** (administrators).
- You need these details from your mail provider (its help pages call them "SMTP settings" or
  "settings for other mail apps"):
  - the **SMTP server name**, for example `smtp.example.net`;
  - which **security** it uses: usually *STARTTLS on port 587*, sometimes *TLS on port 465*;
  - a **username and password**. Many providers want an **app password** here (a separate password
    made for one program) instead of your normal password, especially if your account uses two-factor
    sign-in. Your provider's help explains how to create one.
- The DROOL controller must be able to reach the mail server on that port. Some home internet
  providers block outgoing connections on port 25, but rarely on 587 or 465.

## Add an email destination

1. Open **Settings → Notifications** (or **Alerts → Notification settings**).
2. Click **Add destination** and choose **Email**.
3. Fill in the form. In the example, Alex uses their mail provider:

   | Field | Example | Notes |
   |---|---|---|
   | **Name** | `Alex's mailbox` | How DROOL lists it. Leave empty to call it *Email* |
   | **SMTP server** | `smtp.example.net` | The server's **name**, as your provider gives it. It must match the server's certificate (an IP address usually won't) |
   | **Port** | *(empty)* | Empty means the usual port for the security you choose: 587, 465 or 25 |
   | **Security** | *STARTTLS (port 587, recommended)* | Or *TLS (port 465)*. See [Unencrypted relays](#a-mail-relay-on-your-own-network) for *None* |
   | **Username (optional)** | `alex@example.net` | Usually your full email address |
   | **Password** | *(your app password)* | Required when there is a username. Never shown again after saving |
   | **From** | `DROOL <alex@example.net>` | The sender. Many providers only accept your own address here |
   | **To** | `alex@example.net, sam@example.net` | One or more recipients (up to 20), separated by commas |
   | **Notify for** | *Recommended: Warnings and Critical, plus recoveries* | Or *Only what advanced routes send here* ([Notification Routing](Notification-Routing.md)) |

4. Click **Send test**. Within a few seconds the form shows *✓ Test delivered: accepted by
   smtp.example.net*, or *✕ Test failed* with the reason ([below](#if-the-test-fails)). Check your
   inbox (and the spam folder) for *TEST: DROOL notification test*.
5. Click **Save**.

![The Add Email form filled in with the example: Name "Alex's mailbox", SMTP server smtp.example.net, Security STARTTLS, Username alex@example.net, a password, From "DROOL <alex@example.net>", To alex@example.net and sam@example.net, and Notify for: Recommended](images/notification-email-form.png)

The test says it's a test and creates no alert. The password is stored encrypted with DROOL's
[master key](Master-Key.md) and is never shown again: the form shows *••••••• (unchanged)* when you
edit the destination later, and you only type it again to change it.

## What the emails look like

The subject says what happened, where, and what, for example **CRITICAL: nas-01 — /srv/media is
over 95% full**, **RECOVERED: media-01 — jellyfin.service failed** or **CLEARED BY CONFIGURATION:
gpu-01 — CPU at 95% or more for 5 minutes**. The body is plain text:

```text
/srv/media is over 95% full
327.7 GiB free of 8.0 TiB.
Current value: 96%
First detected: 2026-10-12 12:56 PDT
Open in DROOL: https://drool.example.net:8080/alerts/alr_01m4rv0b006p8mf2mhgdnsezfy
```

and when it's fixed:

```text
Recovered: jellyfin.service failed
Duration: 7m 42s
Acknowledged by sam: Restarting it after the config fix
Open in DROOL: https://drool.example.net:8080/alerts/alr_01m4rx7k2c9d3e4f5g6h7j8k9m
```

The link goes to the address DROOL was opened from when the first destination was added; you can
change it, and the time zone used in messages, under **Advanced → Settings**
([Notification Routing](Notification-Routing.md#notification-settings)). DROOL sends at most 60
emails a minute to one destination; during a big outage the rest wait their turn.

## A mail relay on your own network

If you run a mail relay on a trusted network (a home server that forwards mail), you can send to it
without encryption:

1. Set **SMTP server** to the relay, for example `10.10.5.30`, and **Security** to *None —
   unencrypted*.
2. Tick **Send unencrypted**. *Only for a relay on a trusted network.*
3. Leave **Username** and **Password** empty. DROOL never sends a password without encryption, and
   refuses to save one with *None*.

DROOL then shows *⚠ Email is sent unencrypted* next to the destination, as a reminder. In the
examples, the *Ops mailbox* destination sends to such a relay.

## If the test fails

| Message | What it means | What to do |
|---|---|---|
| *could not connect* / *connection refused* / *timed out* | DROOL couldn't reach the server on that port | Check the server name and port; check that the controller's network allows outgoing connections to it |
| *host name not found (smtp.example.net)* | The name doesn't exist, or the controller can't look names up | Check the spelling; check the controller's DNS |
| *the server doesn't offer STARTTLS; DROOL won't send unencrypted* | The port doesn't support STARTTLS | Use the port your provider gives for STARTTLS (usually 587), or choose *TLS* for port 465 |
| *TLS handshake failed: the server doesn't speak TLS on this port* | *TLS* was chosen for a STARTTLS or plain port | Choose *STARTTLS* for port 587 |
| *TLS certificate error: …* | The server's certificate isn't trusted, or doesn't match the name you entered | Enter the name on the certificate instead of an IP address. A self-signed certificate needs its CA in the controller's system trust store: there is no setting for it per destination |
| *authentication failed: SMTP 535 …* | Wrong username or password | Check both; many providers need an app password |
| *the server doesn't offer authentication (AUTH)* | The server doesn't accept a username on this port | Remove the username (for a relay), or use the provider's submission port (587) |
| *the server rejected the message: SMTP 550 …* / *553 …* | The server refused the sender or a recipient | Use your own address as **From**; check the recipients |

More: [Notification Failed](Notification-Failed.md).

## Related

[Notification Routing](Notification-Routing.md) · [Understanding Alerts](Understanding-Alerts.md) ·
[Discord](Discord-Notifications.md) · [Slack](Slack-Notifications.md) · [Webhooks](Webhooks.md)
