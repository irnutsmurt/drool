import { keepPreviousData, useQuery } from "@tanstack/react-query";
import { api, unwrap, type Schemas } from "@/api/client";

export type NetFlow = Schemas["NetFlow"];
export type NetFlowPage = Schemas["NetFlowPage"];
export type NetFlowDetail = Schemas["NetFlowDetail"];
export type NetHost = Schemas["NetHost"];
export type NetSummary = Schemas["NetSummary"];
export type NetFacets = Schemas["NetFacets"];
export type NetTopology = Schemas["NetTopology"];
export type NetTopoEdge = Schemas["NetTopoEdge"];
export type NetTopoNode = Schemas["NetTopoNode"];
export type NetSettings = Schemas["NetSettings"];
export type NetSettingsView = Schemas["NetSettingsView"];
export type NetEnrichment = Schemas["NetEnrichment"];
export type NetCollection = Schemas["NetCollection"];
export type NetRule = Schemas["NetRule"];
export type NetRuleInput = Schemas["NetRuleInput"];

/** The filters the API understands (all optional). The browser only
 * passes them along: filtering and paging happen on the server. */
export type NetFilter = {
  host?: string[];
  group?: string;
  direction?: "in" | "out";
  scope?: "internal" | "external" | "special";
  protocol?: "tcp" | "udp";
  port?: number;
  local_port?: number;
  remote_port?: number;
  process?: string;
  service?: string;
  country?: string;
  asn?: string;
  org?: string;
  peer?: string;
  policy?: "management" | "covered" | "unclassified" | "denied";
  class?: "drool" | "approved" | "user" | "service" | "unknown" | "classified";
  source?: "conntrack" | "sockets" | "snapshot";
  first_seen_after?: string;
  last_seen_after?: string;
  new?: "host" | "fleet";
  since?: string;
  q?: string;
  sort?: "last_seen" | "first_seen" | "count";
  limit?: number;
  cursor?: string;
};

export const filterKeys = [
  "host", "group", "direction", "scope", "protocol", "port", "local_port", "remote_port", "process", "service", "country", "asn", "org",
  "peer", "policy", "class", "source", "first_seen_after", "last_seen_after", "new", "since", "q", "sort",
] as const;

/** Reads filters from URL search params (so views can be linked). */
export function filterFromParams(p: URLSearchParams): NetFilter {
  const f: Record<string, unknown> = {};
  for (const k of filterKeys) {
    if (k === "host") {
      const hs = p.getAll("host");
      if (hs.length) f.host = hs;
      continue;
    }
    const v = p.get(k);
    if (v === null || v === "") continue;
    f[k] = ["port", "local_port", "remote_port"].includes(k) ? Number(v) : v;
  }
  return f as NetFilter;
}

export function filterToParams(f: NetFilter, base = new URLSearchParams()): URLSearchParams {
  const p = new URLSearchParams(base);
  for (const k of filterKeys) p.delete(k);
  for (const [k, v] of Object.entries(f)) {
    if (v === undefined || v === "" || k === "cursor" || k === "limit") continue;
    if (Array.isArray(v)) v.forEach((x) => p.append(k, x));
    else p.set(k, String(v));
  }
  return p;
}

export const nk = {
  all: ["network"] as const,
  flows: (f: NetFilter) => ["network", "flows", f] as const,
  flow: (id: number) => ["network", "flow", id] as const,
  summary: (f: NetFilter) => ["network", "summary", f] as const,
  facets: (hosts?: string[]) => ["network", "facets", hosts ?? []] as const,
  host: (id: string) => ["network", "host", id] as const,
  topology: (q: Record<string, unknown>) => ["network", "topology", q] as const,
  settings: ["network", "settings"] as const,
  rules: ["network", "classifications"] as const,
};

export const useNetFlows = (f: NetFilter, enabled = true) =>
  useQuery({
    queryKey: nk.flows(f),
    queryFn: () => unwrap(api.GET("/network/flows", { params: { query: f } })),
    placeholderData: keepPreviousData,
    refetchInterval: 30_000,
    enabled,
  });

export const useNetFlow = (id: number | undefined) =>
  useQuery({
    queryKey: nk.flow(id ?? 0),
    queryFn: () => unwrap(api.GET("/network/flows/{id}", { params: { path: { id: id! } } })),
    enabled: id !== undefined,
  });

export const useNetSummary = (f: NetFilter) =>
  useQuery({ queryKey: nk.summary(f), queryFn: () => unwrap(api.GET("/network/summary", { params: { query: f } })), refetchInterval: 30_000 });

export const useNetFacets = (hosts?: string[]) =>
  useQuery({ queryKey: nk.facets(hosts), queryFn: () => unwrap(api.GET("/network/filters", { params: { query: { host: hosts } } })), staleTime: 30_000 });

export const useHostNetwork = (id: string, enabled = true) =>
  useQuery({
    queryKey: nk.host(id),
    queryFn: () => unwrap(api.GET("/hosts/{id}/network", { params: { path: { id } } })),
    refetchInterval: 30_000,
    enabled,
  });

export type TopoQuery = { expand?: string[]; max_external?: number; host?: string[]; scope?: "internal" | "external"; policy?: NetFilter["policy"]; last_seen_after?: string; q?: string };

export const useNetTopology = (q: TopoQuery, enabled: boolean) =>
  useQuery({
    queryKey: nk.topology(q),
    queryFn: () => unwrap(api.GET("/network/topology", { params: { query: q } })),
    placeholderData: keepPreviousData,
    refetchInterval: 60_000,
    enabled,
  });

export const useNetRules = (enabled = true) =>
  useQuery({ queryKey: nk.rules, queryFn: () => unwrap(api.GET("/network/classifications")), enabled });

export const useNetSettings = (enabled = true) =>
  useQuery({ queryKey: nk.settings, queryFn: () => unwrap(api.GET("/network/settings")), enabled });
