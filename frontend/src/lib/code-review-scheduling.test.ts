import { describe, expect, it } from "vitest";
import { coalesceCodeReviewPolicy, policyPatchFor, trackPolicyDraft, policyMerge } from "./code-review-autosave";
import type { CodeReviewPolicyConfig } from "./types";

describe("review policy scheduling edits", () => {
  it("coalesces independent fields without sending the cached policy", () => {
    const base = { enabled: true, scheduling_policy: { quiet_period_seconds: 60, minimum_interval_seconds: 0 } } as CodeReviewPolicyConfig;
    const first = trackPolicyDraft(base, { ...base, scheduling_policy: { ...base.scheduling_policy, quiet_period_seconds: 300 } });
    const second = trackPolicyDraft(first, { ...first, scheduling_policy: { ...first.scheduling_policy, minimum_interval_seconds: 900 } });
    expect(policyPatchFor(coalesceCodeReviewPolicy(first, second))).toEqual({ scheduling_policy: { quiet_period_seconds: 300, minimum_interval_seconds: 900 } });
  });
  it("preserves explicit false and zero", () => {
    const base = { scheduling_policy: { automatic_re_review: true, quiet_period_seconds: 300 } } as CodeReviewPolicyConfig;
    const next = trackPolicyDraft(base, { ...base, scheduling_policy: { automatic_re_review: false, quiet_period_seconds: 0 } });
    expect(policyPatchFor(next)).toEqual({ scheduling_policy: { automatic_re_review: false, quiet_period_seconds: 0 } });
  });
  it("applies null as a reset while retaining unrelated settings", () => {
    expect(policyMerge({ enabled: true, scheduling_policy: { quiet_period_seconds: 300, minimum_interval_seconds: 900 } }, { scheduling_policy: { quiet_period_seconds: null } })).toEqual({ enabled: true, scheduling_policy: { minimum_interval_seconds: 900 } });
  });
});

it("coalesces a parent reset followed by a child edit without restoring removed fields", () => {
  const base = { enabled: true, scheduling_policy: { quiet_period_seconds: 300, minimum_interval_seconds: 900 } } as CodeReviewPolicyConfig;
  const reset = trackPolicyDraft(base, { enabled: true } as CodeReviewPolicyConfig);
  const next = trackPolicyDraft(reset, { ...reset, scheduling_policy: { quiet_period_seconds: 60 } });
  expect(policyPatchFor(coalesceCodeReviewPolicy(reset, next))).toEqual({ scheduling_policy: { quiet_period_seconds: 60, minimum_interval_seconds: null } });
});
