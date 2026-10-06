import { EmptyState } from "@/components/ui/EmptyState";

export function PlaceholderPage({ title, milestone }: { title: string; milestone: string }) {
  return <EmptyState title={title} body={`Planned for milestone ${milestone}.`} />;
}
