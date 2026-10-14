import { useQuery } from "@tanstack/react-query";
import { api, unwrap, type Schemas } from "./client";

export type Host = Schemas["Host"];
export type HostKey = Schemas["HostKey"];
export type Group = Schemas["Group"];
export type Credential = Schemas["Credential"];
export type ProbeResult = Schemas["ProbeResult"];

export const qk = {
  hosts: ["hosts"] as const,
  host: (id: string) => ["hosts", id] as const,
  hostKeys: (id: string) => ["hosts", id, "keys"] as const,
  groups: ["groups"] as const,
  credentials: ["credentials"] as const,
  controllerKey: ["credentials", "controller"] as const,
};

export const useHosts = () => useQuery({ queryKey: qk.hosts, queryFn: () => unwrap(api.GET("/hosts")) });
export const useHost = (id: string) =>
  useQuery({ queryKey: qk.host(id), queryFn: () => unwrap(api.GET("/hosts/{id}", { params: { path: { id } } })) });
export const useHostKeys = (id: string) =>
  useQuery({
    queryKey: qk.hostKeys(id),
    queryFn: () => unwrap(api.GET("/hosts/{id}/hostkeys", { params: { path: { id } } })),
  });
export const useGroups = () => useQuery({ queryKey: qk.groups, queryFn: () => unwrap(api.GET("/groups")) });
export const useGroup = (id: string) =>
  useQuery({ queryKey: [...qk.groups, id], queryFn: () => unwrap(api.GET("/groups/{id}", { params: { path: { id } } })) });
export const useCredentials = (enabled = true) =>
  useQuery({ queryKey: qk.credentials, queryFn: () => unwrap(api.GET("/secrets")), enabled });
export const useControllerKey = () =>
  useQuery({ queryKey: qk.controllerKey, queryFn: () => unwrap(api.GET("/secrets/controller-ssh-key")) });

export type Job = Schemas["Job"];
export type JobHost = Schemas["JobHost"];
export type JobHostDetail = Schemas["JobHostDetail"];
export type JobSpec = Schemas["JobSpec"];

export const jk = {
  jobs: ["jobs"] as const,
  job: (id: string) => ["jobs", id] as const,
  jobHosts: (id: string) => ["jobs", id, "hosts"] as const,
  jobHost: (id: string, host: string) => ["jobs", id, "hosts", host] as const,
};

const active = (status?: string) => status === "pending" || status === "running" || status === "paused";

export const useJobs = (host?: string) =>
  useQuery({
    queryKey: host ? [...jk.jobs, { host }] : jk.jobs,
    queryFn: () => unwrap(api.GET("/jobs", { params: { query: { host, limit: host ? 10 : 100 } } })),
    // Keep the list fresh while anything is running; SSE drives the detail page.
    refetchInterval: (q) => (q.state.data?.items.some((j) => active(j.status)) ? 3000 : false),
  });
export const useJob = (id: string) =>
  useQuery({ queryKey: jk.job(id), queryFn: () => unwrap(api.GET("/jobs/{id}", { params: { path: { id } } })) });
export const useJobHosts = (id: string) =>
  useQuery({ queryKey: jk.jobHosts(id), queryFn: () => unwrap(api.GET("/jobs/{id}/hosts", { params: { path: { id } } })) });
export const useJobHost = (id: string, host: string) =>
  useQuery({
    queryKey: jk.jobHost(id, host),
    queryFn: () => unwrap(api.GET("/jobs/{id}/hosts/{hostId}", { params: { path: { id, hostId: host } } })),
  });

export type HostFacts = Schemas["HostFacts"];
export const useHostFacts = (id: string) =>
  useQuery({ queryKey: [...qk.host(id), "facts"], queryFn: () => unwrap(api.GET("/hosts/{id}/facts", { params: { path: { id } } })) });

/** Shape of facts_json (internal/discovery.Facts). */
export interface DiscoveredFacts {
  cpu_model?: string;
  swap_bytes?: number;
  virtualization?: string;
  systemd_running: boolean;
  filesystems: { mount: string; device: string; type?: string; size_bytes: number; used_bytes: number; avail_bytes: number; used_pct: number }[];
  interfaces: { name: string; mac?: string; state?: string; mtu?: number; addresses: { address: string; prefix: number; family: number; scope?: string }[] }[];
  services: { name: string; description?: string }[];
  failed_units: string[];
  ports: { proto: string; address: string; port: number; process?: string }[];
  warnings: string[];
}
