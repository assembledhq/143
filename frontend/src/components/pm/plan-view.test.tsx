import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { PlanView } from "./plan-view";
import type { PMPlan } from "@/lib/types";

describe("PlanView", () => {
  it("renders plan sections", () => {
    const plan: PMPlan = {
      id: "plan-1",
      org_id: "org-1",
      status: "completed",
      analysis: "Billing timeouts spiked after deploy.",
      tasks: [
        {
          rank: 1,
          issue_ids: ["issue-1"],
          title: "Fix billing timeout",
          reasoning: "High impact",
          approach: "Check handlers/billing.go",
          risk: "Payment flow regression",
          complexity: "moderate",
          confidence: "medium",
        },
      ],
      clusters: [
        {
          issue_ids: ["issue-1", "issue-2"],
          root_cause: "Missing retry",
          strategy: "Fix retry logic",
        },
      ],
      skipped_issues: [
        {
          issue_id: "issue-3",
          reason: "in_avoid_area",
          detail: "Legacy auth",
        },
      ],
      issues_reviewed: 3,
      triggered_by: "manual",
      created_at: new Date().toISOString(),
    };

    render(<PlanView plan={plan} />);

    expect(screen.getByText("Billing timeouts spiked after deploy.")).toBeInTheDocument();
    expect(screen.getByText("Missing retry")).toBeInTheDocument();
    expect(screen.getByText("Legacy auth")).toBeInTheDocument();
  });
});
