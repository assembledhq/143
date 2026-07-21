import { beforeEach, describe, it, expect, vi } from "vitest";
import { act } from "react";
import { http, HttpResponse } from "msw";
import { fireEvent, renderWithProviders, screen, userEvent, waitFor, within } from "@/test/test-utils";
import { server } from "@/test/mocks/server";

const toast = vi.hoisted(() => ({
  success: vi.fn(),
  info: vi.fn(),
  error: vi.fn(),
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
    useResourceSSE: () => ({ healthy: true }),
  };
});
import type {
  CodingCredentialSummary,
  CodeReviewEvidence,
  CodeReviewGitHubTriggerResponse,
  CodeReviewListItem,
  CodeReviewPolicyConfig,
  CodeReviewPolicyRecord,
  CodeReviewResolvedPolicy,
  CodeReviewTemplateOption,
  CodeReviewPromptExamplesResponse,
  ListResponse,
  OpenCodeModelInfo,
  Repository,
  SingleResponse,
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
            categories: ["backend"],
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
      exclude_categories: ["auth", "billing"],
      required_checks: ["lint", "test"],
      eligible_authors: ["anya"],
      require_up_to_date: false,
      allow_forks: false,
      allow_policy_changes: false,
    },
    agent_roster: {
      reviewers: ["codex", "claude_code"],
      orchestrator: "claude_code",
      reviewer_models: ["gpt-5.4", "claude-sonnet-4-6"],
      orchestrator_model: "claude-sonnet-4-6",
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
      severity: "low",
      confidence: "high",
      path: "src/app.ts",
      start_line: 12,
      summary: "Clarify branch name",
      body: "The branch name could be more descriptive.",
      selected_for_inline: true,
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

function mockCodeReviewBaseHandlers(trigger: CodeReviewGitHubTriggerResponse = githubTriggerReady, onPolicyUpdate?: (config: CodeReviewPolicyConfig, source?: string) => void) {
  // Autosave issues whole-config PUTs and refetches on settle, so the GET must
  // reflect the last saved config for optimistic values to stick across the
  // invalidation round-trip.
  let currentConfig: CodeReviewPolicyConfig = policy.config;
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
        meta: {},
      } satisfies ListResponse<CodeReviewListItem>),
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
      currentConfig = body.config;
      onPolicyUpdate?.(body.config, body.source);
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
  });

  it("renders review sessions and policy configuration", async () => {
    const user = userEvent.setup();
    mockCodeReviewBaseHandlers();

    renderWithProviders(<CodeReviewsPage />);

    expect(await screen.findByRole("heading", { name: "Code reviews" })).toBeInTheDocument();
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
    }
    expect(screen.queryByRole("link", { name: "Open final review" })).not.toBeInTheDocument();
    const filterToggle = screen.getByRole("button", {
      name: /Filter reviews/i,
    });
    expect(filterToggle).toHaveAttribute("aria-expanded", "false");
    await user.click(filterToggle);
    expect(filterToggle).toHaveAttribute("aria-expanded", "true");
    expect(screen.getByRole("textbox", { name: "Search code reviews" })).toBeInTheDocument();
    expect(screen.getAllByRole("link", { name: "Open pull request" })).toHaveLength(2);
    const reviewTable = screen.getByRole("table");
    const reviewRow = within(reviewTable).getByRole("row", {
      name: /#428 Fix invoice rounding/i,
    });
    const reviewCells = within(reviewRow).getAllByRole("cell");
    expect(within(reviewCells[2]).getByText("Acceptable").closest('[data-slot="status-label"]')).not.toBeNull();
    expect(within(reviewCells[3]).getByText("Approved").closest('[data-slot="status-label"]')).not.toBeNull();
    expect(within(reviewCells[3]).getByRole("button", { name: "Evidence" })).toBeInTheDocument();
    expect(within(reviewCells[4]).getByText("Completed").closest('[data-slot="status-label"]')).not.toBeNull();
    expect(reviewCells[2].querySelector('[aria-hidden="true"]')).toBeNull();
    expect(within(reviewCells[6]).queryByRole("button", { name: "Evidence" })).not.toBeInTheDocument();
    await user.click(screen.getAllByRole("button", { name: /Evidence/i })[0]);
    const evidenceSheet = await screen.findByRole("dialog", {
      name: /Evidence for #428/i,
    });
    expect(evidenceSheet).toBeInTheDocument();
    expect(within(evidenceSheet).getByText("No blocking issues found.")).toBeInTheDocument();
    expect(within(evidenceSheet).getByText("Clarify branch name")).toBeInTheDocument();
    expect(within(evidenceSheet).getByText("Review this PR.")).toBeInTheDocument();
    expect(within(evidenceSheet).getByText("Completed")).toBeInTheDocument();
    await user.click(within(evidenceSheet).getByRole("button", { name: "Close" }));

    await user.click(await screen.findByRole("tab", { name: /Policy/i }));
    await user.click(screen.getByRole("combobox", { name: "GitHub reviewer repository" }));
    await user.click(await screen.findByRole("option", { name: "acme/api" }));

    // The organization policy, current behavior, outcome, and selected repository trigger are visible without expanding anything.
    expect(await screen.findByText("One policy for every repository")).toBeInTheDocument();
    expect(screen.getByText(/Repository-specific overrides are not available/i)).toBeInTheDocument();
    expect(screen.getByText("Current behavior")).toBeInTheDocument();
    expect(screen.getByText("Comments only")).toBeInTheDocument();
    expect(screen.getByText("GitHub reviewer ready")).toBeInTheDocument();
    expect(screen.getByText("2 reviewers")).toBeInTheDocument();
    expect(screen.getByText("quorum 2")).toBeInTheDocument();
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
    expect(screen.getByText("billing")).toBeInTheDocument();
    expect(screen.getByText("lint")).toBeInTheDocument();
    expect(screen.getByText("anya")).toBeInTheDocument();
    expect(screen.getAllByText("1 item").length).toBeGreaterThan(0);

    await user.click(screen.getByRole("button", { name: /Quality gates/i }));
    expect(await screen.findByText("Enforce sensitive paths")).toBeInTheDocument();
    expect(screen.getByText("Allow policy changes")).toBeInTheDocument();
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
      "GitHub reviewer repository",
      "GitHub reviewer",
      "Advanced controls",
    ];
    for (const label of topLevelGuidance) {
      expect(screen.getByRole("button", { name: `About ${label}` })).toBeInTheDocument();
    }

    const policyNotice = screen.getByText("One policy for every repository");
    const enablement = screen.getByRole("switch", {
      name: "Code reviews enabled",
    });
    const githubHeading = screen.getByText("GitHub reviewer");
    const instructionsHeading = screen.getByText("Additional review instructions (optional)");
    const summaryHeading = screen.getByText("Current behavior");
    const advancedTrigger = screen.getByRole("button", {
      name: "Advanced controls",
    });
    expect(policyNotice.compareDocumentPosition(enablement) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(enablement.compareDocumentPosition(instructionsHeading) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(instructionsHeading.compareDocumentPosition(githubHeading) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(githubHeading.compareDocumentPosition(summaryHeading) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(summaryHeading.compareDocumentPosition(advancedTrigger) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();

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

    await user.click(screen.getByRole("combobox", { name: "GitHub reviewer repository" }));
    await user.click(await screen.findByRole("option", { name: "acme/api" }));
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
      "Excluded categories",
      "Required checks",
      "Eligible authors",
      "Reviewer models",
      "Add reviewer model",
      "Reviewer 1 model",
      "Reviewer 2 model",
      "Orchestrator model",
    ]) {
      expect(screen.getByRole("button", { name: `About ${label}` })).toBeInTheDocument();
    }

    await user.click(screen.getByRole("button", { name: /Quality gates/i }));
    for (const label of [
      "Require passing checks",
      "Enforce sensitive paths",
      "Require up-to-date branch",
      "Allow policy changes",
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

    await user.click(screen.getByRole("combobox", { name: "Requirement applicability" }));
    await user.click(await screen.findByRole("option", { name: "Categories" }));
    expect(await screen.findByRole("button", { name: "About Categories" })).toBeInTheDocument();

    await user.click(screen.getByRole("combobox", { name: "Requirement applicability" }));
    await user.click(await screen.findByRole("option", { name: "Tests changed" }));
    expect(await screen.findByRole("button", { name: "About Require changed test files" })).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Close" }));
    expect(await screen.findByText("When test files changed")).toBeInTheDocument();
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
    await waitFor(() => expect(updates.at(-1)?.automated_approval_policy).toContain("Automatically approve routine, well-tested changes"));
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
    const safeguards = screen.getByText("Hard safeguards").parentElement;
    const instructions = screen.getByRole("region", { name: "Additional review instructions (optional)" });
    expect(approval.compareDocumentPosition(safeguards as Node) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect((safeguards as Node).compareDocumentPosition(instructions) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
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

    // Add-button labels are singularized correctly, including "categories" -> "category".
    expect(screen.getByRole("button", { name: "Add excluded category" })).toBeInTheDocument();
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
    await user.click(screen.getByRole("combobox", { name: "GitHub reviewer repository" }));
    await user.click(await screen.findByRole("option", { name: "acme/api" }));

    expect(await screen.findByText("Needs GitHub account")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /Connect GitHub/i })).toBeInTheDocument();
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
    await user.click(screen.getByRole("combobox", { name: "GitHub reviewer repository" }));
    await user.click(await screen.findByRole("option", { name: "acme/api" }));

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
    await user.click(screen.getByRole("combobox", { name: "GitHub reviewer repository" }));
    await user.click(await screen.findByRole("option", { name: "acme/api" }));
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
    expect(await screen.findByText(/view-only access/i)).toBeInTheDocument();
    expect(screen.getByRole("switch", { name: "Code reviews enabled" })).toBeDisabled();
    expect(screen.getByRole("textbox", { name: "Additional review instructions (optional)" })).toBeDisabled();

    const repositorySelect = screen.getByRole("combobox", { name: "GitHub reviewer repository" });
    expect(repositorySelect).toBeEnabled();
    await user.click(repositorySelect);
    await user.click(await screen.findByRole("option", { name: "acme/api" }));
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
