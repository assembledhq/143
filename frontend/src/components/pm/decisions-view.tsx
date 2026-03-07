"use client";

import { useQuery } from "@tanstack/react-query";
import { CheckCircle2, XCircle, Clock, Minus } from "lucide-react";
import { Card, CardContent } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { api } from "@/lib/api";
import type { PMDecisionView, PMDecisionSummary } from "@/lib/types";

function formatDate(dateStr: string): string {
  const d = new Date(dateStr);
  return d.toLocaleDateString(undefined, { month: "short", day: "numeric" });
}

function DecisionBadge({ decision }: { decision: string }) {
  const styles: Record<string, string> = {
    delegate: "bg-blue-100 text-blue-800",
    skip: "bg-gray-100 text-gray-700",
    cluster: "bg-purple-100 text-purple-800",
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
      <span className="flex items-center gap-1 text-green-700 text-xs font-medium">
        <CheckCircle2 className="h-3.5 w-3.5" />
        Succeeded
      </span>
    );
  }
  if (outcome === "failed") {
    return (
      <span className="flex items-center gap-1 text-red-700 text-xs font-medium">
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

function SummaryBar({ summary }: { summary: PMDecisionSummary }) {
  if (summary.total_delegated === 0) return null;
  const rate = summary.total_delegated > 0
    ? Math.round((summary.succeeded / summary.total_delegated) * 100)
    : 0;

  return (
    <div className="flex flex-wrap items-center gap-4 text-sm">
      <span className="font-medium text-foreground">
        Success rate: {rate}%
      </span>
      <span className="text-muted-foreground">
        ({summary.succeeded}/{summary.total_delegated} delegated tasks succeeded)
      </span>
      {summary.still_open > 0 && (
        <Badge variant="outline" className="text-[11px]">
          {summary.still_open} still open
        </Badge>
      )}
    </div>
  );
}

function DecisionRow({ decision }: { decision: PMDecisionView }) {
  return (
    <div className="flex items-center gap-3 py-2.5 px-4 border-b border-border last:border-b-0 text-sm">
      <span className="w-16 text-xs text-muted-foreground shrink-0">
        {formatDate(decision.created_at)}
      </span>
      <span className="w-36 truncate text-xs text-muted-foreground shrink-0">
        {decision.project_title || <Minus className="h-3 w-3 inline text-muted-foreground/50" />}
      </span>
      <span className="flex-1 min-w-0 truncate text-xs">
        {decision.issue_title || decision.issue_id?.slice(0, 8) || "—"}
      </span>
      <div className="w-20 shrink-0">
        <DecisionBadge decision={decision.decision} />
      </div>
      <div className="w-24 shrink-0">
        <OutcomeCell outcome={decision.outcome} />
      </div>
    </div>
  );
}

export function DecisionsView() {
  const { data, isLoading, error } = useQuery({
    queryKey: ["pm", "decisions"],
    queryFn: () => api.pm.decisions({ limit: 50 }),
    refetchInterval: 30000,
  });

  if (isLoading) {
    return (
      <Card>
        <CardContent className="py-12 text-center text-sm text-muted-foreground">
          Loading decisions...
        </CardContent>
      </Card>
    );
  }

  if (error) {
    return (
      <Card>
        <CardContent className="py-12 text-center text-sm text-muted-foreground">
          Failed to load decision history.
        </CardContent>
      </Card>
    );
  }

  const decisions = data?.data ?? [];
  const summary = data?.summary;

  if (decisions.length === 0) {
    return (
      <Card>
        <CardContent className="py-12 text-center text-sm text-muted-foreground">
          No decisions yet. Run an analysis to start building decision history.
        </CardContent>
      </Card>
    );
  }

  return (
    <div className="space-y-4">
      {summary && <SummaryBar summary={summary} />}

      <Card>
        <CardContent className="p-0">
          <div className="flex items-center gap-3 px-4 py-2.5 border-b border-border bg-muted/30 text-[11px] font-medium text-muted-foreground uppercase tracking-wider">
            <span className="w-16 shrink-0">Date</span>
            <span className="w-36 shrink-0">Project</span>
            <span className="flex-1 min-w-0">Issue</span>
            <span className="w-20 shrink-0">Decision</span>
            <span className="w-24 shrink-0">Outcome</span>
          </div>
          {decisions.map((d) => (
            <DecisionRow key={d.id} decision={d} />
          ))}
        </CardContent>
      </Card>
    </div>
  );
}
