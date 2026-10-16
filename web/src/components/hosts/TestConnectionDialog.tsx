import { useEffect, useRef, useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api, unwrap } from "@/api/client";
import { qk, type Host, type ProbeResult } from "@/api/queries";
import { Alert } from "@/components/ui/Alert";
import { Button } from "@/components/ui/Button";
import { Dialog } from "@/components/ui/Dialog";
import { errorMessage } from "@/lib/format";
import { ProbeSteps } from "./ProbeSteps";

/** "Test connection": the SSH sign-in and sudo checks, step by step. */
export function TestConnectionDialog({ host, onClose }: { host: Host; onClose: () => void }) {
  const qc = useQueryClient();
  const [result, setResult] = useState<ProbeResult>();
  const test = useMutation({
    mutationFn: () => unwrap(api.POST("/hosts/{id}/probe", { params: { path: { id: host.id } } })),
    onSuccess: (r) => {
      setResult(r);
      void qc.invalidateQueries({ queryKey: qk.host(host.id) });
      void qc.invalidateQueries({ queryKey: qk.hosts });
      void qc.invalidateQueries({ queryKey: qk.hostKeys(host.id) });
    },
  });
  // The dialog opens because the user asked for the test: run it once
  // (StrictMode mounts effects twice in development).
  const started = useRef(false);
  useEffect(() => {
    if (started.current) return;
    started.current = true;
    test.mutate();
  }, [test]);
  return (
    <Dialog title={`Test connection: ${host.name}`} onClose={onClose}>
      <div className="space-y-4 text-sm" data-testid="connection-test">
        {test.isPending && <p className="text-muted" role="status">Connecting over SSH and checking sudo…</p>}
        {test.error && <Alert>{errorMessage(test.error)}</Alert>}
        {result && <ProbeSteps result={result} />}
        <div className="flex justify-end gap-2">
          {!test.isPending && <Button onClick={() => test.mutate()}>Test again</Button>}
          <Button variant="primary" onClick={onClose}>Close</Button>
        </div>
      </div>
    </Dialog>
  );
}
