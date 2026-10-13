# Remote audit copy reference

The exact format of what DROOL sends to a [remote audit copy](Remote-Audit-Copy.md) receiver, and how
to check the records' hash chain without DROOL: for whoever writes or configures the receiver. Setting
up the copy in DROOL doesn't need this page.

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

[Remote Audit Copy](Remote-Audit-Copy.md) · [Audit Log](Audit-Log.md) ·
[What Happens After Restore](What-Happens-After-Restore.md)
