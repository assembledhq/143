import type { PRCreationState, PRPushErrorCode, PRPushState, PullRequestHealthResponse } from "./types";

type RepairableFailedChecksInput = Pick<PullRequestHealthResponse, "can_fix_tests" | "failing_test_count" | "checks" | "sync_status">;

export function prHealthBlocksPRActions(health: Pick<PullRequestHealthResponse, "sync_status"> | null | undefined): boolean {
  return health?.sync_status === "blocked";
}

export function hasRepairableFailedChecks(health: RepairableFailedChecksInput | null | undefined): boolean {
  if (!health) {
    return false;
  }
  if (prHealthBlocksPRActions(health)) {
    return false;
  }
  return health.can_fix_tests || health.failing_test_count > 0 || (health.checks ?? []).some((check) => check.status === "failed");
}

export function continueFromPRBranchMessage(headRef?: string | null): string {
  const branch = headRef?.trim();
  const branchClause = branch ? ` (${branch})` : "";
  return `The PR branch has changes that are not in this session checkpoint. Fetch and reconcile the latest PR branch${branchClause} into this session, preserve the current PR branch changes, reapply any still-needed local changes, and stop for review. Do not push changes yet.`;
}

export type LifecycleActionState = {
  visible: boolean;
  disabled: boolean;
  disabledReason?: string;
};

export type LabeledLifecycleActionState = LifecycleActionState & {
  label: string;
  spinning: boolean;
  showError?: boolean;
  requiresBranchSync?: boolean;
};

export type CreatePRActionInput = {
  canShipPR: boolean;
  hasPR: boolean;
  hasSessionChanges: boolean;
  hasSnapshot: boolean;
  isRunning: boolean;
  builderReviewAllowsPR: boolean;
  snapshotUnavailable: boolean;
  snapshotMessage?: string;
  ghBlocked: boolean;
  queueingPR: boolean;
  creatingPR: boolean;
  finalizingPR: boolean;
  prState?: PRCreationState;
  prCreationError?: string;
  localError?: string;
  hasRecoverableError: boolean;
};

export function deriveCreatePRActionState(input: CreatePRActionInput): LabeledLifecycleActionState {
  const visible = input.canShipPR && !input.hasPR && (
    input.hasSessionChanges ||
    input.hasSnapshot ||
    input.snapshotUnavailable ||
    input.queueingPR ||
    input.creatingPR ||
    input.finalizingPR ||
    input.prState === "failed" ||
    input.hasRecoverableError
  );

  if (!visible) {
    return { visible: false, disabled: false, label: "Create PR", spinning: false };
  }

  if (input.queueingPR) {
    return {
      visible: true,
      disabled: true,
      disabledReason: "Sending the PR request to the queue",
      label: "Queueing PR…",
      spinning: true,
    };
  }

  if (input.creatingPR) {
    return {
      visible: true,
      disabled: true,
      disabledReason: "Pushing changes and opening the pull request",
      label: "Creating PR…",
      spinning: true,
    };
  }

  if (input.finalizingPR) {
    return {
      visible: true,
      disabled: true,
      disabledReason: "Finalizing the pull request",
      label: "Finalizing PR…",
      spinning: true,
    };
  }

  if (input.localError) {
    return {
      visible: true,
      disabled: false,
      disabledReason: input.localError,
      label: "Retry",
      spinning: false,
      showError: true,
    };
  }

  if (input.prState === "failed") {
    return {
      visible: true,
      disabled: false,
      disabledReason: input.prCreationError || "PR creation failed",
      label: "Retry",
      spinning: false,
      showError: true,
    };
  }

  if (input.isRunning) {
    return {
      visible: true,
      disabled: true,
      disabledReason: "Wait for the session to finish before creating a PR",
      label: "Create PR",
      spinning: false,
    };
  }

  if (input.snapshotUnavailable || !input.hasSnapshot) {
    return {
      visible: true,
      disabled: true,
      disabledReason: input.snapshotMessage || "A reusable sandbox snapshot is required before creating a PR",
      label: "Create PR",
      spinning: false,
    };
  }

  if (!input.builderReviewAllowsPR) {
    return {
      visible: true,
      disabled: true,
      disabledReason: "Run readiness checks successfully before creating a PR",
      label: "Create PR",
      spinning: false,
    };
  }

  if (input.ghBlocked) {
    return {
      visible: true,
      disabled: true,
      disabledReason: "Connect your GitHub account to create PRs",
      label: "Create PR",
      spinning: false,
    };
  }

  return { visible: true, disabled: false, label: "Create PR", spinning: false };
}

export type PushChangesActionInput = {
  canShipPR: boolean;
  hasOpenPR: boolean;
  hasUnpushedChanges: boolean;
  hasSnapshot: boolean;
  isRunning: boolean;
  builderReviewAllowsPR: boolean;
  snapshotUnavailable: boolean;
  snapshotMessage?: string;
  ghBlocked: boolean;
  queueingPush: boolean;
  pushingChanges: boolean;
  prHealthBlocked?: boolean;
  pushState?: PRPushState;
  pushError?: string;
  pushErrorCode?: PRPushErrorCode;
  localError?: string;
  localErrorCode?: string;
};

export function derivePushChangesActionState(input: PushChangesActionInput): LabeledLifecycleActionState {
  if (input.prHealthBlocked) {
    return { visible: false, disabled: false, label: "Push changes", spinning: false };
  }

  const visible = input.canShipPR && input.hasOpenPR && (
    input.hasUnpushedChanges ||
    input.queueingPush ||
    input.pushingChanges ||
    input.pushState === "failed" ||
    Boolean(input.localError)
  );

  if (!visible) {
    return { visible: false, disabled: false, label: "Push changes", spinning: false };
  }

  if (input.queueingPush) {
    return {
      visible: true,
      disabled: true,
      disabledReason: "Sending the push request to the queue",
      label: "Queueing…",
      spinning: true,
    };
  }

  if (input.pushingChanges) {
    return {
      visible: true,
      disabled: true,
      disabledReason: "Pushing changes to the PR branch",
      label: "Pushing…",
      spinning: true,
    };
  }

  const hasBranchDivergedError =
    input.pushErrorCode === "branch_diverged" ||
    input.localErrorCode === "PR_BRANCH_DIVERGED";
  const hasRetryablePushError = (Boolean(input.localError) || input.pushState === "failed") && !hasBranchDivergedError;
  const branchDivergedReason = "The PR branch changed since this session checkpoint. Continue from the PR branch before pushing again.";
  if (input.isRunning) {
    return {
      visible: true,
      disabled: true,
      disabledReason: hasBranchDivergedError ? "Wait for the session to finish before continuing from the PR branch" : "Wait for the session to finish before pushing changes",
      label: hasBranchDivergedError ? "Continue from PR branch" : hasRetryablePushError ? "Retry" : "Push changes",
      spinning: false,
      showError: hasRetryablePushError || hasBranchDivergedError,
      requiresBranchSync: hasBranchDivergedError,
    };
  }

  if (hasBranchDivergedError) {
    return {
      visible: true,
      disabled: false,
      disabledReason: branchDivergedReason,
      label: "Continue from PR branch",
      spinning: false,
      showError: true,
      requiresBranchSync: true,
    };
  }

  if (input.localError) {
    return {
      visible: true,
      disabled: false,
      disabledReason: input.localError,
      label: "Retry",
      spinning: false,
      showError: true,
    };
  }

  if (input.pushState === "failed") {
    return {
      visible: true,
      disabled: false,
      disabledReason: input.pushError || "Push to PR failed",
      label: "Retry",
      spinning: false,
      showError: true,
    };
  }

  if (input.snapshotUnavailable || !input.hasSnapshot) {
    return {
      visible: true,
      disabled: true,
      disabledReason: input.snapshotMessage || "A reusable sandbox snapshot is required before pushing changes",
      label: "Push changes",
      spinning: false,
    };
  }

  if (!input.builderReviewAllowsPR) {
    return {
      visible: true,
      disabled: true,
      disabledReason: "Run Review successfully before pushing changes",
      label: "Push changes",
      spinning: false,
    };
  }

  if (input.ghBlocked) {
    return {
      visible: true,
      disabled: true,
      disabledReason: "Connect your GitHub account to push changes",
      label: "Push changes",
      spinning: false,
    };
  }

  return { visible: true, disabled: false, label: "Push changes", spinning: false };
}

export type MergeActionInput = {
  health: PullRequestHealthResponse;
  hasActiveRepair: boolean;
  pendingAction: "fix_tests" | "resolve_conflicts" | "merge" | null;
};

export type MergeWhenReadyActionInput = MergeActionInput & {
  pendingMergeWhenReady: boolean;
};

export function deriveMergeActionState({ health, hasActiveRepair, pendingAction }: MergeActionInput): LabeledLifecycleActionState {
  if (health.status !== "open") {
    return { visible: false, disabled: false, label: "Merge", spinning: false };
  }

  if (health.sync_status === "blocked") {
    return { visible: false, disabled: false, label: "Merge", spinning: false };
  }

  if (pendingAction === "merge") {
    return {
      visible: true,
      disabled: true,
      disabledReason: "Merging this pull request",
      label: "Merging…",
      spinning: true,
    };
  }

  if (pendingAction !== null) {
    return {
      visible: true,
      disabled: true,
      disabledReason: "Wait for the current PR action to finish",
      label: "Merge",
      spinning: false,
    };
  }

  if (hasActiveRepair) {
    return {
      visible: true,
      disabled: true,
      disabledReason: "Wait for the active repair session to finish before merging",
      label: "Merge",
      spinning: false,
    };
  }

  if (health.merge_when_ready.state === "queued") {
    return {
      visible: true,
      disabled: true,
      disabledReason: "Waiting for GitHub requirements.",
      label: "Auto-merge on",
      spinning: false,
    };
  }

  if (health.has_conflicts || health.can_resolve_conflicts || health.merge_state === "conflicted") {
    return {
      visible: true,
      disabled: true,
      disabledReason: "Resolve conflicts before merging",
      label: "Merge",
      spinning: false,
    };
  }

  if (health.merge_state === "mergeability_pending" || health.merge_state === "unknown") {
    return {
      visible: true,
      disabled: true,
      disabledReason: "Waiting for GitHub to check mergeability.",
      label: "Checking mergeability…",
      spinning: true,
    };
  }

  if (health.failing_test_count > 0 || health.can_fix_tests || (health.checks ?? []).some((check) => check.status === "failed")) {
    return {
      visible: true,
      disabled: true,
      disabledReason: "Fix failing checks before merging",
      label: "Merge",
      spinning: false,
    };
  }

  if ((health.checks ?? []).some((check) => check.status === "pending")) {
    return {
      visible: true,
      disabled: true,
      disabledReason: "Checks are still running.",
      label: "Merge",
      spinning: false,
    };
  }

  if (!health.checks_confirmed) {
    return {
      visible: true,
      disabled: true,
      disabledReason: "Waiting for GitHub to confirm required checks.",
      label: "Merge",
      spinning: false,
    };
  }

  if (!health.can_merge) {
    return {
      visible: true,
      disabled: true,
      disabledReason: "GitHub is not allowing this PR to merge yet.",
      label: "Merge",
      spinning: false,
    };
  }

  return { visible: true, disabled: false, label: "Merge", spinning: false };
}

export function deriveMergeWhenReadyActionState({ health, hasActiveRepair, pendingAction, pendingMergeWhenReady }: MergeWhenReadyActionInput): LabeledLifecycleActionState {
  if (health.status !== "open") {
    return { visible: false, disabled: false, label: "Merge when ready", spinning: false };
  }

  if (health.sync_status === "blocked") {
    return { visible: false, disabled: false, label: "Merge when ready", spinning: false };
  }

  const current = health.merge_when_ready;
  if (current?.state === "queued") {
    return { visible: true, disabled: false, label: "Turn off auto-merge", spinning: pendingMergeWhenReady };
  }
  if (current?.state === "merging") {
    return { visible: true, disabled: true, disabledReason: "Merging this pull request", label: "Merging when ready…", spinning: true };
  }

  const label = current?.state === "failed" ? "Retry merge when ready" : "Merge when ready";
  if (pendingMergeWhenReady) {
    return { visible: true, disabled: true, disabledReason: "Updating merge when ready", label, spinning: true };
  }
  if (pendingAction !== null) {
    return { visible: true, disabled: true, disabledReason: "Wait for the current PR action to finish", label, spinning: false };
  }
  if (hasActiveRepair) {
    return { visible: true, disabled: true, disabledReason: "Wait for the active repair session to finish before enabling merge when ready", label, spinning: false };
  }
  if (health.has_conflicts || health.can_resolve_conflicts || health.merge_state === "conflicted") {
    return { visible: true, disabled: true, disabledReason: "Resolve conflicts before enabling merge when ready", label, spinning: false };
  }
  if (health.failing_test_count > 0 || health.can_fix_tests || (health.checks ?? []).some((check) => check.status === "failed")) {
    return { visible: true, disabled: true, disabledReason: "Fix failing checks before enabling merge when ready", label, spinning: false };
  }
  if (!health.head_sha) {
    return { visible: true, disabled: true, disabledReason: "Waiting for GitHub to report the pull request head", label, spinning: false };
  }
  if (health.can_merge) {
    return { visible: false, disabled: false, label, spinning: false };
  }
  return { visible: true, disabled: false, label, spinning: false };
}
