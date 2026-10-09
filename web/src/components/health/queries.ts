import { keepPreviousData, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, unwrap, type Schemas } from "@/api/client";

// Health overrides (M12.1): what monitoring considers unhealthy, at global,
// group, host and subject scope. Every value and its source come from the
// server; the browser never resolves inheritance itself.

export type HealthView = Schemas["HealthView"];
export type HealthCheck = Schemas["HealthEffectiveCheck"];
export type HealthField = Schemas["HealthEffectiveField"];
export type HealthSubject = Schemas["HealthSubject"];
export type HealthOverride = Schemas["HealthOverride"];
export type HealthPreview = Schemas["HealthPreview"];
export type HealthFields = Schemas["HealthFields"];
export type HealthGroup = Schemas["HealthGroup"];
export type HealthScope = "global" | "group" | "host";

export const healthKeys = {
  all: ["health"] as const,
  global: ["health", "global"] as const,
  groups: ["health", "groups"] as const,
  group: (id: string) => ["health", "group", id] as const,
  host: (id: string, subject = "") => ["health", "host", id, subject] as const,
  overrides: (f: Record<string, string>) => ["health", "overrides", f] as const,
};

export function useHealthGlobal(enabled = true) {
  return useQuery({ queryKey: healthKeys.global, queryFn: () => unwrap(api.GET("/monitoring/health/global")), enabled });
}

export function useHealthGroups(enabled = true) {
  return useQuery({ queryKey: healthKeys.groups, queryFn: () => unwrap(api.GET("/monitoring/health/groups")), enabled });
}

export function useHealthGroup(id: string, enabled = true) {
  return useQuery({ queryKey: healthKeys.group(id), queryFn: () => unwrap(api.GET("/monitoring/health/groups/{id}", { params: { path: { id } } })), enabled });
}

export function useHostHealth(id: string, subject?: string, enabled = true) {
  return useQuery({
    queryKey: healthKeys.host(id, subject),
    queryFn: () => unwrap(api.GET("/hosts/{id}/monitoring/health", { params: { path: { id }, query: { subject: subject || undefined } } })),
    enabled: enabled && !!id,
    placeholderData: keepPreviousData,
  });
}

export function useHealthOverrides(f: { scope?: HealthScope; group_id?: string; host_id?: string } = {}) {
  return useQuery({
    queryKey: healthKeys.overrides(f as Record<string, string>),
    queryFn: () => unwrap(api.GET("/monitoring/health/overrides", { params: { query: f } })),
  });
}

function useInvalidate() {
  const qc = useQueryClient();
  return () => {
    void qc.invalidateQueries({ queryKey: healthKeys.all });
    void qc.invalidateQueries({ queryKey: ["monitoring"] });
    void qc.invalidateQueries({ queryKey: ["alerts"] });
  };
}

export type OverrideKey = { scope: HealthScope; group_id?: string; host_id?: string; check: string; subject?: string };

export function usePutOverride() {
  const inv = useInvalidate();
  return useMutation({
    mutationFn: (b: OverrideKey & { fields: HealthFields; subject_label?: string }) => unwrap(api.PUT("/monitoring/health/overrides", { body: b })),
    onSuccess: inv,
  });
}

export function useDeleteOverride() {
  const inv = useInvalidate();
  return useMutation({
    mutationFn: (id: string) => unwrap(api.DELETE("/monitoring/health/overrides/{id}", { params: { path: { id } } })),
    onSuccess: inv,
  });
}

export function useSetGroupPriority() {
  const inv = useInvalidate();
  return useMutation({
    mutationFn: (v: { id: string; priority: number }) =>
      unwrap(api.PUT("/monitoring/health/groups/{id}/priority", { params: { path: { id: v.id } }, body: { priority: v.priority } })),
    onSuccess: inv,
  });
}

export function usePreview() {
  return useMutation({
    mutationFn: (b: OverrideKey & { fields: HealthFields; priority?: number }) => unwrap(api.POST("/monitoring/health/preview", { body: b })),
  });
}
