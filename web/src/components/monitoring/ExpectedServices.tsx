import { useState, type FormEvent } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, unwrap } from "@/api/client";
import { useAuth } from "@/auth/AuthContext";
import { Alert } from "@/components/ui/Alert";
import { Button } from "@/components/ui/Button";
import { Card } from "@/components/ui/Card";
import { Input, Select } from "@/components/ui/Field";

type Expected = "running" | "stopped" | "ignored";
const labels: Record<Expected, string> = { running: "Running", stopped: "Stopped", ignored: "Ignored" };

/** The expected state of services for a pack's hosts, or for every host:
 * the same health setting a host's Services tab sets for itself, one level
 * up (a host's own setting wins; packs follow their priority). Changing it
 * is an administrator's (monitoring.settings). */
export function ExpectedServicesCard({ scope, groupId }: { scope: "group" | "global"; groupId?: string }) {
  const qc = useQueryClient();
  const { can } = useAuth();
  const write = can("monitoring.settings");
  const key = ["monitoring", "expected-services", scope, groupId ?? ""];
  const list = useQuery({
    queryKey: key,
    queryFn: () => unwrap(api.GET("/monitoring/health/overrides", { params: { query: { scope, group_id: groupId, check: "service_expected" } } })),
  });
  const [unit, setUnit] = useState("");
  const [value, setValue] = useState<Expected>("running");
  const done = () => {
    void qc.invalidateQueries({ queryKey: key });
    void qc.invalidateQueries({ queryKey: ["monitoring"] });
  };
  const put = useMutation({
    mutationFn: (v: { unit: string; expected?: Expected }) => {
      const name = v.unit.includes(".") ? v.unit : `${v.unit}.service`;
      return unwrap(api.PUT("/monitoring/health/overrides", {
        body: { scope, group_id: groupId, check: "service_expected", subject: `unit:${name}`, subject_label: name, fields: v.expected ? { expected: v.expected } : {} },
      }));
    },
    onSuccess: () => { setUnit(""); done(); },
  });
  const items = (list.data?.items ?? []).filter((o) => o.fields.expected);
  const where = scope === "group" ? "this pack's hosts" : "every host";
  function add(e: FormEvent) {
    e.preventDefault();
    if (unit.trim()) put.mutate({ unit: unit.trim(), expected: value });
  }
  return (
    <Card title={scope === "group" ? "Expected services" : "Expected services on every host"} className="mb-4">
      <p className="mb-3 text-sm text-muted">
        Whether a service should run on {where}. <strong>Running</strong>: stopped or missing is a health issue. <strong>Stopped</strong>: running is.
        <strong> Ignored</strong>: its state stays visible but never alerts. A host's own setting (its Services tab) beats this
        {scope === "global" ? ", and so does a pack's" : "; packs that disagree follow their monitoring priority"}. Starting or stopping a
        service never changes it.
      </p>
      {list.error && <Alert>{String(list.error)}</Alert>}
      {items.length === 0 ? <p className="text-sm text-muted">No services set{scope === "group" ? " for this pack" : ""}.</p> : (
        <ul className="divide-y divide-line text-sm" aria-label={`Expected services on ${where}`}>
          {items.map((o) => {
            const name = o.subject_label || (o.subject ?? "").replace(/^unit:/, "");
            return (
              <li key={o.id} className="flex flex-wrap items-center gap-2 py-1.5">
                <span className="min-w-0 flex-1 font-mono text-xs">{name}</span>
                <Select aria-label={`Expected state of ${name}`} className="h-8 w-32" value={o.fields.expected} disabled={!write || put.isPending}
                  onChange={(e) => put.mutate({ unit: name, expected: e.target.value as Expected })}>
                  {(Object.keys(labels) as Expected[]).map((k) => <option key={k} value={k}>{labels[k]}</option>)}
                </Select>
                {write && <Button size="sm" variant="ghost" disabled={put.isPending} onClick={() => put.mutate({ unit: name })} aria-label={`Remove ${name}`}>Remove</Button>}
              </li>
            );
          })}
        </ul>
      )}
      {write && (
        <form onSubmit={add} className="mt-3 flex flex-wrap items-end gap-2">
          <label className="flex min-w-[12rem] flex-1 flex-col gap-1 text-sm">
            <span className="font-medium">Service</span>
            <Input value={unit} onChange={(e) => setUnit(e.target.value)} placeholder="nginx.service" className="font-mono" />
          </label>
          <label className="flex flex-col gap-1 text-sm">
            <span className="font-medium">Expected</span>
            <Select value={value} onChange={(e) => setValue(e.target.value as Expected)} className="w-32">
              {(Object.keys(labels) as Expected[]).map((k) => <option key={k} value={k}>{labels[k]}</option>)}
            </Select>
          </label>
          <Button type="submit" disabled={!unit.trim() || put.isPending}>Add</Button>
        </form>
      )}
      {put.error && <div className="mt-2"><Alert>{(put.error as Error).message}</Alert></div>}
    </Card>
  );
}
