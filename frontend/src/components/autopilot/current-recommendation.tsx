"use client";

import { useState } from "react";
import { ChevronDown, ChevronRight, Eye, GitPullRequest, History, Search } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { TaskCard } from "@/components/pm/task-card";
import type { PMCurrentRecommendation } from "@/lib/types";

const SKIP_REASON_LABELS: Record<string, string> = {
  duplicate: "Duplicate",
  needs_human_decision: "Needs human decision",
  too_complex: "Too complex",
  misaligned: "Misaligned with direction",
  in_avoid_area: "In avoid area",
  already_in_flight: "Already in-flight",
};

interface CurrentRecommendationProps {
  recommendation: PMCurrentRecommendation | undefined;
}

export function CurrentRecommendation({ recommendation }: CurrentRecommendationProps) {
  const [showSkipped, setShowSkipped] = useState(false);

  if (!recommendation) {
    return (
      <Card>
        <CardContent className="py-12 text-center">
          <p className="text-sm text-muted-foreground">
            Run my first analysis and I&apos;ll tell you which ones matter most...
          </p>
        </CardContent>
      </Card>
    );
  }

  const stats = recommendation.context_stats;

  return (
    <div className="space-y-4">
      {/* Situation analysis */}
      <Card>
        <CardHeader>
          <CardTitle className="text-sm">Situation analysis</CardTitle>
        </CardHeader>
        <CardContent className="text-sm text-muted-foreground">
          {recommendation.analysis || "No analysis provided."}
        </CardContent>
      </Card>

      {/* Context stats strip */}
      <div className="flex flex-wrap gap-3 text-[11px] text-muted-foreground">
        <span className="inline-flex items-center gap-1">
          <Search className="h-3 w-3" />
          {stats.issues_reviewed} issues reviewed
        </span>
        <span className="inline-flex items-center gap-1">
          <History className="h-3 w-3" />
          {stats.past_decisions_reviewed} past decisions learned from
        </span>
        <span className="inline-flex items-center gap-1">
          <GitPullRequest className="h-3 w-3" />
          {stats.recent_prs_checked} PRs checked
        </span>
        <span className="inline-flex items-center gap-1">
          <Eye className="h-3 w-3" />
          {stats.in_flight_runs_checked} in-flight runs checked
        </span>
      </div>

      {/* Priority tasks */}
      <div className="space-y-3">
        <div className="flex items-center justify-between">
          <h3 className="text-sm font-semibold">Priority tasks</h3>
          <Badge variant="secondary" className="text-[11px]">
            {recommendation.tasks.length} slots used
          </Badge>
        </div>
        <div className="space-y-4">
          {recommendation.tasks.map((task, index) => (
            <TaskCard key={task.session_id ?? `${task.rank}-${index}`} task={task} />
          ))}
        </div>
      </div>

      {/* Issue clusters */}
      {recommendation.clusters.length > 0 && (
        <Card>
          <CardHeader>
            <CardTitle className="text-sm">Issue clusters</CardTitle>
          </CardHeader>
          <CardContent className="space-y-4 text-sm">
            {recommendation.clusters.map((cluster, index) => (
              <div key={`${cluster.root_cause}-${index}`} className="space-y-2">
                <div className="flex flex-wrap gap-2">
                  {cluster.issue_ids.map((id) => (
                    <Badge key={id} variant="secondary" className="text-[11px]">
                      {id.slice(0, 8)}
                    </Badge>
                  ))}
                </div>
                <p>
                  <span className="font-medium">Root cause:</span> {cluster.root_cause}
                </p>
                <p>
                  <span className="font-medium">Strategy:</span> {cluster.strategy}
                </p>
                {index < recommendation.clusters.length - 1 && <div className="border-t border-border" />}
              </div>
            ))}
          </CardContent>
        </Card>
      )}

      {/* Deprioritized issues — collapsed with enhanced skip reasoning */}
      {recommendation.skipped_issues.length > 0 && (
        <div>
          <button
            onClick={() => setShowSkipped(!showSkipped)}
            className="flex items-center gap-1.5 text-sm text-muted-foreground hover:text-foreground transition-colors"
          >
            {showSkipped ? <ChevronDown className="h-4 w-4" /> : <ChevronRight className="h-4 w-4" />}
            Deprioritized ({recommendation.skipped_issues.length} issues)
          </button>
          {showSkipped && (
            <Card className="mt-2">
              <CardContent className="space-y-4 text-sm py-4">
                {recommendation.skipped_issues.map((skip, index) => (
                  <div key={`${skip.issue_id}-${index}`} className="space-y-2">
                    <div className="flex items-center gap-2">
                      <Badge variant="outline" className="text-[11px]">
                        {skip.issue_id.slice(0, 8)}
                      </Badge>
                      <Badge variant="secondary" className="text-[11px]">
                        {SKIP_REASON_LABELS[skip.reason] ?? skip.reason.replace(/_/g, " ")}
                      </Badge>
                    </div>
                    <p className="text-muted-foreground">{skip.detail}</p>
                    {index < recommendation.skipped_issues.length - 1 && <div className="border-t border-border" />}
                  </div>
                ))}
              </CardContent>
            </Card>
          )}
        </div>
      )}
    </div>
  );
}
