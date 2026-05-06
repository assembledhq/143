import { MessageSquare } from "lucide-react";
import { StatusDot } from "@/components/status-dot";
import { prMergedAccent } from "@/lib/pr-status-styles";
import { DiffStatsBadge } from "./diff-stats-badge";
import type { DiffStats } from "@/lib/diff-parser";

const statusLabels: Record<string, string> = {
  pending: "Pending",
  running: "Running",
  idle: "Idle",
  awaiting_input: "Awaiting input",
  needs_human_guidance: "Needs guidance",
  completed: "Completed",
  pr_created: "PR created",
  failed: "Failed",
  cancelled: "Cancelled",
  skipped: "Skipped",
};

const statusDotColor: Record<string, string> = {
  pending: "bg-muted-foreground/50",
  running: "bg-primary",
  idle: "bg-sky-500",
  awaiting_input: "bg-amber-500",
  needs_human_guidance: "bg-orange-500",
  completed: "bg-emerald-500",
  pr_created: prMergedAccent.dot,
  failed: "bg-destructive",
  cancelled: "bg-muted-foreground/50",
  skipped: "bg-muted-foreground/30",
};

const workingStatuses = new Set(["pending", "running"]);

interface SessionFooterProps {
  status: string;
  currentTurn: number;
  diffStats: DiffStats | null;
  onDiffClick?: () => void;
  openCommentCount?: number;
  onCommentsClick?: () => void;
}

export function SessionFooter({
  status,
  currentTurn,
  diffStats,
  onDiffClick,
  openCommentCount,
  onCommentsClick,
}: SessionFooterProps) {
  const isWorking = workingStatuses.has(status);

  return (
    <div
      data-testid="session-footer"
      className="h-8 border-t border-border bg-background flex items-center px-4 gap-4 text-xs shrink-0"
    >
      <div className="flex items-center gap-1.5">
        {isWorking ? (
          <StatusDot color={statusDotColor[status] ?? "bg-primary"} animate pingColor="bg-primary/60" />
        ) : (
          <StatusDot color={statusDotColor[status] ?? "bg-muted-foreground/50"} />
        )}
        <span className="text-muted-foreground font-medium">
          {statusLabels[status] ?? status}
        </span>
      </div>

      {currentTurn > 0 && (
        <span className="text-muted-foreground/60">
          Turn {currentTurn}
        </span>
      )}

      {diffStats && (diffStats.added > 0 || diffStats.removed > 0) && (
        <DiffStatsBadge
          added={diffStats.added}
          removed={diffStats.removed}
          onClick={onDiffClick}
        />
      )}

      {openCommentCount != null && openCommentCount > 0 && (
        <button
          onClick={onCommentsClick}
          className="flex items-center gap-1 text-muted-foreground hover:text-foreground transition-colors"
        >
          <MessageSquare className="h-3 w-3" />
          <span>{openCommentCount} comment{openCommentCount > 1 ? "s" : ""}</span>
        </button>
      )}
    </div>
  );
}
