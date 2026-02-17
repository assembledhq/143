import { Play } from "lucide-react";
import { PageHeader } from "@/components/page-header";
import { EmptyState } from "@/components/empty-state";

export default function RunsPage() {
  return (
    <div className="space-y-6">
      <PageHeader
        title="Runs"
        description="Each agent execution shows up as a run."
      />
      <EmptyState
        icon={Play}
        title="No runs yet"
        description="Runs are created automatically when 143 picks up an issue and starts working on a fix."
      />
    </div>
  );
}
