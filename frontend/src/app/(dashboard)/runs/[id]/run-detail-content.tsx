"use client";

import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import {
  ArrowLeft,
  ExternalLink,
  RefreshCw,
  CheckCircle2,
  XCircle,
  MinusCircle,
  AlertTriangle,
} from "lucide-react";
import Link from "next/link";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { LogViewer } from "@/components/log-viewer";
import { DiffViewer } from "@/components/diff-viewer";
import { api } from "@/lib/api";
import type { AgentRun, Validation } from "@/lib/types";

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

function formatTimestamp(dateStr?: string): string {
  if (!dateStr) return "-";
  return new Date(dateStr).toLocaleString();
}

function confidenceColor(score: number): string {
  if (score > 0.8) return "text-green-700";
  if (score >= 0.5) return "text-yellow-700";
  return "text-red-700";
}

const validationChecks: { key: string; label: string }[] = [
  { key: "direction_check", label: "Direction Check" },
  { key: "correctness_check", label: "Correctness Check" },
  { key: "quality_check", label: "Quality Check" },
  { key: "security_scan", label: "Security Scan" },
  { key: "regression_test_check", label: "Regression Test Check" },
  { key: "ci_check", label: "CI Check" },
];

function checkResultBadge(result: string | null) {
  if (!result) return <Badge variant="secondary" className="bg-gray-100 text-gray-600 text-[11px]">skipped</Badge>;
  if (result === "pass") return <Badge variant="secondary" className="bg-green-100 text-green-800 text-[11px]">pass</Badge>;
  if (result === "fail") return <Badge variant="secondary" className="bg-red-100 text-red-800 text-[11px]">fail</Badge>;
  return <Badge variant="secondary" className="text-[11px]">{result}</Badge>;
}

function OverviewTab({ run }: { run: AgentRun }) {
  const queryClient = useQueryClient();
  const retryMutation = useMutation({
    mutationFn: () => api.issues.triggerFix(run.issue_id),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["run", run.id] });
    },
  });

  const status = statusConfig[run.status] || statusConfig.pending;
  const isActive = run.status === "running" || run.status === "awaiting_input";

  return (
    <div className="space-y-4">
      <Card>
        <CardContent className="pt-6">
          <div className="grid grid-cols-2 gap-4 text-sm">
            <div>
              <span className="text-muted-foreground">Status</span>
              <div className="mt-1">
                <span className={`inline-flex items-center rounded-full px-2 py-0.5 text-[11px] font-medium ${status.color}`}>
                  {isActive && (
                    <span className="relative mr-1.5 flex h-2 w-2">
                      <span className="animate-ping absolute inline-flex h-full w-full rounded-full bg-blue-400 opacity-75" />
                      <span className="relative inline-flex rounded-full h-2 w-2 bg-blue-500" />
                    </span>
                  )}
                  {status.label}
                </span>
              </div>
            </div>
            <div>
              <span className="text-muted-foreground">Agent Type</span>
              <p className="mt-1 font-medium">{agentTypeLabels[run.agent_type] || run.agent_type}</p>
            </div>
            {run.confidence_score != null && (
              <div>
                <span className="text-muted-foreground">Confidence</span>
                <p className={`mt-1 font-medium ${confidenceColor(run.confidence_score)}`}>
                  {(run.confidence_score * 100).toFixed(0)}%
                </p>
              </div>
            )}
            <div>
              <span className="text-muted-foreground">Duration</span>
              <p className="mt-1 font-medium">{formatDuration(run.started_at, run.completed_at)}</p>
            </div>
            <div>
              <span className="text-muted-foreground">Started</span>
              <p className="mt-1">{formatTimestamp(run.started_at)}</p>
            </div>
            <div>
              <span className="text-muted-foreground">Completed</span>
              <p className="mt-1">{formatTimestamp(run.completed_at)}</p>
            </div>
          </div>
        </CardContent>
      </Card>

      {run.result_summary && (
        <Card>
          <CardHeader className="pb-2">
            <CardTitle className="text-sm">Result</CardTitle>
          </CardHeader>
          <CardContent>
            <p className="text-sm">{run.result_summary}</p>
          </CardContent>
        </Card>
      )}

      {run.status === "failed" && run.failure_explanation && (
        <Card className="border-red-200">
          <CardHeader className="pb-2">
            <CardTitle className="text-sm text-red-800 flex items-center gap-2">
              <XCircle className="h-4 w-4" />
              Failure Details
            </CardTitle>
          </CardHeader>
          <CardContent className="space-y-3">
            {run.failure_category && (
              <Badge variant="secondary" className="bg-red-100 text-red-800 text-[11px]">
                {run.failure_category}
              </Badge>
            )}
            <p className="text-sm">{run.failure_explanation}</p>
            {run.failure_next_steps && run.failure_next_steps.length > 0 && (
              <div>
                <p className="text-xs font-medium text-muted-foreground mb-1">Next Steps</p>
                <ul className="list-disc list-inside text-sm space-y-1">
                  {run.failure_next_steps.map((step, i) => (
                    <li key={i}>{step}</li>
                  ))}
                </ul>
              </div>
            )}
            {run.failure_retry_advised && (
              <Button
                size="sm"
                variant="outline"
                onClick={() => retryMutation.mutate()}
                disabled={retryMutation.isPending}
              >
                <RefreshCw className={`mr-1.5 h-3 w-3 ${retryMutation.isPending ? "animate-spin" : ""}`} />
                {retryMutation.isPending ? "Retrying..." : "Retry"}
              </Button>
            )}
          </CardContent>
        </Card>
      )}
    </div>
  );
}

function ValidationTab({ runId }: { runId: string }) {
  const { data, isLoading, error } = useQuery({
    queryKey: ["run", runId, "validation"],
    queryFn: () => api.runs.getValidation(runId),
  });

  if (isLoading) {
    return <div className="py-8 text-center text-sm text-muted-foreground">Loading validation...</div>;
  }

  if (error) {
    return <div className="py-8 text-center text-sm text-muted-foreground">No validation data available.</div>;
  }

  const validation = data?.data;
  if (!validation) {
    return <div className="py-8 text-center text-sm text-muted-foreground">No validation data available.</div>;
  }

  const overallStatus = validation.status;

  return (
    <div className="space-y-4">
      <div className="flex items-center gap-2">
        <span className="text-sm font-medium">Overall:</span>
        {overallStatus === "passed" && (
          <Badge variant="secondary" className="bg-green-100 text-green-800">
            <CheckCircle2 className="mr-1 h-3 w-3" /> Passed
          </Badge>
        )}
        {overallStatus === "failed" && (
          <Badge variant="secondary" className="bg-red-100 text-red-800">
            <XCircle className="mr-1 h-3 w-3" /> Failed
          </Badge>
        )}
        {overallStatus !== "passed" && overallStatus !== "failed" && (
          <Badge variant="secondary" className="bg-gray-100 text-gray-800">
            <MinusCircle className="mr-1 h-3 w-3" /> {overallStatus}
          </Badge>
        )}
      </div>

      <Card>
        <CardContent className="p-0">
          <table className="w-full text-sm">
            <thead>
              <tr className="border-b border-border bg-muted/30">
                <th className="text-left px-4 py-2 text-xs font-medium text-muted-foreground uppercase">Check</th>
                <th className="text-left px-4 py-2 text-xs font-medium text-muted-foreground uppercase">Result</th>
                <th className="text-left px-4 py-2 text-xs font-medium text-muted-foreground uppercase">Details</th>
              </tr>
            </thead>
            <tbody>
              {validationChecks.map(({ key, label }) => {
                const result = validation[key as keyof Validation] as string | null;
                const details = validation[`${key}_details` as keyof Validation] as string | null;
                return (
                  <tr key={key} className="border-b border-border last:border-b-0">
                    <td className="px-4 py-2 font-medium">{label}</td>
                    <td className="px-4 py-2">{checkResultBadge(result)}</td>
                    <td className="px-4 py-2 text-muted-foreground">{details || "-"}</td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </CardContent>
      </Card>
    </div>
  );
}

function PRTab({ runId }: { runId: string }) {
  const { data, isLoading, error } = useQuery({
    queryKey: ["run", runId, "pr"],
    queryFn: () => api.runs.getPR(runId),
  });

  if (isLoading) {
    return <div className="py-8 text-center text-sm text-muted-foreground">Loading PR details...</div>;
  }

  if (error || !data?.data) {
    return (
      <div className="py-8 text-center text-sm text-muted-foreground">
        <AlertTriangle className="mx-auto h-8 w-8 text-muted-foreground/50 mb-2" />
        PR not yet created
      </div>
    );
  }

  const pr = data.data;

  const prStatusColor: Record<string, string> = {
    open: "bg-green-100 text-green-800",
    merged: "bg-purple-100 text-purple-800",
    closed: "bg-red-100 text-red-800",
  };

  return (
    <Card>
      <CardContent className="pt-6 space-y-4">
        <div className="flex items-start justify-between">
          <div>
            <h3 className="text-sm font-medium">{pr.title}</h3>
            <p className="text-xs text-muted-foreground mt-1">{pr.github_repo} #{pr.github_pr_number}</p>
          </div>
          <a href={pr.github_pr_url} target="_blank" rel="noopener noreferrer">
            <Button variant="outline" size="sm">
              <ExternalLink className="mr-1.5 h-3 w-3" />
              View on GitHub
            </Button>
          </a>
        </div>

        <div className="flex items-center gap-3 text-sm">
          <div>
            <span className="text-muted-foreground">Status: </span>
            <Badge variant="secondary" className={`text-[11px] ${prStatusColor[pr.status] || "bg-gray-100 text-gray-800"}`}>
              {pr.status}
            </Badge>
          </div>
          {pr.review_status && (
            <div>
              <span className="text-muted-foreground">Review: </span>
              <Badge variant="secondary" className="text-[11px]">{pr.review_status}</Badge>
            </div>
          )}
          <div>
            <span className="text-muted-foreground">Branch: </span>
            <code className="text-xs bg-muted px-1 py-0.5 rounded">{pr.branch_name}</code>
          </div>
        </div>

        {pr.body && (
          <div className="text-sm text-muted-foreground border-t border-border pt-3">
            <p className="whitespace-pre-wrap">{pr.body}</p>
          </div>
        )}
      </CardContent>
    </Card>
  );
}

export function RunDetailContent({ id }: { id: string }) {
  const { data, isLoading, error } = useQuery({
    queryKey: ["run", id],
    queryFn: () => api.runs.get(id),
    refetchInterval: (query) => {
      const run = query.state.data?.data;
      if (run && (run.status === "running" || run.status === "awaiting_input")) {
        return 5000;
      }
      return false;
    },
  });

  const run = data?.data;
  const isActive = run?.status === "running" || run?.status === "awaiting_input";

  if (isLoading) {
    return (
      <div className="space-y-6">
        <Link href="/runs" className="inline-flex items-center gap-1 text-xs text-muted-foreground hover:text-foreground">
          <ArrowLeft className="h-3 w-3" /> Back to runs
        </Link>
        <Card>
          <CardContent className="py-12 text-center text-sm text-muted-foreground">
            Loading run...
          </CardContent>
        </Card>
      </div>
    );
  }

  if (error || !run) {
    return (
      <div className="space-y-6">
        <Link href="/runs" className="inline-flex items-center gap-1 text-xs text-muted-foreground hover:text-foreground">
          <ArrowLeft className="h-3 w-3" /> Back to runs
        </Link>
        <Card>
          <CardContent className="py-12 text-center text-sm text-muted-foreground">
            Failed to load run details.
          </CardContent>
        </Card>
      </div>
    );
  }

  return (
    <div className="space-y-6">
      <Link href="/runs" className="inline-flex items-center gap-1 text-xs text-muted-foreground hover:text-foreground">
        <ArrowLeft className="h-3 w-3" /> Back to runs
      </Link>

      <div>
        <h1 className="text-sm font-semibold text-foreground">
          {run.result_summary || `Run ${run.id.slice(0, 8)}`}
        </h1>
        <p className="mt-1 text-sm text-muted-foreground">
          {agentTypeLabels[run.agent_type] || run.agent_type} run
        </p>
      </div>

      <Tabs defaultValue="overview">
        <TabsList>
          <TabsTrigger value="overview">Overview</TabsTrigger>
          <TabsTrigger value="logs">Logs</TabsTrigger>
          <TabsTrigger value="diff">Diff</TabsTrigger>
          <TabsTrigger value="validation">Validation</TabsTrigger>
          <TabsTrigger value="pr">PR</TabsTrigger>
        </TabsList>

        <TabsContent value="overview">
          <OverviewTab run={run} />
        </TabsContent>

        <TabsContent value="logs">
          <LogViewer runId={id} isActive={isActive} />
        </TabsContent>

        <TabsContent value="diff">
          {run.diff ? (
            <DiffViewer diff={run.diff} />
          ) : (
            <div className="py-8 text-center text-sm text-muted-foreground">
              No diff available for this run.
            </div>
          )}
        </TabsContent>

        <TabsContent value="validation">
          <ValidationTab runId={id} />
        </TabsContent>

        <TabsContent value="pr">
          <PRTab runId={id} />
        </TabsContent>
      </Tabs>
    </div>
  );
}
