import { keepPreviousData, useInfiniteQuery, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, unwrap, type Schemas } from "@/api/client";

export type Operation = Schemas["Operation"];
export type Strategy = Schemas["Strategy"];
export type Workflow = Schemas["Workflow"];
export type WorkflowInput = Schemas["WorkflowInput"];
export type WorkflowRevision = Schemas["WorkflowRevision"];
export type WorkflowTemplate = Schemas["WorkflowTemplate"];
export type Recurrence = Schemas["Recurrence"];
export type ScheduleConfig = Schemas["ScheduleConfig"];
export type ScheduleInput = Schemas["ScheduleInput"];
export type Schedule = Schemas["Schedule"];
export type Occurrence = Schemas["Occurrence"];
export type SchedulePreview = Schemas["SchedulePreview"];
export type NextRun = Schemas["NextRun"];
export type RolloutWave = Schemas["RolloutWave"];

export const autoKeys = {
  workflows: (archived = false) => ["workflows", "list", archived] as const,
  workflow: (id: string) => ["workflows", "detail", id] as const,
  revision: (id: string, rev: number) => ["workflows", "detail", id, rev] as const,
  templates: ["workflows", "templates"] as const,
  schedules: (kind?: "job" | "maintenance", archived = false) => ["schedules", "list", kind ?? "", archived] as const,
  schedule: (id: string) => ["schedules", "detail", id] as const,
  occurrences: (id: string) => ["schedules", "occurrences", id] as const,
};

export function useWorkflows(archived = false, enabled = true) {
  return useQuery({
    queryKey: autoKeys.workflows(archived),
    queryFn: () => unwrap(api.GET("/workflows", { params: { query: { archived } } })),
    enabled,
  });
}

export function useWorkflow(id: string | undefined) {
  return useQuery({
    queryKey: autoKeys.workflow(id ?? ""),
    queryFn: () => unwrap(api.GET("/workflows/{id}", { params: { path: { id: id! } } })),
    enabled: !!id,
  });
}

export function useWorkflowRevision(id: string | undefined, rev: number | undefined) {
  return useQuery({
    queryKey: autoKeys.revision(id ?? "", rev ?? 0),
    queryFn: () => unwrap(api.GET("/workflows/{id}/revisions/{rev}", { params: { path: { id: id!, rev: rev! } } })),
    enabled: !!id && !!rev,
  });
}

export function useTemplates() {
  return useQuery({ queryKey: autoKeys.templates, queryFn: () => unwrap(api.GET("/workflows/templates")), staleTime: Infinity });
}

/** Schedules refresh while shown: next runs move and runs finish. */
export function useSchedules(kind?: "job" | "maintenance", archived = false, enabled = true) {
  return useQuery({
    queryKey: autoKeys.schedules(kind, archived),
    queryFn: () => unwrap(api.GET("/schedules", { params: { query: { kind, archived } } })),
    enabled,
    refetchInterval: 15_000,
    placeholderData: keepPreviousData,
  });
}

export function useSchedule(id: string | undefined) {
  return useQuery({
    queryKey: autoKeys.schedule(id ?? ""),
    queryFn: () => unwrap(api.GET("/schedules/{id}", { params: { path: { id: id! } } })),
    enabled: !!id,
    refetchInterval: 10_000,
  });
}

export function useOccurrences(id: string) {
  return useInfiniteQuery({
    queryKey: autoKeys.occurrences(id),
    initialPageParam: { before: 0, before_id: "" },
    queryFn: ({ pageParam }) => unwrap(api.GET("/schedules/{id}/occurrences", {
      params: { path: { id }, query: { limit: 25, before: pageParam.before || undefined, before_id: pageParam.before_id || undefined } },
    })),
    getNextPageParam: (last) => {
      const o = last.items[last.items.length - 1];
      return last.items.length === 25 && o ? { before: o.scheduled_for, before_id: o.id } : undefined;
    },
    refetchInterval: 10_000,
  });
}

/** Invalidates everything scheduling shows (schedules, workflows, jobs). */
export function useInvalidateAutomation() {
  const qc = useQueryClient();
  return () => {
    void qc.invalidateQueries({ queryKey: ["schedules"] });
    void qc.invalidateQueries({ queryKey: ["workflows"] });
    void qc.invalidateQueries({ queryKey: ["jobs"] });
    void qc.invalidateQueries({ queryKey: ["alerts", "maintenance"] });
  };
}

export function useScheduleAction(id: string) {
  const inv = useInvalidateAutomation();
  return useMutation({
    mutationFn: async (a: "enable" | "disable" | "run-now" | "archive") => {
      const path = { params: { path: { id } } };
      switch (a) {
        case "enable":
          await unwrap(api.POST("/schedules/{id}/enable", path));
          return undefined;
        case "disable":
          await unwrap(api.POST("/schedules/{id}/disable", path));
          return undefined;
        case "archive":
          await unwrap(api.DELETE("/schedules/{id}", path));
          return undefined;
        case "run-now":
          return unwrap(api.POST("/schedules/{id}/run-now", path));
      }
    },
    onSuccess: inv,
  });
}

/** "a few seconds" style text for a duration in minutes. */
export function minutesText(m: number): string {
  if (m % 1440 === 0) return `${m / 1440} day${m === 1440 ? "" : "s"}`;
  if (m % 60 === 0) return `${m / 60} hour${m === 60 ? "" : "s"}`;
  if (m > 60) return `${Math.floor(m / 60)} h ${m % 60} min`;
  return `${m} min`;
}
