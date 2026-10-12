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

## What the receiver gets

Records are sent in order, **at least once**, in JSON batches of up to 500
(`Content-Type: application/json`):

```json
{ "format": "drool-audit-export-v1", "chain_format": "drool-audit-v1", "chain_id": "…",
  "controller": "…", "controller_epoch": "…", "sent_at": "…",
  "records": [ { "seq": 1, "id": "aud_…", "ts": 1760000000000, "time": "…", "actor_type": "user",
    "actor_id": "…", "actor_name": "…", "action": "…", "target_type": "…", "target_id": "…",
    "result": "success", "source_ip": "…", "request_id": "…", "details": { },
    "details_json": "{…}", "prev_hash": "…", "hash": "…" } ] }
```

- Answer **2xx** once the batch is stored. Anything else is retried with growing pauses (up to 10
  minutes), and DROOL's header shows when the copy fails or falls behind.
- Because delivery is at least once, the receiver may get a record twice: drop duplicates by `seq`
  and `hash`.
- **After DROOL is restored from a backup**, its new records continue from the backup's last one, so the
  copy receives sequence numbers it already has with different contents, under a new
  `controller_epoch`. **Keep both**: that fork is the evidence of what happened after the backup.

## Checking records without DROOL

Each record's `hash` is the SHA-256 (hex) of these fields, each written as a netstring
(`<length in bytes>:<bytes>,`), in this order: `drool-audit-v1`, `prev_hash` (hex; 64 zeros for the
first record), `seq`, `id`, `ts` (decimal milliseconds), `actor_type`, `actor_id`, `actor_name`,
`action`, `target_type`, `target_id`, `result`, `source_ip`, `request_id`, `details_json` (exactly as
sent, not re-serialized). Each record's `prev_hash` is the previous record's `hash`.

## Related

[Audit Log](Audit-Log.md) · [Secure Your Installation](Secure-Your-Installation.md) · [What Happens After Restore](What-Happens-After-Restore.md)
