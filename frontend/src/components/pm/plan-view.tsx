"use client";

import { Badge } from "@/components/ui/badge";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Separator } from "@/components/ui/separator";
import { TaskCard } from "@/components/pm/task-card";
import type { PMPlan } from "@/lib/types";

export function PlanView({ plan }: { plan: PMPlan }) {
  return (
    <div className="space-y-6">
      <Card>
        <CardHeader>
          <CardTitle className="text-sm">Situation Analysis</CardTitle>
        </CardHeader>
        <CardContent className="text-sm text-muted-foreground">
          {plan.analysis || "No analysis provided."}
        </CardContent>
      </Card>

      <div className="space-y-3">
        <div className="flex items-center justify-between">
          <h3 className="text-sm font-semibold">Priority Tasks</h3>
          <Badge variant="secondary" className="text-[11px]">
            {plan.tasks.length} tasks
          </Badge>
        </div>
        <div className="space-y-4">
          {plan.tasks.map((task) => (
            <TaskCard key={`${task.rank}-${task.title}`} task={task} />
          ))}
        </div>
      </div>

      {plan.clusters.length > 0 && (
        <Card>
          <CardHeader>
            <CardTitle className="text-sm">Issue Clusters</CardTitle>
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
                {index < plan.clusters.length - 1 && <Separator />}
              </div>
            ))}
          </CardContent>
        </Card>
      )}

      {plan.skipped_issues.length > 0 && (
        <Card>
          <CardHeader>
            <CardTitle className="text-sm">Skipped Issues</CardTitle>
          </CardHeader>
          <CardContent className="space-y-4 text-sm">
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
                {index < plan.skipped_issues.length - 1 && <Separator />}
              </div>
            ))}
          </CardContent>
        </Card>
      )}
    </div>
  );
}
