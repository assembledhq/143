import { describe, it, expect } from "vitest";
import { http, HttpResponse } from "msw";
import { renderWithProviders, screen, userEvent, waitFor } from "@/test/test-utils";
import { server } from "@/test/mocks/server";
import CodeReviewsPage from "./page";
import type { CodeReviewListItem, CodeReviewResolvedPolicy, ListResponse, Repository, SingleResponse } from "@/lib/types";

const repo: Repository = {
  id: "repo-1",
  org_id: "org-1",
  integration_id: "int-1",
  github_id: 143,
  full_name: "acme/api",
  default_branch: "main",
  private: true,
  clone_url: "https://github.com/acme/api.git",
  installation_id: 123,
  status: "active",
  settings: {},
  created_at: "2026-06-26T12:00:00Z",
  updated_at: "2026-06-26T12:00:00Z",
};

const policy: CodeReviewResolvedPolicy = {
  source: "organization",
  config: {
    enabled: true,
    approval_mode: "comment_only",
    description_policy: {
      requirements: [
        { key: "description", title: "Understandable description", prompt: "Explain intent.", required: true },
        { key: "testing", title: "Testing evidence", prompt: "Show validation.", required: true },
      ],
    },
    risk_policy: {
      max_files_changed: 5,
      max_lines_changed: 300,
      require_passing_checks: true,
      exclude_sensitive_paths: true,
      sensitive_paths: ["*auth*"],
      exclude_categories: ["auth", "billing"],
      require_mergeable: true,
      require_up_to_date: false,
      allow_forks: false,
      allow_policy_changes: false,
    },
    agent_roster: {
      reviewers: ["codex", "claude_code"],
      orchestrator: "claude_code",
      review_depth: "standard",
      disagreement_blocks: true,
      require_reviewer_quorum: 2,
      timeout_seconds: 1800,
      max_cost_cents: 500,
    },
    inline_comment_limit: 4,
  },
};

const review: CodeReviewListItem = {
  id: "review-1",
  org_id: "org-1",
  session_id: "session-1",
  repository_id: "repo-1",
  pull_request_id: "pr-1",
  policy_id: "policy-1",
  base_sha: "base-sha",
  head_sha: "abcdef123456",
  from_fork: false,
  trigger_source: "app_reviewer",
  status: "completed",
  decision: "approved",
  acceptable: true,
  stale: false,
  review_output_key: "pr-1:abcdef:policy-1",
  completed_at: "2026-06-26T12:05:00Z",
  created_at: "2026-06-26T12:00:00Z",
  repository_name: "api",
  github_repo: "acme/api",
  github_pr_number: 428,
  github_pr_url: "https://github.com/acme/api/pull/428",
  pull_request_title: "Fix invoice rounding",
  pull_request_author: "anya",
};

describe("CodeReviewsPage", () => {
  it("renders review sessions and policy configuration", async () => {
    const user = userEvent.setup();
    server.use(
      http.get("/api/v1/repositories", () => HttpResponse.json({ data: [repo], meta: {} } satisfies ListResponse<Repository>)),
      http.get("/api/v1/code-reviews", () => HttpResponse.json({ data: [review], meta: {} } satisfies ListResponse<CodeReviewListItem>)),
      http.get("/api/v1/code-review-policies", () => HttpResponse.json({ data: policy } satisfies SingleResponse<CodeReviewResolvedPolicy>)),
    );

    renderWithProviders(<CodeReviewsPage />);

    expect(await screen.findByRole("heading", { name: "Code reviews" })).toBeInTheDocument();
    expect(await screen.findByText("#428 Fix invoice rounding")).toBeInTheDocument();
    expect(screen.getByText("Acceptable")).toBeInTheDocument();
    expect(screen.getByText("Approved")).toBeInTheDocument();

    await user.click(await screen.findByRole("tab", { name: /Configurations/i }));

    await waitFor(() => {
      expect(screen.getByDisplayValue("Understandable description")).toBeInTheDocument();
    });
    expect(screen.getByDisplayValue("*auth*")).toBeInTheDocument();
    expect(screen.getByDisplayValue(/auth\s+billing/)).toBeInTheDocument();
  });
});
