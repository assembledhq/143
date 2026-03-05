import { DollarSign } from "lucide-react";
import { PageHeader } from "@/components/page-header";
import { EmptyState } from "@/components/empty-state";
import { PageContainer } from "@/components/page-container";

export default function CostsPage() {
  return (
    <PageContainer size="default">
      <div className="space-y-6">
        <PageHeader
          title="Costs"
          description="Monitor LLM token usage and compute spend per run."
        />
        <EmptyState
          icon={DollarSign}
          title="No cost data yet"
          description="Cost breakdowns appear after your first run, showing token usage, compute time, and total spend per fix."
        />
      </div>
    </PageContainer>
  );
}
