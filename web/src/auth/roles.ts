import type { Schemas } from "@/api/client";

/** Each role in words: its name and what it may do (Users and Roles in the
 * user guide has the full list). */
export const roleInfo: Record<Schemas["Role"], { name: string; can: string }> = {
  admin: { name: "Administrator", can: "Everything: hosts and their trust, credentials, users, settings, the firewall, notification destinations and baselines." },
  operator: { name: "Operator", can: "Day-to-day work: runs, updates, services and reboots, files, schedules and workflows, alerts and maintenance, restoring Desired State. Not adding hosts, credentials, users, settings or the firewall." },
  auditor: { name: "Auditor", can: "Sees everything, including the audit log, settings and network activity. Changes nothing." },
  viewer: { name: "Viewer", can: "Sees hosts, runs, monitoring, alerts, the firewall policy, schedules, workflows and Desired State. Not the audit log, settings or network activity. Changes nothing." },
};
