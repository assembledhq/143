"use client";

import { useQuery } from "@tanstack/react-query";
import { RefreshCw, Plus, Activity, CheckCircle2, XCircle, Clock } from "lucide-react";
import Link from "next/link";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { api } from "@/lib/api";
import { useAnalyze } from "@/hooks/use-analyze";
import type { PMStatus } from "@/lib/types";

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
  return `${diffDays}d ago`;
}

function StatusDot({ status }: { status: "running" | "completed" | "failed" | "idle" }) {
  if (status === "running") {
    return (
      <span className="relative flex h-2.5 w-2.5">
        <span className="animate-ping absolute inline-flex h-full w-full rounded-full bg-primary/60 opacity-75" />
        <span className="relative inline-flex rounded-full h-2.5 w-2.5 bg-primary" />
      </span>
    );
  }
  if (status === "completed") {
    return <span className="inline-flex rounded-full h-2.5 w-2.5 bg-green-500" />;
  }
  if (status === "failed") {
    return <span className="inline-flex rounded-full h-2.5 w-2.5 bg-red-500" />;
  }
  return <span className="inline-flex rounded-full h-2.5 w-2.5 bg-muted-foreground/30" />;
}

function deriveAgentStatus(pmStatus: PMStatus | undefined, isAnalyzing: boolean): "running" | "completed" | "failed" | "idle" {
  if (isAnalyzing || pmStatus?.is_running) return "running";
  if (!pmStatus?.last_run_status) return "idle";
  if (pmStatus.last_run_status === "completed" || pmStatus.last_run_status === "executing") return "completed";
  if (pmStatus.last_run_status === "failed") return "failed";
  return "idle";
}

interface PMStatusBannerProps {
  hasActivePlanSession: boolean;
}

export function PMStatusBanner({ hasActivePlanSession }: PMStatusBannerProps) {
  const { data: statusData } = useQuery({
    queryKey: ["pm", "status"],
    queryFn: () => api.pm.status(),
    refetchInterval: 15000,
  });

  const pmStatus = statusData?.data;
  const { isAnalyzing, isPending, analyzeError, handleAnalyze, dismissError } = useAnalyze(hasActivePlanSession);

  const agentStatus = deriveAgentStatus(pmStatus, isAnalyzing);
  const statusLabel = agentStatus === "running" ? "Running"
    : agentStatus === "completed" ? "Active"
    : agentStatus === "failed" ? "Attention needed"
    : "Idle";

  return (
    <Card className={agentStatus === "running" ? "border-primary/20 border-l-4 border-l-primary" : ""}>
      <CardContent className="py-4">
        <div className="flex items-start justify-between">
          <div className="space-y-2">
            <div className="flex items-center gap-2">
              <StatusDot status={agentStatus} />
              <span className="text-sm font-semibold text-foreground">PM Agent</span>
              <span className={`text-xs font-medium px-2 py-0.5 rounded-full ${
                agentStatus === "running" ? "bg-primary/10 text-primary"
                : agentStatus === "completed" ? "bg-green-100 text-green-700 dark:bg-green-900/40 dark:text-green-300"
                : agentStatus === "failed" ? "bg-destructive/10 text-destructive"
                : "bg-muted text-muted-foreground"
              }`}>
                {statusLabel}
              </span>
            </div>

            {agentStatus === "running" && (
              <p className="text-sm text-blue-700 dark:text-blue-300">
                Analyzing issues, reviewing context, and generating a plan...
              </p>
            )}

            {!isAnalyzing && pmStatus && (
              <div className="flex flex-wrap items-center gap-x-4 gap-y-1 text-xs text-muted-foreground">
                {pmStatus.last_run_at && (
                  <span className="flex items-center gap-1">
                    <Clock className="h-3 w-3" />
                    Last run: {formatTimeAgo(pmStatus.last_run_at)}
                  </span>
                )}
                {pmStatus.issues_reviewed > 0 && (
                  <span className="flex items-center gap-1">
                    <Activity className="h-3 w-3" />
                    {pmStatus.issues_reviewed} issues reviewed
                  </span>
                )}
                {pmStatus.next_run_in && (
                  <span>Next run: {pmStatus.next_run_in}</span>
                )}
              </div>
            )}

            {!isAnalyzing && pmStatus && pmStatus.total_delegated > 0 && (
              <div className="flex items-center gap-3 text-xs">
                <span className="flex items-center gap-1 text-muted-foreground">
                  {Math.round(pmStatus.success_rate)}% success rate
                </span>
                <span className="flex items-center gap-1 text-green-600">
                  <CheckCircle2 className="h-3 w-3" />
                  {pmStatus.success_count} succeeded
                </span>
                {pmStatus.total_delegated - pmStatus.success_count > 0 && (
                  <span className="flex items-center gap-1 text-red-600">
                    <XCircle className="h-3 w-3" />
                    {pmStatus.total_delegated - pmStatus.success_count} failed
                  </span>
                )}
              </div>
            )}
          </div>

          <div className="flex items-center gap-2 shrink-0">
            <Button size="sm" variant="outline" asChild>
              <Link href="/sessions/new">
                <Plus className="mr-1.5 h-3.5 w-3.5" />
                Manual Session
              </Link>
            </Button>
            <Button
              size="sm"
              onClick={handleAnalyze}
              disabled={isPending || isAnalyzing}
            >
              <RefreshCw className={`mr-1.5 h-3.5 w-3.5 ${isPending || isAnalyzing ? "animate-spin" : ""}`} />
              {isPending ? "Starting..." : isAnalyzing ? "Analyzing..." : "Analyze Now"}
            </Button>
          </div>
        </div>

        {analyzeError && (
          <div className="mt-3 flex items-center justify-between rounded-md bg-red-50 dark:bg-red-950/30 px-3 py-2">
            <p className="text-xs text-red-700 dark:text-red-300">{analyzeError}</p>
            <button onClick={dismissError} className="text-xs text-red-500 hover:text-red-700 ml-2">dismiss</button>
          </div>
        )}
      </CardContent>
    </Card>
  );
}
