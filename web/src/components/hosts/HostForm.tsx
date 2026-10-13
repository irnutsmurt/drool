import { useEffect, useState, type FormEvent } from "react";
import type { Schemas } from "@/api/client";
import { useControllerKey, useCredentials, useHosts, type Host } from "@/api/queries";
import { useAuth } from "@/auth/AuthContext";
import { Button } from "@/components/ui/Button";
import { Alert } from "@/components/ui/Alert";
import { Badge } from "@/components/ui/Badge";
import { Checkbox } from "@/components/ui/Checkbox";
import { CodeBlock } from "@/components/ui/CodeBlock";
import { Field, Input, Select } from "@/components/ui/Field";
import { cx } from "@/components/ui/cx";
import { ROOT_EQUIVALENT } from "./DedicatedAccount";

export type HostRequest = Schemas["HostRequest"];

/** How DROOL signs in to a new host from now on: its own dedicated account
 * (set up with the account entered, which is then used no more), or the
 * account entered. */
export type AccountChoice = { dedicated: true; user: string } | { dedicated: false };
type SetupAuth = "password" | "key" | "stored";

interface Values {
  name: string;
  description: string;
  address: string;
  port: string;
  user: string;
  credential_id: string;
  become_method: "sudo" | "none";
  become_credential_id: string;
  via_host_id: string;
  needs_pty: boolean;
  jump_only: boolean;
  tags: string;
  // New hosts only.
  dedicated: boolean;
  account: string;
  setupAuth: SetupAuth;
  setupPassword: string;
  setupKey: string;
  setupPassphrase: string;
  setupSudo: string;
}

function initial(h?: Host): Values {
  return {
    name: h?.name ?? "",
    description: h?.description ?? "",
    address: h?.address ?? "",
    port: String(h?.port ?? 22),
    user: h?.user ?? "",
    credential_id: h?.credential_id ?? "",
    become_method: h?.become_method ?? "sudo",
    become_credential_id: h?.become_credential_id ?? "",
    via_host_id: h?.via_host_id ?? "",
    needs_pty: h?.needs_pty ?? false,
    jump_only: h?.jump_only ?? false,
    tags: (h?.tags ?? []).join(", "),
    dedicated: !h,
    account: "drool",
    setupAuth: "password",
    setupPassword: "",
    setupKey: "",
    setupPassphrase: "",
    setupSudo: "",
  };
}

function Choice({ checked, onSelect, title, badge, children }: {
  checked: boolean; onSelect: () => void; title: string; badge?: string; children: React.ReactNode;
}) {
  return (
    <label className={cx("flex cursor-pointer gap-3 rounded-ui border p-3 has-[:focus-visible]:ring-2 has-[:focus-visible]:ring-accent",
      checked ? "border-accent bg-accent/10" : "border-line")}>
      <input type="radio" name="host-account" className="mt-1" checked={checked} onChange={onSelect} />
      <span className="space-y-1">
        <span className="flex flex-wrap items-center gap-2 text-sm font-medium">{title}{badge && <Badge tone="accent">{badge}</Badge>}</span>
        <span className="block text-sm text-muted">{children}</span>
      </span>
    </label>
  );
}

/** Create/edit form for a host's connection settings. */
export function HostForm({ host, submitLabel, busy, onSubmit, onCancel, onChoice }: {
  host?: Host;
  submitLabel: string;
  busy?: boolean;
  onSubmit: (req: HostRequest, account: AccountChoice) => void;
  onCancel?: () => void;
  /** Tells a new host's wizard whether DROOL's own account will be set up. */
  onChoice?: (dedicated: boolean) => void;
}) {
  const { can } = useAuth();
  const [v, setV] = useState<Values>(() => initial(host));
  const creds = useCredentials(can("credentials.use"));
  const controllerKey = useControllerKey();
  const hosts = useHosts();
  const set = <K extends keyof Values>(k: K, val: Values[K]) => setV((p) => ({ ...p, [k]: val }));

  const credential = v.credential_id || controllerKey.data?.credential_id || "";
  const sshCreds = (creds.data?.items ?? []).filter((c) => c.kind === "ssh_private_key" || c.kind === "password");
  const sudoCreds = (creds.data?.items ?? []).filter((c) => c.kind === "sudo_password" || c.kind === "password");
  // A jump host must be reached directly, and a host can't jump through itself.
  const jumpCandidates = (hosts.data?.items ?? []).filter((h) => h.id !== host?.id && !h.via_host_id);
  const usingControllerKey = credential !== "" && credential === controllerKey.data?.credential_id;
  // A new host that gets DROOL's own account: the account entered is used
  // for the setup only (a jump host only isn't managed, so it needn't).
  const dedicated = !host && v.dedicated && !v.jump_only;
  const asRoot = v.user === "root";
  useEffect(() => onChoice?.(dedicated), [dedicated, onChoice]);

  function submit(e: FormEvent) {
    e.preventDefault();
    const req: HostRequest = {
      name: v.name,
      description: v.description,
      address: v.address,
      port: Number(v.port) || 22,
      user: v.user,
      credential_id: credential || undefined,
      become_method: v.become_method,
      become_credential_id: v.become_method === "sudo" ? v.become_credential_id : "",
      via_host_id: v.via_host_id,
      needs_pty: v.needs_pty,
      jump_only: v.jump_only,
      tags: v.tags.split(/[,\s]+/).filter(Boolean),
    };
    if (!dedicated) {
      onSubmit(req, { dedicated: false });
      return;
    }
    // Entered for the setup only: stored as temporary credentials, which
    // DROOL forgets once its own account works.
    const setup: NonNullable<HostRequest["setup_credentials"]> = {};
    if (v.setupAuth === "password") setup.ssh_password = v.setupPassword;
    if (v.setupAuth === "key") {
      setup.ssh_private_key = v.setupKey;
      if (v.setupPassphrase) setup.passphrase = v.setupPassphrase;
    }
    if (!asRoot && v.setupSudo) setup.sudo_password = v.setupSudo;
    onSubmit({
      ...req,
      credential_id: v.setupAuth === "stored" ? credential || undefined : undefined,
      become_method: asRoot ? "none" : "sudo",
      become_credential_id: "",
      setup_credentials: Object.keys(setup).length > 0 ? setup : undefined,
    }, { dedicated: true, user: v.account.trim() || "drool" });
  }

  const credentialSelect = (
    <Select value={credential} onChange={(e) => set("credential_id", e.target.value)}>
      {sshCreds.length === 0 && controllerKey.data && (
        <option value={controllerKey.data.credential_id}>controller-default (SSH key)</option>
      )}
      {sshCreds.map((c) => (
        <option key={c.id} value={c.id}>
          {c.name} ({c.kind === "password" ? "password" : "SSH key"})
        </option>
      ))}
    </Select>
  );
  const installKey = usingControllerKey && controllerKey.data && (
    <div className="space-y-2">
      <p className="text-sm text-muted">
        Install the controller's public key for <strong>{v.user || "the SSH user"}</strong> on the host:
      </p>
      <CodeBlock text={controllerKey.data.install_command} />
    </div>
  );
  const jumpField = (
    <Field label="Connect through" hint="Optional jump host.">
      <Select value={v.via_host_id} onChange={(e) => set("via_host_id", e.target.value)}>
        <option value="">Direct connection</option>
        {jumpCandidates.map((h) => (
          <option key={h.id} value={h.id}>{h.name}</option>
        ))}
      </Select>
    </Field>
  );

  return (
    <form onSubmit={submit} className="space-y-5">
      <div className="grid gap-4 sm:grid-cols-2">
        <Field label="Name">
          <Input value={v.name} required autoFocus={!host} placeholder="web01"
            onChange={(e) => set("name", e.target.value)} />
        </Field>
        <Field label="Address" hint="Hostname or IP address the controller connects to.">
          <Input value={v.address} required placeholder="192.168.1.20"
            onChange={(e) => set("address", e.target.value)} />
        </Field>
        {!dedicated && (
          <Field label="SSH user">
            <Input value={v.user} required placeholder="admin"
              onChange={(e) => {
                set("user", e.target.value);
                // For new hosts, root needs no sudo; everyone else defaults to sudo.
                if (!host) set("become_method", e.target.value === "root" ? "none" : "sudo");
              }} />
          </Field>
        )}
        <Field label="SSH port">
          <Input type="number" min={1} max={65535} value={v.port} onChange={(e) => set("port", e.target.value)} />
        </Field>
        {dedicated && jumpField}
      </div>

      {!host && !v.jump_only && (
        <fieldset className="space-y-2" data-testid="account-choice">
          <legend className="mb-2 text-sm font-medium">How DROOL signs in to this host from now on</legend>
          <Choice checked={v.dedicated} onSelect={() => set("dedicated", true)} title="Create a dedicated DROOL SSH account" badge="Recommended">
            DROOL uses an account you already have only to set itself up. It creates its own account on the host, switches to it
            once that account works, and then forgets the password or key you enter here. DROOL won't delete or change the account
            you use for the setup.
          </Choice>
          <Choice checked={!v.dedicated} onSelect={() => set("dedicated", false)} title="Use this SSH account">
            DROOL keeps signing in with the account and credential you enter below.
          </Choice>
        </fieldset>
      )}

      {dedicated ? (
        <div className="space-y-4">
          <div className="grid gap-4 sm:grid-cols-2">
            <Field label="Existing SSH account" hint="An account that can sign in and use sudo (or root). Used only for the setup.">
              <Input value={v.user} required placeholder="admin" autoComplete="off"
                onChange={(e) => set("user", e.target.value)} />
            </Field>
            <Field label="DROOL's account name" hint="Created on the host. Lower-case letters, digits, - and _.">
              <Input value={v.account} required pattern="[a-z_][a-z0-9_\-]{0,31}" className="font-mono" autoComplete="off" spellCheck={false}
                onChange={(e) => set("account", e.target.value)} />
            </Field>
          </div>
          <fieldset>
            <legend className="mb-2 text-sm font-medium">For the setup, sign in with</legend>
            <div role="radiogroup" className="flex flex-wrap gap-2">
              {([["password", "The account's password"], ["key", "A private key"], ["stored", "A stored credential or DROOL's key"]] as const).map(([k, label]) => (
                <label key={k} className={cx("cursor-pointer rounded-ui border px-3 py-1.5 text-sm has-[:focus-visible]:ring-2 has-[:focus-visible]:ring-accent",
                  v.setupAuth === k ? "border-accent bg-accent/15 font-medium" : "border-line")}>
                  <input type="radio" name="setup-auth" className="sr-only" checked={v.setupAuth === k} onChange={() => set("setupAuth", k)} />
                  {label}
                </label>
              ))}
            </div>
          </fieldset>
          {v.setupAuth === "password" && (
            <Field label="Password">
              <Input type="password" required autoComplete="new-password" value={v.setupPassword}
                onChange={(e) => set("setupPassword", e.target.value)} />
            </Field>
          )}
          {v.setupAuth === "key" && (
            <div className="grid gap-4 sm:grid-cols-2">
              <Field label="Private key" hint="The whole key, as in the file (OpenSSH or PEM format).">
                <textarea required value={v.setupKey} spellCheck={false} autoComplete="off"
                  className="h-28 w-full rounded-ui border border-line bg-surface p-2 font-mono text-xs outline-none focus:border-accent focus:ring-2 focus:ring-accent/25"
                  onChange={(e) => set("setupKey", e.target.value)} />
              </Field>
              <Field label="Passphrase" hint="Only if the key has one. Not stored.">
                <Input type="password" autoComplete="new-password" value={v.setupPassphrase}
                  onChange={(e) => set("setupPassphrase", e.target.value)} />
              </Field>
            </div>
          )}
          {v.setupAuth === "stored" && (
            <>
              <Field label="Credential" hint="A stored credential stays under Credentials after the setup.">{credentialSelect}</Field>
              {installKey}
            </>
          )}
          {!asRoot && (
            <Field label="Sudo password" hint="Leave empty if sudo doesn't ask this account for a password.">
              <Input type="password" autoComplete="new-password" value={v.setupSudo} className="sm:max-w-[50%]"
                onChange={(e) => set("setupSudo", e.target.value)} />
            </Field>
          )}
          <p className="text-sm text-muted">
            What you type here is stored encrypted and used only to set up DROOL's account: after the next steps (trusting the
            host key and testing the connection), DROOL shows exactly what it will create before it changes anything, then
            forgets these details once it signs in with its own account.
          </p>
          <Alert tone="warn">{ROOT_EQUIVALENT}</Alert>
        </div>
      ) : (
        <>
          <div className="grid gap-4 sm:grid-cols-2">
            <Field label="Credential">{credentialSelect}</Field>
            {jumpField}
          </div>

          {installKey}

          {!v.jump_only && (
            <div className="grid gap-4 sm:grid-cols-2">
              <Field label="Privilege escalation">
                <Select value={v.become_method} onChange={(e) => set("become_method", e.target.value as Values["become_method"])}>
                  <option value="sudo">sudo</option>
                  <option value="none">none (connect as root)</option>
                </Select>
              </Field>
              {v.become_method === "sudo" && (
                <Field label="Sudo password">
                  <Select value={v.become_credential_id} onChange={(e) => set("become_credential_id", e.target.value)}>
                    <option value="">Not needed (passwordless sudo)</option>
                    {sudoCreds.map((c) => (
                      <option key={c.id} value={c.id}>{c.name}</option>
                    ))}
                  </Select>
                </Field>
              )}
            </div>
          )}
        </>
      )}

      <Field label="Tags" hint="Comma-separated, e.g. role:web, env:prod">
        <Input value={v.tags} onChange={(e) => set("tags", e.target.value)} />
      </Field>
      <Field label="Description">
        <Input value={v.description} onChange={(e) => set("description", e.target.value)} />
      </Field>

      <details className="text-sm">
        <summary className="cursor-pointer text-muted">Advanced</summary>
        <div className="mt-3 space-y-3">
          <Checkbox checked={v.jump_only} onChange={(x) => set("jump_only", x)} label="Jump host only"
            hint="Used only to reach other hosts; not managed itself, so DROOL keeps signing in with the account you enter." />
          <Checkbox checked={v.needs_pty} onChange={(x) => set("needs_pty", x)} label="Requires a terminal for sudo"
            hint="Enable for hosts whose sudoers uses requiretty." />
        </div>
      </details>

      <div className="flex gap-2">
        <Button type="submit" variant="primary" disabled={busy}>{submitLabel}</Button>
        {onCancel && <Button type="button" onClick={onCancel}>Cancel</Button>}
      </div>
    </form>
  );
}
