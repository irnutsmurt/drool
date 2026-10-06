import { useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api, unwrap } from "@/api/client";
import { qk, useHostKeys, type HostKey } from "@/api/queries";
import { useAuth } from "@/auth/AuthContext";
import { Alert } from "@/components/ui/Alert";
import { Button } from "@/components/ui/Button";
import { Field, Input } from "@/components/ui/Field";
import { errorMessage } from "@/lib/format";
import { HostKeyList } from "./HostKeyList";

/**
 * Scan a host's keys and approve one. Used by the add-host flow and the host
 * detail page (including after a key change).
 */
export function HostKeyPanel({ hostId, onApproved }: { hostId: string; onApproved?: () => void }) {
  const qc = useQueryClient();
  const { can } = useAuth();
  const keys = useHostKeys(hostId);
  const [expected, setExpected] = useState("");
  const invalidate = () => {
    void qc.invalidateQueries({ queryKey: qk.hostKeys(hostId) });
    void qc.invalidateQueries({ queryKey: qk.host(hostId) });
    void qc.invalidateQueries({ queryKey: qk.hosts });
  };
  const scan = useMutation({
    mutationFn: () => unwrap(api.POST("/hosts/{id}/hostkeys/scan", { params: { path: { id: hostId } } })),
    onSuccess: invalidate,
  });
  const approve = useMutation({
    mutationFn: (k: HostKey) =>
      unwrap(api.POST("/hosts/{id}/hostkeys/approve", { params: { path: { id: hostId } }, body: { key_id: k.id } })),
    onSuccess: () => {
      invalidate();
      onApproved?.();
    },
  });

  const all = keys.data?.items ?? [];
  const offered = all.filter((k) => k.status === "offered");
  const pinned = all.find((k) => k.status === "pinned");
  const err = scan.error ?? approve.error;

  return (
    <div className="space-y-4">
      {pinned && offered.length > 0 && (
        <Alert tone="danger">
          The host presented a different key than the trusted one. This happens after a reinstall or key rotation,
          but can also indicate an attack. Only trust the new key if you know why it changed.
        </Alert>
      )}
      {err && <Alert>{errorMessage(err)}</Alert>}
      <HostKeyList keys={all} expected={expected} busy={approve.isPending}
        onApprove={can("hostkeys.approve") ? (k) => approve.mutate(k) : undefined} />
      <div className="flex flex-wrap items-end gap-3">
        <Button onClick={() => scan.mutate()} disabled={scan.isPending}>
          {scan.isPending ? "Scanning…" : all.length ? "Scan again" : "Scan host keys"}
        </Button>
      </div>
      {offered.length > 0 && (
        <details className="text-sm" open={!pinned}>
          <summary className="cursor-pointer text-muted">How do I verify a fingerprint?</summary>
          <div className="mt-2 space-y-2 text-muted">
            <p>On the host itself (or its console), run:</p>
            <code className="block rounded-ui bg-surface-2 p-2 font-mono text-xs">
              for f in /etc/ssh/ssh_host_*_key.pub; do ssh-keygen -lf "$f"; done
            </code>
            <Field label="Paste the expected fingerprint to highlight the matching key">
              <Input value={expected} placeholder="SHA256:…" onChange={(e) => setExpected(e.target.value)} />
            </Field>
          </div>
        </details>
      )}
    </div>
  );
}
