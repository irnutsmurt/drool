import { memo, useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  ReactFlow, ReactFlowProvider, Background, Controls, MiniMap, Handle, Position, BaseEdge, EdgeLabelRenderer, MarkerType,
  useNodesState, useReactFlow,
  type Connection, type Edge, type EdgeProps, type Node, type NodeProps,
} from "@xyflow/react";
import "@xyflow/react/dist/style.css";
import dagre from "@dagrejs/dagre";
import { useTheme } from "@/lib/theme";
import { Button } from "@/components/ui/Button";
import { Input, Select } from "@/components/ui/Field";
import { allServices, kindLabel, obsServicesLabel, obsStatusLabel, parseRef, toGraph, type Doc, type GEdge, type GNode, type MgmtPath, type Observed, type ServiceInfo, type TopologyMode } from "./model";
import { useNetTopology } from "@/components/network/queries";
import type { Layout } from "./useDesigner";

export type Selection = { kind: "node"; key: string } | { kind: "edge"; edge: GEdge } | null;

type NodeData = { g: GNode; expanded: boolean; highlighted: boolean; onToggle: (key: string) => void; obsExpanded: boolean; onObsToggle: (key: string) => void };
type EdgeData = { g: GEdge; offset: number; highlighted: boolean; onSelect: () => void };

const nodeStyle: Record<string, string> = {
  host: "var(--ui-node-host)", group: "var(--ui-node-group)", network: "var(--ui-node-network)", cidr: "var(--ui-node-network)",
  internet: "var(--ui-node-external)", internal: "var(--ui-node-external)", any: "var(--ui-node-external)", controller: "var(--ui-node-controller)",
  asn: "var(--ui-node-asn)", ip: "var(--ui-node-asn)", more: "var(--ui-node-asn)", special: "var(--ui-node-external)",
};
const nodeIcon: Record<string, string> = { host: "▣", group: "▦", network: "◇", cidr: "◇", internet: "◎", internal: "◉", any: "✱", controller: "⛨", asn: "☁", ip: "•", more: "…", special: "◌" };
const obsKindLabel: Record<string, string> = { asn: "External network", ip: "Address", more: "Addresses", special: "Special addresses" };
const kindText = (k: string) => obsKindLabel[k] ?? kindLabel[k as keyof typeof kindLabel] ?? k;

/** Observed edges by policy status: colour plus a dash pattern and a
 * labelled symbol, so the status never depends on colour alone. */
const obsStyle: Record<string, { color: string; dash: string }> = {
  covered: { color: "var(--ui-edge-observed)", dash: "2 4" },
  management: { color: "var(--ui-edge-mgmt)", dash: "2 4" },
  unclassified: { color: "var(--ui-warn)", dash: "8 4" },
  denied: { color: "var(--ui-danger)", dash: "3 3" },
};

const DesignerNode = memo(function DesignerNode({ data, selected }: NodeProps<Node<NodeData>>) {
  const { g, expanded, highlighted, onToggle, obsExpanded, onObsToggle } = data;
  const canExpandObs = !!g.observed && g.observed.member_total > 1 && (g.kind === "asn" || g.kind === "internal" || g.kind === "internet");
  const isController = g.kind === "controller";
  return (
    <div
      className="min-w-36 max-w-60 rounded-ui border px-3 py-2 text-xs text-fg shadow-sm"
      style={{
        background: nodeStyle[g.kind],
        borderColor: highlighted ? "var(--ui-danger)" : selected ? "var(--ui-accent)" : "var(--ui-line)",
        borderStyle: isController ? "dashed" : "solid",
        boxShadow: highlighted ? "0 0 0 3px var(--ui-danger-bg)" : selected ? "0 0 0 3px color-mix(in srgb, var(--ui-accent) 25%, transparent)" : undefined,
      }}
      aria-label={`${isController ? "DROOL Controller" : kindText(g.kind)} ${g.label}`}
    >
      <Handle type="target" position={Position.Left} isConnectable={!isController} />
      <div className="flex items-center gap-1.5">
        <span aria-hidden className="text-muted">{nodeIcon[g.kind]}</span>
        <span className="truncate font-semibold">{g.label}</span>
      </div>
      <div className="mt-0.5 text-[10px] uppercase tracking-wide text-muted">{isController ? "management · read-only" : kindText(g.kind)}{g.observed && g.kind !== "ip" && g.kind !== "more" ? ` · ${g.observed.member_total} address${g.observed.member_total === 1 ? "" : "es"}` : ""}</div>
      {g.detail && <div className="mt-0.5 truncate text-muted" title={g.detail}>{g.detail}</div>}
      {g.kind === "group" && (
        <button type="button" className="nodrag mt-1 rounded border border-line bg-surface px-1.5 text-[11px] hover:bg-surface-2"
          aria-expanded={expanded} onClick={(e) => { e.stopPropagation(); onToggle(g.key); }}>
          {expanded ? "Collapse" : "Show members"}
        </button>
      )}
      {canExpandObs && (
        <button type="button" className="nodrag mt-1 rounded border border-line bg-surface px-1.5 text-[11px] hover:bg-surface-2"
          aria-expanded={obsExpanded} onClick={(e) => { e.stopPropagation(); onObsToggle(g.key); }}>
          {obsExpanded ? "Group addresses" : "Show addresses"}
        </button>
      )}
      <Handle type="source" position={Position.Right} isConnectable={!isController} />
    </div>
  );
});

const DesignerEdge = memo(function DesignerEdge({ id, sourceX, sourceY, targetX, targetY, data, selected, markerEnd }: EdgeProps<Edge<EdgeData>>) {
  const g = data!.g;
  const off = data!.offset;
  // A bezier bent by `offset`, so parallel edges (allow and deny, or a
  // management path along a rule) stay apart.
  const mx = (sourceX + targetX) / 2;
  const path = `M ${sourceX},${sourceY} C ${mx},${sourceY + off} ${mx},${targetY + off} ${targetX},${targetY}`;
  // Labels sit on the curve: rules at the middle, management paths nearer
  // their source, so the two don't stack where both run between the same
  // boxes.
  const t = g.kind === "mgmt" ? 0.3 : g.kind === "observed" ? 0.68 : 0.5;
  const u = 1 - t;
  const lx = u * u * u * sourceX + 3 * u * u * t * mx + 3 * u * t * t * mx + t * t * t * targetX;
  const ly = u * u * u * sourceY + 3 * u * u * t * (sourceY + off) + 3 * u * t * t * (targetY + off) + t * t * t * targetY;
  const os = g.kind === "observed" ? obsStyle[g.obs!.worst] ?? obsStyle.covered! : undefined;
  const color = os ? os.color : g.kind === "mgmt" ? (g.status === "blocked" ? "var(--ui-danger)" : g.status === "unverified" ? "var(--ui-warn)" : "var(--ui-edge-mgmt)")
    : g.kind === "member" ? "var(--ui-edge-member)" : g.action === "deny" ? "var(--ui-edge-deny)" : "var(--ui-edge-allow)";
  const dash = os ? os.dash : g.kind === "mgmt" ? "7 5" : g.kind === "member" ? "2 4" : g.action === "deny" ? "10 3" : undefined;
  const width = data!.highlighted || selected ? 3.5 : g.kind === "member" ? 1 : 2;
  return (
    <>
      <BaseEdge id={id} path={path} markerEnd={markerEnd} interactionWidth={18}
        style={{ stroke: data!.highlighted ? "var(--ui-danger)" : selected && g.kind === "rule" ? "var(--ui-accent)" : color, strokeWidth: width, strokeDasharray: dash, opacity: g.kind === "member" ? 0.8 : 1 }}
        className={g.kind === "mgmt" && g.status === "blocked" ? "designer-blocked" : undefined} />
      {g.kind !== "member" && (
        <EdgeLabelRenderer>
          <button type="button" onClick={(ev) => { ev.stopPropagation(); data!.onSelect(); }}
            aria-label={`${g.kind === "mgmt" ? "DROOL management path" : g.kind === "observed" ? "Observed connections" : g.action === "deny" ? "Deny rule" : "Allow rule"}: ${(g.kind === "observed" ? g.detail! : g.label).replace("\n", " · ")}. Show details`}
            className="nodrag nopan pointer-events-auto absolute max-w-64 cursor-pointer rounded border px-1.5 py-0.5 text-left text-[10px] leading-tight shadow-sm focus:outline focus:outline-2 focus:outline-accent"
            style={{
              transform: `translate(-50%, -50%) translate(${lx}px, ${ly}px)`,
              background: "var(--ui-surface)", color: g.kind === "mgmt" || g.action === "deny" || data!.highlighted || (os && g.obs!.worst !== "covered") ? color : "var(--ui-text)",
              borderColor: data!.highlighted ? "var(--ui-danger)" : selected ? "var(--ui-accent)" : os ? color : "var(--ui-line)", borderStyle: g.kind === "mgmt" ? "dashed" : os ? "dotted" : "solid",
              fontWeight: g.kind === "mgmt" && g.status === "blocked" ? 700 : 500,
            }}
            data-edge-label={g.kind} title={(g.kind === "observed" ? g.detail! : g.label).replace("\n", " · ")}>
            {g.label.split("\n").map((l, i) => <span key={i} className={`block ${i > 0 ? "font-normal opacity-90" : ""}`}>{l}</span>)}
          </button>
        </EdgeLabelRenderer>
      )}
    </>
  );
});

const nodeTypes = { designer: DesignerNode };
const edgeTypes = { designer: DesignerEdge };

function autoLayout(nodes: GNode[], edges: GEdge[]): Record<string, { x: number; y: number }> {
  const g = new dagre.graphlib.Graph();
  g.setGraph({ rankdir: "LR", nodesep: 70, ranksep: 230 });
  g.setDefaultEdgeLabel(() => ({}));
  for (const n of nodes) g.setNode(n.key, { width: 190, height: n.kind === "group" ? 92 : 68 });
  for (const e of edges) if (e.kind !== "mgmt") g.setEdge(e.source, e.target);
  // The controller sits left of what it manages.
  for (const e of edges) if (e.kind === "mgmt" && e.source === "controller") g.setEdge(e.source, e.target);
  dagre.layout(g);
  const out: Record<string, { x: number; y: number }> = {};
  for (const n of nodes) {
    const p = g.node(n.key);
    if (p) out[n.key] = { x: p.x - 95, y: p.y - 34 };
  }
  return out;
}

export type TopologyProps = {
  doc: Doc; services: ServiceInfo[]; hosts: { id: string; name: string }[]; management: MgmtPath[];
  layout: Layout; saveLayout: (l: Layout) => void; canWrite: boolean;
  selection: Selection; onSelect: (s: Selection) => void;
  onConnect: (from: string, to: string) => void; onNewRule: () => void;
  highlight: { policies: string[]; nodes: string[] }; focusNodes?: string[];
  /** Configured (the policy), Observed (what DROOL has seen) or both. */
  mode: TopologyMode; onMode: (m: TopologyMode) => void; canObserve: boolean;
};

const modes: { id: TopologyMode; label: string }[] = [
  { id: "configured", label: "Configured" },
  { id: "observed", label: "Observed" },
  { id: "both", label: "Configured + Observed" },
];

function TopologyInner(p: TopologyProps) {
  const { resolved } = useTheme();
  const rf = useReactFlow();
  const [filter, setFilter] = useState("");
  const [showMgmt, setShowMgmt] = useState(true);
  const [showAll, setShowAll] = useState(false);
  const [addKind, setAddKind] = useState("");
  const expanded = useMemo(() => new Set(p.layout.expanded ?? []), [p.layout.expanded]);
  const pinned = useMemo(() => new Set(p.layout.pinned ?? []), [p.layout.pinned]);
  const mode: TopologyMode = p.canObserve ? p.mode : "configured";
  const [obsExpand, setObsExpand] = useState<string[]>([]);
  const [obsStatus, setObsStatus] = useState<"" | "unclassified" | "denied" | "covered" | "management">("");
  const [obsPeriod, setObsPeriod] = useState("7d");
  const [obsScope, setObsScope] = useState<"" | "internal" | "external">("");
  // Few external networks by default (the rest fold into "Other external
  // networks"): a readable picture first, more on request.
  const [obsMax, setObsMax] = useState(8);
  const obsQ = useNetTopology({ expand: obsExpand, policy: obsStatus || undefined, last_seen_after: obsPeriod || undefined, scope: obsScope || undefined, max_external: obsMax },
    mode !== "configured");
  const observed = obsQ.data as Observed | undefined;
  const obsToggle = useCallback((key: string) => setObsExpand((xs) => (xs.includes(key) ? xs.filter((x) => x !== key) : [...xs, key])), []);

  // Built-in services plus the draft's custom ones (new ones included).
  const graph = useMemo(
    () => toGraph(p.doc, allServices(p.doc, p.services), { mode, expanded, pinned, showAllHosts: showAll, showManagement: showMgmt, filter, hosts: p.hosts }, p.management,
      mode !== "configured" ? observed : undefined),
    [p.doc, p.services, mode, expanded, pinned, showAll, showMgmt, filter, p.hosts, p.management, observed],
  );

  const toggle = useCallback((key: string) => {
    const next = new Set(expanded);
    if (next.has(key)) next.delete(key); else next.add(key);
    p.saveLayout({ ...p.layout, expanded: [...next] });
  }, [expanded, p]);

  // Positions: saved ones win; boxes already on screen stay where they are;
  // only new ones get an automatic position (Auto layout redoes all).
  const shown = useRef<Record<string, { x: number; y: number }>>({});
  const positions = useMemo(() => {
    const saved = p.layout.nodes ?? {};
    const prevShown = shown.current;
    const auto = autoLayout(graph.nodes, graph.edges);
    const out: Record<string, { x: number; y: number }> = {};
    const fresh: string[] = [];
    for (const n of graph.nodes) {
      const kept = saved[n.key] ?? shown.current[n.key];
      if (kept) out[n.key] = kept;
      else fresh.push(n.key);
    }
    // New boxes: their automatic position, moved down until it doesn't
    // overlap a box that stayed where it was.
    const overlaps = (a: { x: number; y: number }) =>
      Object.values(out).some((b) => Math.abs(a.x - b.x) < 200 && Math.abs(a.y - b.y) < 100);
    const placed = Object.values(out);
    // Members of an expanded group: a compact grid to the right of it.
    // Members of an expanded group, and the addresses of an expanded
    // external network: a compact grid to the right of where it is (was).
    const parentOf = new Map(graph.nodes.filter((n) => n.parentGroup || n.observed?.parent).map((n) => [n.key, (n.parentGroup ?? n.observed!.parent)!]));
    const gridIndex = new Map<string, number>();
    for (const key of fresh) {
      const parent = parentOf.get(key);
      const anchor = parent ? out[parent] ?? auto[parent] ?? prevShown[parent] ?? saved[parent] : undefined;
      if (parent && anchor) {
        const i = gridIndex.get(parent) ?? 0;
        gridIndex.set(parent, i + 1);
        const cols = 4;
        out[key] = { x: anchor.x + 240 + (i % cols) * 200, y: anchor.y + Math.floor(i / cols) * 76 };
        continue;
      }
      let pos = { ...(auto[key] ?? { x: 0, y: 0 }) };
      if (key === "controller" && placed.length > 0) {
        // DROOL's controller goes left of everything it manages.
        const minX = Math.min(...placed.map((q) => q.x));
        const ys = placed.map((q) => q.y);
        pos = { x: minX - 280, y: (Math.min(...ys) + Math.max(...ys)) / 2 };
      }
      for (let i = 0; i < 50 && overlaps(pos); i++) pos.y += 110;
      out[key] = pos;
    }
    shown.current = out;
    return out;
  }, [graph, p.layout.nodes]);

  const flowNodes: Node<NodeData>[] = useMemo(() => graph.nodes.map((n) => ({
    id: n.key, type: "designer", position: positions[n.key]!,
    data: { g: n, expanded: expanded.has(n.key), highlighted: p.highlight.nodes.includes(n.key), onToggle: toggle, obsExpanded: obsExpand.includes(n.key), onObsToggle: obsToggle },
    selected: p.selection?.kind === "node" && p.selection.key === n.key,
    connectable: p.canWrite && n.kind !== "controller" && !n.observed,
    ariaLabel: `${n.kind === "controller" ? "DROOL Controller" : kindText(n.kind)} ${n.label}`,
  })), [graph.nodes, positions, expanded, p.highlight.nodes, toggle, p.selection, p.canWrite, obsExpand, obsToggle]);

  const flowEdges: Edge<EdgeData>[] = useMemo(() => {
    // Parallel edges between the same pair get alternating offsets.
    const seen = new Map<string, number>();
    return graph.edges.map((e) => {
      const pair = [e.source, e.target].sort().join("|");
      const i = seen.get(pair) ?? 0;
      seen.set(pair, i + 1);
      const offset = i === 0 ? 0 : (i % 2 ? 1 : -1) * Math.ceil(i / 2) * 46;
      const highlighted = e.kind === "rule" && e.rules.some((r) => p.highlight.policies.includes(r.name));
      const color = e.kind === "observed" ? (obsStyle[e.obs!.worst] ?? obsStyle.covered!).color
        : e.kind === "mgmt" ? (e.status === "blocked" ? "var(--ui-danger)" : "var(--ui-edge-mgmt)") : e.action === "deny" ? "var(--ui-edge-deny)" : "var(--ui-edge-allow)";
      return {
        id: e.id, source: e.source, target: e.target, type: "designer", data: { g: e, offset, highlighted, onSelect: () => p.onSelect({ kind: "edge", edge: e }) },
        selected: p.selection?.kind === "edge" && p.selection.edge.id === e.id,
        markerEnd: e.kind === "member" ? undefined : { type: MarkerType.ArrowClosed, color, width: 16, height: 16 },
        focusable: e.kind !== "member",
        ariaLabel: e.kind === "mgmt" ? `Protected DROOL management path from ${e.source} to ${e.target}`
          : e.kind === "observed" ? `Observed connections from ${e.source} to ${e.target}: ${e.detail}` : `${e.action} from ${e.source} to ${e.target}: ${e.label}`,
      };
    });
  }, [graph.edges, p.highlight.policies, p.selection, p.onSelect]);

  const [nodes, setNodes, onNodesChange] = useNodesState<Node<NodeData>>(flowNodes);
  useEffect(() => setNodes(flowNodes), [flowNodes, setNodes]);

  // Moving nodes changes only the layout.
  const saveTimer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const onNodeDragStop = useCallback((_: unknown, _n: Node, dragged: Node[]) => {
    const pos = { ...(p.layout.nodes ?? {}) };
    for (const n of dragged) pos[n.id] = { x: Math.round(n.position.x), y: Math.round(n.position.y) };
    clearTimeout(saveTimer.current);
    saveTimer.current = setTimeout(() => p.saveLayout({ ...p.layout, nodes: pos }), 300);
  }, [p]);

  const relayout = () => {
    const auto = autoLayout(graph.nodes, graph.edges);
    shown.current = {};
    p.saveLayout({ ...p.layout, nodes: auto });
    setTimeout(() => rf.fitView({ padding: 0.15 }), 50);
  };

  // Boxes came or went (the controller appears with the first check, a
  // filter changed, ...): keep everything in view.
  const nodeCount = graph.nodes.length;
  useEffect(() => {
    const t = setTimeout(() => rf.fitView({ padding: 0.15, duration: 200 }), 150);
    return () => clearTimeout(t);
  }, [nodeCount, rf]);

  useEffect(() => {
    if (p.focusNodes?.length) setTimeout(() => rf.fitView({ nodes: p.focusNodes!.map((id) => ({ id })), padding: 0.4, duration: 300 }), 50);
  }, [p.focusNodes, rf]);

  const pin = (ref: string) => {
    if (!ref) return;
    p.saveLayout({ ...p.layout, pinned: [...new Set([...(p.layout.pinned ?? []), ref])] });
    setAddKind("");
    // Bring the new box into view (it may land outside the current viewport).
    setTimeout(() => rf.fitView({ padding: 0.15, duration: 200 }), 120);
  };
  const addOptions = [
    ...p.hosts.map((h) => ({ v: `host:${h.name}`, l: `Host: ${h.name}` })),
    ...p.doc.groups.map((g) => ({ v: `group:${g.name}`, l: `Group: ${g.name}` })),
    ...p.doc.networks.map((n) => ({ v: `network:${n.name}`, l: `Network: ${n.name}` })),
    { v: "internet", l: "Internet" }, { v: "internal", l: "Internal networks" }, { v: "any", l: "Anywhere" },
  ].filter((o) => !graph.nodes.some((n) => n.key === o.v));

  return (
    <div className="flex h-full flex-col">
      <div className="flex flex-wrap items-center gap-2 border-b border-line bg-surface px-3 py-2 text-sm" role="toolbar" aria-label="Topology tools">
        {p.canObserve && (
          <div role="group" aria-label="Show configured policy, observed connections, or both" className="inline-flex overflow-hidden rounded-ui border border-line" data-testid="topology-mode">
            {modes.map((m) => (
              <button key={m.id} type="button" aria-pressed={mode === m.id} onClick={() => p.onMode(m.id)}
                className={`px-2 py-1 text-xs ${mode === m.id ? "bg-accent text-accent-fg" : "bg-surface hover:bg-surface-2"}`}>{m.label}</button>
            ))}
          </div>
        )}
        {p.canWrite && mode !== "observed" && <Button size="sm" variant="primary" onClick={p.onNewRule}>New rule…</Button>}
        {mode !== "observed" && <label className="flex items-center gap-1 whitespace-nowrap text-xs">
          <span className="text-muted">Place on canvas</span>
          <Select aria-label="Place an object on the canvas" value={addKind} onChange={(e) => pin(e.target.value)} className="h-7 !w-44 text-xs">
            <option value="">choose…</option>
            {addOptions.map((o) => <option key={o.v} value={o.v}>{o.l}</option>)}
          </Select>
        </label>}
        <Input aria-label="Filter the topology" placeholder="Filter (name, CIDR)…" value={filter} onChange={(e) => setFilter(e.target.value)} className="h-7 !w-44 text-xs" />
        <label className="flex items-center gap-1 whitespace-nowrap text-xs"><input type="checkbox" checked={showMgmt} onChange={(e) => setShowMgmt(e.target.checked)} /> DROOL management paths</label>
        <label className="flex items-center gap-1 whitespace-nowrap text-xs"><input type="checkbox" checked={showAll} onChange={(e) => setShowAll(e.target.checked)} /> All hosts</label>
        {mode !== "configured" && (
          <>
            <Select aria-label="Observed connections: policy status" value={obsStatus} onChange={(e) => setObsStatus(e.target.value as typeof obsStatus)} className="h-7 !w-36 text-xs">
              <option value="">any status</option><option value="unclassified">unclassified</option><option value="denied">denied / would be</option>
              <option value="covered">covered</option><option value="management">DROOL management</option>
            </Select>
            <Select aria-label="Observed connections: remote addresses" value={obsScope} onChange={(e) => setObsScope(e.target.value as typeof obsScope)} className="h-7 !w-36 text-xs">
              <option value="">int. + external</option><option value="internal">internal only</option><option value="external">external only</option>
            </Select>
            <Select aria-label="Observed connections: last seen" value={obsPeriod} onChange={(e) => setObsPeriod(e.target.value)} className="h-7 !w-32 text-xs">
              <option value="24h">seen in 24 h</option><option value="7d">seen in 7 days</option><option value="">any time</option>
            </Select>
            <Select aria-label="External networks shown" value={obsMax} onChange={(e) => setObsMax(Number(e.target.value))} className="h-7 !w-40 text-xs">
              <option value={0}>external: one box</option>
              <option value={5}>top 5 networks</option><option value={8}>top 8 networks</option>
              <option value={15}>top 15 networks</option><option value={30}>top 30 networks</option>
            </Select>
          </>
        )}
        <Button size="sm" variant="ghost" onClick={relayout}>Auto layout</Button>
        <Button size="sm" variant="ghost" onClick={() => rf.fitView({ padding: 0.15, duration: 200 })}>Fit</Button>
        <span className="ml-auto flex flex-wrap items-center gap-3 text-[11px] text-muted" aria-hidden>
          {mode !== "observed" && <span><span style={{ color: "var(--ui-edge-allow)" }}>━━</span> allow</span>}
          {mode !== "observed" && <span><span style={{ color: "var(--ui-edge-deny)" }}>╍╍</span> deny</span>}
          <span><span style={{ color: "var(--ui-edge-mgmt)" }}>┅┅</span> DROOL management (read-only)</span>
          {mode !== "configured" && <>
            <span><span style={{ color: "var(--ui-edge-observed)" }}>┈┈</span> ✓ observed, covered</span>
            <span><span style={{ color: "var(--ui-warn)" }}>╌╌</span> ⚠ observed, unclassified</span>
            <span><span style={{ color: "var(--ui-danger)" }}>┄┄</span> ✕ observed, denied</span>
          </>}
        </span>
      </div>
      <div className="relative min-h-0 flex-1" data-testid="topology-canvas">
        <ReactFlow
          nodes={nodes} edges={flowEdges} nodeTypes={nodeTypes} edgeTypes={edgeTypes} colorMode={resolved}
          onNodesChange={onNodesChange} onNodeDragStop={onNodeDragStop}
          onNodeClick={(_, n) => p.onSelect({ kind: "node", key: n.id })}
          onEdgeClick={(_, e) => p.onSelect({ kind: "edge", edge: (e.data as EdgeData).g })}
          onPaneClick={() => p.onSelect(null)}
          onConnect={(c: Connection) => {
            if (!p.canWrite || !c.source || !c.target || c.source === c.target) return;
            if (c.source === "controller" || c.target === "controller") return;
            if (/^(asn|ip|more|special):/.test(c.source) || /^(asn|ip|more|special):/.test(c.target)) return;
            p.onConnect(c.source, c.target);
          }}
          nodesConnectable={p.canWrite && mode !== "observed"} edgesReconnectable={false} deleteKeyCode={null}
          fitView fitViewOptions={{ padding: 0.15 }} minZoom={0.1} proOptions={{ hideAttribution: true }}
          style={{ background: "var(--ui-canvas)" }}
        >
          <Background color="var(--ui-canvas-dot)" gap={18} />
          <Controls showInteractive={false} />
          {graph.nodes.length > 12 && <MiniMap pannable zoomable nodeColor={(n) => nodeStyle[(n.data as NodeData).g.kind] ?? "var(--ui-surface)"} />}
        </ReactFlow>
        {graph.nodes.length === 0 && (
          <div className="pointer-events-none absolute inset-0 flex items-center justify-center p-8 text-center text-sm text-muted">
            {filter ? "Nothing matches the filter."
              : mode === "observed" ? (obsQ.isLoading ? "Loading observed connections…" : "No observed connections match. Agent hosts report continuously; SSH hosts only after a snapshot.")
                : "No rules yet. Place hosts, groups or networks on the canvas, then drag from the right edge of one to another to allow traffic — or use New rule."}
          </div>
        )}
      </div>
      <p className="border-t border-line bg-surface px-3 py-1.5 text-[11px] text-muted">
        {mode === "configured" ? <>Lines are <strong>allowed (or denied) communication</strong>, not cabling: moving boxes only changes the picture. Drag from a box&apos;s right edge to another box to create a rule.</>
          : <>Dotted lines are <strong>connections DROOL has observed</strong>; external addresses are grouped by the network that owns them (ASN), not by route or country.
            {observed?.collapsed ? <> {observed.collapsed} smaller external network(s) are grouped as “Other external networks”.</> : null}
            {observed?.truncated ? <> Only the {observed.considered.toLocaleString()} most recent flows are drawn.</> : null}
            {" "}The same connections are listed below the canvas.</>}
        {pinned.size > 0 && <> · <button type="button" className="underline" onClick={() => p.saveLayout({ ...p.layout, pinned: [] })}>Unpin {pinned.size} placed object(s)</button></>}
      </p>
      {mode !== "configured" && <ObservedList edges={graph.edges} onSelect={(e) => p.onSelect({ kind: "edge", edge: e })} />}
    </div>
  );
}

/** The keyboard and screen-reader equivalent of the observed overlay: one
 * row per observed line, each selectable. */
export function ObservedList({ edges, onSelect }: { edges: GEdge[]; onSelect: (e: GEdge) => void }) {
  const obs = edges.filter((e) => e.kind === "observed").sort((a, b) => (b.obs!.count - a.obs!.count));
  if (obs.length === 0) return null;
  const name = (k: string) => (k === "controller" ? "DROOL Controller" : k.replace(/^(host|group|network|asn|ip|special|more):/, ""));
  return (
    <details className="border-t border-line bg-surface px-3 py-2 text-xs" data-testid="observed-list">
      <summary className="cursor-pointer font-medium">Observed connections as a list ({obs.length})</summary>
      <table className="mt-2 w-full text-left">
        <thead><tr className="text-muted"><th scope="col" className="py-1">From</th><th scope="col">To</th><th scope="col">Services</th><th scope="col">Status</th><th scope="col" className="text-right">Connections</th><th scope="col"><span className="sr-only">Details</span></th></tr></thead>
        <tbody>
          {obs.map((e) => (
            <tr key={e.id} className="border-t border-line">
              <td className="py-1">{name(e.source)}</td><td>{name(e.target)}</td><td>{obsServicesLabel(e.obs!.services)}</td>
              <td>{(obsStatusLabel[e.obs!.worst] ?? "").replace("Observed · ", "")}</td><td className="text-right tabular-nums">{e.obs!.count.toLocaleString()}</td>
              <td className="text-right"><button type="button" className="text-accent underline" onClick={() => onSelect(e)}>Details</button></td>
            </tr>
          ))}
        </tbody>
      </table>
    </details>
  );
}

export function Topology(p: TopologyProps) {
  return (
    <ReactFlowProvider>
      <TopologyInner {...p} />
    </ReactFlowProvider>
  );
}

export const nodeKindOf = (key: string) => (key === "controller" ? "controller" : parseRef(key).kind);
