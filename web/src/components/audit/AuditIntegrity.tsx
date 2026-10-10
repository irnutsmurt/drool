import { useState, type FormEvent } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, unwrap, type Schemas } from "@/api/client";
import { useAuth } from "@/auth/AuthContext";
import { Card } from "@/components/ui/Card";
import { Button } from "@/components/ui/Button";
import { Alert } from "@/components/ui/Alert";
import { Badge } from "@/components/ui/Badge";
import { Dialog } from "@/components/ui/Dialog";
import { Field, Input } from "@/components/ui/Field";
import { Checkbox } from "@/components/ui/Checkbox";
import { errorMessage, formatAgo, formatDateTime } from "@/lib/format";

export const integrityKey = ["audit", "integrity"] as const;
export const sinkKey = ["audit", "sink"] as const;

type Verification = Schemas["AuditVerification"];

export function useAuditIntegrity(enabled = true) {
  return useQuery({
    queryKey: integrityKey,
    queryFn: () => unwrap(api.GET("/audit/integrity")),
    enabled,
    refetchInterval: (q) => (q.state.data?.running ? 1000 : 60_000),
  });
}

export function useAuditSink(enabled = true) {
  return useQuery({ queryKey: sinkKey, queryFn: () => unwrap(api.GET("/audit/sink")), enabled, refetchInterval: 30_000 });
}

/** The audit chain's state on the Audit Log page: plain language first,
 * the technical detail underneath. */
export function AuditIntegrityCard() {
  const { can } = useAuth();
  const qc = useQueryClient();
  const st = useAuditIntegrity();
  const [ack, setAck] = useState(false);
  const verify = useMutation({
    mutationFn: () => unwrap(api.POST("/audit/integrity/verify")),
    onSuccess: () => void qc.invalidateQueries({ queryKey: integrityKey }),
  });
  const d = st.data;
  const shown = d?.state === "failed" || d?.state === "acknowledged" ? d.last_failed : d?.last;
  return (
    <Card title="Integrity" actions={
      <Button size="sm" onClick={() => verify.mutate()} disabled={verify.isPending || !!d?.running}>
        {d?.running ? "Checking…" : "Check now"}
      </Button>
    }>
      <div className="space-y-3 text-sm" data-testid="audit-integrity">
        {(st.error ?? verify.error) && <Alert>{errorMessage(st.error ?? verify.error)}</Alert>}
        {!d ? <p className="text-muted">Loading…</p> : (
          <>
            <StateLine state={d.state} last={d.last} />
            {(d.state === "failed" || d.state === "acknowledged") && d.last_failed && (
              <FailureDetail v={d.last_failed} acknowledgement={d.acknowledgement} />
            )}
            {d.state === "failed" && can("settings.write") && (
              <Button size="sm" onClick={() => setAck(true)}>Acknowledge after investigating…</Button>
            )}
            <p className="text-muted">
              Each record is linked to the one before it by a hash, and DROOL seals the history every hour with a key derived
              from the master key, so changing, removing or reordering sealed records is detectable. Someone who can write
              DROOL's database can still rewrite the records since the last seal, and with this machine's master key the whole
              history: for that, send a copy elsewhere (see Remote copy).
            </p>
            <details>
              <summary className="cursor-pointer text-muted">Technical details</summary>
              <dl className="mt-2 grid grid-cols-[minmax(8rem,12rem)_1fr] gap-y-1">
                <dt className="text-muted">Records in the chain</dt><dd>{d.head_seq.toLocaleString()}</dd>
                {d.legacy_records > 0 && <><dt className="text-muted">Older records</dt><dd>{d.legacy_records.toLocaleString()} from before the chain started, covered by its first record's digest</dd></>}
                {shown && <>
                  <dt className="text-muted">Last check</dt>
                  <dd>{shown.finished_at ? formatDateTime(shown.finished_at) : "running"} ({shown.cause}{shown.requested_by ? `, ${shown.requested_by}` : ""}): {shown.records.toLocaleString()} records</dd>
                  <dt className="text-muted">Sealed through</dt>
                  <dd>{shown.sealed_through ? `#${shown.sealed_through.toLocaleString()} (${shown.checkpoints} checkpoint${shown.checkpoints === 1 ? "" : "s"})` : "not sealed yet"}
                    {shown.checkpoints_unverifiable > 0 && `; ${shown.checkpoints_unverifiable} made with a retired master key can't be checked`}</dd>
                </>}
              </dl>
            </details>
          </>
        )}
      </div>
      {ack && <AcknowledgeDialog onClose={() => setAck(false)} />}
    </Card>
  );
}

function StateLine({ state, last }: { state: Schemas["AuditIntegrity"]["state"]; last: Verification | null }) {
  switch (state) {
    case "ok":
      return <p><Badge tone="ok">Intact</Badge> Audit history verified {last?.finished_at ? formatAgo(last.finished_at) : ""}: no record was changed, removed or reordered.</p>;
    case "failed":
      return <Alert>Audit history verification failed. Records were changed, removed or added outside DROOL. Investigate before trusting the log.</Alert>;
    case "acknowledged":
      return <p><Badge tone="warn">Known break</Badge> The audit history has a break an administrator investigated. Later records are still checked.</p>;
    case "running":
      return <p className="text-muted">Checking the audit history…</p>;
    default:
      return <p><Badge>Not checked yet</Badge> The audit history is checked when DROOL starts and every day.</p>;
  }
}

function FailureDetail({ v, acknowledgement }: { v: Verification; acknowledgement: Schemas["AuditIntegrity"]["acknowledgement"] }) {
  return (
    <div className="space-y-2 rounded-ui border border-line p-3">
      <p className="font-medium">{v.problem}</p>
      <ul className="list-disc space-y-1 pl-5 text-muted">
        {v.problems.slice(0, 10).map((p) => <li key={`${p.kind}${p.seq}`}>{p.message}</li>)}
        {v.problem_count > 10 && <li>… and {v.problem_count - 10} more</li>}
      </ul>
      {v.evidence_file && <p className="text-muted">What was found is kept on the controller in <code className="font-mono text-xs">{v.evidence_file}</code>.</p>}
      {acknowledgement && (
        <p className="text-muted">Acknowledged by {acknowledgement.by} {formatAgo(acknowledgement.at)}: “{acknowledgement.note}”</p>
      )}
      <details>
        <summary className="cursor-pointer text-muted">Technical detail</summary>
        <code className="mt-1 block whitespace-pre-wrap break-all font-mono text-xs">{v.problems.map((p) => `${p.kind} @${p.seq}: ${p.detail}`).join("\n")}</code>
      </details>
    </div>
  );
}

function AcknowledgeDialog({ onClose }: { onClose: () => void }) {
  const qc = useQueryClient();
  const [note, setNote] = useState("");
  const mut = useMutation({
    mutationFn: () => unwrap(api.POST("/audit/integrity/acknowledge", { body: { note } })),
    onSuccess: (d) => {
      qc.setQueryData(integrityKey, d);
      onClose();
    },
  });
  function submit(e: FormEvent) {
    e.preventDefault();
    mut.mutate();
  }
  return (
    <Dialog title="Acknowledge the audit history break" onClose={onClose}>
      <form onSubmit={submit} className="space-y-4 text-sm">
        <p>
          Acknowledging doesn't repair anything: the history stays as it is and every check still reports these problems.
          It only records what you found, so the warning stops being an alarm until something new breaks.
        </p>
        {mut.error && <Alert>{errorMessage(mut.error)}</Alert>}
        <Field label="What did you find?">
          <Input value={note} onChange={(e) => setNote(e.target.value)} required maxLength={2000} data-autofocus />
        </Field>
        <div className="flex justify-end gap-2">
          <Button type="button" onClick={onClose}>Cancel</Button>
          <Button type="submit" variant="primary" disabled={mut.isPending}>Acknowledge</Button>
        </div>
      </form>
    </Dialog>
  );
}

const sinkTone = { ok: "ok", off: "neutral", backlogged: "warn", failing: "danger" } as const;

/** The remote copy of the audit history (admins configure it). */
export function AuditSinkCard() {
  const { can } = useAuth();
  const st = useAuditSink(can("settings.read"));
  const [edit, setEdit] = useState(false);
  if (!can("settings.read")) return null;
  const d = st.data;
  return (
    <Card title="Remote copy" actions={can("settings.write") ? <Button size="sm" onClick={() => setEdit(true)}>{d?.configured ? "Change…" : "Set up…"}</Button> : undefined}>
      <div className="space-y-2 text-sm" data-testid="audit-sink">
        {st.error && <Alert>{errorMessage(st.error)}</Alert>}
        {!d ? <p className="text-muted">Loading…</p> : !d.configured ? (
          <p className="text-muted">
            Off. DROOL can send every audit record, with its chain hashes, to an HTTPS endpoint you run elsewhere (a log
            collector or a small receiver). A copy outside this controller survives even if someone rewrites the controller's database.
          </p>
        ) : (
          <>
            <p>
              <Badge tone={sinkTone[d.state]}>{d.state === "ok" ? "Up to date" : d.state === "off" ? "Paused" : d.state === "backlogged" ? "Behind" : "Failing"}</Badge>{" "}
              {d.url_display}
              {d.backlog > 0 && <span className="text-muted"> · {d.backlog.toLocaleString()} record{d.backlog === 1 ? "" : "s"} waiting{d.oldest_unsent && ` (oldest ${formatAgo(d.oldest_unsent)})`}</span>}
            </p>
            {d.state === "failing" && (
              <Alert>Sending failed {d.failures} time{d.failures === 1 ? "" : "s"} in a row: {d.last_error}. DROOL keeps the records and tries again{d.next_attempt_at && ` at ${formatDateTime(d.next_attempt_at)}`}; nothing else is affected.</Alert>
            )}
            <p className="text-muted">
              Last delivered {d.last_success_at ? formatAgo(d.last_success_at) : "never"}; delivered through record #{d.cursor_seq.toLocaleString()} of #{d.head_seq.toLocaleString()}.
            </p>
          </>
        )}
      </div>
      {edit && d && <SinkDialog current={d} onClose={() => setEdit(false)} />}
    </Card>
  );
}

function SinkDialog({ current, onClose }: { current: Schemas["AuditSink"]; onClose: () => void }) {
  const qc = useQueryClient();
  const [enabled, setEnabled] = useState(current.configured ? current.enabled : true);
  const [url, setUrl] = useState(current.url ?? "");
  const [ca, setCa] = useState("");
  const [headerName, setHeaderName] = useState(current.header_name ?? "Authorization");
  const [headerValue, setHeaderValue] = useState("");
  const [startFrom, setStartFrom] = useState<"all" | "now">("all");
  const body = (): Schemas["AuditSinkRequest"] => ({
    enabled, url, header_name: headerName || undefined, start_from: startFrom,
    ...(ca ? { ca_pem: ca } : {}), ...(headerValue ? { header_value: headerValue } : {}),
  });
  const test = useMutation({ mutationFn: () => unwrap(api.POST("/audit/sink/test", { body: body() })) });
  const save = useMutation({
    mutationFn: () => unwrap(api.PUT("/audit/sink", { body: body() })),
    onSuccess: (d) => {
      qc.setQueryData(sinkKey, d);
      onClose();
    },
  });
  const newReceiver = !current.configured || url.trim() !== (current.url ?? "");
  return (
    <Dialog title="Remote copy of the audit history" onClose={onClose}>
      <form onSubmit={(e) => { e.preventDefault(); save.mutate(); }} className="space-y-4 text-sm">
        <Checkbox checked={enabled} onChange={setEnabled} label="Send audit records" />
        <Field label="Receiver URL" hint="https only. Every record is sent as JSON with its chain hashes (see the README for the format).">
          <Input value={url} onChange={(e) => setUrl(e.target.value)} placeholder="https://logs.example.net/drool-audit" required data-autofocus />
        </Field>
        <Field label="Header name" hint="Sent with every request, e.g. Authorization.">
          <Input value={headerName} onChange={(e) => setHeaderName(e.target.value)} />
        </Field>
        <Field label="Header value" hint={current.header_set ? "Stored encrypted. Leave empty to keep the current value." : "Stored encrypted, never shown again, e.g. Bearer <token>."}>
          <Input type="password" autoComplete="off" value={headerValue} onChange={(e) => setHeaderValue(e.target.value)} />
        </Field>
        <Field label="Extra CA certificate (PEM, optional)" hint={current.custom_ca ? "One is set; paste a new one to replace it." : "For a receiver whose certificate is signed by your own CA. Certificates are always checked."}>
          <textarea className="h-20 w-full rounded-ui border border-line bg-surface p-2 font-mono text-xs" value={ca} onChange={(e) => setCa(e.target.value)} />
        </Field>
        {newReceiver && (
          <Field label="Start with">
            <select className="h-9 rounded-ui border border-line bg-surface px-2" value={startFrom} onChange={(e) => setStartFrom(e.target.value as "all" | "now")}>
              <option value="all">The whole history</option>
              <option value="now">Only records from now on</option>
            </select>
          </Field>
        )}
        {test.data && (test.data.ok ? <Alert tone="ok">The receiver accepted a test request.</Alert> : <Alert>Test failed: {test.data.error}</Alert>)}
        {(test.error ?? save.error) && <Alert>{errorMessage(test.error ?? save.error)}</Alert>}
        <div className="flex flex-wrap justify-end gap-2">
          <Button type="button" onClick={() => test.mutate()} disabled={test.isPending || !url}>Send a test</Button>
          <Button type="button" onClick={onClose}>Cancel</Button>
          <Button type="submit" variant="primary" disabled={save.isPending}>Save</Button>
        </div>
      </form>
    </Dialog>
  );
}
