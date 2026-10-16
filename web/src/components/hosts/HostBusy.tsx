import { Link } from "react-router";
import { useQuery } from "@tanstack/react-query";
import { api, unwrap, type Schemas } from "@/api/client";
import { plainLink } from "@/components/ui/links";

export type HostBusy = Schemas["HostBusy"];

/** What is using a host right now, checked every few seconds while asked
 * (an action that needs the host to itself waits for it). Advisory: the
 * action still takes the host's lock when it starts. */
export function useHostBusy(hostId: string, enabled = true, every = 3000) {
  return useQuery({
    queryKey: ["hosts", hostId, "busy"],
    queryFn: () => unwrap(api.GET("/hosts/{id}/busy", { params: { path: { id: hostId } } })),
    enabled,
    refetchInterval: enabled ? every : false,
    retry: false,
  });
}

/** "In use: the run “…”": what is in the way of an action that needs the
 * host to itself, and that it becomes possible once that finishes. */
export function HostBusyNotice({ busy, action }: { busy?: HostBusy; action: string }) {
  if (!busy?.busy) return null;
  return (
    <p className="rounded-ui border border-warn/40 bg-warn-bg px-3 py-2 text-sm text-warn" role="status" data-testid="host-busy">
      <strong>The host is in use: </strong>
      {busy.holders.map((h, i) => (
        <span key={i}>{i > 0 && "; "}{h.job_id ? <Link className={plainLink} to={`/jobs/${h.job_id}`}>{h.label}</Link> : h.label}</span>
      ))}
      . {action} becomes possible as soon as it has finished: this updates by itself.
    </p>
  );
}
