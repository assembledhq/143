import { it, expect } from "vitest";
import { http, HttpResponse } from "msw";
import { renderWithProviders, screen, userEvent } from "@/test/test-utils";
import { server } from "@/test/mocks/server";
import { AssessmentStatus } from "./assessment-status";
import { QueryClient } from "@tanstack/react-query";

it("shows original findings with current evidence reassessments and text sources", async () => {
  const id = "61c58de4-e24d-47d1-8d71-29e08ef9b963";
  const sourceID = "d39b19d4-27bb-45d0-9df0-0341137ece3f";
  const summary = { id, status: "completed" as const, review_scope: "evidence_only" as const, route_reason: "visual_changed", source_assessment_id: sourceID, decision: "blocked" as const, head_sha: "123456789abcdef" };
  server.use(
    http.get(`*/api/v1/code-review-assessments/${id}`, () => HttpResponse.json({ data: { ...summary, session_id: "session-1" } })),
    http.get(`*/api/v1/code-review-assessments/${id}/evidence`, () => HttpResponse.json({ data: {
      assessment: summary, source_assessment_id: sourceID, agent_results: [{ id: "result-1" }],
      findings: [{ id: "finding-1", severity: "high", summary: "Missing test proof", body: "No test result was supplied.", path: "src/view.tsx", start_line: 12 }, { id: "finding-2", severity: "medium", summary: "Risk remains", body: "The issue is still present." }],
      source_findings: [{ id: "finding-1", severity: "high", summary: "Missing test proof", body: "No test result was supplied.", path: "src/view.tsx", start_line: 12 }, { id: "finding-2", severity: "medium", summary: "Risk remains", body: "The issue is still present." }],
      finding_reassessments: [{ finding_id: "finding-1", status: "resolved", reason: "Current test output passes.", evidence_citations: [{ evidence_id: "text-1", quote: "passed" }] }, { finding_id: "finding-2", status: "retained", reason: "Current evidence does not address this risk.", evidence_citations: [] }],
      requirement_reassessments: [{ key: "testing_evidence", status: "satisfied", reason: "Test result supplied.", evidence_citations: [{ evidence_id: "text-1", quote: "passed" }] }],
      text_evidence: { items: [{ evidence_id: "text-1", surface: "description", section: "Testing", provider_object_id: "pr-1", source_url: "https://github.com/acme/repo/pull/1", author_login: "alice", content: "Tests passed", content_digest: "hash" }, { evidence_id: "text-unsafe", surface: "comment", section: "Validation", provider_object_id: "comment-2", source_url: "javascript:alert(1)", author_login: "bob", content: "Untrusted source", content_digest: "other-hash" }], complete: true, source_provenance_complete: true, parse_ambiguous: false, unclassified_digest: "" },
      prompt_records: [], visual_evidence: { evidence: [{ evidence_id: "image-1" }] }, cited_visual_evidence_ids: ["image-1"],
    } })),
  );
  const user = userEvent.setup();
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  renderWithProviders(<AssessmentStatus assessment={summary} />, { queryClient });
  await user.click(screen.getByRole("button", { name: "Evidence re-check completed" }));
  expect(await screen.findByText("Cited images: image-1")).toBeInTheDocument();
  expect(queryClient.getQueriesData({ queryKey: ["code-reviews", "assessment", id] }).map(([key]) => key)).toEqual([
    ["code-reviews", "assessment", id],
    ["code-reviews", "assessment", id, "evidence"],
  ]);
  expect(screen.getByText(sourceID)).toBeInTheDocument();
  expect(screen.getByText("blocked")).toBeInTheDocument();
  expect(screen.getByText("Missing test proof")).toBeInTheDocument();
  expect(screen.getByText("No test result was supplied.")).toBeInTheDocument();
  expect(screen.getByText("Current test output passes.")).toBeInTheDocument();
  expect(screen.getByText("Risk remains")).toBeInTheDocument();
  expect(screen.getByText("Current evidence does not address this risk.")).toBeInTheDocument();
  expect(screen.getByText("Test result supplied.")).toBeInTheDocument();
  expect(screen.getByText("Tests passed")).toBeInTheDocument();
  expect(screen.getByRole("link", { name: "Open text source" })).toHaveAttribute("href", "https://github.com/acme/repo/pull/1");
  expect(screen.getAllByRole("link", { name: "Open text source" })).toHaveLength(1);
  expect(screen.getByRole("link", { name: "Open review session" })).toHaveAttribute("href", "/sessions/session-1");
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
  expect(await screen.findByText("Original concern")).toBeInTheDocument();
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
