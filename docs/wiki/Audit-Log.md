# Audit log

The **audit log** records every security-relevant action: who signed in, who ran what on which hosts,
who changed a credential, approved a host key, applied a firewall policy, acknowledged an alert,
reviewed a file's content, and every certificate DROOL issued. Each record says when, who (a user, or
*system* for DROOL's own work such as schedules), what, on what, whether it succeeded, from which
address, and the details.

## Before you start

You need **audit.read**: administrators and auditors.

## Read it

Open **Audit Log** in the menu. The newest records come first. **Filter by action prefix**, for example
`auth.` for sign-ins, `firewall.` for firewall changes, `ssh.certificate` for issued certificates, or
`host.` for host changes.

![The Audit log: the Integrity card says the history is intact (verified 1 h ago); the Remote copy card is off; and the latest records, such as alex's sign-ins, the certificates issued to web-01, the SSH certificate authority being created, and a firewall transaction that was started and committed](images/audit-log.png)

Scheduled runs appear as *system execution via schedule X, owned by Y*, so you can always tell whose
permissions they used.

## How DROOL protects the history

Each record contains a hash of the record before it, and every hour DROOL **seals** the chain with a key
derived from the [master key](Master-Key.md). DROOL checks the whole history when it starts, every day,
and when you click **Check now** in the **Integrity** card:

- **Intact**: no record was changed, removed, added or reordered outside DROOL.
- **Audit history verification failed**: *Records were changed, removed or added outside DROOL.
  Investigate before trusting the log.*
- **Known break**: a break an administrator investigated and acknowledged. Later records are still
  checked.

**What this can and can't catch.** The audit log is **tamper-evident, not tamper-proof**. Someone who
controls the controller *and* its master key can rewrite the whole history consistently. Without the
key, someone who can write DROOL's database can still rewrite the records since the last hourly seal,
or (while DROOL is stopped) cut off the end of the history. A [remote copy](Remote-Audit-Copy.md) on
another machine is what catches that.

## If verification fails

1. Don't panic, and don't delete anything. Read what DROOL found: the **Integrity** card says which
   records and how (changed, removed, added, reordered); **Technical details** has more, and *what was
   found is kept on the controller* in an evidence file whose name the card shows.
2. Compare with your [remote copy](Remote-Audit-Copy.md), if you have one: it shows what the records
   said when they were written.
3. Think about what happened to the controller: a restore from backup, a disk problem, someone with
   access to the machine.
4. When you understand it, an administrator clicks **Acknowledge after investigating…**, writes **What did
   you find?**, and clicks **Acknowledge**. The break is then shown as *Known break*. A later, new break is
   still reported.

Acknowledging asks you to [confirm it's you](Re-authentication.md) and is itself recorded.

## Good to know

- The audit log is kept with everything else in DROOL's database and is part of every
  [backup](What-a-Backup-Contains.md).
- Secrets never appear in it: it records *that* a secret changed, not the secret.

## Related

[Remote Audit Copy](Remote-Audit-Copy.md) · [Master Key](Master-Key.md) · [Users and Roles](Users-and-Roles.md)
