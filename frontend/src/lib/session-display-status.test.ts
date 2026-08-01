import { describe, expect, it } from "vitest";

import { deriveSessionDisplayStatus } from "./session-display-status";
import type { Session } from "./types";

function makeSession(overrides: Partial<Session> = {}): Session {
  return {
    id: "session-1",
    org_id: "org-1",
    agent_type: "codex",
    status: "completed",
    autonomy_level: "semi",
    token_mode: "standard",
    current_turn: 1,
    last_activity_at: "2026-01-01T00:00:00.000Z",
    sandbox_state: "snapshotted",
    pr_creation_state: "idle",
    pr_push_state: "idle",
    created_at: "2026-01-01T00:00:00.000Z",
    ...overrides,
  };
}

describe("deriveSessionDisplayStatus", () => {
  it("uses the regular session status when no PR action is in flight", () => {
    const status = deriveSessionDisplayStatus(makeSession({ status: "idle" }));

    expect(status.label, "idle sessions should clearly communicate readiness").toBe("Ready to continue");
    expect(status.kind, "idle sessions should be classified as session status").toBe("session");
    expect(status.activity, "idle sessions should remain visually still").toBe("none");
    expect(status.tone, "idle sessions should use the neutral operational tone").toBe("neutral");
  });

  it("shows PR creation while create PR is queued or pushing", () => {
    const tests = [
      { name: "queued", state: "queued" as const },
      { name: "pushing", state: "pushing" as const },
    ];

    for (const tt of tests) {
      const status = deriveSessionDisplayStatus(makeSession({ pr_creation_state: tt.state }));

      expect(status.label, `${tt.name} PR creation should override the session status`).toBe("Creating PR");
      expect(status.kind, `${tt.name} PR creation should identify the PR creation kind`).toBe("pr_creation");
      expect(status.activity, `${tt.name} PR creation should use short-operation progress`).toBe("indeterminate");
    }
  });

  it("shows push changes while a PR push is queued or pushing", () => {
    const tests = [
      { name: "queued", state: "queued" as const },
      { name: "pushing", state: "pushing" as const },
    ];

    for (const tt of tests) {
      const status = deriveSessionDisplayStatus(makeSession({ pr_push_state: tt.state }));

      expect(status.label, `${tt.name} PR push should override the session status`).toBe("Pushing changes");
      expect(status.kind, `${tt.name} PR push should identify the PR push kind`).toBe("pr_push");
      expect(status.activity, `${tt.name} PR push should use short-operation progress`).toBe("indeterminate");
    }
  });

  it.each([
    ["running", "Running", "primary", "breathing", "none"],
    ["awaiting_input", "Waiting for you", "warning", "none", "action_required"],
    ["needs_human_guidance", "Needs guidance", "attention", "none", "blocking"],
    ["completed", "Completed", "success", "none", "none"],
    ["failed", "Failed", "destructive", "none", "blocking"],
  ] as const)("maps %s into one canonical presentation", (sessionStatus, label, tone, activity, attention) => {
    const status = deriveSessionDisplayStatus(makeSession({ status: sessionStatus }));

    expect(status.label, `${sessionStatus} should use consistent copy`).toBe(label);
    expect(status.tone, `${sessionStatus} should use its semantic tone`).toBe(tone);
    expect(status.activity, `${sessionStatus} should only move when progressing`).toBe(activity);
    expect(status.attention, `${sessionStatus} should expose required attention`).toBe(attention);
  });

  it("prioritizes PR pushes over PR creation when both are in flight", () => {
    const status = deriveSessionDisplayStatus(makeSession({
      pr_creation_state: "pushing",
      pr_push_state: "queued",
    }));

    expect(status.label, "push changes should be the more specific in-flight status").toBe("Pushing changes");
    expect(status.kind, "push changes should win over PR creation").toBe("pr_push");
  });

  it("does not let failed PR actions override the primary session status", () => {
    const status = deriveSessionDisplayStatus(makeSession({
      status: "completed",
      pr_creation_state: "failed",
      pr_push_state: "failed",
    }));

    expect(status.label, "failed PR actions should not make the whole session look failed").toBe("Completed");
    expect(status.kind, "failed PR actions should leave the primary status as session status").toBe("session");
  });

  it.each([
    ["merged", "PR merged", "success"],
    ["closed", "PR closed", "neutral"],
  ] as const)("preserves the %s pull request terminal state", (prStatus, label, tone) => {
    const status = deriveSessionDisplayStatus(makeSession({ status: "pr_created" }), prStatus);

    expect(status.label, `${prStatus} pull requests should retain their terminal label`).toBe(label);
    expect(status.kind, `${prStatus} pull requests should be classified separately`).toBe("pull_request");
    expect(status.tone, `${prStatus} pull requests should retain their semantic tone`).toBe(tone);
    expect(status.activity, `${prStatus} pull requests should remain still`).toBe("none");
  });
});
