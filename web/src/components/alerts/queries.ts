import { keepPreviousData, useInfiniteQuery, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, unwrap, type Schemas } from "@/api/client";

export type Alert = Schemas["Alert"];
export type AlertDetail = Schemas["AlertDetail"];
export type AlertSummary = Schemas["AlertSummary"];
export type TimelineEvent = Schemas["TimelineEvent"];
export type Maintenance = Schemas["Maintenance"];
export type MaintenanceInput = Schemas["MaintenanceInput"];
export type Destination = Schemas["Destination"];
export type DestinationInput = Schemas["DestinationInput"];
export type DestinationConfig = Schemas["DestinationConfig"];
export type DestinationSecrets = Schemas["DestinationSecrets"];
export type TestResult = Schemas["TestResult"];
export type Route = Schemas["Route"];
export type RouteInput = Schemas["RouteInput"];
export type Delivery = Schemas["Delivery"];
export type NotificationSettings = Schemas["NotificationSettings"];

export type AlertCategory = "cpu" | "memory" | "disk" | "storage" | "services" | "network" | "temperature" | "gpu" | "stale" | "fidelity";

export type AlertFilter = {
  view?: "active" | "acknowledged" | "open" | "resolved" | "all";
  severity?: "warning" | "critical";
  host_id?: string;
  group_id?: string;
  category?: AlertCategory;
  acknowledged?: boolean;
  suppressed?: boolean;
  since?: number;
  until?: number;
};

export const alertKeys = {
  all: ["alerts"] as const,
  summary: ["alerts", "summary"] as const,
  list: (f: AlertFilter) => ["alerts", "list", f] as const,
  detail: (id: string) => ["alerts", "detail", id] as const,
  events: (host?: string) => ["alerts", "events", host ?? ""] as const,
  maintenance: (state: string, host?: string) => ["alerts", "maintenance", state, host ?? ""] as const,
  destinations: ["notifications", "destinations"] as const,
  routes: ["notifications", "routes"] as const,
  deliveries: (f: Record<string, string | undefined>) => ["notifications", "deliveries", f] as const,
  settings: ["notifications", "settings"] as const,
};

const live = { refetchInterval: 30_000, placeholderData: keepPreviousData } as const;

export function useAlertSummary(enabled = true) {
  return useQuery({ queryKey: alertKeys.summary, queryFn: () => unwrap(api.GET("/alerts/summary")), enabled, ...live });
}

export function useAlerts(f: AlertFilter) {
  return useInfiniteQuery({
    queryKey: alertKeys.list(f),
    initialPageParam: "",
    queryFn: ({ pageParam }) => unwrap(api.GET("/alerts", { params: { query: { ...f, limit: 50, cursor: pageParam || undefined } } })),
    getNextPageParam: (last) => last.next_cursor || undefined,
    refetchInterval: 30_000,
  });
}

export function useAlert(id: string) {
  return useQuery({ queryKey: alertKeys.detail(id), queryFn: () => unwrap(api.GET("/alerts/{id}", { params: { path: { id } } })), ...live });
}

export function useEvents(hostId?: string, limit = 20, enabled = true) {
  return useQuery({
    queryKey: alertKeys.events(hostId),
    queryFn: () => unwrap(api.GET("/events", { params: { query: { host_id: hostId, limit } } })),
    enabled,
    ...live,
  });
}

function useInvalidateAlerts() {
  const qc = useQueryClient();
  return () => {
    void qc.invalidateQueries({ queryKey: alertKeys.all });
    void qc.invalidateQueries({ queryKey: ["monitoring"] });
  };
}

export function useAcknowledge(id: string) {
  const inv = useInvalidateAlerts();
  return useMutation({
    mutationFn: (note: string) => unwrap(api.POST("/alerts/{id}/acknowledge", { params: { path: { id } }, body: { note } })),
    onSuccess: inv,
  });
}

export function useMaintenance(state: "active" | "ended" | "all", hostId?: string, enabled = true) {
  return useQuery({
    queryKey: alertKeys.maintenance(state, hostId),
    queryFn: () => unwrap(api.GET("/maintenance", { params: { query: { state, host_id: hostId, limit: 100 } } })),
    enabled,
    ...live,
  });
}

export function useStartMaintenance() {
  const inv = useInvalidateAlerts();
  return useMutation({ mutationFn: (body: MaintenanceInput) => unwrap(api.POST("/maintenance", { body })), onSuccess: inv });
}

export function useEndMaintenance() {
  const inv = useInvalidateAlerts();
  return useMutation({ mutationFn: (id: string) => unwrap(api.POST("/maintenance/{id}/end", { params: { path: { id } } })), onSuccess: inv });
}

export function useDestinations(enabled = true) {
  return useQuery({ queryKey: alertKeys.destinations, queryFn: () => unwrap(api.GET("/notifications/destinations")), enabled, ...live });
}

function useInvalidateNotifications() {
  const qc = useQueryClient();
  return () => {
    void qc.invalidateQueries({ queryKey: ["notifications"] });
    void qc.invalidateQueries({ queryKey: alertKeys.summary });
  };
}

export function useSaveDestination() {
  const inv = useInvalidateNotifications();
  return useMutation({
    mutationFn: ({ id, body }: { id?: string; body: DestinationInput }) =>
      id ? unwrap(api.PATCH("/notifications/destinations/{id}", { params: { path: { id } }, body }))
        : unwrap(api.POST("/notifications/destinations", { body })),
    onSuccess: inv,
  });
}

export function useDeleteDestination() {
  const inv = useInvalidateNotifications();
  return useMutation({ mutationFn: (id: string) => unwrap(api.DELETE("/notifications/destinations/{id}", { params: { path: { id } } })), onSuccess: inv });
}

export function useTestDestination() {
  const inv = useInvalidateNotifications();
  return useMutation({
    mutationFn: (arg: { id: string } | { kind: string; config: DestinationConfig; secrets?: DestinationSecrets }) =>
      "id" in arg
        ? unwrap(api.POST("/notifications/destinations/{id}/test", { params: { path: { id: arg.id } } }))
        : unwrap(api.POST("/notifications/test", { body: { kind: arg.kind as "email", config: arg.config, secrets: arg.secrets } })),
    onSuccess: inv,
  });
}

export function useRoutes(enabled = true) {
  return useQuery({ queryKey: alertKeys.routes, queryFn: () => unwrap(api.GET("/notifications/routes")), enabled });
}

export function useSaveRoute() {
  const inv = useInvalidateNotifications();
  return useMutation({
    mutationFn: ({ id, body }: { id?: string; body: RouteInput }) =>
      id ? unwrap(api.PATCH("/notifications/routes/{id}", { params: { path: { id } }, body }))
        : unwrap(api.POST("/notifications/routes", { body })),
    onSuccess: inv,
  });
}

export function useDeleteRoute() {
  const inv = useInvalidateNotifications();
  return useMutation({ mutationFn: (id: string) => unwrap(api.DELETE("/notifications/routes/{id}", { params: { path: { id } } })), onSuccess: inv });
}

export function useDeliveries(f: { destination_id?: string; status?: string }, enabled = true) {
  return useInfiniteQuery({
    queryKey: alertKeys.deliveries(f),
    initialPageParam: "",
    queryFn: ({ pageParam }) => unwrap(api.GET("/notifications/deliveries", {
      params: { query: { destination_id: f.destination_id, status: f.status as "queued" | undefined, limit: 50, cursor: pageParam || undefined } },
    })),
    getNextPageParam: (last) => last.next_cursor || undefined,
    enabled,
    refetchInterval: 30_000,
  });
}

export function useNotificationSettings(enabled = true) {
  return useQuery({ queryKey: alertKeys.settings, queryFn: () => unwrap(api.GET("/notifications/settings")), enabled });
}

export function useSaveNotificationSettings() {
  const inv = useInvalidateNotifications();
  return useMutation({ mutationFn: (body: NotificationSettings) => unwrap(api.PUT("/notifications/settings", { body })), onSuccess: inv });
}
