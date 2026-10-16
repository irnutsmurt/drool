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
import { Dialog } from "@/components/ui/Dialog";
import { errorMessage, formatDateTime } from "@/lib/format";
import { confirmAction } from "@/components/ui/ConfirmDialog";

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
  const [settingPassword, setSettingPassword] = useState<User | null>(null);
  const [passwordSet, setPasswordSet] = useState("");

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
  const resetMfa = useMutation({
    mutationFn: (id: string) => unwrap(api.POST("/users/{id}/mfa/reset", { params: { path: { id } } })),
    onSuccess: invalidate,
  });

  function submit(e: FormEvent) {
    e.preventDefault();
    create.mutate();
  }

  const err = list.error ?? update.error ?? remove.error ?? resetMfa.error;
  const isMe = (u: User) => u.id === me?.user.id;
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
        {passwordSet && <div className="mb-4"><Alert tone="ok">{passwordSet}</Alert></div>}
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
            {
              header: "Two-factor",
              cell: (u) => u.mfa_enabled ? <Badge tone="ok">on</Badge>
                : <span className={u.role === "admin" ? "text-warn" : "text-muted"}>off{u.role === "admin" && " (recommended for administrators)"}</span>,
            },
            { header: "Last login", cell: (u) => formatDateTime(u.last_login_at) },
            {
              header: "",
              cell: (u) => (
                <div className="flex justify-end gap-2">
                  {!isMe(u) && (
                    <Button size="sm" onClick={() => { setPasswordSet(""); setSettingPassword(u); }} aria-label={`Set password for ${u.username}`}>
                      Set password…
                    </Button>
                  )}
                  <Button size="sm" disabled={update.isPending}
                    onClick={() => update.mutate({ id: u.id, body: { disabled: !u.disabled } })}>
                    {u.disabled ? "Enable" : "Disable"}
                  </Button>
                  {u.mfa_enabled && u.id !== me?.user.id && (
                    <Button size="sm" disabled={resetMfa.isPending}
                      onClick={() => {
                        void confirmAction({ title: `Turn off two-factor authentication for ${u.username}?`, confirmLabel: "Reset two-factor", danger: true,
                          body: <p>Use this when they lost their authenticator and recovery codes. They are signed out, and sign in with their password only until they set two-factor up again.</p> })
                          .then((ok) => ok && resetMfa.mutate(u.id));
                      }}>
                      Reset two-factor
                    </Button>
                  )}
                  <Button size="sm" variant="danger" disabled={remove.isPending}
                    onClick={() => void confirmAction({ title: `Delete the user ${u.username}?`, confirmLabel: "Delete user", danger: true,
                      body: <p>{u.username} is signed out and can&apos;t sign in any more. What they did stays in the audit log under their name. Schedules they own stop running until an administrator assigns them a new owner.</p> })
                      .then((ok) => ok && remove.mutate(u.id))}>
                    Delete
                  </Button>
                </div>
              ),
            },
          ]}
        />
        <p className="mt-3 text-xs text-muted">Your own password: Settings → Account. Locked out with no administrator who can sign in?
          On the controller, <code className="font-mono">drool user set-password --username NAME</code>.</p>
      </Card>
      {settingPassword && (
        <SetPasswordDialog user={settingPassword} onClose={() => setSettingPassword(null)}
          onDone={(u) => { setSettingPassword(null); setPasswordSet(`Password set for ${u.username}. They were signed out everywhere and sign in with the new password${u.mfa_enabled ? " and their authenticator" : ""}.`); invalidate(); }} />
      )}
    </div>
  );
}

/** An administrator sets another user's password (forgotten, or a new
 * colleague's first one): their sessions end; two-factor stays as it is.
 * The server asks the administrator to confirm who they are first. */
function SetPasswordDialog({ user, onClose, onDone }: { user: User; onClose: () => void; onDone: (u: User) => void }) {
  const [password, setPassword] = useState("");
  const [again, setAgain] = useState("");
  const mismatch = again !== "" && again !== password;
  const set = useMutation({
    mutationFn: () => unwrap(api.PATCH("/users/{id}", { params: { path: { id: user.id } }, body: { password } })),
    onSuccess: () => onDone(user),
  });
  return (
    <Dialog title={`Set a new password for ${user.username}`} onClose={onClose}>
      <form className="space-y-4 text-sm" onSubmit={(e) => { e.preventDefault(); if (!mismatch) set.mutate(); }}>
        <p>{user.username} is signed out of every session and signs in with the new password{user.mfa_enabled ? " and their authenticator (two-factor stays on)" : ""}.
          Give it to them through a channel you trust; they can change it under Settings → Account.</p>
        <Field label="New password" hint="At least 12 characters.">
          <Input type="password" autoComplete="new-password" minLength={12} value={password} onChange={(e) => setPassword(e.target.value)} required data-autofocus />
        </Field>
        <Field label="New password again">
          <Input type="password" autoComplete="new-password" minLength={12} value={again} onChange={(e) => setAgain(e.target.value)} required />
        </Field>
        {mismatch && <Alert tone="warn">The two passwords differ.</Alert>}
        {set.error && <Alert>{errorMessage(set.error)}</Alert>}
        <div className="flex justify-end gap-2">
          <Button type="button" onClick={onClose}>Cancel</Button>
          <Button type="submit" variant="primary" disabled={set.isPending || mismatch || password.length < 12}>{set.isPending ? "Setting…" : "Set password"}</Button>
        </div>
      </form>
    </Dialog>
  );
}
