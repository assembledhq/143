import { describe, expect, it } from "vitest";

import { codeReviewReasonDescription, codeReviewReasonLabel } from "./code-review-reasons";

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
