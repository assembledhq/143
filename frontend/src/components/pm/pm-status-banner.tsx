"use client";

import { useQuery } from "@tanstack/react-query";
import { RefreshCw, Plus, Clock, Activity, Timer } from "lucide-react";
import Link from "next/link";
import { Button } from "@/components/ui/button";
import { api } from "@/lib/api";
import { useAnalyze } from "@/hooks/use-analyze";
import { formatTimeAgo } from "@/lib/utils";
import { StatusDot } from "@/components/status-dot";
import type { PMStatus } from "@/lib/types";

const pmDotColors: Record<string, string> = {
  running: "bg-primary",
  completed: "bg-green-500",
  failed: "bg-red-500",
  idle: "bg-muted-foreground/30",
};

function PMStatusDot({ status }: { status: "running" | "completed" | "failed" | "idle" }) {
  return (
    <StatusDot
      animate={status === "running"}
      color={pmDotColors[status] || "bg-muted-foreground/30"}
    />
  );
}

function deriveAgentStatus(pmStatus: PMStatus | undefined, isAnalyzing: boolean): "running" | "completed" | "failed" | "idle" {
  if (isAnalyzing || pmStatus?.is_running) return "running";
  if (pmStatus?.last_error) return "failed";
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
        <PMStatusDot status={agentStatus} />

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
            {pmStatus.next_run_in && (
              <span className="flex items-center gap-1">
                <Timer className="h-3 w-3" />
                Next run {pmStatus.next_run_in}
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
            title="Run the PM agent now without waiting for the next scheduled run"
          >
            <RefreshCw className={`mr-1 h-3 w-3 ${isPending || isAnalyzing ? "animate-spin" : ""}`} />
            {isPending ? "Starting..." : isAnalyzing ? "Running..." : "Run now"}
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
