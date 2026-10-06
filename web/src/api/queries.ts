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
export const useCredentials = (enabled = true) =>
  useQuery({ queryKey: qk.credentials, queryFn: () => unwrap(api.GET("/secrets")), enabled });
export const useControllerKey = () =>
  useQuery({ queryKey: qk.controllerKey, queryFn: () => unwrap(api.GET("/secrets/controller-ssh-key")) });
