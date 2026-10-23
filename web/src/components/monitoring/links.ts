/** Where a health issue's subject is shown on its host, and the link's
 * words: a service among the host's services, CPU, memory, disk space and
 * interfaces with their charts, storage health and sensors on their tabs,
 * how the host is monitored for collection notices, else the summary. */
export function issueLink(i: { host_id: string; check: string; subject?: string }): { to: string; label: string } {
  const base = `/hosts/${i.host_id}/monitoring`;
  if (i.check.startsWith("service_") && i.subject) return { to: `${base}?tab=services&unit=${encodeURIComponent(i.subject)}`, label: `View ${i.subject}` };
  const tab = ({ cpu: "metrics", memory: "metrics", filesystem: "metrics", inodes: "metrics", interface_down: "metrics", temperature: "hardware",
    gpu_utilization: "hardware", storage_health: "storage", media_errors: "storage", kernel_storage: "storage", device_readonly: "storage",
    filesystem_readonly: "storage", probe: "storage", fidelity: "agent" } as Record<string, string>)[i.check];
  const label = ({ metrics: "View metrics", hardware: "View sensors and GPU", storage: "View storage health", agent: "View collection" } as Record<string, string>)[tab ?? ""];
  return tab ? { to: `${base}?tab=${tab}`, label: label! } : { to: base, label: "View monitoring" };
}
