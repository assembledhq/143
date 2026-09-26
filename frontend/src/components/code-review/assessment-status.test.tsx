import { it, expect, vi } from "vitest";
import { http, HttpResponse } from "msw";
import { renderWithProviders, screen, userEvent } from "@/test/test-utils";
import { server } from "@/test/mocks/server";
import { AssessmentFromLink, AssessmentStatus, ReviewAssessmentDialog, assessmentForReview } from "./assessment-status";
import { QueryClient } from "@tanstack/react-query";
import type { CodeReviewAssessmentSummary, CodeReviewDecision, CodeReviewListItem } from "@/lib/types";

it("shows original findings with current evidence reassessments and text sources", async () => {
  const id = "61c58de4-e24d-47d1-8d71-29e08ef9b963";
  const sourceID = "d39b19d4-27bb-45d0-9df0-0341137ece3f";
  const summary = { id, status: "completed" as const, review_scope: "evidence_only" as const, route_reason: "visual_changed", source_assessment_id: sourceID, decision: "blocked" as const, risk_reason_details: [{ code: "blocking_findings" }], head_sha: "123456789abcdef" };
  server.use(
    http.get(`*/api/v1/code-review-assessments/${id}`, () => HttpResponse.json({ data: { ...summary, session_id: "session-1" } })),
    http.get("*/api/v1/code-reviews/session-1", () => new HttpResponse(null, { status: 404 })),
    http.get(`*/api/v1/code-review-assessments/${id}/evidence`, () => HttpResponse.json({ data: {
      assessment: summary, source_assessment_id: sourceID, agent_results: [{ id: "result-1", status: "completed", role: "reviewer", agent_provider: "codex" }],
      findings: [{ id: "finding-1", severity: "high", summary: "Missing test proof", body: "No test result was supplied.", path: "src/view.tsx", start_line: 12 }, { id: "finding-2", severity: "medium", summary: "Risk remains", body: "The issue is still present." }],
      source_findings: [{ id: "finding-1", severity: "high", summary: "Missing test proof", body: "No test result was supplied.", path: "src/view.tsx", start_line: 12 }, { id: "finding-2", severity: "medium", summary: "Risk remains", body: "The issue is still present." }],
      finding_reassessments: [{ finding_id: "finding-1", status: "resolved", reason: "Current test output passes.", evidence_citations: [{ evidence_id: "text-1", quote: "passed" }] }, { finding_id: "finding-2", status: "retained", reason: "Current evidence does not address this risk.", evidence_citations: [] }],
      requirement_reassessments: [{ key: "testing_evidence", status: "satisfied", reason: "Test result supplied.", evidence_citations: [{ evidence_id: "text-1", quote: "passed" }] }],
      text_evidence: { items: [{ evidence_id: "text-1", surface: "description", section: "Testing", provider_object_id: "pr-1", source_url: "https://github.com/acme/repo/pull/1", author_login: "alice", content: "Tests passed", content_digest: "hash" }, { evidence_id: "text-unsafe", surface: "comment", section: "Validation", provider_object_id: "comment-2", source_url: "javascript:alert(1)", author_login: "bob", content: "Untrusted source", content_digest: "other-hash" }], complete: true, source_provenance_complete: true, parse_ambiguous: false, unclassified_digest: "" },
      prompt_records: [], visual_evidence: { evidence: [{ evidence_id: "image-1", status: "available", stored_url: "https://example.com/evidence.png" }] }, cited_visual_evidence_ids: ["image-1"],
    } })),
  );
  const user = userEvent.setup();
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  renderWithProviders(<AssessmentStatus assessment={summary} />, { queryClient });
  await user.click(screen.getByRole("button", { name: "Evidence re-check completed" }));
  expect(await screen.findByRole("button", { name: "Reviewer findings 2" })).toHaveAttribute("aria-expanded", "false");
  expect(screen.getByRole("heading", { name: "Blocked" })).toBeInTheDocument();
  expect(screen.getByText("Reviewers found a blocking issue")).toBeInTheDocument();
  expect(screen.queryByText("Missing test proof")).not.toBeInTheDocument();
  expect(screen.queryByText("Tests passed")).not.toBeInTheDocument();
  expect(screen.queryByText(sourceID)).not.toBeInTheDocument();
  expect(screen.queryByRole("img")).not.toBeInTheDocument();
  for (const name of ["Reviewer findings 2", "Description requirements 1", "Captured text sources 2", "Visual evidence 1", "Assessment details"]) {
    const trigger = screen.getByRole("button", { name });
    expect(trigger).toHaveAttribute("aria-expanded", "false");
    await user.click(trigger);
    expect(trigger).toHaveAttribute("aria-expanded", "true");
  }
  expect(screen.getByText("Cited images: image-1")).toBeInTheDocument();
  expect(queryClient.getQueriesData({ queryKey: ["code-reviews", "assessment", id] }).map(([key]) => key)).toEqual([
    ["code-reviews", "assessment", id],
    ["code-reviews", "assessment", id, "evidence"],
  ]);
  expect(screen.getByText(sourceID)).toBeInTheDocument();
  expect(screen.getByText("Missing test proof")).toBeInTheDocument();
  expect(screen.getByText("No test result was supplied.")).toBeInTheDocument();
  expect(screen.getByText("Current test output passes.")).toBeInTheDocument();
  expect(screen.getByText("Risk remains")).toBeInTheDocument();
  expect(screen.getByText("Current evidence does not address this risk.")).toBeInTheDocument();
  expect(screen.getByText("Test result supplied.")).toBeInTheDocument();
  expect(screen.getByText("Tests passed")).toBeInTheDocument();
  expect(screen.getByRole("link", { name: "Open text source" })).toHaveAttribute("href", "https://github.com/acme/repo/pull/1");
  expect(screen.getAllByRole("link", { name: "Open text source" })).toHaveLength(1);
  expect(screen.getByRole("link", { name: "Open session" })).toHaveAttribute("href", "/sessions/session-1");
  const findingsTrigger = screen.getByRole("button", { name: "Reviewer findings 2" });
  findingsTrigger.focus();
  await user.keyboard("{Enter}");
  expect(findingsTrigger).toHaveAttribute("aria-expanded", "false");
  expect(screen.queryByText("Missing test proof")).not.toBeInTheDocument();
  expect(screen.getByText("Tests passed")).toBeInTheDocument();
});

it("keeps a historical finding visible when no reassessment was recorded", async () => {
  const id = "9730433f-d527-468e-ab5a-fea838a819ca";
  const summary = { id, status: "completed" as const, review_scope: "evidence_only" as const, route_reason: "visual_changed", head_sha: "123456789abcdef" };
  server.use(
    http.get(`*/api/v1/code-review-assessments/${id}`, () => HttpResponse.json({ data: summary })),
    http.get(`*/api/v1/code-review-assessments/${id}/evidence`, () => HttpResponse.json({ data: { assessment: summary, agent_results: [], findings: [{ id: "finding-old", severity: "high", summary: "Original concern", body: "Preserved source detail" }], prompt_records: [] } })),
  );
  const user = userEvent.setup();
  renderWithProviders(<AssessmentStatus assessment={summary} />);
  await user.click(screen.getByRole("button", { name: "Evidence re-check completed" }));
  await user.click(await screen.findByRole("button", { name: "Reviewer findings 1" }));
  expect(screen.getByText("Original concern")).toBeInTheDocument();
  expect(screen.getByText("No current reassessment recorded for this finding.")).toBeInTheDocument();
});

it("shows a failed attempt separately from completed coverage", async () => {
  const id = "a6ce860c-e53b-4a6c-9bd4-42d268a2f0cc";
  const summary = { id, status: "failed" as const, review_scope: "evidence_only" as const, route_reason: "visual_changed", head_sha: "123456789abcdef" };
  server.use(
    http.get(`*/api/v1/code-review-assessments/${id}`, () => HttpResponse.json({ data: { ...summary, failure_detail: "Evidence validation failed" } })),
    http.get(`*/api/v1/code-review-assessments/${id}/evidence`, () => HttpResponse.json({ data: { assessment: summary, agent_results: [], findings: [], prompt_records: [], cited_visual_evidence_ids: [] } })),
  );
  const user = userEvent.setup();
  renderWithProviders(<AssessmentStatus assessment={summary} />);
  await user.click(screen.getByRole("button", { name: "Latest attempt failed" }));
  expect(await screen.findByText("This attempt did not replace the completed assessment. Open that assessment separately for its recorded result.")).toBeInTheDocument();
  expect(screen.getByRole("alert")).toHaveTextContent("Evidence validation failed");
});

const assessmentID = "f2b93745-c23c-4be6-8e86-fd2ad00e6e39";

function mockAssessment(summary: CodeReviewAssessmentSummary) {
  server.use(
    http.get(`*/api/v1/code-review-assessments/${summary.id}`, () => HttpResponse.json({ data: summary })),
    http.get(`*/api/v1/code-review-assessments/${summary.id}/evidence`, () => HttpResponse.json({ data: {
      assessment: summary, agent_results: [], findings: [], prompt_records: [],
    } })),
  );
}

it.each([
  { decision: "approved", label: "Approved" },
  { decision: "needs_human_review", label: "Needs human review" },
  { decision: "blocked", label: "Blocked" },
  { decision: "comment_only", label: "Comment only" },
  { decision: undefined, label: "No verdict recorded" },
] satisfies { decision: CodeReviewDecision | undefined; label: string }[])("shows the $label verdict without inventing a reason", async ({ decision, label }) => {
  const summary: CodeReviewAssessmentSummary = {
    id: assessmentID, status: "completed", review_scope: "full", route_reason: "initial", head_sha: "123456789abcdef", decision,
  };
  mockAssessment(summary);
  const user = userEvent.setup();
  renderWithProviders(<AssessmentStatus assessment={summary} />);
  await user.click(screen.getByRole("button", { name: "Full review completed" }));
  expect(await screen.findByRole("heading", { name: label })).toBeInTheDocument();
  expect(screen.getByText("No decision reasoning was recorded for this assessment.")).toBeInTheDocument();
  expect(screen.queryByRole("button", { name: /Reviewer findings/ })).not.toBeInTheDocument();
});

it("keeps the verdict concise while preserving all recorded reasons", async () => {
  const summary: CodeReviewAssessmentSummary = {
    id: assessmentID, status: "completed", review_scope: "full", route_reason: "initial", head_sha: "123456789abcdef", decision: "needs_human_review",
    risk_reason_details: [
      { code: "blocking_findings" },
      { code: "lines_limit_exceeded", actual: 1200, limit: 500 },
      { code: "required_check_failing", subject: "Frontend tests" },
      { code: "description_failed" },
      { code: "blocking_findings" },
    ],
  };
  mockAssessment(summary);
  const user = userEvent.setup();
  renderWithProviders(<AssessmentFromLink />, { searchParams: { assessment: assessmentID } });
  expect(await screen.findByRole("heading", { name: "Needs human review" })).toBeInTheDocument();
  expect(screen.getByText("Reviewers found a blocking issue")).toBeInTheDocument();
  expect(screen.getByText("Line-count limit exceeded (1,200 of 500)")).toBeInTheDocument();
  expect(screen.getByText("A named required check was not passing: Frontend tests")).toBeInTheDocument();
  expect(screen.getByText("1 more reason in Decision details below.")).toBeInTheDocument();
  expect(screen.queryByText("PR description requirements were not met")).not.toBeInTheDocument();
  await user.click(screen.getByRole("button", { name: "Decision details 4" }));
  expect(screen.getByText("PR description requirements were not met")).toBeInTheDocument();
});

it.each(["reserved", "running", "publishing", "failed", "superseded", "cancelled"] as const)("does not present a %s attempt as an approval", async (status) => {
  const summary: CodeReviewAssessmentSummary = {
    id: assessmentID, status, review_scope: "full", route_reason: "initial", head_sha: "123456789abcdef", decision: "approved",
  };
  mockAssessment(summary);
  renderWithProviders(<AssessmentFromLink />, { searchParams: { assessment: assessmentID } });
  expect(await screen.findByRole("button", { name: "Assessment details" })).toBeInTheDocument();
  expect(screen.queryByRole("heading", { name: "Approved" })).not.toBeInTheDocument();
});

it("keeps the verdict and reasoning available if evidence cannot load", async () => {
  const summary: CodeReviewAssessmentSummary = {
    id: assessmentID, status: "completed", review_scope: "full", route_reason: "initial", head_sha: "123456789abcdef", decision: "blocked",
    risk_reason_details: [{ code: "blocking_findings" }],
  };
  mockAssessment(summary);
  server.use(http.get(`*/api/v1/code-review-assessments/${assessmentID}/evidence`, () => new HttpResponse(null, { status: 500 })));
  renderWithProviders(<AssessmentFromLink />, { searchParams: { assessment: assessmentID } });
  expect(await screen.findByRole("alert")).toHaveTextContent("Evidence could not be loaded");
  expect(screen.getByRole("heading", { name: "Blocked" })).toBeInTheDocument();
  expect(screen.getByText("Reviewers found a blocking issue")).toBeInTheDocument();
});

it("reports an invalid assessment link", () => {
  renderWithProviders(<AssessmentFromLink />, { searchParams: { assessment: "invalid" } });
  expect(screen.getByRole("alert")).toHaveTextContent("This assessment link is invalid.");
  expect(screen.queryByText("Loading assessment…")).not.toBeInTheDocument();
});

function reviewFixture(): CodeReviewListItem {
  server.use(http.get("*/api/v1/code-review-policies", () => HttpResponse.json({ data: { capabilities: { conditional_recheck: false }, config: {} } })));
  return {
    id: "review-1", org_id: "org-1", session_id: "session-1", repository_id: "repo-1", pull_request_id: "pr-1", policy_id: "policy-1",
    base_sha: "abcdef0", head_sha: "123456789abcdef", from_fork: false, trigger_source: "github", status: "completed",
    retryable_failure: false, retry_eligible: false, stale: false, review_output_key: "output-1", decision: "needs_human_review",
    github_repo: "acme/repo", github_pr_number: 42, github_pr_url: "https://github.com/acme/repo/pull/42",
    pull_request_title: "Improve date handling", pull_request_author: "alice", created_at: "2026-09-25T10:00:00Z",
  };
}

it.each([
  { name: "another session", session: "session-other", head: "123456789abcdef" },
  { name: "another commit", session: "session-1", head: "different-head" },
])("does not use the assessment from $name", ({ session, head }) => {
  const review = reviewFixture();
  const assessment: CodeReviewAssessmentSummary = { id: assessmentID, status: "completed", review_scope: "full", route_reason: "initial", session_id: session, head_sha: head };
  expect(assessmentForReview({ ...review, current_assessment: assessment })).toBeNull();
});

it("keeps a failed re-check separate from legacy completed coverage", async () => {
  const review = reviewFixture();
  review.latest_failed_assessment = { id: assessmentID, status: "failed", review_scope: "evidence_only", route_reason: "visual_changed", session_id: review.session_id, head_sha: review.head_sha };
  const user = userEvent.setup();
  renderWithProviders(<ReviewAssessmentDialog review={review} isLoading={false} error={null} onRetryEvidence={() => {}} open onOpenChange={() => {}} />);
  expect(screen.getByRole("heading", { name: "Needs human review" })).toBeInTheDocument();
  await user.click(screen.getByRole("button", { name: "Assessment details" }));
  expect(screen.getByRole("link", { name: "View latest failed attempt" })).toHaveAttribute("href", `/code-reviews?assessment=${assessmentID}`);
});

it("uses the selected assessment snapshot and retains raw reviewer and prompt details", async () => {
  const review = reviewFixture();
  const assessment: CodeReviewAssessmentSummary = { id: assessmentID, status: "completed", review_scope: "full", route_reason: "initial", session_id: review.session_id, head_sha: review.head_sha, decision: "blocked", risk_reason_details: [{ code: "blocking_findings" }] };
  review.current_assessment = assessment;
  server.use(
    http.get(`*/api/v1/code-review-assessments/${assessmentID}`, () => HttpResponse.json({ data: { ...assessment, pull_request_id: review.pull_request_id } })),
    http.get(`*/api/v1/code-review-assessments/${assessmentID}/evidence`, () => HttpResponse.json({ data: {
      assessment,
      findings: [{ id: "snapshot-finding", summary: "Current assessment finding", body: "Current assessment detail", severity: "high" }],
      agent_results: [{ id: "reviewer-1", agent_provider: "codex", role: "reviewer", status: "completed", raw_output: "Raw reviewer output", structured_result: { verdict: "blocked" } }],
      prompt_records: [{ id: "prompt-1", role: "reviewer", record_key: "prompt-key", content: "Original reviewer instructions" }],
    } })),
  );
  const user = userEvent.setup();
  renderWithProviders(<ReviewAssessmentDialog review={review} evidence={{ agent_results: [], findings: [] }} isLoading={false} error={null} onRetryEvidence={() => {}} open onOpenChange={() => {}} />);
  expect(await screen.findByRole("heading", { name: "Blocked" })).toBeInTheDocument();
  expect(screen.queryByText("Raw reviewer output")).not.toBeInTheDocument();
  await user.click(await screen.findByRole("button", { name: "Reviewer findings 1" }));
  expect(screen.getByText("Current assessment finding")).toBeInTheDocument();
  await user.click(screen.getByRole("button", { name: "Reviewer outputs 1" }));
  expect(screen.getByText("Raw reviewer output")).toBeInTheDocument();
  expect(screen.getByText(/"verdict": "blocked"/)).toBeInTheDocument();
  await user.click(screen.getByRole("button", { name: "Prompt records 1" }));
  expect(screen.getByText("Original reviewer instructions")).toBeInTheDocument();
});

it("uses the shared bottom-sheet treatment for review details on mobile", () => {
  const originalMatchMedia = window.matchMedia;
  Object.defineProperty(window, "matchMedia", {
    configurable: true,
    value: vi.fn().mockImplementation((query: string) => ({
      matches: query === "(max-width: 639px)",
      media: query,
      onchange: null,
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
      addListener: vi.fn(),
      removeListener: vi.fn(),
      dispatchEvent: vi.fn(),
    })),
  });

  const review = reviewFixture();
  renderWithProviders(<ReviewAssessmentDialog review={review} isLoading={false} error={null} onRetryEvidence={() => {}} open onOpenChange={() => {}} />);

  const dialog = screen.getByRole("dialog", { name: "Review for #42" });
  expect(dialog).toHaveAttribute("data-slot", "sheet-content");
  expect(dialog).toHaveClass("bottom-0", "max-h-[100svh]");
  expect(screen.getByRole("link", { name: "Open session" }).closest('[data-slot="responsive-modal-footer"]')).toHaveClass("pb-[max(1rem,env(safe-area-inset-bottom))]");

  Object.defineProperty(window, "matchMedia", { configurable: true, value: originalMatchMedia });
});
