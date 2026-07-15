import { describe, expect, it, vi } from "vitest";
import { http, HttpResponse } from "msw";

import type {
  AutomationDecision,
  AutomationDecisionStats,
  AutomationOutcomeDecision,
  AutomationRunStatus,
} from "@/lib/types";
import {
  renderWithProviders,
  screen,
  waitFor,
} from "@/test/test-utils";
import { server } from "@/test/mocks/server";

import { DecisionHistory } from "./decision-history";

vi.mock("next/navigation", () => ({
  useRouter: () => ({ push: vi.fn() }),
}));

const stats: AutomationDecisionStats = {
  unique_pull_requests: 4,
  unique_revisions: 7,
  total_runs: 9,
  evaluating: 1,
  passed: 1,
  changes_requested: 1,
  advisory: 1,
  not_applicable: 1,
  outcome_not_reported: 1,
  execution_failed: 1,
};

function makeDecision({
  id,
  status = "completed",
  outcome,
  attemptCount = 1,
}: {
  id: string;
  status?: AutomationRunStatus;
  outcome?: AutomationOutcomeDecision;
  attemptCount?: number;
}): AutomationDecision {
  const prNumber = Number(id.replace(/\D/g, "")) || 1;
  return {
    automation_id: "auto-1",
    run_id: `run-${id}`,
    session_id: `session-${id}`,
    target: {
      repository: "acme/api",
      pull_request_number: prNumber,
      pull_request_url: `https://github.com/acme/api/pull/${prNumber}`,
      pull_request_title: `PR ${prNumber} title`,
      head_sha: `abcdef0${prNumber}`,
    },
    execution_status: status,
    triggered_at: "2026-07-14T12:00:00Z",
    completed_at:
      status === "pending" || status === "running"
        ? undefined
        : "2026-07-14T12:05:00Z",
    attempt_count: attemptCount,
    outcome: outcome
      ? {
          id: `outcome-${id}`,
          org_id: "org-1",
          automation_id: "auto-1",
          automation_run_id: `run-${id}`,
          session_id: `session-${id}`,
          repository: "acme/api",
          pull_request_number: prNumber,
          pull_request_url: `https://github.com/acme/api/pull/${prNumber}`,
          pull_request_title: `PR ${prNumber} title`,
          head_sha: `abcdef0${prNumber}`,
          decision: outcome,
          reason: `${outcome} because the review found this result.`,
          source:
            outcome === "changes_requested"
              ? "legacy_inferred"
              : "agent_reported",
          reported_at: "2026-07-14T12:04:00Z",
          created_at: "2026-07-14T12:04:00Z",
          external_action:
            outcome === "changes_requested"
              ? {
                  id: "action-1",
                  org_id: "org-1",
                  outcome_id: `outcome-${id}`,
                  provider: "github",
                  action_type: "github_review_changes_requested",
                  url: "https://github.com/acme/api/pull/2#pullrequestreview-1",
                  verification_status: "reported",
                  created_at: "2026-07-14T12:04:00Z",
                }
              : undefined,
        }
      : undefined,
  };
}

function useDecisionHandlers(decisions: AutomationDecision[]) {
  server.use(
    http.get("*/api/v1/automations/auto-1/decisions*", () =>
      HttpResponse.json({ data: decisions, meta: {} }),
    ),
    http.get("*/api/v1/automations/auto-1/decision-stats", () =>
      HttpResponse.json({ data: stats }),
    ),
  );
}

describe("DecisionHistory", () => {
  it("separates PR outcomes from execution state and links external actions", async () => {
    useDecisionHandlers([
      makeDecision({ id: "1", outcome: "passed" }),
      makeDecision({
        id: "2",
        outcome: "changes_requested",
        attemptCount: 2,
      }),
      makeDecision({ id: "3", outcome: "advisory" }),
      makeDecision({ id: "4", outcome: "not_applicable" }),
      makeDecision({ id: "5", status: "running" }),
      makeDecision({ id: "6", status: "failed" }),
      makeDecision({ id: "7" }),
    ]);

    renderWithProviders(<DecisionHistory automationId="auto-1" />);

    expect(
      await screen.findByRole("heading", { name: "PR decisions" }),
    ).toBeInTheDocument();
    expect(await screen.findByText("Passed")).toBeInTheDocument();
    expect(screen.getByText("Changes requested")).toBeInTheDocument();
    expect(screen.getByText("Advisory")).toBeInTheDocument();
    expect(screen.getByText("Not applicable")).toBeInTheDocument();
    expect(screen.getByText("Evaluating")).toBeInTheDocument();
    expect(screen.getByText("Execution failed")).toBeInTheDocument();
    expect(screen.getByText("Outcome not reported")).toBeInTheDocument();
    expect(screen.getByText("Evaluated 2 times")).toBeInTheDocument();
    expect(
      screen.getByText("Inferred from legacy summary"),
    ).toBeInTheDocument();
    expect(
      screen.getByText(
        "The automation failed before it reported a review decision.",
      ),
    ).toBeInTheDocument();
    expect(
      screen.getByText(/Do not infer that the PR passed/),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("link", { name: /View requested changes/ }),
    ).toHaveAttribute(
      "href",
      "https://github.com/acme/api/pull/2#pullrequestreview-1",
    );
    expect(screen.getByText("PRs").previousSibling).toHaveTextContent("4");
    expect(screen.getByText("Revisions").previousSibling).toHaveTextContent(
      "7",
    );
    expect(screen.getByText("Attempts").previousSibling).toHaveTextContent(
      "9",
    );
  });

  it("falls back to execution history when decision endpoints are unavailable", async () => {
    server.use(
      http.get("*/api/v1/automations/auto-1/runs*", () =>
        HttpResponse.json({ data: [], meta: {} }),
      ),
    );

    renderWithProviders(<DecisionHistory automationId="auto-1" />);

    expect(
      await screen.findByRole("heading", { name: "Execution history" }),
    ).toBeInTheDocument();
    expect(
      screen.getByText(/Run status below describes execution only/),
    ).toBeInTheDocument();
    expect(screen.queryByRole("tab", { name: "Decisions" })).not.toBeInTheDocument();
    expect(await screen.findByText("No runs yet")).toBeInTheDocument();
  });

  it("sends bookmarkable outcome and PR filters to the decisions API", async () => {
    const requestURL = vi.fn<(value: string) => void>();
    server.use(
      http.get("*/api/v1/automations/auto-1/decisions*", ({ request }) => {
        requestURL(request.url);
        return HttpResponse.json({ data: [], meta: {} });
      }),
      http.get("*/api/v1/automations/auto-1/decision-stats", () =>
        HttpResponse.json({ data: stats }),
      ),
    );

    renderWithProviders(<DecisionHistory automationId="auto-1" />, {
      searchParams: { outcome: "changes_requested", pr: "42" },
    });

    await waitFor(() => expect(requestURL).toHaveBeenCalled());
    const url = new URL(requestURL.mock.calls.at(-1)?.[0] ?? "http://test");
    expect(url.searchParams.get("outcome")).toBe("changes_requested");
    expect(url.searchParams.get("pr")).toBe("42");
    expect(await screen.findByText("No matching PR decisions")).toBeInTheDocument();
  });
});
