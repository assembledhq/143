"use client";

import { AlertTriangle, CheckCircle2, GitMerge, GitPullRequest, Loader2, Wrench } from "lucide-react";

import type { PullRequestHealthResponse } from "@/lib/types";
import { cn, formatTimeAgo } from "@/lib/utils";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";

// PRBannerAction names every action the banner can launch. The pending value
// is shared across buttons so they can disable each other while one is in
// flight; the union is intentionally explicit so the spinner/label switch in
// each button is type-checked.
export type PRBannerAction = "fix_tests" | "resolve_conflicts" | "merge" | null;

type PRHealthBannerProps = {
  health: PullRequestHealthResponse;
  pendingAction: PRBannerAction;
  repairError?: string | null;
  onFixTests: () => void;
  onResolveConflicts: () => void;
  onMerge: () => void;
};

export function PRHealthBanner({
  health,
  pendingAction,
  repairError,
  onFixTests,
  onResolveConflicts,
  onMerge,
}: PRHealthBannerProps) {
  const isHealthy = !health.can_fix_tests && !health.can_resolve_conflicts;
  const syncedLabel = health.github_state_synced_at ? formatTimeAgo(health.github_state_synced_at) : "Syncing";
  const hasActionableButton = health.can_resolve_conflicts || health.can_fix_tests || health.can_merge;

  return (
    <Card className="border-border/60">
      <CardContent className="p-4">
        <div className="flex items-start justify-between gap-3">
          <div className="min-w-0 space-y-2">
            <div className="flex items-center gap-2">
              <div className={cn(
                "flex h-8 w-8 items-center justify-center rounded-full",
                isHealthy ? "bg-emerald-50 text-emerald-700 dark:bg-emerald-950/30 dark:text-emerald-400" : "bg-amber-50 text-amber-700 dark:bg-amber-950/30 dark:text-amber-400",
              )}>
                {isHealthy ? <CheckCircle2 className="h-4 w-4" /> : <GitPullRequest className="h-4 w-4" />}
              </div>
              <div className="min-w-0">
                <div className="text-sm font-medium text-foreground">PR health</div>
                <div className="text-sm text-muted-foreground">
                  PR #{health.pull_request_number} · {health.repository}
                </div>
              </div>
            </div>

            <p className="text-sm text-foreground">{health.summary}</p>

            <div className="flex flex-wrap items-center gap-2">
              <Badge variant="secondary" className="text-xs">
                Synced {syncedLabel}
              </Badge>
              {health.has_conflicts && (
                <Badge variant="secondary" className="bg-amber-50 text-amber-700 dark:bg-amber-950/30 dark:text-amber-400 text-xs">
                  conflicts
                </Badge>
              )}
              {health.failing_test_count > 0 && (
                <Badge variant="secondary" className="bg-destructive/10 text-destructive text-xs">
                  {health.failing_test_count} failing test{health.failing_test_count === 1 ? "" : "s"}
                </Badge>
              )}
              {health.obsolete_active_repair_sessions && (
                <Badge variant="secondary" className="text-xs">
                  newer repair context available
                </Badge>
              )}
            </div>

            {repairError && (
              <div className="flex items-start gap-2 rounded-md border border-destructive/20 bg-destructive/5 px-3 py-2 text-xs text-destructive">
                <AlertTriangle className="mt-0.5 h-3.5 w-3.5 shrink-0" />
                <span>{repairError}</span>
              </div>
            )}

            {hasActionableButton && (
              <div className="space-y-2">
                <div className="flex flex-wrap gap-2">
                  {health.can_resolve_conflicts && (
                    <Button
                      size="sm"
                      variant="outline"
                      disabled={pendingAction !== null}
                      onClick={onResolveConflicts}
                    >
                      {pendingAction === "resolve_conflicts" ? (
                        <Loader2 className="mr-1.5 h-3.5 w-3.5 animate-spin" />
                      ) : (
                        <Wrench className="mr-1.5 h-3.5 w-3.5" />
                      )}
                      {pendingAction === "resolve_conflicts" ? "Opening repair session…" : "Resolve conflicts"}
                    </Button>
                  )}
                  {health.can_fix_tests && (
                    <Button
                      size="sm"
                      variant="outline"
                      disabled={pendingAction !== null}
                      onClick={onFixTests}
                    >
                      {pendingAction === "fix_tests" ? (
                        <Loader2 className="mr-1.5 h-3.5 w-3.5 animate-spin" />
                      ) : (
                        <Wrench className="mr-1.5 h-3.5 w-3.5" />
                      )}
                      {pendingAction === "fix_tests" ? "Opening repair session…" : "Fix tests"}
                    </Button>
                  )}
                  {health.can_merge && (
                    <Button
                      size="sm"
                      variant="default"
                      disabled={pendingAction !== null}
                      onClick={onMerge}
                    >
                      {pendingAction === "merge" ? (
                        <Loader2 className="mr-1.5 h-3.5 w-3.5 animate-spin" />
                      ) : (
                        <GitMerge className="mr-1.5 h-3.5 w-3.5" />
                      )}
                      {pendingAction === "merge" ? "Merging…" : "Merge"}
                    </Button>
                  )}
                </div>
                {health.can_resolve_conflicts && health.can_fix_tests && (
                  <p className="text-xs text-muted-foreground">
                    Resolve conflicts first. CI may need to rerun afterward.
                  </p>
                )}
              </div>
            )}
          </div>
        </div>
      </CardContent>
    </Card>
  );
}
