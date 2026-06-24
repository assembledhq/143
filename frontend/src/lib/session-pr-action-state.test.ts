import { describe, expect, it } from "vitest";

import type { PullRequestHealthResponse } from "./types";
import {
  deriveCreatePRActionState,
  deriveMergeActionState,
  deriveMergeWhenReadyActionState,
  derivePushChangesActionState,
  hasRepairableFailedChecks,
} from "./session-pr-action-state";

const baseHealth: PullRequestHealthResponse = {
  pull_request_id: "pr-123",
  pull_request_number: 42,
  repository: "acme/widgets",
  url: "https://github.com/acme/widgets/pull/42",
  status: "open",
  head_sha: "head-sha",
  base_sha: "base-sha",
  health_version: 1,
  sync_status: "synced",
  merge_state: "clean",
  has_conflicts: false,
  failing_test_count: 0,
  needs_agent_action: false,
  summary: "healthy",
  checks: [{ name: "unit", category: "test", status: "passed" }],
  checks_confirmed: true,
  can_resolve_conflicts: false,
  can_fix_tests: false,
  can_merge: true,
  enrichment_status: "ready",
  enrichment_requested: true,
  enrichment_ready: true,
  conflict_detail_available: false,
  failing_test_detail_available: false,
  active_repairs: [],
  merge_when_ready: { state: "off" },
};

describe("session PR action state", () => {
  it("detects repairable failed checks from flags, counts, or check summaries", () => {
    const tests = [
      {
        name: "backend flag",
        health: { ...baseHealth, can_fix_tests: true },
        expected: true,
      },
      {
        name: "legacy count",
        health: { ...baseHealth, failing_test_count: 1 },
        expected: true,
      },
      {
        name: "failed check",
        health: { ...baseHealth, checks: [{ name: "backend", category: "unknown" as const, status: "failed" as const }] },
        expected: true,
      },
      {
        name: "passing checks",
        health: baseHealth,
        expected: false,
      },
      {
        name: "blocked repository with stale failed checks",
        health: {
          ...baseHealth,
          sync_status: "blocked" as const,
          sync_blocker: "repository_disconnected" as const,
          can_fix_tests: true,
          failing_test_count: 1,
          checks: [{ name: "unit", category: "test" as const, status: "failed" as const }],
        },
        expected: false,
      },
    ];

    for (const tt of tests) {
      expect(hasRepairableFailedChecks(tt.health), `${tt.name} should map repairable failed-check state`).toBe(tt.expected);
    }
  });

  it("maps create PR lifecycle blockers into visible disabled states", () => {
    const base = {
      canShipPR: true,
      hasPR: false,
      hasSessionChanges: true,
      hasSnapshot: true,
      isRunning: false,
      builderReviewAllowsPR: true,
      snapshotUnavailable: false,
      ghBlocked: false,
      queueingPR: false,
      creatingPR: false,
      finalizingPR: false,
      prState: "idle" as const,
      hasRecoverableError: false,
    };

    const tests = [
      {
        name: "running",
        input: { ...base, isRunning: true },
        reason: "Wait for the session to finish before creating a PR",
      },
      {
        name: "review required",
        input: { ...base, builderReviewAllowsPR: false },
        reason: "Run readiness checks successfully before creating a PR",
      },
      {
        name: "snapshot missing",
        input: { ...base, hasSnapshot: false },
        reason: "A reusable sandbox snapshot is required before creating a PR",
      },
    ];

    for (const tt of tests) {
      const state = deriveCreatePRActionState(tt.input);
      expect(state.visible, `${tt.name} should keep Create PR discoverable`).toBe(true);
      expect(state.disabled, `${tt.name} should disable Create PR`).toBe(true);
      expect(state.disabledReason, `${tt.name} should explain the disabled state`).toBe(tt.reason);
    }
  });

  it("maps push changes lifecycle blockers into visible disabled states", () => {
    const state = derivePushChangesActionState({
      canShipPR: true,
      hasOpenPR: true,
      hasUnpushedChanges: true,
      hasSnapshot: true,
      isRunning: true,
      builderReviewAllowsPR: true,
      snapshotUnavailable: false,
      ghBlocked: false,
      queueingPush: false,
      pushingChanges: false,
      pushState: "idle",
    });

    expect(state.visible, "Push changes should remain visible when temporarily blocked").toBe(true);
    expect(state.disabled, "Push changes should be disabled while the session runs").toBe(true);
    expect(state.disabledReason, "Push changes should explain the running-session blocker").toBe("Wait for the session to finish before pushing changes");
  });

  it("hides push changes when PR health is blocked", () => {
    const state = derivePushChangesActionState({
      canShipPR: true,
      hasOpenPR: true,
      hasUnpushedChanges: true,
      hasSnapshot: true,
      isRunning: false,
      builderReviewAllowsPR: true,
      snapshotUnavailable: false,
      ghBlocked: false,
      queueingPush: false,
      pushingChanges: false,
      prHealthBlocked: true,
    } as Parameters<typeof derivePushChangesActionState>[0] & { prHealthBlocked: boolean });

    expect(state.visible, "blocked PR health should hide push changes action derivation").toBe(false);
    expect(state.disabled, "hidden push changes action should not be disabled").toBe(false);
  });

  it("maps merge health states into stable visible states", () => {
    const tests = [
      {
        name: "pending checks",
        health: { ...baseHealth, can_merge: false, checks: [{ name: "unit", category: "test" as const, status: "pending" as const }] },
        disabled: true,
        reason: "Checks are still running.",
      },
      {
        name: "auto-merge queued",
        health: { ...baseHealth, can_merge: false, merge_when_ready: { state: "queued" as const } },
        disabled: true,
        reason: "Waiting for GitHub requirements.",
        label: "Auto-merge on",
      },
      {
        name: "unconfirmed checks",
        health: { ...baseHealth, checks_confirmed: false, checks: [] },
        disabled: true,
        reason: "Waiting for GitHub to confirm required checks.",
      },
      {
        name: "blocked by disconnected repository",
        health: {
          ...baseHealth,
          sync_status: "blocked" as const,
          sync_blocker: "repository_disconnected" as const,
          can_merge: false,
          merge_state: "unknown" as const,
        },
        disabled: false,
        reason: undefined,
        label: "Merge",
        visible: false,
      },
      {
        name: "pending mergeability",
        health: { ...baseHealth, can_merge: false, merge_state: "mergeability_pending" as const },
        disabled: true,
        reason: "Waiting for GitHub to check mergeability.",
        label: "Checking mergeability…",
      },
      {
        name: "healthy",
        health: baseHealth,
        disabled: false,
        reason: undefined,
        label: "Merge",
      },
    ];

    for (const tt of tests) {
      const state = deriveMergeActionState({ health: tt.health, hasActiveRepair: false, pendingAction: null });
      expect(state.visible, `${tt.name} should map Merge visibility`).toBe(tt.visible ?? true);
      expect(state.disabled, `${tt.name} should map Merge disabled state`).toBe(tt.disabled);
      expect(state.disabledReason, `${tt.name} should map Merge reason`).toBe(tt.reason);
      expect(state.label, `${tt.name} should map Merge label`).toBe(tt.label ?? "Merge");
    }
  });

  it("maps merge-when-ready queueable and blocked states", () => {
    const tests = [
      {
        name: "blocked by disconnected repository cannot queue",
        health: {
          ...baseHealth,
          sync_status: "blocked" as const,
          sync_blocker: "repository_disconnected" as const,
          can_merge: false,
        },
        visible: false,
        disabled: false,
        label: "Merge when ready",
      },
      {
        name: "pending checks can queue",
        health: { ...baseHealth, can_merge: false, checks: [{ name: "unit", category: "test" as const, status: "pending" as const }] },
        visible: true,
        disabled: false,
        label: "Merge when ready",
      },
      {
        name: "conflicts cannot queue",
        health: { ...baseHealth, can_merge: false, has_conflicts: true },
        visible: true,
        disabled: true,
        label: "Merge when ready",
        reason: "Resolve conflicts before enabling merge when ready",
      },
      {
        name: "failed checks cannot queue",
        health: { ...baseHealth, can_merge: false, checks: [{ name: "unit", category: "test" as const, status: "failed" as const }] },
        visible: true,
        disabled: true,
        label: "Merge when ready",
        reason: "Fix failing checks before enabling merge when ready",
      },
      {
        name: "queued can cancel",
        health: { ...baseHealth, can_merge: false, merge_when_ready: { state: "queued" as const } },
        visible: true,
        disabled: false,
        label: "Turn off auto-merge",
      },
      {
        name: "cancelled can requeue",
        health: { ...baseHealth, can_merge: false, merge_when_ready: { state: "cancelled" as const } },
        visible: true,
        disabled: false,
        label: "Merge when ready",
      },
    ];

    for (const tt of tests) {
      const state = deriveMergeWhenReadyActionState({ health: tt.health, hasActiveRepair: false, pendingAction: null, pendingMergeWhenReady: false });
      expect(state.visible, `${tt.name} should map visibility`).toBe(tt.visible);
      expect(state.disabled, `${tt.name} should map disabled state`).toBe(tt.disabled);
      expect(state.label, `${tt.name} should map label`).toBe(tt.label);
      expect(state.disabledReason, `${tt.name} should map disabled reason`).toBe(tt.reason);
    }
  });
});
