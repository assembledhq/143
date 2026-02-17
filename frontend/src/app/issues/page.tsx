import { AlertCircle } from "lucide-react";
import { PageHeader } from "@/components/page-header";
import { EmptyState } from "@/components/empty-state";

export default function IssuesPage() {
  return (
    <div className="space-y-6">
      <PageHeader
        title="Issues"
        description="Issues from your connected trackers appear here."
      />
      <EmptyState
        icon={AlertCircle}
        title="No issues yet"
        description="Connect Sentry, Linear, or another issue tracker to start pulling in issues automatically."
        action={{ label: "Go to Settings", href: "/settings" }}
      />
    </div>
  );
}
