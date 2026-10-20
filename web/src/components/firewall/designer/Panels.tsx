import { useEffect, useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { api, unwrap, type Schemas } from "@/api/client";
import { Badge } from "@/components/ui/Badge";
import { Button } from "@/components/ui/Button";
import { Alert } from "@/components/ui/Alert";
import { Input, Select } from "@/components/ui/Field";
import { errorMessage } from "@/lib/format";
import { Link } from "react-router";
import { FlowInspector } from "@/components/network/FlowInspector";
import { PolicyBadge, when } from "@/components/network/labels";
import { useNetFlow } from "@/components/network/queries";
import { affectedHosts, entriesLabel, kindLabel, obsStatusLabel, parseRef, refLabel, serviceLabel, type Doc, type GEdge, type MgmtPath, type ServiceInfo } from "./model";
import type { CheckResult } from "./useDesigner";
import type { Selection } from "./Topology";
import { hopVerdict, modeInfo } from "../parts";
import { useFwHosts } from "../queries";
import { plainLink } from "@/components/ui/links";

const hopText = (h: MgmtPath["hops"][number]) => `${h.from_name} → ${h.to_name} : ${h.protocol.toUpperCase()} ${h.port || "?"}`;

/** "Management path blocked" warnings, worded for people who don't know
 * nftables, with the required path and the blocked segment. */
export function MgmtAlerts({ management, onShow }: { management: MgmtPath[]; onShow: (p: MgmtPath) => void }) {
  const blocked = management.filter((p) => p.blocked);
  const atRisk = management.filter((p) => !p.blocked && p.at_risk);
  const unverified = management.filter((p) => !p.blocked && !p.at_risk && p.unverified && p.mode === "enforce");
  // At-risk hosts, grouped by the rules that put them at risk.
  const risky = new Map<string, string[]>();
  for (const p of atRisk) {
    const rules = [...new Set(p.hops.flatMap((h) => (h.at_risk ? h.blocked_by : [])))].join(", ");
    risky.set(rules, [...(risky.get(rules) ?? []), p.host]);
  }
  if (blocked.length === 0 && unverified.length === 0 && atRisk.length === 0) return null;
  return (
    <div className="space-y-2" data-testid="mgmt-alerts">
      {blocked.map((p) => {
        const seg = p.hops.find((h) => h.status === "blocked");
        return (
          <div key={p.host_id + p.kind} role="alert" className="rounded-ui border-2 border-danger bg-danger-bg px-3 py-2 text-sm text-fg">
            <div className="font-semibold text-danger">{p.kind === "agent" ? "Agent management path blocked" : "Management path blocked"}</div>
            {p.kind === "agent" ? (
              <p>This policy would prevent <strong>{p.host}</strong> from reaching the DROOL controller on TCP {seg?.port}.</p>
            ) : (
              <>
                <p>This change would prevent DROOL from managing <strong>{p.host}</strong>.</p>
                <div className="mt-1 text-xs">
                  <div className="text-muted">Required path:</div>
                  <ol className="ml-4 font-mono">
                    <li>DROOL Controller</li>
                    {p.hops.map((h, i) => <li key={i} className={h.status === "blocked" ? "font-bold text-danger" : ""}>→ {h.to_name} : TCP {h.port}</li>)}
                  </ol>
                </div>
              </>
            )}
            {seg && <p className="mt-1 text-xs"><span className="text-muted">Blocked segment:</span> <span className="font-mono">{hopText(seg)}</span> by rule <strong>{seg.blocked_by.join(", ")}</strong></p>}
            <p className="mt-1 text-xs text-muted">Applying is refused while this is so. You can still save the draft; an administrator&apos;s audited emergency override is still protected by automatic rollback.</p>
            <Button size="sm" className="mt-1" onClick={() => onShow(p)}>Show on the topology</Button>
          </div>
        );
      })}
      {[...risky].map(([rules, hosts]) => (
        <div key={rules} role="alert" className="rounded-ui border-2 border-warn bg-warn-bg px-3 py-2 text-sm text-fg">
          <div className="font-semibold text-warn">Management path may be blocked</div>
          <p>Rule <strong>{rules}</strong> denies the port DROOL connects to on {hosts.length === 1 ? <strong>{hosts[0]}</strong> : <><strong>{hosts.length} hosts</strong> ({hosts.slice(0, 5).join(", ")}{hosts.length > 5 ? ", …" : ""})</>}.
            DROOL hasn&apos;t measured where its connection to {hosts.length === 1 ? "it" : "them"} comes from, so it can&apos;t tell yet whether this cuts it off.</p>
          <p className="mt-1 text-xs text-muted">Click <strong>Check</strong> on {hosts.length === 1 ? "the host" : "those hosts"} (Firewall → Hosts) to measure it. Applying measures it again first and refuses if the path would be cut.</p>
        </div>
      ))}
      {unverified.length > 0 && (
        <Alert tone="warn">
          DROOL hasn&apos;t measured the management path of {unverified.map((p) => p.host).join(", ")} yet, so this check can&apos;t confirm it.
          Click <strong>Check</strong> on those hosts; DROOL measures it again over a fresh connection before every apply anyway.
        </Alert>
      )}
    </div>
  );
}

/** Before saving: what the draft changes where, and what blocks it.
 * Applying comes after saving (AfterSave). */
export function PreviewPanel({ check }: { check: CheckResult | null }) {
  if (!check) return <p className="text-sm text-muted">Checking…</p>;
  return (
    <div className="space-y-3 text-sm" data-testid="preview">
      {check.problems.length > 0 && (
        <div role="alert" className="rounded-ui border border-danger bg-danger-bg p-2">
          <div className="font-medium text-danger">Validation problems ({check.problems.length})</div>
          <ul className="mt-1 list-disc pl-5 text-xs">
            {check.problems.map((p, i) => <li key={i}>{p.line ? `Line ${p.line}: ` : ""}{p.message}{p.path && !p.line ? ` (${p.path})` : ""}</li>)}
          </ul>
        </div>
      )}
      {check.blockers.length > 0 && (
        <div className="rounded-ui border border-danger p-2">
          <div className="font-medium text-danger">Transaction safety blockers</div>
          <ul className="mt-1 list-disc pl-5 text-xs">{check.blockers.map((b) => <li key={b}>{b}</li>)}</ul>
        </div>
      )}
      {check.notices.map((n) => <p key={n} className="text-xs text-muted">{n}</p>)}
      {check.valid && check.impact.length === 0 && <p className="text-muted">{check.changed ? "No host's firewall changes." : "No changes."}</p>}
      {groupImpacts(check.impact).map(({ im, names }) => (
        <div key={im.host_id} className="rounded-ui border border-line p-2">
          <div className="flex flex-wrap items-center gap-1.5">
            <strong>{names.length === 1 ? im.host_name : `${names.length} hosts`}</strong>
            <Badge tone={im.mode === "enforce" ? "accent" : "neutral"}>{im.mode}</Badge>
            {im.enforced && <Badge tone="ok">enforced</Badge>}
            {im.supported === false && <Badge tone="danger">refused</Badge>}
            {im.conflicts.length > 0 && <Badge tone="danger">management path</Badge>}
          </div>
          {names.length > 1 && <p className="text-xs text-muted" title={names.join(", ")}>{names.slice(0, 8).join(", ")}{names.length > 8 ? `, and ${names.length - 8} more` : ""} — the same changes on each</p>}
          {im.refused && (
            <details className="text-xs">
              <summary className="cursor-pointer text-danger">Why refused? DROOL can&apos;t enforce on this host</summary>
              <p className="mt-1 text-muted">{im.refused}</p>
            </details>
          )}
          {im.mode !== "enforce" && <p className="text-xs text-muted">Not enforced (mode {im.mode}): only Explain changes.</p>}
          <ul className="mt-1 text-xs">
            {im.added.map((a) => <li key={"+" + a} className="text-ok">+ {a}</li>)}
            {im.new_denies.map((a) => <li key={"d" + a} className="text-danger">+ {a}</li>)}
            {im.removed.map((a) => <li key={"-" + a} className="text-muted line-through">{a}</li>)}
            {im.conflicts.map((c) => <li key={"c" + c} className="font-medium text-danger">⚠ {c}</li>)}
          </ul>
        </div>
      ))}
      {check.changed && check.impact.length > 0 && <p className="text-xs text-muted">Saving changes nothing on these hosts: after saving, DROOL lists them to review and apply.</p>}
    </div>
  );
}

type HostSummary = Schemas["FwHostSummary"];

/** What the saved policy means for each host whose firewall the save
 * changed (the server's list, with each host's live status): saved here,
 * nothing applied there yet. Hosts DROOL can apply to now have Review and
 * apply…, which reviews each host (and says when it switches one to
 * Enforce) before anything changes; the rest say why not. */
export function AfterSave({ hosts, canApply, onReview }: { hosts: HostSummary[]; canApply: boolean; onReview: (hostIds: string[]) => void }) {
  const live = useFwHosts();
  const rows = hosts.map((h) => live.data?.items.find((x) => x.host_id === h.host_id) ?? h);
  const eligible = rows.filter(applicable).map((h) => h.host_id);
  return (
    <section className="space-y-2 rounded-ui border border-line bg-surface p-3 text-sm" aria-labelledby="after-save-title" data-testid="after-save">
      <h3 id="after-save-title" className="font-semibold"><span aria-hidden="true" className="text-ok">✓ </span>Saved. Nothing has changed on any host yet.</h3>
      {hosts.length === 0 ? <p className="text-muted">No host&apos;s firewall changes with this save.</p> : (
        <>
          <p className="text-muted">This changes the policy of {hosts.length === 1 ? "one host" : `${hosts.length} hosts`}. A host&apos;s firewall changes only when its policy is applied:</p>
          <ul className="divide-y divide-line">
            {rows.map((h) => (
              <li key={h.host_id} className="flex flex-wrap items-center justify-between gap-x-3 gap-y-1 py-1.5">
                <span className="min-w-0">
                  <Link className={plainLink} to={`/hosts/${h.host_id}/firewall`}>{h.host_name}</Link>{" "}
                  <span data-testid="after-save-state">{savedState(h)}</span>
                </span>
                {canApply && applicable(h) && eligible.length > 1 && <Button size="sm" onClick={() => onReview([h.host_id])}>Review and apply…</Button>}
              </li>
            ))}
          </ul>
          {canApply && eligible.length > 0 && (
            <Button variant="primary" onClick={() => onReview(eligible)}>
              {eligible.length === 1 ? `Review and apply to ${rows.find((h) => h.host_id === eligible[0])!.host_name}…` : `Review and apply to ${eligible.length} hosts…`}
            </Button>
          )}
          {!canApply && <p className="text-xs text-muted">Applying needs the firewall.write permission (administrators).</p>}
        </>
      )}
    </section>
  );
}

/** A host DROOL can review and apply to now: something to apply, and
 * nothing the server would refuse before its transaction. */
const applicable = (h: HostSummary) => (h.status === "not_enforced" || h.status === "changes_pending") && h.supported !== false && h.conflicts === 0
  && h.transport !== "none" && h.transport !== "blocked";

/** A host's state right after a save, in words: saved is never enforced. */
function savedState(h: HostSummary): string {
  const mode = modeInfo[h.mode]?.label ?? h.mode;
  const why = h.supported === false ? ` DROOL can't enforce on this host${h.reason ? `: ${h.reason}` : ""}.`
    : h.conflicts > 0 ? " ✕ Applying is refused: a rule would cut DROOL's management path."
      : h.transport === "none" || h.transport === "blocked" ? " DROOL has no management path to this host right now." : "";
  switch (h.status) {
    case "pending_transaction":
      return "◷ A firewall change is in progress there.";
    case "drift":
      return "⚠ DROOL's rules there were changed outside DROOL: review it on its firewall page.";
    case "in_sync":
      return `✓ In force: revision ${h.revision}.`;
    case "sets_pending":
      return `In force: revision ${h.revision}; its new addresses are sent to it automatically.`;
    case "changes_pending":
    case "conflicts":
      return `↻ Not applied yet: the host runs revision ${h.revision}.${why}`;
    default:
      return (h.mode === "enforce" ? "Policy saved — not yet enforced (Enforce is set; nothing applied yet)."
        : `Policy saved — not yet enforced (mode ${mode}: applying switches it to Enforce).`) + why;
  }
}

/** Hosts whose changes are identical are shown once (large groups). The
 * lines name the policy and the peer, not the host, so they compare. */
function groupImpacts(impact: CheckResult["impact"]) {
  const out: { im: CheckResult["impact"][number]; names: string[] }[] = [];
  const byKey = new Map<string, number>();
  for (const im of impact) {
    const key = JSON.stringify([im.mode, im.enforced, im.supported, im.refused ?? "", im.added, im.new_denies, im.removed, im.conflicts]);
    const i = byKey.get(key);
    if (i === undefined) {
      byKey.set(key, out.length);
      out.push({ im, names: [im.host_name] });
    } else out[i]!.names.push(im.host_name);
  }
  return out;
}

type Allowance = Schemas["FwAllowance"];

function ExplainList({ items }: { items: Allowance[] }) {
  const rows = items.filter((a) => a.reason.kind !== "base");
  const section = (dir: "in" | "out", title: string) => {
    const xs = rows.filter((a) => a.direction === dir);
    return (
      <div>
        <div className="mt-2 text-xs font-medium uppercase text-muted">{title}</div>
        <ul className="text-xs">
          {xs.map((a, i) => (
            <li key={i} className="flex flex-wrap gap-1 py-0.5">
              <Badge tone={a.verdict === "accept" ? (a.reason.kind === "management" ? "accent" : "ok") : "danger"}>
                {a.reason.kind === "management" ? "DROOL management" : a.verdict === "accept" ? "allow" : "deny"}
              </Badge>
              <span>{a.service}</span><span className="text-muted">{dir === "in" ? "from" : "to"}</span><span>{a.peer}</span>
              <span className="text-muted">— {a.reason.kind === "default" ? "default" : a.reason.text}</span>
            </li>
          ))}
        </ul>
      </div>
    );
  };
  return <>{section("in", "Inbound (to this host)")}{section("out", "Outbound (from this host)")}</>;
}

/** Can A reach B? Uses the server's simulation on the draft. */
export function SimulateBox({ doc, hosts, from, to, service }: { doc: Doc; hosts: { id: string; name: string }[]; from?: string; to?: string; service?: { protocol: string; ports?: string } }) {
  const endpoint = (ref: string | undefined) => {
    if (!ref) return { host: "", addr: "" };
    const r = parseRef(ref);
    if (r.kind === "host") return { host: hosts.find((h) => h.name === r.name)?.id ?? "", addr: "" };
    if (r.kind === "network") return { host: "", addr: (doc.networks.find((n) => n.name === r.name)?.cidrs[0] ?? "").replace(/\/.*$/, "") };
    if (r.kind === "cidr") return { host: "", addr: r.name.replace(/\/.*$/, "") };
    if (r.kind === "internet") return { host: "", addr: "203.0.113.10" };
    if (r.kind === "internal") return { host: "", addr: "10.0.0.1" };
    return { host: "", addr: "" };
  };
  const [src, setSrc] = useState(endpoint(from));
  const [dst, setDst] = useState(endpoint(to));
  const firstPort = (service?.ports ?? "22").split(/[,-]/)[0] ?? "22";
  const [protocol, setProtocol] = useState<"tcp" | "udp">(service?.protocol === "udp" ? "udp" : "tcp");
  const [port, setPort] = useState(firstPort);
  const [result, setResult] = useState<Schemas["FwSimResult"] | null>(null);
  const [err, setErr] = useState("");
  useEffect(() => { setSrc(endpoint(from)); setDst(endpoint(to)); setResult(null); }, [from, to]); // eslint-disable-line react-hooks/exhaustive-deps
  const groupMembers = (ref?: string) => (ref && parseRef(ref).kind === "group" ? doc.groups.find((g) => g.name === parseRef(ref).name)?.members ?? [] : []);
  const ask = async () => {
    setErr("");
    try {
      setResult(await unwrap(api.POST("/firewall/simulate", { body: {
        from_host: src.host || undefined, from_address: src.host ? undefined : src.addr, to_host: dst.host || undefined, to_address: dst.host ? undefined : dst.addr,
        protocol, port: Number(port), document: doc as Schemas["FwDocument"],
      } })));
    } catch (e) {
      setResult(null);
      setErr(errorMessage(e));
    }
  };
  const side = (label: string, v: { host: string; addr: string }, set: (x: { host: string; addr: string }) => void, ref?: string) => {
    const members = groupMembers(ref);
    return (
      <label className="flex flex-col gap-1 text-xs">
        <span className="text-muted">{label}</span>
        {members.length > 0 ? (
          <Select aria-label={`${label} member`} value={v.host} onChange={(e) => set({ host: e.target.value, addr: "" })}>
            <option value="">a member of {refLabel(ref!)}…</option>
            {members.map((m) => <option key={m} value={hosts.find((h) => h.name === m)?.id ?? ""}>{m}</option>)}
          </Select>
        ) : (
          <Select aria-label={`${label} host`} value={v.host} onChange={(e) => set({ host: e.target.value, addr: e.target.value ? "" : v.addr })}>
            <option value="">an address…</option>
            {hosts.map((h) => <option key={h.id} value={h.id}>{h.name}</option>)}
          </Select>
        )}
        {!v.host && <Input aria-label={`${label} address`} placeholder="IP address" value={v.addr} onChange={(e) => set({ host: "", addr: e.target.value })} />}
      </label>
    );
  };
  return (
    <div className="space-y-2 rounded-ui border border-line p-2" data-testid="simulate">
      <div className="text-sm font-medium">Can it reach…? <span className="font-normal text-muted">(simulated on this draft: nothing is applied)</span></div>
      <div className="grid grid-cols-2 gap-2">{side("From", src, setSrc, from)}{side("To", dst, setDst, to)}</div>
      <div className="flex items-end gap-2">
        <label className="flex flex-col gap-1 text-xs"><span className="text-muted">Protocol</span>
          <Select aria-label="Simulation protocol" value={protocol} onChange={(e) => setProtocol(e.target.value as "tcp" | "udp")} className="w-20"><option value="tcp">TCP</option><option value="udp">UDP</option></Select>
        </label>
        <label className="flex flex-col gap-1 text-xs"><span className="text-muted">Port</span>
          <Input aria-label="Simulation port" value={port} onChange={(e) => setPort(e.target.value)} className="w-24" />
        </label>
        <Button size="sm" variant="primary" onClick={() => void ask()}>Ask</Button>
      </div>
      {err && <Alert>{err}</Alert>}
      {result && (
        <div aria-live="polite">
          <div className="flex items-baseline gap-2">
            <span className={`text-xl font-bold ${result.answer === "YES" ? "text-ok" : result.answer === "NO" ? "text-danger" : "text-warn"}`}>{result.answer}</span>
            <span className="text-xs">{result.from} → {result.to} {protocol.toUpperCase()}/{port}</span>
          </div>
          <ul className="text-xs">
            {result.hops.map((h, i) => (
              <li key={i}><strong>{h.host}</strong> ({h.direction === "out" ? "leaving" : "arriving"}): <Badge tone={h.verdict === "accept" ? "ok" : "danger"}>{hopVerdict(h)}</Badge>{" "}
                {h.reason.text}{h.rule ? <span className="text-muted"> — {h.rule}</span> : null}{h.reason.kind === "management" && <> <Badge tone="accent">management-path exception</Badge></>}
                {(h.notes ?? []).map((n) => <div key={n} className="text-muted">{n}</div>)}
              </li>
            ))}
          </ul>
          <p className="text-xs text-muted">{result.summary}</p>
        </div>
      )}
    </div>
  );
}

/** Details of the selected node or edge. */
export function Inspector({ selection, doc, services, hosts, management, onEditRule, onClose }: {
  selection: Selection; doc: Doc; services: ServiceInfo[]; hosts: { id: string; name: string }[]; management: MgmtPath[];
  onEditRule: (index: number) => void; onClose: () => void;
}) {
  const [simTo, setSimTo] = useState("");
  const all = useMemo(() => [...services.filter((s) => s.builtin), ...doc.services.map((s) => ({ ...s, builtin: false }))], [services, doc.services]);
  const nodeKey = selection?.kind === "node" ? selection.key : "";
  const hostName = nodeKey.startsWith("host:") ? nodeKey.slice(5) : "";
  const hostId = hosts.find((h) => h.name === hostName)?.id ?? "";
  const explain = useQuery({
    queryKey: ["firewall", "designer-explain", hostId, JSON.stringify(doc)],
    queryFn: () => unwrap(api.POST("/firewall/document/explain", { body: { host_id: hostId, document: doc as Schemas["FwDocument"] } })),
    enabled: !!hostId,
  });
  if (!selection) {
    return <p className="text-sm text-muted">Select a box or a line on the topology to see what it allows, or to ask whether one object can reach another.</p>;
  }
  const head = (title: string, sub: string) => (
    <div className="flex items-start justify-between gap-2">
      <div><div className="text-xs uppercase text-muted">{sub}</div><h3 className="text-base font-semibold">{title}</h3></div>
      <Button size="sm" variant="ghost" onClick={onClose} aria-label="Close details">×</Button>
    </div>
  );
  if (selection.kind === "edge") {
    const e: GEdge = selection.edge;
    if (e.kind === "observed") return <ObservedEdge e={e} hosts={hosts} onClose={onClose} />;
    if (e.kind === "mgmt") {
      return (
        <div className="space-y-2 text-sm">
          {head("Protected DROOL management path", "read-only")}
          <p>{refLabel(e.source === "controller" ? "host:DROOL Controller" : e.source)} → {refLabel(e.target)}</p>
          <p className="text-xs">{e.detail}</p>
          <p className="text-xs text-muted">DROOL needs this connection to manage {e.count === 1 ? "this host" : `${e.count} hosts`}. It is always allowed ahead of every rule and can&apos;t be edited as a rule.
            A deny that overlaps it makes applying refuse (an audited emergency override is still protected by automatic rollback).</p>
          {e.status !== "protected" && <Alert tone={e.status === "blocked" ? "danger" : "warn"}>{e.status === "blocked" ? "Blocked by the draft: applying is refused." : "Not measured yet: click Check on the host."}</Alert>}
        </div>
      );
    }
    if (e.kind === "member") return <div className="text-sm">{head("Pack membership", "member")}<p className="text-xs text-muted">{refLabel(e.target)} is a member of {refLabel(e.source)}. Edit members in the Rules view.</p></div>;
    return (
      <div className="space-y-3 text-sm">
        {head(`${refLabel(e.source)} → ${refLabel(e.target)}`, e.action === "deny" ? "deny" : "allow")}
        {e.rules.map((r) => {
          const rule = doc.policy[r.index]!;
          return (
            <div key={r.index} className="rounded-ui border border-line p-2">
              <div className="flex items-center justify-between gap-2">
                <strong>{r.name}</strong>
                <Button size="sm" onClick={() => onEditRule(r.index)}>Edit rule</Button>
              </div>
              {!r.enabled && <Badge>disabled</Badge>}
              <ul className="text-xs">{r.services.map((s) => <li key={s}>{serviceLabel(s, all)}</li>)}</ul>
              {(rule.from.length > 1 || rule.to.length > 1) && <p className="text-xs text-muted">This rule also covers: {[...rule.from, ...rule.to].filter((x) => x !== e.source && x !== e.target).map(refLabel).join(", ")}.</p>}
              <p className="text-xs text-muted">Affected hosts: {affectedHosts(doc, rule, hosts).join(", ") || "none (external peers only)"}</p>
            </div>
          );
        })}
        <SimulateBox doc={doc} hosts={hosts} from={e.source} to={e.target}
          service={all.find((s) => s.name === e.rules[0]?.services[0])?.entries.find((x) => x.protocol === "tcp" || x.protocol === "udp")} />
      </div>
    );
  }
  if (/^(asn|ip|more|special):/.test(nodeKey)) return <ObservedNode nodeKey={nodeKey} head={head} />;
  const r = parseRef(nodeKey);
  const usedBy = doc.policy.flatMap((p, i) => (p.from.includes(nodeKey) || p.to.includes(nodeKey) ? [i] : []));
  const mine = management.filter((m) => m.host === hostName);
  return (
    <div className="space-y-3 text-sm">
      {head(nodeKey === "controller" ? "DROOL Controller" : refLabel(nodeKey), nodeKey === "controller" ? "management (read-only)" : kindLabel[r.kind])}
      {r.kind === "network" && <p className="font-mono text-xs">{doc.networks.find((n) => n.name === r.name)?.cidrs.join(", ")}</p>}
      {r.kind === "group" && <p className="text-xs">Members: {doc.groups.find((g) => g.name === r.name)?.members.join(", ") || "none"}</p>}
      {usedBy.length > 0 && (
        <div>
          <div className="text-xs font-medium uppercase text-muted">Rules</div>
          <ul className="text-xs">{usedBy.map((i) => <li key={i}><button type="button" className="underline" onClick={() => onEditRule(i)}>{doc.policy[i]!.name}</button> <span className="text-muted">({doc.policy[i]!.action})</span></li>)}</ul>
        </div>
      )}
      {hostId && (
        <div>
          <div className="text-xs font-medium uppercase text-muted">DROOL-required management access</div>
          {mine.length === 0 && <p className="text-xs text-muted">None recorded.</p>}
          {mine.map((m) => (
            <div key={m.kind} className="text-xs">
              <Badge tone={m.blocked ? "danger" : m.unverified ? "warn" : "accent"}>{m.kind === "agent" ? "agent (mTLS)" : "SSH"}</Badge>{" "}
              {m.hops.map((h) => `${h.from_name} → ${h.to_name} :${h.protocol.toUpperCase()} ${h.port || "?"} (${h.status})`).join("; ")}
            </div>
          ))}
          {explain.isLoading && <p className="text-xs text-muted">Explaining…</p>}
          {explain.error && <Alert>{errorMessage(explain.error)}</Alert>}
          {explain.data && (
            <>
              {explain.data.mode !== "enforce" && <p className="mt-1 text-xs text-muted">Mode {explain.data.mode}: DROOL doesn&apos;t filter this host now; this is what it would enforce.</p>}
              <ExplainList items={explain.data.explain} />
              {explain.data.conflicts.map((c) => <Alert key={c}>{c}</Alert>)}
            </>
          )}
        </div>
      )}
      {nodeKey !== "controller" && (
        <>
          <label className="flex flex-col gap-1 text-xs"><span className="text-muted">Can it reach…</span>
            <Select aria-label="Simulation destination" value={simTo} onChange={(e) => setSimTo(e.target.value)}>
              <option value="">choose a destination…</option>
              {hosts.map((h) => <option key={h.id} value={`host:${h.name}`}>{h.name}</option>)}
              {doc.groups.map((g) => <option key={g.name} value={`group:${g.name}`}>group {g.name}</option>)}
              <option value="internet">Internet</option>
            </Select>
          </label>
          {simTo && <SimulateBox doc={doc} hosts={hosts} from={nodeKey} to={simTo} />}
        </>
      )}
      <p className="text-[11px] text-muted">Services show their protocol and port: {all.slice(0, 3).map((s) => `${s.name} = ${entriesLabel(s.entries)}`).join(", ")}…</p>
    </div>
  );
}

const nodeName = (k: string) => (k === "controller" ? "DROOL Controller" : k.replace(/^(host|group|network|asn|ip|special|more):/, "").replace(/^internal$/, "Internal").replace(/^internet$/, "Internet"));

/** Where an observed line's connections are listed in Network activity. */
function flowsLink(e: GEdge, hosts: { id: string; name: string }[]): string {
  const p = new URLSearchParams();
  for (const k of [e.source, e.target]) {
    if (k.startsWith("host:")) {
      const id = hosts.find((h) => h.name === k.slice(5))?.id;
      if (id && !p.has("host")) p.append("host", id);
    } else if (k.startsWith("asn:") && /^asn:\d+$/.test(k)) p.set("asn", k.slice(4));
    else if (k.startsWith("ip:")) p.set("peer", k.slice(3));
    else if (k === "internal") p.set("scope", "internal");
  }
  return `/network?${p.toString()}`;
}

/** An observed line: what was seen, how the policy classifies it, and the
 * connections behind it. */
function ObservedEdge({ e, hosts, onClose }: { e: GEdge; hosts: { id: string; name: string }[]; onClose: () => void }) {
  const o = e.obs!;
  const [open, setOpen] = useState<number>();
  return (
    <div className="space-y-3 text-sm" data-testid="observed-details">
      <div className="flex items-start justify-between gap-2">
        <div><div className="text-xs uppercase text-muted">observed connections</div><h3 className="text-base font-semibold">{nodeName(e.source)} → {nodeName(e.target)}</h3></div>
        <Button size="sm" variant="ghost" onClick={onClose} aria-label="Close details">×</Button>
      </div>
      <p>{obsStatusLabel[o.worst]?.replace("Observed · ", "Most notable: ")}</p>
      <dl className="grid grid-cols-[7rem_1fr] gap-y-1 text-xs">
        <dt className="text-muted">Connections</dt><dd>{o.count.toLocaleString()} in {o.flows} flow(s)</dd>
        <dt className="text-muted">Services</dt><dd>{o.services.map((s) => `${s.service ? s.service + " " : ""}${s.protocol.toUpperCase()} ${s.port} (${s.count})`).join(", ")}</dd>
        <dt className="text-muted">Processes</dt><dd>{o.processes.length ? o.processes.join(", ") : <span className="italic">Unknown</span>}</dd>
        <dt className="text-muted">First seen</dt><dd>{when(o.first_seen)}</dd>
        <dt className="text-muted">Last seen</dt><dd>{when(o.last_seen)}</dd>
        <dt className="text-muted">Policy</dt>
        <dd className="space-x-1">{Object.entries(o.status).map(([k, v]) => <Badge key={k} tone={k === "denied" ? "danger" : k === "unclassified" ? "warn" : k === "management" ? "accent" : "ok"}>{obsStatusLabel[k]?.replace("Observed · ", "")}: {v}</Badge>)}
          {o.would_deny > 0 && <Badge tone="danger">✕ would be denied: {o.would_deny}</Badge>}</dd>
      </dl>
      <div>
        <div className="mb-1 text-xs font-medium uppercase text-muted">Connections</div>
        <ul className="space-y-1">{o.flow_ids.slice(0, 6).map((id) => <FlowLine key={id} id={id} onOpen={() => setOpen(id)} />)}</ul>
        <Link className="mt-1 inline-block text-xs text-accent underline" to={flowsLink(e, hosts)}>Show all in Network Activity</Link>
      </div>
      <p className="text-[11px] text-muted">Observed lines show traffic DROOL has seen, not routes. Their status is the saved policy&apos;s verdict (not this draft&apos;s), whether or not a host enforces it; nothing here changes it — use “Create policy from observed connection” in a connection&apos;s details to start a draft rule.</p>
      {open !== undefined && <FlowInspector id={open} onClose={() => setOpen(undefined)} />}
    </div>
  );
}

function FlowLine({ id, onOpen }: { id: number; onOpen: () => void }) {
  const q = useNetFlow(id);
  const f = q.data;
  if (!f) return <li className="text-xs text-muted">…</li>;
  return (
    <li className="text-xs">
      <button type="button" className="text-left text-accent underline" onClick={onOpen}>
        {f.host.name} {f.direction === "in" ? "←" : "→"} {f.remote_ip}:{f.port} {f.protocol.toUpperCase()}
      </button>{" "}
      <span className="text-muted">{f.process || "unknown process"} · {f.count}</span> <PolicyBadge flow={f} />
    </li>
  );
}

function ObservedNode({ nodeKey, head }: { nodeKey: string; head: (title: string, sub: string) => React.ReactNode }) {
  const [kind, value] = [nodeKey.split(":")[0]!, nodeKey.slice(nodeKey.indexOf(":") + 1)];
  const link = kind === "asn" && /^\d+$/.test(value) ? `/network?asn=${value}` : kind === "ip" ? `/network?peer=${encodeURIComponent(value)}` : "/network?scope=external";
  return (
    <div className="space-y-2 text-sm">
      {head(kind === "asn" ? (value === "other" ? "Other external networks" : `AS${value}`) : value, kind === "asn" ? "external network (observed)" : kind === "ip" ? "address (observed)" : "observed")}
      <p className="text-xs text-muted">
        {kind === "asn" ? "Public addresses grouped by the network that owns them, from the local ASN database. Country and owner are context, not a verdict."
          : "An address DROOL has seen hosts connect to or from."}
      </p>
      <Link className="text-xs text-accent underline" to={link}>Show its connections in Network Activity</Link>
    </div>
  );
}
