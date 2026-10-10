import type { ReactNode } from "react";
import { Link, Navigate, useLocation } from "react-router";
import { useAuth } from "./AuthContext";
import { EmptyState } from "@/components/ui/EmptyState";
import { plainLink } from "@/components/ui/links";

export function RequireAuth({ children }: { children: ReactNode }) {
  const { me, loading } = useAuth();
  const location = useLocation();
  if (loading) return <div className="p-8 text-muted">Loading…</div>;
  if (!me) return <Navigate to="/login" replace state={{ from: location.pathname }} />;
  return children;
}

/** A page the user's role can't view: hidden from the sidebar and search;
 * reached by a link or typed address, it says so plainly (403). */
export function RequirePermission({ permission, children }: { permission: string; children: ReactNode }) {
  const { can, me } = useAuth();
  if (!can(permission)) {
    return (
      <EmptyState title="Not permitted"
        body={<>Your role ({me?.user.role}) can't view this page: it needs the <code className="font-mono text-xs">{permission}</code> permission.
          An administrator can change your role under Settings → Users.</>}
        action={<Link className={plainLink} to="/">Go to the Overview</Link>} />
    );
  }
  return children;
}
