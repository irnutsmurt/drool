# Notification failed

A destination shows **Delivery failing** under **Settings → Notifications**, an alert's notification
column says **Delivery retrying**, **Partly delivered** or **Delivery failed**, or the alert's page shows
*⚠ Retrying* with an error.

**The alerts themselves are fine.** A failing destination never loses or changes an alert: everything
is still on the **Alerts** page. Only the message didn't get through.

![The destinations: Home Assistant delivering, Homelab Discord disabled, and Ops mailbox with "Delivery failing": last success never, last failure just now — could not connect, 2 waiting](images/notifications-destinations.png)

## How DROOL retries

Each message is saved before it's sent, so a restart never loses one. When a destination doesn't
accept it, DROOL tries again after 30 seconds, then waits twice as long each time, up to 30 minutes,
for about an hour in all (8 attempts). It gives up **at once** on failures that can't fix themselves:
wrong credentials, a rejected request, a certificate problem. After 3 failures in a row, the destination
shows **Delivery failing**, and the Overview's events say so once (and again when it works).

## Find the cause

The destination's line shows *Last failure: … — could not connect*; an alert's **Notifications** card
shows the error and **Attempts** for each try. Then:

| Error | Likely cause | Fix |
|---|---|---|
| *could not connect*, *connection refused*, *timed out*, *host name not found* | The controller can't reach the server: wrong address, the server is down, a firewall, DNS | Check the address; test from the controller (`nc -vz 10.10.5.30 25`) |
| *TLS certificate error: …* | The server's certificate isn't trusted, or doesn't match the name | Use the name on the certificate; a self-signed certificate needs its CA in the controller's system trust store |
| *authentication failed: SMTP 535 …*, *the receiver refused the credentials (HTTP 401)* | Wrong password, token or webhook URL | Edit the destination and enter it again |
| *the receiver rejected the notification (HTTP 404)* | The webhook was deleted on the other side | Create a new webhook, paste its URL |
| *rate limited by the receiver (HTTP 429)* | Too many messages at once | Nothing: DROOL waits as long as asked |

Per destination: [Email](Email-Notifications.md#if-the-test-fails) · [Discord](Discord-Notifications.md#if-the-test-fails) ·
[Slack](Slack-Notifications.md#if-the-test-fails) · [Webhook Reference](Webhook-Reference.md#how-drool-treats-the-receivers-answer).

## After fixing it

1. **Send test** on the destination.
2. **Edit** and save the destination if you changed it: new settings get a fresh start, without the old
   "failing" state. (Until a message gets through, the destination then shows *Not used yet*, even
   though its last failure is still listed.)
3. Messages already given up on aren't sent again; what's still waiting is retried as scheduled.

## May have arrived twice

*may have arrived twice* on a delivery means DROOL can't know whether the receiver got it: the request
was sent, but the answer was lost (a timeout, a restart at the wrong moment). DROOL sends it again rather
than risk losing it. Webhook receivers can drop duplicates by the `delivery_id`
([Webhook Reference](Webhook-Reference.md#how-drool-treats-the-receivers-answer)).

## Related

[Notification Routing](Notification-Routing.md) · [Understanding Alerts](Understanding-Alerts.md)
