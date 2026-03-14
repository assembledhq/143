"use client";

import { useQuery } from "@tanstack/react-query";
import { RefreshCw, Plus, Clock, Activity } from "lucide-react";
import Link from "next/link";
import { Button } from "@/components/ui/button";
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
      <span className="relative flex h-2 w-2">
        <span className="animate-ping absolute inline-flex h-full w-full rounded-full bg-primary/60 opacity-75" />
        <span className="relative inline-flex rounded-full h-2 w-2 bg-primary" />
      </span>
    );
  }
  if (status === "completed") {
    return <span className="inline-flex rounded-full h-2 w-2 bg-green-500" />;
  }
  if (status === "failed") {
    return <span className="inline-flex rounded-full h-2 w-2 bg-red-500" />;
  }
  return <span className="inline-flex rounded-full h-2 w-2 bg-muted-foreground/30" />;
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

  const isRunning = agentStatus === "running";

  return (
    <div className="space-y-2">
      <div
        className={`flex items-center gap-3 rounded-lg border px-4 py-2.5 transition-colors ${
          isRunning
            ? "border-primary/20 bg-primary/5 dark:border-primary/30 dark:bg-primary/10 dark:shadow-[0_0_20px_oklch(0.6_0.15_270_/_8%)]"
            : "border-border bg-muted/30"
        }`}
      >
        <StatusDot status={agentStatus} />

        <span className="text-[13px] font-medium text-foreground">PM Agent</span>

        <span className={`text-[11px] font-medium px-1.5 py-0.5 rounded ${
          isRunning ? "bg-primary/10 text-primary shadow-[var(--glow-primary-sm)]"
          : agentStatus === "completed" ? "bg-emerald-500/10 text-emerald-700 dark:text-emerald-400"
          : agentStatus === "failed" ? "bg-destructive/10 text-destructive"
          : "bg-muted text-muted-foreground"
        }`}>
          {statusLabel}
        </span>

        {isRunning && (
          <span className="text-[12px] text-primary">
            Analyzing issues and generating a plan...
          </span>
        )}

        {!isRunning && pmStatus && (
          <div className="flex items-center gap-3 text-[11px] text-muted-foreground">
            {pmStatus.total_delegated > 0 && (
              <span>{Math.round(pmStatus.success_rate)}% success</span>
            )}
            {pmStatus.last_run_at && (
              <span className="flex items-center gap-1">
                <Clock className="h-3 w-3" />
                {formatTimeAgo(pmStatus.last_run_at)}
              </span>
            )}
            {pmStatus.issues_reviewed > 0 && (
              <span className="flex items-center gap-1">
                <Activity className="h-3 w-3" />
                {pmStatus.issues_reviewed} reviewed
              </span>
            )}
          </div>
        )}

        <div className="flex items-center gap-2 ml-auto shrink-0">
          <Button size="sm" variant="outline" className="h-7 text-[12px]" asChild>
            <Link href="/sessions/new">
              <Plus className="mr-1 h-3 w-3" />
              Manual Session
            </Link>
          </Button>
          <Button
            size="sm"
            className="h-7 text-[12px]"
            onClick={handleAnalyze}
            disabled={isPending || isAnalyzing}
          >
            <RefreshCw className={`mr-1 h-3 w-3 ${isPending || isAnalyzing ? "animate-spin" : ""}`} />
            {isPending ? "Starting..." : isAnalyzing ? "Running..." : "Run PM agent"}
          </Button>
        </div>
      </div>

      {analyzeError && (
        <div className="flex items-center justify-between rounded-md bg-red-50 dark:bg-red-950/30 px-3 py-2">
          <p className="text-xs text-red-700 dark:text-red-300">{analyzeError}</p>
          <button onClick={dismissError} className="text-xs text-red-500 hover:text-red-700 ml-2">dismiss</button>
        </div>
      )}
    </div>
  );
}
