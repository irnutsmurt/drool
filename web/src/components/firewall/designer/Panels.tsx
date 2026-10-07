import { useEffect, useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { api, unwrap, type Schemas } from "@/api/client";
import { Badge } from "@/components/ui/Badge";
import { Button } from "@/components/ui/Button";
import { Alert } from "@/components/ui/Alert";
import { Input, Select } from "@/components/ui/Field";
import { errorMessage } from "@/lib/format";
import { affectedHosts, entriesLabel, kindLabel, parseRef, refLabel, serviceLabel, type Doc, type GEdge, type MgmtPath, type ServiceInfo } from "./model";
import type { CheckResult } from "./useDesigner";
import type { Selection } from "./Topology";

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
            <p className="mt-1 text-xs text-muted">Applying is refused while this is so. You can still save the draft; an administrator&apos;s audited emergency override still goes through the dead-man rollback.</p>
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

/** Before saving/applying: what changes where, and what blocks it. */
export function PreviewPanel({ check, onApply, canApply }: { check: CheckResult | null; onApply?: (hostIds: string[]) => void; canApply: boolean }) {
  if (!check) return <p className="text-sm text-muted">Checking…</p>;
  const enforcedTargets = check.impact.filter((i) => i.mode === "enforce" && i.supported !== false && i.conflicts.length === 0).map((i) => i.host_id);
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
      {onApply && canApply && enforcedTargets.length > 0 && !check.changed && (
        <Button variant="primary" onClick={() => onApply(enforcedTargets)}>Apply to {enforcedTargets.length} enforced host(s)…</Button>
      )}
    </div>
  );
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
      <div className="text-sm font-medium">Can it reach…? <span className="font-normal text-muted">(on this draft)</span></div>
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
              <li key={i}><strong>{h.host}</strong> ({h.direction === "out" ? "leaving" : "arriving"}): <Badge tone={h.verdict === "accept" ? "ok" : "danger"}>{h.verdict === "accept" ? "allowed" : "blocked"}</Badge>{" "}
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
    if (e.kind === "mgmt") {
      return (
        <div className="space-y-2 text-sm">
          {head("Protected DROOL management path", "read-only")}
          <p>{refLabel(e.source === "controller" ? "host:DROOL Controller" : e.source)} → {refLabel(e.target)}</p>
          <p className="text-xs">{e.detail}</p>
          <p className="text-xs text-muted">DROOL needs this connection to manage {e.count === 1 ? "this host" : `${e.count} hosts`}. It is always allowed ahead of every rule and can&apos;t be edited as a rule.
            A deny that overlaps it makes applying refuse (an audited emergency override still goes through the dead-man rollback).</p>
          {e.status !== "protected" && <Alert tone={e.status === "blocked" ? "danger" : "warn"}>{e.status === "blocked" ? "Blocked by the draft: applying is refused." : "Not measured yet: click Check on the host."}</Alert>}
        </div>
      );
    }
    if (e.kind === "member") return <div className="text-sm">{head("Group membership", "member")}<p className="text-xs text-muted">{refLabel(e.target)} is a member of {refLabel(e.source)}. Edit members in the Rules view.</p></div>;
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
