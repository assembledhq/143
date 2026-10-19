import { describe, expect, it } from "vitest";

import {
  blockingCodeReviewReasons,
  codeReviewReasonDescription,
  codeReviewReasonLabel,
  informationalCodeReviewReasons,
  sizeSafeguardMeasurements,
  sizeSafeguardSummary,
} from "./code-review-reasons";

describe("codeReviewReasonLabel", () => {
  it("uses the curated label for a known code", () => {
    expect(codeReviewReasonLabel("blocking_findings")).toBe("Reviewers found a blocking issue");
  });

  it("humanizes a code the frontend does not know yet", () => {
    expect(codeReviewReasonLabel("future_policy_gate")).toBe("Future policy gate");
  });
});

describe("codeReviewReasonDescription", () => {
  it("renders the bare label when the reason carries no detail", () => {
    expect(codeReviewReasonDescription({ code: "blocking_findings" })).toBe("Reviewers found a blocking issue");
  });

  it("appends the measurement when both sides are present", () => {
    expect(codeReviewReasonDescription({ code: "lines_limit_exceeded", actual: 1200, limit: 300 })).toBe(
      "Line-count limit exceeded (1,200 of 300)",
    );
  });

  it("keeps the measurement when the API omits a zero-valued side", () => {
    expect(codeReviewReasonDescription({ code: "files_limit_exceeded", actual: 34 })).toBe("File-count limit exceeded (34)");
    expect(codeReviewReasonDescription({ code: "files_limit_exceeded", limit: 25 })).toBe("File-count limit exceeded (limit 25)");
  });

  it("prefers the subject over counts and trims it", () => {
    expect(codeReviewReasonDescription({ code: "required_check_failing", subject: "  ci/build  " })).toBe(
      "A named required check was not passing: ci/build",
    );
    expect(codeReviewReasonDescription({ code: "sensitive_path", subject: "infra/secrets.tf", actual: 1, limit: 0 })).toBe(
      "Sensitive paths changed: infra/secrets.tf",
    );
  });

  it("ignores a blank subject", () => {
    expect(codeReviewReasonDescription({ code: "blocked_path", subject: "   " })).toBe("Blocked paths changed");
  });
});

describe("independent size limits", () => {
  it.each([
    ["additions_limit_exceeded", "Additions limit exceeded (301 of 300)"],
    ["deletions_limit_exceeded", "Deletions limit exceeded (301 of 300)"],
  ])("describes %s", (code, expected) => {
    expect(codeReviewReasonDescription({ code, actual: 301, limit: 300 })).toBe(expected);
  });
});

describe("informational size safeguard records", () => {
  const safeguard = {
    code: "size_safeguard",
    size_safeguard: {
      outcome: "waived" as const,
      source: "authoritative",
      tier: "low",
      mode: "enforce",
      check_run_id: 77,
      head_sha: "abcdef1",
      files: { actual: 6, limit: 5, exceeded: true },
      additions: { actual: 420, limit: 300, exceeded: true },
      deletions: { actual: 6, limit: 300, exceeded: false },
    },
  };
  const reasons = [{ code: "blocking_findings" }, safeguard, { code: "files_limit_exceeded", actual: 6, limit: 5 }];

  it("separates blocking reasons from informational records", () => {
    expect(blockingCodeReviewReasons(reasons).map((reason) => reason.code)).toEqual(["blocking_findings", "files_limit_exceeded"]);
    expect(informationalCodeReviewReasons(reasons)).toEqual([safeguard]);
  });

  it("summarizes a waived safeguard with its deciding check run", () => {
    expect(sizeSafeguardSummary(safeguard.size_safeguard)).toBe("Size limits waived: risk tier low, enforce mode, check run 77");
  });

  it("summarizes an applied safeguard without an authoritative tier", () => {
    expect(sizeSafeguardSummary({ outcome: "applied", source: "shadow_mode", mode: "shadow", check_run_id: 78 })).toBe(
      "Size limits applied: shadow-mode risk-tier check, shadow mode, check run 78",
    );
    expect(sizeSafeguardSummary({ outcome: "applied", source: "missing" })).toBe("Size limits applied: no risk-tier check");
  });

  it("lists every measured limit", () => {
    expect(sizeSafeguardMeasurements(safeguard.size_safeguard)).toEqual([
      "Files: 6 of 5 (over limit)",
      "Additions: 420 of 300 (over limit)",
      "Deletions: 6 of 300",
    ]);
  });

  it.each([
    {
      name: "only exceeded limits",
      decision: { exceeded_limits: [{ code: "files_limit_exceeded", actual: 6, limit: 5 }, { code: "lines_limit_exceeded", actual: 900, limit: 500 }] },
      expected: ["File-count limit exceeded (6 of 5)", "Line-count limit exceeded (900 of 500)"],
    },
    {
      name: "some measurements and an exceeded limit they do not cover",
      decision: {
        files: { actual: 6, limit: 5, exceeded: true },
        exceeded_limits: [{ code: "files_limit_exceeded", actual: 6, limit: 5 }, { code: "deletions_limit_exceeded", actual: 301, limit: 300 }],
      },
      expected: ["Files: 6 of 5 (over limit)", "Deletions limit exceeded (301 of 300)"],
    },
    { name: "no measurements", decision: {}, expected: [] },
  ])("renders a record with $name without inventing measurements", ({ decision, expected }) => {
    expect(sizeSafeguardMeasurements(decision)).toEqual(expected);
  });
});
