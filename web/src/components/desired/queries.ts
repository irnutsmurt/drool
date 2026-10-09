import { keepPreviousData, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, unwrap, type Schemas } from "@/api/client";

export type DesiredResource = Schemas["DesiredResource"];
export type DesiredAssignment = Schemas["DesiredAssignment"];
export type BaselineInput = Schemas["BaselineInput"];
export type Baseline = Schemas["Baseline"];
export type DesiredSummary = Schemas["DesiredSummary"];
export type DesiredHostRow = Schemas["DesiredHostRow"];
export type DesiredHost = Schemas["DesiredHost"];
export type DesiredResourceView = Schemas["DesiredResourceView"];
export type DesiredHistoryEntry = Schemas["DesiredHistoryEntry"];
export type DesiredSource = Schemas["DesiredSource"];
export type DesiredDiff = Schemas["DesiredDiff"];
export type DesiredPlan = Schemas["DesiredPlan"];
export type DesiredImpact = Schemas["DesiredImpact"];
export type DesiredRestoreRequest = Schemas["DesiredRestoreRequest"];
export type DesiredReview = Schemas["DesiredReview"];
export type DesiredGroupPriority = Schemas["DesiredGroupPriority"];
export type DesiredStatus = Schemas["DesiredStatus"];

export const dsKeys = {
  all: ["desired"] as const,
  summary: ["desired", "summary"] as const,
  hosts: (status: string, q: string, baseline: string) => ["desired", "hosts", status, q, baseline] as const,
  host: (id: string) => ["desired", "host", id] as const,
  resource: (host: string, key: string) => ["desired", "resource", host, key] as const,
  review: (host: string, key: string) => ["desired", "review", host, key] as const,
  history: (filter: Record<string, string>) => ["desired", "history", filter] as const,
  baselines: (archived: boolean) => ["desired", "baselines", archived] as const,
  baseline: (id: string) => ["desired", "baseline", id] as const,
  revision: (id: string, rev: number) => ["desired", "baseline", id, "rev", rev] as const,
  groups: ["desired", "groups"] as const,
};

/** Compliance changes as hosts report: lists refresh while shown. */
const live = 15_000;

export const useDesiredSummary = (enabled = true) =>
  useQuery({ queryKey: dsKeys.summary, queryFn: () => unwrap(api.GET("/desired/summary")), refetchInterval: live, enabled });

export const useDesiredHosts = (status: string, q: string, baseline = "") =>
  useQuery({
    queryKey: dsKeys.hosts(status, q, baseline),
    queryFn: () => unwrap(api.GET("/desired/hosts", { params: { query: { status: (status || undefined) as never, q: q || undefined, baseline: baseline || undefined } } })),
    refetchInterval: live,
    placeholderData: keepPreviousData,
  });

export const useDesiredHost = (id: string, enabled = true) =>
  useQuery({
    queryKey: dsKeys.host(id),
    queryFn: () => unwrap(api.GET("/desired/hosts/{id}", { params: { path: { id } } })),
    refetchInterval: (q) => (q.state.data?.counts.reconciling ? 3000 : live),
    enabled,
  });

export const useDesiredResource = (host: string, key: string | undefined) =>
  useQuery({
    queryKey: dsKeys.resource(host, key ?? ""),
    queryFn: () => unwrap(api.GET("/desired/hosts/{id}/resource", { params: { path: { id: host }, query: { key: key! } } })),
    enabled: !!key,
    refetchInterval: (q) => (q.state.data?.resource.status === "reconciling" ? 3000 : live),
  });

export const useDesiredReview = (host: string, key: string | undefined) =>
  useQuery({
    queryKey: dsKeys.review(host, key ?? ""),
    queryFn: () => unwrap(api.GET("/desired/hosts/{id}/review", { params: { path: { id: host }, query: { key: key! } } })),
    enabled: !!key,
    staleTime: 0,
  });

export const useDesiredHistory = (filter: { host?: string; key?: string; baseline?: string; kind?: string }) => {
  const f: Record<string, string> = {};
  for (const [k, v] of Object.entries(filter)) if (v) f[k] = v;
  return useQuery({
    queryKey: dsKeys.history(f),
    queryFn: () => unwrap(api.GET("/desired/history", { params: { query: { ...f, limit: 200 } } })),
    refetchInterval: live,
  });
};

export const useBaselines = (archived = false) =>
  useQuery({ queryKey: dsKeys.baselines(archived), queryFn: () => unwrap(api.GET("/desired/baselines", { params: { query: { archived } } })), refetchInterval: live });

export const useBaseline = (id: string | undefined) =>
  useQuery({
    queryKey: dsKeys.baseline(id ?? ""),
    queryFn: () => unwrap(api.GET("/desired/baselines/{id}", { params: { path: { id: id! } } })),
    enabled: !!id,
    refetchInterval: live,
  });

export const useBaselineRevision = (id: string, rev: number | undefined) =>
  useQuery({
    queryKey: dsKeys.revision(id, rev ?? 0),
    queryFn: () => unwrap(api.GET("/desired/baselines/{id}/revisions/{rev}", { params: { path: { id, rev: rev! } } })),
    enabled: !!rev,
    staleTime: Infinity,
  });

export const useDesiredGroups = (enabled = true) =>
  useQuery({ queryKey: dsKeys.groups, queryFn: () => unwrap(api.GET("/desired/groups")), enabled });

/** Refreshes everything desired state shows (after an action). */
export function useInvalidateDesired() {
  const qc = useQueryClient();
  return () => {
    void qc.invalidateQueries({ queryKey: dsKeys.all });
    void qc.invalidateQueries({ queryKey: ["jobs"] });
  };
}

/** "service:jellyfin.service" → kind and name. */
export function splitKey(key: string): { kind: string; name: string } {
  const i = key.indexOf(":");
  return { kind: key.slice(0, i), name: key.slice(i + 1) };
}

/** A resource as people say it: "jellyfin.service", "package telnet", "/etc/caddy/Caddyfile". */
export function describeKey(key: string): string {
  const { kind, name } = splitKey(key);
  return kind === "package" ? `package ${name}` : name;
}

/** What a declaration asks for, in words. */
export function wantText(r: DesiredResource): string {
  if (r.kind === "service") {
    const parts: string[] = [];
    if (r.state) parts.push(r.state);
    if (r.enabled != null) parts.push(r.enabled ? "enabled at boot" : "disabled at boot");
    return parts.join(", ");
  }
  if (r.kind === "package") return r.state ?? "";
  const parts = [`managed content${r.artifact_name ? ` (${r.artifact_name})` : ""}`];
  if (r.owner || r.group) parts.push(`owner ${r.owner || "*"}:${r.group || "*"}`);
  if (r.mode) parts.push(`mode ${r.mode}`);
  return parts.join(", ");
}

export function resourceKey(r: DesiredResource): string {
  if (r.kind === "file") return `file:${r.path ?? ""}`;
  const name = r.kind === "service" && r.name && !r.name.includes(".") ? `${r.name}.service` : r.name ?? "";
  return `${r.kind}:${name}`;
}

/** A short SHA-256 for people. */
export const shortSha = (s?: string) => (s ? s.replace(/^sha256 /, "").slice(0, 12) + "…" : "—");
