import type { Host } from "@/api/queries";

/** What a host status means and what to do next; null when nothing is needed. */
export function statusHelp(h: Pick<Host, "status" | "status_detail" | "jump_only">): { tone: "warn" | "danger"; text: string } | null {
  const detail = h.status_detail ? ` (${h.status_detail})` : "";
  switch (h.status) {
    case "pending_hostkey":
      return { tone: "warn", text: "DROOL won't connect until you approve this host's SSH key. Compare the fingerprint below with the one on the host itself (ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub), then approve it." };
    case "untested":
      return { tone: "warn", text: "Run \"Test connection\" to check SSH access and sudo before using this host." };
    case "unreachable":
      return { tone: "danger", text: `The last attempt couldn't connect${detail}. DROOL keeps checking in the background; the status changes by itself once the host answers.` };
    case "hostkey_mismatch":
      return { tone: "danger", text: "The host presented a different SSH key than the one you approved, so DROOL is not connecting to it. If the host was reinstalled or its keys were rotated, check the new fingerprint on the host and approve it below. Otherwise treat this as a possible attack." };
    case "auth_failed":
      return { tone: "danger", text: `DROOL reached the host but was not let in, or sudo was refused${detail}. Check the user, credential and sudo settings (Edit), then test the connection again.` };
    case "unsupported":
      return {
        tone: "warn",
        text: /systemd/.test(h.status_detail ?? "")
          ? "systemd is not running on this host. Commands, scripts and package operations work; service management and reboots don't."
          : `DROOL connected, but the host isn't supported${detail}.`,
      };
  }
  return null;
}
