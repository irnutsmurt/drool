import { useAuth } from "@/auth/AuthContext";
import { Card } from "@/components/ui/Card";
import { PageHeader } from "@/components/ui/PageHeader";

export function DashboardPage() {
  const { me } = useAuth();
  return (
    <>
      <PageHeader title="Dashboard" description={`Signed in as ${me?.user.username}.`} />
      <div className="grid gap-4 md:grid-cols-3">
        {["Hosts", "Updates pending", "Recent jobs"].map((t) => (
          <Card key={t} title={t}>
            <p className="text-sm text-muted">Available once hosts and jobs are implemented (M1–M3).</p>
          </Card>
        ))}
      </div>
    </>
  );
}
