import { it, expect } from "vitest";
import { http, HttpResponse } from "msw";
import { renderWithProviders, screen, userEvent } from "@/test/test-utils";
import { server } from "@/test/mocks/server";
import { AssessmentStatus } from "./assessment-status";
import { QueryClient } from "@tanstack/react-query";

it("shows the recorded decision and source of an evidence re-check", async () => {
  const id = "61c58de4-e24d-47d1-8d71-29e08ef9b963";
  const sourceID = "d39b19d4-27bb-45d0-9df0-0341137ece3f";
  const summary = { id, status: "completed" as const, review_scope: "evidence_only" as const, route_reason: "visual_changed", source_assessment_id: sourceID, decision: "blocked" as const, head_sha: "123456789abcdef" };
  server.use(
    http.get(`*/api/v1/code-review-assessments/${id}`, () => HttpResponse.json({ data: { ...summary, session_id: "session-1" } })),
    http.get(`*/api/v1/code-review-assessments/${id}/evidence`, () => HttpResponse.json({ data: { assessment: summary, source_assessment_id: sourceID, agent_results: [{ id: "result-1" }], findings: [], prompt_records: [], visual_evidence: { evidence: [{ evidence_id: "image-1" }] }, cited_visual_evidence_ids: ["image-1"] } })),
  );
  const user = userEvent.setup();
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  renderWithProviders(<AssessmentStatus assessment={summary} />, { queryClient });
  await user.click(screen.getByRole("button", { name: "Visual evidence re-checked" }));
  expect(await screen.findByText("Cited images: image-1")).toBeInTheDocument();
  expect(queryClient.getQueriesData({ queryKey: ["code-reviews", "assessment", id] }).map(([key]) => key)).toEqual([
    ["code-reviews", "assessment", id],
    ["code-reviews", "assessment", id, "evidence"],
  ]);
  expect(screen.getByText(sourceID)).toBeInTheDocument();
  expect(screen.getByText("blocked")).toBeInTheDocument();
  expect(screen.getByRole("link", { name: "Open review session" })).toHaveAttribute("href", "/sessions/session-1");
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
