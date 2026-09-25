import { describe, it, expect } from "vitest";
import { http, HttpResponse } from "msw";
import { createTestQueryClient, renderWithProviders, screen, userEvent, waitFor } from "@/test/test-utils";
import { server } from "@/test/mocks/server";
import { RecheckActions } from "./recheck-actions";

function policy(capability = true, enabled = true) {
  server.use(http.get("*/api/v1/code-review-policies", () => HttpResponse.json({ data: {
    capabilities: { conditional_recheck: capability }, config: { continuation_policy: { enabled, automatic_evidence_rechecks: false } },
  } })));
}

describe.each(["inline", "menu"] as const)("PR re-check actions (%s)", (presentation) => {
  async function recheck(user: ReturnType<typeof userEvent.setup>) {
    if (presentation === "menu") {
      expect(screen.queryByRole("button", { name: "Re-check PR evidence" })).not.toBeInTheDocument();
      const trigger = await screen.findByRole("button", { name: "More review actions" });
      await waitFor(() => expect(trigger).toBeEnabled());
      await user.click(trigger);
      await user.click(screen.getByRole("menuitem", { name: "Re-check PR evidence" }));
    } else {
      const button = await screen.findByRole("button", { name: "Re-check PR evidence" });
      await waitFor(() => expect(button).toBeEnabled());
      await user.click(button);
    }
  }

  it("reuses an uncertain request ID and reports backend reuse", async () => {
    policy();
    const requests: { request_id: string; mode: string }[] = [];
    server.use(http.post("*/api/v1/pull-requests/:id/code-review/requests", async ({ request }) => {
      requests.push(await request.json() as { request_id: string; mode: string });
      if (requests.length === 1) return HttpResponse.json({ error: { code: "UNAVAILABLE", message: "Retry request" } }, { status: 503 });
      return HttpResponse.json({ data: { disposition: "reused" } });
    }));
    const user = userEvent.setup();
    renderWithProviders(<RecheckActions prID="pr-1" canManage completed presentation={presentation} />);
    await recheck(user);
    expect(await screen.findByRole("alert")).toHaveTextContent("Retry request");
    await recheck(user);
    expect(await screen.findByRole("status")).toHaveTextContent("Existing assessment reused for captured inputs");
    expect(requests).toEqual([{ request_id: expect.any(String), mode: "recheck" }, { request_id: requests[0].request_id, mode: "recheck" }]);
  });

  it("requires a reason for force fresh and sends it", async () => {
    policy();
    const requests: unknown[] = [];
    server.use(http.post("*/api/v1/pull-requests/:id/code-review/requests", async ({ request }) => {
      requests.push(await request.json());
      return HttpResponse.json({ data: { disposition: "queued" } }, { status: 202 });
    }));
    const user = userEvent.setup();
    renderWithProviders(<RecheckActions prID="pr-1" canManage completed presentation={presentation} />);
    const trigger = await screen.findByRole("button", { name: "More review actions" });
    await waitFor(() => expect(trigger).toBeEnabled());
    await user.click(trigger);
    await user.click(screen.getByRole("menuitem", { name: "Force fresh review" }));
    expect(screen.getByRole("button", { name: "Request full review" })).toBeDisabled();
    await user.type(screen.getByRole("textbox", { name: "Reason" }), "Please revisit the full code path");
    await user.click(screen.getByRole("button", { name: "Request full review" }));
    await waitFor(() => expect(requests).toEqual([{ request_id: expect.any(String), mode: "force_fresh", reason: "Please revisit the full code path" }]));
    expect(await screen.findByRole("status")).toHaveTextContent("Full review requested.");
  });

  it("explains a queued re-check and links its assessment", async () => {
    policy();
    server.use(http.post("*/api/v1/pull-requests/:id/code-review/requests", () => HttpResponse.json({ data: { disposition: "queued", assessment_id: "00000000-0000-4000-8000-000000000001" } }, { status: 202 })));
    const user = userEvent.setup();
    renderWithProviders(<RecheckActions prID="pr-1" canManage completed presentation={presentation} />);
    await recheck(user);
    expect(await screen.findByRole("status")).toHaveTextContent("Re-check requested. Changes to code or review policy require a full review.");
    expect(screen.getByRole("link", { name: "View assessment" })).toHaveAttribute("href", "/code-reviews?assessment=00000000-0000-4000-8000-000000000001");
  });

  it("allocates a new identity when the force reason changes after an uncertain failure", async () => {
    policy();
    const requests: { request_id: string; reason: string }[] = [];
    server.use(http.post("*/api/v1/pull-requests/:id/code-review/requests", async ({ request }) => {
      requests.push(await request.json() as { request_id: string; reason: string });
      return HttpResponse.json({ error: { code: "UNAVAILABLE", message: "Retry request" } }, { status: 503 });
    }));
    const user = userEvent.setup();
    renderWithProviders(<RecheckActions prID="pr-1" canManage completed presentation={presentation} />);
    const trigger = await screen.findByRole("button", { name: "More review actions" });
    await waitFor(() => expect(trigger).toBeEnabled());
    await user.click(trigger);
    await user.click(screen.getByRole("menuitem", { name: "Force fresh review" }));
    const reason = screen.getByRole("textbox", { name: "Reason" });
    await user.type(reason, "First reason");
    await user.click(screen.getByRole("button", { name: "Request full review" }));
    await waitFor(() => expect(requests).toHaveLength(1));
    await waitFor(() => expect(screen.getByRole("button", { name: "Request full review" })).toBeEnabled());
    await user.click(screen.getByRole("button", { name: "Request full review" }));
    await waitFor(() => expect(requests).toHaveLength(2));
    expect(requests[1].request_id).toBe(requests[0].request_id);
    await user.clear(reason);
    await user.type(reason, "Different reason");
    await user.click(screen.getByRole("button", { name: "Request full review" }));
    await waitFor(() => expect(requests).toHaveLength(3));
    expect(requests[2].request_id).not.toBe(requests[0].request_id);
  });

  it("disables both actions until continuation is enabled", async () => {
    policy(true, false);
    const user = userEvent.setup();
    const queryClient = createTestQueryClient();
    renderWithProviders(<RecheckActions prID="pr-1" canManage completed presentation={presentation} />, { queryClient });
    await waitFor(() => expect(queryClient.isFetching()).toBe(0));
    if (presentation === "inline") expect(screen.getByRole("button", { name: "Re-check PR evidence" })).toBeDisabled();
    const trigger = screen.getByRole("button", { name: "More review actions" });
    expect(trigger).toBeDisabled();
    await user.hover(trigger.parentElement!);
    expect(await screen.findByRole("tooltip")).toHaveTextContent("An administrator must enable review continuation.");
  });

  it.each([
    { capability: false, enabled: true, canManage: true, completed: true },
    { capability: true, enabled: true, canManage: false, completed: true },
    { capability: true, enabled: true, canManage: true, completed: false },
  ])("hides unavailable controls: %j", async (state) => {
    policy(state.capability, state.enabled);
    const queryClient = createTestQueryClient();
    renderWithProviders(<RecheckActions prID="pr-1" canManage={state.canManage} completed={state.completed} presentation={presentation} />, { queryClient });
    await waitFor(() => expect(queryClient.isFetching()).toBe(0));
    expect(screen.queryByRole("button", { name: "Re-check PR evidence" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "More review actions" })).not.toBeInTheDocument();
  });
});
