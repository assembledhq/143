import { describe, it, expect } from "vitest";
import { http, HttpResponse } from "msw";
import { renderWithProviders, screen, userEvent, waitFor } from "@/test/test-utils";
import { server } from "@/test/mocks/server";
import CodeReviewsPage from "./page";
import type {
  CodeReviewEvidence,
  CodeReviewGitHubTriggerResponse,
  CodeReviewListItem,
  CodeReviewResolvedPolicy,
  CodeReviewTemplateOption,
  ListResponse,
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
    description_policy: {
      requirements: [
        { key: "description", title: "Understandable description", prompt: "Explain intent.", required: true, applies_when: { kind: "all" } },
        {
          key: "testing",
          title: "Testing evidence",
          prompt: "Show validation.",
          required: true,
          applicability: "nontrivial",
          applies_when: { kind: "nontrivial", min_files_changed: 2, min_lines_changed: 31, categories: ["backend"] },
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
    inheritance: {
      inherit_org_defaults: false,
    },
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

function mockCodeReviewBaseHandlers(trigger: CodeReviewGitHubTriggerResponse = githubTriggerReady) {
  server.use(
    http.get("/api/v1/repositories", () => HttpResponse.json({ data: [repo], meta: {} } satisfies ListResponse<Repository>)),
    http.get("/api/v1/code-reviews", () => HttpResponse.json({ data: [review], meta: {} } satisfies ListResponse<CodeReviewListItem>)),
    http.get("/api/v1/code-reviews/session-1/evidence", () => HttpResponse.json({ data: evidence } satisfies SingleResponse<CodeReviewEvidence>)),
    http.get("/api/v1/code-reviews/templates", () => HttpResponse.json({ data: [template], meta: {} } satisfies ListResponse<CodeReviewTemplateOption>)),
    http.get("/api/v1/code-review-policies", () => HttpResponse.json({ data: policy } satisfies SingleResponse<CodeReviewResolvedPolicy>)),
    http.get("/api/v1/code-review-github-trigger", () => HttpResponse.json({ data: trigger } satisfies SingleResponse<CodeReviewGitHubTriggerResponse>)),
  );
}

describe("CodeReviewsPage", () => {
  it("renders review sessions and policy configuration", async () => {
    const user = userEvent.setup();
    mockCodeReviewBaseHandlers();

    renderWithProviders(<CodeReviewsPage />);

    expect(await screen.findByRole("heading", { name: "Code reviews" })).toBeInTheDocument();
    expect(await screen.findByText("#428 Fix invoice rounding")).toBeInTheDocument();
    expect(screen.getByText("Acceptable")).toBeInTheDocument();
    expect(screen.getByText("Approved")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: /Evidence/i }));
    expect(await screen.findByText(/Evidence for #428/i)).toBeInTheDocument();
    expect(screen.getByText("No blocking issues found.")).toBeInTheDocument();
    expect(screen.getByText("Clarify branch name")).toBeInTheDocument();
    expect(screen.getByText("Review this PR.")).toBeInTheDocument();

    await user.click(screen.getByRole("combobox", { name: /Repository/i }));
    await user.click(await screen.findByRole("option", { name: "acme/api" }));
    await user.click(await screen.findByRole("tab", { name: /Configurations/i }));

    await waitFor(() => {
      expect(screen.getByDisplayValue("Understandable description")).toBeInTheDocument();
    });
    expect(await screen.findByText("@acme/143-code-reviewer")).toBeInTheDocument();
    expect(screen.getByText("Ready")).toBeInTheDocument();
    expect(screen.getByDisplayValue("*auth*")).toBeInTheDocument();
    expect(screen.getByDisplayValue("internal/**")).toBeInTheDocument();
    expect(screen.getByDisplayValue("migrations/**")).toBeInTheDocument();
    expect(screen.getByRole("combobox", { name: /testing applicability/i })).toBeInTheDocument();
    expect(screen.getByDisplayValue(/auth\s+billing/)).toBeInTheDocument();
    expect(screen.getByText("Enforce sensitive paths")).toBeInTheDocument();
    expect(screen.getByText("Allow policy changes")).toBeInTheDocument();
    expect(screen.getByText("Block reviewer disagreement")).toBeInTheDocument();
    expect(screen.getByRole("combobox", { name: /Review depth/i })).toBeInTheDocument();

    await user.click(screen.getByRole("combobox", { name: /Starter template/i }));
    await user.click(await screen.findByRole("option", { name: "Small backend change" }));
    await user.click(screen.getByRole("button", { name: /Apply template/i }));
    expect(screen.getAllByDisplayValue("4").length).toBeGreaterThan(0);

    await user.click(screen.getByRole("button", { name: /Add requirement/i }));
    expect(screen.getByDisplayValue("Custom requirement")).toBeInTheDocument();
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

    await user.click(await screen.findByRole("combobox", { name: /Repository/i }));
    await user.click(await screen.findByRole("option", { name: "acme/api" }));
    await user.click(await screen.findByRole("tab", { name: /Configurations/i }));

    expect(await screen.findByText("Needs GitHub account")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /Connect GitHub/i })).toBeInTheDocument();
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
          { error: { code: "GITHUB_TRIGGER_PERMISSION_REQUIRED", message: "GitHub rejected setup" } },
          { status: 403 },
        );
      }),
    );

    renderWithProviders(<CodeReviewsPage />);

    await user.click(await screen.findByRole("combobox", { name: /Repository/i }));
    await user.click(await screen.findByRole("option", { name: "acme/api" }));
    await user.click(await screen.findByRole("tab", { name: /Configurations/i }));
    await user.click(await screen.findByRole("button", { name: /Create \/ repair team/i }));

    await waitFor(() => {
      expect(setupCalls).toBe(1);
    });
    expect(await screen.findByText("GitHub rejected setup")).toBeInTheDocument();
  });
});
