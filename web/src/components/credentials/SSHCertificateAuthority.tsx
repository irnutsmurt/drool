import { useState } from "react";
import { useNavigate } from "react-router";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api, unwrap, type Schemas } from "@/api/client";
import { useHosts } from "@/api/queries";
import { useAuth } from "@/auth/AuthContext";
import { Alert } from "@/components/ui/Alert";
import { Badge } from "@/components/ui/Badge";
import { Button } from "@/components/ui/Button";
import { Card } from "@/components/ui/Card";
import { Checkbox } from "@/components/ui/Checkbox";
import { CopyButton } from "@/components/ui/CopyButton";
import { Dialog } from "@/components/ui/Dialog";
import { Field, Input } from "@/components/ui/Field";
import { sshcaKey, useSSHCA } from "@/components/hosts/SSHSignIn";
import { errorMessage, formatDateTime } from "@/lib/format";

type CA = Schemas["SSHCA"];
const stateText: Record<CA["state"], string> = { active: "issuing", next: "introduced", previous: "replaced", retired: "retired" };
const stateTone: Record<CA["state"], "ok" | "accent" | "warn" | "neutral"> = { active: "ok", next: "accent", previous: "warn", retired: "neutral" };

/** DROOL's SSH user certificate authority on the Credentials page (JIT
 * SSH, M16): create it, roll its trust out to hosts, rotate it without
 * stranding anyone. */
export function SSHCertificateAuthority() {
  const { can } = useAuth();
  const qc = useQueryClient();
  const st = useSSHCA();
  const [dialog, setDialog] = useState<"create" | "rotate" | "trust" | null>(null);
  const set = (d: Schemas["SSHCAState"]) => qc.setQueryData(sshcaKey, d);
  const activate = useMutation({
    mutationFn: (id: string) => unwrap(api.POST("/ssh-ca/{id}/activate", { params: { path: { id } } })),
    onSuccess: set,
  });
  const retire = useMutation({
    mutationFn: (id: string) => unwrap(api.POST("/ssh-ca/{id}/retire", { params: { path: { id } } })),
    onSuccess: set,
  });
  const d = st.data;
  const live = (d?.authorities ?? []).filter((c) => c.state !== "retired");
  const active = live.find((c) => c.state === "active");
  const next = live.find((c) => c.state === "next");
  const previous = live.find((c) => c.state === "previous");
  const manage = can("credentials.manage");
  const err = st.error ?? activate.error ?? retire.error;
  return (
    <Card title="SSH certificate authority" className="mb-4 max-w-3xl">
      <div className="space-y-3 text-sm" data-testid="ssh-ca">
        {err && <Alert>{errorMessage(err)}</Alert>}
        {!d ? <p className="text-muted">Loading…</p> : !active ? (
          <>
            <p>
              Instead of a stored key that works for ever, DROOL can sign in to hosts with a short-lived SSH certificate: a fresh
              key for every connection, signed by DROOL's own certificate authority, valid for minutes and only for the host's
              login user. OpenSSH enforces the expiry. Hosts trust the authority through <code className="font-mono text-xs">TrustedUserCAKeys</code>.
            </p>
            {manage && <Button variant="primary" onClick={() => setDialog("create")}>Create certificate authority…</Button>}
          </>
        ) : (
          <>
            <ul className="space-y-3">
              {live.map((c) => (
                <li key={c.id} className="space-y-1 rounded-ui border border-line p-3">
                  <div className="flex flex-wrap items-center gap-2">
                    <span className="font-medium">{c.name}</span>
                    <Badge tone={stateTone[c.state]}>{stateText[c.state]}</Badge>
                    <span className="text-xs text-muted">created {formatDateTime(c.created_at)} · {c.issued.toLocaleString()} certificate{c.issued === 1 ? "" : "s"} issued</span>
                  </div>
                  <code className="block break-all font-mono text-xs">{c.fingerprint}</code>
                  <div className="flex flex-wrap gap-2">
                    <CopyButton text={c.public_key} label="Copy public key" />
                    {manage && c.state === "next" && (
                      <Button size="sm" variant="primary" disabled={activate.isPending || (d.unverified.length > 0)}
                        title={d.unverified.length ? "Every host in certificate mode has to accept it first" : undefined}
                        onClick={() => activate.mutate(c.id)}>Start issuing with it</Button>
                    )}
                    {manage && (c.state === "previous" || c.state === "next") && (
                      <Button size="sm" variant="danger" disabled={retire.isPending}
                        onClick={() => {
                          if (confirm(c.state === "next"
                            ? `Abandon the rotation and destroy ${c.name}'s private key?`
                            : `Retire ${c.name}? Its private key is destroyed; run the trust step on hosts afterwards so they stop trusting it too.`)) retire.mutate(c.id);
                        }}>{c.state === "next" ? "Abandon" : "Retire"}</Button>
                    )}
                  </div>
                </li>
              ))}
            </ul>
            <p className="text-muted">
              {d.certificate_hosts} host{d.certificate_hosts === 1 ? " signs" : "s sign"} in with certificates
              {d.certificate_only_hosts > 0 && <>, {d.certificate_only_hosts} of them with no other credential</>}. Certificates are
              valid from {Math.round(d.clock_skew_s / 60)} minutes before until {Math.round(d.ttl_s / 60)} minutes after they are issued.
            </p>
            {next && (
              <Alert tone={d.unverified.length ? "warn" : "ok"}>
                Rotation in progress: {d.unverified.length
                  ? <>{d.unverified.length} host{d.unverified.length === 1 ? "" : "s"} in certificate mode still {d.unverified.length === 1 ? "has" : "have"} to accept {next.name} ({d.unverified.slice(0, 5).join(", ")}{d.unverified.length > 5 ? ", …" : ""}). Set up trust on {d.unverified.length === 1 ? "it" : "them"}, then start issuing with it.</>
                  : <>every host in certificate mode accepts {next.name}; you can start issuing with it.</>}
              </Alert>
            )}
            {previous && !next && (
              <p className="text-muted">{previous.name} was replaced. Retire it once you no longer need it, then run the trust step so hosts drop it.</p>
            )}
            {manage && (
              <div className="flex flex-wrap gap-2">
                <Button onClick={() => setDialog("trust")}>Set up trust on hosts…</Button>
                {!next && <Button onClick={() => setDialog("rotate")}>Rotate…</Button>}
              </div>
            )}
            <details>
              <summary className="cursor-pointer text-muted">Trusting it by hand</summary>
              <p className="mt-2 text-muted">Where DROOL can't change the sshd configuration (it already trusts another CA file, or there's no
                sshd_config.d), add the public keys to the file that <code className="font-mono text-xs">TrustedUserCAKeys</code> names,
                then use "Check CA trust" on the host page:</p>
              <code className="mt-1 block whitespace-pre-wrap break-all font-mono text-xs">{live.map((c) => c.public_key).join("\n")}</code>
            </details>
          </>
        )}
      </div>
      {dialog === "create" && <NameDialog title="Create the SSH certificate authority" action="Create" path="create" onDone={(s) => { set(s); setDialog(null); }} onClose={() => setDialog(null)} />}
      {dialog === "rotate" && <NameDialog title="Rotate the SSH certificate authority" action="Introduce new authority" path="rotate" onDone={(s) => { set(s); setDialog(null); }} onClose={() => setDialog(null)} />}
      {dialog === "trust" && <TrustDialog onClose={() => setDialog(null)} />}
    </Card>
  );
}

function NameDialog({ title, action, path, onDone, onClose }: {
  title: string; action: string; path: "create" | "rotate";
  onDone: (s: Schemas["SSHCAState"]) => void; onClose: () => void;
}) {
  const [name, setName] = useState(path === "create" ? "drool" : `drool-${new Date().getFullYear()}`);
  const mut = useMutation({
    mutationFn: () => unwrap(path === "create" ? api.POST("/ssh-ca", { body: { name } }) : api.POST("/ssh-ca/rotate", { body: { name } })),
    onSuccess: onDone,
  });
  return (
    <Dialog title={title} onClose={onClose}>
      <form onSubmit={(e) => { e.preventDefault(); mut.mutate(); }} className="space-y-4 text-sm">
        {path === "create" ? (
          <p>DROOL creates an Ed25519 key pair; the private key is stored encrypted with the master key and never leaves the controller.
            Nothing changes on hosts until you set up their trust.</p>
        ) : (
          <p>A new authority is introduced next to the current one, which keeps issuing. Set up trust on the hosts (they then trust
            both), and once every host in certificate mode accepts the new one, start issuing with it and retire the old one. No
            host is ever left without a working authority.</p>
        )}
        <Field label="Name" hint="Shown in certificates' key ids and in the public key's comment.">
          <Input value={name} onChange={(e) => setName(e.target.value)} required maxLength={64} data-autofocus />
        </Field>
        {mut.error && <Alert>{errorMessage(mut.error)}</Alert>}
        <div className="flex justify-end gap-2">
          <Button type="button" onClick={onClose}>Cancel</Button>
          <Button type="submit" variant="primary" disabled={mut.isPending}>{action}</Button>
        </div>
      </form>
    </Dialog>
  );
}

/** Runs the trust step on chosen hosts as an ordinary run: one host first,
 * then a few at a time, pausing after two failures. */
function TrustDialog({ onClose }: { onClose: () => void }) {
  const navigate = useNavigate();
  const hosts = useHosts();
  const ssh = (hosts.data?.items ?? []).filter((h) => h.management.ssh_configured);
  const [picked, setPicked] = useState<string[]>([]);
  const mut = useMutation({
    mutationFn: () => unwrap(api.POST("/jobs", {
      body: {
        name: "Trust DROOL's SSH certificate authority", steps: [{ type: "ssh_ca_trust" }], targets: { hosts: picked },
        strategy: picked.length > 1 ? { concurrency: 5, failure_threshold: 2, canary_count: 1, batch_size: 5 } : undefined,
      },
    })),
    onSuccess: (j) => navigate(`/jobs/${j.id}`),
  });
  return (
    <Dialog title="Set up trust in DROOL's SSH certificate authority" onClose={onClose}>
      <div className="space-y-4 text-sm">
        <p>
          On each host DROOL lists its authorities in <code className="font-mono text-xs">/etc/ssh/drool_user_ca.pub</code>, points
          <code className="mx-1 font-mono text-xs">TrustedUserCAKeys</code> at it in <code className="font-mono text-xs">/etc/ssh/sshd_config.d</code>,
          checks the configuration and reloads sshd (open sessions stay up), then proves it with a brand-new connection that
          offers only a certificate. Hosts that already trust another CA are left alone. Nothing about how DROOL signs in changes yet.
        </p>
        <div className="flex gap-2">
          <Button size="sm" type="button" onClick={() => setPicked(ssh.map((h) => h.id))}>All SSH hosts ({ssh.length})</Button>
          <Button size="sm" type="button" onClick={() => setPicked([])}>None</Button>
        </div>
        <div className="max-h-64 space-y-1.5 overflow-y-auto">
          {ssh.map((h) => (
            <Checkbox key={h.id} checked={picked.includes(h.id)} label={<>{h.name}{h.jump_only && <span className="text-muted"> (jump host)</span>}</>}
              onChange={(on) => setPicked(on ? [...picked, h.id] : picked.filter((x) => x !== h.id))} />
          ))}
        </div>
        {picked.length > 1 && <p className="text-muted">One host first; then 5 at a time, pausing after 2 failures.</p>}
        {mut.error && <Alert>{errorMessage(mut.error)}</Alert>}
        <div className="flex justify-end gap-2">
          <Button onClick={onClose}>Cancel</Button>
          <Button variant="primary" disabled={!picked.length || mut.isPending} onClick={() => mut.mutate()}>
            Run on {picked.length} host{picked.length === 1 ? "" : "s"}
          </Button>
        </div>
      </div>
    </Dialog>
  );
}
