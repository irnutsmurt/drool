import { Link } from "react-router";
import { EmptyState } from "@/components/ui/EmptyState";

export function NotFoundPage() {
  return <EmptyState title="Page not found" action={<Link className="text-accent" to="/">Go to dashboard</Link>} />;
}
