import { it, expect } from "vitest";
import { http, HttpResponse } from "msw";
import { renderWithProviders, screen, userEvent } from "@/test/test-utils";
import { server } from "@/test/mocks/server";
import { RecheckFromComment } from "./recheck-from-comment";

const assessmentID = "4b3404e2-b58e-4ec7-b0b1-e0a684ba3308";

it("opens a linked assessment without sending a review request", async () => {
  let posts = 0;
  server.use(
    http.get(`*/api/v1/code-review-assessments/${assessmentID}`, () => HttpResponse.json({ data: { id: assessmentID, pull_request_id: "pr-1", status: "completed", review_scope: "full", route_reason: "initial_full", head_sha: "123456789abcdef", github_repo: "acme/app", github_pr_number: 17, pull_request_title: "Add tests" } })),
    http.post("*/api/v1/pull-requests/:id/code-review/requests", () => { posts++; return HttpResponse.json({ data: { disposition: "queued" } }); }),
  );
  renderWithProviders(<RecheckFromComment canManage={false} />, { searchParams: { recheck: assessmentID } });
  expect(await screen.findByText("acme/app #17")).toBeInTheDocument();
  expect(screen.getByText("An organization member or admin must request the review.")).toBeInTheDocument();
  expect(posts).toBe(0);
});

it("submits only after an authorized user clicks Re-check evidence", async () => {
  let posts = 0;
  server.use(
    http.get(`*/api/v1/code-review-assessments/${assessmentID}`, () => HttpResponse.json({ data: { id: assessmentID, pull_request_id: "pr-1", status: "completed", review_scope: "full", route_reason: "initial_full", head_sha: "123456789abcdef", pull_request_title: "Add tests" } })),
    http.get("*/api/v1/code-review-policies", () => HttpResponse.json({ data: { capabilities: { conditional_recheck: true }, config: { continuation_policy: { enabled: true } } } })),
    http.post("*/api/v1/pull-requests/:id/code-review/requests", () => { posts++; return HttpResponse.json({ data: { disposition: "queued" } }, { status: 202 }); }),
  );
  const user = userEvent.setup();
  renderWithProviders(<RecheckFromComment canManage />, { searchParams: { recheck: assessmentID } });
  await screen.findByText("Add tests");
  expect(posts).toBe(0);
  await user.click(await screen.findByRole("button", { name: "Re-check evidence" }));
  expect(await screen.findByRole("status")).toHaveTextContent("Queued");
  expect(posts).toBe(1);
});
