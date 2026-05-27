import type { PullRequestHealthResponse } from "./types";

export type LifecycleActionState = {
  visible: boolean;
  disabled: boolean;
  disabledReason?: string;
};

export type LabeledLifecycleActionState = LifecycleActionState & {
  label: string;
  spinning: boolean;
  showError?: boolean;
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
  prState?: "idle" | "queued" | "pushing" | "succeeded" | "failed";
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
      disabledReason: "Run Review successfully before creating a PR",
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
  pushState?: "idle" | "queued" | "pushing" | "succeeded" | "failed";
  pushError?: string;
  localError?: string;
};

export function derivePushChangesActionState(input: PushChangesActionInput): LabeledLifecycleActionState {
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

  if (input.isRunning) {
    return {
      visible: true,
      disabled: true,
      disabledReason: "Wait for the session to finish before pushing changes",
      label: "Push changes",
      spinning: false,
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

export function deriveMergeActionState({ health, hasActiveRepair, pendingAction }: MergeActionInput): LabeledLifecycleActionState {
  if (health.status !== "open") {
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
