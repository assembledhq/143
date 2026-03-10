import { describe, it, expect, vi, beforeEach } from "vitest";
import { renderHook, act, waitFor } from "@testing-library/react";
import React from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { http, HttpResponse } from "msw";
import { server } from "@/test/mocks/server";
import { useAnalyze } from "./use-analyze";

function createWrapper() {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false, gcTime: 0 } },
  });
  return function Wrapper({ children }: { children: React.ReactNode }) {
    return React.createElement(QueryClientProvider, { client: queryClient }, children);
  };
}

beforeEach(() => {
  sessionStorage.clear();
});

describe("useAnalyze", () => {
  it("returns initial state with no sessionStorage", () => {
    const { result } = renderHook(() => useAnalyze(false), {
      wrapper: createWrapper(),
    });

    expect(result.current.isAnalyzing).toBe(false);
    expect(result.current.isPending).toBe(false);
    expect(result.current.analyzeError).toBeNull();
  });

  it("seeds isAnalyzing from recent sessionStorage", () => {
    sessionStorage.setItem("143:analyze-started-at", String(Date.now()));

    const { result } = renderHook(() => useAnalyze(false), {
      wrapper: createWrapper(),
    });

    expect(result.current.isAnalyzing).toBe(true);
  });

  it("ignores expired sessionStorage (>90s)", () => {
    sessionStorage.setItem(
      "143:analyze-started-at",
      String(Date.now() - 100_000),
    );

    const { result } = renderHook(() => useAnalyze(false), {
      wrapper: createWrapper(),
    });

    expect(result.current.isAnalyzing).toBe(false);
    expect(sessionStorage.getItem("143:analyze-started-at")).toBeNull();
  });

  it("handleAnalyze success sets isAnalyzing true", async () => {
    const { result } = renderHook(() => useAnalyze(false), {
      wrapper: createWrapper(),
    });

    act(() => {
      result.current.handleAnalyze();
    });

    await waitFor(() => {
      expect(result.current.isAnalyzing).toBe(true);
    });

    expect(sessionStorage.getItem("143:analyze-started-at")).not.toBeNull();
  });

  it("handleAnalyze failure sets analyzeError", async () => {
    server.use(
      http.post("/api/v1/pm/analyze", () => {
        return HttpResponse.json(
          { error: { message: "Internal Server Error" } },
          { status: 500 },
        );
      }),
    );

    const { result } = renderHook(() => useAnalyze(false), {
      wrapper: createWrapper(),
    });

    act(() => {
      result.current.handleAnalyze();
    });

    await waitFor(() => {
      expect(result.current.analyzeError).toBe(
        "Failed to start analysis. Make sure the backend is running.",
      );
    });
  });

  it("clears local flag when hasActivePlanSession becomes true", async () => {
    sessionStorage.setItem("143:analyze-started-at", String(Date.now()));

    const { result, rerender } = renderHook(
      ({ hasActive }) => useAnalyze(hasActive),
      {
        wrapper: createWrapper(),
        initialProps: { hasActive: false },
      },
    );

    expect(result.current.isAnalyzing).toBe(true);

    rerender({ hasActive: true });

    await waitFor(() => {
      expect(sessionStorage.getItem("143:analyze-started-at")).toBeNull();
    });
  });

  it("timeout clears analyzing after 90s", () => {
    vi.useFakeTimers();

    sessionStorage.setItem("143:analyze-started-at", String(Date.now()));

    const { result } = renderHook(() => useAnalyze(false), {
      wrapper: createWrapper(),
    });

    expect(result.current.isAnalyzing).toBe(true);

    act(() => {
      vi.advanceTimersByTime(91_000);
    });

    expect(result.current.isAnalyzing).toBe(false);
    expect(result.current.analyzeError).toContain("Analysis may have failed");

    vi.useRealTimers();
  });

  it("dismissError clears error", async () => {
    server.use(
      http.post("/api/v1/pm/analyze", () => {
        return HttpResponse.json(
          { error: { message: "Internal Server Error" } },
          { status: 500 },
        );
      }),
    );

    const { result } = renderHook(() => useAnalyze(false), {
      wrapper: createWrapper(),
    });

    act(() => {
      result.current.handleAnalyze();
    });

    await waitFor(() => {
      expect(result.current.analyzeError).not.toBeNull();
    });

    act(() => {
      result.current.dismissError();
    });

    expect(result.current.analyzeError).toBeNull();
  });
});
