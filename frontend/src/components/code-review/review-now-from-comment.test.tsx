import { describe, it, expect } from "vitest";
import { http, HttpResponse } from "msw";
import { renderWithProviders, screen, userEvent, waitFor, within } from "@/test/test-utils";
import { server } from "@/test/mocks/server";
import { ReviewNowFromComment } from "./review-now-from-comment";

const sessionID = "90d8a47d-d87e-4780-90af-040f5144685a";
const prID = "b8c1dede-d37b-497c-95c9-ec096f514c2a";
function mockTarget({ enabled = true, inaccessible = false } = {}) {
  server.use(
    http.get("*/api/v1/code-reviews/:id", () => inaccessible
      ? HttpResponse.json({ error: { code: "NOT_FOUND", message: "Not found" } }, { status: 404 })
      : HttpResponse.json({ data: { session_id: sessionID, pull_request_id: prID, github_repo: "acme/api", github_pr_number: 17, pull_request_title: "Reduce duplicate reviews" } })),
    http.get("*/api/v1/code-review-policies", () => HttpResponse.json({ data: { capabilities: { scheduling: enabled } } })),
  );
}
function renderLink(canManage = true) {
  return renderWithProviders(<ReviewNowFromComment canManage={canManage} />, {
    searchParams: { review_now: sessionID }, nuqsHasMemory: true,
  });
}

describe("review link from a GitHub comment", () => {
  it("loads the PR without mutation and submits only after the user clicks Request full review", async () => {
    mockTarget();
    const requests: { path: string; body: unknown }[] = [];
    server.use(http.post("*/api/v1/pull-requests/:id/code-review/requests", async ({ request }) => {
      requests.push({ path: new URL(request.url).pathname, body: await request.json() });
      return HttpResponse.json({ data: { disposition: "queued" } }, { status: 202 });
    }));
    const user = userEvent.setup();
    renderLink();
    const dialog = await screen.findByRole("dialog", { name: "Request full review" });
    expect(await within(dialog).findByText("acme/api #17")).toBeInTheDocument();
    const action = await within(dialog).findByRole("button", { name: "Request full review" });
    expect(requests).toEqual([]);
    await user.click(action);
    await waitFor(() => expect(requests).toEqual([{
      path: `/api/v1/pull-requests/${prID}/code-review/requests`,
      body: { request_id: expect.any(String), mode: "review_now" },
    }]));
    expect(await within(dialog).findByRole("status")).toHaveTextContent("Queued");
  });
  it.each([
    { name: "viewer", canManage: false, enabled: true, inaccessible: false, message: /member or admin/ },
    { name: "capability disabled", canManage: true, enabled: false, inaccessible: false, message: /not enabled/ },
    { name: "wrong organization or inaccessible PR", canManage: true, enabled: true, inaccessible: true, message: /correct organization/ },
  ])("does not allow submission for $name", async ({ canManage, enabled, inaccessible, message }) => {
    mockTarget({ enabled, inaccessible });
    renderLink(canManage);
    const dialog = await screen.findByRole("dialog");
    expect(await within(dialog).findByText(message)).toBeInTheDocument();
    const action = within(dialog).queryByRole("button", { name: "Request full review" });
    if (action) expect(action).toBeDisabled();
  });
  it("closing the dialog does not submit a review", async () => {
    mockTarget();
    renderLink();
    const dialog = await screen.findByRole("dialog");
    await userEvent.click(await within(dialog).findByRole("button", { name: "Cancel" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
  });
});
