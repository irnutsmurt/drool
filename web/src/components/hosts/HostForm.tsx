import { useState, type FormEvent } from "react";
import type { Schemas } from "@/api/client";
import { useControllerKey, useCredentials, useHosts, type Host } from "@/api/queries";
import { useAuth } from "@/auth/AuthContext";
import { Button } from "@/components/ui/Button";
import { Checkbox } from "@/components/ui/Checkbox";
import { CodeBlock } from "@/components/ui/CodeBlock";
import { Field, Input, Select } from "@/components/ui/Field";

export type HostRequest = Schemas["HostRequest"];

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
  };
}

/** Create/edit form for a host's connection settings. */
export function HostForm({ host, submitLabel, busy, onSubmit, onCancel }: {
  host?: Host;
  submitLabel: string;
  busy?: boolean;
  onSubmit: (req: HostRequest) => void;
  onCancel?: () => void;
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

  function submit(e: FormEvent) {
    e.preventDefault();
    onSubmit({
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
    });
  }

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
        <Field label="SSH user">
          <Input value={v.user} required placeholder="admin"
            onChange={(e) => {
              set("user", e.target.value);
              // For new hosts, root needs no sudo; everyone else defaults to sudo.
              if (!host) set("become_method", e.target.value === "root" ? "none" : "sudo");
            }} />
        </Field>
        <Field label="SSH port">
          <Input type="number" min={1} max={65535} value={v.port} onChange={(e) => set("port", e.target.value)} />
        </Field>
      </div>

      <div className="grid gap-4 sm:grid-cols-2">
        <Field label="Credential">
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
        </Field>
        <Field label="Connect through" hint="Optional jump host.">
          <Select value={v.via_host_id} onChange={(e) => set("via_host_id", e.target.value)}>
            <option value="">Direct connection</option>
            {jumpCandidates.map((h) => (
              <option key={h.id} value={h.id}>{h.name}</option>
            ))}
          </Select>
        </Field>
      </div>

      {usingControllerKey && controllerKey.data && (
        <div className="space-y-2">
          <p className="text-sm text-muted">
            Install the controller's public key for <strong>{v.user || "the SSH user"}</strong> on the host:
          </p>
          <CodeBlock text={controllerKey.data.install_command} />
        </div>
      )}

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
            hint="Used only to reach other hosts; not managed itself." />
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
