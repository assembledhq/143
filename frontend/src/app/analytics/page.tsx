import { BarChart3 } from "lucide-react";
import { PageHeader } from "@/components/page-header";
import { EmptyState } from "@/components/empty-state";

export default function AnalyticsPage() {
  return (
    <div className="space-y-6">
      <PageHeader
        title="Analytics"
        description="Track fix rates, resolution times, and agent performance."
      />
      <EmptyState
        icon={BarChart3}
        title="No data yet"
        description="Analytics will appear here after your first completed run, showing fix rates, resolution times, and success metrics."
      />
    </div>
  );
}
