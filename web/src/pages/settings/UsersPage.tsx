import { useState, type FormEvent } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, unwrap, type Schemas } from "@/api/client";
import { useAuth } from "@/auth/AuthContext";
import { Card } from "@/components/ui/Card";
import { Table } from "@/components/ui/Table";
import { Field, Input, Select } from "@/components/ui/Field";
import { Button } from "@/components/ui/Button";
import { Badge } from "@/components/ui/Badge";
import { Alert } from "@/components/ui/Alert";
import { errorMessage, formatDateTime } from "@/lib/format";

type User = Schemas["User"];
type Role = Schemas["Role"];
const roles: Role[] = ["admin", "operator", "auditor", "viewer"];

export function UsersPage() {
  const qc = useQueryClient();
  const { me } = useAuth();
  const list = useQuery({ queryKey: ["users"], queryFn: () => unwrap(api.GET("/users")) });
  const invalidate = () => void qc.invalidateQueries({ queryKey: ["users"] });

  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [role, setRole] = useState<Role>("operator");

  const create = useMutation({
    mutationFn: () => unwrap(api.POST("/users", { body: { username, password, role } })),
    onSuccess: () => {
      setUsername("");
      setPassword("");
      invalidate();
    },
  });
  const update = useMutation({
    mutationFn: (v: { id: string; body: Schemas["UpdateUserRequest"] }) =>
      unwrap(api.PATCH("/users/{id}", { params: { path: { id: v.id } }, body: v.body })),
    onSuccess: invalidate,
  });
  const remove = useMutation({
    mutationFn: (id: string) => unwrap(api.DELETE("/users/{id}", { params: { path: { id } } })),
    onSuccess: invalidate,
  });

  function submit(e: FormEvent) {
    e.preventDefault();
    create.mutate();
  }

  const err = list.error ?? update.error ?? remove.error;
  return (
    <div className="grid gap-4">
      <Card title="Add user">
        <form onSubmit={submit} className="grid gap-4 md:grid-cols-[1fr_1fr_10rem_auto] md:items-end">
          <Field label="Username">
            <Input value={username} onChange={(e) => setUsername(e.target.value)} required />
          </Field>
          <Field label="Initial password">
            <Input type="password" autoComplete="new-password" minLength={12} value={password}
              onChange={(e) => setPassword(e.target.value)} required />
          </Field>
          <Field label="Role">
            <Select value={role} onChange={(e) => setRole(e.target.value as Role)}>
              {roles.map((r) => <option key={r} value={r}>{r}</option>)}
            </Select>
          </Field>
          <Button type="submit" variant="primary" disabled={create.isPending}>Add</Button>
        </form>
        {create.error && <div className="mt-4"><Alert>{errorMessage(create.error)}</Alert></div>}
      </Card>
      <Card title="Users">
        {err && <div className="mb-4"><Alert>{errorMessage(err)}</Alert></div>}
        <Table<User>
          rows={list.data?.items ?? []}
          rowKey={(u) => u.id}
          empty={list.isPending ? "Loading…" : "No users."}
          columns={[
            { header: "Username", cell: (u) => <>{u.username}{u.id === me?.user.id && <span className="ml-2 text-muted">(you)</span>}</> },
            {
              header: "Role",
              cell: (u) => (
                <Select className="h-8 w-32" value={u.role} disabled={update.isPending}
                  onChange={(e) => update.mutate({ id: u.id, body: { role: e.target.value as Role } })}>
                  {roles.map((r) => <option key={r} value={r}>{r}</option>)}
                </Select>
              ),
            },
            { header: "Status", cell: (u) => (u.disabled ? <Badge tone="danger">disabled</Badge> : <Badge tone="ok">active</Badge>) },
            { header: "Last login", cell: (u) => formatDateTime(u.last_login_at) },
            {
              header: "",
              cell: (u) => (
                <div className="flex justify-end gap-2">
                  <Button size="sm" disabled={update.isPending}
                    onClick={() => update.mutate({ id: u.id, body: { disabled: !u.disabled } })}>
                    {u.disabled ? "Enable" : "Disable"}
                  </Button>
                  <Button size="sm" variant="danger" disabled={remove.isPending}
                    onClick={() => { if (confirm(`Delete user ${u.username}?`)) remove.mutate(u.id); }}>
                    Delete
                  </Button>
                </div>
              ),
            },
          ]}
        />
      </Card>
    </div>
  );
}
