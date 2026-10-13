# Webhooks

A **webhook** destination makes DROOL send each alert as a small JSON message (an HTTP `POST`) to any
address you choose. Use it to feed Home Assistant, an automation tool such as n8n or Node-RED, a
chat or push service through a small relay, or your own script. In the examples, Alex sends alerts to
Home Assistant at `http://10.10.5.40:8123/api/webhook/drool-alerts`.

## Before you start

- You need **notifications.manage** in DROOL (administrators).
- Something must be listening at the address and accept a JSON `POST`, answering with a `2xx` status
  (any of 200–299).
- The DROOL controller must be able to reach that address. Addresses on your own network (like
  `10.10.5.40`) are allowed on purpose.

## Add a webhook destination

1. Open **Settings → Notifications** (or **Alerts → Notification settings**).
2. Click **Add destination** and choose **Webhook**.
3. Fill in:
   - **Name**: for example *Home Assistant* (empty: *Webhook*).
   - **URL**: the full address, starting with `https://` or `http://`, for example
     `http://10.10.5.40:8123/api/webhook/drool-alerts`. Don't put a username or password in the URL
     (DROOL refuses it): use the secret header instead.
   - **Secret header (optional)** and **Header value**: a header DROOL adds to every request, for
     receivers that want a password or token, for example `Authorization` with
     `Bearer 7f3c9a2e5d1b8f4e6a0c3d9b2e7f1a5c`. If you enter only a value, the header is called
     `Authorization`. Header names may contain letters, digits and `-`.
   - **Notify for**: *Recommended* or *Only what advanced routes send here*
     ([Notification Routing](Notification-Routing.md)).
4. Click **Send test**. The receiver gets a message with `"event": "test"` and `"test": true`; the
   form shows *✓ Test delivered: delivered (HTTP 204)* (or whichever status your receiver answered).
5. Click **Save**.

The URL and the header value are stored encrypted with DROOL's [master key](Master-Key.md) and never
shown again: the list shows only `http://10.10.5.40:8123/…•••••••`.

![The destinations: Home Assistant (Webhook, Delivering, sent unencrypted over http), Homelab Discord (Disabled) and Ops mailbox (Email, Delivery failing: could not connect, 2 waiting)](images/notifications-destinations.png)

**`http://` is unencrypted.** DROOL then shows *⚠ Notifications are sent unencrypted (http)* (or
*Notifications and the authorization header are sent unencrypted*) next to the destination. That's
acceptable on a network you trust; use `https://` anywhere else. For `https://`, the receiver's
certificate must be trusted by the controller's operating system: a self-signed certificate needs its
CA in the controller's system trust store (there is no setting for it per destination).

## Example: Home Assistant

In Home Assistant, create an automation with a **Webhook** trigger and the webhook ID `drool-alerts`;
its address is then `http://<your Home Assistant>:8123/api/webhook/drool-alerts`. The JSON arrives as
`trigger.json`, so a notification action can use `{{ trigger.json.summary }}` as its title and
`{{ trigger.json.text }}` as its message, and a condition can check
`{{ trigger.json.alert.severity == 'critical' }}`. See Home Assistant's own documentation on webhook
triggers for the details of your version.

## What the receiver must do

- **Answer with a `2xx` status** (200–299) once it has the message. DROOL tries again later after a
  `5xx`, `408`, `425` or `429`, a timeout (10 seconds) or no connection, and stops at `401` or `403`
  (*the receiver refused the credentials*: check the secret header) and at any other `4xx`.
- **Expect an occasional duplicate.** If the receiver's answer gets lost, DROOL can't know the message
  arrived and sends it again; the delivery history then says *may have arrived twice*. A receiver that
  must not act twice can drop repeats by their `delivery_id`.
- The fields most receivers need: `summary` (for example *CRITICAL: media-01*), `text` (the rest of
  the message, one fact per line), `event` (`opened`, `recovered`, …) and `alert.severity`
  (`warning` or `critical`).

Everything else — both message examples in full, every field, the headers, and exactly how each
answer is treated — is in the [Webhook Reference](Webhook-Reference.md).

## Related

[Webhook Reference](Webhook-Reference.md) · [Notification Routing](Notification-Routing.md) ·
[Understanding Alerts](Understanding-Alerts.md) · [Notification Failed](Notification-Failed.md) ·
[Email](Email-Notifications.md)
