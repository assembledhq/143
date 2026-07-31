import { beforeEach, describe, it, expect, vi } from "vitest";
import { act } from "react";
import { http, HttpResponse } from "msw";
import { createTestQueryClient, fireEvent, renderWithProviders, screen, userEvent, waitFor, within } from "@/test/test-utils";
import { server } from "@/test/mocks/server";
import { queryKeys } from "@/lib/query-keys";

const toast = vi.hoisted(() => ({
  success: vi.fn(),
  info: vi.fn(),
  error: vi.fn(),
}));
const sse = vi.hoisted(() => ({
  onEvent: undefined as undefined | (() => void),
}));

vi.mock("@/lib/notify", () => ({ notify: toast }));

import CodeReviewsPage from "./page";

// jsdom has no EventSource; stub the SSE hook so the live-refresh subscription
// is a no-op in tests (the list refreshes via React Query as usual). Mirrors
// the eval batch page test.
vi.mock("@/lib/use-resource-sse", async () => {
  const actual = await vi.importActual<typeof import("@/lib/use-resource-sse")>("@/lib/use-resource-sse");
  return {
    ...actual,
    useResourceSSE: ({ onEvent }: { onEvent: () => void }) => {
      sse.onEvent = onEvent;
      return { healthy: true };
    },
  };
});
import type {
  CodingCredentialSummary,
  CodeReviewAnalytics,
  CodeReviewEvidence,
  CodeReviewGitHubTriggerResponse,
  CodeReviewListItem,
  CodeReviewPolicyConfig,
  CodeReviewPolicyRecord,
  CodeReviewResolvedPolicy,
  CodeReviewStats,
  CodeReviewTemplateOption,
  CodeReviewPromptExamplesResponse,
  AuditLog,
  ListResponse,
  OpenCodeModelInfo,
  Repository,
  SingleResponse,
  User,
} from "@/lib/types";

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
    review_instructions: "",
    automated_approval_policy: "Automatically approve routine, well-tested changes when safe.",
    description_policy: {
      requirements: [
        {
          key: "description",
          title: "Understandable description",
          prompt: "Explain intent.",
          required: true,
          applies_when: { kind: "all" },
        },
        {
          key: "testing",
          title: "Testing evidence",
          prompt: "Show validation.",
          required: true,
          applicability: "nontrivial",
          applies_when: {
            kind: "nontrivial",
            min_files_changed: 2,
            min_lines_changed: 31,
          },
        },
      ],
    },
    risk_policy: {
      max_files_changed: 5,
      max_lines_changed: 300,
      require_passing_checks: true,
      exclude_sensitive_paths: true,
      sensitive_paths: ["*auth*"],
      allowed_path_patterns: ["internal/**"],
      blocked_path_patterns: ["migrations/**"],
      required_checks: ["lint", "test"],
      eligible_authors: ["anya"],
      require_up_to_date: false,
      allow_forks: false,
    },
    agent_roster: {
      reviewers: ["codex", "claude_code"],
      orchestrator: "claude_code",
      reviewer_models: ["gpt-5.4", "claude-sonnet-4-6"],
      reviewer_reasoning_efforts: ["high", "high"],
      orchestrator_model: "claude-sonnet-4-6",
      reasoning_effort: "high",
      disagreement_blocks: true,
      require_reviewer_quorum: 2,
      timeout_seconds: 1800,
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
  retryable_failure: false,
  retry_eligible: false,
  decision: "approved",
  acceptable: true,
  stale: false,
  review_output_key: "pr-1:abcdef:policy-1",
  github_review_id: 143428,
  completed_at: "2026-06-26T12:05:00Z",
  created_at: "2026-06-26T12:00:00Z",
  repository_name: "api",
  github_repo: "acme/api",
  github_pr_number: 428,
  github_pr_url: "https://github.com/acme/api/pull/428",
  github_review_url: "https://github.com/acme/api/pull/428#pullrequestreview-143428",
  pull_request_title: "Fix invoice rounding",
  pull_request_author: "anya",
};

const evidence: CodeReviewEvidence = {
  agent_results: [
    {
      id: "agent-result-1",
      org_id: "org-1",
      session_id: "session-1",
      agent_provider: "codex",
      role: "reviewer",
      status: "completed",
      raw_output: "No blocking issues found.",
      structured_result: { native_review: true, read_only: true },
      created_at: "2026-06-26T12:03:00Z",
    },
  ],
  findings: [
    {
      id: "finding-1",
      org_id: "org-1",
      session_id: "session-1",
      agent_result_id: "agent-result-1",
      dedupe_key: "src/app.ts:12",
      severity: "medium",
      confidence: "high",
      path: "src/app.ts",
      start_line: 12,
      summary: "Clarify branch name",
      body: "The branch name could be more descriptive.",
      selected_for_inline: false,
      created_at: "2026-06-26T12:04:00Z",
    },
  ],
  prompt_artifacts: [
    {
      id: "artifact-1",
      org_id: "org-1",
      session_id: "session-1",
      artifact_key: "code-review-prompts/session-1/head/reviewer-01-codex",
      role: "reviewer",
      agent_provider: "codex",
      content: "Review this PR.",
      created_at: "2026-06-26T12:02:00Z",
    },
  ],
};

const reviewStats: CodeReviewStats = {
  reviews_completed: 128,
  automatically_approved: 92,
  needs_human_review: 21,
  median_turnaround_seconds: 480,
};

const reviewAnalytics: CodeReviewAnalytics = {
  summary: {
    reviews_requested: 32,
    reviews_completed: 28,
    automatically_approved: 17,
    not_approved: 11,
    needs_human_review: 8,
    comment_only: 2,
    blocked: 0,
    approval_not_posted: 1,
    failed_reviews: 2,
    stale_reviews: 2,
    reviews_with_size_data: 24,
    reviews_with_change_breakdown: 20,
    average_lines_changed: 143,
    median_lines_changed: 96,
    average_additions: 105,
    median_additions: 70,
    average_deletions: 38,
    median_deletions: 26,
    average_files_changed: 4,
    median_files_changed: 3,
    reviews_above_size_limit: 5,
    approvals_above_size_limit: 0,
    reviews_with_findings: 9,
    reviews_with_blocking_findings: 3,
    total_findings: 14,
  },
  authors: [
    {
      author: "anya",
      reviews_completed: 12,
      automatically_approved: 9,
      not_approved: 3,
      reviews_with_size_data: 10,
      reviews_with_change_breakdown: 9,
      average_lines_changed: 88,
      median_lines_changed: 72,
      average_additions: 60,
      median_additions: 52,
      average_deletions: 28,
      median_deletions: 20,
    },
    {
      author: "sam",
      reviews_completed: 8,
      automatically_approved: 3,
      not_approved: 5,
      reviews_with_size_data: 7,
      reviews_with_change_breakdown: 6,
      average_lines_changed: 225,
      median_lines_changed: 190,
      average_additions: 150,
      median_additions: 130,
      average_deletions: 75,
      median_deletions: 60,
    },
  ],
  size_buckets: [
    { bucket: "0_49", reviews_completed: 8, automatically_approved: 7 },
    { bucket: "50_199", reviews_completed: 12, automatically_approved: 8 },
    { bucket: "200_499", reviews_completed: 3, automatically_approved: 2 },
    { bucket: "500_plus", reviews_completed: 1, automatically_approved: 0 },
  ],
  non_approval_reasons: [
    { code: "lines_limit_exceeded", reviews: 5 },
    { code: "blocking_findings", reviews: 3 },
  ],
};

const template: CodeReviewTemplateOption = {
  key: "small_backend_change",
  title: "Small backend change",
  description: "Small backend changes outside sensitive packages.",
  config: {
    ...policy.config,
    approval_mode: "approve_acceptable",
    risk_policy: {
      ...policy.config.risk_policy,
      max_files_changed: 4,
    },
  },
};

const githubTriggerReady: CodeReviewGitHubTriggerResponse = {
  status: "ready",
  repository_id: "repo-1",
  repository_full_name: "acme/api",
  github_org: "acme",
  team_slug: "143-code-reviewer",
  team_name: "143 Code Reviewer",
  team_reviewer: "@acme/143-code-reviewer",
  repo_permission: "pull",
  trigger: {
    id: "trigger-1",
    org_id: "org-1",
    repository_id: "repo-1",
    installation_id: 123,
    active: true,
    version: 1,
    team_slug: "143-code-reviewer",
    team_name: "143 Code Reviewer",
    team_id: 143,
    repo_permission: "pull",
    created_at: "2026-06-26T12:00:00Z",
  },
};

function expectCreatedAfterDaysAgo(value: string | undefined, days: number): void {
  expect(value).toBeTruthy();
  const ageMs = Date.now() - Date.parse(value ?? "");
  expect(Math.abs(ageMs - days * 24 * 60 * 60 * 1000)).toBeLessThan(5 * 60 * 1000);
}

function mockCodeReviewBaseHandlers(
  trigger: CodeReviewGitHubTriggerResponse = githubTriggerReady,
  onPolicyUpdate?: (config: CodeReviewPolicyConfig, source?: string) => void,
  initialConfig: CodeReviewPolicyConfig = policy.config,
) {
  // Autosave issues whole-config PUTs and refetches on settle, so the GET must
  // reflect the last saved config for optimistic values to stick across the
  // invalidation round-trip.
  let currentConfig: CodeReviewPolicyConfig = initialConfig;
  server.use(
    http.get("/api/v1/repositories", () =>
      HttpResponse.json({
        data: [repo],
        meta: {},
      } satisfies ListResponse<Repository>),
    ),
    http.get("/api/v1/code-reviews", () =>
      HttpResponse.json({
        data: [review],
        meta: { total_count: 1 },
      } satisfies ListResponse<CodeReviewListItem>),
    ),
    http.get("/api/v1/code-reviews/stats", () =>
      HttpResponse.json({
        data: reviewStats,
      } satisfies SingleResponse<CodeReviewStats>),
    ),
    http.get("/api/v1/code-reviews/analytics", () =>
      HttpResponse.json({
        data: reviewAnalytics,
      } satisfies SingleResponse<CodeReviewAnalytics>),
    ),
    http.get("/api/v1/code-reviews/session-1/evidence", () =>
      HttpResponse.json({
        data: evidence,
      } satisfies SingleResponse<CodeReviewEvidence>),
    ),
    http.get("/api/v1/code-reviews/templates", () =>
      HttpResponse.json({
        data: [template],
        meta: {},
      } satisfies ListResponse<CodeReviewTemplateOption>),
    ),
    http.get("/api/v1/code-reviews/prompt-examples", () =>
      HttpResponse.json({ data: {
        review_instructions: [{ key: "balanced", title: "Balanced review", description: "Balanced", instructions: "Balanced instructions" }],
        automated_approval_policies: [{ key: "conservative_low_risk", title: "Conservative low-risk approval", description: "Conservative", policy: "Conservative approval policy" }],
      } } satisfies SingleResponse<CodeReviewPromptExamplesResponse>),
    ),
    http.post("/api/v1/code-reviews/policy-events", () => new HttpResponse(null, { status: 204 })),
    http.get("/api/v1/settings/opencode-models", () => HttpResponse.json({ data: [] } satisfies SingleResponse<OpenCodeModelInfo[]>)),
    http.get("/api/v1/code-review-policies", () =>
      HttpResponse.json({
        data: { ...policy, config: currentConfig },
      } satisfies SingleResponse<CodeReviewResolvedPolicy>),
    ),
    http.put("/api/v1/code-review-policies", async ({ request }) => {
      const body = (await request.json()) as { config: CodeReviewPolicyConfig; source?: string };
      // Match SavePolicy's canonicalization so invalidation returns the exact
      // prompt value the production backend persists.
      currentConfig = {
        ...body.config,
        review_instructions: body.config.review_instructions.trim(),
        automated_approval_policy: body.config.automated_approval_policy.trim(),
      };
      onPolicyUpdate?.(currentConfig, body.source);
      return HttpResponse.json({
        data: {
          ...currentConfig,
          id: "policy-1",
          org_id: "org-1",
          active: true,
          version: 2,
          created_at: "2026-06-26T12:00:00Z",
        },
      } satisfies SingleResponse<CodeReviewPolicyRecord>);
    }),
    http.get("/api/v1/code-review-github-trigger", () =>
      HttpResponse.json({
        data: trigger,
      } satisfies SingleResponse<CodeReviewGitHubTriggerResponse>),
    ),
  );
  return {
    getCurrentConfig: () => currentConfig,
  };
}

it("previews and applies a review example without changing other policy controls", async () => {
  let saved: CodeReviewPolicyConfig | undefined;
  let source: string | undefined;
  mockCodeReviewBaseHandlers(undefined, (config, nextSource) => { saved = config; source = nextSource; });
  renderWithProviders(<CodeReviewsPage />);
  await userEvent.click(await screen.findByRole("tab", { name: "Policy" }));

  await userEvent.click(await screen.findByRole("combobox", { name: /Additional review instructions.*prompt example/i }));
  await userEvent.click(await screen.findByRole("option", { name: "Balanced review" }));
  expect(await screen.findByRole("dialog", { name: "Balanced review" })).toHaveTextContent("Only additional review instructions will be replaced");
  await userEvent.click(screen.getByRole("button", { name: "Use example" }));

  await waitFor(() => expect(saved?.review_instructions).toBe("Balanced instructions"));
  expect(saved?.approval_mode).toBe(policy.config.approval_mode);
  expect(saved?.agent_roster).toEqual(policy.config.agent_roster);
  expect(saved?.risk_policy).toEqual(policy.config.risk_policy);
  expect(source).toBe("example");
});

it("applies an approval example without changing safeguards and does not grant approval authority", async () => {
  let saved: CodeReviewPolicyConfig | undefined;
  mockCodeReviewBaseHandlers(undefined, (config) => { saved = config; });
  renderWithProviders(<CodeReviewsPage />);
  await userEvent.click(await screen.findByRole("tab", { name: "Policy" }));
  // The saved policy remains comment-only; invoke the hidden composer's example control directly after temporarily revealing it.
  await userEvent.click(screen.getByRole("radio", { name: /Approve acceptable PRs/i }));
  await waitFor(() => expect(saved?.approval_mode).toBe("approve_acceptable"));
  await userEvent.click(await screen.findByRole("combobox", { name: "Automated approval policy prompt example" }));
  await userEvent.click(await screen.findByRole("option", { name: "Conservative low-risk approval" }));
  await userEvent.click(screen.getByRole("button", { name: "Use example" }));
  await waitFor(() => expect(saved?.automated_approval_policy).toBe("Conservative approval policy"));
  expect(saved?.risk_policy).toEqual(policy.config.risk_policy);
  expect(saved?.agent_roster).toEqual(policy.config.agent_roster);
});

describe("CodeReviewsPage", () => {
  beforeEach(() => {
    toast.success.mockReset();
    toast.info.mockReset();
    toast.error.mockReset();
    sse.onEvent = undefined;
  });

  it("renders review sessions and policy configuration", async () => {
    const user = userEvent.setup();
    mockCodeReviewBaseHandlers();

    renderWithProviders(<CodeReviewsPage />);

    expect(await screen.findByRole("heading", { name: "Code reviews" })).toBeInTheDocument();
    const page = screen.getByRole("heading", { level: 1, name: "Code reviews" })
      .closest('[data-slot="list-page"]');
    expect(page?.parentElement).toHaveClass("max-w-7xl");
    const stats = await screen.findByRole("region", { name: "Code review statistics" });
    expect(within(stats).getByText("Reviews completed")).toBeInTheDocument();
    expect(within(stats).getByText("128")).toBeInTheDocument();
    expect(within(stats).getByText("Automatically approved")).toBeInTheDocument();
    expect(within(stats).getByText("92")).toBeInTheDocument();
    expect(within(stats).getByText("72% of completed reviews")).toBeInTheDocument();
    expect(within(stats).getByText("Needs human review")).toBeInTheDocument();
    expect(within(stats).getByText("21")).toBeInTheDocument();
    expect(within(stats).getByText("16% of completed reviews")).toBeInTheDocument();
    expect(within(stats).getByText("Median turnaround")).toBeInTheDocument();
    expect(within(stats).getByText("8m")).toBeInTheDocument();
    const timeWindow = screen.getByRole("combobox", { name: "Time window" });
    expect(timeWindow).toHaveTextContent("Last 30 days");
    const filters = timeWindow.closest("#code-review-filters");
    expect(filters).toContainElement(screen.getByRole("combobox", { name: "Repository" }));
    expect(filters?.lastElementChild).toContainElement(timeWindow);
    expect(screen.getByRole("heading", { level: 2, name: "Review activity" })).toBeInTheDocument();
    expect(screen.getByRole("combobox", { name: "Status" })).toHaveTextContent("Current reviews");
    expect(await screen.findAllByText("#428 Fix invoice rounding")).toHaveLength(2);
    expect(screen.getAllByText("Acceptable")).toHaveLength(2);
    expect(screen.getAllByText("Approved")).toHaveLength(2);
    expect(
      screen.getAllByText("Completed").filter((element) => element.closest('[data-slot="status-label"]')),
    ).toHaveLength(2);
    const finalReviewLinks = screen.getAllByRole("link", { name: "#428 Fix invoice rounding" });
    expect(finalReviewLinks).toHaveLength(2);
    for (const link of finalReviewLinks) {
      expect(link).toHaveAttribute("href", review.github_review_url);
      expect(link).toHaveAttribute("target", "_blank");
      expect(link.querySelector('[data-slot="external-link-icon"]')).toBeInTheDocument();
    }
    expect(screen.queryByRole("link", { name: "Open final review" })).not.toBeInTheDocument();
    const filterToggle = screen.getByRole("button", {
      name: /Filter reviews/i,
    });
    expect(filterToggle).toHaveAttribute("aria-expanded", "false");
    await user.click(filterToggle);
    expect(filterToggle).toHaveAttribute("aria-expanded", "true");
    expect(screen.getByRole("textbox", { name: "Search code reviews" })).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "Open pull request" })).not.toBeInTheDocument();
    expect(screen.getByText("Showing 1 of 1")).toBeInTheDocument();
    const reviewTable = screen.getByRole("table");
    const reviewRow = within(reviewTable).getByRole("row", {
      name: /#428 Fix invoice rounding/i,
    });
    expect(within(reviewTable).getAllByRole("columnheader").map((header) => header.textContent)).toEqual([
      "PR",
      "Outcome",
      "Risk",
      "Run status",
      "Repo",
      "Completed",
      "Actions",
    ]);
    const reviewCells = within(reviewRow).getAllByRole("cell");
    expect(within(reviewCells[2]).getByText("Acceptable").closest('[data-slot="status-label"]')).not.toBeNull();
    expect(within(reviewCells[1]).getByText("Approved").closest('[data-slot="status-label"]')).not.toBeNull();
    expect(within(reviewCells[3]).getByText("Completed").closest('[data-slot="status-label"]')).not.toBeNull();
    expect(reviewCells[2].querySelector('[aria-hidden="true"]')).toBeNull();
    expect(within(reviewCells[6]).getByRole("button", { name: "Evidence" })).toBeInTheDocument();
    expect(within(reviewCells[6]).getByRole("link", { name: "Session" }).querySelector("svg")).toBeInTheDocument();
    await user.click(screen.getAllByRole("button", { name: /Evidence/i })[0]);
    const evidenceSheet = await screen.findByRole("dialog", {
      name: /Evidence for #428/i,
    });
    expect(evidenceSheet).toBeInTheDocument();
    expect(within(evidenceSheet).getByText("No blocking issues found.")).toBeInTheDocument();
    expect(within(evidenceSheet).getByText("Clarify branch name")).toBeInTheDocument();
    expect(within(evidenceSheet).getByText("P2 · Advisory")).toBeInTheDocument();
    expect(
      within(evidenceSheet).getByText(
        "P0 and P1 findings block approval. P2 and P3 findings are advisory and are not posted as inline GitHub comments.",
      ),
    ).toBeInTheDocument();
    expect(within(evidenceSheet).getByText("Review this PR.")).toBeInTheDocument();
    expect(within(evidenceSheet).getByText("Completed")).toBeInTheDocument();
    await user.click(within(evidenceSheet).getByRole("button", { name: "Close" }));

    await user.click(await screen.findByRole("tab", { name: /Policy/i }));
    // The current behavior, outcome, and repository trigger are visible without expanding anything.
    expect(screen.getByText("Current behavior:")).toBeInTheDocument();
    expect(screen.getByText(/Comments only · 2 reviewers · quorum 2 · passing checks required · disagreement blocks approval · sensitive paths need human review/i)).toBeInTheDocument();
    expect(screen.getByRole("radio", { name: /Comment only/i })).toBeChecked();
    expect(screen.getByRole("region", { name: "Additional review instructions (optional)" })).toBeInTheDocument();
    expect(screen.getByText(/native \/review behavior without extra guidance/i)).toBeInTheDocument();
    expect(screen.getByRole("region", { name: "Automated approval policy" })).toHaveClass("hidden");
    expect(await screen.findByText("@acme/143-code-reviewer")).toBeInTheDocument();
    expect(screen.getByText("Ready")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Repair GitHub reviewer/i })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Set up GitHub reviewer/i })).not.toBeInTheDocument();

    // Advanced controls and their focused groups are collapsed by default.
    const advancedControls = screen.getByRole("button", {
      name: "Advanced controls",
    });
    expect(advancedControls).toHaveAttribute("aria-expanded", "false");
    expect(screen.queryByRole("button", { name: /Approval criteria/i })).not.toBeInTheDocument();
    await user.click(advancedControls);
    expect(advancedControls).toHaveAttribute("aria-expanded", "true");
    await user.click(screen.getByRole("button", { name: /Paths, authors & checks/i }));
    expect(await screen.findByText("*auth*")).toBeInTheDocument();
    expect(screen.getByText("internal/**")).toBeInTheDocument();
    expect(screen.getByText("migrations/**")).toBeInTheDocument();
    expect(screen.getByText("lint")).toBeInTheDocument();
    expect(screen.getByText("anya")).toBeInTheDocument();
    expect(screen.getAllByText("1 item").length).toBeGreaterThan(0);

    await user.click(screen.getByRole("button", { name: /Quality gates/i }));
    expect(await screen.findByText("Enforce sensitive paths")).toBeInTheDocument();
    expect(screen.getByText("Block reviewer disagreement")).toBeInTheDocument();
    await user.hover(screen.getByRole("button", { name: /About Require passing checks/i }));
    expect((await screen.findAllByText(/Blocks approval until the PR's required GitHub checks are passing/i)).length).toBeGreaterThan(0);

    await user.click(screen.getByRole("button", { name: /Structured PR-description checks/i }));
    expect(await screen.findByText("Understandable description")).toBeInTheDocument();
    expect(screen.getByText("Every PR")).toBeInTheDocument();
    expect(screen.getByText("Nontrivial: 2+ files or 31+ lines")).toBeInTheDocument();

    // Review depth was removed entirely.
    expect(screen.queryByRole("combobox", { name: /Review depth/i })).not.toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: /Reviewers & agents/i }));
    expect(await screen.findByRole("combobox", { name: "Reviewer 1 model" })).toBeInTheDocument();
    expect(screen.getByRole("combobox", { name: "Reviewer 2 model" })).toBeInTheDocument();
    expect(screen.getByRole("combobox", { name: "Orchestrator model" })).toBeInTheDocument();
    expect(screen.getByRole("combobox", { name: "Reviewer 1 reasoning level" })).toHaveTextContent("High");
    expect(screen.getByRole("combobox", { name: "Reviewer 2 reasoning level" })).toHaveTextContent("High");
    expect(screen.getByRole("combobox", { name: "Orchestrator reasoning level" })).toHaveTextContent("High");

    // Autosave: applying a template persists without a Save button.
    await user.click(screen.getByRole("combobox", { name: /Advanced policy preset/i }));
    await user.click(await screen.findByRole("option", { name: "Small backend change" }));
    await user.click(screen.getByRole("button", { name: /Apply preset/i }));
    await waitFor(() => {
      expect(toast.success).toHaveBeenCalledWith("Applied Small backend change");
    });
    await user.click(screen.getByRole("button", { name: /Approval criteria/i }));
    expect((await screen.findAllByDisplayValue("4")).length).toBeGreaterThan(0);
    expect(screen.getByLabelText("Timeout value")).toHaveValue(30);
    expect(screen.getByRole("combobox", { name: "Timeout unit" })).toHaveTextContent("Minutes");

    await user.click(screen.getByRole("button", { name: /Add requirement/i }));
    expect(await screen.findByDisplayValue("Custom requirement")).toBeInTheDocument();
  }, 30_000);

  it("reports approval usage, PR size, authors, and policy signals in Analytics", async () => {
    const user = userEvent.setup();
    const analyticsRequests: URLSearchParams[] = [];
    mockCodeReviewBaseHandlers();
    server.use(
      http.get("/api/v1/code-reviews/analytics", ({ request }) => {
        analyticsRequests.push(new URL(request.url).searchParams);
        return HttpResponse.json({
          data: reviewAnalytics,
        } satisfies SingleResponse<CodeReviewAnalytics>);
      }),
    );

    renderWithProviders(<CodeReviewsPage />, { nuqsHasMemory: true });
    await user.click(await screen.findByRole("tab", { name: "Analytics" }));

    expect(await screen.findByText("Usage by PR author")).toBeInTheDocument();
    expect(screen.getByText("PR size and policy fit")).toBeInTheDocument();
    expect(screen.getByText("Why reviews were not approved")).toBeInTheDocument();
    expect(screen.getByText("Review findings")).toBeInTheDocument();
    expect(screen.getByText(/20 of 28 completed reviews with a captured breakdown/)).toBeInTheDocument();
    expect(screen.getByText(/total-line and file data is available for 24 reviews/)).toBeInTheDocument();
    expect(screen.getByText("Line-count limit exceeded")).toBeInTheDocument();
    expect(screen.getByText("Reviewers found a blocking issue")).toBeInTheDocument();
    expect(screen.getByText("500+ total lines")).toBeInTheDocument();

    const authorTable = screen.getByRole("table", { name: "Code review analytics by PR author" });
    expect(within(authorTable).getAllByRole("columnheader").map((header) => header.textContent)).toEqual([
      "PR author",
      "Reviews",
      "Approved",
      "Not approved",
      "Approval rate",
      "Median additions",
      "Median deletions",
    ]);
    const anyaRow = within(authorTable).getByRole("row", { name: /anya/i });
    expect(within(anyaRow).getAllByRole("cell").map((cell) => cell.textContent)).toEqual([
      "anya",
      "12",
      "9",
      "3",
      "75%",
      "+52",
      "-20",
    ]);
    const overallRow = within(authorTable).getByRole("row", { name: /Overall/i });
    expect(overallRow.closest("tfoot")).not.toBeNull();
    expect(within(overallRow).getByRole("rowheader")).toHaveTextContent("Overall");
    expect(within(overallRow).getAllByRole("cell").map((cell) => cell.textContent)).toEqual([
      "28",
      "17",
      "11",
      "61%",
      "+70",
      "-26",
    ]);
    expect(within(overallRow).queryByRole("link")).not.toBeInTheDocument();
    expect(within(overallRow).getByRole("cell", { name: "+70 median additions overall" })).toBeInTheDocument();
    expect(within(overallRow).getByRole("cell", { name: "-26 median deletions overall" })).toBeInTheDocument();
    expect(within(overallRow).getByRole("button", { name: "About this summary" })).toBeInTheDocument();
    expect(within(anyaRow).getByRole("link", { name: "12 completed reviews by anya" })).toHaveAttribute(
      "href",
      "/code-reviews?tab=reviews&author=anya&status=completed&range=30d",
    );
    expect(within(anyaRow).getByRole("link", { name: "9 automatically approved reviews by anya" })).toHaveAttribute(
      "href",
      "/code-reviews?tab=reviews&author=anya&status=completed&range=30d&outcome=automatically_approved",
    );
    expect(within(anyaRow).getByRole("link", { name: "3 not approved reviews by anya" })).toHaveAttribute(
      "href",
      "/code-reviews?tab=reviews&author=anya&status=completed&range=30d&outcome=completed_not_approved",
    );
    await waitFor(() => expect(analyticsRequests).toHaveLength(1));
    expectCreatedAfterDaysAgo(analyticsRequests[0]?.get("created_after") ?? undefined, 30);
    expect(analyticsRequests[0]?.has("repository_id")).toBe(false);
  });

  it("restores the analytics section from the URL", async () => {
    mockCodeReviewBaseHandlers();
    server.use(
      http.get("/api/v1/code-reviews/analytics", () =>
        HttpResponse.json({ data: reviewAnalytics } satisfies SingleResponse<CodeReviewAnalytics>)),
    );

    renderWithProviders(<CodeReviewsPage />, {
      searchParams: { tab: "analytics" },
    });

    expect(await screen.findByRole("tab", { name: "Analytics" })).toHaveAttribute("data-state", "active");
    expect(await screen.findByText("Usage by PR author")).toBeInTheDocument();
  });

  it("marks overall medians as unavailable when no change breakdown was captured", async () => {
    // Author and summary medians come from percentile_cont over the same
    // filtered set, so an absent breakdown has to be absent at both levels.
    const analyticsWithoutMedians: CodeReviewAnalytics = {
      ...reviewAnalytics,
      summary: {
        ...reviewAnalytics.summary,
        reviews_with_change_breakdown: 0,
        average_additions: null,
        median_additions: null,
        average_deletions: null,
        median_deletions: null,
      },
      authors: reviewAnalytics.authors.map((author) => ({
        ...author,
        reviews_with_change_breakdown: 0,
        average_additions: null,
        median_additions: null,
        average_deletions: null,
        median_deletions: null,
      })),
    };
    mockCodeReviewBaseHandlers();
    server.use(
      http.get("/api/v1/code-reviews/analytics", () =>
        HttpResponse.json({ data: analyticsWithoutMedians } satisfies SingleResponse<CodeReviewAnalytics>)),
    );

    renderWithProviders(<CodeReviewsPage />, { searchParams: { tab: "analytics" } });

    const authorTable = await screen.findByRole("table", { name: "Code review analytics by PR author" });
    const overallRow = within(authorTable).getByRole("row", { name: /Overall/i });
    expect(within(overallRow).getAllByRole("cell").map((cell) => cell.textContent)).toEqual([
      "28",
      "17",
      "11",
      "61%",
      "—",
      "—",
    ]);
    expect(within(overallRow).getByRole("cell", { name: "No median additions data overall" })).toBeInTheDocument();
    expect(within(overallRow).getByRole("cell", { name: "No median deletions data overall" })).toBeInTheDocument();
    const anyaRow = within(authorTable).getByRole("row", { name: /anya/i });
    expect(within(anyaRow).getAllByRole("cell").map((cell) => cell.textContent)).toEqual([
      "anya",
      "12",
      "9",
      "3",
      "75%",
      "—",
      "—",
    ]);
  });

  it("shows failed and stale attempts when no review completed", async () => {
    const user = userEvent.setup();
    const failedOnlyAnalytics: CodeReviewAnalytics = {
      summary: {
        reviews_requested: 5,
        reviews_completed: 0,
        automatically_approved: 0,
        not_approved: 0,
        needs_human_review: 0,
        comment_only: 0,
        blocked: 0,
        approval_not_posted: 0,
        failed_reviews: 4,
        stale_reviews: 1,
        reviews_with_size_data: 0,
        reviews_with_change_breakdown: 0,
        average_lines_changed: null,
        median_lines_changed: null,
        average_additions: null,
        median_additions: null,
        average_deletions: null,
        median_deletions: null,
        average_files_changed: null,
        median_files_changed: null,
        reviews_above_size_limit: 0,
        approvals_above_size_limit: 0,
        reviews_with_findings: 0,
        reviews_with_blocking_findings: 0,
        total_findings: 0,
      },
      authors: [],
      size_buckets: [],
      non_approval_reasons: [],
    };
    mockCodeReviewBaseHandlers();
    server.use(
      http.get("/api/v1/code-reviews/analytics", () =>
        HttpResponse.json({
          data: failedOnlyAnalytics,
        } satisfies SingleResponse<CodeReviewAnalytics>),
      ),
    );

    renderWithProviders(<CodeReviewsPage />, { nuqsHasMemory: true });
    await user.click(await screen.findByRole("tab", { name: "Analytics" }));

    expect(await screen.findByText("5 total attempts")).toBeInTheDocument();
    expect(screen.getByText("4 failed · 1 stale")).toBeInTheDocument();
    expect(screen.getByText("No completed reviews in this time window")).toBeInTheDocument();
    expect(screen.getByText(/failed or became stale before reaching an approval decision/)).toBeInTheDocument();
  });

  it("applies shared filters to rows and stats while status scopes review activity only", async () => {
    const user = userEvent.setup();
    const listRequests: URLSearchParams[] = [];
    const statsRequests: URLSearchParams[] = [];
    mockCodeReviewBaseHandlers();
    server.use(
      http.get("/api/v1/code-reviews", ({ request }) => {
        listRequests.push(new URL(request.url).searchParams);
        return HttpResponse.json({
          data: [review],
          meta: {},
        } satisfies ListResponse<CodeReviewListItem>);
      }),
      http.get("/api/v1/code-reviews/stats", ({ request }) => {
        statsRequests.push(new URL(request.url).searchParams);
        return HttpResponse.json({
          data: reviewStats,
        } satisfies SingleResponse<CodeReviewStats>);
      }),
    );

    renderWithProviders(<CodeReviewsPage />, { nuqsHasMemory: true });

    expect(await screen.findByRole("combobox", { name: "Time window" })).toHaveTextContent("Last 30 days");
    await waitFor(() => {
      expect(listRequests.at(-1)?.get("created_after")).toBeTruthy();
      expect(statsRequests.at(-1)?.get("created_after")).toBeTruthy();
      expect(listRequests.at(-1)?.get("activity_status")).toBe("current");
      expect(statsRequests.at(-1)?.get("activity_status")).toBe("current");
    });
    const initialListCreatedAfter = listRequests.at(-1)?.get("created_after");
    const initialStatsCreatedAfter = statsRequests.at(-1)?.get("created_after");

    await user.click(screen.getByRole("combobox", { name: "Time window" }));
    await user.click(await screen.findByRole("option", { name: "Last 7 days" }));

    expect(await screen.findByRole("combobox", { name: "Time window" })).toHaveTextContent("Last 7 days");
    await waitFor(() => {
      expect(listRequests.at(-1)?.get("created_after")).not.toBe(initialListCreatedAfter);
      expect(statsRequests.at(-1)?.get("created_after")).not.toBe(initialStatsCreatedAfter);
    });
    expectCreatedAfterDaysAgo(listRequests.at(-1)?.get("created_after") ?? undefined, 7);
    expectCreatedAfterDaysAgo(statsRequests.at(-1)?.get("created_after") ?? undefined, 7);

    await user.click(screen.getByRole("button", { name: /Filter reviews/i }));
    await user.click(screen.getByRole("combobox", { name: "Repository" }));
    await user.click(await screen.findByRole("option", { name: "acme/api" }));
    await user.click(screen.getByRole("combobox", { name: "Outcome" }));
    await user.click(await screen.findByRole("option", { name: "Blocked" }));
    await user.click(screen.getByRole("combobox", { name: "Risk" }));
    await user.click(await screen.findByRole("option", { name: "Needs review" }));
    await user.click(screen.getByRole("combobox", { name: "Status" }));
    await user.click(await screen.findByRole("option", { name: "Completed" }));
    await user.type(screen.getByRole("textbox", { name: "Search code reviews" }), "invoice");

    await waitFor(() => {
      const listParams = listRequests.at(-1);
      const statsParams = statsRequests.at(-1);
      for (const params of [listParams, statsParams]) {
        expect(params?.get("repository_id")).toBe(repo.id);
        expect(params?.get("decision")).toBe("blocked");
        expect(params?.get("risk")).toBe("needs_review");
        expect(params?.get("search")).toBe("invoice");
        expectCreatedAfterDaysAgo(params?.get("created_after") ?? undefined, 7);
      }
      expect(listParams?.get("activity_status")).toBe("completed");
      expect(statsParams?.get("activity_status")).toBe("current");
      expect(listParams?.get("status")).toBeNull();
      expect(statsParams?.get("status")).toBeNull();
    });
    expect(
      [...new Set(listRequests.flatMap((params) => params.has("search") ? [params.get("search")] : []))],
    ).toEqual(["invoice"]);
    expect(
      [...new Set(statsRequests.flatMap((params) => params.has("search") ? [params.get("search")] : []))],
    ).toEqual(["invoice"]);
  });

  it("keeps superseded history out of headline metrics and ordinary activity by default", async () => {
    const user = userEvent.setup();
    const listStatuses: string[] = [];
    const statsStatuses: string[] = [];
    mockCodeReviewBaseHandlers();
    server.use(
      http.get("/api/v1/code-reviews", ({ request }) => {
        listStatuses.push(new URL(request.url).searchParams.get("activity_status") ?? "");
        return HttpResponse.json({ data: [review], meta: { total_count: 1 } });
      }),
      http.get("/api/v1/code-reviews/stats", ({ request }) => {
        statsStatuses.push(new URL(request.url).searchParams.get("activity_status") ?? "");
        return HttpResponse.json({ data: reviewStats });
      }),
    );

    renderWithProviders(<CodeReviewsPage />, { nuqsHasMemory: true });

    await waitFor(() => {
      expect(listStatuses).toContain("current");
      expect(statsStatuses.length).toBeGreaterThan(0);
      expect(new Set(statsStatuses)).toEqual(new Set(["current"]));
    });
    const statsRequestCount = statsStatuses.length;

    await user.click(screen.getByRole("combobox", { name: "Status" }));
    await user.click(await screen.findByRole("option", { name: "Superseded history" }));
    await waitFor(() => {
      expect(listStatuses).toContain("superseded");
      expect(statsStatuses).toHaveLength(statsRequestCount);
    });

    await user.click(screen.getByRole("combobox", { name: "Status" }));
    await user.click(await screen.findByRole("option", { name: "All attempts" }));
    await waitFor(() => {
      expect(listStatuses).toContain("all");
      expect(statsStatuses).toHaveLength(statsRequestCount);
    });
  });

  it("clears the whole-page time window from the filtered empty state", async () => {
    const user = userEvent.setup();
    const listCreatedAfterValues: Array<string | null> = [];
    mockCodeReviewBaseHandlers();
    server.use(
      http.get("/api/v1/code-reviews", ({ request }) => {
        const createdAfter = new URL(request.url).searchParams.get("created_after");
        listCreatedAfterValues.push(createdAfter);
        return HttpResponse.json({
          data: createdAfter ? [] : [review],
          meta: {},
        } satisfies ListResponse<CodeReviewListItem>);
      }),
    );

    renderWithProviders(<CodeReviewsPage />, { nuqsHasMemory: true });

    expect(await screen.findByText("No reviews match these filters")).toBeInTheDocument();
    expect(listCreatedAfterValues.at(-1)).toBeTruthy();

    await user.click(screen.getByRole("button", { name: "Clear filters" }));

    expect(await screen.findAllByText("#428 Fix invoice rounding")).toHaveLength(2);
    await waitFor(() => expect(listCreatedAfterValues.at(-1)).toBeNull());
    expect(screen.getByRole("combobox", { name: "Time window" })).toHaveTextContent("All time");
  });

  it("keeps rows and metrics visible while the rolling window refreshes", async () => {
    const originalSetInterval = globalThis.setInterval.bind(globalThis);
    let refreshRollingWindow: (() => void) | undefined;
    const intervalSpy = vi.spyOn(globalThis, "setInterval").mockImplementation((handler, timeout) => {
      if (timeout === 60_000) {
        refreshRollingWindow = () => handler();
        return originalSetInterval(() => undefined, timeout);
      }
      return originalSetInterval(handler, timeout);
    });
    const createdAfterValues: string[] = [];
    let blockListRefresh = false;
    let blockStatsRefresh = false;
    let releaseListRefresh: (() => void) | undefined;
    let releaseStatsRefresh: (() => void) | undefined;
    mockCodeReviewBaseHandlers();
    server.use(
      http.get("/api/v1/code-reviews", async ({ request }) => {
        createdAfterValues.push(new URL(request.url).searchParams.get("created_after") ?? "");
        if (blockListRefresh) {
          blockListRefresh = false;
          await new Promise<void>((resolve) => {
            releaseListRefresh = resolve;
          });
        }
        return HttpResponse.json({
          data: [review],
          meta: {},
        } satisfies ListResponse<CodeReviewListItem>);
      }),
      http.get("/api/v1/code-reviews/stats", async () => {
        if (blockStatsRefresh) {
          blockStatsRefresh = false;
          await new Promise<void>((resolve) => {
            releaseStatsRefresh = resolve;
          });
        }
        return HttpResponse.json({
          data: reviewStats,
        } satisfies SingleResponse<CodeReviewStats>);
      }),
    );

    try {
      renderWithProviders(<CodeReviewsPage />);

      expect(await screen.findAllByText("#428 Fix invoice rounding")).toHaveLength(2);
      const stats = await screen.findByRole("region", { name: "Code review statistics" });
      expect(await within(stats).findByText("128")).toBeInTheDocument();
      expect(refreshRollingWindow).toBeTypeOf("function");
      const initialCreatedAfter = createdAfterValues.at(-1);

      blockListRefresh = true;
      blockStatsRefresh = true;
      const nowSpy = vi.spyOn(Date, "now").mockReturnValue(Date.now() + 60_000);
      act(() => refreshRollingWindow?.());
      nowSpy.mockRestore();

      await waitFor(() => {
        expect(releaseListRefresh).toBeTypeOf("function");
        expect(releaseStatsRefresh).toBeTypeOf("function");
      });
      expect(screen.getAllByText("#428 Fix invoice rounding")).toHaveLength(2);
      expect(within(stats).getByText("128")).toBeInTheDocument();
      expect(screen.queryByText("No reviews match these filters")).not.toBeInTheDocument();

      act(() => {
        releaseListRefresh?.();
        releaseStatsRefresh?.();
      });
      await waitFor(() => expect(createdAfterValues.at(-1)).not.toBe(initialCreatedAfter));
    } finally {
      releaseListRefresh?.();
      releaseStatsRefresh?.();
      intervalSpy.mockRestore();
    }
  });

  it("labels cached metrics as stale when a background refresh fails", async () => {
    const user = userEvent.setup();
    const queryClient = createTestQueryClient();
    let statsRequests = 0;
    mockCodeReviewBaseHandlers();
    server.use(
      http.get("/api/v1/code-reviews/stats", () => {
        statsRequests += 1;
        if (statsRequests === 2) {
          return HttpResponse.json(
            {
              error: {
                code: "CODE_REVIEW_STATS_LOAD_FAILED",
                message: "failed to load code review stats",
              },
            },
            { status: 503 },
          );
        }
        return HttpResponse.json({
          data: reviewStats,
        } satisfies SingleResponse<CodeReviewStats>);
      }),
    );

    renderWithProviders(<CodeReviewsPage />, { queryClient });

    const stats = await screen.findByRole("region", { name: "Code review statistics" });
    expect(await within(stats).findByText("128")).toBeInTheDocument();

    await act(async () => {
      await queryClient.invalidateQueries({ queryKey: queryKeys.codeReviews.stats() });
    });

    const staleNotice = await within(stats).findByRole("alert");
    expect(staleNotice).toHaveTextContent("Metrics may be out of date");
    expect(staleNotice).toHaveTextContent("Showing the last successful result");
    expect(within(stats).getByText("128")).toBeInTheDocument();

    await user.click(within(staleNotice).getByRole("button", { name: "Retry" }));
    await waitFor(() => expect(within(stats).queryByRole("alert")).not.toBeInTheDocument());
    expect(statsRequests).toBeGreaterThanOrEqual(3);
  });

  it("uses the standard error notice and retries evidence loading", async () => {
    const user = userEvent.setup();
    let evidenceRequests = 0;
    mockCodeReviewBaseHandlers();
    server.use(
      http.get("/api/v1/code-reviews/session-1/evidence", () => {
        evidenceRequests += 1;
        if (evidenceRequests === 1) {
          return HttpResponse.json(
            {
              error: {
                code: "unavailable",
                message: "temporarily unavailable",
              },
            },
            { status: 503 },
          );
        }
        return HttpResponse.json({
          data: evidence,
        } satisfies SingleResponse<CodeReviewEvidence>);
      }),
    );

    renderWithProviders(<CodeReviewsPage />);

    expect(await screen.findAllByText("#428 Fix invoice rounding")).toHaveLength(2);
    await user.click(screen.getAllByRole("button", { name: /Evidence/i })[0]);
    const evidenceSheet = await screen.findByRole("dialog", {
      name: /Evidence for #428/i,
    });
    expect(within(evidenceSheet).getByRole("alert")).toHaveTextContent("Evidence could not be loaded");

    await user.click(within(evidenceSheet).getByRole("button", { name: "Retry" }));

    expect(await within(evidenceSheet).findByText("No blocking issues found.")).toBeInTheDocument();
    expect(evidenceRequests).toBe(2);
  });

  it("loads the next cursor page and updates the visible count", async () => {
    const secondReview = {
      ...review,
      id: "review-2",
      session_id: "session-2",
      github_pr_number: 429,
      pull_request_title: "Fix tax rounding",
    };
    const requestedCursors: Array<string | null> = [];
    mockCodeReviewBaseHandlers();
    server.use(
      http.get("/api/v1/code-reviews", ({ request }) => {
        const cursor = new URL(request.url).searchParams.get("cursor");
        requestedCursors.push(cursor);
        return HttpResponse.json(cursor
          ? { data: [secondReview], meta: { total_count: 2 } }
          : { data: [review], meta: { next_cursor: "review-1", total_count: 2 } });
      }),
    );
    renderWithProviders(<CodeReviewsPage />);

    expect(await screen.findByText("Showing 1 of 2")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Show 50 more" }));

    expect(await screen.findByText("Showing 2 of 2")).toBeInTheDocument();
    expect(screen.getAllByText("#429 Fix tax rounding")).toHaveLength(2);
    expect(requestedCursors).toContain("review-1");
    expect(screen.queryByRole("button", { name: "Show 50 more" })).not.toBeInTheDocument();
  });

  it("preserves loaded history and offers a refresh when a live review arrives", async () => {
    mockCodeReviewBaseHandlers();
    server.use(
      http.get("/api/v1/code-reviews", ({ request }) =>
        HttpResponse.json(new URL(request.url).searchParams.has("cursor")
          ? { data: [], meta: { total_count: 2 } }
          : { data: [review], meta: { next_cursor: "review-1", total_count: 2 } })),
    );
    renderWithProviders(<CodeReviewsPage />);
    await userEvent.click(await screen.findByRole("button", { name: "Show 50 more" }));

    act(() => sse.onEvent?.());

    expect(await screen.findByText("New reviews are available.")).toBeInTheDocument();
    expect(screen.getAllByText("#428 Fix invoice rounding")).toHaveLength(2);
    await userEvent.click(screen.getByRole("button", { name: "Refresh" }));
    expect(screen.queryByText("New reviews are available.")).not.toBeInTheDocument();
    expect(await screen.findByRole("button", { name: "Show 50 more" })).toBeInTheDocument();
  });

  it("keeps the loaded-history refresh signal when an event arrives on Analytics", async () => {
    const user = userEvent.setup();
    mockCodeReviewBaseHandlers();
    server.use(
      http.get("/api/v1/code-reviews", ({ request }) =>
        HttpResponse.json(new URL(request.url).searchParams.has("cursor")
          ? { data: [], meta: { total_count: 2 } }
          : { data: [review], meta: { next_cursor: "review-1", total_count: 2 } })),
    );
    renderWithProviders(<CodeReviewsPage />);
    await user.click(await screen.findByRole("button", { name: "Show 50 more" }));
    await user.click(screen.getByRole("tab", { name: "Analytics" }));

    act(() => sse.onEvent?.());

    await user.click(screen.getByRole("tab", { name: "Reviews" }));
    expect(await screen.findByText("New reviews are available.")).toBeInTheDocument();
    expect(screen.getAllByText("#428 Fix invoice rounding")).toHaveLength(2);
  });

  it("does not replace the first page when another feature invalidates review lists", async () => {
    const queryClient = createTestQueryClient();
    let firstPageRequests = 0;
    mockCodeReviewBaseHandlers();
    server.use(
      http.get("/api/v1/code-reviews", ({ request }) => {
        if (new URL(request.url).searchParams.has("cursor")) {
          return HttpResponse.json({ data: [], meta: { total_count: 2 } });
        }
        firstPageRequests += 1;
        return HttpResponse.json({
          data: [review],
          meta: { next_cursor: "review-1", total_count: 2 },
        });
      }),
    );
    renderWithProviders(<CodeReviewsPage />, { queryClient });
    await userEvent.click(await screen.findByRole("button", { name: "Show 50 more" }));

    await act(async () => {
      await queryClient.invalidateQueries({ queryKey: queryKeys.codeReviews.lists() });
    });

    expect(firstPageRequests).toBe(1);
    expect(screen.getAllByText("#428 Fix invoice rounding")).toHaveLength(2);
  });

  it("does not append an in-flight history page after the filter scope changes", async () => {
    const staleReview = {
      ...review,
      id: "review-stale",
      session_id: "session-stale",
      github_pr_number: 429,
      pull_request_title: "Stale history result",
    };
    let resolveHistory: (() => void) | undefined;
    const historyGate = new Promise<void>((resolve) => {
      resolveHistory = resolve;
    });
    mockCodeReviewBaseHandlers();
    server.use(
      http.get("/api/v1/code-reviews", async ({ request }) => {
        const params = new URL(request.url).searchParams;
        if (params.has("cursor")) {
          await historyGate;
          return HttpResponse.json({ data: [staleReview], meta: { total_count: 2 } });
        }
        return HttpResponse.json({
          data: [review],
          meta: params.get("decision") === "blocked"
            ? { total_count: 1 }
            : { next_cursor: "review-1", total_count: 2 },
        });
      }),
    );
    renderWithProviders(<CodeReviewsPage />, { nuqsHasMemory: true });
    await userEvent.click(await screen.findByRole("button", { name: "Show 50 more" }));
    await userEvent.click(screen.getByRole("combobox", { name: "Outcome" }));
    await userEvent.click(await screen.findByRole("option", { name: "Blocked" }));

    act(() => resolveHistory?.());

    await waitFor(() => {
      expect(screen.queryByText("#429 Stale history result")).not.toBeInTheDocument();
      expect(screen.getByText("Showing 1 of 1")).toBeInTheDocument();
    });
  });

  it("preserves loaded reviews and allows retry after a history-page failure", async () => {
    const secondReview = {
      ...review,
      id: "review-2",
      session_id: "session-2",
      github_pr_number: 429,
      pull_request_title: "Recovered history page",
    };
    let historyAttempts = 0;
    mockCodeReviewBaseHandlers();
    server.use(
      http.get("/api/v1/code-reviews", ({ request }) => {
        if (!new URL(request.url).searchParams.has("cursor")) {
          return HttpResponse.json({
            data: [review],
            meta: { next_cursor: "review-1", total_count: 2 },
          });
        }
        historyAttempts += 1;
        if (historyAttempts === 1) {
          return HttpResponse.json(
            { error: { code: "CODE_REVIEWS_LOAD_FAILED", message: "temporary failure" } },
            { status: 500 },
          );
        }
        return HttpResponse.json({ data: [secondReview], meta: { total_count: 2 } });
      }),
    );
    renderWithProviders(<CodeReviewsPage />);

    await userEvent.click(await screen.findByRole("button", { name: "Show 50 more" }));
    expect(await screen.findByText("Couldn't load more reviews.")).toBeInTheDocument();
    expect(screen.getAllByText("#428 Fix invoice rounding")).toHaveLength(2);

    await userEvent.click(screen.getByRole("button", { name: "Show 50 more" }));
    expect(await screen.findByText("Showing 2 of 2")).toBeInTheDocument();
    expect(screen.getAllByText("#429 Recovered history page")).toHaveLength(2);
  });

  it("omits the mobile completion timestamp until a review completes", async () => {
    const queuedReview: CodeReviewListItem = {
      ...review,
      status: "queued",
      decision: undefined,
      acceptable: undefined,
      completed_at: undefined,
      github_review_id: undefined,
      github_review_url: undefined,
    };
    mockCodeReviewBaseHandlers();
    server.use(
      http.get("/api/v1/code-reviews", () =>
        HttpResponse.json({
          data: [queuedReview],
          meta: {},
        } satisfies ListResponse<CodeReviewListItem>),
      ),
    );

    renderWithProviders(<CodeReviewsPage />);

    const mobileActivity = await screen.findByLabelText("Code review activity");
    expect(within(mobileActivity).getByText("Queued")).toBeInTheDocument();
    expect(within(mobileActivity).queryByText("-")).not.toBeInTheDocument();
  });

  it("presents superseded attempts as neutral history rather than failures", async () => {
    const supersededReview: CodeReviewListItem = {
      ...review,
      status: "stale",
      stale: true,
      superseded_by_session_id: "replacement-session",
      decision: "blocked",
      acceptable: false,
      github_review_id: undefined,
      github_review_url: undefined,
    };
    mockCodeReviewBaseHandlers();
    server.use(
      http.get("/api/v1/code-reviews", () =>
        HttpResponse.json({ data: [supersededReview], meta: { total_count: 1 } } satisfies ListResponse<CodeReviewListItem>),
      ),
    );

    renderWithProviders(<CodeReviewsPage />, {
      searchParams: { status: "superseded" },
    });

    const supersededLabels = await screen.findAllByText("Superseded");
    expect(supersededLabels).toHaveLength(2);
    expect(screen.getAllByText("No outcome")).toHaveLength(2);
    expect(screen.getAllByText("Not applicable")).toHaveLength(2);
    for (const label of supersededLabels) {
      expect(label).toHaveClass("text-muted-foreground");
      expect(label).not.toHaveClass("text-destructive");
    }
  });

  it("shows operational phases and the automatic GitHub retry countdown without a manual retry action", async () => {
    const waitingReview: CodeReviewListItem = {
      ...review,
      status: "running",
      phase: "waiting_for_github",
      status_code: "github_rate_limited",
      status_message: "GitHub is rate-limited. The review will resume automatically.",
      retry_at: new Date(Date.now() + 72_000).toISOString(),
      retryable_failure: true,
      decision: undefined,
      acceptable: undefined,
      completed_at: undefined,
    };
    mockCodeReviewBaseHandlers();
    server.use(
      http.get("/api/v1/code-reviews", () =>
        HttpResponse.json({ data: [waitingReview], meta: {} } satisfies ListResponse<CodeReviewListItem>),
      ),
    );

    renderWithProviders(<CodeReviewsPage />);

    expect(await screen.findAllByText("Waiting for GitHub")).toHaveLength(2);
    expect(screen.getAllByText("GitHub is rate-limited. The review will resume automatically.")).toHaveLength(2);
    expect(screen.getAllByText(/Retrying in 1m 1[12]s/)).toHaveLength(2);
    expect(screen.queryByRole("button", { name: "Retry review" })).not.toBeInTheDocument();
  });

  it("shows a retryable failure in the evidence sheet and starts a fresh review", async () => {
    const user = userEvent.setup();
    const failedReview: CodeReviewListItem = {
      ...review,
      status: "failed",
      status_code: "reviewer_failed",
      status_message: "Reviewer agents did not produce usable output.",
      retryable_failure: true,
      retry_eligible: true,
      decision: "blocked",
      acceptable: false,
      completed_at: "2026-06-26T12:05:00Z",
      github_review_id: undefined,
      github_review_url: undefined,
    };
    let requestedSessionId: string | null = null;
    let releaseRetry!: () => void;
    const retryGate = new Promise<void>((resolve) => {
      releaseRetry = resolve;
    });
    mockCodeReviewBaseHandlers();
    server.use(
      http.get("/api/v1/auth/me", () =>
        HttpResponse.json({
          data: {
            id: "member-1",
            org_id: "org-1",
            email: "member@example.com",
            name: "Member User",
            role: "member",
            created_at: "2026-01-01T00:00:00Z",
          },
        } satisfies SingleResponse<User>),
      ),
      http.get("/api/v1/code-reviews", () =>
        HttpResponse.json({ data: [failedReview], meta: {} } satisfies ListResponse<CodeReviewListItem>),
      ),
      http.post("/api/v1/code-reviews/:sessionId/retry", async ({ params }) => {
        requestedSessionId = String(params.sessionId);
        await retryGate;
        return HttpResponse.json({
          data: {
            previous_session_id: failedReview.session_id,
            session_id: "session-2",
            metadata_id: "review-2",
            job_id: "job-2",
          },
        });
      }),
    );

    renderWithProviders(<CodeReviewsPage />);

    const reviewTable = await screen.findByRole("table");
    const failedRow = within(reviewTable).getByRole("row", { name: /#428 Fix invoice rounding/i });
    expect(within(failedRow).getByText("Reviewer agents did not produce usable output.")).toBeInTheDocument();

    await user.click(within(failedRow).getByRole("button", { name: "Evidence" }));
    const evidenceSheet = await screen.findByRole("dialog", { name: /Evidence for #428/i });
    expect(within(evidenceSheet).getByRole("alert")).toHaveTextContent("Reviewer agents did not produce usable output.");

    await user.click(within(evidenceSheet).getByRole("button", { name: "Retry review" }));
    expect(await within(evidenceSheet).findByRole("button", { name: "Retrying…" })).toBeDisabled();
    expect(requestedSessionId).toBe("session-1");

    releaseRetry();
    await waitFor(() => expect(toast.success).toHaveBeenCalledWith("Code review retry started"));
    await waitFor(() => expect(screen.queryByRole("dialog", { name: /Evidence for #428/i })).not.toBeInTheDocument());
  });

  it("refreshes the review list when retry dispatch fails", async () => {
    const user = userEvent.setup();
    const failedReview: CodeReviewListItem = {
      ...review,
      status: "failed",
      status_message: "The review could not recover automatically.",
      retryable_failure: true,
      retry_eligible: true,
      decision: "blocked",
      acceptable: false,
      github_review_id: undefined,
      github_review_url: undefined,
    };
    let listRequests = 0;
    mockCodeReviewBaseHandlers();
    server.use(
      http.get("/api/v1/auth/me", () =>
        HttpResponse.json({
          data: {
            id: "member-1",
            org_id: "org-1",
            email: "member@example.com",
            name: "Member User",
            role: "member",
            created_at: "2026-01-01T00:00:00Z",
          },
        } satisfies SingleResponse<User>),
      ),
      http.get("/api/v1/code-reviews", () => {
        listRequests += 1;
        return HttpResponse.json({ data: [failedReview], meta: {} } satisfies ListResponse<CodeReviewListItem>);
      }),
      http.post("/api/v1/code-reviews/:sessionId/retry", () =>
        HttpResponse.json(
          { error: { code: "CODE_REVIEW_RETRY_FAILED", message: "The replacement could not be queued." } },
          { status: 500 },
        ),
      ),
    );

    renderWithProviders(<CodeReviewsPage />);

    const reviewTable = await screen.findByRole("table");
    const failedRow = within(reviewTable).getByRole("row", { name: /#428 Fix invoice rounding/i });
    await user.click(within(failedRow).getByRole("button", { name: "Retry review" }));

    await waitFor(() =>
      expect(toast.error).toHaveBeenCalledWith("Code review could not be retried", {
        description: "The replacement could not be queued.",
      }),
    );
    await waitFor(() => expect(listRequests).toBeGreaterThanOrEqual(2));
    expect(within(failedRow).getByRole("button", { name: "Retry review" })).toBeEnabled();
  });

  it("does not offer retry for a historical failure the server marks ineligible", async () => {
    const historicalFailure: CodeReviewListItem = {
      ...review,
      status: "failed",
      status_message: "A newer review attempt already exists.",
      retryable_failure: true,
      retry_eligible: false,
      decision: "blocked",
      acceptable: false,
      github_review_id: undefined,
      github_review_url: undefined,
    };
    mockCodeReviewBaseHandlers();
    server.use(
      http.get("/api/v1/auth/me", () =>
        HttpResponse.json({
          data: {
            id: "member-1",
            org_id: "org-1",
            email: "member@example.com",
            name: "Member User",
            role: "member",
            created_at: "2026-01-01T00:00:00Z",
          },
        } satisfies SingleResponse<User>),
      ),
      http.get("/api/v1/code-reviews", () =>
        HttpResponse.json({ data: [historicalFailure], meta: {} } satisfies ListResponse<CodeReviewListItem>),
      ),
    );

    renderWithProviders(<CodeReviewsPage />);

    expect(await screen.findAllByText("A newer review attempt already exists.")).toHaveLength(2);
    expect(screen.queryByRole("button", { name: "Retry review" })).not.toBeInTheDocument();
  });

  it("does not offer manual review retry to builders", async () => {
    const failedReview: CodeReviewListItem = {
      ...review,
      status: "failed",
      status_message: "The code review stopped before it could finish.",
      retryable_failure: true,
      retry_eligible: true,
      decision: "blocked",
      acceptable: false,
      github_review_id: undefined,
      github_review_url: undefined,
    };
    mockCodeReviewBaseHandlers();
    server.use(
      http.get("/api/v1/auth/me", () =>
        HttpResponse.json({
          data: {
            id: "builder-1",
            org_id: "org-1",
            email: "builder@example.com",
            name: "Build User",
            role: "builder",
            created_at: "2026-01-01T00:00:00Z",
          },
        } satisfies SingleResponse<User>),
      ),
      http.get("/api/v1/code-reviews", () =>
        HttpResponse.json({ data: [failedReview], meta: {} } satisfies ListResponse<CodeReviewListItem>),
      ),
    );

    renderWithProviders(<CodeReviewsPage />);

    expect(await screen.findAllByText("The code review stopped before it could finish.")).toHaveLength(2);
    expect(screen.queryByRole("button", { name: "Retry review" })).not.toBeInTheDocument();
  });

  it("shows who changed the review policy over time", async () => {
    const user = userEvent.setup();
    const members: User[] = [
      {
        id: "user-1",
        org_id: "org-1",
        email: "alice@example.com",
        name: "Alice Smith",
        role: "admin",
        created_at: "2026-01-01T00:00:00Z",
      },
      {
        id: "user-2",
        org_id: "org-1",
        email: "bob@example.com",
        name: "Bob Chen",
        role: "admin",
        created_at: "2026-01-02T00:00:00Z",
      },
    ];
    const entries: AuditLog[] = [
      {
        id: 2,
        org_id: "org-1",
        actor_type: "user",
        actor_id: "user-1",
        user_id: "user-1",
        action: "code_review_policy.updated",
        resource_type: "code_review_policy",
        resource_id: "policy-2",
        details: { source: "manual", version: 2 },
        created_at: "2026-06-26T12:05:00Z",
      },
      {
        id: 1,
        org_id: "org-1",
        actor_type: "user",
        actor_id: "user-2",
        user_id: "user-2",
        action: "code_review_policy.updated",
        resource_type: "code_review_policy",
        resource_id: "policy-1",
        details: { source: "example", version: 1 },
        created_at: "2026-06-25T09:00:00Z",
      },
    ];
    mockCodeReviewBaseHandlers();
    server.use(
      http.get("/api/v1/team/members", () =>
        HttpResponse.json({ data: members, meta: {} } satisfies ListResponse<User>),
      ),
      http.get("/api/v1/audit-logs", ({ request }) => {
        const url = new URL(request.url);
        const data = url.searchParams.get("limit") === "1" ? entries.slice(0, 1) : entries;
        return HttpResponse.json({ data, meta: {} } satisfies ListResponse<AuditLog>);
      }),
    );

    renderWithProviders(<CodeReviewsPage />);
    await user.click(await screen.findByRole("tab", { name: "Policy" }));

    const historyTrigger = await screen.findByRole("button", { name: /Last activity:.*Alice Smith/i });
    expect(historyTrigger).toBeInTheDocument();
    await user.click(historyTrigger);

    const history = await screen.findByRole("dialog", { name: "Review policy history" });
    expect(within(history).getByText("Alice Smith")).toBeInTheDocument();
    expect(within(history).getByText("Bob Chen")).toBeInTheDocument();
    expect(within(history).getAllByText("updated review policy")).toHaveLength(2);
  });

  it("exposes accessible policy guidance and the compact GitHub management disclosure", async () => {
    const user = userEvent.setup();
    mockCodeReviewBaseHandlers();
    renderWithProviders(<CodeReviewsPage />);

    await user.click(await screen.findByRole("tab", { name: /Policy/i }));

    const topLevelGuidance = [
      "Code reviews enabled",
      "Review outcome",
      "Automated approval policy",
      "Additional review instructions (optional)",
      "acme/api GitHub reviewer",
      "Advanced controls",
    ];
    for (const label of topLevelGuidance) {
      expect(screen.getByRole("button", { name: `About ${label}` })).toBeInTheDocument();
    }

    const enablement = screen.getByRole("switch", {
      name: "Code reviews enabled",
    });
    const githubHeading = screen.getByText("acme/api");
    const instructionsHeading = screen.getByText("Additional review instructions (optional)");
    const summaryHeading = screen.getByText("Current behavior:");
    const advancedTrigger = screen.getByRole("button", {
      name: "Advanced controls",
    });
    expect(advancedTrigger).toHaveClass("h-auto", "p-4", "sm:h-auto");
    expect(enablement.compareDocumentPosition(summaryHeading) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(summaryHeading.compareDocumentPosition(instructionsHeading) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(instructionsHeading.compareDocumentPosition(githubHeading) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(githubHeading.compareDocumentPosition(advancedTrigger) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();

    const outcomeInfo = screen.getByRole("button", {
      name: "About Review outcome",
    });
    await user.hover(outcomeInfo);
    expect(await screen.findByRole("tooltip")).toHaveTextContent(/Hard safeguards|deterministic safeguard/i);
    await user.unhover(outcomeInfo);
    const enablementInfo = screen.getByRole("button", { name: "About Code reviews enabled" });
    act(() => enablementInfo.focus());
    expect(await screen.findByRole("tooltip")).toHaveTextContent(/built-in default is on/i);
    act(() => enablementInfo.blur());
    const advancedInfo = screen.getByRole("button", { name: "About Advanced controls" });
    await user.hover(advancedInfo);
    expect(await screen.findByRole("tooltip")).toHaveTextContent(/deterministic approval safeguards/i);
    await user.unhover(advancedInfo);
    const instructionsInfo = screen.getByRole("button", { name: "About Additional review instructions (optional)" });
    await user.click(instructionsInfo);
    expect(await screen.findByRole("tooltip")).toHaveTextContent(/native \/review command/i);
    await user.keyboard("{Escape}");
    await user.click(screen.getByRole("radio", { name: /Approve acceptable PRs/i }));
    const approvalInfo = screen.getByRole("button", { name: "About Automated approval policy" });
    act(() => approvalInfo.focus());
    expect(await screen.findByRole("tooltip")).toHaveTextContent(/cannot bypass hard safeguards/i);
    act(() => approvalInfo.blur());

    const manage = await screen.findByRole("button", { name: "Manage" });
    expect(manage).toHaveAttribute("aria-expanded", "false");
    await user.click(manage);
    expect(manage).toHaveAttribute("aria-expanded", "true");
    expect(screen.getByText("143-code-reviewer")).toBeInTheDocument();
    expect(screen.getByText("Repository access")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Disable reviewer" })).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Advanced controls" }));
    expect(screen.getByText(/Applying a preset replaces safety controls/i)).toBeVisible();
    for (const section of ["Approval criteria", "Paths, authors & checks", "Reviewers & agents"]) {
      await user.click(screen.getByRole("button", { name: new RegExp(section, "i") }));
    }
    for (const label of [
      "Advanced policy preset",
      "Apply advanced policy preset",
      "Files changed",
      "Lines changed",
      "Inline comments",
      "Timeout",
      "Reviewer quorum",
      "Sensitive paths",
      "Allowed path patterns",
      "Blocked path patterns",
      "Required checks",
      "Eligible authors",
      "Reviewer models",
      "Add reviewer model",
      "Reviewer 1 model",
      "Reviewer 2 model",
      "Reasoning level",
      "Orchestrator model",
    ]) {
      expect(screen.getByRole("button", { name: `About ${label}` })).toBeInTheDocument();
    }

    await user.click(screen.getByRole("button", { name: /Quality gates/i }));
    for (const label of [
      "Require passing checks",
      "Enforce sensitive paths",
      "Require up-to-date branch",
      "Block reviewer disagreement",
      "Allow fork PRs",
    ]) {
      expect(screen.getByRole("button", { name: `About ${label}` })).toBeInTheDocument();
    }

    await user.click(screen.getByRole("button", { name: /Structured PR-description checks/i }));
    expect(screen.getByRole("button", { name: "About Add structured PR-description check" })).toBeInTheDocument();
  }, 30_000);

  it("filters automatic approvals and successful non-approvals as distinct outcomes", async () => {
    const user = userEvent.setup();
    const requestedOutcomes: string[] = [];
    const successfulNotApproved: CodeReviewListItem = {
      ...review,
      id: "review-2",
      session_id: "session-2",
      pull_request_id: "pr-2",
      status: "completed",
      decision: "needs_human_review",
      acceptable: false,
      github_review_id: 143429,
      github_pr_number: 429,
      github_pr_url: "https://github.com/acme/api/pull/429",
      pull_request_title: "Keep manual approval",
    };
    mockCodeReviewBaseHandlers();
    server.use(
      http.get("/api/v1/code-reviews", ({ request }) => {
        const outcome = new URL(request.url).searchParams.get("outcome") ?? "";
        requestedOutcomes.push(outcome);
        return HttpResponse.json({
          data: outcome === "completed_not_approved" ? [successfulNotApproved] : [review],
          meta: {},
        } satisfies ListResponse<CodeReviewListItem>);
      }),
    );

    renderWithProviders(<CodeReviewsPage />, { nuqsHasMemory: true });

    expect(await screen.findAllByText("Approved")).toHaveLength(2);
    expect(
      screen.getAllByText("Completed").filter((element) => element.closest('[data-slot="status-label"]')),
    ).toHaveLength(2);

    await user.click(screen.getByRole("combobox", { name: "Outcome" }));
    await user.click(
      await screen.findByRole("option", {
        name: "Ran successfully — not approved",
      }),
    );

    expect(await screen.findAllByText("#429 Keep manual approval")).toHaveLength(2);
    expect(screen.getAllByText("Review needed")).toHaveLength(4);
    expect(
      screen.getAllByText("Completed").filter((element) => element.closest('[data-slot="status-label"]')),
    ).toHaveLength(2);
    await waitFor(() => {
      expect(requestedOutcomes).toContain("completed_not_approved");
    });

    await user.click(screen.getByRole("combobox", { name: "Outcome" }));
    await user.click(await screen.findByRole("option", { name: "Automatically approved" }));

    expect(await screen.findAllByText("#428 Fix invoice rounding")).toHaveLength(2);
    await waitFor(() => {
      expect(requestedOutcomes).toContain("automatically_approved");
    });
  });

  it("restores every review filter from the URL and sends the complete filtered request", async () => {
    const requests: URLSearchParams[] = [];
    mockCodeReviewBaseHandlers();
    server.use(
      http.get("/api/v1/code-reviews", ({ request }) => {
        requests.push(new URL(request.url).searchParams);
        return HttpResponse.json({ data: [review], meta: { total_count: 1 } });
      }),
    );

    renderWithProviders(<CodeReviewsPage />, {
      searchParams: {
        repository: repo.id,
        outcome: "blocked",
        risk: "needs_review",
        status: "completed",
        author: "anya",
        search: "invoice",
      },
    });

    expect(await screen.findByRole("textbox", { name: "PR author" })).toHaveValue("anya");
    expect(await screen.findByRole("textbox", { name: "Search code reviews" })).toHaveValue("invoice");
    await waitFor(() => {
      expect(requests.some((params) =>
        params.get("repository_id") === repo.id
        && params.get("decision") === "blocked"
        && params.get("risk") === "needs_review"
        && params.get("activity_status") === "completed"
        && params.get("status") === null
        && params.get("author") === "anya"
        && params.get("search") === "invoice"
        && params.get("limit") === "50",
      )).toBe(true);
    });
  });

  it.each([
    {
      legacyStatus: "queued",
      displayedStatus: "In progress",
      activityStatus: "in_progress",
      sessionStatus: null,
    },
    {
      legacyStatus: "running",
      displayedStatus: "In progress",
      activityStatus: "in_progress",
      sessionStatus: null,
    },
    {
      legacyStatus: "stale",
      displayedStatus: "Superseded history",
      activityStatus: "superseded",
      sessionStatus: null,
    },
    {
      legacyStatus: "cancelled",
      displayedStatus: "Cancelled",
      activityStatus: "current",
      sessionStatus: "cancelled",
    },
  ])(
    "preserves the legacy $legacyStatus status URL",
    async ({ legacyStatus, displayedStatus, activityStatus, sessionStatus }) => {
      const requests: URLSearchParams[] = [];
      mockCodeReviewBaseHandlers();
      server.use(
        http.get("/api/v1/code-reviews", ({ request }) => {
          requests.push(new URL(request.url).searchParams);
          return HttpResponse.json({ data: [review], meta: { total_count: 1 } });
        }),
      );

      renderWithProviders(<CodeReviewsPage />, {
        searchParams: { status: legacyStatus },
      });

      expect(await screen.findByRole("combobox", { name: "Status" })).toHaveTextContent(displayedStatus);
      await waitFor(() => {
        expect(requests.some((params) =>
          params.get("activity_status") === activityStatus
          && params.get("status") === sessionStatus,
        )).toBe(true);
      });
    },
  );

  it("distinguishes a filtered empty result and clears the active filters", async () => {
    mockCodeReviewBaseHandlers();
    server.use(
      http.get("/api/v1/code-reviews", () =>
        HttpResponse.json({ data: [], meta: { total_count: 0 } })),
    );
    renderWithProviders(<CodeReviewsPage />, {
      searchParams: { outcome: "blocked", search: "missing" },
      nuqsHasMemory: true,
    });

    expect(await screen.findByText("No reviews match these filters")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Clear filters" }));

    await waitFor(() => {
      expect(screen.getByRole("textbox", { name: "Search code reviews" })).toHaveValue("");
      expect(screen.getByRole("combobox", { name: "Outcome" })).toHaveTextContent("All outcomes");
    });
  });

  it("edits description requirements in a focused side sheet", async () => {
    const user = userEvent.setup();
    mockCodeReviewBaseHandlers();

    renderWithProviders(<CodeReviewsPage />);

    await user.click(await screen.findByRole("tab", { name: /Policy/i }));
    await user.click(await screen.findByRole("button", { name: "Advanced controls" }));
    await user.click(
      await screen.findByRole("button", {
        name: /Structured PR-description checks/i,
      }),
    );
    await user.click(await screen.findByRole("button", { name: "Edit Testing evidence" }));

    const sheet = await screen.findByRole("dialog", {
      name: "Edit structured PR-description check",
    });
    expect(sheet).toBeInTheDocument();
    expect(screen.getByDisplayValue("Testing evidence")).toBeInTheDocument();
    expect(screen.getByRole("combobox", { name: "Requirement applicability" })).toHaveTextContent("Nontrivial");
    expect(screen.getByText("Files changed at least")).toBeInTheDocument();
    expect(screen.getByText("Lines changed at least")).toBeInTheDocument();
    expect(screen.queryByText("Categories")).not.toBeInTheDocument();
    for (const label of [
      "Title",
      "Required description check",
      "Applies to",
      "Files changed at least",
      "Lines changed at least",
      "Description check instruction",
      "Delete structured PR-description check",
    ]) {
      expect(within(sheet).getByRole("button", { name: `About ${label}` })).toBeInTheDocument();
    }

    await user.click(screen.getByRole("combobox", { name: "Requirement applicability" }));
    await user.click(await screen.findByRole("option", { name: "Paths" }));

    expect(await screen.findByText("Path patterns")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "About Path patterns" })).toBeInTheDocument();
    expect(screen.queryByText("Files changed at least")).not.toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Close" }));
    expect(await screen.findByText("Paths: no paths set")).toBeInTheDocument();
  });

  it("saves outcome choices to the existing policy fields", async () => {
    const user = userEvent.setup();
    const state = mockCodeReviewBaseHandlers();

    renderWithProviders(<CodeReviewsPage />);

    await user.click(await screen.findByRole("tab", { name: /Policy/i }));

    await user.click(await screen.findByRole("radio", { name: /^Comment only/i }));
    await waitFor(() => {
      expect(state.getCurrentConfig().enabled).toBe(true);
    });
    expect(state.getCurrentConfig().approval_mode).toBe("comment_only");

    await user.click(screen.getByRole("radio", { name: /^Approve acceptable PRs/i }));
    await waitFor(() => expect(state.getCurrentConfig().approval_mode).toBe("approve_acceptable"));

    await user.click(screen.getByRole("switch", { name: "Code reviews enabled" }));
    await waitFor(() => {
      expect(state.getCurrentConfig().enabled).toBe(false);
    });
    expect(state.getCurrentConfig().approval_mode).toBe("approve_acceptable");

    await user.click(screen.getByRole("switch", { name: "Code reviews enabled" }));
    await waitFor(() => {
      expect(state.getCurrentConfig().enabled).toBe(true);
    });
    expect(state.getCurrentConfig().approval_mode).toBe("approve_acceptable");
    expect(screen.getByRole("radio", { name: /^Approve acceptable PRs/i })).toBeChecked();
  });

  it("saves independent reasoning levels for each reviewer", async () => {
    const user = userEvent.setup();
    const state = mockCodeReviewBaseHandlers();

    renderWithProviders(<CodeReviewsPage />);
    await user.click(await screen.findByRole("tab", { name: /Policy/i }));
    await user.click(screen.getByRole("button", { name: "Advanced controls" }));
    await user.click(screen.getByRole("button", { name: /Reviewers & agents/i }));
    await user.click(await screen.findByRole("combobox", { name: "Reviewer 1 reasoning level" }));
    await user.click(await screen.findByRole("option", { name: "Extra high" }));

    await waitFor(() => {
      expect(state.getCurrentConfig().agent_roster.reviewer_reasoning_efforts).toEqual(["xhigh", "high"]);
      expect(state.getCurrentConfig().agent_roster.reasoning_effort).toBe("high");
    });

    await user.click(screen.getByRole("combobox", { name: "Reviewer 2 reasoning level" }));
    await user.click(await screen.findByRole("option", { name: "Max" }));

    await waitFor(() => {
      expect(state.getCurrentConfig().agent_roster.reviewer_reasoning_efforts).toEqual(["xhigh", "max"]);
    });

    await user.click(screen.getByRole("button", { name: "Remove reviewer 1" }));
    await waitFor(() => {
      expect(state.getCurrentConfig().agent_roster.reviewers).toEqual(["claude_code"]);
      expect(state.getCurrentConfig().agent_roster.reviewer_models).toEqual(["claude-sonnet-4-6"]);
      expect(state.getCurrentConfig().agent_roster.reviewer_reasoning_efforts).toEqual(["max"]);
    });
  });

  it("supports max reasoning for Claude-only rosters and normalizes it when Codex is selected", async () => {
    const user = userEvent.setup();
    const claudeOnlyConfig: CodeReviewPolicyConfig = {
      ...policy.config,
      agent_roster: {
        ...policy.config.agent_roster,
        reviewers: ["claude_code"],
        reviewer_models: ["claude-sonnet-4-6"],
        reviewer_reasoning_efforts: ["max"],
        orchestrator: "claude_code",
        orchestrator_model: "claude-sonnet-4-6",
        reasoning_effort: "max",
        require_reviewer_quorum: 1,
      },
    };
    const state = mockCodeReviewBaseHandlers(githubTriggerReady, undefined, claudeOnlyConfig);
    const codexCredential: CodingCredentialSummary = {
      id: "cred-codex",
      org_id: "org-1",
      scope: "org",
      priority: 1,
      agent: "codex",
      auth_type: "api_key",
      provider: "openai",
      label: "OpenAI",
      status: "healthy",
      is_default: true,
      created_at: "2026-06-26T12:00:00Z",
      updated_at: "2026-06-26T12:00:00Z",
    };
    server.use(
      http.get("/api/v1/coding-credentials", ({ request }) => {
        const scope = new URL(request.url).searchParams.get("scope");
        return HttpResponse.json({ data: scope === "personal" ? [] : [codexCredential], meta: { scope } });
      }),
    );

    renderWithProviders(<CodeReviewsPage />);
    await user.click(await screen.findByRole("tab", { name: /Policy/i }));
    await user.click(screen.getByRole("button", { name: "Advanced controls" }));
    await user.click(screen.getByRole("button", { name: /Reviewers & agents/i }));

    const reasoningSelect = await screen.findByRole("combobox", { name: "Reviewer 1 reasoning level" });
    expect(reasoningSelect).toHaveTextContent("Max");
    await user.click(reasoningSelect);
    expect(await screen.findByRole("option", { name: "Max" })).toBeInTheDocument();
    await user.keyboard("{Escape}");

    await user.click(screen.getByRole("combobox", { name: "Reviewer 1 model" }));
    await user.click(await screen.findByRole("option", { name: "gpt-5.4" }));

    await waitFor(() => {
      expect(state.getCurrentConfig().agent_roster.reviewers).toEqual(["codex"]);
      expect(state.getCurrentConfig().agent_roster.reviewer_reasoning_efforts).toEqual(["high"]);
      expect(state.getCurrentConfig().agent_roster.reasoning_effort).toBe("max");
    });
  });

  it("debounces both prompt composers and autosaves the latest full config without clobbering", async () => {
    const user = userEvent.setup();
    const updates: CodeReviewPolicyConfig[] = [];
    mockCodeReviewBaseHandlers(githubTriggerReady, (config) => updates.push(config));
    renderWithProviders(<CodeReviewsPage />);
    await user.click(await screen.findByRole("tab", { name: /Policy/i }));
    await user.click(screen.getByRole("radio", { name: /Approve acceptable PRs/i }));
    await waitFor(() => expect(updates.at(-1)?.approval_mode).toBe("approve_acceptable"));

    const reviewInstructions = within(screen.getByRole("region", { name: "Additional review instructions (optional)" })).getByRole("textbox");
    const approvalPolicy = within(screen.getByRole("region", { name: "Automated approval policy" })).getByRole("textbox");
    await user.clear(reviewInstructions);
    await user.type(reviewInstructions, "Review tenant boundaries and authorization.");
    await user.clear(approvalPolicy);
    await user.type(approvalPolicy, "Approve only routine changes with proportionate tests.");
    fireEvent.blur(approvalPolicy);

    await waitFor(() => {
      const latest = updates.at(-1);
      expect(latest?.review_instructions).toBe("Review tenant boundaries and authorization.");
      expect(latest?.automated_approval_policy).toBe("Approve only routine changes with proportionate tests.");
      expect(latest?.risk_policy).toEqual(policy.config.risk_policy);
      expect(latest?.agent_roster).toEqual(policy.config.agent_roster);
    });
    await user.click(screen.getByRole("radio", { name: /Comment only/i }));
    expect(screen.getByRole("region", { name: "Automated approval policy" })).toHaveClass("hidden");
    await user.click(screen.getByRole("radio", { name: /Approve acceptable PRs/i }));
    expect(within(screen.getByRole("region", { name: "Automated approval policy" })).getByRole("textbox")).toHaveValue("Approve only routine changes with proportionate tests.");
  });

  it("waits through a short typing pause before versioning textarea edits and flushes on blur", async () => {
    const user = userEvent.setup();
    const updates: CodeReviewPolicyConfig[] = [];
    mockCodeReviewBaseHandlers(githubTriggerReady, (config) => updates.push(config));
    renderWithProviders(<CodeReviewsPage />);
    await user.click(await screen.findByRole("tab", { name: /Policy/i }));

    const reviewInstructions = within(
      screen.getByRole("region", { name: "Additional review instructions (optional)" }),
    ).getByRole("textbox");
    fireEvent.change(reviewInstructions, { target: { value: "Review tenant boundaries." } });

    await act(async () => { await new Promise((resolve) => setTimeout(resolve, 450)); });
    expect(updates).toHaveLength(0);

    fireEvent.blur(reviewInstructions);
    await waitFor(() => expect(updates.at(-1)?.review_instructions).toBe("Review tenant boundaries."));
  });

  it("keeps a word-separating space in the approval policy while an autosave is canonicalized", async () => {
    const user = userEvent.setup();
    const state = mockCodeReviewBaseHandlers();
    renderWithProviders(<CodeReviewsPage />);
    await user.click(await screen.findByRole("tab", { name: /Policy/i }));
    await user.click(screen.getByRole("radio", { name: /Approve acceptable PRs/i }));
    await waitFor(() => expect(state.getCurrentConfig().approval_mode).toBe("approve_acceptable"));

    const approvalPolicy = within(screen.getByRole("region", { name: "Automated approval policy" })).getByRole("textbox");
    await user.clear(approvalPolicy);
    await user.type(approvalPolicy, "Approve routine ");
    fireEvent.blur(approvalPolicy);
    await waitFor(() => expect(state.getCurrentConfig().automated_approval_policy).toBe("Approve routine"));
    expect(approvalPolicy).toHaveValue("Approve routine ");

    await user.type(approvalPolicy, "changes");
    expect(approvalPolicy).toHaveValue("Approve routine changes");
    fireEvent.blur(approvalPolicy);
    await waitFor(() => expect(state.getCurrentConfig().automated_approval_policy).toBe("Approve routine changes"));
  });

  it("keeps invalid rune-count text visible without sending it", async () => {
    const user = userEvent.setup();
    const updates: CodeReviewPolicyConfig[] = [];
    mockCodeReviewBaseHandlers(githubTriggerReady, (config) => updates.push(config));
    renderWithProviders(<CodeReviewsPage />);
    await user.click(await screen.findByRole("tab", { name: /Policy/i }));
    const input = within(screen.getByRole("region", { name: "Additional review instructions (optional)" })).getByRole("textbox");
    const overLimit = "界".repeat(8001);
    fireEvent.change(input, { target: { value: overLimit } });
    fireEvent.blur(input);

    expect(input).toHaveValue(overLimit);
    expect(input).toHaveAttribute("aria-invalid", "true");
    expect(screen.getByText("8001 / 8000")).toBeInTheDocument();
    expect(screen.getByText("Prompt is too long.")).toBeInTheDocument();
    await act(async () => { await new Promise((resolve) => setTimeout(resolve, 450)); });
    expect(updates).toHaveLength(0);
  });

  it("saves at-limit text padded with trailing whitespace by trimming before the length check", async () => {
    const user = userEvent.setup();
    const updates: CodeReviewPolicyConfig[] = [];
    mockCodeReviewBaseHandlers(githubTriggerReady, (config) => updates.push(config));
    renderWithProviders(<CodeReviewsPage />);
    await user.click(await screen.findByRole("tab", { name: /Policy/i }));
    const input = within(screen.getByRole("region", { name: "Additional review instructions (optional)" })).getByRole("textbox");
    const atLimit = "界".repeat(8000);
    // Raw length 8002 > 8000, but trimmed length is exactly 8000 — the gate must
    // measure the trimmed value that actually gets persisted.
    fireEvent.change(input, { target: { value: `${atLimit}\n\n` } });
    fireEvent.blur(input);

    expect(input).toHaveAttribute("aria-invalid", "false");
    expect(screen.getByText("8000 / 8000")).toBeInTheDocument();
    expect(screen.queryByText("Prompt is too long.")).not.toBeInTheDocument();
    await waitFor(() => expect(updates.at(-1)?.review_instructions).toBe(atLimit));
  });

  it("retains local prompt text after a failed save", async () => {
    const user = userEvent.setup();
    mockCodeReviewBaseHandlers();
    server.use(http.put("/api/v1/code-review-policies", () => HttpResponse.json({ error: { code: "SAVE_FAILED", message: "failed" } }, { status: 500 })));
    renderWithProviders(<CodeReviewsPage />);
    await user.click(await screen.findByRole("tab", { name: /Policy/i }));
    const input = within(screen.getByRole("region", { name: "Additional review instructions (optional)" })).getByRole("textbox");
    await user.type(input, "Keep this unsaved local guidance");
    fireEvent.blur(input);

    expect(await screen.findAllByText("Couldn't save")).not.toHaveLength(0);
    expect(input).toHaveValue("Keep this unsaved local guidance");
  });

  it("resets organization prompts to built-in values", async () => {
    const user = userEvent.setup();
    const updates: CodeReviewPolicyConfig[] = [];
    mockCodeReviewBaseHandlers(githubTriggerReady, (config) => updates.push(config));
    renderWithProviders(<CodeReviewsPage />);
    await user.click(await screen.findByRole("tab", { name: /Policy/i }));
    await user.click(screen.getByRole("radio", { name: /Approve acceptable PRs/i }));
    await user.click(within(screen.getByRole("region", { name: "Additional review instructions (optional)" })).getByRole("button", { name: "Clear instructions" }));
    await waitFor(() => expect(updates.at(-1)?.review_instructions).toBe(""));
    await user.click(within(screen.getByRole("region", { name: "Automated approval policy" })).getByRole("button", { name: "Reset to default" }));
    await waitFor(() => expect(updates.at(-1)?.automated_approval_policy).toContain("Automatically approve routine changes"));
  });

  it("places compact example and reset actions together above each prompt editor", async () => {
    const user = userEvent.setup();
    mockCodeReviewBaseHandlers();
    renderWithProviders(<CodeReviewsPage />);
    await user.click(await screen.findByRole("tab", { name: /Policy/i }));
    await user.click(screen.getByRole("radio", { name: /Approve acceptable PRs/i }));

    const composers = ["Automated approval policy", "Additional review instructions (optional)"];
    for (const title of composers) {
      const composer = screen.getByRole("region", { name: title });
      const actions = within(composer).getByRole("group", { name: `${title} actions` });
      const examples = within(actions).getByRole("combobox", { name: `${title} prompt example` });
      const reset = within(actions).getByRole("button");
      const editor = within(composer).getByRole("textbox");

      expect(examples).toHaveTextContent("Examples");
      expect(examples).toHaveClass("border-0", "bg-transparent");
      expect(reset).toHaveClass("sm:h-8");
      expect(actions.compareDocumentPosition(editor) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    }
  });

  it("preserves prompt composer order at a mobile viewport", async () => {
    const user = userEvent.setup();
    const originalWidth = Object.getOwnPropertyDescriptor(window, "innerWidth");
    Object.defineProperty(window, "innerWidth", { configurable: true, value: 375 });
    mockCodeReviewBaseHandlers();
    renderWithProviders(<CodeReviewsPage />);
    await user.click(await screen.findByRole("tab", { name: /Policy/i }));
    await user.click(screen.getByRole("radio", { name: /Approve acceptable PRs/i }));

    const approval = screen.getByRole("region", { name: "Automated approval policy" });
    const instructions = screen.getByRole("region", { name: "Additional review instructions (optional)" });
    expect(screen.queryByText("Hard safeguards")).not.toBeInTheDocument();
    expect(approval.compareDocumentPosition(instructions) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    if (originalWidth) Object.defineProperty(window, "innerWidth", originalWidth);
  });

  it("edits paths, authors, and checks as compact autosaved lists", async () => {
    const user = userEvent.setup();
    const policyUpdates = vi.fn();
    mockCodeReviewBaseHandlers(githubTriggerReady, policyUpdates);

    renderWithProviders(<CodeReviewsPage />);

    await user.click(await screen.findByRole("tab", { name: /Policy/i }));
    await user.click(await screen.findByRole("button", { name: "Advanced controls" }));
    await user.click(await screen.findByRole("button", { name: /Paths, authors & checks/i }));

    const sensitivePathsInput = await screen.findByRole("textbox", {
      name: "Sensitive paths",
    });
    await user.type(sensitivePathsInput, " src/payments/** {enter}");

    await waitFor(() => {
      expect(policyUpdates).toHaveBeenLastCalledWith(
        expect.objectContaining({
          risk_policy: expect.objectContaining({
            sensitive_paths: ["*auth*", "src/payments/**"],
          }),
        }),
        "manual",
      );
    });
    expect(await screen.findByText("src/payments/**")).toBeInTheDocument();

    await user.click(sensitivePathsInput);
    await user.paste("src/admin/**\nsrc/reports/**\nsrc/admin/**");

    await waitFor(() => {
      expect(policyUpdates).toHaveBeenLastCalledWith(
        expect.objectContaining({
          risk_policy: expect.objectContaining({
            sensitive_paths: ["*auth*", "src/payments/**", "src/admin/**", "src/reports/**"],
          }),
        }),
        "manual",
      );
    });
    expect(await screen.findByText("src/admin/**")).toBeInTheDocument();
    expect(screen.getByText("src/reports/**")).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Remove *auth*" }));

    await waitFor(() => {
      expect(policyUpdates).toHaveBeenLastCalledWith(
        expect.objectContaining({
          risk_policy: expect.objectContaining({
            sensitive_paths: ["src/payments/**", "src/admin/**", "src/reports/**"],
          }),
        }),
        "manual",
      );
    });
    expect(screen.queryByText("*auth*")).not.toBeInTheDocument();

    const requiredChecksEditor = screen.getByText("Required checks").closest("section");
    expect(requiredChecksEditor).not.toBeNull();
    expect(within(requiredChecksEditor as HTMLElement).getByText("2 items")).toBeInTheDocument();
    expect(within(requiredChecksEditor as HTMLElement).getByText("lint")).toBeInTheDocument();
    expect(within(requiredChecksEditor as HTMLElement).getByText("test")).toBeInTheDocument();

    expect(screen.getByRole("button", { name: "Add required check" })).toBeInTheDocument();
  });

  it("surfaces template apply save failures through the shared toast", async () => {
    const user = userEvent.setup();
    mockCodeReviewBaseHandlers();
    server.use(
      http.put("/api/v1/code-review-policies", () =>
        HttpResponse.json(
          {
            error: {
              code: "SAVE_FAILED",
              message: "Policy could not be saved",
            },
          },
          { status: 500 },
        ),
      ),
    );

    renderWithProviders(<CodeReviewsPage />);

    await user.click(await screen.findByRole("tab", { name: /Policy/i }));
    await user.click(screen.getByRole("button", { name: "Advanced controls" }));
    await user.click(screen.getByRole("combobox", { name: /Advanced policy preset/i }));
    await user.click(await screen.findByRole("option", { name: "Small backend change" }));
    await user.click(screen.getByRole("button", { name: /Apply preset/i }));

    await waitFor(() => {
      expect(toast.error).toHaveBeenCalledWith("Couldn't save. Your change was reverted.");
    });
  });

  it("saves code review timeout in seconds from the selected unit", async () => {
    const user = userEvent.setup();
    const state = mockCodeReviewBaseHandlers();

    renderWithProviders(<CodeReviewsPage />);

    await user.click(await screen.findByRole("tab", { name: /Policy/i }));
    await user.click(screen.getByRole("button", { name: "Advanced controls" }));
    await user.click(screen.getByRole("button", { name: /Approval criteria/i }));

    expect(await screen.findByLabelText("Timeout value")).toHaveValue(30);
    await user.click(screen.getByRole("combobox", { name: "Timeout unit" }));
    await user.click(await screen.findByRole("option", { name: "Hours" }));

    await waitFor(() => {
      expect(state.getCurrentConfig().agent_roster.timeout_seconds).toBe(30 * 60 * 60);
    });
  });

  it("uses shared model option badges in reviewer model pickers", async () => {
    const user = userEvent.setup();
    mockCodeReviewBaseHandlers();
    const opencodeCredential: CodingCredentialSummary = {
      id: "cred-openrouter",
      org_id: "org-1",
      scope: "org",
      priority: 1,
      agent: "opencode",
      auth_type: "api_key",
      provider: "openrouter",
      label: "OpenRouter",
      status: "healthy",
      is_default: true,
      created_at: "2026-06-26T12:00:00Z",
      updated_at: "2026-06-26T12:00:00Z",
    };
    const opencodeModels: OpenCodeModelInfo[] = [
      {
        id: "glm-5.2",
        display_name: "GLM 5.2",
        routes: [
          {
            backing: "openrouter",
            transport_label: "OpenRouter",
            physical_model_id: "openrouter/z-ai/glm-5.2",
          },
          {
            backing: "opencode",
            transport_label: "OpenCode native",
            physical_model_id: "opencode/glm-5.2",
          },
        ],
      },
      {
        id: "glm-5.1",
        display_name: "GLM 5.1",
        routes: [
          {
            backing: "opencode",
            transport_label: "OpenCode native",
            physical_model_id: "opencode/glm-5.1",
          },
        ],
      },
    ];
    server.use(
      http.get("/api/v1/coding-credentials", ({ request }) => {
        const scope = new URL(request.url).searchParams.get("scope");
        return HttpResponse.json({
          data: scope === "org" ? [opencodeCredential] : [],
          meta: {},
        } satisfies ListResponse<CodingCredentialSummary>);
      }),
      http.get("/api/v1/settings/opencode-models", () => HttpResponse.json({ data: opencodeModels } satisfies SingleResponse<OpenCodeModelInfo[]>)),
    );

    renderWithProviders(<CodeReviewsPage />);

    await user.click(await screen.findByRole("tab", { name: /Policy/i }));
    await user.click(await screen.findByRole("button", { name: "Advanced controls" }));
    await user.click(await screen.findByRole("button", { name: /Reviewers & agents/i }));
    await user.click(await screen.findByRole("combobox", { name: "Reviewer 1 model" }));

    expect(await screen.findByRole("option", { name: /GLM 5\.2.*OpenRouter/ })).toBeInTheDocument();
    // GLM 5.1 has no runnable route given the configured keys, so the shared
    // picker hides it (rather than showing a disabled option).
    expect(screen.queryByRole("option", { name: /GLM 5\.1/ })).not.toBeInTheDocument();
  });

  it("renders GitHub trigger account-required state", async () => {
    const user = userEvent.setup();
    mockCodeReviewBaseHandlers({
      status: "auth_required",
      repository_id: "repo-1",
      repository_full_name: "acme/api",
      github_org: "acme",
      team_slug: "143-code-reviewer",
      team_name: "143 Code Reviewer",
      team_reviewer: "@acme/143-code-reviewer",
      repo_permission: "pull",
      message: "Connect your GitHub account before creating the reviewer team.",
    });

    renderWithProviders(<CodeReviewsPage />);

    await user.click(await screen.findByRole("tab", { name: /Policy/i }));
    expect(await screen.findByText("Needs GitHub account")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /Connect GitHub/i })).toBeInTheDocument();
  });

  it("shows reviewer setup status for every connected repository", async () => {
    const secondRepo: Repository = {
      ...repo,
      id: "repo-2",
      github_id: 144,
      full_name: "acme/web",
      clone_url: "https://github.com/acme/web.git",
    };
    mockCodeReviewBaseHandlers();
    server.use(
      http.get("/api/v1/repositories", () =>
        HttpResponse.json({ data: [repo, secondRepo], meta: {} } satisfies ListResponse<Repository>),
      ),
      http.get("/api/v1/code-review-github-trigger", ({ request }) => {
        const repositoryID = new URL(request.url).searchParams.get("repository_id");
        const trigger: CodeReviewGitHubTriggerResponse = repositoryID === secondRepo.id
          ? {
              ...githubTriggerReady,
              status: "unconfigured",
              repository_id: secondRepo.id,
              repository_full_name: secondRepo.full_name,
              trigger: undefined,
            }
          : githubTriggerReady;
        return HttpResponse.json({ data: trigger } satisfies SingleResponse<CodeReviewGitHubTriggerResponse>);
      }),
    );

    renderWithProviders(<CodeReviewsPage />);
    await userEvent.click(await screen.findByRole("tab", { name: /Policy/i }));

    const apiRepository = await screen.findByRole("region", { name: "acme/api GitHub reviewer" });
    const webRepository = await screen.findByRole("region", { name: "acme/web GitHub reviewer" });
    expect(within(apiRepository).getByText("Ready")).toBeInTheDocument();
    expect(within(apiRepository).getByText("@acme/143-code-reviewer")).toBeInTheDocument();
    expect(within(webRepository).getByText("Not configured")).toBeInTheDocument();
    expect(within(webRepository).getByRole("button", { name: "Set up GitHub reviewer" })).toBeEnabled();
  });

  it("explains why GitHub reviewer setup is disabled", async () => {
    const user = userEvent.setup();
    mockCodeReviewBaseHandlers({
      status: "auth_required",
      repository_id: "repo-1",
      repository_full_name: "acme/api",
      github_org: "acme",
      team_slug: "143-code-reviewer",
      team_name: "143 Code Reviewer",
      team_reviewer: "@acme/143-code-reviewer",
      repo_permission: "pull",
      message: "Connect your GitHub account before creating the reviewer team.",
    });

    renderWithProviders(<CodeReviewsPage />);

    await user.click(await screen.findByRole("tab", { name: /Policy/i }));
    const setupButton = await screen.findByRole("button", {
      name: /Set up GitHub reviewer/i,
    });
    expect(setupButton).toBeDisabled();

    await user.hover(setupButton);

    expect(
      await screen.findByRole("tooltip", {
        name: "Connect your GitHub account first so 143 can set up the GitHub reviewer menu option.",
      }),
    ).toBeInTheDocument();
  });

  it("surfaces GitHub trigger setup permission errors", async () => {
    const user = userEvent.setup();
    let setupCalls = 0;
    mockCodeReviewBaseHandlers({
      status: "unconfigured",
      repository_id: "repo-1",
      repository_full_name: "acme/api",
      github_org: "acme",
      team_slug: "143-code-reviewer",
      team_name: "143 Code Reviewer",
      team_reviewer: "@acme/143-code-reviewer",
      repo_permission: "pull",
    });
    server.use(
      http.post("/api/v1/code-review-github-trigger/setup", () => {
        setupCalls += 1;
        return HttpResponse.json(
          {
            error: {
              code: "GITHUB_TRIGGER_PERMISSION_REQUIRED",
              message: "GitHub rejected setup",
            },
          },
          { status: 403 },
        );
      }),
    );

    renderWithProviders(<CodeReviewsPage />);

    await user.click(await screen.findByRole("tab", { name: /Policy/i }));
    const setupButton = await screen.findByRole("button", { name: /Set up GitHub reviewer/i });
    await waitFor(() => expect(setupButton).toBeEnabled());
    await user.click(setupButton);

    await waitFor(() => {
      expect(setupCalls).toBe(1);
    });
    expect(await screen.findByText("GitHub rejected setup")).toBeInTheDocument();
  });

  it("renders policy controls read-only for viewers", async () => {
    const user = userEvent.setup();
    mockCodeReviewBaseHandlers();
    server.use(http.get("/api/v1/auth/me", () => HttpResponse.json({ data: { id: "viewer-1", org_id: "org-1", email: "viewer@example.com", name: "Viewer", role: "viewer", created_at: "2026-01-01T00:00:00Z" } })));
    renderWithProviders(<CodeReviewsPage />);
    await user.click(await screen.findByRole("tab", { name: "Policy" }));
    const viewOnlyNotice = await screen.findByText(/view-only access/i);
    expect(viewOnlyNotice).toHaveClass("bg-muted/40");
    expect(screen.getByRole("switch", { name: "Code reviews enabled" })).toBeDisabled();
    expect(screen.getByRole("textbox", { name: "Additional review instructions (optional)" })).toBeDisabled();

    expect(await screen.findByText("@acme/143-code-reviewer")).toBeInTheDocument();
    const manageButton = screen.getByRole("button", { name: "Manage" });
    expect(manageButton).toBeEnabled();
    await user.click(manageButton);
    expect(screen.getByRole("button", { name: "Disable reviewer" })).toBeDisabled();
  });

  it("surfaces prompt example loading failures with retry", async () => {
    const user=userEvent.setup();let calls=0;mockCodeReviewBaseHandlers();server.use(http.get("/api/v1/code-reviews/prompt-examples",()=>{calls+=1;return HttpResponse.json({error:{code:"EXAMPLES_FAILED",message:"examples unavailable"}},{status:500})}));renderWithProviders(<CodeReviewsPage/>);await user.click(await screen.findByRole("tab",{name:"Policy"}));expect(await screen.findByText("examples unavailable")).toBeInTheDocument();await user.click(screen.getByRole("button",{name:"Retry"}));await waitFor(()=>expect(calls).toBeGreaterThan(1));
  });

  it("re-opens the prompt example dialog when the same example is chosen twice", async () => {
    const user = userEvent.setup();
    mockCodeReviewBaseHandlers();
    renderWithProviders(<CodeReviewsPage />);
    await user.click(await screen.findByRole("tab", { name: "Policy" }));

    await user.click(await screen.findByRole("combobox", { name: /Additional review instructions.*prompt example/i }));
    await user.click(await screen.findByRole("option", { name: "Balanced review" }));
    expect(await screen.findByRole("dialog", { name: "Balanced review" })).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Cancel" }));
    await waitFor(() => expect(screen.queryByRole("dialog", { name: "Balanced review" })).not.toBeInTheDocument());

    // Selecting the identical example again must re-fire and re-open the dialog.
    await user.click(screen.getByRole("combobox", { name: /Additional review instructions.*prompt example/i }));
    await user.click(await screen.findByRole("option", { name: "Balanced review" }));
    expect(await screen.findByRole("dialog", { name: "Balanced review" })).toBeInTheDocument();
  });

  it("opens and focuses the relevant advanced subsection for structured field errors", async () => {
    const user = userEvent.setup();
    mockCodeReviewBaseHandlers();
    server.use(http.put("/api/v1/code-review-policies", () => HttpResponse.json({ error: { code: "CODE_REVIEW_POLICY_INVALID", message: "invalid code review policy", details: { field: "agent_roster" } } }, { status: 400 })));
    renderWithProviders(<CodeReviewsPage />);
    await user.click(await screen.findByRole("tab", { name: "Policy" }));
    await user.click(screen.getByRole("switch", { name: "Code reviews enabled" }));
    const subsection = await screen.findByRole("button", { name: /Reviewers & agents/i });
    await waitFor(() => expect(subsection).toHaveFocus());
    expect(screen.getByRole("button", { name: "Advanced controls" })).toHaveAttribute("aria-expanded", "true");
  });
});
