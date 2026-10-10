import { useState, type FormEvent } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api, unwrap } from "@/api/client";
import { qk, useControllerKey, useCredentials, type Credential } from "@/api/queries";
import { useAuth } from "@/auth/AuthContext";
import { Alert } from "@/components/ui/Alert";
import { Button } from "@/components/ui/Button";
import { Card } from "@/components/ui/Card";
import { CodeBlock } from "@/components/ui/CodeBlock";
import { CopyButton } from "@/components/ui/CopyButton";
import { Field, Input, Select } from "@/components/ui/Field";
import { PageHeader } from "@/components/ui/PageHeader";
import { Table } from "@/components/ui/Table";
import { errorMessage, formatDateTime } from "@/lib/format";
import { SSHCertificateAuthority } from "@/components/credentials/SSHCertificateAuthority";

type Mode = "generate" | "import" | "password" | "sudo_password";

const kindLabel: Record<Credential["kind"], string> = {
  ssh_private_key: "SSH key",
  password: "Password",
  sudo_password: "Sudo password",
};

export function CredentialsPage() {
  const qc = useQueryClient();
  const { can } = useAuth();
  const list = useCredentials();
  const controllerKey = useControllerKey();
  const [mode, setMode] = useState<Mode>("generate");
  const [name, setName] = useState("");
  const [value, setValue] = useState("");
  const [passphrase, setPassphrase] = useState("");

  const create = useMutation({
    mutationFn: () => {
      const kind = mode === "generate" || mode === "import" ? "ssh_private_key" : mode;
      return unwrap(api.POST("/secrets", {
        body: { name, kind, generate: mode === "generate", value: mode === "generate" ? undefined : value,
          passphrase: mode === "import" && passphrase ? passphrase : undefined },
      }));
    },
    onSuccess: () => {
      setName("");
      setValue("");
      setPassphrase("");
      void qc.invalidateQueries({ queryKey: qk.credentials });
    },
  });
  const remove = useMutation({
    mutationFn: (id: string) => unwrap(api.DELETE("/secrets/{id}", { params: { path: { id } } })),
    onSuccess: () => void qc.invalidateQueries({ queryKey: qk.credentials }),
  });

  function submit(e: FormEvent) {
    e.preventDefault();
    create.mutate();
  }

  return (
    <>
      <PageHeader title="Credentials"
        description="SSH keys and passwords used to reach hosts. Stored encrypted; values are never shown again." />
      {controllerKey.data && (
        <Card title="DROOL's SSH key" className="mb-4 max-w-3xl">
          <div className="space-y-3 text-sm">
            <p>
              New hosts use this key by default. To let DROOL log in, run this on the host as the user DROOL should connect
              as (it adds the key to that user's <code className="font-mono text-xs">~/.ssh/authorized_keys</code>):
            </p>
            <CodeBlock text={controllerKey.data.install_command} />
            <p className="text-muted">
              The private key never leaves the controller. You can also add your own SSH key or a password below.
            </p>
          </div>
        </Card>
      )}
      <SSHCertificateAuthority />
      {can("credentials.manage") && (
        <Card title="Add credential" className="mb-4 max-w-3xl">
          <form onSubmit={submit} className="space-y-4">
            {create.error && <Alert>{errorMessage(create.error)}</Alert>}
            <div className="grid gap-4 sm:grid-cols-2">
              <Field label="Type">
                <Select value={mode} onChange={(e) => setMode(e.target.value as Mode)}>
                  <option value="generate">Generate new SSH key</option>
                  <option value="import">Import existing SSH private key</option>
                  <option value="password">SSH password</option>
                  <option value="sudo_password">Sudo password</option>
                </Select>
              </Field>
              <Field label="Name"><Input value={name} required placeholder="ops-key" onChange={(e) => setName(e.target.value)} /></Field>
            </div>
            {mode === "import" && (
              <>
                <Field label="Private key">
                  <textarea className="h-32 w-full rounded-ui border border-line bg-surface p-3 font-mono text-xs"
                    value={value} required placeholder="-----BEGIN OPENSSH PRIVATE KEY-----" onChange={(e) => setValue(e.target.value)} />
                </Field>
                <Field label="Passphrase" hint="Only if the key is encrypted. Used once to decrypt; not stored.">
                  <Input type="password" autoComplete="off" value={passphrase} onChange={(e) => setPassphrase(e.target.value)} />
                </Field>
              </>
            )}
            {(mode === "password" || mode === "sudo_password") && (
              <Field label="Password">
                <Input type="password" autoComplete="new-password" value={value} required onChange={(e) => setValue(e.target.value)} />
              </Field>
            )}
            <Button type="submit" variant="primary" disabled={create.isPending}>Add</Button>
          </form>
        </Card>
      )}
      <Card>
        {(list.error || remove.error) && <div className="mb-3"><Alert>{errorMessage(list.error ?? remove.error)}</Alert></div>}
        <Table<Credential>
          rows={list.data?.items ?? []}
          rowKey={(c) => c.id}
          empty={list.isPending ? "Loading…" : "No credentials."}
          columns={[
            { header: "Name", cell: (c) => <span className="font-medium">{c.name}</span> },
            { header: "Type", cell: (c) => kindLabel[c.kind] },
            { header: "Used by", cell: (c) => (c.used_by.length ? c.used_by.map((h) => h.name).join(", ") : "—") },
            { header: "Public key", cell: (c) => (c.public_key ? <CopyButton text={c.public_key} label="Copy public key" /> : "—") },
            { header: "Created", cell: (c) => <span className="whitespace-nowrap text-muted">{formatDateTime(c.created_at)}</span> },
            {
              header: "",
              cell: (c) => can("credentials.manage") && c.name !== "controller-default" && (
                <Button size="sm" variant="danger" disabled={remove.isPending || c.used_by.length > 0}
                  title={c.used_by.length ? "In use by hosts" : undefined}
                  onClick={() => { if (confirm(`Delete credential ${c.name}?`)) remove.mutate(c.id); }}>
                  Delete
                </Button>
              ),
            },
          ]}
        />
      </Card>
    </>
  );
}
