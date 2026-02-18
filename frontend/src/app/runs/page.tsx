"use client";

import { useQuery } from "@tanstack/react-query";
import { Play } from "lucide-react";
import { PageHeader } from "@/components/page-header";
import { EmptyState } from "@/components/empty-state";
import { Badge } from "@/components/ui/badge";
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

function RunRow({ run }: { run: AgentRun }) {
  const status = statusConfig[run.status] || statusConfig.pending;

  return (
    <div className="flex items-center justify-between py-3 px-4 border-b border-border last:border-b-0 hover:bg-muted/50 transition-colors">
      <div className="flex-1 min-w-0">
        <div className="flex items-center gap-2">
          <span className={`inline-flex items-center rounded-full px-2 py-0.5 text-[11px] font-medium ${status.color}`}>
            {status.label}
          </span>
          <span className="text-sm font-medium text-foreground truncate">
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
    </div>
  );
}

export default function RunsPage() {
  const { data, isLoading, error } = useQuery({
    queryKey: ["runs"],
    queryFn: () => api.runs.list({ limit: 50 }),
  });

  const runs = data?.data ?? [];

  return (
    <div className="space-y-6">
      <PageHeader
        title="Runs"
        description="Each agent execution shows up as a run."
      />

      {isLoading && (
        <Card>
          <CardContent className="py-12 text-center text-sm text-muted-foreground">
            Loading runs...
          </CardContent>
        </Card>
      )}

      {error && (
        <Card>
          <CardContent className="py-12 text-center text-sm text-muted-foreground">
            Failed to load runs. Make sure the backend is running.
          </CardContent>
        </Card>
      )}

      {!isLoading && !error && runs.length === 0 && (
        <EmptyState
          icon={Play}
          title="No runs yet"
          description="Runs are created automatically when 143 picks up an issue and starts working on a fix."
        />
      )}

      {!isLoading && !error && runs.length > 0 && (
        <Card>
          <CardContent className="p-0">
            <div className="flex items-center justify-between px-4 py-3 border-b border-border bg-muted/30">
              <span className="text-xs font-medium text-muted-foreground uppercase tracking-wider">
                {runs.length} run{runs.length !== 1 ? "s" : ""}
              </span>
            </div>
            {runs.map((run) => (
              <RunRow key={run.id} run={run} />
            ))}
          </CardContent>
        </Card>
      )}
    </div>
  );
}
