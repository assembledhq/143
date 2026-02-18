"use client";

import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { AlertCircle, X, Wrench, Loader2 } from "lucide-react";
import { useRouter } from "next/navigation";
import { useQueryState, parseAsString } from "nuqs";
import { PageHeader } from "@/components/page-header";
import { EmptyState } from "@/components/empty-state";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
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

const statusOptions = [
  { value: "open", label: "Open" },
  { value: "triaged", label: "Triaged" },
  { value: "in_progress", label: "In Progress" },
  { value: "fixed", label: "Fixed" },
  { value: "wont_fix", label: "Won't Fix" },
  { value: "duplicate", label: "Duplicate" },
];

const sourceOptions = [
  { value: "sentry", label: "Sentry" },
  { value: "linear", label: "Linear" },
  { value: "support", label: "Support" },
];

const severityOptions = [
  { value: "critical", label: "Critical" },
  { value: "high", label: "High" },
  { value: "medium", label: "Medium" },
  { value: "low", label: "Low" },
];

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
  const router = useRouter();
  const queryClient = useQueryClient();
  const canFix = issue.status === "open" || issue.status === "triaged";

  const fixMutation = useMutation({
    mutationFn: () => api.issues.triggerFix(issue.id),
    onSuccess: (result) => {
      queryClient.invalidateQueries({ queryKey: ["runs"] });
      router.push(`/runs/${result.data.id}`);
    },
  });

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
      {canFix && (
        <Button
          variant="outline"
          size="sm"
          onClick={() => fixMutation.mutate()}
          disabled={fixMutation.isPending}
          className="ml-3 shrink-0"
        >
          {fixMutation.isPending ? (
            <Loader2 className="mr-1.5 h-3 w-3 animate-spin" />
          ) : (
            <Wrench className="mr-1.5 h-3 w-3" />
          )}
          {fixMutation.isPending ? "Starting..." : "Fix This"}
        </Button>
      )}
    </div>
  );
}

export function IssuesPageContent() {
  const [status, setStatus] = useQueryState("status", parseAsString);
  const [source, setSource] = useQueryState("source", parseAsString);
  const [severity, setSeverity] = useQueryState("severity", parseAsString);

  const hasFilters = status !== null || source !== null || severity !== null;

  const { data, isLoading, error } = useQuery({
    queryKey: ["issues", { status, source, severity }],
    queryFn: () =>
      api.issues.list({
        status: status ?? undefined,
        source: source ?? undefined,
        severity: severity ?? undefined,
        limit: 50,
      }),
  });

  const issues = data?.data ?? [];

  function clearFilters() {
    setStatus(null);
    setSource(null);
    setSeverity(null);
  }

  return (
    <div className="space-y-6">
      <PageHeader
        title="Issues"
        description="Issues from your connected trackers appear here."
      />

      <div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:gap-4">
        <div className="flex flex-col gap-1.5">
          <label htmlFor="status-filter" className="text-xs font-medium text-muted-foreground">
            Status
          </label>
          <Select
            value={status ?? "all"}
            onValueChange={(v) => setStatus(v === "all" ? null : v)}
          >
            <SelectTrigger id="status-filter" className="w-[140px]" size="sm">
              <SelectValue placeholder="All statuses" />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="all">All statuses</SelectItem>
              {statusOptions.map((opt) => (
                <SelectItem key={opt.value} value={opt.value}>
                  {opt.label}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>

        <div className="flex flex-col gap-1.5">
          <label htmlFor="source-filter" className="text-xs font-medium text-muted-foreground">
            Source
          </label>
          <Select
            value={source ?? "all"}
            onValueChange={(v) => setSource(v === "all" ? null : v)}
          >
            <SelectTrigger id="source-filter" className="w-[140px]" size="sm">
              <SelectValue placeholder="All sources" />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="all">All sources</SelectItem>
              {sourceOptions.map((opt) => (
                <SelectItem key={opt.value} value={opt.value}>
                  {opt.label}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>

        <div className="flex flex-col gap-1.5">
          <label htmlFor="severity-filter" className="text-xs font-medium text-muted-foreground">
            Severity
          </label>
          <Select
            value={severity ?? "all"}
            onValueChange={(v) => setSeverity(v === "all" ? null : v)}
          >
            <SelectTrigger id="severity-filter" className="w-[140px]" size="sm">
              <SelectValue placeholder="All severities" />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="all">All severities</SelectItem>
              {severityOptions.map((opt) => (
                <SelectItem key={opt.value} value={opt.value}>
                  {opt.label}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>

        {hasFilters && (
          <div className="flex flex-col gap-1.5 sm:self-end">
            <div className="hidden sm:block text-xs">&nbsp;</div>
            <Button
              variant="ghost"
              size="sm"
              onClick={clearFilters}
              className="text-xs text-muted-foreground"
            >
              <X className="mr-1 h-3 w-3" />
              Clear filters
            </Button>
          </div>
        )}
      </div>

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
