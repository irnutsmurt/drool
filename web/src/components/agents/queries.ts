import { useQuery } from "@tanstack/react-query";
import { api, unwrap, type Schemas } from "@/api/client";

export type AgentCaps = Schemas["AgentCaps"];
export type HostAgent = Schemas["HostAgent"];
export type AgentIdentity = Schemas["AgentIdentity"];
export type Management = Schemas["Management"];

export const ak = {
  hostAgent: (id: string) => ["hosts", id, "agent"] as const,
  pki: ["agents", "pki"] as const,
  presets: ["agents", "presets"] as const,
  tokens: ["agents", "tokens"] as const,
  agents: ["agents", "list"] as const,
  releases: ["agents", "releases"] as const,
  releaseKeys: ["agents", "release-keys"] as const,
  rollouts: ["agents", "rollouts"] as const,
  rollout: (id: string) => ["agents", "rollouts", id] as const,
  policy: ["agents", "policy"] as const,
  updateStatus: ["agents", "update-status"] as const,
  endpoints: ["agents", "endpoints"] as const,
};

export const useHostAgent = (id: string) =>
  useQuery({
    queryKey: ak.hostAgent(id),
    queryFn: () => unwrap(api.GET("/hosts/{id}/agent", { params: { path: { id } } })),
    refetchInterval: 5000,
  });
export const usePKI = (enabled = true) =>
  useQuery({ queryKey: ak.pki, queryFn: () => unwrap(api.GET("/agents/pki")), enabled, retry: false, staleTime: 60_000 });
export const usePresets = () =>
  useQuery({ queryKey: ak.presets, queryFn: () => unwrap(api.GET("/agents/presets")), staleTime: Infinity });
export const useEnrollmentTokens = () => useQuery({ queryKey: ak.tokens, queryFn: () => unwrap(api.GET("/agents/tokens")) });
export const useAgents = () =>
  useQuery({ queryKey: ak.agents, queryFn: () => unwrap(api.GET("/agents")), refetchInterval: 5000 });
export const useReleases = () => useQuery({ queryKey: ak.releases, queryFn: () => unwrap(api.GET("/agents/releases")) });
export const useReleaseKeys = () => useQuery({ queryKey: ak.releaseKeys, queryFn: () => unwrap(api.GET("/agents/release-keys")) });
export const useRollouts = () =>
  useQuery({ queryKey: ak.rollouts, queryFn: () => unwrap(api.GET("/agents/rollouts")), refetchInterval: 4000 });
export const useRollout = (id: string) =>
  useQuery({
    queryKey: ak.rollout(id),
    queryFn: () => unwrap(api.GET("/agents/rollouts/{id}", { params: { path: { id } } })),
    refetchInterval: 3000,
  });
export const useUpdatePolicy = () => useQuery({ queryKey: ak.policy, queryFn: () => unwrap(api.GET("/agents/updates/policy")) });
/** The Shepherd update overview; every 2 s while a check runs. */
export const useUpdateStatus = () =>
  useQuery({
    queryKey: ak.updateStatus,
    queryFn: () => unwrap(api.GET("/agents/updates/status")),
    refetchInterval: (q) => (q.state.data?.check.checking ? 2000 : 5000),
  });
export const useEndpointMigrations = () =>
  useQuery({ queryKey: ak.endpoints, queryFn: () => unwrap(api.GET("/agents/endpoints")), refetchInterval: 5000 });
