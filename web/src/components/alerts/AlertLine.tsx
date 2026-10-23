import { useState } from "react";
import { Link } from "react-router";
import { ServiceActionsDialog, useServiceFix } from "@/components/monitoring/ServiceActions";
import { fmtSince } from "@/components/monitoring/format";
import { cx } from "@/components/ui/cx";
import { plainLink, touchLink } from "@/components/ui/links";
import type { Alert } from "./queries";

/** An open alert in a "Needs attention" list: its title, opening the alert,
 * since when, and for a service the fix next to it ("Restart nginx…", reviewed and
 * followed in a dialog), so the most common response is one step away. */
export function AlertLine({ a, withHost }: { a: Alert; withHost?: boolean }) {
  const fix = useServiceFix(a.host_id, a.category === "services" ? a.subject : undefined, a.check);
  const [fixing, setFixing] = useState(false);
  return (
    <>
      <Link className={plainLink} to={`/alerts/${a.id}`}>{withHost ? `${a.host_name}: ${a.title}` : a.title}</Link>
      <span className="text-xs text-muted"> · {fmtSince(a.observed_at ?? a.issue_started_at)}</span>
      {fix && <> · <button type="button" className={cx(plainLink, touchLink)} onClick={() => setFixing(true)}
        aria-label={withHost ? `${fix.label.replace(/…$/, "")} on ${a.host_name}` : undefined}>{fix.label}</button></>}
      {fixing && fix && <ServiceActionsDialog hostId={a.host_id} hostName={a.host_name} unit={fix.unit} initial={fix.action} onClose={() => setFixing(false)} />}
    </>
  );
}
