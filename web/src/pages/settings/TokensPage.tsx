import { useState, type FormEvent } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, unwrap, type Schemas } from "@/api/client";
import { Card } from "@/components/ui/Card";
import { Table } from "@/components/ui/Table";
import { Field, Input } from "@/components/ui/Field";
import { Button } from "@/components/ui/Button";
import { Alert } from "@/components/ui/Alert";
import { errorMessage, formatDateTime } from "@/lib/format";

type Token = Schemas["APIToken"];

export function TokensPage() {
  const qc = useQueryClient();
  const list = useQuery({ queryKey: ["tokens"], queryFn: () => unwrap(api.GET("/tokens")) });
  const [name, setName] = useState("");
  const [days, setDays] = useState("");
  const [created, setCreated] = useState<string>();

  const create = useMutation({
    mutationFn: () =>
      unwrap(api.POST("/tokens", { body: { name, expires_in_days: days ? Number(days) : undefined } })),
    onSuccess: (t) => {
      setCreated(t.token);
      setName("");
      setDays("");
      void qc.invalidateQueries({ queryKey: ["tokens"] });
    },
  });
  const revoke = useMutation({
    mutationFn: (id: string) => unwrap(api.DELETE("/tokens/{id}", { params: { path: { id } } })),
    onSuccess: () => void qc.invalidateQueries({ queryKey: ["tokens"] }),
  });

  function submit(e: FormEvent) {
    e.preventDefault();
    setCreated(undefined);
    create.mutate();
  }

  return (
    <div className="grid max-w-3xl gap-4">
      <Card title="Create token">
        <form onSubmit={submit} className="grid gap-4 sm:grid-cols-[1fr_10rem_auto] sm:items-end">
          <Field label="Name">
            <Input value={name} onChange={(e) => setName(e.target.value)} maxLength={100} required />
          </Field>
          <Field label="Expires in (days)">
            <Input type="number" min={1} max={3650} placeholder="Never" value={days} onChange={(e) => setDays(e.target.value)} />
          </Field>
          <Button type="submit" variant="primary" disabled={create.isPending}>Create</Button>
        </form>
        {create.error && <div className="mt-4"><Alert>{errorMessage(create.error)}</Alert></div>}
        {created && (
          <div className="mt-4 space-y-2">
            <Alert tone="warn">Copy this token now. It will not be shown again.</Alert>
            <code className="block break-all rounded-fm bg-surface-2 p-3 font-mono text-xs">{created}</code>
          </div>
        )}
      </Card>
      <Card title="Your tokens">
        {(list.error || revoke.error) && <Alert>{errorMessage(list.error ?? revoke.error)}</Alert>}
        <Table<Token>
          rows={list.data?.items ?? []}
          rowKey={(t) => t.id}
          empty={list.isPending ? "Loading…" : "No tokens."}
          columns={[
            { header: "Name", cell: (t) => t.name },
            { header: "Created", cell: (t) => formatDateTime(t.created_at) },
            { header: "Last used", cell: (t) => formatDateTime(t.last_used_at) },
            { header: "Expires", cell: (t) => (t.expires_at ? formatDateTime(t.expires_at) : "Never") },
            {
              header: "",
              cell: (t) => (
                <Button size="sm" variant="danger" disabled={revoke.isPending}
                  onClick={() => { if (confirm(`Revoke token "${t.name}"?`)) revoke.mutate(t.id); }}>
                  Revoke
                </Button>
              ),
            },
          ]}
        />
      </Card>
    </div>
  );
}
