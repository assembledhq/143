"use client";

import { useState } from "react";
import { ChevronDown, ChevronRight } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { TaskCard } from "@/components/pm/task-card";
import type { PMPlan } from "@/lib/types";

interface CurrentRecommendationProps {
  plan: PMPlan | undefined;
}

export function CurrentRecommendation({ plan }: CurrentRecommendationProps) {
  const [showSkipped, setShowSkipped] = useState(false);

  if (!plan) {
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

  return (
    <div className="space-y-4">
      {/* Situation analysis */}
      <Card>
        <CardHeader>
          <CardTitle className="text-sm">Situation analysis</CardTitle>
        </CardHeader>
        <CardContent className="text-sm text-muted-foreground">
          {plan.analysis || "No analysis provided."}
        </CardContent>
      </Card>

      {/* Priority tasks */}
      <div className="space-y-3">
        <div className="flex items-center justify-between">
          <h3 className="text-sm font-semibold">Priority tasks</h3>
          <Badge variant="secondary" className="text-[11px]">
            {plan.tasks.length} slots used
          </Badge>
        </div>
        <div className="space-y-4">
          {plan.tasks.map((task, index) => (
            <TaskCard key={task.session_id ?? `${task.rank}-${index}`} task={task} />
          ))}
        </div>
      </div>

      {/* Issue clusters */}
      {plan.clusters.length > 0 && (
        <Card>
          <CardHeader>
            <CardTitle className="text-sm">Issue clusters</CardTitle>
          </CardHeader>
          <CardContent className="space-y-4 text-sm">
            {plan.clusters.map((cluster, index) => (
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
                {index < plan.clusters.length - 1 && <div className="border-t border-border" />}
              </div>
            ))}
          </CardContent>
        </Card>
      )}

      {/* Skipped issues — collapsed */}
      {plan.skipped_issues.length > 0 && (
        <div>
          <button
            onClick={() => setShowSkipped(!showSkipped)}
            className="flex items-center gap-1.5 text-sm text-muted-foreground hover:text-foreground transition-colors"
          >
            {showSkipped ? <ChevronDown className="h-4 w-4" /> : <ChevronRight className="h-4 w-4" />}
            {plan.skipped_issues.length} skipped issues
          </button>
          {showSkipped && (
            <Card className="mt-2">
              <CardContent className="space-y-4 text-sm py-4">
                {plan.skipped_issues.map((skip, index) => (
                  <div key={`${skip.issue_id}-${index}`} className="space-y-2">
                    <div className="flex items-center gap-2">
                      <Badge variant="outline" className="text-[11px]">
                        {skip.issue_id.slice(0, 8)}
                      </Badge>
                      <Badge variant="secondary" className="text-[11px]">
                        {skip.reason.replace("_", " ")}
                      </Badge>
                    </div>
                    <p>{skip.detail}</p>
                    {index < plan.skipped_issues.length - 1 && <div className="border-t border-border" />}
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
