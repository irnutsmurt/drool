import { useState } from "react";
import { useNavigate } from "react-router";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api, unwrap } from "@/api/client";
import { qk, type Host, type ProbeResult } from "@/api/queries";
import { HostForm, type HostRequest } from "@/components/hosts/HostForm";
import { HostKeyPanel } from "@/components/hosts/HostKeyPanel";
import { ProbeSteps } from "@/components/hosts/ProbeSteps";
import { Alert } from "@/components/ui/Alert";
import { Button } from "@/components/ui/Button";
import { Card } from "@/components/ui/Card";
import { PageHeader } from "@/components/ui/PageHeader";
import { cx } from "@/components/ui/cx";
import { errorMessage } from "@/lib/format";

const steps = ["Connection", "Host key", "Test"] as const;

/** Add a host: connection details → trust its host key → connection test. */
export function AddHostPage() {
  const qc = useQueryClient();
  const navigate = useNavigate();
  const [step, setStep] = useState(0);
  const [host, setHost] = useState<Host>();
  const [probe, setProbe] = useState<ProbeResult>();

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
      <PageHeader title="Add host" />
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
            <HostForm submitLabel="Continue" busy={create.isPending} onSubmit={(b) => create.mutate(b)}
              onCancel={() => navigate("/hosts")} />
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
                  ? <Alert tone="ok">{host.name} is ready.</Alert>
                  : <Alert tone="warn">Some checks failed. Fix the issue on the host or adjust the settings, then test again.</Alert>}
                <ProbeSteps result={probe} />
              </>
            )}
            <div className="flex gap-2">
              <Button variant="primary" onClick={() => navigate(`/hosts/${host.id}`)}>
                {probe?.ok ? "Done" : "Go to host"}
              </Button>
              <Button onClick={() => test.mutate(host.id)} disabled={test.isPending}>Test again</Button>
            </div>
          </div>
        )}
      </Card>
    </>
  );
}
