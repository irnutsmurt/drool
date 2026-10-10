import { useState } from "react";
import { Link, useNavigate } from "react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, unwrap } from "@/api/client";
import { qk, type Host } from "@/api/queries";
import { useAuth } from "@/auth/AuthContext";
import { Alert } from "@/components/ui/Alert";
import { Badge } from "@/components/ui/Badge";
import { Button } from "@/components/ui/Button";
import { Dialog } from "@/components/ui/Dialog";
import { Field, Input } from "@/components/ui/Field";
import { plainLink } from "@/components/ui/links";
import { errorMessage, formatAgo } from "@/lib/format";

export const sshAuthKey = (id: string) => ["hosts", id, "ssh-auth"] as const;
export const sshcaKey = ["ssh-ca"] as const;

export function useSSHCA(enabled = true) {
  return useQuery({ queryKey: sshcaKey, queryFn: () => unwrap(api.GET("/ssh-ca")), enabled });
}

/** How DROOL signs in to a host over SSH (JIT SSH, M16): its credential,
 * or a fresh short-lived certificate from DROOL's CA for every connection;
 * switching is proven on a brand-new connection before it takes effect. */
export function SSHSignIn({ host }: { host: Host }) {
  const { can } = useAuth();
  const qc = useQueryClient();
  const navigate = useNavigate();
  const auth = useQuery({ queryKey: sshAuthKey(host.id), queryFn: () => unwrap(api.GET("/hosts/{id}/ssh-auth", { params: { path: { id: host.id } } })) });
  const ca = useSSHCA(can("credentials.use"));
  const [switching, setSwitching] = useState(false);
  const trustRun = useMutation({
    mutationFn: () => unwrap(api.POST("/jobs", {
      body: { name: `Trust DROOL's SSH certificate authority: ${host.name}`, steps: [{ type: "ssh_ca_trust" }], targets: { hosts: [host.id] } },
    })),
    onSuccess: (j) => navigate(`/jobs/${j.id}`),
  });
  const back = useMutation({
    mutationFn: () => unwrap(api.PUT("/hosts/{id}/ssh-auth", { params: { path: { id: host.id } }, body: { mode: "credential" } })),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: sshAuthKey(host.id) });
      void qc.invalidateQueries({ queryKey: qk.host(host.id) });
    },
  });
  const a = auth.data;
  if (!a) return null;
  const active = ca.data?.authorities.find((c) => c.state === "active");
  const trust = a.trust.find((t) => t.ca_id === active?.id);
  const certMode = a.mode === "certificate";
  return (
    <div className="mt-3 space-y-2 border-t border-line pt-3 text-sm" data-testid="ssh-sign-in">
      <div className="grid grid-cols-[minmax(7rem,9rem)_1fr] gap-y-1.5">
        <span className="text-muted">Signs in with</span>
        <span>
          {certMode ? <>a fresh certificate from DROOL's SSH CA for each connection{a.principal && <> (principal <code className="font-mono text-xs">{a.principal}</code>)</>}</>
            : "its credential"}
        </span>
        {active && (
          <>
            <span className="text-muted">CA trust</span>
            <span>
              {trust?.verified_at && !(trust.failed_at && trust.failed_at > trust.verified_at)
                ? <><Badge tone="ok">accepted</Badge> checked {formatAgo(trust.verified_at)}</>
                : trust?.failed_at ? <><Badge tone="danger">refused</Badge> {trust.error}</>
                  : <span className="text-muted">not set up</span>}
            </span>
          </>
        )}
      </div>
      {certMode && !a.credential_assigned && (
        <Alert tone="warn">
          DROOL's SSH certificate authority is the only way DROOL can sign in to this host. If the controller's master key were
          lost, you'd need console access or another key to let DROOL back in.
        </Alert>
      )}
      {can("credentials.manage") && active && (
        <div className="flex flex-wrap gap-2">
          <Button size="sm" onClick={() => trustRun.mutate()} disabled={trustRun.isPending}
            title="Runs a step that installs DROOL's CA in the host's sshd configuration and checks it with a fresh connection">
            {trust?.verified_at ? "Check CA trust again" : "Set up CA trust"}
          </Button>
          {!certMode && trust?.verified_at && <Button size="sm" variant="primary" onClick={() => setSwitching(true)}>Use certificates…</Button>}
          {certMode && a.credential_assigned && (
            <Button size="sm" onClick={() => back.mutate()} disabled={back.isPending}>Go back to the credential</Button>
          )}
        </div>
      )}
      {!active && can("credentials.manage") && (
        <p className="text-muted">Short-lived SSH certificates instead of a stored key: set up DROOL's SSH certificate authority under <Link className={plainLink} to="/credentials">Credentials</Link>.</p>
      )}
      {(back.error ?? trustRun.error) && <Alert>{errorMessage(back.error ?? trustRun.error)}</Alert>}
      {switching && <SwitchDialog host={host} principal={a.principal} onClose={() => setSwitching(false)} />}
    </div>
  );
}

function SwitchDialog({ host, principal, onClose }: { host: Host; principal: string; onClose: () => void }) {
  const qc = useQueryClient();
  const [p, setP] = useState(principal);
  const mut = useMutation({
    mutationFn: () => unwrap(api.PUT("/hosts/{id}/ssh-auth", { params: { path: { id: host.id } }, body: { mode: "certificate", principal: p } })),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: sshAuthKey(host.id) });
      void qc.invalidateQueries({ queryKey: qk.host(host.id) });
      void qc.invalidateQueries({ queryKey: sshcaKey });
      onClose();
    },
  });
  return (
    <Dialog title={`Sign in to ${host.name} with certificates`} onClose={onClose}>
      <div className="space-y-4 text-sm">
        <p>
          DROOL opens a brand-new connection that offers only a certificate from its CA. Only if that works does it switch;
          otherwise nothing changes. From then on every connection gets its own certificate, valid for minutes, and the
          stored credential is no longer used (you can keep it as a way back, or remove it later).
        </p>
        <Field label="Principal" hint={`Leave empty to use the SSH user (${host.user}). Set it when the host maps certificates with AuthorizedPrincipalsFile.`}>
          <Input value={p} onChange={(e) => setP(e.target.value)} placeholder={host.user} data-autofocus />
        </Field>
        {mut.error && <Alert>{errorMessage(mut.error)}</Alert>}
        <div className="flex justify-end gap-2">
          <Button onClick={onClose}>Cancel</Button>
          <Button variant="primary" onClick={() => mut.mutate()} disabled={mut.isPending}>{mut.isPending ? "Checking…" : "Check and switch"}</Button>
        </div>
      </div>
    </Dialog>
  );
}
