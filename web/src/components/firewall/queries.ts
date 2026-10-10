import { useQuery } from "@tanstack/react-query";
import { api, unwrap, type Schemas } from "@/api/client";

export type FwPolicy = Schemas["FwPolicy"];
export type FwPolicyInput = Schemas["FwPolicyInput"];
export type FwSelector = Schemas["FwSelector"];
export type FwService = Schemas["FwService"];
export type FwPlan = Schemas["FwPlan"];
export type FwHostSummary = Schemas["FwHostSummary"];
export type FwTransaction = Schemas["FwTransaction"];
export type FwAllowance = Schemas["FwAllowance"];
export type FwObservation = Schemas["FwObservation"];
export type FwSuggestion = Schemas["FwSuggestion"];
export type FwSimResult = Schemas["FwSimResult"];

export const fk = {
  all: ["firewall"] as const,
  hosts: ["firewall", "hosts"] as const,
  host: (id: string) => ["firewall", "hosts", id] as const,
  policies: ["firewall", "policies"] as const,
  services: ["firewall", "services"] as const,
  settings: ["firewall", "settings"] as const,
  groups: ["firewall", "groups"] as const,
  txs: (host?: string) => ["firewall", "transactions", host ?? ""] as const,
  observations: (id: string) => ["firewall", "observations", id] as const,
  suggestions: (id: string) => ["firewall", "suggestions", id] as const,
};

export const useFwHosts = (enabled = true) =>
  useQuery({ queryKey: fk.hosts, queryFn: () => unwrap(api.GET("/firewall/hosts")), refetchInterval: 5000, enabled });
export const useFwHost = (id: string, enabled = true) =>
  useQuery({
    queryKey: fk.host(id),
    queryFn: () => unwrap(api.GET("/firewall/hosts/{id}", { params: { path: { id } } })),
    refetchInterval: 4000,
    enabled,
  });
export const useFwPolicies = () => useQuery({ queryKey: fk.policies, queryFn: () => unwrap(api.GET("/firewall/policies")) });
export const useFwServices = () => useQuery({ queryKey: fk.services, queryFn: () => unwrap(api.GET("/firewall/services")), staleTime: 60_000 });
export const useFwSettings = () => useQuery({ queryKey: fk.settings, queryFn: () => unwrap(api.GET("/firewall/settings")) });
export const useFwGroups = () => useQuery({ queryKey: fk.groups, queryFn: () => unwrap(api.GET("/firewall/groups")) });
export const useFwTransactions = (host?: string) =>
  useQuery({
    queryKey: fk.txs(host),
    queryFn: () => unwrap(api.GET("/firewall/transactions", { params: { query: { host_id: host, limit: 50 } } })),
    refetchInterval: 4000,
  });
export const useFwObservations = (id: string, enabled = true) =>
  useQuery({
    queryKey: fk.observations(id),
    queryFn: () => unwrap(api.GET("/firewall/hosts/{id}/observations", { params: { path: { id } } })),
    enabled,
    refetchInterval: 15000,
  });
export const useFwSuggestions = (id: string, enabled = true) =>
  useQuery({
    queryKey: fk.suggestions(id),
    queryFn: () => unwrap(api.GET("/firewall/hosts/{id}/suggestions", { params: { path: { id } } })),
    enabled,
  });
