# Remote audit copy

A **remote copy** sends every [audit log](Audit-Log.md) record, as it is written, to an HTTPS address
you run on **another machine**: a log collector, or a small receiver. If the controller itself is ever
compromised, the copy shows what really happened, because whoever controls the controller can't change
what was already sent away.

## Before you start

- You need **settings.write** (administrators), and DROOL asks you to [confirm it's you](Re-authentication.md).
- You need a receiver: something on another machine that accepts an HTTPS `POST` with JSON, stores it,
  and answers with a `2xx` status. For example a log collector with an HTTP input, or a tiny web app
  that appends each request body to a file.
- **HTTPS only.** If the receiver's certificate is signed by your own CA, you can give DROOL that CA's
  certificate.

## Set it up

1. Open **Audit Log**. In the **Remote copy** card, click **Set up…**.
2. Tick **Send audit records**.
3. **Receiver URL**: for example `https://logs.example.net/drool-audit`.
4. Optionally a **Header name** and **Header value** sent with every request, for example
   `Authorization` and `Bearer 2b7e1516a8f04c3d9f6e0b1c5d4a7e93`. The value is stored encrypted and never
   shown again.
5. **Extra CA certificate (PEM, optional)**: paste your CA's certificate if the receiver uses one.
   *Certificates are always checked.*
6. **Start with**: **The whole history** (everything so far, then new records) or **Only records from now
   on**.
7. Click **Send a test**: *The receiver accepted a test request.* Then **Save**.

![The Remote copy of the audit history dialog: Send audit records, the receiver URL https://logs.example.net/drool-audit, an optional header name and value, an extra CA certificate, Start with the whole history or only records from now on, Send a test and Save](images/audit-remote-copy.png)

## What the receiver must do

- **Store each batch, then answer with a `2xx` status.** Anything else is retried with growing pauses
  (up to 10 minutes), and DROOL's header shows when the copy fails or falls behind.
- **Keep duplicates out.** Delivery is *at least once*, so a record can arrive twice: drop repeats by
  their `seq` and `hash`.
- **After DROOL is restored from a backup, keep both versions.** The restored controller continues
  from the backup's last record, so the receiver gets sequence numbers it already has, with different
  contents and a new `controller_epoch`. That fork is the evidence of what happened after the backup.

The batch format, every field, and how to check the records' hash chain yourself are in the
[Remote Audit Copy Reference](Remote-Audit-Copy-Reference.md).

## Related

[Remote Audit Copy Reference](Remote-Audit-Copy-Reference.md) · [Audit Log](Audit-Log.md) ·
[Secure Your Installation](Secure-Your-Installation.md) · [What Happens After Restore](What-Happens-After-Restore.md)
