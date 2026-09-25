import { describe, expect, it } from "vitest";
import { http, HttpResponse } from "msw";
import { renderWithProviders, screen, userEvent, waitFor, within } from "@/test/test-utils";
import { server } from "@/test/mocks/server";
import { ReviewNowFromComment } from "./review-now-from-comment";
import { RecheckFromComment } from "./recheck-from-comment";

const sessionID = "90d8a47d-d87e-4780-90af-040f5144685a";
const assessmentID = "4b3404e2-b58e-4ec7-b0b1-e0a684ba3308";
const target = { pull_request_id: "pr-1", github_repo: "acme/app", github_pr_number: 17, pull_request_title: "Add tests", status: "completed" };
function mockContext({ capability = true, continuation = true, status = "completed" } = {}) {
  server.use(
    http.get(`*/api/v1/code-reviews/${sessionID}`, () => HttpResponse.json({ data: target })),
    http.get(`*/api/v1/code-review-assessments/${assessmentID}`, () => HttpResponse.json({ data: { ...target, status } })),
    http.get("*/api/v1/code-review-policies", () => HttpResponse.json({ data: { capabilities: { scheduling: capability, conditional_recheck: capability }, config: { continuation_policy: { enabled: continuation } } } })),
  );
}
const modes = [
  { mode: "review_now", title: "Request full review", queued: "Full review requested", id: sessionID, Component: ReviewNowFromComment },
  { mode: "recheck", title: "Re-check evidence", queued: "Evidence re-check requested", id: assessmentID, Component: RecheckFromComment },
] as const;

describe.each(modes)("$mode confirmation", ({ mode, title, queued, id, Component }) => {
  function open(canManage = true, linkID: string = id) {
    return renderWithProviders(<Component canManage={canManage} />, { searchParams: { [mode]: linkID }, nuqsHasMemory: true });
  }
  it.each([
    { disposition: "queued", heading: "", action: "View review queue", href: "/code-reviews?tab=queue" },
    { disposition: "joined", heading: "Review already in progress", action: "Open session", href: "/sessions/session-new", session_id: "session-new" },
    { disposition: "reused", heading: "Existing review reused", action: "View assessment", href: "/code-reviews?assessment=assessment-new", assessment_id: "assessment-new" },
    { disposition: "cancelled", heading: "Review not scheduled", action: "View review queue", href: "/code-reviews?tab=queue" },
  ])("shows a distinct $disposition receipt and removes the submit action", async ({ disposition, heading, action, href, ...ids }) => {
    mockContext();
    const requests: unknown[] = [];
    server.use(http.post("*/api/v1/pull-requests/pr-1/code-review/requests", async ({ request }) => {
      requests.push(await request.json());
      return HttpResponse.json({ data: { disposition, ...ids } }, { status: 202 });
    }));
    const user = userEvent.setup();
    open();
    const submit = await screen.findByRole("button", { name: title });
    await waitFor(() => expect(submit).toBeEnabled());
    expect(requests).toEqual([]);
    await user.click(submit);
    const dialog = await screen.findByRole("dialog", { name: heading || queued });
    expect(requests).toEqual([{ request_id: expect.any(String), mode }]);
    expect(within(dialog).queryByRole("button", { name: title })).not.toBeInTheDocument();
    expect(within(dialog).getByRole("link", { name: action })).toHaveAttribute("href", href);
    expect(within(dialog).getByText("acme/app #17")).toBeInTheDocument();
    await user.click(within(dialog).getByRole("button", { name: "Done" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(requests).toHaveLength(1);
  });
  it.each(["UNKNOWN", "CODE_REVIEW_RECHECK_UNAVAILABLE"])("preserves pending and retry behavior for %s", async (code) => {
    mockContext();
    const requests: { request_id: string; mode: string }[] = [];
    let resolve!: () => void;
    const pending = new Promise<void>((done) => { resolve = done; });
    server.use(http.post("*/api/v1/pull-requests/pr-1/code-review/requests", async ({ request }) => {
      requests.push(await request.json() as typeof requests[number]);
      if (requests.length === 1) {
        await pending;
        return HttpResponse.json({ error: { code, message: "The response was interrupted." } }, { status: 503 });
      }
      return HttpResponse.json({ data: { disposition: "queued" } }, { status: 202 });
    }));
    const user = userEvent.setup();
    open();
    const submit = await screen.findByRole("button", { name: title });
    await waitFor(() => expect(submit).toBeEnabled());
    await user.click(submit);
    expect(await screen.findByRole("button", { name: "Requesting…" })).toBeDisabled();
    resolve();
    expect(await screen.findByRole("alert")).toHaveTextContent("The response was interrupted.");
    await user.click(screen.getByRole("button", { name: "Try again" }));
    await screen.findByRole("dialog", { name: queued });
    expect(requests).toHaveLength(2);
    if (code === "UNKNOWN") expect(requests[1]).toEqual(requests[0]);
    else expect(requests[1].request_id).not.toBe(requests[0].request_id);
  });
  it.each([
    { name: "viewer", canManage: false, reason: /member or admin/ },
    { name: "unsupported installation", canManage: true, reason: /not enabled/ },
  ])("explains unavailable submission for $name", async ({ canManage, reason }) => {
    mockContext({ capability: false });
    open(canManage);
    await waitFor(() => expect(screen.getByRole("status")).toHaveTextContent(reason));
    expect(screen.getByRole("button", { name: title })).toBeDisabled();
  });
  it("rejects malformed deep links", async () => {
    open(true, "invalid");
    expect(await screen.findByRole("alert")).toHaveTextContent("link is invalid");
    expect(screen.queryByRole("button", { name: title })).not.toBeInTheDocument();
  });
  it.each(["target", "policy"])("fails closed when %s loading fails", async (failure) => {
    mockContext();
    const endpoint = failure === "policy" ? "*/api/v1/code-review-policies" : mode === "recheck" ? `*/api/v1/code-review-assessments/${assessmentID}` : `*/api/v1/code-reviews/${sessionID}`;
    server.use(http.get(endpoint, () => HttpResponse.json({ error: { code: "UNAVAILABLE", message: "Unavailable" } }, { status: 503 })));
    open();
    expect(await screen.findByRole("alert")).toHaveTextContent(failure === "policy" ? "Review settings could not be loaded" : "correct organization");
    expect(screen.queryByRole("button", { name: title })).not.toBeInTheDocument();
  });
});

it.each([
  { status: "running", continuation: true, message: "Evidence can be re-checked after the review is completed." },
  { status: "completed", continuation: false, message: "An administrator must enable review continuation." },
])("prevents rechecks when status=$status and continuation=$continuation", async ({ status, continuation, message }) => {
  mockContext({ status, continuation });
  renderWithProviders(<RecheckFromComment canManage />, { searchParams: { recheck: assessmentID } });
  expect(await screen.findByText(message)).toBeInTheDocument();
  expect(screen.getByRole("button", { name: "Re-check evidence" })).toBeDisabled();
});
