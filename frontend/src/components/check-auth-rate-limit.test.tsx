import { describe, expect, it, vi } from "vitest";
import { http, HttpResponse } from "msw";
import { QueryClient, useQuery } from "@tanstack/react-query";
import { toast } from "sonner";
import { renderWithProviders, screen, userEvent, waitFor } from "@/test/test-utils";
import { server } from "@/test/mocks/server";
import { api } from "@/lib/api";
import { queryKeys } from "@/lib/query-keys";
import type { CodingCredentialSummary } from "@/lib/types";
import { CheckAuthRateLimit } from "./check-auth-rate-limit";

const row: CodingCredentialSummary = {
  id: "auth-1", org_id: "org-1", scope: "org", provider: "openai_subscription",
  agent: "codex", auth_type: "subscription", label: "Team seat", priority: 1,
  status: "rate_limited", is_default: false, created_at: "2026-09-10T00:00:00Z", updated_at: "2026-09-10T00:00:00Z",
};

function AuthRow() {
  const { data } = useQuery({queryKey: queryKeys.codingCredentials.list("org"), queryFn: () => api.codingCredentials.list("org")});
  const auth = data?.data[0];
  return auth ? <><span>{auth.status}</span><CheckAuthRateLimit row={auth} /></> : null;
}

describe("CheckAuthRateLimit", () => {
  it.each(["org", "personal"] as const)("checks the selected %s credential and refreshes cached scopes", async (scope) => {
    const request = vi.fn();
    server.use(http.post("*/api/v1/coding-credentials/auth-1/check-rate-limit", ({request: req}) => {
      request(new URL(req.url).searchParams.get("scope"));
      return new HttpResponse(null, { status: 204 });
    }));
    const client = new QueryClient();
    client.setQueryData(queryKeys.codingCredentials.list("resolved"), {data: [row]});
    const invalidate = vi.spyOn(client, "invalidateQueries");
    renderWithProviders(<CheckAuthRateLimit row={{...row, scope}} />, {queryClient: client});
    await userEvent.click(screen.getByRole("button", {name: "Check rate limit for Team seat"}));
    await waitFor(() => expect(invalidate).toHaveBeenCalledWith({queryKey: queryKeys.codingCredentials.all}));
    expect(request).toHaveBeenCalledExactlyOnceWith(scope);
  });

  it("replaces the stale badge after a provider confirms the reset", async () => {
    let checked = false;
    server.use(
      http.get("*/api/v1/coding-credentials", () => HttpResponse.json({data: [{...row, status: checked ? "healthy" : "rate_limited"}], meta: {}})),
      http.post("*/api/v1/coding-credentials/auth-1/check-rate-limit", () => {checked = true; return new HttpResponse(null, {status: 204});}),
    );
    renderWithProviders(<AuthRow />);
    await userEvent.click(await screen.findByRole("button", {name: "Check rate limit for Team seat"}));
    expect(await screen.findByText("healthy")).toBeInTheDocument();
    expect(screen.queryByText("rate_limited")).not.toBeInTheDocument();
    expect(screen.queryByRole("button")).not.toBeInTheDocument();
  });

  it("keeps the cooldown and surfaces provider-check errors", async () => {
    const errorToast = vi.spyOn(toast, "error");
    server.use(http.post("*/api/v1/coding-credentials/auth-1/check-rate-limit", () => HttpResponse.json({error:{code:"CHECK_FAILED",message:"Provider check unavailable"}}, {status:502})));
    renderWithProviders(<CheckAuthRateLimit row={row} />);
    await userEvent.click(screen.getByRole("button", {name:"Check rate limit for Team seat"}));
    await waitFor(() => expect(errorToast).toHaveBeenCalledWith("Provider check unavailable"));
    expect(screen.getByRole("button", {name:"Check rate limit for Team seat"})).toBeEnabled();
  });

  it("disables repeat checks while the request is pending", async () => {
    let finish!: () => void;
    const pending = new Promise<void>((resolve) => {finish = resolve;});
    server.use(http.post("*/api/v1/coding-credentials/auth-1/check-rate-limit", async () => {await pending;return new HttpResponse(null, {status:204});}));
    renderWithProviders(<CheckAuthRateLimit row={row} />);
    await userEvent.click(screen.getByRole("button", {name:"Check rate limit for Team seat"}));
    expect(screen.getByRole("button", {name:"Check rate limit for Team seat"})).toBeDisabled();
    expect(screen.getByText("Checking…")).toBeInTheDocument();
    finish();
    await waitFor(() => expect(screen.getByRole("button")).toBeEnabled());
  });

  it.each([
    {...row, status:"healthy" as const},
    {...row, provider:"openai" as const, auth_type:"api_key" as const},
  ])("hides the action for auths without a supported cooldown", (auth) => {
    renderWithProviders(<CheckAuthRateLimit row={auth} />);
    expect(screen.queryByRole("button")).not.toBeInTheDocument();
  });
});
