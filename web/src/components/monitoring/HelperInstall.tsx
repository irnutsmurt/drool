import { useState } from "react";
import { useMutation } from "@tanstack/react-query";
import { Link } from "react-router";
import { api, unwrap } from "@/api/client";
import { useAuth } from "@/auth/AuthContext";
import { Alert } from "@/components/ui/Alert";
import { Button } from "@/components/ui/Button";
import { Dialog } from "@/components/ui/Dialog";
import { errorMessage } from "@/lib/format";
import { useMonInvalidate, type MonHelperPreview } from "./queries";

/** Previews and starts the installation of monitoring helpers on some
 * hosts, as ordinary package jobs. */
export function HelperInstallDialog({ hostIds, packages, onClose }: { hostIds: string[]; packages?: string[]; onClose: () => void }) {
  const inv = useMonInvalidate();
  const [pv, setPv] = useState<MonHelperPreview>();
  const preview = useMutation({
    mutationFn: () => unwrap(api.POST("/monitoring/helpers/preview", { body: { host_ids: hostIds, packages: packages as never } })),
    onSuccess: setPv,
  });
  const install = useMutation({
    mutationFn: () => unwrap(api.POST("/monitoring/helpers/install", { body: { host_ids: hostIds, packages: packages as never } })),
    onSuccess: () => inv(),
  });
  if (!pv && !preview.isPending && !preview.error) preview.mutate();
  return (
    <Dialog title="Install monitoring helpers" onClose={onClose} wide>
      {preview.error && <Alert>{errorMessage(preview.error)}</Alert>}
      {!pv ? <p className="text-sm text-muted">Working out what each host needs…</p> : (
        <div className="space-y-3 text-sm">
          <p><strong>{pv.hosts}</strong> host{pv.hosts === 1 ? "" : "s"} selected{Object.keys(pv.counts).length > 0 && ":"}</p>
          <ul className="list-inside list-disc">
            {Object.entries(pv.counts).map(([p, n]) => <li key={p}>{n} need{n === 1 ? "s" : ""} <strong>{p}</strong></li>)}
          </ul>
          <table className="w-full text-left text-sm">
            <caption className="sr-only">Packages per host</caption>
            <thead><tr className="border-b border-line text-xs text-muted"><th className="py-1 font-medium">Host</th><th className="py-1 font-medium">Packages that will be installed</th></tr></thead>
            <tbody>
              {pv.plan.map((p) => (
                <tr key={p.host_id} className="border-b border-line last:border-0">
                  <td className="py-1">{p.name}</td>
                  <td className="py-1">{p.skip ? <span className="text-muted">{p.skip}</span> : <span className="font-mono text-xs">{p.packages.join(" ")}</span>}</td>
                </tr>
              ))}
            </tbody>
          </table>
          <p className="text-xs text-muted">
            Installed with the normal package job (the same locks, capability checks and audit as any install), from the hosts'
            configured repositories. DROOL never installs GPU drivers and never uninstalls helpers later. A host where the
            installation fails keeps working with reduced monitoring.
          </p>
          {install.error && <Alert>{errorMessage(install.error)}</Alert>}
          {install.data && (
            <Alert tone="ok">
              Started {install.data.jobs.length} job{install.data.jobs.length === 1 ? "" : "s"}:{" "}
              {install.data.jobs.map((j) => <Link key={j} className="underline" to={`/jobs/${j}`}>{j}</Link>)}
            </Alert>
          )}
          <div className="flex justify-end gap-2">
            <Button onClick={onClose}>{install.data ? "Close" : "Cancel"}</Button>
            {!install.data && (
              <Button variant="primary" disabled={pv.installs === 0 || install.isPending} onClick={() => install.mutate()}>
                {install.isPending ? "Starting…" : `Install on ${pv.installs} host${pv.installs === 1 ? "" : "s"}`}
              </Button>
            )}
          </div>
        </div>
      )}
    </Dialog>
  );
}

/** "Install smartmontools" for one host. */
export function InstallHelperButton({ hostId, pkg, label }: { hostId: string; pkg: string; label?: string }) {
  const { can } = useAuth();
  const [open, setOpen] = useState(false);
  if (!can("jobs.execute")) return null;
  return (
    <>
      <Button size="sm" onClick={() => setOpen(true)}>{label ?? `Install ${pkg}`}</Button>
      {open && <HelperInstallDialog hostIds={[hostId]} packages={[pkg]} onClose={() => setOpen(false)} />}
    </>
  );
}
