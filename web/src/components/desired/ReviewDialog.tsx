import { useState } from "react";
import { useMutation } from "@tanstack/react-query";
import { api, unwrap } from "@/api/client";
import { DiffView } from "@/components/files/DiffView";
import { Alert } from "@/components/ui/Alert";
import { Button } from "@/components/ui/Button";
import { Dialog } from "@/components/ui/Dialog";
import { Field, Input } from "@/components/ui/Field";
import { errorMessage, formatBytes } from "@/lib/format";
import { describeKey, useDesiredReview, useInvalidateDesired, type DesiredResourceView } from "./queries";
import { DesiredStatusBadge, DiffTable } from "./ui";

const fileStates: Record<string, string> = {
  create: "The file is missing on the host; restoring creates it.",
  identical: "The host's file matches the desired content and permissions.",
  replace: "The content differs.",
  metadata: "The content matches; owner, group or permissions differ.",
};

/** Review difference: read-only, nothing on the host changes. */
export function ReviewDialog({ hostId, k, onClose }: { hostId: string; k: string; onClose: () => void }) {
  const rv = useDesiredReview(hostId, k);
  const r = rv.data?.resource;
  const f = rv.data?.file;
  return (
    <Dialog title={`Review difference: ${describeKey(k)}`} onClose={onClose} wide>
      {rv.isPending && <p className="text-sm text-muted" role="status">Comparing…</p>}
      {rv.error && <Alert>{errorMessage(rv.error)}</Alert>}
      {r && (
        <div className="space-y-4">
          <p className="text-sm text-muted">Read-only: reviewing never changes the host.</p>
          <div className="flex flex-wrap items-center gap-2 text-sm">
            <DesiredStatusBadge status={r.status} />
            {r.observed_at && <span className="text-muted">observed {new Date(r.observed_at).toLocaleString()}</span>}
          </div>
          <dl className="grid gap-x-6 gap-y-1 text-sm sm:grid-cols-[10rem_1fr]">
            <dt className="text-muted">Desired</dt><dd>{r.want}</dd>
            <dt className="text-muted">Current</dt><dd>{r.current || "—"}</dd>
            {r.observed?.package?.version && <><dt className="text-muted">Installed version</dt><dd className="font-mono text-xs">{r.observed.package.version}</dd></>}
          </dl>
          <DiffTable diffs={r.diffs} />
          {r.note && r.status !== "compliant" && <p className="text-sm text-muted">{r.note}</p>}
          {r.kind === "file" && rv.data?.file_error && <Alert tone="warn">{rv.data.file_error}</Alert>}
          {f && (
            <div className="space-y-3">
              <p className="text-sm">{fileStates[f.state] ?? f.state}</p>
              <table className="w-full text-left text-sm" aria-label="File details">
                <thead>
                  <tr className="border-b border-line text-xs uppercase tracking-wide text-muted">
                    <th className="py-1.5 pr-3 font-medium" />
                    <th className="py-1.5 pr-3 font-medium">Desired</th>
                    <th className="py-1.5 font-medium">On the host</th>
                  </tr>
                </thead>
                <tbody className="font-mono text-xs">
                  <tr className="border-b border-line"><td className="py-1.5 pr-3 font-sans text-sm">SHA-256</td><td className="break-all py-1.5 pr-3">{f.artifact}</td><td className="break-all py-1.5">{f.current?.sha256 ?? "missing"}</td></tr>
                  <tr className="border-b border-line"><td className="py-1.5 pr-3 font-sans text-sm">Size</td><td className="py-1.5 pr-3">{formatBytes(f.artifact_size)}</td><td className="py-1.5">{f.current ? formatBytes(f.current.size) : "—"}</td></tr>
                  <tr className="border-b border-line"><td className="py-1.5 pr-3 font-sans text-sm">Owner</td><td className="py-1.5 pr-3">{f.desired.owner || "(kept)"}:{f.desired.group || "(kept)"}</td><td className="py-1.5">{f.current ? `${f.current.owner}:${f.current.group}` : "—"}</td></tr>
                  <tr><td className="py-1.5 pr-3 font-sans text-sm">Permissions</td><td className="py-1.5 pr-3">{f.desired.mode}</td><td className="py-1.5">{f.current?.mode ?? "—"}</td></tr>
                </tbody>
              </table>
              {(f.state === "replace" || f.state === "create") && (
                rv.data?.content_allowed
                  ? <DiffView diff={f.diff} note={f.diff_note || (f.artifact_text ? undefined : "Binary or large file: compared by hash only.")} />
                  : <p className="text-sm text-muted">{f.diff_note || "Showing file contents needs the files.read permission."}</p>
              )}
            </div>
          )}
          <div className="flex justify-end"><Button type="button" onClick={onClose}>Close</Button></div>
        </div>
      )}
    </Dialog>
  );
}

/** Accept current state: what the host has now becomes its own desired
 * state. Policy, not a restore. */
export function AcceptDialog({ hostId, hostName, resources, onClose }: {
  hostId: string;
  hostName: string;
  resources: DesiredResourceView[];
  onClose: () => void;
}) {
  const invalidate = useInvalidateDesired();
  const [note, setNote] = useState("");
  const accept = useMutation({
    mutationFn: () => unwrap(api.POST("/desired/hosts/{id}/accept", { params: { path: { id: hostId } }, body: { keys: resources.map((r) => r.key), note } })),
    onSuccess: () => invalidate(),
  });
  const done = accept.data?.accepted;
  return (
    <Dialog title={`Accept current state on ${hostName}`} onClose={onClose}>
      {!done ? (
        <form className="space-y-4" onSubmit={(e) => { e.preventDefault(); accept.mutate(); }}>
          <p className="text-sm">
            DROOL checks the host again, then makes what it has now <strong>this host's own desired state</strong> for:
          </p>
          <ul className="list-disc space-y-1 pl-5 text-sm">
            {resources.map((r) => (
              <li key={r.key}><span className="font-mono text-xs">{describeKey(r.key)}</span>: <span className="text-muted">{r.current || "as it is now"}</span></li>
            ))}
          </ul>
          <p className="text-sm text-muted">
            Nothing on the host changes, and no baseline changes: other hosts keep their desired state. History records this as
            resolved by configuration, not as a restore. You can reset it to the inherited value later.
          </p>
          {resources.some((r) => r.kind === "file") && (
            <p className="text-sm text-muted">A file's current content is copied to DROOL (up to 64 MiB) and becomes its managed content here.</p>
          )}
          <Field label="Note (optional)" hint="Why this host is different.">
            <Input value={note} maxLength={500} onChange={(e) => setNote(e.target.value)} data-autofocus />
          </Field>
          {accept.error && <Alert>{errorMessage(accept.error)}</Alert>}
          <div className="flex justify-end gap-2">
            <Button type="button" onClick={onClose}>Cancel</Button>
            <Button type="submit" variant="primary" disabled={accept.isPending}>{accept.isPending ? "Checking…" : "Accept current state"}</Button>
          </div>
        </form>
      ) : (
        <div className="space-y-4">
          <Alert tone="ok">Accepted. {hostName} now has its own desired state for {done.length === 1 ? "this resource" : `these ${done.length} resources`}.</Alert>
          <ul className="space-y-1 text-sm">
            {done.map((a) => (
              <li key={a.key}><span className="font-mono text-xs">{describeKey(a.key)}</span>: <span className="text-muted">{a.was}</span> → <strong>{a.now}</strong></li>
            ))}
          </ul>
          <div className="flex justify-end"><Button type="button" variant="primary" onClick={onClose}>Done</Button></div>
        </div>
      )}
    </Dialog>
  );
}
