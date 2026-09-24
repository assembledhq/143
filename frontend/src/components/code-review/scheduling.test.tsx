import { describe, it, expect } from "vitest";
import { http, HttpResponse } from "msw";
import { createTestQueryClient, renderWithProviders, screen, userEvent, waitFor, within } from "@/test/test-utils";
import { server } from "@/test/mocks/server";
import { ReviewNowButton, ScheduledReviews } from "./scheduling";

function enableScheduling(enabled = true) {
  server.use(http.get("*/api/v1/code-review-policies", () => HttpResponse.json({ data: { capabilities: { scheduling: enabled } } })));
}

describe("Request Full Re-Review Now", () => {
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
    await user.click(await screen.findByRole("button", { name: "Request Full Re-Review Now" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("Please retry");
    await user.click(screen.getByRole("button", { name: "Request Full Re-Review Now" }));
    expect(await screen.findByRole("status")).toHaveTextContent("Review requested");
    await user.click(screen.getByRole("button", { name: "Request Full Re-Review Now" }));
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
    expect(screen.queryByRole("button", { name: "Request Full Re-Review Now" })).not.toBeInTheDocument();
  });
  it("explains and disables an ineligible request", async () => {
    enableScheduling();
    renderWithProviders(<ReviewNowButton prID="pr-1" disabledReason="This PR is closed." />);
    expect(await screen.findByRole("button", { name: "Request Full Re-Review Now" })).toBeDisabled();
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
    const queue = within(await screen.findByRole("table", { name: "Review queue" }));
    expect(queue.getByText("Automatic reviews paused")).toBeInTheDocument();
    expect(queue.getByRole("link", { name: /Reduce duplicate reviews/ })).toHaveAttribute("href", "https://github.com/acme/api/pull/17");
    if (canManage) expect(await queue.findByRole("button", { name: "Request Full Re-Review Now" })).toBeEnabled();
    else expect(screen.queryByRole("button", { name: "Request Full Re-Review Now" })).not.toBeInTheDocument();
    expect(screen.queryByText(/session/i)).not.toBeInTheDocument();
  });
});

function queuedTarget(index: number) {
  return {
    title: `Queued PR ${index}`, github_repo: "acme/api", github_pr_number: index, github_pr_url: `https://github.com/acme/api/pull/${index}`,
    schedule: { id: `state-${index}`, pull_request_id: `pr-${index}`, automatic_paused: false, state: "waiting", wait_reason: "quiet_period", first_pending_at: "2026-09-10T10:00:00Z", eligible_at: null },
  };
}

describe("queue pagination", () => {
  it("replaces each page instead of accumulating a large queue", async () => {
    const requests: URLSearchParams[] = [];
    server.use(http.get("*/api/v1/code-review-targets", ({ request }) => {
      const params = new URL(request.url).searchParams;
      requests.push(params);
      return HttpResponse.json(params.get("cursor")
        ? { data: [queuedTarget(26)], meta: {} }
        : { data: Array.from({ length: 25 }, (_, index) => queuedTarget(index + 1)), meta: { next_cursor: "state-25" } });
    }));
    const user = userEvent.setup();
    renderWithProviders(<ScheduledReviews enabled canManage={false} />);
    const firstPage = within(await screen.findByRole("table", { name: "Review queue" }));
    expect(firstPage.getAllByRole("row")).toHaveLength(26);
    expect(screen.getByRole("button", { name: "Previous" })).toBeDisabled();
    await user.click(screen.getByRole("button", { name: "Next" }));
    const secondPage = within(await screen.findByRole("table", { name: "Review queue" }));
    expect(await secondPage.findByRole("link", { name: "#26 Queued PR 26" })).toBeInTheDocument();
    expect(secondPage.getAllByRole("row")).toHaveLength(2);
    expect(screen.queryAllByRole("link", { name: "#1 Queued PR 1" })).toHaveLength(0);
    expect(screen.getByRole("button", { name: "Next" })).toBeDisabled();
    expect(screen.getByText("Page 2 · 25 per page")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Previous" }));
    expect(await within(await screen.findByRole("table", { name: "Review queue" })).findByRole("link", { name: "#1 Queued PR 1" })).toBeInTheDocument();
    expect(requests.every((params) => params.get("limit") === "25")).toBe(true);
    expect(requests.some((params) => params.get("cursor") === "state-25")).toBe(true);
  });

  it("keeps a way back when the next page empties as reviews start", async () => {
    server.use(http.get("*/api/v1/code-review-targets", ({ request }) => HttpResponse.json(new URL(request.url).searchParams.has("cursor")
      ? { data: [], meta: {} } : { data: [queuedTarget(1)], meta: { next_cursor: "state-1" } })));
    const user = userEvent.setup();
    renderWithProviders(<ScheduledReviews enabled canManage={false} />);
    await screen.findByRole("table", { name: "Review queue" });
    await user.click(screen.getByRole("button", { name: "Next" }));
    expect(await screen.findByText("No pending reviews on this page")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Previous" })).toBeEnabled();
    await user.click(screen.getByRole("button", { name: "Previous" }));
    expect(await screen.findByRole("table", { name: "Review queue" })).toBeInTheDocument();
  });

  it("shows an empty queue and recovers from a failed load", async () => {
    let unavailable = true;
    server.use(http.get("*/api/v1/code-review-targets", () => unavailable
      ? HttpResponse.json({ error: { message: "Unavailable" } }, { status: 503 })
      : HttpResponse.json({ data: [], meta: {} })));
    const user = userEvent.setup();
    renderWithProviders(<ScheduledReviews enabled canManage={false} />);
    expect(await screen.findByRole("alert")).toHaveTextContent("Review queue could not be loaded");
    unavailable = false;
    await user.click(screen.getByRole("button", { name: "Retry" }));
    expect(await screen.findByText("No reviews waiting")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Next" })).toBeDisabled();
  });
});


describe("draft eligibility", () => {
  it.each([
    { name: "open draft", state: "waiting", wait_reason: "quiet_period", enabled: true },
    { name: "legacy draft hold", state: "paused", wait_reason: "draft", enabled: true },
    { name: "closed draft", state: "closed", wait_reason: "", enabled: false },
    { name: "disabled policy", state: "paused", wait_reason: "policy_disabled", enabled: false },
  ])("preserves Request Full Re-Review Now eligibility for $name", async ({ state, wait_reason, enabled }) => {
    enableScheduling();
    const target = queuedTarget(1);
    server.use(http.get("*/api/v1/code-review-targets", () => HttpResponse.json({ data: [{ ...target,
      schedule: { ...target.schedule, is_draft: true, state, wait_reason },
    }], meta: {} })));
    const requests: unknown[] = [];
    server.use(http.post("*/api/v1/pull-requests/:id/code-review/requests", async ({ request }) => {
      requests.push(await request.json());
      return HttpResponse.json({ data: { disposition: "queued" } }, { status: 202 });
    }));
    const user = userEvent.setup();
    renderWithProviders(<ScheduledReviews enabled canManage />);
    const queue = within(await screen.findByRole("table", { name: "Review queue" }));
    const button = await queue.findByRole("button", { name: "Request Full Re-Review Now" });
    if (enabled) {
      expect(button).toBeEnabled();
      await user.click(button);
      await waitFor(() => expect(requests).toEqual([{ request_id: expect.any(String), mode: "review_now" }]));
    } else expect(button).toBeDisabled();
    if (wait_reason === "draft") {
      expect(queue.getByText("Review queued")).toBeInTheDocument();
      expect(queue.queryByText(/PR to be ready/)).not.toBeInTheDocument();
    }
  });
});
