import { beforeEach, describe, it, expect, vi } from "vitest";
import { act } from "react";
import { delay, http, HttpResponse } from "msw";
import { createTestQueryClient, fireEvent, renderWithProviders as renderWithBaseProviders, screen, userEvent, waitFor, within } from "@/test/test-utils";
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

function renderWithProviders(
  ui: Parameters<typeof renderWithBaseProviders>[0],
  options?: Parameters<typeof renderWithBaseProviders>[1],
) {
  return renderWithBaseProviders(ui, { nuqsHasMemory: true, ...options });
}

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
  CodeReviewDispute,
  CodeReviewGitHubTriggerResponse,
  CodeReviewListItem,
  CodeReviewPolicyConfig,
  CodeReviewPolicyRecord,
  CodeReviewResolvedPolicy,
  CodeReviewStats,
  CodeReviewPromptExamplesResponse,
  GitHubRepositoryClaimCandidate,
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
      semantic_dedupe_cooldown_seconds: 900,
      stop_after_deterministic_failure: false,
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
  risk_reason_codes: ["blocking_findings"],
  prompt_records: [
    {
      id: "record-1",
      org_id: "org-1",
      session_id: "session-1",
      record_key: "code-review-prompts/session-1/head/reviewer-01-codex",
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
    prs_reviewed: 32,
    prs_with_completed_round: 28,
    approved_by_143: 17,
    not_approved: 11,
    approved_first_round: 10,
    median_rounds_to_approval: 2,
    needs_human_review: 8,
    comment_only: 2,
    blocked: 0,
    approval_not_posted: 1,
    prs_with_failed_attempt: 2,
    prs_with_stale_attempt: 2,
    prs_with_change_breakdown: 20,
    median_additions: 70,
    median_deletions: 26,
    prs_with_findings: 9,
    prs_with_blocking_findings: 3,
    total_findings: 14,
  },
  approval_rounds: [
    { bucket: "round_1", prs: 10 },
    { bucket: "round_2", prs: 5 },
    { bucket: "round_3", prs: 1 },
    { bucket: "round_4_plus", prs: 1 },
    { bucket: "not_yet_approved", prs: 15 },
  ],
  authors: [
    {
      author: "anya",
      prs_reviewed: 12,
      approved_by_143: 9,
      not_approved: 3,
      approved_first_round: 6,
      median_rounds_to_approval: 2,
      median_additions: 52,
      median_deletions: 20,
    },
    {
      author: "sam",
      prs_reviewed: 8,
      approved_by_143: 3,
      not_approved: 5,
      approved_first_round: 2,
      median_rounds_to_approval: 2,
      median_additions: 130,
      median_deletions: 60,
    },
  ],
  non_approval_reasons: [
    { code: "lines_limit_exceeded", prs: 5 },
    { code: "blocking_findings", prs: 3 },
  ],
  comment_requests_total: 7,
  comment_requests_by_user: [
    { github_login: "anya", requests: 5 },
    { github_login: "sam", requests: 2 },
  ],
};

const githubTriggerReady: CodeReviewGitHubTriggerResponse = {
  status: "ready",
  repository_id: "repo-1",
  repository_full_name: "acme/api",
  repository_status: "active",
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
  let disputes: CodeReviewDispute[] = [];
  server.use(
    http.get("/api/v1/repositories", () =>
      HttpResponse.json({
        data: [repo],
        meta: {},
      } satisfies ListResponse<Repository>),
    ),
    http.get("/api/v1/integrations", () =>
      HttpResponse.json({
        data: [{
          id: "int-1",
          org_id: "org-1",
          provider: "github",
          status: "active",
          github_app_installed: true,
          github_installation_id: 123,
          created_at: "2026-06-26T12:00:00Z",
        }],
        meta: {},
      }),
    ),
    http.get("/api/v1/users/me/github-status", () =>
      HttpResponse.json({
        connected: true,
        has_repo_scope: true,
        pr_authorship_mode: "user_preferred",
        pr_draft_default: false,
        account_requirement: "recommended",
        needs_reconnect: false,
      }),
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
    http.get("/api/v1/code-reviews/session-1/disputes", () =>
      HttpResponse.json({ data: disputes, meta: {} } satisfies ListResponse<CodeReviewDispute>),
    ),
    http.get("/api/v1/code-review-disputes", () =>
      HttpResponse.json({ data: [], meta: {} } satisfies ListResponse<CodeReviewDispute>),
    ),
    http.post("/api/v1/code-reviews/session-1/disputes", async ({ request }) => {
      const body = await request.json() as { body: string; contested_reason_codes?: string[] };
      const dispute: CodeReviewDispute = {
        id: "dispute-1", org_id: "org-1", session_id: "session-1", pull_request_id: "pr-1",
        repository_id: "repo-1", policy_id: "policy-1", reviewed_head_sha: review.head_sha,
        decision: "approved", filed_by_login: "anya", author_association: "MEMBER",
        author_is_pr_author: true, repository_visibility: "private", trusted: true, source: "app_ui",
        body: body.body, contested_reason_codes: body.contested_reason_codes ?? [], intake_status: "pending",
        reassessment_status: "not_requested", queue_signals: {}, queue_priority: 0,
        reply_status: "not_applicable", version: 1, created_at: "2026-06-26T12:06:00Z", updated_at: "2026-06-26T12:06:00Z",
      };
      disputes = [dispute];
      return HttpResponse.json({ data: dispute } satisfies SingleResponse<CodeReviewDispute>, { status: 201 });
    }),
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
    http.get("/api/v1/code-review-github-triggers", () =>
      HttpResponse.json({ data: [trigger], meta: {} } satisfies ListResponse<CodeReviewGitHubTriggerResponse>),
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
  await userEvent.click(screen.getByRole("button", { name: "Add instructions" }));

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
    toast.error.mockReset();
    sse.onEvent = undefined;
  });

  it("does not show inactive or required-policy messaging before the policy loads", async () => {
    const user = userEvent.setup();
    mockCodeReviewBaseHandlers();
    server.use(
      http.get("/api/v1/code-review-policies", async () => {
        await delay("infinite");
        return HttpResponse.json({ data: policy } satisfies SingleResponse<CodeReviewResolvedPolicy>);
      }),
    );

    renderWithProviders(<CodeReviewsPage />);
    await user.click(await screen.findByRole("tab", { name: "Policy" }));

    const approval = screen.getByRole("region", { name: "Automated approval policy" });
    expect(within(approval).getByRole("textbox")).toHaveAttribute("aria-invalid", "false");
    expect(within(approval).queryByText(/saved and ready/i)).not.toBeInTheDocument();
    expect(within(approval).queryByText(/automated approval policy is required/i)).not.toBeInTheDocument();
  });

  it("does not require an automated approval policy while approval is inactive", async () => {
    const user = userEvent.setup();
    mockCodeReviewBaseHandlers();
    server.use(
      http.get("/api/v1/code-review-policies", () =>
        HttpResponse.json({
          data: {
            ...policy,
            config: { ...policy.config, automated_approval_policy: "" },
          },
        } satisfies SingleResponse<CodeReviewResolvedPolicy>),
      ),
    );

    renderWithProviders(<CodeReviewsPage />);
    await user.click(await screen.findByRole("tab", { name: "Policy" }));

    const approval = screen.getByRole("region", { name: "Automated approval policy" });
    expect(within(approval).getByRole("textbox")).toHaveAttribute("aria-invalid", "false");
    expect(within(approval).getByText(/only used when “Approve acceptable PRs” is selected/i)).toBeInTheDocument();
    expect(within(approval).queryByText(/automated approval policy is required/i)).not.toBeInTheDocument();
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
    expect(within(stats).getByRole("button", { name: "About Automatically approved" })).toBeInTheDocument();
    expect(within(stats).getByText("Approval rate")).toBeInTheDocument();
    expect(within(stats).getByText("72%")).toBeInTheDocument();
    expect(within(stats).getByRole("button", { name: "About Approval rate" })).toBeInTheDocument();
    expect(within(stats).getByText("Median turnaround")).toBeInTheDocument();
    expect(within(stats).getByText("8m")).toBeInTheDocument();
    const timeWindow = screen.getByRole("button", { name: "Time window" });
    expect(timeWindow).toHaveTextContent("Last 30 days");
    const filters = timeWindow.closest("#code-review-filters");
    expect(filters).toHaveAttribute("data-slot", "card");
    expect(filters).toContainElement(screen.getByRole("combobox", { name: "Repository" }));
    expect(filters?.lastElementChild).toContainElement(timeWindow);
    expect(screen.getByText("Repository", { selector: "label" })).toHaveAttribute(
      "for",
      screen.getByRole("combobox", { name: "Repository" }).id,
    );
    expect(screen.getByText("PR author", { selector: "label" })).toHaveAttribute(
      "for",
      screen.getByRole("textbox", { name: "PR author" }).id,
    );
    expect(screen.getByText("Search", { selector: "label" })).toHaveAttribute(
      "for",
      screen.getByRole("textbox", { name: "Search code reviews" }).id,
    );
    expect(screen.getByRole("heading", { level: 2, name: "Review activity" })).toBeInTheDocument();
    expect(screen.getByRole("combobox", { name: "Status" })).toHaveTextContent("Current reviews");
    expect(await screen.findAllByText("#428 Fix invoice rounding")).toHaveLength(2);
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
      "Why not approved",
      "Run status",
      "Completed",
      "Actions",
    ]);
    const reviewCells = within(reviewRow).getAllByRole("cell");
    expect(within(reviewCells[1]).getByText("Approved").closest('[data-slot="status-label"]')).not.toBeNull();
    expect(within(reviewCells[3]).getByText("Completed").closest('[data-slot="status-label"]')).not.toBeNull();
    expect(within(reviewCells[0]).getByText(/api · anya · abcdef1/)).toBeInTheDocument();
    expect(within(reviewCells[2]).getByText("—")).toBeInTheDocument();
    expect(within(reviewCells[5]).getByRole("button", { name: "Evidence" })).toBeInTheDocument();
    expect(within(reviewCells[5]).getByRole("link", { name: "Session" }).querySelector("svg")).toBeInTheDocument();
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
    // The effective behavior, outcome, and repository trigger are visible without expanding anything.
    expect(screen.getByText("Effective policy:")).toBeInTheDocument();
    expect(screen.getByText(/Reviews use 2 reviewers with a quorum of 2 and leave comments without approving/i)).toBeInTheDocument();
    expect(screen.getByRole("radio", { name: /Comment only/i })).toBeChecked();
    expect(screen.getByRole("region", { name: "Additional review instructions (optional)" })).toBeInTheDocument();
    expect(screen.getByText(/native \/review behavior without extra guidance/i)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Add instructions" })).toHaveAttribute("aria-expanded", "false");
    expect(screen.queryByRole("textbox", { name: "Additional review instructions (optional)" })).not.toBeInTheDocument();
    expect(screen.getByRole("region", { name: "Automated approval policy" })).toBeVisible();
    expect(screen.getByText(/only used when “Approve acceptable PRs” is selected/i)).toBeInTheDocument();
    expect(await screen.findByText("@acme/143-code-reviewer")).toBeInTheDocument();
    expect(screen.getByText("Ready")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Repair GitHub reviewer/i })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Set up GitHub reviewer/i })).not.toBeInTheDocument();

    // Safeguard categories are visible without opening a second disclosure, but
    // their details still start collapsed — that is now the only progressive
    // disclosure left on this card.
    expect(screen.getByRole("heading", { level: 3, name: "Safeguards" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Safeguards" })).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: /Approval criteria/i })).toHaveAttribute("aria-expanded", "false");
    expect(screen.queryByLabelText("Files changed")).not.toBeInTheDocument();
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
    expect(screen.getByText("Stop after stable policy blockers")).toBeInTheDocument();
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

    await user.click(screen.getByRole("button", { name: /Add requirement/i }));
    expect(await screen.findByDisplayValue("Custom requirement")).toBeInTheDocument();
  }, 30_000);

  it("explains completed non-approvals in the table, mobile row, and evidence sheet", async () => {
    const user = userEvent.setup();
    const needsReview: CodeReviewListItem = {
      ...review,
      decision: "needs_human_review",
      acceptable: false,
      github_review_id: undefined,
      github_review_url: undefined,
      risk_reason_details: [
        { code: "blocking_findings" },
        { code: "files_limit_exceeded", actual: 34, limit: 25 },
      ],
    };
    const commentOnly: CodeReviewListItem = {
      ...review,
      id: "review-2",
      session_id: "session-2",
      pull_request_id: "pr-2",
      github_pr_number: 429,
      pull_request_title: "Document the billing flow",
      decision: "comment_only",
      acceptable: true,
      github_review_id: undefined,
      github_review_url: undefined,
      risk_reason_details: [],
    };
    mockCodeReviewBaseHandlers();
    server.use(
      http.get("/api/v1/code-reviews", () =>
        HttpResponse.json({ data: [needsReview, commentOnly], meta: { total_count: 2 } } satisfies ListResponse<CodeReviewListItem>),
      ),
    );

    renderWithProviders(<CodeReviewsPage />);

    const reviewTable = await screen.findByRole("table", { name: "Code reviews" });
    const needsReviewRow = within(reviewTable).getByRole("row", { name: /#428 Fix invoice rounding/i });
    const needsReviewCells = within(needsReviewRow).getAllByRole("cell");
    expect(within(needsReviewCells[2]).getByText("Reviewers found a blocking issue")).toBeInTheDocument();
    expect(within(needsReviewCells[2]).getByText("+1 more")).toBeInTheDocument();
    await user.click(
      within(needsReviewCells[2]).getByRole("button", { name: "Show all 2 reasons this review was not approved" }),
    );
    expect(await screen.findByText(/File-count limit exceeded \(34 of 25\)/)).toBeInTheDocument();

    const commentOnlyRow = within(reviewTable).getByRole("row", { name: /#429 Document the billing flow/i });
    expect(within(commentOnlyRow).getByText("Configured for comment-only reviews")).toBeInTheDocument();

    const mobileList = screen.getByRole("list", { name: "Code review activity" });
    const mobileNeedsReview = within(mobileList).getAllByRole("listitem")[0];
    expect(within(mobileNeedsReview).getByText("Why not approved")).toBeInTheDocument();
    expect(within(mobileNeedsReview).getByText("Reviewers found a blocking issue")).toBeInTheDocument();
    expect(within(mobileNeedsReview).queryByText("Risk")).not.toBeInTheDocument();

    await user.keyboard("{Escape}");
    await user.click(within(needsReviewCells[5]).getByRole("button", { name: "Evidence" }));
    const evidenceSheet = await screen.findByRole("dialog", { name: /Evidence for #428/i });
    expect(within(evidenceSheet).getByText("Why not approved")).toBeInTheDocument();
    expect(within(evidenceSheet).getByText("Reviewers found a blocking issue")).toBeInTheDocument();
    expect(within(evidenceSheet).getByText("File-count limit exceeded (34 of 25)")).toBeInTheDocument();
  });

  it("caps the non-approval reason list a path-heavy review produces", async () => {
    const user = userEvent.setup();
    const pathHeavy: CodeReviewListItem = {
      ...review,
      decision: "needs_human_review",
      acceptable: false,
      github_review_id: undefined,
      github_review_url: undefined,
      risk_reason_details: Array.from({ length: 15 }, (_, index) => ({
        code: "path_outside_scope",
        subject: `services/api/file-${index}.go`,
      })),
    };
    mockCodeReviewBaseHandlers();
    server.use(
      http.get("/api/v1/code-reviews", () =>
        HttpResponse.json({ data: [pathHeavy], meta: { total_count: 1 } } satisfies ListResponse<CodeReviewListItem>),
      ),
    );

    renderWithProviders(<CodeReviewsPage />);

    const reviewTable = await screen.findByRole("table", { name: "Code reviews" });
    const reviewRow = within(reviewTable).getByRole("row", { name: /#428 Fix invoice rounding/i });
    const cells = within(reviewRow).getAllByRole("cell");
    await user.click(within(cells[2]).getByRole("button", { name: "Show all 15 reasons this review was not approved" }));
    const popover = (await screen.findByText("Why this review was not approved")).parentElement as HTMLElement;
    expect(within(popover).getAllByRole("listitem")).toHaveLength(10);
    expect(within(popover).getByText("and 5 more")).toBeInTheDocument();
  });

  it("keeps the non-approval explanation in the evidence sheet when evidence cannot be loaded", async () => {
    const user = userEvent.setup();
    const needsReview: CodeReviewListItem = {
      ...review,
      decision: "needs_human_review",
      acceptable: false,
      github_review_id: undefined,
      github_review_url: undefined,
      risk_reason_details: [{ code: "checks_failing" }],
    };
    mockCodeReviewBaseHandlers();
    server.use(
      http.get("/api/v1/code-reviews", () =>
        HttpResponse.json({ data: [needsReview], meta: { total_count: 1 } } satisfies ListResponse<CodeReviewListItem>),
      ),
      http.get("/api/v1/code-reviews/session-1/evidence", () => HttpResponse.json({ error: "boom" }, { status: 500 })),
    );

    renderWithProviders(<CodeReviewsPage />);

    await screen.findAllByText("#428 Fix invoice rounding");
    await user.click(screen.getAllByRole("button", { name: /Evidence/i })[0]);
    const evidenceSheet = await screen.findByRole("dialog", { name: /Evidence for #428/i });
    expect(await within(evidenceSheet).findByText("Evidence could not be loaded")).toBeInTheDocument();
    expect(within(evidenceSheet).getByText("Why not approved")).toBeInTheDocument();
    expect(within(evidenceSheet).getByText("Required checks were not passing")).toBeInTheDocument();
  });

  it("renders the PR-centric Analytics report with controls before headline metrics", async () => {
    const user = userEvent.setup();
    const analyticsRequests: URLSearchParams[] = [];
    // The retired insights report has no handler in the base mocks, so a
    // regression that reintroduces the query would only log an unhandled
    // request. Record it explicitly instead so the test fails.
    const insightsRequests: string[] = [];
    mockCodeReviewBaseHandlers();
    server.use(
      http.get("/api/v1/code-reviews/analytics", ({ request }) => {
        analyticsRequests.push(new URL(request.url).searchParams);
        return HttpResponse.json({
          data: reviewAnalytics,
        } satisfies SingleResponse<CodeReviewAnalytics>);
      }),
      http.get("/api/v1/code-review-insights", ({ request }) => {
        insightsRequests.push(request.url);
        return new HttpResponse(null, { status: 410 });
      }),
    );

    renderWithProviders(<CodeReviewsPage />, { nuqsHasMemory: true });
    await user.click(await screen.findByRole("tab", { name: "Analytics" }));

    expect(await screen.findByText("Usage by PR author")).toBeInTheDocument();
    // The headline cards have to come from the same report as the tables below
    // them. The stats endpoint answers a different question (current activity
    // only, ignoring the status filter) and reports 128/92 for this fixture.
    expect(screen.queryByRole("region", { name: "Code review statistics" })).not.toBeInTheDocument();
    const approvalOutcomes = screen.getByLabelText("Approval outcomes");
    expect(within(approvalOutcomes).getByText("32")).toBeInTheDocument();
    expect(within(approvalOutcomes).getByRole("button", { name: "About PRs reviewed" })).toBeInTheDocument();
    expect(within(approvalOutcomes).getByText("17")).toBeInTheDocument();
    expect(within(approvalOutcomes).getByText("53%")).toBeInTheDocument();
    expect(within(approvalOutcomes).getByText("2.0")).toBeInTheDocument();
    expect(within(approvalOutcomes).queryByText("128")).not.toBeInTheDocument();
    expect(screen.getByText("Approval by round")).toBeInTheDocument();
    expect(screen.queryByText("Decision feedback")).not.toBeInTheDocument();
    expect(screen.getByText("Why PRs were not approved right away")).toBeInTheDocument();
    expect(screen.getByText("PR findings and operational outcomes")).toBeInTheDocument();
    const analyticsFilters = document.getElementById("code-review-analytics-filters");
    expect(analyticsFilters).not.toBeNull();
    const authorUsage = screen.getByText("Usage by PR author");
    const authorTable = screen.getByRole("table", { name: "Code review analytics by PR author" });
    expect(
      (analyticsFilters as Node).compareDocumentPosition(approvalOutcomes) & Node.DOCUMENT_POSITION_FOLLOWING,
    ).toBeTruthy();
    expect(
      approvalOutcomes.compareDocumentPosition(authorUsage) & Node.DOCUMENT_POSITION_FOLLOWING,
    ).toBeTruthy();
    expect(screen.getByText("Line-count limit exceeded")).toBeInTheDocument();
    expect(screen.getByText("Reviewers found a blocking issue")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "View 5 PRs where line-count limit exceeded" })).toHaveAttribute(
      "href",
      "/code-reviews?tab=reviews&outcome=completed_not_approved&reason=lines_limit_exceeded&status=all&range=30d",
    );
    expect(screen.getByRole("link", { name: "View 3 PRs where reviewers found a blocking issue" })).toHaveAttribute(
      "href",
      "/code-reviews?tab=reviews&outcome=completed_not_approved&reason=blocking_findings&status=all&range=30d",
    );
    expect(screen.queryByText("PR size and policy fit")).not.toBeInTheDocument();
    // The list-only filters stay in the URL for the Reviews tab, so Analytics
    // has to say which of them it ignores rather than silently dropping them.
    expect(screen.queryByLabelText("Search code reviews")).not.toBeInTheDocument();
    expect(screen.getByText(/apply to the Reviews tab only/)).toBeInTheDocument();
    expect(screen.getByText(/20 of 32 PRs whose representative assessment captured a change/))
      .toBeInTheDocument();

    expect(within(authorTable).getAllByRole("columnheader").map((header) => header.textContent)).toEqual([
      "PR author",
      "PRs",
      "Approved",
      "Not approved",
      "Approval rate",
      "First-round approval",
      "Median rounds",
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
      "6",
      "2.0",
      "+52",
      "-20",
    ]);
    expect(within(authorTable).getByText("Overall")).toBeInTheDocument();
    expect(within(authorTable).getByLabelText("32 PRs reviewed overall")).toHaveTextContent("32");
    expect(within(authorTable).getByLabelText("53% overall approval rate")).toHaveTextContent("53%");
    expect(within(authorTable).getByLabelText("2.0 median rounds to approval overall")).toHaveTextContent("2.0");
    expect(within(anyaRow).getByRole("link", { name: "12 reviewed PRs by anya" })).toHaveAttribute(
      "href",
      "/code-reviews?tab=reviews&author=anya&range=30d",
    );
    expect(within(anyaRow).getByRole("link", { name: "9 PRs approved by 143 by anya" })).toHaveAttribute(
      "href",
      "/code-reviews?tab=reviews&author=anya&range=30d&status=completed&outcome=automatically_approved",
    );
    expect(within(anyaRow).getByRole("link", { name: "3 not approved PRs by anya" })).toHaveAttribute(
      "href",
      "/code-reviews?tab=reviews&author=anya&range=30d&status=completed&outcome=completed_not_approved",
    );
    await waitFor(() => expect(analyticsRequests).toHaveLength(1));
    expectCreatedAfterDaysAgo(analyticsRequests[0]?.get("created_after") ?? undefined, 30);
    expect(analyticsRequests[0]?.has("repository_id")).toBe(false);
    await user.click(within(authorTable).getByRole("button", { name: "Sort by PR author ascending" }));
    await waitFor(() => expect(analyticsRequests.at(-1)?.get("author_sort_by")).toBe("author"));
    expect(analyticsRequests.at(-1)?.get("author_sort_order")).toBe("asc");
    expect(insightsRequests).toEqual([]);
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

	it("reveals a policy limit targeted by an Insights deep link", async () => {
		mockCodeReviewBaseHandlers();
		window.location.hash = "#policy-max-lines-changed";

		renderWithProviders(<CodeReviewsPage />, { searchParams: { tab: "policy" } });

		expect(await screen.findByRole("spinbutton", { name: "Lines changed" })).toBeInTheDocument();
		expect(document.getElementById("policy-max-lines-changed")).not.toBeNull();
	});

  it("writes tab navigation to the URL without dropping filters", async () => {
    const user = userEvent.setup();
    const onUrlUpdate = vi.fn();
    mockCodeReviewBaseHandlers();
    server.use(
      http.get("/api/v1/code-reviews/analytics", () =>
        HttpResponse.json({ data: reviewAnalytics } satisfies SingleResponse<CodeReviewAnalytics>)),
    );

    renderWithProviders(<CodeReviewsPage />, {
      searchParams: {
        repository: repo.id,
        range: "7d",
        outcome: "blocked",
      },
      nuqsHasMemory: true,
      nuqsOnUrlUpdate: onUrlUpdate,
    });

    await user.click(await screen.findByRole("tab", { name: "Analytics" }));
    await waitFor(() => {
      const update = onUrlUpdate.mock.calls.at(-1)?.[0];
      expect(update?.searchParams.get("tab")).toBe("analytics");
      expect(update?.searchParams.get("repository")).toBe(repo.id);
      expect(update?.searchParams.get("range")).toBe("7d");
      expect(update?.searchParams.get("outcome")).toBe("blocked");
      expect(update?.options.history).toBe("push");
    });

    const updatesBeforeDrilldown = onUrlUpdate.mock.calls.length;
    const drilldown = await screen.findByRole("link", { name: "12 reviewed PRs by anya" });
    drilldown.addEventListener("click", (event) => event.preventDefault(), { capture: true, once: true });
    await user.click(drilldown);
    await act(async () => {
      await new Promise((resolve) => window.setTimeout(resolve, 25));
    });
    expect(
      onUrlUpdate.mock.calls
        .slice(updatesBeforeDrilldown)
        .every(([update]) => update.searchParams.get("tab") === "analytics"),
    ).toBe(true);

    await user.click(screen.getByRole("tab", { name: "Policy" }));
    await waitFor(() => {
      const update = onUrlUpdate.mock.calls.at(-1)?.[0];
      expect(update?.searchParams.get("tab")).toBe("policy");
      expect(update?.searchParams.get("repository")).toBe(repo.id);
      expect(update?.searchParams.get("range")).toBe("7d");
      expect(update?.searchParams.get("outcome")).toBe("blocked");
      expect(update?.options.history).toBe("push");
    });
  });

  it("shows failed and stale attempts when no review completed", async () => {
    const user = userEvent.setup();
    const failedOnlyAnalytics: CodeReviewAnalytics = {
      summary: {
        prs_reviewed: 5,
        prs_with_completed_round: 0,
        approved_by_143: 0,
        not_approved: 0,
        approved_first_round: 0,
        median_rounds_to_approval: null,
        needs_human_review: 0,
        comment_only: 0,
        blocked: 0,
        approval_not_posted: 0,
        prs_with_failed_attempt: 4,
        prs_with_stale_attempt: 1,
        prs_with_change_breakdown: 0,
        median_additions: null,
        median_deletions: null,
        prs_with_findings: 0,
        prs_with_blocking_findings: 0,
        total_findings: 0,
      },
      approval_rounds: [
        { bucket: "round_1", prs: 0 },
        { bucket: "round_2", prs: 0 },
        { bucket: "round_3", prs: 0 },
        { bucket: "round_4_plus", prs: 0 },
        { bucket: "not_yet_approved", prs: 5 },
      ],
      authors: [],
      non_approval_reasons: [],
      comment_requests_total: 0,
      comment_requests_by_user: [],
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

    expect(await screen.findByRole("button", { name: "About PRs reviewed" })).toBeInTheDocument();
    expect(screen.getByText(/4 PRs had a failed attempt/)).toBeInTheDocument();
    expect(screen.getByText("Not yet approved")).toBeInTheDocument();
  });

  it("uses the shared empty state when completed reviews have no author attribution", async () => {
    const user = userEvent.setup();
    mockCodeReviewBaseHandlers();
    server.use(
      http.get("/api/v1/code-reviews/analytics", () =>
        HttpResponse.json({
          data: {
            ...reviewAnalytics,
            authors: [],
          },
        } satisfies SingleResponse<CodeReviewAnalytics>),
      ),
    );

    renderWithProviders(<CodeReviewsPage />, { nuqsHasMemory: true });
    await user.click(await screen.findByRole("tab", { name: "Analytics" }));

    expect(await screen.findByText("No author attribution available")).toBeInTheDocument();
    expect(
      screen.getByText("Completed reviews in this report could not be matched to a pull request author."),
    ).toBeInTheDocument();
    expect(screen.queryByRole("table", { name: "Code review analytics by PR author" })).not.toBeInTheDocument();
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

    expect(await screen.findByRole("button", { name: "Time window" })).toHaveTextContent("Last 30 days");
    await waitFor(() => {
      expect(listRequests.at(-1)?.get("created_after")).toBeTruthy();
      expect(statsRequests.at(-1)?.get("created_after")).toBeTruthy();
      expect(listRequests.at(-1)?.get("activity_status")).toBe("current");
      expect(statsRequests.at(-1)?.get("activity_status")).toBe("current");
    });
    await user.click(screen.getByRole("button", { name: "Sort by PR ascending" }));
    await waitFor(() => expect(listRequests.at(-1)?.get("sort_by")).toBe("pull_request"));
    expect(listRequests.at(-1)?.get("sort_order")).toBe("asc");
    await user.click(screen.getByRole("button", { name: "Sort by PR descending" }));
    await waitFor(() => expect(listRequests.at(-1)?.get("sort_order")).toBe("desc"));
    // A third click has to reach the default newest-first order again;
    // otherwise the two-state cycle strands the user in an explicit sort.
    await user.click(screen.getByRole("button", { name: "Stop sorting by PR" }));
    await waitFor(() => expect(listRequests.at(-1)?.has("sort_by")).toBe(false));
    expect(listRequests.at(-1)?.has("sort_order")).toBe(false);
    const initialListCreatedAfter = listRequests.at(-1)?.get("created_after");
    const initialStatsCreatedAfter = statsRequests.at(-1)?.get("created_after");

    await user.click(screen.getByRole("button", { name: "Time window" }));
    await user.click(await screen.findByRole("button", { name: /Last 7 days/ }));

    expect(await screen.findByRole("button", { name: "Time window" })).toHaveTextContent("Last 7 days");
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
    await user.click(screen.getByRole("combobox", { name: "Reason" }));
    await user.click(await screen.findByRole("option", { name: "Reviewers found a blocking issue" }));
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
        expect(params?.get("reason")).toBe("blocking_findings");
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

  it("restores a custom date range from the URL and sends both boundaries", async () => {
    const listRequests: URLSearchParams[] = [];
    const statsRequests: URLSearchParams[] = [];
    mockCodeReviewBaseHandlers();
    server.use(
      http.get("/api/v1/code-reviews", ({ request }) => {
        listRequests.push(new URL(request.url).searchParams);
        return HttpResponse.json({ data: [review], meta: {} } satisfies ListResponse<CodeReviewListItem>);
      }),
      http.get("/api/v1/code-reviews/stats", ({ request }) => {
        statsRequests.push(new URL(request.url).searchParams);
        return HttpResponse.json({ data: reviewStats } satisfies SingleResponse<CodeReviewStats>);
      }),
    );

    renderWithProviders(<CodeReviewsPage />, {
      searchParams: { range: "custom:2026-07-01:2026-07-31" },
    });

    expect(await screen.findByRole("button", { name: "Time window" }))
      .toHaveTextContent("Jul 1, 2026 – Jul 31, 2026");
    await waitFor(() => {
      for (const params of [listRequests.at(-1), statsRequests.at(-1)]) {
        expect(params?.get("created_after")).toBe(new Date(2026, 6, 1, 0, 0, 0, 0).toISOString());
        expect(params?.get("created_before")).toBe(new Date(2026, 6, 31, 23, 59, 59, 999).toISOString());
      }
    });
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
    await user.click(screen.getByRole("combobox", { name: "Status" }));
    await user.click(await screen.findByRole("option", { name: "Superseded history" }));
    await waitFor(() => {
      expect(listStatuses).toContain("superseded");
      expect(new Set(statsStatuses)).toEqual(new Set(["current"]));
    });

    await user.click(screen.getByRole("combobox", { name: "Status" }));
    await user.click(await screen.findByRole("option", { name: "All attempts" }));
    await waitFor(() => {
      expect(listStatuses).toContain("all");
      expect(new Set(statsStatuses)).toEqual(new Set(["current"]));
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
    expect(screen.getByRole("button", { name: "Time window" })).toHaveTextContent("All time");
  });

  it("keeps rows and metrics visible while the rolling window refreshes", async () => {
    const originalSetTimeout = globalThis.setTimeout.bind(globalThis);
    let refreshRollingWindow: (() => void) | undefined;
    const timeoutSpy = vi.spyOn(globalThis, "setTimeout").mockImplementation((handler, timeout, ...args) => {
      if (typeof timeout === "number" && timeout >= 59_000 && timeout <= 60_000) {
        refreshRollingWindow = () => {
          if (typeof handler === "function") handler(...args);
        };
        return originalSetTimeout(() => undefined, timeout);
      }
      return originalSetTimeout(handler, timeout, ...args);
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
      timeoutSpy.mockRestore();
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

  // The dispute queue and every GitHub dispute reply deep-link to
  // ?evidence=<session id>. The reviews list is windowed to 30 days and paged at
  // 50, so the sheet has to be able to open a review the list never loaded.
  it("opens the evidence sheet for a deep link to a review outside the loaded list", async () => {
    mockCodeReviewBaseHandlers();
    const archived: CodeReviewListItem = {
      ...review,
      id: "review-9",
      session_id: "session-9",
      github_pr_number: 311,
      pull_request_title: "Archived rounding fix",
    };
    let detailRequests = 0;
    server.use(
      // The list only ever returns the recent review, exactly as a 30-day
      // window would.
      http.get("/api/v1/code-reviews", () =>
        HttpResponse.json({ data: [review], meta: { total_count: 1 } } satisfies ListResponse<CodeReviewListItem>),
      ),
      http.get("/api/v1/code-reviews/session-9", () => {
        detailRequests += 1;
        return HttpResponse.json({ data: archived } satisfies SingleResponse<CodeReviewListItem>);
      }),
      http.get("/api/v1/code-reviews/session-9/evidence", () =>
        HttpResponse.json({ data: evidence } satisfies SingleResponse<CodeReviewEvidence>),
      ),
      http.get("/api/v1/code-reviews/session-9/disputes", () =>
        HttpResponse.json({ data: [], meta: {} } satisfies ListResponse<CodeReviewDispute>),
      ),
    );

    renderWithProviders(<CodeReviewsPage />, {
      searchParams: { evidence: "session-9" },
      nuqsHasMemory: true,
    });

    const evidenceSheet = await screen.findByRole("dialog", { name: /Evidence for #311/i });
    expect(within(evidenceSheet).getByText("Archived rounding fix")).toBeInTheDocument();
    expect(detailRequests).toBe(1);
  });

  it("explains an unresolvable evidence deep link instead of doing nothing", async () => {
    const user = userEvent.setup();
    mockCodeReviewBaseHandlers();
    server.use(
      http.get("/api/v1/code-reviews/session-9", () =>
        HttpResponse.json({ error: { code: "CODE_REVIEW_NOT_FOUND", message: "code review not found" } }, { status: 404 }),
      ),
      // The evidence query keys off the same URL param, so it fires and 404s too.
      http.get("/api/v1/code-reviews/session-9/evidence", () =>
        HttpResponse.json({ error: { code: "CODE_REVIEW_NOT_FOUND", message: "code review not found" } }, { status: 404 }),
      ),
    );

    renderWithProviders(<CodeReviewsPage />, {
      searchParams: { evidence: "session-9" },
      nuqsHasMemory: true,
    });

    const notice = await screen.findByRole("alert");
    expect(notice).toHaveTextContent("That code review could not be opened");
    // Clearing the link must return the page to its ordinary state.
    await user.click(within(notice).getByRole("button", { name: "Clear the evidence link" }));
    await waitFor(() => expect(screen.queryByRole("alert")).not.toBeInTheDocument());
  });

  it("does not fetch a review detail when the deep link is already in the loaded list", async () => {
    const user = userEvent.setup();
    mockCodeReviewBaseHandlers();
    let detailRequests = 0;
    server.use(
      http.get("/api/v1/code-reviews/session-1", () => {
        detailRequests += 1;
        return HttpResponse.json({ data: review } satisfies SingleResponse<CodeReviewListItem>);
      }),
    );

    renderWithProviders(<CodeReviewsPage />, { nuqsHasMemory: true });

    await user.click((await screen.findAllByRole("button", { name: /Evidence/i }))[0]);
    expect(await screen.findByRole("dialog", { name: /Evidence for #428/i })).toBeInTheDocument();
    expect(detailRequests).toBe(0);
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

  it("records unsafe approval feedback and shows it in the review timeline", async () => {
    const user = userEvent.setup();
    mockCodeReviewBaseHandlers();

    renderWithProviders(<CodeReviewsPage />);

    expect(await screen.findAllByText("#428 Fix invoice rounding")).toHaveLength(2);
    await user.click(screen.getAllByRole("button", { name: /Evidence/i })[0]);
    const evidenceSheet = await screen.findByRole("dialog", { name: /Evidence for #428/i });
    await user.click(within(evidenceSheet).getByRole("button", { name: "Report an unsafe approval" }));

    const feedbackDialog = await screen.findByRole("dialog", { name: "Report an unsafe approval" });
    await user.type(within(feedbackDialog).getByLabelText("What should be reconsidered?"), "This approval missed an authorization bypass.");
    await user.click(within(feedbackDialog).getByLabelText(/blocking findings/i));
    await user.click(within(feedbackDialog).getByRole("button", { name: "Record feedback" }));

    expect(await within(evidenceSheet).findByText("This approval missed an authorization bypass.")).toBeInTheDocument();
    expect(within(evidenceSheet).getByText("Pending")).toBeInTheDocument();
  });

  it("labels a classified bare mention as an ordinary review request", async () => {
    const user = userEvent.setup();
    const reviewRequest: CodeReviewDispute = {
      id: "review-request-1", org_id: "org-1", session_id: "session-1", pull_request_id: "pr-1",
      repository_id: "repo-1", policy_id: "policy-1", reviewed_head_sha: review.head_sha,
      decision: "approved", direction: "should_not_have_approved", filed_by_login: "anya",
      author_association: "MEMBER", author_is_pr_author: true, repository_visibility: "private",
      trusted: true, source: "github_comment", body: "@acme/reviewers please review again",
      contested_reason_codes: [], intake_status: "discarded", routing: "review_request",
      reassessment_status: "not_requested", queue_signals: {}, queue_priority: 0,
      reply_status: "not_applicable", version: 2, created_at: "2026-06-26T12:06:00Z", updated_at: "2026-06-26T12:06:00Z",
    };
    mockCodeReviewBaseHandlers();
    server.use(
      http.get("/api/v1/code-reviews/session-1/disputes", () =>
        HttpResponse.json({ data: [reviewRequest], meta: {} } satisfies ListResponse<CodeReviewDispute>),
      ),
    );

    renderWithProviders(<CodeReviewsPage />);

    expect(await screen.findAllByText("#428 Fix invoice rounding")).toHaveLength(2);
    await user.click(screen.getAllByRole("button", { name: /Evidence/i })[0]);
    const evidenceSheet = await screen.findByRole("dialog", { name: /Evidence for #428/i });
    expect(within(evidenceSheet).getByText("Review requested")).toBeInTheDocument();
    expect(within(evidenceSheet).getByText("Ordinary review request")).toBeInTheDocument();
    expect(within(evidenceSheet).queryByText("Discarded")).not.toBeInTheDocument();
  });

  it("does not request or show decision feedback for viewer roles", async () => {
    const user = userEvent.setup();
    let disputeRequests = 0;
    mockCodeReviewBaseHandlers();
    server.use(
      http.get("/api/v1/auth/me", () => HttpResponse.json({
        data: {
          id: "viewer-1",
          org_id: "org-1",
          email: "viewer@example.com",
          name: "Viewer",
          role: "viewer",
          created_at: "2026-01-01T00:00:00Z",
        } satisfies User,
      })),
      http.get("/api/v1/code-reviews/session-1/disputes", () => {
        disputeRequests += 1;
        return HttpResponse.json({ data: [], meta: {} } satisfies ListResponse<CodeReviewDispute>);
      }),
    );

    renderWithProviders(<CodeReviewsPage />);

    expect(await screen.findAllByText("#428 Fix invoice rounding")).toHaveLength(2);
    await user.click(screen.getAllByRole("button", { name: /Evidence/i })[0]);
    const evidenceSheet = await screen.findByRole("dialog", { name: /Evidence for #428/i });

    expect(within(evidenceSheet).queryByText("Decision feedback")).not.toBeInTheDocument();
    expect(disputeRequests).toBe(0);
  });

  it("opens the linked evidence timeline from the dispute URL", async () => {
    mockCodeReviewBaseHandlers();

    renderWithProviders(<CodeReviewsPage />, { searchParams: { evidence: "session-1" } });

    const evidenceSheet = await screen.findByRole("dialog", { name: /Evidence for #428/i });
    expect(within(evidenceSheet).getByText("Decision feedback")).toBeInTheDocument();
  });

  it("keeps the dispute list concise and submits a CAS adjudication from the review sheet", async () => {
    const user = userEvent.setup();
    let updateBody: unknown;
    let currentTime = 1_000;
    const performanceNow = vi.spyOn(performance, "now").mockImplementation(() => currentTime);
    const dispute: CodeReviewDispute = {
      id: "dispute-queue-1", org_id: "org-1", session_id: "session-1", pull_request_id: "pr-1",
      repository_id: "repo-1", policy_id: "policy-1", reviewed_head_sha: review.head_sha,
      decision: "blocked", direction: "should_have_approved", filed_by_login: "anya",
      author_association: "MEMBER", author_is_pr_author: true, repository_visibility: "private",
      trusted: true, source: "app_ui", body: "The size exception was already approved.",
      contested_reason_codes: ["lines_limit_exceeded"], intake_status: "triaged", routing: "policy_signal_only",
      reassessment_status: "not_requested", adjudication_status: "pending", queue_signals: {
        pull_request_title: "Fix invoice rounding", github_repository: "acme/api", github_pr_number: 428,
        github_pr_url: "https://github.com/acme/api/pull/428",
        independent_human_contradiction: true, reassessment_unchanged: true,
        filer_is_not_pr_author: true, repeat_reason_disputes_14_days: 3,
        ranking_enabled: true,
      }, queue_priority: 75,
      reply_status: "not_applicable", version: 3, created_at: "2026-06-26T12:06:00Z", updated_at: "2026-06-26T12:06:00Z",
    };
    const secondDispute: CodeReviewDispute = {
      ...dispute,
      id: "dispute-queue-2",
      body: "A second objection under review.",
      queue_signals: {},
      queue_priority: 0,
    };
    mockCodeReviewBaseHandlers();
    server.use(
      http.get("/api/v1/code-review-disputes", () => HttpResponse.json({ data: [dispute, secondDispute], meta: {} } satisfies ListResponse<CodeReviewDispute>)),
      http.patch("/api/v1/code-review-disputes/dispute-queue-1", async ({ request }) => {
        updateBody = await request.json();
        return HttpResponse.json({ data: { ...dispute, adjudication_status: "upheld", version: 4 } } satisfies SingleResponse<CodeReviewDispute>);
      }),
    );

    renderWithProviders(<CodeReviewsPage />);

    await user.click(await screen.findByRole("tab", { name: "Disputes" }));
    const disputeTable = await screen.findByRole("table", { name: "Code review disputes" });
    expect(within(disputeTable).getByRole("columnheader", { name: "Objection" })).toBeInTheDocument();
    expect(within(disputeTable).getByRole("columnheader", { name: "Original decision" })).toBeInTheDocument();
    expect(within(disputeTable).getByRole("columnheader", { name: "Reassessment" })).toBeInTheDocument();
    expect(within(disputeTable).queryByRole("columnheader", { name: "Queue signals" })).not.toBeInTheDocument();
    expect(within(disputeTable).queryByRole("columnheader", { name: "Trust" })).not.toBeInTheDocument();
    expect(screen.queryByText("Human reviewer disagreed")).not.toBeInTheDocument();
    const disputeRow = within(disputeTable).getByText("The size exception was already approved.").closest("tr");
    expect(disputeRow).not.toBeNull();
    // Self-describing, so it cannot read as a second verdict beside "Blocked".
    expect(within(disputeRow as HTMLTableRowElement).getByText("Asks to approve")).toBeInTheDocument();
    await user.click(within(disputeRow as HTMLTableRowElement).getByRole("button", { name: "Review dispute on acme/api #428" }));
    const disputeSheet = await screen.findByRole("dialog", { name: "Review dispute" });
    expect(within(disputeSheet).getByText("Human reviewer disagreed")).toBeInTheDocument();
    expect(within(disputeSheet).getByText("Same result after reassessment")).toBeInTheDocument();
    expect(within(disputeSheet).getByText("Filed by another contributor")).toBeInTheDocument();
    expect(within(disputeSheet).getByText("3 similar objections")).toBeInTheDocument();
    expect(within(disputeSheet).getByText("Queue priority 75")).toBeInTheDocument();
    expect(within(disputeSheet).getByRole("link", { name: "acme/api #428" })).toHaveAttribute("href", "https://github.com/acme/api/pull/428");
    currentTime = 2_000;
    await user.type(within(disputeSheet).getByLabelText(/Decision note/), "Confirmed exception in the policy record.");
    await user.click(within(disputeSheet).getByRole("button", { name: "Uphold" }));

    await waitFor(() => expect(updateBody).toEqual({
      expected_version: 3,
      adjudication_status: "upheld",
      adjudication_note: "Confirmed exception in the policy record.",
      policy_owner_active_seconds: 1,
    }));
    performanceNow.mockRestore();
  });

  // policy_owner_active_seconds feeds the adjudication-effort analytics, so the
  // pauses matter as much as the accumulation: time spent on a different
  // dispute, or with the window backgrounded, must not be billed.
  it("bills only time spent actively on the dispute being adjudicated", async () => {
    const user = userEvent.setup();
    let updateBody: unknown;
    let currentTime = 1_000;
    const performanceNow = vi.spyOn(performance, "now").mockImplementation(() => currentTime);
    const first: CodeReviewDispute = {
      id: "dispute-timing-1", org_id: "org-1", session_id: "session-1", pull_request_id: "pr-1",
      repository_id: "repo-1", policy_id: "policy-1", reviewed_head_sha: review.head_sha,
      decision: "blocked", direction: "should_have_approved", filed_by_login: "anya",
      author_association: "MEMBER", author_is_pr_author: true, repository_visibility: "private",
      trusted: true, source: "app_ui", body: "First objection to read.",
      contested_reason_codes: [], intake_status: "triaged", routing: "policy_signal_only",
      reassessment_status: "not_requested", adjudication_status: "pending",
      queue_signals: { github_repository: "acme/api", github_pr_number: 428 }, queue_priority: 0,
      reply_status: "not_applicable", version: 3, created_at: "2026-06-26T12:06:00Z", updated_at: "2026-06-26T12:06:00Z",
    };
    const second: CodeReviewDispute = {
      ...first,
      id: "dispute-timing-2",
      body: "Second objection to adjudicate.",
      queue_signals: { github_repository: "acme/api", github_pr_number: 429 },
    };
    mockCodeReviewBaseHandlers();
    server.use(
      http.get("/api/v1/code-review-disputes", () => HttpResponse.json({ data: [first, second], meta: {} } satisfies ListResponse<CodeReviewDispute>)),
      http.patch("/api/v1/code-review-disputes/dispute-timing-2", async ({ request }) => {
        updateBody = await request.json();
        return HttpResponse.json({ data: { ...second, adjudication_status: "upheld", version: 4 } } satisfies SingleResponse<CodeReviewDispute>);
      }),
    );

    renderWithProviders(<CodeReviewsPage />);

    await user.click(await screen.findByRole("tab", { name: "Disputes" }));
    const disputeTable = await screen.findByRole("table", { name: "Code review disputes" });

    // 1s spent on the first dispute, then closed. It must not reach the second.
    await user.click(within(disputeTable).getByRole("button", { name: "Review dispute on acme/api #428" }));
    const firstSheet = await screen.findByRole("dialog", { name: "Review dispute" });
    expect(within(firstSheet).getByText("First objection to read.")).toBeInTheDocument();
    currentTime = 2_000;
    await user.click(within(firstSheet).getByRole("button", { name: "Close" }));
    await waitFor(() => expect(screen.queryByRole("dialog", { name: "Review dispute" })).not.toBeInTheDocument());

    currentTime = 50_000;
    await user.click(within(disputeTable).getByRole("button", { name: "Review dispute on acme/api #429" }));
    const secondSheet = await screen.findByRole("dialog", { name: "Review dispute" });
    expect(within(secondSheet).getByText("Second objection to adjudicate.")).toBeInTheDocument();

    // 1s of real attention, then the window is backgrounded for 39s.
    currentTime = 51_000;
    fireEvent.blur(window);
    currentTime = 90_000;
    fireEvent.focus(window);
    await user.click(within(secondSheet).getByRole("button", { name: "Uphold" }));

    await waitFor(() => expect(updateBody).toEqual({
      expected_version: 3,
      adjudication_status: "upheld",
      policy_owner_active_seconds: 1,
    }));
    performanceNow.mockRestore();
  });

  // The queue polls while the sheet is open. A snapshot taken at click time
  // would show pre-reassessment context and submit a stale CAS version.
  it("refreshes the open dispute sheet when the queue reports a newer version", async () => {
    const user = userEvent.setup();
    const queryClient = createTestQueryClient();
    let updateBody: unknown;
    let reassessmentLanded = false;
    const pending: CodeReviewDispute = {
      id: "dispute-fresh-1", org_id: "org-1", session_id: "session-1", pull_request_id: "pr-1",
      repository_id: "repo-1", policy_id: "policy-1", reviewed_head_sha: review.head_sha,
      decision: "blocked", direction: "should_have_approved", filed_by_login: "anya",
      author_association: "MEMBER", author_is_pr_author: true, repository_visibility: "private",
      trusted: true, source: "app_ui", body: "Reassess this block please.",
      contested_reason_codes: [], intake_status: "triaged", routing: "reassess",
      reassessment_status: "running", adjudication_status: "pending",
      queue_signals: { github_repository: "acme/api", github_pr_number: 428 }, queue_priority: 0,
      reply_status: "not_applicable", version: 3, created_at: "2026-06-26T12:06:00Z", updated_at: "2026-06-26T12:06:00Z",
    };
    const reassessed: CodeReviewDispute = {
      ...pending,
      reassessment_status: "completed",
      reassessment_decision: "approved",
      reassessment_flipped: true,
      version: 4,
    };
    mockCodeReviewBaseHandlers();
    server.use(
      http.get("/api/v1/code-review-disputes", () =>
        HttpResponse.json({ data: [reassessmentLanded ? reassessed : pending], meta: {} } satisfies ListResponse<CodeReviewDispute>)),
      http.patch("/api/v1/code-review-disputes/dispute-fresh-1", async ({ request }) => {
        updateBody = await request.json();
        return HttpResponse.json({ data: { ...reassessed, adjudication_status: "upheld", version: 5 } } satisfies SingleResponse<CodeReviewDispute>);
      }),
    );

    renderWithProviders(<CodeReviewsPage />, { queryClient });

    await user.click(await screen.findByRole("tab", { name: "Disputes" }));
    const disputeTable = await screen.findByRole("table", { name: "Code review disputes" });
    await user.click(within(disputeTable).getByRole("button", { name: "Review dispute on acme/api #428" }));
    const disputeSheet = await screen.findByRole("dialog", { name: "Review dispute" });
    expect(within(disputeSheet).getByText("Running")).toBeInTheDocument();

    reassessmentLanded = true;
    await act(async () => {
      await queryClient.invalidateQueries({ queryKey: ["code-reviews", "dispute-queue"] });
    });

    // The reassessment landed while the sheet was open: the policy owner sees
    // the flip they are adjudicating against, not the stale "Running".
    expect(await within(disputeSheet).findByText("Decision changed")).toBeInTheDocument();
    expect(within(disputeSheet).getByText("Approved")).toBeInTheDocument();
    // The queue row behind the sheet tracks the same refresh.
    expect(within(disputeTable).getByText("Approved")).toBeInTheDocument();

    await user.click(within(disputeSheet).getByRole("button", { name: "Uphold" }));
    await waitFor(() => expect(updateBody).toEqual({
      expected_version: 4,
      adjudication_status: "upheld",
      policy_owner_active_seconds: expect.any(Number),
    }));
  });

  // "Not run" is a deliberate state; "Failed" means the evidence the policy
  // owner expected is missing. They must not read the same.
  it("flags a failed reassessment rather than showing it as quietly as an unrequested one", async () => {
    const user = userEvent.setup();
    const failed: CodeReviewDispute = {
      id: "dispute-failed-1", org_id: "org-1", session_id: "session-1", pull_request_id: "pr-1",
      repository_id: "repo-1", policy_id: "policy-1", reviewed_head_sha: review.head_sha,
      decision: "blocked", direction: "should_not_have_approved", filed_by_login: "anya",
      author_association: "MEMBER", author_is_pr_author: true, repository_visibility: "private",
      trusted: true, source: "app_ui", body: "The reassessment never produced a result.",
      contested_reason_codes: [], intake_status: "triaged", routing: "reassess",
      reassessment_status: "failed", adjudication_status: "pending",
      queue_signals: {}, queue_priority: 0,
      reply_status: "not_applicable", version: 3, created_at: "2026-06-26T12:06:00Z", updated_at: "2026-06-26T12:06:00Z",
    };
    mockCodeReviewBaseHandlers();
    server.use(
      http.get("/api/v1/code-review-disputes", () => HttpResponse.json({ data: [failed], meta: {} } satisfies ListResponse<CodeReviewDispute>)),
    );

    renderWithProviders(<CodeReviewsPage />);

    await user.click(await screen.findByRole("tab", { name: "Disputes" }));
    const disputeTable = await screen.findByRole("table", { name: "Code review disputes" });
    expect(within(disputeTable).getByText("Failed")).toBeInTheDocument();
    expect(within(disputeTable).getByText("Evidence unavailable")).toBeInTheDocument();
    // Falls back to the reviewed commit when the PR context is missing.
    expect(within(disputeTable).getByRole("button", { name: `Review dispute on commit ${review.head_sha.slice(0, 7)}` })).toBeInTheDocument();
    expect(within(disputeTable).getByText("Asks not to approve")).toBeInTheDocument();
  });

  it("closes the dispute sheet when the dispute leaves the pending queue", async () => {
    const user = userEvent.setup();
    const queryClient = createTestQueryClient();
    let adjudicatedElsewhere = false;
    const pending: CodeReviewDispute = {
      id: "dispute-gone-1", org_id: "org-1", session_id: "session-1", pull_request_id: "pr-1",
      repository_id: "repo-1", policy_id: "policy-1", reviewed_head_sha: review.head_sha,
      decision: "blocked", direction: "should_have_approved", filed_by_login: "anya",
      author_association: "MEMBER", author_is_pr_author: true, repository_visibility: "private",
      trusted: true, source: "app_ui", body: "Another admin is on this one.",
      contested_reason_codes: [], intake_status: "triaged", routing: "policy_signal_only",
      reassessment_status: "not_requested", adjudication_status: "pending",
      queue_signals: { github_repository: "acme/api", github_pr_number: 428 }, queue_priority: 0,
      reply_status: "not_applicable", version: 3, created_at: "2026-06-26T12:06:00Z", updated_at: "2026-06-26T12:06:00Z",
    };
    mockCodeReviewBaseHandlers();
    server.use(
      http.get("/api/v1/code-review-disputes", () =>
        HttpResponse.json({ data: adjudicatedElsewhere ? [] : [pending], meta: {} } satisfies ListResponse<CodeReviewDispute>)),
    );

    renderWithProviders(<CodeReviewsPage />, { queryClient });

    await user.click(await screen.findByRole("tab", { name: "Disputes" }));
    const disputeTable = await screen.findByRole("table", { name: "Code review disputes" });
    await user.click(within(disputeTable).getByRole("button", { name: "Review dispute on acme/api #428" }));
    expect(await screen.findByRole("dialog", { name: "Review dispute" })).toBeInTheDocument();

    adjudicatedElsewhere = true;
    await act(async () => {
      await queryClient.invalidateQueries({ queryKey: ["code-reviews", "dispute-queue"] });
    });

    await waitFor(() => expect(screen.queryByRole("dialog", { name: "Review dispute" })).not.toBeInTheDocument());
    expect(await screen.findByText("No disputes need adjudication")).toBeInTheDocument();
  });

  it("pages the dispute queue past the first cursor page", async () => {
    const user = userEvent.setup();
    const baseDispute: CodeReviewDispute = {
      id: "dispute-page-1", org_id: "org-1", session_id: "session-1", pull_request_id: "pr-1",
      repository_id: "repo-1", policy_id: "policy-1", reviewed_head_sha: review.head_sha,
      decision: "blocked", direction: "should_have_approved", filed_by_login: "anya",
      author_association: "MEMBER", author_is_pr_author: true, repository_visibility: "private",
      trusted: true, source: "app_ui", body: "First page objection.",
      contested_reason_codes: [], intake_status: "triaged", routing: "policy_signal_only",
      reassessment_status: "not_requested", adjudication_status: "pending", queue_signals: {}, queue_priority: 0,
      reply_status: "not_applicable", version: 3, created_at: "2026-06-26T12:06:00Z", updated_at: "2026-06-26T12:06:00Z",
    };
    const requestedCursors: (string | null)[] = [];
    mockCodeReviewBaseHandlers();
    server.use(
      http.get("/api/v1/code-review-disputes", ({ request }) => {
        const cursor = new URL(request.url).searchParams.get("cursor");
        requestedCursors.push(cursor);
        if (cursor === "dispute-page-1") {
          return HttpResponse.json({
            data: [{ ...baseDispute, id: "dispute-page-2", body: "Second page objection." }],
            meta: {},
          } satisfies ListResponse<CodeReviewDispute>);
        }
        return HttpResponse.json({
          data: [baseDispute],
          meta: { next_cursor: "dispute-page-1" },
        } satisfies ListResponse<CodeReviewDispute>);
      }),
    );

    renderWithProviders(<CodeReviewsPage />);

    await user.click(await screen.findByRole("tab", { name: "Disputes" }));
    const disputeTable = await screen.findByRole("table", { name: "Code review disputes" });
    expect(within(disputeTable).getByText("First page objection.")).toBeInTheDocument();
    // Without the cursor the badge would cap at the page size and the rest of
    // the queue would be unreachable.
    expect(await within(screen.getByRole("tab", { name: /Disputes/ })).findByText("1+")).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Show more" }));

    expect(await within(disputeTable).findByText("Second page objection.")).toBeInTheDocument();
    expect(within(disputeTable).getByText("First page objection.")).toBeInTheDocument();
    // The first-page refetch on tab entry prevents a materialized ranking
    // update from remaining hidden behind the query cache.
    expect(requestedCursors).toEqual([null, null, "dispute-page-1"]);
    expect(screen.queryByRole("button", { name: "Show more" })).not.toBeInTheDocument();
  });

  it("lets an admin promote an untrusted timeline dispute without adjudicating it", async () => {
    const user = userEvent.setup();
    let updateBody: unknown;
    const dispute: CodeReviewDispute = {
      id: "dispute-untrusted-1", org_id: "org-1", session_id: "session-1", pull_request_id: "pr-1",
      repository_id: "repo-1", policy_id: "policy-1", reviewed_head_sha: review.head_sha,
      decision: "approved", direction: "should_not_have_approved", filed_by_login: "external-contributor",
      author_association: "CONTRIBUTOR", author_is_pr_author: true, repository_visibility: "public",
      trusted: false, source: "github_comment", body: "This approval missed an authorization bypass.",
      contested_reason_codes: [], intake_status: "triaged", routing: "policy_signal_only",
      reassessment_status: "not_requested", queue_signals: {}, queue_priority: 0,
      reply_status: "published", version: 2, created_at: "2026-06-26T12:06:00Z", updated_at: "2026-06-26T12:06:00Z",
    };
    mockCodeReviewBaseHandlers();
    server.use(
      http.get("/api/v1/code-reviews/session-1/disputes", () => HttpResponse.json({ data: [dispute], meta: {} } satisfies ListResponse<CodeReviewDispute>)),
      http.patch("/api/v1/code-review-disputes/dispute-untrusted-1", async ({ request }) => {
        updateBody = await request.json();
        return HttpResponse.json({ data: { ...dispute, trusted: true, trust_override: true, adjudication_status: "pending", version: 3 } } satisfies SingleResponse<CodeReviewDispute>);
      }),
    );

    renderWithProviders(<CodeReviewsPage />);

    expect(await screen.findAllByText("#428 Fix invoice rounding")).toHaveLength(2);
    await user.click(screen.getAllByRole("button", { name: /Evidence/i })[0]);
    const evidenceSheet = await screen.findByRole("dialog", { name: /Evidence for #428/i });
    await user.click(await within(evidenceSheet).findByRole("button", { name: "Promote to policy queue" }));

    await waitFor(() => expect(updateBody).toEqual({ expected_version: 2, trust_override: true }));
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
    expect(mobileActivity.querySelector('[data-activity="indeterminate"]')).toBeInTheDocument();
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
    expect(screen.queryByText("Not applicable")).not.toBeInTheDocument();
    const supersededTable = screen.getByRole("table", { name: "Code reviews" });
    const supersededRow = within(supersededTable).getByRole("row", { name: /#428 Fix invoice rounding/i });
    expect(within(within(supersededRow).getAllByRole("cell")[2]).getByText("—")).toBeInTheDocument();
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
      "Safeguards",
    ];
    for (const label of topLevelGuidance) {
      expect(screen.getByRole("button", { name: `About ${label}` })).toBeInTheDocument();
    }

    const enablement = screen.getByRole("switch", {
      name: "Code reviews enabled",
    });
    const githubHeading = screen.getByText("acme/api");
    const instructionsHeading = screen.getByText("Additional review instructions (optional)");
    const approvalHeading = screen.getByText("Automated approval policy");
    const approvalRegion = screen.getByRole("region", { name: "Automated approval policy" });
    const instructionsRegion = screen.getByRole("region", { name: "Additional review instructions (optional)" });
    const summaryHeading = screen.getByText("Effective policy:");
    const safeguardsHeading = screen.getByRole("heading", { level: 3, name: "Safeguards" });
    const safeguardsCard = safeguardsHeading.closest("section");
    const policyPanel = screen.getByRole("tabpanel");
    // Width and centering belong to the page shell — `ListPage` wraps this page in
    // `PageContainer size="wide"` — so this panel keeps only the canonical tab
    // rhythm and its left edge stays flush with the left-aligned tab bar. It carries
    // no width constraint or centering of its own, so any `mx-auto` or `max-w-*` on
    // this panel — responsive variants and `max-w-none` included — fails here.
    expect(policyPanel).toHaveClass("space-y-6");
    expect(policyPanel.className).not.toMatch(/\bmx-auto\b|\bmax-w-/);
    expect(screen.getByText("Reviewer setup and the rules that gate automatic approval.")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Safeguards" })).not.toBeInTheDocument();
    // Behavior, then both prompts together, then safeguards, then GitHub setup.
    expect(enablement.compareDocumentPosition(summaryHeading) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(summaryHeading.compareDocumentPosition(approvalHeading) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(approvalHeading.compareDocumentPosition(instructionsHeading) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(instructionsHeading.compareDocumentPosition(safeguardsHeading) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(safeguardsHeading.compareDocumentPosition(githubHeading) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    // Prominence comes from order and size only — no badge, tint, ring, or shadow.
    expect(within(approvalRegion).queryByText("Primary policy")).not.toBeInTheDocument();
    expect(approvalRegion.className).toBe(instructionsRegion.className);
    expect(within(approvalRegion).getByRole("textbox")).toHaveClass("min-h-72", "w-full");
    expect(within(approvalRegion).getByRole("textbox")).not.toHaveClass("max-w-3xl");
    await user.click(within(instructionsRegion).getByRole("button", { name: "Add instructions" }));
    expect(within(instructionsRegion).getByRole("textbox")).toHaveClass("min-h-32");
    // Every section title is a real heading, nested under the tab's own h2,
    // and all of them share one size.
    expect(screen.getByRole("heading", { level: 2, name: "Organization review policy" })).toBeInTheDocument();
    for (const name of ["Review behavior", "Automated approval policy", "Additional review instructions (optional)", "Safeguards", "GitHub reviewer connections"]) {
      expect(screen.getByRole("heading", { level: 3, name })).toBeInTheDocument();
    }
    for (const heading of [approvalHeading, instructionsHeading, safeguardsHeading]) {
      expect(heading).toHaveClass("font-display", "text-lg", "font-semibold");
    }
    // Safeguards is a plain bordered card like its siblings — no extra tint, ring,
    // or shadow. Asserted by class rather than string-equality with approvalRegion:
    // that card hardcodes its chrome while this one comes from `SectionGroup`, and
    // the shared component is allowed to evolve (the fix then is to move the prompt
    // composers onto it too, not to re-pin this test).
    expect(safeguardsCard).toHaveClass("rounded-xl", "border", "border-border", "bg-card", "p-4", "sm:p-5");

    const outcomeInfo = screen.getByRole("button", {
      name: "About Review outcome",
    });
    const selectedOutcome = screen.getByText("Comment only").closest("label");
    expect(selectedOutcome).toHaveClass("has-[[data-state=checked]]:border-primary", "has-[[data-state=checked]]:bg-primary/5");
    await user.hover(outcomeInfo);
    expect(await screen.findByRole("tooltip")).toHaveTextContent(/Hard safeguards|deterministic safeguard/i);
    await user.unhover(outcomeInfo);
    const enablementInfo = screen.getByRole("button", { name: "About Code reviews enabled" });
    act(() => enablementInfo.focus());
    expect(await screen.findByRole("tooltip")).toHaveTextContent(/built-in default is on/i);
    act(() => enablementInfo.blur());
    const advancedInfo = screen.getByRole("button", { name: "About Safeguards" });
    await user.hover(advancedInfo);
    expect(await screen.findByRole("tooltip")).toHaveTextContent(/Reviewer models, limits.*always enforced.*require human review/i);
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

    expect(screen.queryByRole("combobox", { name: /Advanced policy preset/i })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Apply preset/i })).not.toBeInTheDocument();
    for (const section of ["Approval criteria", "Paths, authors & checks", "Reviewers & agents"]) {
      await user.click(screen.getByRole("button", { name: new RegExp(section, "i") }));
    }
    for (const label of [
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
      "Stop after stable policy blockers",
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
    expect(screen.getAllByText("Review needed")).toHaveLength(2);
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
    await user.click(screen.getByRole("button", { name: "Add instructions" }));

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
    expect(screen.getByRole("region", { name: "Automated approval policy" })).toBeVisible();
    expect(screen.getByText(/only used when “Approve acceptable PRs” is selected/i)).toBeInTheDocument();
    await user.click(screen.getByRole("radio", { name: /Approve acceptable PRs/i }));
    expect(within(screen.getByRole("region", { name: "Automated approval policy" })).getByRole("textbox")).toHaveValue("Approve only routine changes with proportionate tests.");
  });

  it("waits through a short typing pause before versioning textarea edits and flushes on blur", async () => {
    const user = userEvent.setup();
    const updates: CodeReviewPolicyConfig[] = [];
    mockCodeReviewBaseHandlers(githubTriggerReady, (config) => updates.push(config));
    renderWithProviders(<CodeReviewsPage />);
    await user.click(await screen.findByRole("tab", { name: /Policy/i }));
    await user.click(screen.getByRole("button", { name: "Add instructions" }));

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
    await user.click(screen.getByRole("button", { name: "Add instructions" }));
    const input = within(screen.getByRole("region", { name: "Additional review instructions (optional)" })).getByRole("textbox");
    const overLimit = "界".repeat(8001);
    fireEvent.change(input, { target: { value: overLimit } });
    fireEvent.blur(input);

    expect(input).toHaveValue(overLimit);
    expect(input).toHaveAttribute("aria-invalid", "true");
    expect(screen.getByText("8001 / 8000")).toBeInTheDocument();
    expect(screen.getByText("Prompt is too long.")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Hide editor" })).not.toBeInTheDocument();
    expect(input).toBeVisible();
    await act(async () => { await new Promise((resolve) => setTimeout(resolve, 450)); });
    expect(updates).toHaveLength(0);
  });

  it("saves at-limit text padded with trailing whitespace by trimming before the length check", async () => {
    const user = userEvent.setup();
    const updates: CodeReviewPolicyConfig[] = [];
    mockCodeReviewBaseHandlers(githubTriggerReady, (config) => updates.push(config));
    renderWithProviders(<CodeReviewsPage />);
    await user.click(await screen.findByRole("tab", { name: /Policy/i }));
    await user.click(screen.getByRole("button", { name: "Add instructions" }));
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
    await user.click(screen.getByRole("button", { name: "Add instructions" }));
    const input = within(screen.getByRole("region", { name: "Additional review instructions (optional)" })).getByRole("textbox");
    await user.type(input, "Keep this unsaved local guidance");
    fireEvent.blur(input);

    // Both editors keep visual feedback nearby, while the page-level indicator
    // remains the only live status announced to assistive technology.
    expect(await screen.findAllByText("Couldn't save")).toHaveLength(3);
    expect(screen.getAllByRole("status")).toHaveLength(1);
    expect(input).toHaveValue("Keep this unsaved local guidance");
  });

  it("keeps a failed prompt reset available for retry", async () => {
    const user = userEvent.setup();
    const initialInstructions = "Review tenant boundaries and authorization.";
    let attempts = 0;
    mockCodeReviewBaseHandlers(
      githubTriggerReady,
      undefined,
      { ...policy.config, review_instructions: initialInstructions },
    );
    server.use(
      http.put("/api/v1/code-review-policies", () => {
        attempts += 1;
        return HttpResponse.json({ error: { code: "SAVE_FAILED", message: "failed" } }, { status: 500 });
      }),
    );
    renderWithProviders(<CodeReviewsPage />);
    await user.click(await screen.findByRole("tab", { name: /Policy/i }));

    const instructions = screen.getByRole("region", { name: "Additional review instructions (optional)" });
    await user.click(within(instructions).getByRole("button", { name: "Clear instructions" }));

    await waitFor(() => expect(attempts).toBe(1));
    await screen.findAllByText("Couldn't save");
    const retry = await within(instructions).findByRole("button", { name: "Clear instructions" });
    expect(retry).toBeEnabled();

    await user.click(retry);
    await waitFor(() => expect(attempts).toBe(2));
  });

  it("resets organization prompts to built-in values", async () => {
    const user = userEvent.setup();
    const updates: CodeReviewPolicyConfig[] = [];
    const initialInstructions = "Review tenant boundaries and authorization.";
    mockCodeReviewBaseHandlers(
      githubTriggerReady,
      (config) => updates.push(config),
      { ...policy.config, review_instructions: initialInstructions },
    );
    renderWithProviders(<CodeReviewsPage />);
    await user.click(await screen.findByRole("tab", { name: /Policy/i }));
    await user.click(screen.getByRole("radio", { name: /Approve acceptable PRs/i }));
    const instructions = screen.getByRole("region", { name: "Additional review instructions (optional)" });
    const instructionsInput = within(instructions).getByRole("textbox");
    fireEvent.change(instructionsInput, { target: { value: "界".repeat(8001) } });
    expect(instructionsInput).toHaveAttribute("aria-invalid", "true");
    await user.click(within(instructions).getByRole("button", { name: "Clear instructions" }));
    await waitFor(() => expect(updates.at(-1)?.review_instructions).toBe(""));
    expect(toast.info).toHaveBeenLastCalledWith("Additional instructions cleared", expect.objectContaining({ action: expect.objectContaining({ label: "Undo" }) }));
    const clearToastOptions = toast.info.mock.calls.at(-1)?.[1] as { action: { onClick: () => void } };
    await act(async () => clearToastOptions.action.onClick());
    await waitFor(() => expect(updates.at(-1)?.review_instructions).toBe(initialInstructions));
    expect(updates.at(-1)?.review_instructions).not.toBe("界".repeat(8001));

    const originalApprovalPolicy = policy.config.automated_approval_policy;
    await user.click(within(screen.getByRole("region", { name: "Automated approval policy" })).getByRole("button", { name: "Restore recommended policy" }));
    await waitFor(() => expect(updates.at(-1)?.automated_approval_policy).toContain("Automatically approve routine changes"));
    expect(toast.info).toHaveBeenLastCalledWith("Recommended approval policy restored", expect.objectContaining({ action: expect.objectContaining({ label: "Undo" }) }));
    const restoreToastOptions = toast.info.mock.calls.at(-1)?.[1] as { action: { onClick: () => void } };
    await act(async () => restoreToastOptions.action.onClick());
    await waitFor(() => expect(updates.at(-1)?.automated_approval_policy).toBe(originalApprovalPolicy));
  });

  it("composes full-width prompt editors with attached example and reset actions", async () => {
    const user = userEvent.setup();
    mockCodeReviewBaseHandlers();
    renderWithProviders(<CodeReviewsPage />);
    await user.click(await screen.findByRole("tab", { name: /Policy/i }));
    await user.click(screen.getByRole("radio", { name: /Approve acceptable PRs/i }));
    await user.click(screen.getByRole("button", { name: "Add instructions" }));

    const composers = ["Automated approval policy", "Additional review instructions (optional)"];
    for (const title of composers) {
      const composer = screen.getByRole("region", { name: title });
      const actions = within(composer).getByRole("group", { name: `${title} actions` });
      const examples = within(actions).getByRole("combobox", { name: `${title} prompt example` });
      const reset = within(actions).getByRole("button");
      const editor = within(composer).getByRole("textbox");

      expect(examples).toHaveTextContent("Use an example…");
      expect(examples).toHaveClass("border-0", "bg-transparent");
      // Reset discards the author's text, so it stays the quietest control here.
      expect(reset).toHaveClass("text-xs", "text-muted-foreground", "sm:h-8");
      expect(editor).toHaveClass("w-full");
      expect(actions.parentElement).toHaveClass("w-full", "border-t", "bg-muted/20");
      // The second-best spot on the card belongs to the field, not to reset.
      expect(editor.compareDocumentPosition(actions) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
      // The character count is noise until a prompt approaches the limit.
      expect(within(composer).queryByText(/\/ 8000$/)).not.toBeInTheDocument();
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

  it("saves code review timeout in seconds from the selected unit", async () => {
    const user = userEvent.setup();
    const state = mockCodeReviewBaseHandlers();

    renderWithProviders(<CodeReviewsPage />);

    await user.click(await screen.findByRole("tab", { name: /Policy/i }));
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
      repository_status: "active",
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
      http.get("/api/v1/code-review-github-triggers", () =>
        HttpResponse.json({ data: [githubTriggerReady, {
          ...githubTriggerReady,
          status: "unconfigured",
          repository_id: secondRepo.id,
          repository_full_name: secondRepo.full_name,
          trigger: undefined,
        }], meta: {} } satisfies ListResponse<CodeReviewGitHubTriggerResponse>),
      ),
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

  it("restores the open repository flow after GitHub App installation", async () => {
    mockCodeReviewBaseHandlers();
    server.use(
      http.get("/api/v1/integrations/github/repositories", () =>
        HttpResponse.json({ data: [], meta: {} } satisfies ListResponse<GitHubRepositoryClaimCandidate>),
      ),
    );

    renderWithProviders(<CodeReviewsPage />, {
      searchParams: { tab: "policy", add_repository: "1", github: "connected" },
      nuqsHasMemory: true,
    });

    expect(await screen.findByRole("dialog", { name: "Add GitHub reviewer" })).toBeInTheDocument();
    await waitFor(() => expect(toast.success).toHaveBeenCalledWith(
      "GitHub App connected",
      expect.objectContaining({ description: expect.stringContaining("Choose a repository") }),
    ));
  });

  it("explains the GitHub App prerequisite before listing repositories", async () => {
    const user = userEvent.setup();
    mockCodeReviewBaseHandlers();
    server.use(
      http.get("/api/v1/integrations", () => HttpResponse.json({ data: [], meta: {} })),
    );

    renderWithProviders(<CodeReviewsPage />);
    await user.click(await screen.findByRole("tab", { name: "Policy" }));
    await user.click(await screen.findByRole("button", { name: "Add repository" }));
    const sheet = await screen.findByRole("dialog", { name: "Add GitHub reviewer" });

    expect(within(sheet).getByText("Connect the GitHub App first")).toBeInTheDocument();
    expect(within(sheet).getByRole("button", { name: "Connect GitHub App" })).toBeEnabled();
    expect(within(sheet).queryByRole("textbox", { name: "Search GitHub repositories" })).not.toBeInTheDocument();
  });

  it("retries repository discovery without leaving the reviewer sheet", async () => {
    const user = userEvent.setup();
    let repositoryListCalls = 0;
    mockCodeReviewBaseHandlers();
    server.use(
      http.get("/api/v1/integrations/github/repositories", () => {
        repositoryListCalls += 1;
        if (repositoryListCalls === 1) {
          return HttpResponse.json({ error: { code: "LIST_REPOS_FAILED", message: "GitHub repositories are temporarily unavailable" } }, { status: 502 });
        }
        return HttpResponse.json({ data: [], meta: {} } satisfies ListResponse<GitHubRepositoryClaimCandidate>);
      }),
    );

    renderWithProviders(<CodeReviewsPage />);
    await user.click(await screen.findByRole("tab", { name: "Policy" }));
    await user.click(await screen.findByRole("button", { name: "Add repository" }));
    const sheet = await screen.findByRole("dialog", { name: "Add GitHub reviewer" });
    expect(await within(sheet).findByText("GitHub repositories could not be loaded")).toBeInTheDocument();
    await user.click(within(sheet).getByRole("button", { name: "Retry" }));

    await waitFor(() => expect(repositoryListCalls).toBe(2));
    expect(await within(sheet).findByText("No repositories are available")).toBeInTheDocument();
  });

  it("keeps cached repository rows visible when a background refresh fails", async () => {
    const user = userEvent.setup();
    const newRepo: Repository = {
      ...repo,
      id: "repo-refresh-failure",
      github_id: 149,
      full_name: "acme/offline-refresh",
      clone_url: "https://github.com/acme/offline-refresh.git",
    };
    let candidateCalls = 0;
    mockCodeReviewBaseHandlers();
    server.use(
      http.get("/api/v1/repositories", () =>
        HttpResponse.json({ data: [repo, newRepo], meta: {} } satisfies ListResponse<Repository>),
      ),
      http.get("/api/v1/integrations/github/repositories", () => {
        candidateCalls += 1;
        if (candidateCalls > 1) {
          return HttpResponse.json({ error: {
            code: "LIST_REPOS_FAILED",
            message: "GitHub repository refresh failed",
          } }, { status: 502 });
        }
        return HttpResponse.json({ data: [{
          github_id: newRepo.github_id,
          full_name: newRepo.full_name,
          default_branch: newRepo.default_branch,
          private: newRepo.private,
          clone_url: newRepo.clone_url,
          installation_id: newRepo.installation_id,
          status: "unclaimed",
          can_transfer: false,
        }], meta: {} } satisfies ListResponse<GitHubRepositoryClaimCandidate>);
      }),
      http.post("/api/v1/integrations/github/repositories/claim", () =>
        HttpResponse.json({ data: { claimed: 1 } }),
      ),
      http.post("/api/v1/code-review-github-trigger/setup", () =>
        HttpResponse.json({ error: {
          code: "GITHUB_TRIGGER_PERMISSION_REQUIRED",
          message: "Reviewer setup still needs approval",
        } }, { status: 403 }),
      ),
    );

    renderWithProviders(<CodeReviewsPage />);
    await user.click(await screen.findByRole("tab", { name: "Policy" }));
    await user.click(await screen.findByRole("button", { name: "Add repository" }));
    const sheet = await screen.findByRole("dialog", { name: "Add GitHub reviewer" });
    await user.click(within(sheet).getByRole("button", { name: "Connect & set up" }));

    expect(await within(sheet).findByText("GitHub repositories could not be refreshed")).toBeInTheDocument();
    expect(within(sheet).getByText("acme/offline-refresh")).toBeInTheDocument();
    expect(await within(sheet).findByText(/Repository connected, but reviewer setup failed/)).toHaveTextContent(
      "Reviewer setup still needs approval",
    );
    expect(within(sheet).getByRole("button", { name: "Retry setup" })).toBeEnabled();
  });

  it("adds a connected repository reviewer without leaving the policy", async () => {
    const user = userEvent.setup();
    const secondRepo: Repository = {
      ...repo,
      id: "repo-2",
      github_id: 144,
      full_name: "acme/web",
      clone_url: "https://github.com/acme/web.git",
    };
    let setupRepositoryID: string | undefined;
    mockCodeReviewBaseHandlers();
    server.use(
      http.get("/api/v1/repositories", () =>
        HttpResponse.json({ data: [repo, secondRepo], meta: {} } satisfies ListResponse<Repository>),
      ),
      http.get("/api/v1/integrations/github/repositories", () =>
        HttpResponse.json({ data: [{
          github_id: secondRepo.github_id,
          full_name: secondRepo.full_name,
          default_branch: secondRepo.default_branch,
          private: secondRepo.private,
          clone_url: secondRepo.clone_url,
          installation_id: secondRepo.installation_id,
          status: "owned_by_current_org",
          repository_id: secondRepo.id,
          can_transfer: false,
        }], meta: {} } satisfies ListResponse<GitHubRepositoryClaimCandidate>),
      ),
      http.post("/api/v1/code-review-github-trigger/setup", async ({ request }) => {
        setupRepositoryID = ((await request.json()) as { repository_id: string }).repository_id;
        return HttpResponse.json({ data: {
          ...githubTriggerReady,
          repository_id: secondRepo.id,
          repository_full_name: secondRepo.full_name,
        } } satisfies SingleResponse<CodeReviewGitHubTriggerResponse>);
      }),
    );

    renderWithProviders(<CodeReviewsPage />);
    await user.click(await screen.findByRole("tab", { name: "Policy" }));
    await user.click(await screen.findByRole("button", { name: "Add repository" }));

    const sheet = await screen.findByRole("dialog", { name: "Add GitHub reviewer" });
    expect(within(sheet).getByText("acme/web")).toBeInTheDocument();
    expect(within(sheet).getByText("Connected")).toBeInTheDocument();
    await user.click(within(sheet).getByRole("button", { name: "Set up reviewer" }));

    await waitFor(() => expect(setupRepositoryID).toBe(secondRepo.id));
    expect(await within(sheet).findByText("Ready")).toBeInTheDocument();
    expect(toast.success).toHaveBeenCalledWith(
      "GitHub reviewer added to acme/web",
      expect.objectContaining({ description: expect.stringContaining("@acme/143-code-reviewer") }),
    );
  });

  it("keeps a connected repository recoverable when reviewer setup fails", async () => {
    const user = userEvent.setup();
    const newRepo: Repository = {
      ...repo,
      id: "repo-3",
      github_id: 145,
      full_name: "acme/mobile",
      clone_url: "https://github.com/acme/mobile.git",
    };
    let claimCalls = 0;
    let setupCalls = 0;
    mockCodeReviewBaseHandlers();
    server.use(
      http.get("/api/v1/repositories", () =>
        HttpResponse.json({ data: claimCalls > 0 ? [repo, newRepo] : [repo], meta: {} } satisfies ListResponse<Repository>),
      ),
      http.get("/api/v1/integrations/github/repositories", () =>
        HttpResponse.json({ data: [{
          github_id: newRepo.github_id,
          full_name: newRepo.full_name,
          default_branch: newRepo.default_branch,
          private: newRepo.private,
          clone_url: newRepo.clone_url,
          installation_id: newRepo.installation_id,
          status: claimCalls > 0 ? "owned_by_current_org" : "unclaimed",
          repository_id: claimCalls > 0 ? newRepo.id : undefined,
          can_transfer: false,
        }], meta: {} } satisfies ListResponse<GitHubRepositoryClaimCandidate>),
      ),
      http.get("/api/v1/code-review-github-triggers", () =>
        HttpResponse.json({ data: claimCalls > 0 ? [githubTriggerReady, {
          ...githubTriggerReady,
          status: setupCalls > 1 ? "ready" : "unconfigured",
          repository_id: newRepo.id,
          repository_full_name: newRepo.full_name,
          trigger: setupCalls > 1 ? { ...githubTriggerReady.trigger!, repository_id: newRepo.id } : undefined,
        }] : [githubTriggerReady], meta: {} } satisfies ListResponse<CodeReviewGitHubTriggerResponse>),
      ),
      http.post("/api/v1/integrations/github/repositories/claim", () => {
        claimCalls += 1;
        return HttpResponse.json({ data: { claimed: 1 } });
      }),
      http.post("/api/v1/code-review-github-trigger/setup", () => {
        setupCalls += 1;
        if (setupCalls > 1) {
          return HttpResponse.json({ data: {
            ...githubTriggerReady,
            repository_id: newRepo.id,
            repository_full_name: newRepo.full_name,
          } } satisfies SingleResponse<CodeReviewGitHubTriggerResponse>);
        }
        return HttpResponse.json({ error: {
          code: "GITHUB_TRIGGER_PERMISSION_REQUIRED",
          message: "GitHub App permissions need organization-owner approval",
        } }, { status: 403 });
      }),
    );

    renderWithProviders(<CodeReviewsPage />);
    await user.click(await screen.findByRole("tab", { name: "Policy" }));
    await user.click(await screen.findByRole("button", { name: "Add repository" }));
    const sheet = await screen.findByRole("dialog", { name: "Add GitHub reviewer" });
    await user.click(within(sheet).getByRole("button", { name: "Connect & set up" }));

    expect(await within(sheet).findByText(/Repository connected, but reviewer setup failed/)).toHaveTextContent(
      "GitHub App permissions need organization-owner approval",
    );
    await user.click(within(sheet).getByRole("button", { name: "Done" }));
    const connectedRepository = await screen.findByRole("region", { name: "acme/mobile GitHub reviewer" });
    expect(within(connectedRepository).getByText("Not configured")).toBeInTheDocument();
    await user.click(within(connectedRepository).getByRole("button", { name: "Set up GitHub reviewer" }));

    expect(await within(connectedRepository).findByText("Ready")).toBeInTheDocument();
    expect(claimCalls).toBe(1);
    expect(setupCalls).toBe(2);
  });

  it("reclaims a disconnected repository before setting up its reviewer", async () => {
    const user = userEvent.setup();
    const disconnectedRepo: Repository = {
      ...repo,
      id: "repo-disconnected",
      github_id: 146,
      full_name: "acme/legacy",
      clone_url: "https://github.com/acme/legacy.git",
      status: "disconnected",
    };
    let claimed = false;
    let setupRepositoryID: string | undefined;
    mockCodeReviewBaseHandlers();
    server.use(
      http.get("/api/v1/repositories", () =>
        HttpResponse.json({ data: claimed ? [repo, { ...disconnectedRepo, status: "active" }] : [repo], meta: {} } satisfies ListResponse<Repository>),
      ),
      http.get("/api/v1/integrations/github/repositories", () =>
        HttpResponse.json({ data: [{
          github_id: disconnectedRepo.github_id,
          full_name: disconnectedRepo.full_name,
          default_branch: disconnectedRepo.default_branch,
          private: disconnectedRepo.private,
          clone_url: disconnectedRepo.clone_url,
          installation_id: disconnectedRepo.installation_id,
          status: claimed ? "owned_by_current_org" : "disconnected_in_current_org",
          repository_id: disconnectedRepo.id,
          can_transfer: false,
        }], meta: {} } satisfies ListResponse<GitHubRepositoryClaimCandidate>),
      ),
      http.post("/api/v1/integrations/github/repositories/claim", () => {
        claimed = true;
        return HttpResponse.json({ data: { claimed: 1 } });
      }),
      http.post("/api/v1/code-review-github-trigger/setup", async ({ request }) => {
        setupRepositoryID = ((await request.json()) as { repository_id: string }).repository_id;
        return HttpResponse.json({ data: {
          ...githubTriggerReady,
          repository_id: disconnectedRepo.id,
          repository_full_name: disconnectedRepo.full_name,
        } } satisfies SingleResponse<CodeReviewGitHubTriggerResponse>);
      }),
    );

    renderWithProviders(<CodeReviewsPage />);
    await user.click(await screen.findByRole("tab", { name: "Policy" }));
    await user.click(await screen.findByRole("button", { name: "Add repository" }));
    const sheet = await screen.findByRole("dialog", { name: "Add GitHub reviewer" });
    await user.type(within(sheet).getByRole("textbox", { name: "Search GitHub repositories" }), "legacy");
    expect(within(sheet).getByText("acme/legacy")).toBeInTheDocument();
    await user.click(within(sheet).getByRole("button", { name: "Reconnect & set up" }));

    await waitFor(() => expect(claimed).toBe(true));
    await waitFor(() => expect(setupRepositoryID).toBe(disconnectedRepo.id));
    expect(toast.success).toHaveBeenCalledWith(
      "GitHub reviewer added to acme/legacy",
      expect.objectContaining({ description: expect.stringContaining("@acme/143-code-reviewer") }),
    );
  });

  it("returns to a retryable repository row when reviewer setup fails after transfer", async () => {
    const user = userEvent.setup();
    const transferredRepo: Repository = {
      ...repo,
      id: "repo-transferred",
      github_id: 147,
      full_name: "acme/transferred",
      clone_url: "https://github.com/acme/transferred.git",
    };
    let transferred = false;
    mockCodeReviewBaseHandlers();
    server.use(
      http.get("/api/v1/repositories", () =>
        HttpResponse.json({ data: transferred ? [repo, transferredRepo] : [repo], meta: {} } satisfies ListResponse<Repository>),
      ),
      http.get("/api/v1/integrations/github/repositories", () =>
        HttpResponse.json({ data: [{
          github_id: transferredRepo.github_id,
          full_name: transferredRepo.full_name,
          default_branch: transferredRepo.default_branch,
          private: transferredRepo.private,
          clone_url: transferredRepo.clone_url,
          installation_id: transferredRepo.installation_id,
          status: transferred ? "owned_by_current_org" : "owned_by_other_org",
          repository_id: transferredRepo.id,
          owner_org_name: "Previous workspace",
          can_transfer: true,
        }], meta: {} } satisfies ListResponse<GitHubRepositoryClaimCandidate>),
      ),
      http.post("/api/v1/integrations/github/repositories/claim", () => {
        transferred = true;
        return HttpResponse.json({ data: { claimed: 1 } });
      }),
      http.post("/api/v1/code-review-github-trigger/setup", () =>
        HttpResponse.json({ error: {
          code: "GITHUB_TRIGGER_PERMISSION_REQUIRED",
          message: "Organization-owner approval is required",
        } }, { status: 403 }),
      ),
    );

    renderWithProviders(<CodeReviewsPage />);
    await user.click(await screen.findByRole("tab", { name: "Policy" }));
    await user.click(await screen.findByRole("button", { name: "Add repository" }));
    const sheet = await screen.findByRole("dialog", { name: "Add GitHub reviewer" });
    await user.click(within(sheet).getByRole("button", { name: "Transfer…" }));
    const confirmation = await screen.findByRole("alertdialog", { name: "Transfer acme/transferred?" });
    await user.click(within(confirmation).getByRole("button", { name: "Transfer and set up" }));

    await waitFor(() => expect(screen.queryByRole("alertdialog", { name: "Transfer acme/transferred?" })).not.toBeInTheDocument());
    expect(await within(sheet).findByText(/Repository connected, but reviewer setup failed/)).toHaveTextContent(
      "Organization-owner approval is required",
    );
    expect(within(sheet).getByRole("button", { name: "Retry setup" })).toBeEnabled();
  });

  it("explains the GitHub account prerequisite inside the add repository flow", async () => {
    const user = userEvent.setup();
    mockCodeReviewBaseHandlers({ ...githubTriggerReady, status: "auth_required", trigger: undefined });
    server.use(
      http.get("/api/v1/users/me/github-status", () => HttpResponse.json({
        connected: false,
        has_repo_scope: false,
        pr_authorship_mode: "user_preferred",
        pr_draft_default: false,
        account_requirement: "recommended",
        needs_reconnect: false,
      })),
      http.get("/api/v1/integrations/github/repositories", () =>
        HttpResponse.json({ data: [{
          github_id: 144,
          full_name: "acme/web",
          default_branch: "main",
          private: true,
          clone_url: "https://github.com/acme/web.git",
          installation_id: 123,
          status: "unclaimed",
          can_transfer: false,
        }], meta: {} } satisfies ListResponse<GitHubRepositoryClaimCandidate>),
      ),
    );

    renderWithProviders(<CodeReviewsPage />);
    await user.click(await screen.findByRole("tab", { name: "Policy" }));
    await user.click(await screen.findByRole("button", { name: "Add repository" }));
    const sheet = await screen.findByRole("dialog", { name: "Add GitHub reviewer" });

    expect(within(sheet).getByText("Connect your GitHub account")).toBeInTheDocument();
    expect(within(sheet).getByRole("button", { name: "Connect account" })).toBeEnabled();
    expect(within(sheet).getByRole("button", { name: "Connect & set up" })).toBeDisabled();
  });

  it("offers account reconnection when authorization expires during setup", async () => {
    const user = userEvent.setup();
    const connectedRepo: Repository = {
      ...repo,
      id: "repo-expired-auth",
      github_id: 150,
      full_name: "acme/expired-auth",
      clone_url: "https://github.com/acme/expired-auth.git",
    };
    let accountStatusCalls = 0;
    mockCodeReviewBaseHandlers();
    server.use(
      http.get("/api/v1/users/me/github-status", () => {
        accountStatusCalls += 1;
        return HttpResponse.json({
          connected: accountStatusCalls === 1,
          has_repo_scope: accountStatusCalls === 1,
          pr_authorship_mode: "user_preferred",
          pr_draft_default: false,
          account_requirement: "recommended",
          needs_reconnect: accountStatusCalls > 1,
        });
      }),
      http.get("/api/v1/integrations/github/repositories", () =>
        HttpResponse.json({ data: [{
          github_id: connectedRepo.github_id,
          full_name: connectedRepo.full_name,
          default_branch: connectedRepo.default_branch,
          private: connectedRepo.private,
          clone_url: connectedRepo.clone_url,
          installation_id: connectedRepo.installation_id,
          status: "owned_by_current_org",
          repository_id: connectedRepo.id,
          can_transfer: false,
        }], meta: {} } satisfies ListResponse<GitHubRepositoryClaimCandidate>),
      ),
      http.post("/api/v1/code-review-github-trigger/setup", () =>
        HttpResponse.json({ error: {
          code: "GITHUB_USER_AUTH_REQUIRED",
          message: "Connect your GitHub account before creating the reviewer team",
        } }, { status: 409 }),
      ),
    );

    renderWithProviders(<CodeReviewsPage />);
    await user.click(await screen.findByRole("tab", { name: "Policy" }));
    await user.click(await screen.findByRole("button", { name: "Add repository" }));
    const sheet = await screen.findByRole("dialog", { name: "Add GitHub reviewer" });
    await user.click(within(sheet).getByRole("button", { name: "Set up reviewer" }));

    expect(await within(sheet).findByText("Reconnect your GitHub account")).toBeInTheDocument();
    expect(within(sheet).getByRole("button", { name: "Reconnect account" })).toBeEnabled();
    expect(within(sheet).getByRole("button", { name: "Retry setup" })).toBeDisabled();
    await waitFor(() => expect(accountStatusCalls).toBeGreaterThanOrEqual(2));
  });

  it("explains why GitHub reviewer setup is disabled", async () => {
    const user = userEvent.setup();
    mockCodeReviewBaseHandlers({
      status: "auth_required",
      repository_id: "repo-1",
      repository_full_name: "acme/api",
      repository_status: "active",
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
      repository_status: "active",
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
    expect(viewOnlyNotice).toHaveClass("text-muted-foreground");
    expect(viewOnlyNotice.className).not.toMatch(/\bbg-/);
    expect(screen.getByRole("switch", { name: "Code reviews enabled" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Add instructions" })).toBeDisabled();
    expect(screen.queryByRole("textbox", { name: "Additional review instructions (optional)" })).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Add repository" })).toBeDisabled();

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
    await user.click(screen.getByRole("button", { name: "Add instructions" }));

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
    expect(subsection).toHaveAttribute("aria-expanded", "true");
    // The save error sits in the Safeguards card above the divided subsection list,
    // not inside it, so no divider hairline lands against the notice's card border.
    const notice = screen.getByRole("alert");
    expect(notice).toHaveTextContent("Could not save this policy setting");
    expect(notice.parentElement).toBe(screen.getByRole("heading", { level: 3, name: "Safeguards" }).closest("section"));
    expect(notice.compareDocumentPosition(subsection) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
  });
});
