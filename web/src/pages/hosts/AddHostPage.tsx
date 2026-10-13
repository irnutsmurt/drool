import { useCallback, useState } from "react";
import { useNavigate } from "react-router";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api, unwrap } from "@/api/client";
import { qk, type Host, type ProbeResult } from "@/api/queries";
import { DedicatedAccountSetup } from "@/components/hosts/DedicatedAccount";
import { HostForm, type AccountChoice, type HostRequest } from "@/components/hosts/HostForm";
import { HostKeyPanel } from "@/components/hosts/HostKeyPanel";
import { ProbeSteps } from "@/components/hosts/ProbeSteps";
import { Alert } from "@/components/ui/Alert";
import { Button } from "@/components/ui/Button";
import { Card } from "@/components/ui/Card";
import { PageHeader } from "@/components/ui/PageHeader";
import { cx } from "@/components/ui/cx";
import { errorMessage } from "@/lib/format";


/** True when the only failed check is the init system (usable without systemd). */
function onlyNoSystemd(p: ProbeResult): boolean {
  const failed = p.steps.filter((s) => s.status === "failed");
  return failed.length > 0 && failed.every((s) => s.name === "init system");
}

/** Add a host: connection details → trust its host key → connection test,
 * then, for DROOL's own account, the setup that switches to it. */
export function AddHostPage() {
  const qc = useQueryClient();
  const navigate = useNavigate();
  const [step, setStep] = useState(0);
  const [host, setHost] = useState<Host>();
  const [probe, setProbe] = useState<ProbeResult>();
  const [account, setAccount] = useState<AccountChoice>({ dedicated: true, user: "drool" });
  const steps = account.dedicated ? ["Connection", "Host key", "Test", "DROOL account"] : ["Connection", "Host key", "Test"];
  const usable = !!probe && (probe.ok || onlyNoSystemd(probe));
  const choose = useCallback((dedicated: boolean) => setAccount((a) => (a.dedicated === dedicated ? a : dedicated ? { dedicated, user: "drool" } : { dedicated })), []);

  const create = useMutation({
    mutationFn: (body: HostRequest) => unwrap(api.POST("/hosts", { body })),
    onSuccess: async (h) => {
      setHost(h);
      void qc.invalidateQueries({ queryKey: qk.hosts });
      setStep(1);
      // Scan right away so the keys are ready to review.
      await unwrap(api.POST("/hosts/{id}/hostkeys/scan", { params: { path: { id: h.id } } })).catch(() => undefined);
      void qc.invalidateQueries({ queryKey: qk.hostKeys(h.id) });
    },
  });
  const test = useMutation({
    mutationFn: (id: string) => unwrap(api.POST("/hosts/{id}/probe", { params: { path: { id } } })),
    onSuccess: (r) => {
      setProbe(r);
      void qc.invalidateQueries({ queryKey: qk.hosts });
    },
  });

  return (
    <>
      <PageHeader crumbs={[{ label: "Hosts", to: "/hosts" }]} title="Add host"
        description="Connect a Linux machine over SSH. To manage it through a Shepherd (DROOL agent) instead, install one afterwards from the host's page, or add the host from Shepherds → Add a host with a Shepherd." />
      <ol className="mb-6 flex gap-6 text-sm">
        {steps.map((s, i) => (
          <li key={s} className={cx(i === step ? "font-semibold text-fg" : i < step ? "text-ok" : "text-muted")}>
            {i + 1}. {s}
          </li>
        ))}
      </ol>
      <Card className="max-w-3xl">
        {step === 0 && (
          <div className="space-y-4">
            {create.error && <Alert>{errorMessage(create.error)}</Alert>}
            <HostForm submitLabel="Continue" busy={create.isPending} onSubmit={(b, a) => { setAccount(a); create.mutate(b); }}
              onChoice={choose} onCancel={() => navigate("/hosts")} />
          </div>
        )}
        {step === 1 && host && (
          <div className="space-y-4">
            <p className="text-sm text-muted">
              DROOL only connects to <strong>{host.name}</strong> after you trust its SSH host key. This protects
              against connecting to an impostor.
            </p>
            <HostKeyPanel hostId={host.id} onApproved={() => {
              setStep(2);
              test.mutate(host.id);
            }} />
          </div>
        )}
        {step === 2 && host && (
          <div className="space-y-4">
            {test.isPending && <p className="text-sm text-muted">Testing the connection…</p>}
            {test.error && <Alert>{errorMessage(test.error)}</Alert>}
            {probe && (
              <>
                {probe.ok
                  ? <Alert tone="ok">{account.dedicated ? `DROOL can sign in to ${host.name} as ${host.user} and become root: ready to set up its own account.` : `${host.name} is ready.`}</Alert>
                  : onlyNoSystemd(probe)
                    ? <Alert tone="warn">Connected. {host.name} has no systemd, so service management and reboots won't work; commands, scripts and package operations will.</Alert>
                    : <Alert tone="warn">
                        Some checks failed. Fix the issue on the host or adjust the settings, then test again.
                        {account.dedicated && ` DROOL needs to sign in as ${host.user} and become root to set up its own account.`}
                      </Alert>}
                <ProbeSteps result={probe} />
              </>
            )}
            <div className="flex gap-2">
              {account.dedicated && usable
                ? <Button variant="primary" onClick={() => setStep(3)}>Continue</Button>
                : <Button variant="primary" onClick={() => navigate(`/hosts/${host.id}`)}>{usable ? "Done" : "Go to host"}</Button>}
              <Button onClick={() => test.mutate(host.id)} disabled={test.isPending}>Test again</Button>
            </div>
          </div>
        )}
        {step === 3 && host && account.dedicated && (
          <DedicatedAccountSetup host={host} initialUser={account.user} cancelLabel="Not now"
            onFinished={() => navigate(`/hosts/${host.id}`)} onCancel={() => navigate(`/hosts/${host.id}`)} />
        )}
      </Card>
    </>
  );
}
