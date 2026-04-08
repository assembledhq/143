"use client";

import { Badge } from "@/components/ui/badge";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import type { PMTask } from "@/lib/types";

const statusStyles: Record<string, string> = {
  delegated: "bg-emerald-500/10 text-emerald-700 dark:text-emerald-400",
  skipped_capacity: "bg-muted text-muted-foreground",
  pending: "bg-yellow-500/10 text-yellow-700 dark:text-yellow-400",
};

export function TaskCard({ task }: { task: PMTask }) {
  const status = task.status ?? "pending";

  return (
    <Card>
      <CardHeader className="space-y-2">
        <div className="flex items-center justify-between">
          <CardTitle className="text-sm">
            #{task.rank} · {task.title}
          </CardTitle>
          <Badge className={statusStyles[status] ?? statusStyles.pending}>
            {status.replace("_", " ")}
          </Badge>
        </div>
        <div className="flex flex-wrap gap-2">
          <Badge variant="outline" className="text-xs">
            {task.complexity}
          </Badge>
          <Badge variant="outline" className="text-xs">
            {task.confidence} confidence
          </Badge>
          {task.issue_ids.map((id) => (
            <Badge key={id} variant="secondary" className="text-xs">
              {id.slice(0, 8)}
            </Badge>
          ))}
        </div>
      </CardHeader>
      <CardContent className="space-y-3 text-sm">
        <div>
          <p className="text-xs font-medium text-muted-foreground">Reasoning</p>
          <p>{task.reasoning}</p>
        </div>
        <div>
          <p className="text-xs font-medium text-muted-foreground">Approach</p>
          <p>{task.approach}</p>
        </div>
        {task.risk && (
          <div>
            <p className="text-xs font-medium text-muted-foreground">Risk</p>
            <p>{task.risk}</p>
          </div>
        )}
      </CardContent>
    </Card>
  );
}
