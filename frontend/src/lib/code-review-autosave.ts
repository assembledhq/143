import type { CodeReviewPolicyConfig, CodeReviewResolvedPolicy, SingleResponse } from "@/lib/types";

export type PolicyPatch = { [key: string]: unknown };
const patches = new WeakMap<CodeReviewPolicyConfig, PolicyPatch>();
const bases = new WeakMap<CodeReviewPolicyConfig, PolicyPatch>();
const isObject = (value: unknown): value is PolicyPatch => value !== null && typeof value === "object" && !Array.isArray(value);

export function policyMerge(target: PolicyPatch, patch: PolicyPatch): PolicyPatch {
  const result = { ...target };
  for (const [key, value] of Object.entries(patch)) {
    if (value === null) delete result[key];
    else result[key] = isObject(value) ? policyMerge(isObject(result[key]) ? result[key] : {}, value) : value;
  }
  return result;
}

export function policyDiff(before: PolicyPatch, after: PolicyPatch): PolicyPatch {
  const result: PolicyPatch = {};
  for (const key of new Set([...Object.keys(before), ...Object.keys(after)])) {
    if (JSON.stringify(before[key]) === JSON.stringify(after[key])) continue;
    if (!(key in after)) result[key] = null;
    else if (isObject(before[key]) && isObject(after[key])) result[key] = policyDiff(before[key], after[key]);
    else result[key] = after[key];
  }
  return result;
}

// Each editor records its changed fields before the optimistic cache changes.
export function trackPolicyDraft(base: CodeReviewPolicyConfig, next: CodeReviewPolicyConfig): CodeReviewPolicyConfig {
  patches.set(next, policyDiff(base as unknown as PolicyPatch, next as unknown as PolicyPatch));
  bases.set(next, base as unknown as PolicyPatch);
  return next;
}
export function policyPatchFor(config: CodeReviewPolicyConfig): PolicyPatch {
  const patch = patches.get(config);
  if (!patch) throw new Error("Policy edit is missing its changed-field patch");
  return patch;
}
export function applyCodeReviewPolicyOptimistic(prev: unknown, config: CodeReviewPolicyConfig): unknown {
  const previous = prev as SingleResponse<CodeReviewResolvedPolicy> | undefined;
  if (!previous?.data) return previous;
  return { ...previous, data: { ...previous.data, config: policyMerge(previous.data.config as unknown as PolicyPatch, policyPatchFor(config)) } };
}

// Compose against the queued edit's base, including parent resets.
export function coalesceCodeReviewPolicy(a: CodeReviewPolicyConfig, b: CodeReviewPolicyConfig): CodeReviewPolicyConfig {
  const base = bases.get(a);
  if (!base) throw new Error("Policy edit is missing its original base");
  const final = policyMerge(policyMerge(base, policyPatchFor(a)), policyPatchFor(b));
  const result = { ...b };
  patches.set(result, policyDiff(base, final));
  bases.set(result, base);
  return result;
}
