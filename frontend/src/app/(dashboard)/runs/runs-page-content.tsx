"use client";

import { useQuery } from "@tanstack/react-query";
import { Play } from "lucide-react";
import Link from "next/link";
import { useQueryState, parseAsString } from "nuqs";
import { PageHeader } from "@/components/page-header";
import { EmptyState } from "@/components/empty-state";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { api } from "@/lib/api";
import type { AgentRun } from "@/lib/types";

const statusConfig: Record<string, { color: string; label: string }> = {
  pending: { color: "bg-gray-100 text-gray-800", label: "Pending" },
  running: { color: "bg-blue-100 text-blue-800", label: "Running" },
  awaiting_input: { color: "bg-yellow-100 text-yellow-800", label: "Awaiting Input" },
  needs_human_guidance: { color: "bg-orange-100 text-orange-800", label: "Needs Guidance" },
  resumed_locally: { color: "bg-purple-100 text-purple-800", label: "Resumed Locally" },
  completed: { color: "bg-green-100 text-green-800", label: "Completed" },
  pr_created: { color: "bg-green-100 text-green-800", label: "PR Created" },
  failed: { color: "bg-red-100 text-red-800", label: "Failed" },
  cancelled: { color: "bg-gray-100 text-gray-700", label: "Cancelled" },
  skipped: { color: "bg-gray-100 text-gray-700", label: "Skipped" },
};

const agentTypeLabels: Record<string, string> = {
  claude_code: "Claude Code",
  codex: "Codex",
  gemini_cli: "Gemini CLI",
  custom: "Custom",
};

const statusFilterTabs = [
  { value: "all", label: "All" },
  { value: "active", label: "Active" },
  { value: "needs_human_guidance", label: "Needs Review" },
  { value: "failed", label: "Failed" },
  { value: "done", label: "Completed" },
];

function formatDuration(startedAt?: string, completedAt?: string): string {
  if (!startedAt) return "-";
  const start = new Date(startedAt);
  const end = completedAt ? new Date(completedAt) : new Date();
  const diffMs = end.getTime() - start.getTime();
  const diffSecs = Math.floor(diffMs / 1000);
  if (diffSecs < 60) return `${diffSecs}s`;
  const mins = Math.floor(diffSecs / 60);
  const secs = diffSecs % 60;
  return `${mins}m ${secs}s`;
}

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

function isActive(status: string): boolean {
  return status === "running" || status === "awaiting_input";
}

function isNeedsReview(status: string): boolean {
  return status === "needs_human_guidance";
}

function isFailed(status: string): boolean {
  return status === "failed";
}

function isDone(status: string): boolean {
  return status === "completed" || status === "pr_created";
}

function filterRuns(runs: AgentRun[], filter: string | null): AgentRun[] {
  if (!filter || filter === "all") return runs;
  if (filter === "active") return runs.filter((r) => isActive(r.status));
  if (filter === "needs_human_guidance") return runs.filter((r) => isNeedsReview(r.status));
  if (filter === "failed") return runs.filter((r) => isFailed(r.status));
  if (filter === "done") return runs.filter((r) => isDone(r.status));
  return runs;
}

function RunRow({ run }: { run: AgentRun }) {
  const status = statusConfig[run.status] || statusConfig.pending;
  const active = isActive(run.status);

  return (
    <Link
      href={`/runs/${run.id}`}
      className="flex items-center justify-between py-3 px-4 border-b border-border last:border-b-0 hover:bg-muted/50 transition-colors cursor-pointer"
    >
      <div className="flex-1 min-w-0">
        <div className="flex items-center gap-2">
          <span className={`inline-flex items-center rounded-full px-2 py-0.5 text-[11px] font-medium ${status.color}`}>
            {active && (
              <span className="relative mr-1.5 flex h-2 w-2">
                <span className="animate-ping absolute inline-flex h-full w-full rounded-full bg-blue-400 opacity-75" />
                <span className="relative inline-flex rounded-full h-2 w-2 bg-blue-500" />
              </span>
            )}
            {status.label}
          </span>
          <span className="text-[13px] font-medium text-foreground truncate">
            {run.result_summary || `Run ${run.id.slice(0, 8)}`}
          </span>
        </div>
        <div className="mt-1 flex items-center gap-3 text-xs text-muted-foreground">
          <Badge variant="outline" className="text-[11px] px-1.5 py-0">
            {agentTypeLabels[run.agent_type] || run.agent_type}
          </Badge>
          {run.confidence_score != null && (
            <span>Confidence: {(run.confidence_score * 100).toFixed(0)}%</span>
          )}
          <span>Duration: {formatDuration(run.started_at, run.completed_at)}</span>
          <span>{formatTimeAgo(run.created_at)}</span>
        </div>
        {run.failure_explanation && (
          <p className="mt-1 text-xs text-red-600 truncate">
            {run.failure_explanation}
          </p>
        )}
      </div>
    </Link>
  );
}

function RunSection({ title, runs, badge }: { title: string; runs: AgentRun[]; badge?: React.ReactNode }) {
  if (runs.length === 0) return null;
  return (
    <Card>
      <CardContent className="p-0">
        <div className="flex items-center gap-2 px-4 py-3 border-b border-border bg-muted/30">
          <span className="text-xs font-medium text-muted-foreground uppercase tracking-wider">
            {title}
          </span>
          {badge}
          <span className="text-xs text-muted-foreground">({runs.length})</span>
        </div>
        {runs.map((run) => (
          <RunRow key={run.id} run={run} />
        ))}
      </CardContent>
    </Card>
  );
}

export function RunsPageContent() {
  const [statusFilter, setStatusFilter] = useQueryState("status", parseAsString);

  const { data, isLoading, error } = useQuery({
    queryKey: ["runs"],
    queryFn: () => api.runs.list({ limit: 50 }),
    refetchInterval: 10000,
  });

  const allRuns = data?.data ?? [];
  const runs = filterRuns(allRuns, statusFilter);

  const showGrouped = !statusFilter || statusFilter === "all";

  const activeRuns = allRuns.filter((r) => isActive(r.status));
  const reviewRuns = allRuns.filter((r) => isNeedsReview(r.status));
  const failedRuns = allRuns.filter((r) => isFailed(r.status));
  const doneRuns = allRuns.filter((r) => isDone(r.status));

  return (
    <div className="space-y-6">
      <PageHeader
        title="Runs"
        description="Each agent execution shows up as a run."
      />

      <div className="flex items-center gap-1">
        {statusFilterTabs.map((tab) => (
          <Button
            key={tab.value}
            variant={(statusFilter ?? "all") === tab.value ? "default" : "ghost"}
            size="sm"
            className="text-xs"
            onClick={() => setStatusFilter(tab.value === "all" ? null : tab.value)}
          >
            {tab.label}
            {tab.value === "active" && activeRuns.length > 0 && (
              <span className="ml-1.5 rounded-full bg-blue-500 text-white text-[10px] px-1.5 py-0">{activeRuns.length}</span>
            )}
            {tab.value === "needs_human_guidance" && reviewRuns.length > 0 && (
              <span className="ml-1.5 rounded-full bg-orange-500 text-white text-[10px] px-1.5 py-0">{reviewRuns.length}</span>
            )}
            {tab.value === "failed" && failedRuns.length > 0 && (
              <span className="ml-1.5 rounded-full bg-red-500 text-white text-[10px] px-1.5 py-0">{failedRuns.length}</span>
            )}
          </Button>
        ))}
      </div>

      {isLoading && (
        <Card>
          <CardContent className="py-12 text-center text-[13px] text-muted-foreground">
            Loading runs...
          </CardContent>
        </Card>
      )}

      {error && (
        <Card>
          <CardContent className="py-12 text-center text-[13px] text-muted-foreground">
            Failed to load runs. Make sure the backend is running.
          </CardContent>
        </Card>
      )}

      {!isLoading && !error && allRuns.length === 0 && (
        <EmptyState
          icon={Play}
          title="No runs yet"
          description="Runs are created automatically when 143 picks up an issue and starts working on a fix."
        />
      )}

      {!isLoading && !error && allRuns.length > 0 && showGrouped && (
        <div className="space-y-4">
          <RunSection
            title="Active"
            runs={activeRuns}
            badge={
              activeRuns.length > 0 ? (
                <span className="relative flex h-2 w-2">
                  <span className="animate-ping absolute inline-flex h-full w-full rounded-full bg-blue-400 opacity-75" />
                  <span className="relative inline-flex rounded-full h-2 w-2 bg-blue-500" />
                </span>
              ) : undefined
            }
          />
          <RunSection
            title="Needs Review"
            runs={reviewRuns}
            badge={reviewRuns.length > 0 ? <Badge variant="secondary" className="bg-orange-100 text-orange-800 text-[10px] px-1.5 py-0">action needed</Badge> : undefined}
          />
          <RunSection title="Failed" runs={failedRuns} />
          <RunSection title="Completed" runs={doneRuns} />
        </div>
      )}

      {!isLoading && !error && allRuns.length > 0 && !showGrouped && (
        <Card>
          <CardContent className="p-0">
            <div className="flex items-center justify-between px-4 py-3 border-b border-border bg-muted/30">
              <span className="text-xs font-medium text-muted-foreground uppercase tracking-wider">
                {runs.length} run{runs.length !== 1 ? "s" : ""}
              </span>
            </div>
            {runs.length === 0 ? (
              <div className="py-8 text-center text-[13px] text-muted-foreground">
                No runs match this filter.
              </div>
            ) : (
              runs.map((run) => (
                <RunRow key={run.id} run={run} />
              ))
            )}
          </CardContent>
        </Card>
      )}
    </div>
  );
}
