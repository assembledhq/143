"use client";

import { ArrowUpRight } from "lucide-react";
import type { PMDecisionSummary } from "@/lib/types";

const AUTONOMY_LABELS: Record<string, string> = {
  manual: "Suggest",
  auto_simple: "Act on low-risk work",
  auto_all: "Operate broadly",
};

const NEXT_LEVEL: Record<string, string> = {
  manual: "auto_simple",
  auto_simple: "auto_all",
};

interface AutonomyReadinessProps {
  autonomyLevel: string;
  decisionSummary?: PMDecisionSummary;
  totalCycles: number;
}

export function AutonomyReadiness({ autonomyLevel, decisionSummary, totalCycles }: AutonomyReadinessProps) {
  const nextLevel = NEXT_LEVEL[autonomyLevel];

  // Can't advance further
  if (!nextLevel) return null;

  // Not enough data to recommend
  if (!decisionSummary || decisionSummary.total_delegated === 0 || totalCycles < 3) {
    return (
      <div className="rounded-md border border-border bg-surface-pane px-4 py-3 text-xs text-muted-foreground">
        After a few more analysis cycles, readiness signals will appear here.
      </div>
    );
  }

  const successRate = decisionSummary.total_delegated > 0
    ? decisionSummary.succeeded / decisionSummary.total_delegated
    : 0;
  const successPct = Math.round(successRate * 100);

  // Readiness heuristic: >80% success rate over 5+ cycles
  const isReady = successRate >= 0.8 && totalCycles >= 5;

  return (
    <div className={`rounded-md border px-4 py-3 space-y-1.5 ${
      isReady
        ? "border-primary/30 bg-surface-selected dark:bg-primary/10"
        : "border-border bg-surface-pane"
    }`}>
      <div className="flex items-center justify-between">
        <span className="text-xs font-medium text-muted-foreground uppercase tracking-wider">
          Autonomy readiness
        </span>
        {isReady && (
          <span className="inline-flex items-center gap-1 text-xs font-medium text-primary">
            <ArrowUpRight className="h-3 w-3" />
            Ready to advance
          </span>
        )}
      </div>
      <p className="text-xs text-foreground">
        {successPct}% success rate over {totalCycles} analysis cycles
        {decisionSummary.total_delegated > 0 && (
          <span className="text-muted-foreground">
            {" "}({decisionSummary.succeeded}/{decisionSummary.total_delegated} delegated tasks succeeded)
          </span>
        )}
      </p>
      {isReady ? (
        <p className="text-xs text-primary">
          Consider advancing to <span className="font-medium">{AUTONOMY_LABELS[nextLevel]}</span>.
          This would let the PM {nextLevel === "auto_simple"
            ? "auto-create sessions for bounded work and handle routine issue actions"
            : "act automatically on most policy-compliant work"
          }.
        </p>
      ) : (
        <p className="text-xs text-muted-foreground">
          {successRate < 0.8
            ? `Success rate needs to reach 80% (currently ${successPct}%) before advancing to ${AUTONOMY_LABELS[nextLevel]}.`
            : `Need ${Math.max(1, 5 - totalCycles)} more analysis cycles before readiness assessment.`
          }
        </p>
      )}
    </div>
  );
}
