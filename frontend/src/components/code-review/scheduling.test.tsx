import { describe, it, expect } from "vitest";
import { http, HttpResponse } from "msw";
import { createTestQueryClient, renderWithProviders, screen, userEvent, waitFor } from "@/test/test-utils";
import { server } from "@/test/mocks/server";
import { ReviewNowButton, ScheduledReviews } from "./scheduling";

function enableScheduling(enabled = true) {
  server.use(http.get("*/api/v1/code-review-policies", () => HttpResponse.json({ data: { capabilities: { scheduling: enabled } } })));
}

describe("Review now", () => {
  it("retries an uncertain response with the same request ID, then uses a new ID for new intent", async () => {
    enableScheduling();
    const requests: { request_id: string; mode: string }[] = [];
    server.use(http.post("*/api/v1/pull-requests/:id/code-review/requests", async ({ request }) => {
      requests.push(await request.json() as { request_id: string; mode: string });
      if (requests.length === 1) return HttpResponse.json({ error: { code: "UNAVAILABLE", message: "Please retry" } }, { status: 503 });
      return HttpResponse.json({ data: { disposition: "queued" } }, { status: 202 });
    }));
    const user = userEvent.setup();
    renderWithProviders(<ReviewNowButton prID="pr-1" />);
    await user.click(await screen.findByRole("button", { name: "Review now" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("Please retry");
    await user.click(screen.getByRole("button", { name: "Review now" }));
    expect(await screen.findByRole("status")).toHaveTextContent("Review requested");
    await user.click(screen.getByRole("button", { name: "Review now" }));
    await waitFor(() => expect(requests).toHaveLength(3));
    expect(requests[0]).toEqual({ request_id: expect.any(String), mode: "review_now" });
    expect(requests[1]).toEqual(requests[0]);
    expect(requests[2].request_id).not.toEqual(requests[0].request_id);
  });
  it("hides the control while the scheduler capability is disabled", async () => {
    enableScheduling(false);
    const queryClient = createTestQueryClient();
    renderWithProviders(<ReviewNowButton prID="pr-1" />, { queryClient });
    await waitFor(() => expect(queryClient.isFetching()).toBe(0));
    expect(screen.queryByRole("button", { name: "Review now" })).not.toBeInTheDocument();
  });
  it("explains and disables an ineligible request", async () => {
    enableScheduling();
    renderWithProviders(<ReviewNowButton prID="pr-1" disabledReason="Mark this PR ready before requesting review." />);
    expect(await screen.findByRole("button", { name: "Review now" })).toBeDisabled();
  });
});

describe("pending reviews", () => {
  it.each([true, false])("shows pending work without inventing a session; canManage=%s", async (canManage) => {
    enableScheduling();
    server.use(http.get("*/api/v1/code-review-targets", () => HttpResponse.json({ data: [{
      title: "Reduce duplicate reviews", github_repo: "acme/api", github_pr_number: 17, github_pr_url: "https://github.com/acme/api/pull/17",
      schedule: { id: "state-1", pull_request_id: "pr-1", automatic_paused: true, state: "paused", wait_reason: "manual_pause", first_pending_at: "2026-09-10T10:00:00Z", eligible_at: null },
    }], meta: {} })));
    renderWithProviders(<ScheduledReviews enabled canManage={canManage} />);
    expect(await screen.findByText("Automatic reviews paused")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: /Reduce duplicate reviews/ })).toHaveAttribute("href", "https://github.com/acme/api/pull/17");
    if (canManage) expect(await screen.findByRole("button", { name: "Review now" })).toBeEnabled();
    else expect(screen.queryByRole("button", { name: "Review now" })).not.toBeInTheDocument();
    expect(screen.queryByText(/session/i)).not.toBeInTheDocument();
  });
});
