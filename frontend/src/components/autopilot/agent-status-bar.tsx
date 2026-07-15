"use client";

import { Clock, Activity, Timer } from "lucide-react";
import { StatusDot } from "@/components/status-dot";
import { formatTimeAgo } from "@/lib/utils";
import type { PMStatus } from "@/lib/types";

export const agentStatusDotColors: Record<string, string> = {
  running: "bg-primary",
  completed: "bg-success",
  failed: "bg-destructive",
  idle: "bg-muted-foreground/30",
};

export type AgentStatusValue = "running" | "completed" | "failed" | "idle";

export function deriveAgentStatus(pmStatus: PMStatus | undefined, isAnalyzing: boolean): AgentStatusValue {
  if (isAnalyzing || pmStatus?.is_running) return "running";
  if (pmStatus?.last_error) return "failed";
  if (!pmStatus?.last_run_status) return "idle";
  if (pmStatus.last_run_status === "completed" || pmStatus.last_run_status === "executing") return "completed";
  if (pmStatus.last_run_status === "failed") return "failed";
  return "idle";
}

export function agentStatusLabel(status: AgentStatusValue): string {
  switch (status) {
    case "running": return "Running";
    case "completed": return "Active";
    case "failed": return "Attention needed";
    case "idle": return "Idle";
  }
}

interface AgentStatusBarProps {
  label: string;
  pmStatus: PMStatus | undefined;
  agentStatus: AgentStatusValue;
  /** Optional extra actions rendered on the right side of the bar. */
  children?: React.ReactNode;
}

export function AgentStatusBar({ label, pmStatus, agentStatus, children }: AgentStatusBarProps) {
  const isRunning = agentStatus === "running";
  const dotColor = agentStatusDotColors[agentStatus] || "bg-muted-foreground/30";
  const statusText = agentStatusLabel(agentStatus);

  return (
    <div
      className={`flex items-center gap-3 rounded-lg border px-4 py-2.5 transition-colors ${
        isRunning
          ? "border-primary/25 bg-primary/5 dark:border-primary/30 dark:bg-primary/10"
          : "border-border bg-muted/30"
      }`}
    >
      {isRunning ? (
        <StatusDot animate color={dotColor} pingColor="bg-primary/60" />
      ) : (
        <StatusDot color={dotColor} />
      )}

      <span className="text-xs font-medium text-foreground">{label}</span>

      <span className={`text-xs font-medium px-1.5 py-0.5 rounded ${
        isRunning ? "bg-primary/10 text-primary"
        : agentStatus === "completed" ? "bg-success/10 text-success"
        : agentStatus === "failed" ? "bg-destructive/10 text-destructive"
        : "bg-muted text-muted-foreground"
      }`}>
        {statusText}
      </span>

      {isRunning && (
        <span className="text-xs text-primary">
          Analyzing issues and generating a plan...
        </span>
      )}

      {!isRunning && pmStatus && (
        <div className="flex items-center gap-3 text-xs text-muted-foreground">
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

      <div className="ml-auto shrink-0 flex items-center gap-2">
        {children}
      </div>
    </div>
  );
}
