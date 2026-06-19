"use client";

import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { CheckCircle2, XCircle, Clock, Minus, TrendingUp, TrendingDown } from "lucide-react";
import { Card, CardContent } from "@/components/ui/card";
import { api } from "@/lib/api";
import type { PMDecisionView, PMDecisionSummary } from "@/lib/types";

function formatDate(dateStr: string): string {
  const d = new Date(dateStr);
  return d.toLocaleDateString(undefined, { month: "short", day: "numeric" });
}

function DecisionBadge({ decision }: { decision: string }) {
  const styles: Record<string, string> = {
    delegate: "bg-info/10 text-info",
    skip: "bg-muted text-muted-foreground",
    cluster: "bg-purple-500/10 text-purple-700 dark:text-purple-400",
  };
  return (
    <span className={`inline-flex items-center rounded-full px-2 py-0.5 text-xs font-medium ${styles[decision] ?? styles.skip}`}>
      {decision === "delegate" ? "Delegated" : decision === "cluster" ? "Clustered" : "Skipped"}
    </span>
  );
}

function OutcomeCell({ outcome }: { outcome?: string }) {
  if (outcome === "succeeded") {
    return (
      <span className="flex items-center gap-1 text-success text-xs font-medium">
        <CheckCircle2 className="h-3.5 w-3.5" />
        Succeeded
      </span>
    );
  }
  if (outcome === "failed") {
    return (
      <span className="flex items-center gap-1 text-destructive text-xs font-medium">
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
  const rate = Math.round((summary.succeeded / summary.total_delegated) * 100);

  return (
    <div className="grid grid-cols-4 gap-4">
      <Card>
        <CardContent className="pt-4 pb-3 text-center">
          <p className="text-2xl font-semibold tabular-nums">{rate}%</p>
          <p className="text-xs text-muted-foreground mt-0.5">Success rate</p>
        </CardContent>
      </Card>
      <Card>
        <CardContent className="pt-4 pb-3 text-center">
          <p className="text-2xl font-semibold tabular-nums text-success">
            <TrendingUp className="h-4 w-4 inline mr-1" />
            {summary.succeeded}
          </p>
          <p className="text-xs text-muted-foreground mt-0.5">Succeeded</p>
        </CardContent>
      </Card>
      <Card>
        <CardContent className="pt-4 pb-3 text-center">
          <p className="text-2xl font-semibold tabular-nums text-destructive">
            <TrendingDown className="h-4 w-4 inline mr-1" />
            {summary.failed}
          </p>
          <p className="text-xs text-muted-foreground mt-0.5">Failed</p>
        </CardContent>
      </Card>
      <Card>
        <CardContent className="pt-4 pb-3 text-center">
          <p className="text-2xl font-semibold tabular-nums text-muted-foreground">{summary.still_open}</p>
          <p className="text-xs text-muted-foreground mt-0.5">Still open</p>
        </CardContent>
      </Card>
    </div>
  );
}

type DecisionFilter = "all" | "delegate" | "skip" | "cluster";
type OutcomeFilter = "all" | "succeeded" | "failed" | "still_open";

function DecisionRow({ decision }: { decision: PMDecisionView }) {
  const [expanded, setExpanded] = useState(false);

  return (
    <div>
      <button
        onClick={() => setExpanded(!expanded)}
        className="flex items-center gap-3 w-full py-2.5 px-4 border-b border-border last:border-b-0 text-xs text-left hover:bg-muted/30 transition-colors"
      >
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
      </button>
      {expanded && decision.reasoning && (
        <div className="px-4 py-2.5 bg-muted/20 border-b border-border text-xs text-muted-foreground">
          <span className="font-medium text-foreground">Reasoning:</span> {decision.reasoning}
        </div>
      )}
    </div>
  );
}

export function DecisionsView() {
  const [decisionFilter, setDecisionFilter] = useState<DecisionFilter>("all");
  const [outcomeFilter, setOutcomeFilter] = useState<OutcomeFilter>("all");

  const { data, isLoading, error } = useQuery({
    queryKey: ["pm", "decisions", decisionFilter, outcomeFilter],
    queryFn: () =>
      api.pm.decisions({
        limit: 200,
        decision_type: decisionFilter !== "all" ? decisionFilter : undefined,
        outcome: outcomeFilter !== "all" ? outcomeFilter : undefined,
      }),
    refetchInterval: 30000,
  });

  if (isLoading) {
    return (
      <Card>
        <CardContent className="py-12 text-center text-xs text-muted-foreground">
          Loading decisions...
        </CardContent>
      </Card>
    );
  }

  if (error) {
    return (
      <Card>
        <CardContent className="py-12 text-center text-xs text-muted-foreground">
          Failed to load decision history.
        </CardContent>
      </Card>
    );
  }

  const allDecisions = data?.data ?? [];
  const summary = data?.summary;

  if (allDecisions.length === 0) {
    return (
      <Card>
        <CardContent className="py-12 text-center text-xs text-muted-foreground">
          No decisions yet. Run an analysis to start building decision history.
        </CardContent>
      </Card>
    );
  }

  // Server-side filtering is applied via query params; use results directly.
  const decisions = allDecisions;

  const filterButtons: { value: DecisionFilter; label: string }[] = [
    { value: "all", label: "All" },
    { value: "delegate", label: "Delegated" },
    { value: "skip", label: "Skipped" },
    { value: "cluster", label: "Clustered" },
  ];

  const outcomeButtons: { value: OutcomeFilter; label: string }[] = [
    { value: "all", label: "All outcomes" },
    { value: "succeeded", label: "Succeeded" },
    { value: "failed", label: "Failed" },
    { value: "still_open", label: "Still open" },
  ];

  return (
    <div className="space-y-4">
      {summary && <SummaryBar summary={summary} />}

      {/* Filters */}
      <div className="flex flex-wrap gap-4">
        <div className="flex gap-1">
          {filterButtons.map((btn) => (
            <button
              key={btn.value}
              onClick={() => setDecisionFilter(btn.value)}
              className={`px-2.5 py-1 rounded-md text-xs font-medium transition-colors ${
                decisionFilter === btn.value
                  ? "bg-primary/10 text-primary"
                  : "text-muted-foreground hover:text-foreground hover:bg-muted/50"
              }`}
            >
              {btn.label}
            </button>
          ))}
        </div>
        <div className="flex gap-1">
          {outcomeButtons.map((btn) => (
            <button
              key={btn.value}
              onClick={() => setOutcomeFilter(btn.value)}
              className={`px-2.5 py-1 rounded-md text-xs font-medium transition-colors ${
                outcomeFilter === btn.value
                  ? "bg-primary/10 text-primary"
                  : "text-muted-foreground hover:text-foreground hover:bg-muted/50"
              }`}
            >
              {btn.label}
            </button>
          ))}
        </div>
        {(decisionFilter !== "all" || outcomeFilter !== "all") && (
          <span className="text-xs text-muted-foreground self-center">
            {decisions.length} decisions
          </span>
        )}
      </div>

      <Card>
        <CardContent className="p-0">
          <div className="flex items-center gap-3 px-4 py-2.5 border-b border-border bg-muted/30 text-xs font-medium text-muted-foreground uppercase tracking-wider">
            <span className="w-16 shrink-0">Date</span>
            <span className="w-36 shrink-0">Project</span>
            <span className="flex-1 min-w-0">Issue</span>
            <span className="w-20 shrink-0">Decision</span>
            <span className="w-24 shrink-0">Outcome</span>
          </div>
          {decisions.length === 0 ? (
            <div className="px-4 py-8 text-center text-xs text-muted-foreground">
              No decisions match the current filters.
            </div>
          ) : (
            decisions.map((d) => (
              <DecisionRow key={d.id} decision={d} />
            ))
          )}
        </CardContent>
      </Card>
    </div>
  );
}
