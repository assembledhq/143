"use client";

import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import type { PMPlan, PMDecisionSummary } from "@/lib/types";

function formatDay(dateStr: string): string {
  const d = new Date(dateStr);
  const now = new Date();
  const isToday = d.toDateString() === now.toDateString();
  const yesterday = new Date(now);
  yesterday.setDate(yesterday.getDate() - 1);
  const isYesterday = d.toDateString() === yesterday.toDateString();
  if (isToday) return "Today";
  if (isYesterday) return "Yesterday";
  return d.toLocaleDateString(undefined, { month: "short", day: "numeric" });
}

interface RecentActivityProps {
  plans: PMPlan[];
  summary: PMDecisionSummary | undefined;
}

export function RecentActivity({ plans, summary }: RecentActivityProps) {
  if (plans.length === 0) {
    return (
      <Card>
        <CardHeader>
          <CardTitle className="text-sm">Recent activity</CardTitle>
        </CardHeader>
        <CardContent className="text-sm text-muted-foreground">
          No activity yet. Run an analysis to get started.
        </CardContent>
      </Card>
    );
  }

  // Group plans by day
  const byDay = new Map<string, PMPlan[]>();
  for (const plan of plans) {
    const day = formatDay(plan.created_at);
    if (!byDay.has(day)) byDay.set(day, []);
    byDay.get(day)!.push(plan);
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle className="text-sm">Recent activity</CardTitle>
      </CardHeader>
      <CardContent className="space-y-2">
        {Array.from(byDay.entries()).map(([day, dayPlans]) => {
          const totalReviewed = dayPlans.reduce((sum, p) => sum + p.issues_reviewed, 0);
          const totalDelegated = dayPlans.reduce((sum, p) => sum + p.tasks.filter((t) => t.status === "delegated").length, 0);
          const totalSkipped = dayPlans.reduce((sum, p) => sum + p.skipped_issues.length, 0);

          return (
            <div key={day} className="flex items-start gap-3 text-[13px]">
              <span className="w-20 shrink-0 text-muted-foreground font-medium">{day}</span>
              <span className="text-foreground">
                Analyzed {totalReviewed} issues
                {totalDelegated > 0 && <> &middot; {totalDelegated} delegated</>}
                {totalSkipped > 0 && <> &middot; {totalSkipped} skipped</>}
              </span>
            </div>
          );
        })}
        {summary && summary.total_delegated > 0 && (
          <div className="pt-1 border-t border-border mt-2 text-xs text-muted-foreground">
            Overall: {summary.succeeded}/{summary.total_delegated} sessions succeeded
          </div>
        )}
      </CardContent>
    </Card>
  );
}
