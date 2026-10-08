import { keepPreviousData, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, unwrap, type Schemas } from "@/api/client";

export type MonHostOverview = Schemas["MonHostOverview"];
export type MonState = Schemas["MonState"];
export type MonTile = Schemas["MonTile"];
export type MonIssue = Schemas["MonIssue"];
export type MonHistory = Schemas["MonHistory"];
export type MonSeries = Schemas["MonSeries"];
export type MonService = Schemas["MonService"];
export type MonServicesView = Schemas["MonServicesView"];
export type MonServiceEvent = Schemas["MonServiceEvent"];
export type MonStorageView = Schemas["MonStorageView"];
export type MonDevice = Schemas["MonDevice"];
export type MonSensor = Schemas["MonSensor"];
export type MonGPU = Schemas["MonGPU"];
export type MonFleet = Schemas["MonFleet"];
export type MonFleetHost = Schemas["MonFleetHost"];
export type MonSettings = Schemas["MonSettings"];
export type MonSettingsView = Schemas["MonSettingsView"];
export type MonThresholds = Schemas["MonThresholds"];
export type MonHostSettings = Schemas["MonHostSettings"];
export type MonHelperPreview = Schemas["MonHelperPreview"];
export type MonHelperStatus = Schemas["MonHelperStatus"];
export type MonApproval = Schemas["MonApproval"];
export type MonEvent = Schemas["MonEvent"];
export type MonProbe = Schemas["MonProbe"];
export type MonLimits = Schemas["MonLimits"];
export type ConversionPreview = Schemas["ConversionPreview"];

export const monKeys = {
  host: (id: string) => ["monitoring", "host", id] as const,
  history: (id: string, range: string, series: string[]) => ["monitoring", "history", id, range, ...series] as const,
  services: (id: string, filter: string, all: boolean) => ["monitoring", "services", id, filter, all] as const,
  serviceHistory: (id: string, unit: string) => ["monitoring", "service-history", id, unit] as const,
  storage: (id: string) => ["monitoring", "storage", id] as const,
  events: (id: string, kind: string) => ["monitoring", "events", id, kind] as const,
  fleet: (params: Record<string, string | number | undefined>) => ["monitoring", "fleet", params] as const,
  settings: ["monitoring", "settings"] as const,
  approvals: ["monitoring", "approvals"] as const,
  diagnostics: (id: string) => ["monitoring", "diagnostics", id] as const,
};

const live = { refetchInterval: 30_000, placeholderData: keepPreviousData } as const;

export function useHostMonitoring(id: string, enabled = true) {
  return useQuery({
    queryKey: monKeys.host(id),
    queryFn: () => unwrap(api.GET("/hosts/{id}/monitoring", { params: { path: { id } } })),
    enabled,
    ...live,
  });
}

export function useHistory(id: string, range: string, series: string[]) {
  return useQuery({
    queryKey: monKeys.history(id, range, series),
    queryFn: () => unwrap(api.GET("/hosts/{id}/monitoring/history", {
      params: { path: { id }, query: { range: range as "1h", series } },
    })),
    ...live,
    refetchInterval: range === "1h" || range === "6h" ? 60_000 : 300_000,
  });
}

export function useServices(id: string, filter: string, all: boolean) {
  return useQuery({
    queryKey: monKeys.services(id, filter, all),
    queryFn: () => unwrap(api.GET("/hosts/{id}/monitoring/services", {
      params: { path: { id }, query: { filter: filter as "all", all } },
    })),
    ...live,
  });
}

export function useServiceHistory(id: string, unit: string) {
  return useQuery({
    queryKey: monKeys.serviceHistory(id, unit),
    queryFn: () => unwrap(api.GET("/hosts/{id}/monitoring/services/{unit}/history", { params: { path: { id, unit }, query: { limit: 50 } } })),
    enabled: unit !== "",
  });
}

export function useStorage(id: string) {
  return useQuery({
    queryKey: monKeys.storage(id),
    queryFn: () => unwrap(api.GET("/hosts/{id}/monitoring/storage", { params: { path: { id } } })),
    ...live,
  });
}

export function useMonEvents(id: string, kind = "") {
  return useQuery({
    queryKey: monKeys.events(id, kind),
    queryFn: () => unwrap(api.GET("/hosts/{id}/monitoring/events", {
      params: { path: { id }, query: { limit: 50, ...(kind ? { kind: kind as "kernel" } : {}) } },
    })),
    ...live,
  });
}

export function useFleet(params: { category?: string; q?: string; sort?: string; limit?: number; offset?: number }) {
  return useQuery({
    queryKey: monKeys.fleet(params),
    queryFn: () => unwrap(api.GET("/monitoring/summary", { params: { query: params as never } })),
    ...live,
  });
}

export function useMonSettings() {
  return useQuery({ queryKey: monKeys.settings, queryFn: () => unwrap(api.GET("/monitoring/settings")) });
}

export function useApprovals(enabled = true) {
  return useQuery({ queryKey: monKeys.approvals, queryFn: () => unwrap(api.GET("/monitoring/approvals")), enabled });
}

export function useDiagnostics(id: string, enabled: boolean) {
  return useQuery({
    queryKey: monKeys.diagnostics(id),
    queryFn: () => unwrap(api.GET("/hosts/{id}/monitoring/diagnostics", { params: { path: { id } } })),
    enabled,
    refetchInterval: 3_000,
  });
}

/** Invalidates everything monitoring shows (after a change). */
export function useMonInvalidate() {
  const qc = useQueryClient();
  return () => void qc.invalidateQueries({ queryKey: ["monitoring"] });
}

export function useSaveHostSettings(id: string) {
  const inv = useMonInvalidate();
  return useMutation({
    mutationFn: (body: MonHostSettings) => unwrap(api.PUT("/hosts/{id}/monitoring/settings", { params: { path: { id } }, body })),
    onSuccess: () => inv(),
  });
}

export function useRefreshHost(id: string) {
  const inv = useMonInvalidate();
  return useMutation({
    mutationFn: () => unwrap(api.POST("/hosts/{id}/monitoring/refresh", { params: { path: { id } } })),
    onSuccess: () => setTimeout(inv, 1500),
  });
}
