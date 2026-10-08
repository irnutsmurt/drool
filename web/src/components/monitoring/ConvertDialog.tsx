import { useState } from "react";
import { Link } from "react-router";
import { useMutation } from "@tanstack/react-query";
import { api, unwrap } from "@/api/client";
import { Alert } from "@/components/ui/Alert";
import { Button } from "@/components/ui/Button";
import { Dialog } from "@/components/ui/Dialog";
import { Field, Input, Select } from "@/components/ui/Field";
import { errorMessage } from "@/lib/format";
import type { ConversionPreview } from "./queries";

const statusWord: Record<string, string> = { compatible: "Compatible", agent: "Already agent-managed", unsupported: "Unsupported", unreachable: "Unreachable" };

/** Bulk SSH → agent conversion: preview, then a canary-first rollout of
 * the existing single-host installation over each host's pinned SSH
 * connection. */
export function ConvertDialog({ hostIds, onClose }: { hostIds: string[]; onClose: () => void }) {
  const [pv, setPv] = useState<ConversionPreview>();
  const [preset, setPreset] = useState("managed");
  const [canary, setCanary] = useState(1);
  const [batch, setBatch] = useState(5);
  const preview = useMutation({
    mutationFn: () => unwrap(api.POST("/agents/conversions/preview", { body: { host_ids: hostIds } })),
    onSuccess: setPv,
  });
  const start = useMutation({
    mutationFn: () => unwrap(api.POST("/agents/conversions", { body: { host_ids: hostIds, preset: preset as "managed", canary, batch_size: batch, max_failures: 1 } })),
  });
  if (!pv && !preview.isPending && !preview.error) preview.mutate();
  const n = pv?.counts.compatible ?? 0;
  return (
    <Dialog title="Convert to agent management" onClose={onClose} wide>
      {preview.error && <Alert>{errorMessage(preview.error)}</Alert>}
      {!pv ? <p className="text-sm text-muted">Checking the selected hosts…</p> : (
        <div className="space-y-3 text-sm">
          <p><strong>{pv.selected}</strong> host{pv.selected === 1 ? "" : "s"} selected</p>
          <ul className="flex flex-wrap gap-x-4">
            {(["compatible", "agent", "unsupported", "unreachable"] as const).map((k) => <li key={k}>{statusWord[k]}: <strong>{pv.counts[k] ?? 0}</strong></li>)}
          </ul>
          <table className="w-full text-left text-sm">
            <caption className="sr-only">Hosts</caption>
            <thead><tr className="border-b border-line text-xs text-muted"><th className="py-1 font-medium">Host</th><th className="py-1 font-medium">Status</th><th className="py-1 font-medium">Why</th></tr></thead>
            <tbody>{pv.hosts.map((h) => (
              <tr key={h.host_id} className="border-b border-line last:border-0"><td className="py-1">{h.name}</td><td className="py-1">{statusWord[h.status]}</td><td className="py-1 text-xs text-muted">{h.reason ?? (h.arch ? `linux/${h.arch}` : "")}</td></tr>
            ))}</tbody>
          </table>
          {Object.entries(pv.binary).map(([arch, what]) => <p key={arch} className="text-xs text-muted">linux/{arch}: {what || "no binary on the controller"}</p>)}
          <div className="grid gap-3 sm:grid-cols-3">
            <Field label="Capability preset" hint="What DROOL may do on each host (the host's local ceiling)">
              <Select value={preset} onChange={(e) => setPreset(e.target.value)}>
                <option value="monitor">Monitor only</option>
                <option value="managed">Managed</option>
                <option value="full">Full (root-equivalent)</option>
              </Select>
            </Field>
            <Field label="Canary hosts" hint="Converted first, alone"><Input type="number" min={1} max={50} value={canary} onChange={(e) => setCanary(Number(e.target.value) || 1)} /></Field>
            <Field label="Batch size" hint="Hosts at a time after the canary"><Input type="number" min={1} max={50} value={batch} onChange={(e) => setBatch(Number(e.target.value) || 5)} /></Field>
          </div>
          <p className="text-xs text-muted">
            Each host gets the agent over its already trusted SSH connection with a short-lived, single-use token on stdin; the agent creates
            its own identity key and must connect back over mutual TLS before the host counts as converted. SSH stays configured as the
            fallback. The rollout pauses at the first failure, and a failed host stays managed over SSH.
          </p>
          {start.error && <Alert>{errorMessage(start.error)}</Alert>}
          {start.data && <Alert tone="ok">Started. Follow it under <Link className="underline" to="/agents">Agents → Rollouts</Link>.</Alert>}
          <div className="flex justify-end gap-2">
            <Button onClick={onClose}>{start.data ? "Close" : "Cancel"}</Button>
            {!start.data && <Button variant="primary" disabled={n === 0 || start.isPending} onClick={() => start.mutate()}>{start.isPending ? "Starting…" : `Convert ${n} host${n === 1 ? "" : "s"}`}</Button>}
          </div>
        </div>
      )}
    </Dialog>
  );
}
