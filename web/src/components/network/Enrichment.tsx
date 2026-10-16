import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api, unwrap } from "@/api/client";
import { Alert } from "@/components/ui/Alert";
import { Badge } from "@/components/ui/Badge";
import { Button } from "@/components/ui/Button";
import { Card } from "@/components/ui/Card";
import { errorMessage, formatBytes, formatDateTime } from "@/lib/format";
import { nk, type NetEnrichState, type NetEnrichment } from "./queries";

type Tone = "ok" | "warn" | "danger" | "neutral" | "accent";

/** Enrichment as one state, in words (UI/UX batch §12.4). */
const states: Record<NetEnrichment["state"], { label: string; tone: Tone }> = {
  available: { label: "Available", tone: "ok" },
  updating: { label: "Checking for an update…", tone: "accent" },
  update_failed: { label: "Update failed — the databases in use are kept", tone: "warn" },
  preparing: { label: "Preparing the databases…", tone: "accent" },
  unavailable: { label: "Temporarily unavailable", tone: "warn" },
  off: { label: "Lookups off", tone: "neutral" },
  not_configured: { label: "Not set up", tone: "neutral" },
};

const kindName = { country: "Country", asn: "Network owner (ASN)" } as const;

/** The attribution DB-IP's licence (CC BY 4.0) asks for, shown with the
 * data wherever downloaded databases answered. */
export function GeoAttribution({ state, className }: { state?: Pick<NetEnrichState, "attribution" | "attribution_url">; className?: string }) {
  if (!state?.attribution) return null;
  return (
    <span className={className ?? "text-[11px] text-muted"} data-testid="geo-attribution">
      <a className="underline underline-offset-2 hover:text-fg" href={state.attribution_url} target="_blank" rel="noopener noreferrer">{state.attribution}</a>
    </span>
  );
}

/** Where each kind of data comes from: the operator's own file, or the
 * database DROOL downloaded (with its release). */
function Sources({ st }: { st: NetEnrichment }) {
  const dl = st.downloads;
  return (
    <dl className="grid grid-cols-[minmax(7rem,10rem)_1fr] gap-x-3 gap-y-1.5" data-testid="enrichment-sources">
      {(["country", "asn"] as const).map((k) => {
        const f = st.files.find((x) => x.used.includes(k));
        const d = dl?.databases.find((x) => x.kind === k);
        return (
          <div key={k} className="contents">
            <dt className="text-muted">{kindName[k]}</dt>
            <dd>
              {!f ? <span className="text-muted">Not available</span>
                : f.downloaded && d ? <>{dl!.provider}, release {d.release}{d.build_date && <span className="text-muted"> (built {d.build_date})</span>}</>
                  : <>Your database <span className="font-mono text-xs">{f.name}</span>{f.build_date && <span className="text-muted"> (built {f.build_date})</span>}</>}
            </dd>
          </div>
        );
      })}
    </dl>
  );
}

/** Country and network owner: what is available, where it comes from,
 * automatic downloads (DB-IP Lite) and Check now. */
export function EnrichmentCard({ st, canEdit }: { st: NetEnrichment; canEdit: boolean }) {
  const qc = useQueryClient();
  const reload = useMutation({
    mutationFn: () => unwrap(api.POST("/network/enrichment/reload")),
    onSuccess: () => void qc.invalidateQueries({ queryKey: nk.all }),
  });
  const check = useMutation({
    mutationFn: () => unwrap(api.POST("/network/enrichment/check")),
    onSuccess: () => void qc.invalidateQueries({ queryKey: nk.all }),
  });
  const dl = st.downloads;
  const s = states[st.state];
  const yours = st.files.filter((f) => !f.downloaded);
  return (
    <Card title="Country and network owner (GeoIP/ASN)"
      actions={canEdit && (
        <span className="flex flex-wrap gap-2">
          {dl?.enabled && <Button size="sm" onClick={() => check.mutate()} disabled={check.isPending || dl.checking}>{dl.checking ? "Checking…" : "Check now"}</Button>}
          <Button size="sm" variant="ghost" onClick={() => reload.mutate()} disabled={reload.isPending}>Reload files</Button>
        </span>
      )}>
      <div className="space-y-3 text-sm" data-testid="enrichment-status" data-state={st.state}>
        <p className="flex flex-wrap items-center gap-2"><Badge tone={s.tone}>{s.label}</Badge>
          {st.state === "preparing" && <span className="text-muted">DROOL is downloading them for the first time; it takes a minute or so.</span>}
          {st.state === "unavailable" && dl?.last_error && <span className="text-muted">The download failed; DROOL tries again{dl.next_check ? ` at ${formatDateTime(dl.next_check)}` : " soon"}.</span>}
        </p>
        {(st.state === "unavailable" || st.state === "update_failed") && dl?.last_error && (
          <p className="rounded-ui border border-warn/40 bg-warn-bg px-3 py-2 text-xs text-warn" role="note">{dl.last_error}</p>
        )}
        {check.error && <Alert>{errorMessage(check.error)}</Alert>}
        {reload.error && <Alert>{errorMessage(reload.error)}</Alert>}
        <Sources st={st} />
        {dl && (
          <div className="space-y-1 rounded-ui border border-line p-3 text-xs" data-testid="enrichment-downloads">
            <p>
              <span className="font-medium">Automatic downloads:</span>{" "}
              {dl.enabled ? <>on — {dl.provider} ({dl.license}), checked daily for a new monthly release.</> : <>off — {dl.disabled}.</>}
            </p>
            {dl.yours.length > 0 && <p className="text-muted">Your own {dl.yours.map((k) => (k === "asn" ? "ASN" : "country")).join(" and ")} database is used: DROOL doesn&apos;t download that kind.</p>}
            <p className="text-muted">
              {dl.last_success ? <>Last successful check {formatDateTime(dl.last_success)}</> : <>Not checked yet</>}
              {dl.enabled && dl.next_check && !dl.checking && <> · next {formatDateTime(dl.next_check)}</>}
              {dl.last_error && st.state !== "unavailable" && st.state !== "update_failed" && <> · last error: {dl.last_error}</>}
            </p>
            {(dl.enabled || dl.databases.length > 0) && (
              <p className="text-muted">
                {dl.enabled && <>Only the database files are downloaded. </>}
                <a className="underline underline-offset-2 hover:text-fg" href={dl.provider_url} target="_blank" rel="noopener noreferrer">{dl.attribution}</a>
              </p>
            )}
          </div>
        )}
        <p className="text-xs text-muted">
          Addresses are looked up on this controller: DROOL never sends addresses to an online service, and never looks up private, loopback,
          link-local, multicast or documentation addresses.
        </p>
        {st.state === "not_configured" && (
          <div className="rounded-ui border border-line bg-surface-2 p-3 text-xs">
            <p className="font-medium">No databases. Monitoring works without them: connections show no country or network owner (“—”), and Network Activity says why.</p>
            <p className="mt-1">
              {dl ? <>Automatic downloads are off ({dl.disabled}).</> : <>This controller doesn&apos;t download them.</>} You can put country and ASN
              databases in the directory below, for example:
            </p>
            <ul className="mt-1 list-disc space-y-0.5 pl-5">
              <li>DB-IP Lite country and ASN (monthly downloads, CC BY 4.0)</li>
              <li>MaxMind GeoLite2 Country and ASN (free account; keep them current with <code>geoipupdate</code>)</li>
              <li>IPinfo Lite (country and ASN in one file)</li>
            </ul>
            {st.error && <p className="mt-1 text-muted">({st.error})</p>}
          </div>
        )}
        <details className="text-xs" open={yours.length > 0 || st.files.some((f) => f.error)}>
          <summary className="cursor-pointer text-muted">Database files ({st.files.length})</summary>
          <div className="mt-2 space-y-2">
            <p className="text-muted">
              Your databases: <code className="break-all">{st.directory}</code> (MaxMind-format <code>.mmdb</code> files; DROOL never changes them, and they win
              for the kinds they provide).
              {st.download_directory && <> Downloaded by DROOL: <code className="break-all">{st.download_directory}</code>.</>} Files are reloaded when they change.
            </p>
            {st.files.length > 0 && (
              <ul className="space-y-1">
                {st.files.map((f) => (
                  <li key={(f.downloaded ? "dl/" : "") + f.name} className="rounded-ui border border-line p-2">
                    <span className="font-mono">{f.name}</span>{f.downloaded && <span className="text-muted"> (downloaded)</span>} · {formatBytes(f.size)} · {f.database_type || "unknown type"}{f.build_date && <> · built {f.build_date}</>}
                    <div className="text-muted">provides: {f.provides.join(", ") || "nothing recognised"}{f.used.length > 0 ? <> · used for {f.used.join(", ")}</> : <> · not used</>}</div>
                    {f.error && <div className="text-danger">✕ {f.error}</div>}
                  </li>
                ))}
              </ul>
            )}
          </div>
        </details>
        <p className="text-xs text-muted">
          {st.loaded_at && <>Loaded {formatDateTime(st.loaded_at)} · </>}cache {st.cache.entries.toLocaleString()} / {st.cache.max.toLocaleString()} addresses
          ({st.cache.hits.toLocaleString()} hits, {st.cache.misses.toLocaleString()} lookups).
        </p>
        <p className="text-xs text-muted">GeoIP and ASN data are context, not a security verdict: they can be wrong or out of date, and DROOL never blocks or scores traffic by country or network.</p>
      </div>
    </Card>
  );
}
