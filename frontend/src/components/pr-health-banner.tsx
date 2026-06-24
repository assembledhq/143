"use client";

import { AlertTriangle, CheckCircle2, ChevronDown, ClipboardList, ExternalLink, GitMerge, GitPullRequest, Loader2, Upload, Wrench } from "lucide-react";
import Link from "next/link";

import type { PullRequestHealthResponse } from "@/lib/types";
import { cn } from "@/lib/utils";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { DisabledTooltip } from "@/components/ui/disabled-tooltip";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from "@/components/ui/dropdown-menu";
import { HoverCard, HoverCardContent, HoverCardTrigger } from "@/components/ui/hover-card";
import { SyncTimeText } from "@/components/sync-time-text";
import { deriveMergeActionState, deriveMergeWhenReadyActionState, hasRepairableFailedChecks, prHealthBlocksPRActions } from "@/lib/session-pr-action-state";

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

export type ReviewPRAction = {
  disabled: boolean;
  spinning: boolean;
  title?: string;
  onClick: () => void;
};

type PRHealthBannerProps = {
  health: PullRequestHealthResponse;
  currentSessionId?: string;
  currentThreadId?: string | null;
  pendingAction: PRBannerAction;
  repairError?: string | null;
  mergeAuthRequired?: boolean;
  mergeWhenReadyPending?: boolean;
  onFixTests: () => void;
  onFixTestsWithoutPushing?: () => void;
  onResolveConflicts: () => void;
  onResolveConflictsWithoutPushing?: () => void;
  onMerge: () => void;
  onQueueMergeWhenReady?: () => void;
  onCancelMergeWhenReady?: () => void;
  onOpenRepairSession?: (sessionId: string, threadId?: string) => void;
  pushChanges?: PushChangesAction;
  reviewAction?: ReviewPRAction;
};

export function PRHealthBanner({
  health,
  currentSessionId,
  currentThreadId,
  pendingAction,
  repairError,
  mergeAuthRequired = false,
  mergeWhenReadyPending = false,
  onFixTests,
  onFixTestsWithoutPushing,
  onResolveConflicts,
  onResolveConflictsWithoutPushing,
  onMerge,
  onQueueMergeWhenReady,
  onCancelMergeWhenReady,
  onOpenRepairSession,
  pushChanges,
  reviewAction,
}: PRHealthBannerProps) {
  const activeRepairState = deriveActiveRepairState(health.active_repairs, currentSessionId, currentThreadId);
  const prHealthBlocked = prHealthBlocksPRActions(health);
  const isRepositoryDisconnected = prHealthBlocked && health.sync_blocker === "repository_disconnected";
  const isHealthy = !prHealthBlocked && activeRepairState.label === null && health.can_merge;
  const orderedChecks = [...(health.checks ?? [])]
    .map((check) => ({ ...check, status: normalizeCheckStatus(check.status) }))
    .sort((a, b) => statusRank(a.status) - statusRank(b.status) || a.name.localeCompare(b.name));
  const canShowResolveConflictsButton = !prHealthBlocked && health.can_resolve_conflicts && !activeRepairState.suppressResolveConflicts;
  const canShowFixTestsButton = !prHealthBlocked && hasRepairableFailedChecks({ ...health, checks: orderedChecks }) && !activeRepairState.suppressFixTests;
  const mergeAction = deriveMergeActionState({
    health: { ...health, checks: orderedChecks },
    hasActiveRepair: activeRepairState.suppressMerge,
    pendingAction,
  });
  const mergeWhenReadyAction = deriveMergeWhenReadyActionState({
    health: { ...health, checks: orderedChecks },
    hasActiveRepair: activeRepairState.suppressMerge,
    pendingAction,
    pendingMergeWhenReady: mergeWhenReadyPending,
  });
  const canShowMergeButton = !prHealthBlocked && mergeAction.visible;
  const canShowMergeWhenReady = !prHealthBlocked && mergeWhenReadyAction.visible && Boolean(onQueueMergeWhenReady || onCancelMergeWhenReady);
  const canShowReviewAction = !prHealthBlocked && !!reviewAction;
  const canShowPushChanges = !prHealthBlocked && !!pushChanges;
  const hasActionableButton =
    !!activeRepairState.label ||
    canShowResolveConflictsButton ||
    canShowFixTestsButton ||
    canShowMergeButton ||
    canShowMergeWhenReady ||
    canShowReviewAction ||
    canShowPushChanges ||
    !!activeRepairState.openSessionID;
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
                isHealthy ? "bg-success/10 text-success" : "bg-warning/10 text-warning",
              )}>
                {isHealthy ? <CheckCircle2 className="h-4 w-4" /> : isRepositoryDisconnected ? <AlertTriangle className="h-4 w-4" /> : <GitPullRequest className="h-4 w-4" />}
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
              {isRepositoryDisconnected ? (
                <span className="text-xs font-medium text-warning">Sync blocked</span>
              ) : (
                <SyncTimeText syncedAt={health.github_state_synced_at} prefix="Synced" />
              )}
              {isRepositoryDisconnected && (
                <Badge variant="secondary" className="bg-warning/10 text-warning text-xs">
                  Repository disconnected
                </Badge>
              )}
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

            {isRepositoryDisconnected && (
              <div className="flex flex-col gap-2 rounded-md border border-warning/20 bg-warning/5 px-3 py-2 text-xs text-warning sm:flex-row sm:items-center sm:justify-between">
                <div className="flex min-w-0 items-start gap-2">
                  <AlertTriangle className="mt-0.5 h-3.5 w-3.5 shrink-0" />
                  <span className="text-warning">Reconnect this repository to update PR status and resume PR actions.</span>
                </div>
                <Button asChild size="sm" variant="outline" className="w-fit bg-background text-foreground">
                  <Link href="/settings/integrations">
                    Open GitHub settings
                  </Link>
                </Button>
              </div>
            )}

            {hasActionableButton && (
              <div className="space-y-2">
                {activeRepairState.label && pendingAction === null && (
                  <div className="flex flex-wrap items-center gap-2">
                    <Badge variant="secondary" className="text-xs">
                      {activeRepairState.label}
                    </Badge>
                    {activeRepairState.openSessionID && onOpenRepairSession && (
                      <Button
                        size="sm"
                        variant="outline"
                        onClick={() => onOpenRepairSession(activeRepairState.openSessionID!, activeRepairState.openThreadID ?? undefined)}
                      >
                        Open repair session
                      </Button>
                    )}
                  </div>
                )}
                <div className="flex flex-wrap gap-2">
                  {canShowMergeButton && (
                    <div className="inline-flex">
                      <DisabledTooltip disabled={mergeAction.disabled} content={mergeAction.disabledReason}>
                        <Button
                          size="sm"
                          variant="default"
                          className={cn(canShowMergeWhenReady && "rounded-r-none border-r border-primary-foreground/20")}
                          disabled={mergeAction.disabled}
                          title={mergeAction.disabledReason ?? "Merge PR (p m)"}
                          onClick={onMerge}
                        >
                          {mergeAction.spinning ? (
                            <Loader2 className="mr-1.5 h-3.5 w-3.5 animate-spin" />
                          ) : (
                            <GitMerge className="mr-1.5 h-3.5 w-3.5" />
                          )}
                          {mergeAction.label}
                        </Button>
                      </DisabledTooltip>
                      {canShowMergeWhenReady && (
                        <DropdownMenu>
                          <DropdownMenuTrigger asChild>
                            <Button
                              size="icon"
                              variant="default"
                              className="h-7 w-7 rounded-l-none"
                              disabled={mergeWhenReadyAction.disabled}
                              title={mergeWhenReadyAction.disabledReason ?? "More merge actions"}
                              aria-label="More merge actions"
                            >
                              {mergeWhenReadyAction.spinning ? (
                                <Loader2 className="h-3.5 w-3.5 animate-spin" />
                              ) : (
                                <ChevronDown className="h-3.5 w-3.5" />
                              )}
                            </Button>
                          </DropdownMenuTrigger>
                          <DropdownMenuContent align="end">
                            <DropdownMenuItem
                              onClick={health.merge_when_ready.state === "queued" ? onCancelMergeWhenReady : onQueueMergeWhenReady}
                              disabled={mergeWhenReadyAction.disabled}
                              title={mergeWhenReadyAction.disabledReason}
                            >
                              <GitMerge className="h-3.5 w-3.5" />
                              {mergeWhenReadyAction.label}
                            </DropdownMenuItem>
                          </DropdownMenuContent>
                        </DropdownMenu>
                      )}
                    </div>
                  )}
                  {canShowResolveConflictsButton && (
                    <DisabledTooltip disabled={pendingAction !== null} content="Wait for the current PR action to finish">
                      <span className="inline-flex">
                        <Button
                          size="sm"
                          variant="outline"
                          className={onResolveConflictsWithoutPushing ? "rounded-r-none" : undefined}
                          disabled={pendingAction !== null}
                          title={pendingAction !== null ? "Wait for the current PR action to finish" : "Resolve conflicts (p r)"}
                          onClick={onResolveConflicts}
                        >
                          {pendingAction === "resolve_conflicts" ? (
                            <Loader2 className="mr-1.5 h-3.5 w-3.5 animate-spin" />
                          ) : (
                            <Wrench className="mr-1.5 h-3.5 w-3.5" />
                          )}
                          {pendingAction === "resolve_conflicts" ? "Opening repair session…" : "Resolve conflicts"}
                        </Button>
                        {onResolveConflictsWithoutPushing && (
                          <DropdownMenu>
                            <DropdownMenuTrigger asChild>
                              <Button
                                size="icon"
                                variant="outline"
                                className="h-7 w-7 rounded-l-none border-l-0"
                                disabled={pendingAction !== null}
                                title={pendingAction !== null ? "Wait for the current PR action to finish" : "More resolve conflicts actions"}
                                aria-label="More resolve conflicts actions"
                              >
                                <ChevronDown className="h-3.5 w-3.5" />
                              </Button>
                            </DropdownMenuTrigger>
                            <DropdownMenuContent align="end">
                              <DropdownMenuItem onClick={onResolveConflictsWithoutPushing} disabled={pendingAction !== null}>
                                <Wrench className="h-3.5 w-3.5" />
                                Resolve without pushing changes
                              </DropdownMenuItem>
                            </DropdownMenuContent>
                          </DropdownMenu>
                        )}
                      </span>
                    </DisabledTooltip>
                  )}
                  {canShowFixTestsButton && (
                    <DisabledTooltip disabled={pendingAction !== null} content="Wait for the current PR action to finish">
                      <span className="inline-flex">
                        <Button
                          size="sm"
                          variant="outline"
                          className={onFixTestsWithoutPushing ? "rounded-r-none" : undefined}
                          disabled={pendingAction !== null}
                          title={pendingAction !== null ? "Wait for the current PR action to finish" : "Fix tests (p t)"}
                          onClick={onFixTests}
                        >
                          {pendingAction === "fix_tests" ? (
                            <Loader2 className="mr-1.5 h-3.5 w-3.5 animate-spin" />
                          ) : (
                            <Wrench className="mr-1.5 h-3.5 w-3.5" />
                          )}
                          {pendingAction === "fix_tests" ? "Opening repair session…" : "Fix tests"}
                        </Button>
                        {onFixTestsWithoutPushing && (
                          <DropdownMenu>
                            <DropdownMenuTrigger asChild>
                              <Button
                                size="icon"
                                variant="outline"
                                className="h-7 w-7 rounded-l-none border-l-0"
                                disabled={pendingAction !== null}
                                title={pendingAction !== null ? "Wait for the current PR action to finish" : "More fix tests actions"}
                                aria-label="More fix tests actions"
                              >
                                <ChevronDown className="h-3.5 w-3.5" />
                              </Button>
                            </DropdownMenuTrigger>
                            <DropdownMenuContent align="end">
                              <DropdownMenuItem onClick={onFixTestsWithoutPushing} disabled={pendingAction !== null}>
                                <Wrench className="h-3.5 w-3.5" />
                                Fix without pushing changes
                              </DropdownMenuItem>
                            </DropdownMenuContent>
                          </DropdownMenu>
                        )}
                      </span>
                    </DisabledTooltip>
                  )}
                  {canShowReviewAction && reviewAction && (
                    <DisabledTooltip
                      disabled={reviewAction.disabled || pendingAction !== null}
                      content={pendingAction !== null ? "Wait for the current PR action to finish" : reviewAction.title}
                    >
                      <Button
                        size="sm"
                        variant="outline"
                        disabled={reviewAction.disabled || pendingAction !== null}
                        title={pendingAction !== null ? "Wait for the current PR action to finish" : reviewAction.title}
                        onClick={reviewAction.onClick}
                      >
                        {reviewAction.spinning ? (
                          <Loader2 className="mr-1.5 h-3.5 w-3.5 animate-spin" />
                        ) : (
                          <ClipboardList className="mr-1.5 h-3.5 w-3.5" />
                        )}
                        Review
                      </Button>
                    </DisabledTooltip>
                  )}
                  {canShowPushChanges && pushChanges && (
                    <DisabledTooltip
                      disabled={pushChanges.disabled || pendingAction !== null}
                      content={pendingAction !== null ? "Wait for the current PR action to finish" : pushChanges.title}
                    >
                      <Button
                        size="sm"
                        variant="outline"
                        disabled={pushChanges.disabled || pendingAction !== null}
                        title={pendingAction !== null ? "Wait for the current PR action to finish" : pushChanges.title ?? "Push changes (p p)"}
                        aria-keyshortcuts="p p"
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
                    </DisabledTooltip>
                  )}
                </div>
                {canShowResolveConflictsButton && canShowFixTestsButton && (
                  <p className="text-xs text-muted-foreground">
                    Resolve conflicts first. CI may need to rerun afterward.
                  </p>
                )}
                {mergeAuthRequired && canShowMergeButton && (
                  <p className="text-xs text-muted-foreground">
                    Connect your GitHub account to merge this pull request as yourself.
                  </p>
                )}
                {health.merge_when_ready.state === "queued" && (
                  <p className="text-xs text-muted-foreground">
                    Waiting for GitHub requirements.
                  </p>
                )}
                {health.merge_when_ready.state === "failed" && health.merge_when_ready.last_error && (
                  <div
                    role="status"
                    aria-label="Merge when ready stopped"
                    className="flex flex-col gap-1.5 text-xs text-muted-foreground sm:flex-row sm:items-center sm:justify-between sm:gap-2"
                  >
                    <span className="min-w-0">
                      Merge when ready stopped: {health.merge_when_ready.last_error}
                    </span>
                    {onQueueMergeWhenReady && (
                      <DisabledTooltip disabled={mergeWhenReadyAction.disabled || mergeWhenReadyPending} content={mergeWhenReadyAction.disabledReason}>
                        <Button
                          size="sm"
                          variant="ghost"
                          className="h-6 w-fit shrink-0 px-2 text-xs"
                          onClick={onQueueMergeWhenReady}
                          disabled={mergeWhenReadyAction.disabled || mergeWhenReadyPending}
                          title={mergeWhenReadyAction.disabledReason}
                          aria-label="Retry merge when ready"
                        >
                          {mergeWhenReadyAction.spinning ? "Retrying…" : "Retry"}
                        </Button>
                      </DisabledTooltip>
                    )}
                  </div>
                )}
              </div>
            )}
          </div>
        </div>
      </CardContent>
    </Card>
  );
}

function deriveActiveRepairState(
  activeRepairs: PullRequestHealthResponse["active_repairs"],
  currentSessionId?: string,
  currentThreadId?: string | null,
): {
  label: string | null;
  openSessionID: string | null;
  openThreadID: string | null;
  suppressFixTests: boolean;
  suppressResolveConflicts: boolean;
  suppressMerge: boolean;
} {
  const repairs = activeRepairs ?? [];
  const resolveConflicts = repairs.find((repair) => repair.action_type === "resolve_conflicts");
  const fixTests = repairs.find((repair) => repair.action_type === "fix_tests");
  const dominantRepair = resolveConflicts ?? fixTests ?? null;
  const repairIsInDifferentView = !!dominantRepair && (
    dominantRepair.session_id !== currentSessionId ||
    (currentThreadId != null && !!dominantRepair.thread_id && dominantRepair.thread_id !== currentThreadId)
  );

  return {
    label: dominantRepair
      ? dominantRepair.action_type === "resolve_conflicts"
        ? "Resolve conflicts running"
        : "Fix tests running"
      : null,
    openSessionID: dominantRepair && repairIsInDifferentView ? dominantRepair.session_id : null,
    openThreadID: dominantRepair?.thread_id ?? null,
    suppressFixTests: !!fixTests || !!resolveConflicts,
    suppressResolveConflicts: !!resolveConflicts,
    suppressMerge: repairs.length > 0,
  };
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

// prHealthAllowsMerge mirrors the merge gating the banner uses internally so
// callers outside the banner (keyboard shortcuts, command palette) can decide
// whether to expose the merge action without re-deriving the rule.
export function prHealthAllowsMerge(health: PullRequestHealthResponse | undefined): boolean {
  if (!health?.can_merge || !health.checks_confirmed) return false;
  return (health.checks ?? []).every((c) => c.status === "passed");
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
      return "bg-warning/10 text-warning";
    case "passed":
      return "bg-success/10 text-success";
  }
}
