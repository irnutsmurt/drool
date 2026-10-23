import { useState, type FormEvent } from "react";
import { Link } from "react-router";
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
import { MenuButton, type MenuItem } from "@/components/ui/Menu";
import { plainLink } from "@/components/ui/links";
import { errorMessage, formatDateTime } from "@/lib/format";
import { confirmAction } from "@/components/ui/ConfirmDialog";
import { emptyText } from "@/components/ui/States";
import { roleInfo } from "@/auth/roles";

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
  const [changingRole, setChangingRole] = useState<User | null>(null);
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
  // The rarer changes, each confirmed where it takes something away; Delete last.
  const moreItems = (u: User): MenuItem[] => {
    const items: MenuItem[] = [{ label: "Set password…", hint: "For a forgotten password or a first one", onSelect: () => { setPasswordSet(""); setSettingPassword(u); } }];
    if (u.mfa_enabled) {
      items.push({ label: "Reset two-factor…", hint: "For a lost authenticator", onSelect: () => void confirmAction({ title: `Turn off two-factor authentication for ${u.username}?`, confirmLabel: "Reset two-factor", danger: true,
        body: <p>Use this when they lost their authenticator and recovery codes. They are signed out, and sign in with their password only until they set two-factor up again.</p> })
        .then((ok) => ok && resetMfa.mutate(u.id)) });
    }
    items.push(u.disabled
      ? { label: "Enable", hint: "They can sign in again", onSelect: () => update.mutate({ id: u.id, body: { disabled: false } }) }
      : { label: "Disable…", hint: "When someone leaves: their history stays", onSelect: () => void confirmAction({ title: `Disable ${u.username}?`, confirmLabel: "Disable user", danger: true,
        body: <p>{u.username} is signed out at once and can&apos;t sign in or use their API tokens until an administrator enables the account again. What they did stays in the audit log. Schedules they own stop running meanwhile.</p> })
        .then((ok) => ok && update.mutate({ id: u.id, body: { disabled: true } })) });
    items.push({ label: "Delete user…", danger: true, separated: true, onSelect: () => void confirmAction({ title: `Delete the user ${u.username}?`, confirmLabel: "Delete user", danger: true,
      body: <p>{u.username} is signed out and can&apos;t sign in any more. What they did stays in the audit log under their name. Schedules they own stop running until an administrator assigns them a new owner. To keep the account for later, disable it instead.</p> })
      .then((ok) => ok && remove.mutate(u.id)) });
    return items;
  };
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
              {roles.map((r) => <option key={r} value={r}>{roleInfo[r].name}</option>)}
            </Select>
          </Field>
          <Button type="submit" variant="primary" disabled={create.isPending}>Add</Button>
        </form>
        <p className="mt-2 text-xs text-muted" data-testid="role-hint">{roleInfo[role].name}: {roleInfo[role].can}</p>
        {create.error && <div className="mt-4"><Alert>{errorMessage(create.error)}</Alert></div>}
      </Card>
      <Card title="Users">
        {err && <div className="mb-4"><Alert>{errorMessage(err)}</Alert></div>}
        {passwordSet && <div className="mb-4"><Alert tone="ok">{passwordSet}</Alert></div>}
        <Table<User>
          rows={list.data?.items ?? []}
          rowKey={(u) => u.id}
          empty={emptyText("users", list, "No users.")}
          columns={[
            { header: "Username", cell: (u) => <>{u.username}{u.id === me?.user.id && <span className="ml-2 text-muted">(you)</span>}</> },
            { header: "Role", cell: (u) => roleInfo[u.role]?.name ?? u.role },
            { header: "Status", cell: (u) => (u.disabled ? <Badge tone="danger">disabled</Badge> : <Badge tone="ok">active</Badge>) },
            {
              header: "Two-factor",
              cell: (u) => u.mfa_enabled ? <Badge tone="ok">on</Badge>
                : <span className={u.role === "admin" ? "text-warn" : "text-muted"}>off{u.role === "admin" && " (recommended for administrators)"}</span>,
            },
            { header: "Last login", cell: (u) => formatDateTime(u.last_login_at) },
            {
              header: "",
              // Your own account is changed under Account; DROOL never lets
              // you disable, demote or delete yourself.
              cell: (u) => isMe(u) ? <Link className={`${plainLink} text-sm`} to="/settings/account">Your account</Link> : (
                <div className="flex justify-end gap-2">
                  <Button size="sm" onClick={() => setChangingRole(u)} aria-label={`Change role of ${u.username}`}>Change role…</Button>
                  <MenuButton size="sm" label="More" ariaLabel={`More for ${u.username}`} items={moreItems(u)} />
                </div>
              ),
            },
          ]}
        />
        <p className="mt-3 text-xs text-muted">Your own password: Settings → Account. Locked out with no administrator who can sign in?
          On the controller, <code className="font-mono">drool user set-password --username NAME</code>.</p>
      </Card>
      {changingRole && <ChangeRoleDialog user={changingRole} onClose={() => setChangingRole(null)} onDone={() => { setChangingRole(null); invalidate(); }} />}
      {settingPassword && (
        <SetPasswordDialog user={settingPassword} onClose={() => setSettingPassword(null)}
          onDone={(u) => { setSettingPassword(null); setPasswordSet(`Password set for ${u.username}. They were signed out everywhere and sign in with the new password${u.mfa_enabled ? " and their authenticator" : ""}.`); invalidate(); }} />
      )}
    </div>
  );
}

/** Changing someone's role is a decision: the roles side by side with what
 * each may do, the current one marked, and what changes when it's saved. */
function ChangeRoleDialog({ user, onClose, onDone }: { user: User; onClose: () => void; onDone: () => void }) {
  const [picked, setPicked] = useState<Role>(user.role);
  const save = useMutation({
    mutationFn: () => unwrap(api.PATCH("/users/{id}", { params: { path: { id: user.id } }, body: { role: picked } })),
    onSuccess: onDone,
  });
  return (
    <Dialog title={`Change the role of ${user.username}`} onClose={onClose}>
      <form className="space-y-4 text-sm" onSubmit={(e) => { e.preventDefault(); if (picked !== user.role) save.mutate(); }} data-testid="change-role">
        <fieldset className="space-y-2">
          <legend className="sr-only">Role</legend>
          {roles.map((r) => (
            <label key={r} className={`flex items-start gap-2 rounded-ui border p-2 ${picked === r ? "border-accent bg-accent/5" : "border-line"}`}>
              <input type="radio" name="role" className="mt-1 accent-[var(--ui-accent)]" checked={picked === r} onChange={() => setPicked(r)} />
              <span>
                <span className="font-medium">{roleInfo[r].name}</span>{r === user.role && <span className="text-muted"> (now)</span>}
                <span className="block text-xs text-muted">{roleInfo[r].can}</span>
              </span>
            </label>
          ))}
        </fieldset>
        <p className="text-xs text-muted">It applies at once, to the sessions and API tokens {user.username} has now. Schedules run with their owner&apos;s
          permissions: those {user.username} owns that need something the new role can&apos;t do stop running.</p>
        {save.error && <Alert>{errorMessage(save.error)}</Alert>}
        <div className="flex justify-end gap-2">
          <Button type="button" onClick={onClose}>Cancel</Button>
          <Button type="submit" variant="primary" disabled={picked === user.role || save.isPending}>
            {save.isPending ? "Saving…" : picked === user.role ? "Change role" : `Make ${user.username} ${/^[AEIOU]/.test(roleInfo[picked].name) ? "an" : "a"} ${roleInfo[picked].name.toLowerCase()}`}
          </Button>
        </div>
      </form>
    </Dialog>
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
