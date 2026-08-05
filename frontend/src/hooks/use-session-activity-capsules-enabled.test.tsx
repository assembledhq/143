import { createElement, type ReactNode } from "react";
import { act, renderHook, waitFor } from "@testing-library/react";
import { focusManager, QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  SESSION_ACTIVITY_CONFIG_FRESHNESS_MS,
  useSessionActivityCapsulesEnabled,
} from "./use-session-activity-capsules-enabled";

const { getApplicationConfig } = vi.hoisted(() => ({
  getApplicationConfig: vi.fn(),
}));

vi.mock("@/lib/api", () => ({
  api: { applicationConfig: { get: getApplicationConfig } },
}));

function setup() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  const Wrapper = ({ children }: { children: ReactNode }) =>
    createElement(QueryClientProvider, { client }, children);
  const rendered = renderHook(() => useSessionActivityCapsulesEnabled(), {
    wrapper: Wrapper,
  });
  return { client, ...rendered };
}

describe("useSessionActivityCapsulesEnabled", () => {
  beforeEach(() => {
    getApplicationConfig.mockReset();
  });

  afterEach(() => {
    focusManager.setFocused(undefined);
  });

  it("fails closed until the initial config request succeeds", async () => {
    getApplicationConfig.mockRejectedValue(new Error("offline"));

    const { result } = setup();

    expect(result.current.enabled).toBe(false);
    await waitFor(() => expect(result.current.query.isError).toBe(true));
    expect(result.current.enabled).toBe(false);
  });

  it("retains the last known config when a later refresh fails", async () => {
    getApplicationConfig
      .mockResolvedValueOnce({
        data: {
          session_activity_capsules_enabled: true,
          revision: "enabled",
          updated_at: "2026-08-03T12:00:00Z",
        },
      })
      .mockRejectedValueOnce(new Error("offline"));

    const { result } = setup();
    await waitFor(() => expect(result.current.enabled).toBe(true));

    await act(async () => {
      await result.current.query.refetch();
    });

    expect(getApplicationConfig).toHaveBeenCalledTimes(2);
    expect(result.current.enabled).toBe(true);
  });

  it("uses the documented 30-second freshness bound", () => {
    expect(SESSION_ACTIVITY_CONFIG_FRESHNESS_MS).toBe(30_000);
  });

  it("refreshes on focus even while the cached config is fresh", async () => {
    getApplicationConfig
      .mockResolvedValueOnce({
        data: {
          session_activity_capsules_enabled: false,
          revision: "disabled",
          updated_at: "2026-08-03T12:00:00Z",
        },
      })
      .mockResolvedValue({
        data: {
          session_activity_capsules_enabled: true,
          revision: "enabled",
          updated_at: "2026-08-03T12:00:01Z",
        },
      });

    const { result } = setup();
    await waitFor(() => expect(getApplicationConfig).toHaveBeenCalledTimes(1));
    expect(result.current.enabled).toBe(false);

    act(() => {
      focusManager.setFocused(false);
      focusManager.setFocused(true);
    });

    await waitFor(() => expect(getApplicationConfig).toHaveBeenCalledTimes(2));
    await waitFor(() => expect(result.current.enabled).toBe(true));
  });
});
