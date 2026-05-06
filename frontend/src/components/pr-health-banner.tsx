"use client";

import { AlertTriangle, CheckCircle2, ExternalLink, GitMerge, GitPullRequest, Loader2, Upload, Wrench } from "lucide-react";

import type { PullRequestHealthResponse } from "@/lib/types";
import { cn } from "@/lib/utils";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { HoverCard, HoverCardContent, HoverCardTrigger } from "@/components/ui/hover-card";
import { SyncTimeText } from "@/components/sync-time-text";

// PRBannerAction names every action the banner can launch. The pending value
// is shared across buttons so they can disable each other while one is in
// flight; the union is intentionally explicit so the spinner/label switch in
// each button is type-checked.
export type PRBannerAction = "fix_tests" | "resolve_conflicts" | "merge" | null;
type PullRequestCheckStatus = NonNullable<PullRequestHealthResponse["checks"]>[number]["status"];

// PushChangesAction is the descriptor the parent passes when a push-to-PR
// button should appear in the banner's action row. The parent owns the full
// state machine (idle/queueing/pushing/failed/retry); the banner renders
// what it's told. Nullish means the button is hidden — render nothing.
export type PushChangesAction = {
  label: string;
  disabled: boolean;
  spinning: boolean;
  showError: boolean;
  title?: string;
  onClick: () => void;
};

type PRHealthBannerProps = {
  health: PullRequestHealthResponse;
  pendingAction: PRBannerAction;
  repairError?: string | null;
  mergeAuthRequired?: boolean;
  onFixTests: () => void;
  onResolveConflicts: () => void;
  onMerge: () => void;
  pushChanges?: PushChangesAction;
};

export function PRHealthBanner({
  health,
  pendingAction,
  repairError,
  mergeAuthRequired = false,
  onFixTests,
  onResolveConflicts,
  onMerge,
  pushChanges,
}: PRHealthBannerProps) {
  const isHealthy = !health.can_fix_tests && !health.can_resolve_conflicts;
  const orderedChecks = [...(health.checks ?? [])]
    .map((check) => ({ ...check, status: normalizeCheckStatus(check.status) }))
    .sort((a, b) => statusRank(a.status) - statusRank(b.status) || a.name.localeCompare(b.name));
  const canShowMergeButton = health.can_merge && checksAllowMerge(health.checks_confirmed, orderedChecks);
  const hasActionableButton =
    health.can_resolve_conflicts || health.can_fix_tests || canShowMergeButton || !!pushChanges;
  const failedChecks = orderedChecks.filter((check) => check.status === "failed").length;
  const failedSummaryLabel = orderedChecks.length > 0
    ? `${failedChecks}/${orderedChecks.length} failed`
    : `${health.failing_test_count} failing test${health.failing_test_count === 1 ? "" : "s"}`;

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
                <div className="text-xs text-muted-foreground">
                  PR #{health.pull_request_number} · {health.repository}
                </div>
              </div>
            </div>

            <p className="text-xs text-foreground">{health.summary}</p>

            <div className="flex flex-wrap items-center gap-2">
              <SyncTimeText syncedAt={health.github_state_synced_at} prefix="Synced" />
              {health.failing_test_count > 0 && (
                orderedChecks.length > 0 ? (
                  <HoverCard openDelay={100} closeDelay={100}>
                    <HoverCardTrigger asChild>
                      <Badge variant="secondary" className="bg-destructive/10 text-destructive text-xs cursor-default">
                        {failedSummaryLabel}
                      </Badge>
                    </HoverCardTrigger>
                    <HoverCardContent align="start" className="w-80 p-3">
                      <div className="space-y-2">
                        <div className="text-xs font-medium text-foreground">CI jobs</div>
                        <div className="space-y-1.5">
                          {orderedChecks.map((check) => (
                            check.details_url ? (
                              <a
                                key={`${check.name}-${check.status}`}
                                href={check.details_url}
                                target="_blank"
                                rel="noopener noreferrer"
                                className="flex items-center justify-between gap-3 rounded-sm px-1 py-1 text-xs transition-colors hover:bg-muted/60 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                              >
                                <div className="flex min-w-0 items-center gap-1.5">
                                  <span className="min-w-0 truncate text-foreground">{check.name}</span>
                                  <ExternalLink aria-hidden="true" className="h-3 w-3 shrink-0 text-muted-foreground" />
                                </div>
                                <Badge variant="secondary" className={cn("shrink-0 text-xs", checkStatusBadgeClassName(check.status))}>
                                  {checkStatusLabel(check.status)}
                                </Badge>
                              </a>
                            ) : (
                              <div key={`${check.name}-${check.status}`} className="flex items-center justify-between gap-3 px-1 py-1">
                                <div className="min-w-0 text-xs text-foreground truncate">{check.name}</div>
                                <Badge variant="secondary" className={cn("shrink-0 text-xs", checkStatusBadgeClassName(check.status))}>
                                  {checkStatusLabel(check.status)}
                                </Badge>
                              </div>
                            )
                          ))}
                        </div>
                      </div>
                    </HoverCardContent>
                  </HoverCard>
                ) : (
                  <Badge variant="secondary" className="bg-destructive/10 text-destructive text-xs">
                    {failedSummaryLabel}
                  </Badge>
                )
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
                  {canShowMergeButton && (
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
                  {pushChanges && (
                    <Button
                      size="sm"
                      variant="outline"
                      disabled={pushChanges.disabled || pendingAction !== null}
                      title={pushChanges.title}
                      onClick={pushChanges.onClick}
                    >
                      {pushChanges.spinning ? (
                        <Loader2 className="mr-1.5 h-3.5 w-3.5 animate-spin" />
                      ) : pushChanges.showError ? (
                        <AlertTriangle className="mr-1.5 h-3.5 w-3.5" />
                      ) : (
                        <Upload className="mr-1.5 h-3.5 w-3.5" />
                      )}
                      {pushChanges.label}
                    </Button>
                  )}
                </div>
                {health.can_resolve_conflicts && health.can_fix_tests && (
                  <p className="text-xs text-muted-foreground">
                    Resolve conflicts first. CI may need to rerun afterward.
                  </p>
                )}
                {mergeAuthRequired && canShowMergeButton && (
                  <p className="text-xs text-muted-foreground">
                    Connect your GitHub account to merge this pull request as yourself.
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

function statusRank(status: PullRequestCheckStatus) {
  switch (status) {
    case "failed":
      return 0;
    case "pending":
      return 1;
    case "passed":
      return 2;
  }
}

function normalizeCheckStatus(status?: string): PullRequestCheckStatus {
  switch (status) {
    case "failed":
    case "pending":
    case "passed":
      return status;
    default:
      return "pending";
  }
}

function checksAllowMerge(
  checksConfirmed: boolean,
  checks: Array<{ status: PullRequestCheckStatus }>,
) {
  return checks.length === 0
    ? checksConfirmed
    : checks.every((check) => check.status === "passed");
}

function checkStatusLabel(status: PullRequestCheckStatus) {
  switch (status) {
    case "failed":
      return "Failed";
    case "pending":
      return "Pending";
    case "passed":
      return "Passed";
  }
}

function checkStatusBadgeClassName(status: PullRequestCheckStatus) {
  switch (status) {
    case "failed":
      return "bg-destructive/10 text-destructive";
    case "pending":
      return "bg-amber-50 text-amber-700 dark:bg-amber-950/30 dark:text-amber-400";
    case "passed":
      return "bg-emerald-50 text-emerald-700 dark:bg-emerald-950/30 dark:text-emerald-400";
  }
}
