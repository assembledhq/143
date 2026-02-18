"use client";

import { useQuery } from "@tanstack/react-query";
import { AlertCircle } from "lucide-react";
import { PageHeader } from "@/components/page-header";
import { EmptyState } from "@/components/empty-state";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent } from "@/components/ui/card";
import { api } from "@/lib/api";
import type { Issue } from "@/lib/types";

const severityColors: Record<string, string> = {
  critical: "bg-red-100 text-red-800",
  high: "bg-orange-100 text-orange-800",
  medium: "bg-yellow-100 text-yellow-800",
  low: "bg-gray-100 text-gray-700",
};

const statusColors: Record<string, string> = {
  open: "bg-blue-100 text-blue-800",
  triaged: "bg-purple-100 text-purple-800",
  in_progress: "bg-yellow-100 text-yellow-800",
  fixed: "bg-green-100 text-green-800",
  wont_fix: "bg-gray-100 text-gray-700",
  duplicate: "bg-gray-100 text-gray-700",
};

const sourceLabels: Record<string, string> = {
  sentry: "Sentry",
  linear: "Linear",
  support: "Support",
};

function formatTimeAgo(dateStr: string): string {
  const date = new Date(dateStr);
  const now = new Date();
  const diffMs = now.getTime() - date.getTime();
  const diffMins = Math.floor(diffMs / 60000);
  if (diffMins < 1) return "just now";
  if (diffMins < 60) return `${diffMins}m ago`;
  const diffHours = Math.floor(diffMins / 60);
  if (diffHours < 24) return `${diffHours}h ago`;
  const diffDays = Math.floor(diffHours / 24);
  if (diffDays < 30) return `${diffDays}d ago`;
  return date.toLocaleDateString();
}

function IssueRow({ issue }: { issue: Issue }) {
  return (
    <div className="flex items-center justify-between py-3 px-4 border-b border-border last:border-b-0 hover:bg-muted/50 transition-colors">
      <div className="flex-1 min-w-0">
        <div className="flex items-center gap-2">
          <span className={`inline-flex items-center rounded-full px-2 py-0.5 text-[11px] font-medium ${severityColors[issue.severity] || severityColors.medium}`}>
            {issue.severity}
          </span>
          <span className="text-sm font-medium text-foreground truncate">
            {issue.title}
          </span>
        </div>
        <div className="mt-1 flex items-center gap-3 text-xs text-muted-foreground">
          <span className={`inline-flex items-center rounded px-1.5 py-0.5 text-[11px] font-medium ${statusColors[issue.status] || statusColors.open}`}>
            {issue.status}
          </span>
          <Badge variant="outline" className="text-[11px] px-1.5 py-0">
            {sourceLabels[issue.source] || issue.source}
          </Badge>
          <span>{issue.occurrence_count.toLocaleString()} occurrences</span>
          {issue.affected_customer_count > 0 && (
            <span>{issue.affected_customer_count.toLocaleString()} customers</span>
          )}
          <span>Last seen {formatTimeAgo(issue.last_seen_at)}</span>
        </div>
      </div>
    </div>
  );
}

export default function IssuesPage() {
  const { data, isLoading, error } = useQuery({
    queryKey: ["issues"],
    queryFn: () => api.issues.list({ limit: 50 }),
  });

  const issues = data?.data ?? [];

  return (
    <div className="space-y-6">
      <PageHeader
        title="Issues"
        description="Issues from your connected trackers appear here."
      />

      {isLoading && (
        <Card>
          <CardContent className="py-12 text-center text-sm text-muted-foreground">
            Loading issues...
          </CardContent>
        </Card>
      )}

      {error && (
        <Card>
          <CardContent className="py-12 text-center text-sm text-muted-foreground">
            Failed to load issues. Make sure the backend is running.
          </CardContent>
        </Card>
      )}

      {!isLoading && !error && issues.length === 0 && (
        <EmptyState
          icon={AlertCircle}
          title="No issues yet"
          description="Connect Sentry, Linear, or another issue tracker to start pulling in issues automatically."
          action={{ label: "Go to Settings", href: "/settings" }}
        />
      )}

      {!isLoading && !error && issues.length > 0 && (
        <Card>
          <CardContent className="p-0">
            <div className="flex items-center justify-between px-4 py-3 border-b border-border bg-muted/30">
              <span className="text-xs font-medium text-muted-foreground uppercase tracking-wider">
                {issues.length} issue{issues.length !== 1 ? "s" : ""}
              </span>
            </div>
            {issues.map((issue) => (
              <IssueRow key={issue.id} issue={issue} />
            ))}
          </CardContent>
        </Card>
      )}
    </div>
  );
}
