import { describe, it, expect } from "vitest";
import { http, HttpResponse } from "msw";
import { renderWithProviders, screen, userEvent, waitFor, within } from "@/test/test-utils";
import { server } from "@/test/mocks/server";
import { ReviewNowFromComment } from "./review-now-from-comment";

const sessionID = "90d8a47d-d87e-4780-90af-040f5144685a";
const prID = "b8c1dede-d37b-497c-95c9-ec096f514c2a";
function mockTarget({ enabled = true, inaccessible = false, allowOverride = false, reviewHistory = false } = {}) {
  server.use(
    http.get("*/api/v1/code-reviews/:id", () => inaccessible
      ? HttpResponse.json({ error: { code: "NOT_FOUND", message: "Not found" } }, { status: 404 })
      : HttpResponse.json({ data: { session_id: sessionID, pull_request_id: prID, github_repo: "acme/api", github_pr_number: 17, pull_request_title: "Reduce duplicate reviews" } })),
    http.get("*/api/v1/code-review-policies", () => HttpResponse.json({ data: { capabilities: { scheduling: enabled, review_history: reviewHistory }, config: { risk_policy: { allow_size_limit_override: allowOverride } } } })),
  );
}
function renderLink(canManage = true) {
  return renderWithProviders(<ReviewNowFromComment canManage={canManage} />, {
    searchParams: { review_now: sessionID }, nuqsHasMemory: true,
  });
}

describe("review link from a GitHub comment", () => {
  it.each<{ name: string; linkParams: Record<string, string>; toggle: boolean; history: boolean }>([
    { name: "review history is unchecked by default", linkParams: {}, toggle: false, history: false },
    { name: "the history link preselects the opt-in", linkParams: { use_review_history: "true" }, toggle: false, history: true },
    { name: "review history can be selected in the dialog", linkParams: {}, toggle: true, history: true },
    { name: "a preselected history opt-in can be cleared", linkParams: { use_review_history: "true" }, toggle: true, history: false },
  ])("$name", async ({ linkParams, toggle, history }) => {
    mockTarget({ reviewHistory: true });
    const bodies: unknown[] = [];
    server.use(http.post("*/api/v1/pull-requests/:id/code-review/requests", async ({ request }) => {
      bodies.push(await request.json());
      return HttpResponse.json({ data: { disposition: "queued" } }, { status: 202 });
    }));
    const user = userEvent.setup();
    renderWithProviders(<ReviewNowFromComment canManage />, { searchParams: { review_now: sessionID, ...linkParams }, nuqsHasMemory: true });
    const dialog = await screen.findByRole("dialog", { name: "Request full review" });
    const optIn = await within(dialog).findByRole("checkbox", { name: "Use earlier review history (experimental)" });
    expect(optIn).toHaveAccessibleDescription(/retrieve earlier review reports and session excerpts/i);
    if (toggle) await user.click(optIn);
    expect(optIn).toHaveAttribute("aria-checked", String(history));
    expect(within(dialog).getByText(history ? /Starts a new full review/ : /may be reused/)).toBeInTheDocument();
    expect(bodies).toEqual([]);
    await user.click(within(dialog).getByRole("button", { name: "Request full review" }));
    await waitFor(() => expect(bodies).toEqual([{ request_id: expect.any(String), mode: "review_now", ...(history ? { use_review_history: true } : {}) }]));
  });
  it("ignores a history link until the installation supports retrieval", async () => {
    mockTarget();
    const bodies: unknown[] = [];
    server.use(http.post("*/api/v1/pull-requests/:id/code-review/requests", async ({ request }) => {
      bodies.push(await request.json());
      return HttpResponse.json({ data: { disposition: "queued" } }, { status: 202 });
    }));
    const user = userEvent.setup();
    renderWithProviders(<ReviewNowFromComment canManage />, { searchParams: { review_now: sessionID, use_review_history: "true" }, nuqsHasMemory: true });
    const dialog = await screen.findByRole("dialog", { name: "Request full review" });
    const action = await within(dialog).findByRole("button", { name: "Request full review" });
    await waitFor(() => expect(action).toBeEnabled());
    expect(within(dialog).queryByRole("checkbox", { name: "Use earlier review history (experimental)" })).not.toBeInTheDocument();
    await user.click(action);
    await waitFor(() => expect(bodies).toEqual([{ request_id: expect.any(String), mode: "review_now" }]));
  });
  it("submits history retrieval and a size override as independent options", async () => {
    mockTarget({ reviewHistory: true, allowOverride: true });
    const bodies: unknown[] = [];
    server.use(http.post("*/api/v1/pull-requests/:id/code-review/requests", async ({ request }) => {
      bodies.push(await request.json());
      return HttpResponse.json({ data: { disposition: "queued" } }, { status: 202 });
    }));
    const user = userEvent.setup();
    renderWithProviders(<ReviewNowFromComment canManage />, { searchParams: { review_now: sessionID, use_review_history: "true", override_size_limits: "true" }, nuqsHasMemory: true });
    const dialog = await screen.findByRole("dialog", { name: "Request full review" });
    await within(dialog).findByRole("checkbox", { name: "Use earlier review history (experimental)" });
    await user.click(within(dialog).getByRole("button", { name: "Request full review" }));
    await waitFor(() => expect(bodies).toEqual([{ request_id: expect.any(String), mode: "review_now", use_review_history: true, override_size_limits: true, override_review_session_id: sessionID }]));
  });
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
  it.each<{ name: string; linkParams: Record<string, string>; toggle: boolean; override: boolean }>([
    { name: "the override link preselects the opt-in", linkParams: { override_size_limits: "true" }, toggle: false, override: true },
    { name: "the opt-in can be selected in the dialog", linkParams: {}, toggle: true, override: true },
    { name: "a preselected opt-in can be cleared", linkParams: { override_size_limits: "true" }, toggle: true, override: false },
    { name: "the opt-in stays off by default", linkParams: {}, toggle: false, override: false },
  ])("$name", async ({ linkParams, toggle, override }) => {
    mockTarget({ allowOverride: true });
    const bodies: unknown[] = [];
    server.use(http.post("*/api/v1/pull-requests/:id/code-review/requests", async ({ request }) => {
      bodies.push(await request.json());
      return HttpResponse.json({ data: { disposition: "queued" } }, { status: 202 });
    }));
    const user = userEvent.setup();
    renderWithProviders(<ReviewNowFromComment canManage />, { searchParams: { review_now: sessionID, ...linkParams }, nuqsHasMemory: true });
    const dialog = await screen.findByRole("dialog", { name: "Request full review" });
    const optIn = await within(dialog).findByRole("checkbox", { name: "Override size limits for this review" });
    expect(optIn).toHaveAccessibleDescription(/waives the files, additions, and deletions limits/i);
    if (toggle) await user.click(optIn);
    await user.click(await within(dialog).findByRole("button", { name: "Request full review" }));
    const expected = override ? { override_size_limits: true, override_review_session_id: sessionID } : {};
    await waitFor(() => expect(bodies).toEqual([{ request_id: expect.any(String), mode: "review_now", ...expected }]));
  });
  it("lets the user clear a stale override and deliberately request a plain review", async () => {
    mockTarget({ allowOverride: true });
    const staleMessage = "This review link refers to an older comparison. Uncheck the override or open the latest review link.";
    const bodies: { request_id: string; override_size_limits?: boolean }[] = [];
    server.use(http.post("*/api/v1/pull-requests/:id/code-review/requests", async ({ request }) => {
      const body = await request.json() as typeof bodies[number];
      bodies.push(body);
      if (body.override_size_limits) {
        return HttpResponse.json({ error: { code: "CODE_REVIEW_OVERRIDE_STALE", message: staleMessage } }, { status: 409 });
      }
      return HttpResponse.json({ data: { disposition: "queued" } }, { status: 202 });
    }));
    const user = userEvent.setup();
    renderWithProviders(<ReviewNowFromComment canManage />, { searchParams: { review_now: sessionID, override_size_limits: "true" }, nuqsHasMemory: true });
    const dialog = await screen.findByRole("dialog", { name: "Request full review" });
    const optIn = await within(dialog).findByRole("checkbox", { name: "Override size limits for this review" });
    const firstRequest = { request_id: expect.any(String), mode: "review_now", override_size_limits: true, override_review_session_id: sessionID };
    expect(bodies).toEqual([]);
    await user.click(within(dialog).getByRole("button", { name: "Request full review" }));
    expect(await within(dialog).findByRole("alert")).toHaveTextContent(staleMessage);
    expect(bodies).toEqual([firstRequest]);
    expect(within(dialog).getByRole("button", { name: "Try again" })).toBeDisabled();

    await user.click(optIn);
    expect(within(dialog).queryByRole("alert")).not.toBeInTheDocument();
    expect(bodies).toEqual([firstRequest]);
    await user.click(within(dialog).getByRole("button", { name: "Request full review" }));

    expect(await within(dialog).findByRole("status")).toHaveTextContent("Queued");
    expect(bodies).toEqual([firstRequest, { request_id: expect.any(String), mode: "review_now" }]);
    expect(bodies[1]?.request_id).not.toBe(bodies[0]?.request_id);
  });
  it("hides the opt-in and ignores a crafted link when the policy does not allow overrides", async () => {
    mockTarget();
    const bodies: unknown[] = [];
    server.use(http.post("*/api/v1/pull-requests/:id/code-review/requests", async ({ request }) => {
      bodies.push(await request.json());
      return HttpResponse.json({ data: { disposition: "queued" } }, { status: 202 });
    }));
    const user = userEvent.setup();
    renderWithProviders(<ReviewNowFromComment canManage />, { searchParams: { review_now: sessionID, override_size_limits: "true" }, nuqsHasMemory: true });
    const dialog = await screen.findByRole("dialog", { name: "Request full review" });
    const action = await within(dialog).findByRole("button", { name: "Request full review" });
    expect(within(dialog).queryByRole("checkbox")).not.toBeInTheDocument();
    await user.click(action);
    await waitFor(() => expect(bodies).toEqual([{ request_id: expect.any(String), mode: "review_now" }]));
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
