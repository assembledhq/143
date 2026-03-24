"use client";

import { CheckCircle2, XCircle, Clock, Minus } from "lucide-react";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import type { PMDecisionView } from "@/lib/types";

function formatDate(dateStr: string): string {
  const d = new Date(dateStr);
  return d.toLocaleDateString(undefined, { month: "short", day: "numeric" });
}

function DecisionBadge({ decision }: { decision: string }) {
  const styles: Record<string, string> = {
    delegate: "bg-blue-500/10 text-blue-700 dark:text-blue-400",
    skip: "bg-muted text-muted-foreground",
    cluster: "bg-purple-500/10 text-purple-700 dark:text-purple-400",
  };
  return (
    <span className={`inline-flex items-center rounded-full px-2 py-0.5 text-[11px] font-medium ${styles[decision] ?? styles.skip}`}>
      {decision === "delegate" ? "Delegated" : decision === "cluster" ? "Clustered" : "Skipped"}
    </span>
  );
}

function OutcomeCell({ outcome }: { outcome?: string }) {
  if (outcome === "succeeded") {
    return (
      <span className="flex items-center gap-1 text-green-700 dark:text-green-400 text-xs font-medium">
        <CheckCircle2 className="h-3.5 w-3.5" />
        Succeeded
      </span>
    );
  }
  if (outcome === "failed") {
    return (
      <span className="flex items-center gap-1 text-red-700 dark:text-red-400 text-xs font-medium">
        <XCircle className="h-3.5 w-3.5" />
        Failed
      </span>
    );
  }
  return (
    <span className="flex items-center gap-1 text-muted-foreground text-xs">
      <Clock className="h-3.5 w-3.5" />
      Still open
    </span>
  );
}

interface DecisionsCardProps {
  decisions: PMDecisionView[];
  isLoading: boolean;
}

export function DecisionsCard({ decisions, isLoading }: DecisionsCardProps) {
  if (isLoading) {
    return (
      <Card>
        <CardHeader>
          <CardTitle className="text-sm">Recent decisions</CardTitle>
        </CardHeader>
        <CardContent className="py-6 text-center text-sm text-muted-foreground">
          Loading decisions...
        </CardContent>
      </Card>
    );
  }

  const recentDecisions = decisions.slice(0, 5);

  return (
    <Card>
      <CardHeader>
        <CardTitle className="text-sm">Recent decisions</CardTitle>
      </CardHeader>
      <CardContent className="p-0">
        {recentDecisions.length === 0 ? (
          <div className="px-4 pb-4 text-sm text-muted-foreground">
            No decisions yet. Run an analysis to start building decision history.
          </div>
        ) : (
          <>
            {recentDecisions.map((d) => (
              <div key={d.id} className="flex items-center gap-3 py-2 px-4 border-b border-border last:border-b-0 text-sm">
                <span className="w-16 text-xs text-muted-foreground shrink-0">
                  {formatDate(d.created_at)}
                </span>
                <span className="flex-1 min-w-0 truncate text-xs">
                  {d.issue_title || d.issue_id?.slice(0, 8) || <Minus className="h-3 w-3 inline text-muted-foreground/50" />}
                </span>
                <div className="w-20 shrink-0">
                  <DecisionBadge decision={d.decision} />
                </div>
                <div className="w-24 shrink-0">
                  <OutcomeCell outcome={d.outcome} />
                </div>
              </div>
            ))}
            {decisions.length > 5 && (
              <div className="px-4 py-2 text-xs text-muted-foreground">
                {decisions.length - 5} more decisions not shown
              </div>
            )}
          </>
        )}
      </CardContent>
    </Card>
  );
}
