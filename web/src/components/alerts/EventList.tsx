import { Link } from "react-router";
import { StatusIcon } from "@/components/monitoring/Status";
import { fmtSince } from "@/components/monitoring/format";
import { useEvents } from "./queries";

const eventStatus: Record<string, "critical" | "warning" | "info"> = { critical: "critical", warning: "warning", info: "info" };

/** Where an event's object lives in the UI. */
export function eventLink(e: { object_type?: string; object_id?: string }): string | undefined {
  switch (e.object_type) {
    case "alert":
      return e.object_id ? `/alerts/${e.object_id}` : undefined;
    case "job":
      return e.object_id ? `/jobs/${e.object_id}` : undefined;
    case "schedule":
      return e.object_id ? `/jobs/schedules/${e.object_id}` : "/jobs/schedules";
    case "maintenance":
      return "/alerts?view=maintenance";
    case "baseline":
      return e.object_id ? `/desired/baselines/${e.object_id}` : "/desired/baselines";
    case "desired_host":
      return e.object_id ? `/hosts/${e.object_id}/desired` : "/desired";
    case "desired":
      return "/desired";
  }
  return undefined;
}

/** The controller's event timeline, newest first (alerts opened,
 * recovered, acknowledged; maintenance; failing notifications), for the
 * fleet or one host. */
export function EventList({ hostId, limit }: { hostId?: string; limit: number }) {
  const ev = useEvents(hostId, limit);
  const rows = ev.data?.events ?? [];
  if (!ev.data) return <p className="text-sm text-muted">{ev.isError ? "Couldn't load events." : "Loading…"}</p>;
  if (rows.length === 0) return <p className="text-sm text-muted">{hostId ? "No events for this host yet." : "No events yet."}</p>;
  return (
    <ul className="space-y-2 text-sm">
      {rows.map((e) => (
        <li key={e.id} className="flex items-start gap-2">
          <StatusIcon status={e.kind === "alert.recovered" || e.kind === "notifications.recovered" ? "ok" : e.kind === "alert.cleared" ? "info" : eventStatus[e.severity] ?? "info"} />
          {eventLink(e) ? <Link className="min-w-0 text-accent" to={eventLink(e)!}>{e.title}</Link> : <span className="min-w-0">{e.title}</span>}
          <span className="ml-auto shrink-0 text-xs text-muted">{fmtSince(e.ts)}</span>
        </li>
      ))}
    </ul>
  );
}
