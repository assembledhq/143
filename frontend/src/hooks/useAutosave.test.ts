import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { renderHook, act, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createElement, type ReactNode } from "react";
import { toast } from "sonner";
import { useAutosave } from "./useAutosave";

vi.mock("sonner", () => ({
  toast: {
    error: vi.fn(),
    success: vi.fn(),
  },
}));

vi.mock("@/lib/errors", () => ({
  captureError: vi.fn(),
}));

interface Settings {
  data: { settings: Record<string, unknown> };
}

function makeWrapper(client: QueryClient) {
  const Wrapper = ({ children }: { children: ReactNode }) =>
    createElement(QueryClientProvider, { client }, children);
  Wrapper.displayName = "TestQueryClientProvider";
  return Wrapper;
}

const applyOptimistic = (prev: unknown, vars: { settings: Record<string, unknown> }): Settings => {
  const current = (prev as Settings | undefined) ?? { data: { settings: {} } };
  return {
    data: {
      settings: { ...current.data.settings, ...vars.settings },
    },
  };
};

const coalesce = (
  a: { settings: Record<string, unknown> },
  b: { settings: Record<string, unknown> },
) => ({ settings: { ...a.settings, ...b.settings } });

const sleep = (ms: number) => new Promise<void>((resolve) => setTimeout(resolve, ms));

describe("useAutosave", () => {
  let queryClient: QueryClient;

  beforeEach(() => {
    queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
    });
    vi.clearAllMocks();
  });

  afterEach(() => {
    queryClient.clear();
  });

  it("fires the mutation immediately when debounceMs is 0", async () => {
    const mutationFn = vi.fn().mockResolvedValue(undefined);
    const { result } = renderHook(
      () =>
        useAutosave<{ settings: { foo: string } }>({
          queryKey: ["settings", "test-immediate"],
          mutationFn,
          applyOptimistic,
          debounceMs: 0,
        }),
      { wrapper: makeWrapper(queryClient) },
    );

    act(() => {
      result.current.save({ settings: { foo: "bar" } });
    });

    await waitFor(() => expect(mutationFn).toHaveBeenCalledTimes(1));
    expect(mutationFn).toHaveBeenCalledWith({ settings: { foo: "bar" } });
  });

  it("debounces successive calls when debounceMs > 0", async () => {
    const mutationFn = vi.fn().mockResolvedValue(undefined);
    const { result } = renderHook(
      () =>
        useAutosave<{ settings: { text: string } }>({
          queryKey: ["settings", "test-debounce"],
          mutationFn,
          applyOptimistic,
          debounceMs: 40,
        }),
      { wrapper: makeWrapper(queryClient) },
    );

    act(() => {
      result.current.save({ settings: { text: "a" } });
      result.current.save({ settings: { text: "ab" } });
      result.current.save({ settings: { text: "abc" } });
    });

    expect(mutationFn).not.toHaveBeenCalled();

    await waitFor(() => expect(mutationFn).toHaveBeenCalledTimes(1), { timeout: 500 });
    expect(mutationFn).toHaveBeenCalledWith({ settings: { text: "abc" } });
  });

  it("flush() fires the pending debounced payload immediately", async () => {
    const mutationFn = vi.fn().mockResolvedValue(undefined);
    const { result } = renderHook(
      () =>
        useAutosave<{ settings: { text: string } }>({
          queryKey: ["settings", "test-flush"],
          mutationFn,
          applyOptimistic,
          debounceMs: 5_000,
        }),
      { wrapper: makeWrapper(queryClient) },
    );

    act(() => {
      result.current.save({ settings: { text: "typed" } });
    });
    expect(mutationFn).not.toHaveBeenCalled();

    act(() => {
      result.current.flush();
    });

    await waitFor(() => expect(mutationFn).toHaveBeenCalledWith({ settings: { text: "typed" } }));
  });

  it("coalesces calls that arrive while a mutation is in flight", async () => {
    let resolveFirst: (() => void) | undefined;
    const mutationFn = vi
      .fn()
      .mockImplementationOnce(
        () =>
          new Promise<void>((resolve) => {
            resolveFirst = resolve;
          }),
      )
      .mockResolvedValue(undefined);

    const { result } = renderHook(
      () =>
        useAutosave<{ settings: Record<string, unknown> }>({
          queryKey: ["settings", "test-coalesce"],
          mutationFn,
          applyOptimistic,
          coalesce,
          debounceMs: 0,
        }),
      { wrapper: makeWrapper(queryClient) },
    );

    act(() => {
      result.current.save({ settings: { a: 1 } });
    });
    await waitFor(() => expect(mutationFn).toHaveBeenCalledTimes(1));

    act(() => {
      result.current.save({ settings: { b: 2 } });
      result.current.save({ settings: { c: 3 } });
    });
    expect(mutationFn).toHaveBeenCalledTimes(1);

    await act(async () => {
      resolveFirst?.();
      await Promise.resolve();
    });

    await waitFor(() => expect(mutationFn).toHaveBeenCalledTimes(2));
    expect(mutationFn).toHaveBeenNthCalledWith(2, { settings: { b: 2, c: 3 } });
  });

  it("rolls back optimistic update and shows toast on error", async () => {
    const mutationFn = vi.fn().mockRejectedValue(new Error("500 boom"));
    const queryKey = ["settings", "test-rollback"];
    queryClient.setQueryData(queryKey, { data: { settings: { existing: "value" } } });

    const { result } = renderHook(
      () =>
        useAutosave<{ settings: { new: string } }>({
          queryKey,
          mutationFn,
          applyOptimistic,
          errorMessage: "Nope.",
          debounceMs: 0,
        }),
      { wrapper: makeWrapper(queryClient) },
    );

    act(() => {
      result.current.save({ settings: { new: "x" } });
    });

    await waitFor(() => expect(toast.error).toHaveBeenCalledWith("Nope."));
    expect(queryClient.getQueryData(queryKey)).toEqual({ data: { settings: { existing: "value" } } });
  });

  it("transitions status idle → saving → saved → idle", async () => {
    let resolveMutation: (() => void) | undefined;
    const mutationFn = vi.fn().mockImplementation(
      () =>
        new Promise<void>((resolve) => {
          resolveMutation = resolve;
        }),
    );

    const { result } = renderHook(
      () =>
        useAutosave<{ settings: { v: number } }>({
          queryKey: ["settings", "test-status"],
          mutationFn,
          applyOptimistic,
          debounceMs: 0,
        }),
      { wrapper: makeWrapper(queryClient) },
    );

    expect(result.current.status).toBe("idle");

    act(() => {
      result.current.save({ settings: { v: 1 } });
    });
    await waitFor(() => expect(result.current.status).toBe("saving"));

    await act(async () => {
      resolveMutation?.();
      await Promise.resolve();
    });
    await waitFor(() => expect(result.current.status).toBe("saved"));

    // Wait longer than SAVED_LINGER_MS (1500ms) using real timers.
    await act(async () => {
      await sleep(1700);
    });
    expect(result.current.status).toBe("idle");
  }, 10_000);

  it("serializes across two hooks sharing the same queryKey", async () => {
    const callOrder: string[] = [];
    let resolveA: (() => void) | undefined;
    const mutationFn = vi.fn().mockImplementation((vars: { id: string }) => {
      callOrder.push(vars.id);
      if (vars.id === "A") {
        return new Promise<void>((resolve) => {
          resolveA = resolve;
        });
      }
      return Promise.resolve();
    });

    const queryKey = ["settings", "test-serialize"];
    const { result: resultA } = renderHook(
      () =>
        useAutosave<{ id: string }>({
          queryKey,
          mutationFn,
          applyOptimistic: (prev) => prev,
          debounceMs: 0,
        }),
      { wrapper: makeWrapper(queryClient) },
    );
    const { result: resultB } = renderHook(
      () =>
        useAutosave<{ id: string }>({
          queryKey,
          mutationFn,
          applyOptimistic: (prev) => prev,
          debounceMs: 0,
        }),
      { wrapper: makeWrapper(queryClient) },
    );

    act(() => {
      resultA.current.save({ id: "A" });
      resultB.current.save({ id: "B" });
    });

    await waitFor(() => expect(mutationFn).toHaveBeenCalledTimes(1));
    expect(callOrder).toEqual(["A"]);

    await act(async () => {
      resolveA?.();
      await Promise.resolve();
    });

    await waitFor(() => expect(mutationFn).toHaveBeenCalledTimes(2));
    expect(callOrder).toEqual(["A", "B"]);
  });

  it("shares status transitions across two hooks on the same queryKey", async () => {
    let resolveMutation: (() => void) | undefined;
    const mutationFn = vi.fn().mockImplementation(
      () =>
        new Promise<void>((resolve) => {
          resolveMutation = resolve;
        }),
    );

    const queryKey = ["settings", "test-shared-status"];
    const { result: resultA } = renderHook(
      () =>
        useAutosave<{ settings: { v: number } }>({
          queryKey,
          mutationFn,
          applyOptimistic,
          debounceMs: 0,
        }),
      { wrapper: makeWrapper(queryClient) },
    );
    const { result: resultB } = renderHook(
      () =>
        useAutosave<{ settings: { v: number } }>({
          queryKey,
          mutationFn,
          applyOptimistic,
          debounceMs: 0,
        }),
      { wrapper: makeWrapper(queryClient) },
    );

    expect(resultA.current.status).toBe("idle");
    expect(resultB.current.status).toBe("idle");

    act(() => {
      resultA.current.save({ settings: { v: 1 } });
    });

    // Both hooks observe the "saving" transition even though only A dispatched.
    await waitFor(() => expect(resultA.current.status).toBe("saving"));
    expect(resultB.current.status).toBe("saving");

    await act(async () => {
      resolveMutation?.();
      await Promise.resolve();
    });

    await waitFor(() => expect(resultA.current.status).toBe("saved"));
    expect(resultB.current.status).toBe("saved");
  });

  it("cancels pending debounce on unmount without firing the mutation", async () => {
    const mutationFn = vi.fn().mockResolvedValue(undefined);
    const { result, unmount } = renderHook(
      () =>
        useAutosave<{ settings: { x: number } }>({
          queryKey: ["settings", "test-unmount"],
          mutationFn,
          applyOptimistic,
          debounceMs: 200,
        }),
      { wrapper: makeWrapper(queryClient) },
    );

    act(() => {
      result.current.save({ settings: { x: 1 } });
    });
    unmount();

    await sleep(400);

    expect(mutationFn).not.toHaveBeenCalled();
  });
});
