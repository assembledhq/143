import { createElement, type ReactNode } from "react";
import { act, renderHook, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { useSessionActivityDetail } from "./use-session-activity-detail";

const { updateSettings, showError } = vi.hoisted(() => ({
  updateSettings: vi.fn(),
  showError: vi.fn(),
}));

vi.mock("@/hooks/use-auth", () => ({
  useAuth: () => ({ user: { id: "user-1", settings: { session_activity_detail: "compact" } } }),
}));
vi.mock("@/lib/api", () => ({ api: { auth: { updateSettings } } }));
vi.mock("@/lib/notify", () => ({ notify: { error: showError } }));
vi.mock("@/lib/session-activity-events", () => ({ recordSessionActivityEvent: vi.fn() }));

function setup() {
  const client = new QueryClient({ defaultOptions: { mutations: { retry: false }, queries: { retry: false } } });
  client.setQueryData(["auth", "me"], {
    data: { id: "user-1", settings: { session_activity_detail: "compact" } },
  });
  const Wrapper = ({ children }: { children: ReactNode }) => createElement(QueryClientProvider, { client }, children);
  const rendered = renderHook(() => useSessionActivityDetail(), { wrapper: Wrapper });
  return { client, ...rendered };
}

describe("useSessionActivityDetail", () => {
  beforeEach(() => {
    updateSettings.mockReset();
    showError.mockReset();
  });

  it("optimistically updates the shared authenticated-user cache", async () => {
    let resolveMutation: ((value: unknown) => void) | undefined;
    updateSettings.mockReturnValue(new Promise((resolve) => { resolveMutation = resolve; }));
    const { client, result } = setup();

    act(() => result.current.setDetail("detailed"));

    await waitFor(() => expect(client.getQueryData<{ data: { settings: { session_activity_detail: string } } }>(["auth", "me"])?.data.settings.session_activity_detail).toBe("detailed"));
    await act(async () => resolveMutation?.({ data: { id: "user-1", settings: { session_activity_detail: "detailed" } } }));
    await waitFor(() => expect(result.current.mutation.isSuccess).toBe(true));
  });

  it("restores the prior preference and surfaces an error when persistence fails", async () => {
    updateSettings.mockRejectedValue(new Error("offline"));
    const { client, result } = setup();

    act(() => result.current.setDetail("detailed"));

    await waitFor(() => expect(result.current.mutation.isError).toBe(true));
    expect(client.getQueryData<{ data: { settings: { session_activity_detail: string } } }>(["auth", "me"])?.data.settings.session_activity_detail).toBe("compact");
    expect(showError).toHaveBeenCalledWith("Could not save activity detail preference");
  });
});
