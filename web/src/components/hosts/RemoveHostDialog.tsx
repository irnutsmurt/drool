import { useState } from "react";
import { Link } from "react-router";
import { useMutation, useQuery } from "@tanstack/react-query";
import { api, unwrap } from "@/api/client";
import { Alert } from "@/components/ui/Alert";
import { Button } from "@/components/ui/Button";
import { Dialog } from "@/components/ui/Dialog";
import { plainLink } from "@/components/ui/links";
import { errorMessage } from "@/lib/format";

/** Removing a host (dogfooding correction batch): from DROOL only (nothing
 * on the host changes), or, when DROOL installed something it can remove
 * (its Shepherd, the SSH account it created), cleaning that up first. The
 * plan is checked on the host before anything changes; a cleanup that
 * can't start changes nothing, and one that stops halfway keeps the host
 * and says what was done. */
export function RemoveHostDialog({ hostId, hostName, onRemoved, onClose }: { hostId: string; hostName: string; onRemoved: () => void; onClose: () => void }) {
  // Read once when the dialog opens, and kept: not under ["hosts"], so
  // what the removal refreshes never asks again about a host that's gone.
  const plan = useQuery({
    queryKey: ["host-removal-plan", hostId],
    queryFn: () => unwrap(api.POST("/hosts/{id}/removal/plan", { params: { path: { id: hostId } } })),
    gcTime: 0,
    staleTime: Infinity,
    refetchOnWindowFocus: false,
    retry: false,
  });
  const p = plan.data;
  const [choice, setChoice] = useState<"cleanup" | "forget" | null>(null);
  const picked = choice ?? (p?.can_clean_up ? "cleanup" : "forget");
  const cleanup = useMutation({
    mutationFn: () => unwrap(api.POST("/hosts/{id}/removal", { params: { path: { id: hostId } } })),
  });
  const forget = useMutation({
    mutationFn: () => unwrap(api.DELETE("/hosts/{id}", { params: { path: { id: hostId } } })),
    onSuccess: onRemoved,
  });
  const res = cleanup.data;
  const busy = cleanup.isPending || forget.isPending;
  const hasParts = (p?.parts.length ?? 0) > 0;
  const stepWord: Record<string, string> = { shepherd: "Shepherd", account: "SSH account" };
  const changeWord: Record<string, string> = { schedule: "Schedule", firewall_policy: "Firewall policy" };
  const footer = (
    <>
      {res?.host_deleted ? <Button variant="primary" onClick={onRemoved} data-autofocus>Done</Button> : <Button onClick={onClose}>{res ? "Close" : "Cancel"}</Button>}
      {p && !p.blocked && !res && (
        <Button variant="danger" disabled={busy || (picked === "cleanup" && !p.can_clean_up)}
          onClick={() => (picked === "cleanup" ? cleanup.mutate() : forget.mutate())}>
          {busy ? "Removing…" : picked === "cleanup" ? "Clean up and remove" : "Remove from DROOL"}
        </Button>
      )}
      {res && !res.host_deleted && (
        <Button variant="danger" disabled={busy} onClick={() => forget.mutate()}>Remove from DROOL only</Button>
      )}
    </>
  );
  return (
    <Dialog title={`Remove ${hostName} from DROOL?`} onClose={res?.host_deleted ? onRemoved : onClose} footer={footer}>
      <div className="space-y-4 text-sm" data-testid="remove-host">
        {plan.isPending && <p className="text-muted">Checking what DROOL installed on {hostName}…</p>}
        {plan.error && <Alert>{errorMessage(plan.error)}</Alert>}
        {p?.blocked && <Alert tone="warn">{p.blocked}</Alert>}
        {p && !p.blocked && !res && (
          <>
            <fieldset className="space-y-2">
              <legend className="sr-only">How to remove it</legend>
              {hasParts && (
                <label className={`flex items-start gap-2 rounded-ui border p-3 ${picked === "cleanup" ? "border-accent bg-accent/5" : "border-line"} ${p.can_clean_up ? "" : "opacity-60"}`}>
                  <input type="radio" name="removal" className="mt-1 accent-[var(--ui-accent)]" checked={picked === "cleanup"} disabled={!p.can_clean_up}
                    onChange={() => setChoice("cleanup")} />
                  <span className="space-y-1">
                    <span className="block font-medium">Clean up DROOL access and remove the host{p.can_clean_up ? " (recommended)" : ""}</span>
                    {p.parts.map((part) => (
                      <span key={part.what} className="block">
                        <span className={part.ready ? "" : "text-warn"}>{part.ready ? "✓" : "✕"} {part.label}</span>
                        {part.ready
                          ? <span className="block text-xs text-muted">Removes {part.removes.join("; ")}.</span>
                          : <span className="block text-xs text-warn">{part.reason}</span>}
                      </span>
                    ))}
                    <span className="block text-xs text-muted">{p.after}</span>
                  </span>
                </label>
              )}
              <label className={`flex items-start gap-2 rounded-ui border p-3 ${picked === "forget" ? "border-accent bg-accent/5" : "border-line"}`}>
                <input type="radio" name="removal" className="mt-1 accent-[var(--ui-accent)]" checked={picked === "forget"} onChange={() => setChoice("forget")} />
                <span>
                  <span className="block font-medium">Remove from DROOL only</span>
                  <span className="block text-xs text-muted">
                    Nothing on {hostName} changes{hasParts ? `: ${p.parts.map((x) => x.label.replace(/ removes itself|, removed over SSH/, "")).join(" and ")} stay${p.parts.length === 1 ? "s" : ""} there` : ""}.
                    A password or key typed only to set the host up goes too, unless another host uses it.
                  </span>
                </span>
              </label>
            </fieldset>
            <p className="text-xs text-muted">Either way, {p.stays.join("; ")} stay on the host.</p>
            {p.changes.length > 0 && (
              <div className="space-y-1 text-xs" data-testid="remove-host-changes">
                <p className="font-medium">Either way, in DROOL:</p>
                <ul className="space-y-1">
                  {p.changes.map((c) => (
                    <li key={c.kind + c.id}>
                      <Link className={plainLink} to={c.path}>{changeWord[c.kind] ?? c.kind} “{c.name}”</Link>: {c.detail}
                    </li>
                  ))}
                </ul>
              </div>
            )}
          </>
        )}
        {cleanup.error && <Alert>{errorMessage(cleanup.error)}</Alert>}
        {forget.error && <Alert>{errorMessage(forget.error)}</Alert>}
        {res && (
          <div className="space-y-2" data-testid="remove-host-result">
            <Alert tone={res.host_deleted ? "ok" : "warn"}>{res.host_deleted ? `${hostName} is removed from DROOL.` : `${hostName} stays in DROOL.`} {res.host_deleted ? "" : res.message}</Alert>
            <ul className="space-y-1">
              {res.steps.map((st) => (
                <li key={st.what}><strong>{stepWord[st.what] ?? st.what}: {st.outcome}.</strong> {st.message}</li>
              ))}
            </ul>
          </div>
        )}
      </div>
    </Dialog>
  );
}
