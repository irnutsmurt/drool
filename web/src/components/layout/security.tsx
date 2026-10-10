import type { ReactNode } from "react";
import { Link } from "react-router";
import { useQuery } from "@tanstack/react-query";
import { api, unwrap } from "@/api/client";
import { useAuth } from "@/auth/AuthContext";
import { usePKI } from "@/components/agents/queries";
import { plainLink } from "@/components/ui/links";

export function useSystemInfo() {
  return useQuery({ queryKey: ["system", "info"], queryFn: () => unwrap(api.GET("/system/info")), staleTime: 5 * 60_000 });
}

export interface SecurityItem {
  id: string;
  tone: "danger" | "warn";
  title: string;
  body: ReactNode;
}

/** Unresolved security posture the controller reports: shown in full on
 * the Overview and behind the header's security indicator on every page
 * (never dismissed: it stays until the cause is fixed). */
export function useSecurityItems(): SecurityItem[] {
  const { can } = useAuth();
  const info = useSystemInfo();
  const pki = usePKI(can("agents.read"));
  const items: SecurityItem[] = [];
  if (info.data?.insecure_plaintext) {
    items.push({
      id: "plaintext",
      tone: "danger",
      title: "Plaintext HTTP on a network address",
      body: "This controller serves plaintext HTTP on a network address. Credentials and sessions are exposed to anyone on the path.",
    });
  }
  if (pki.data?.root_key_online) {
    items.push({
      id: "root-key",
      tone: "warn",
      title: "Agent Root CA private key is on this controller",
      body: (
        <>
          The private key of the Agent Root CA, which Shepherds (DROOL agents) trust, is stored on this controller. Export it to offline
          storage and remove it{can("agents.pki")
            ? <> — <Link className={plainLink} to="/agents?tab=trust">Shepherds → Trust (PKI)</Link>.</>
            : <>. An administrator can do this under Shepherds → Trust (PKI).</>}
        </>
      ),
    });
  }
  return items;
}
