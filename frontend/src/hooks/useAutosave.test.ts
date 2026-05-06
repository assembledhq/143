import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { renderHook, act, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createElement, type ReactNode } from "react";
import { notify as toast } from "@/lib/notify";
import { useAutosave, __resetAutosaveQueuesForTests } from "./useAutosave";

vi.mock("@/lib/notify", () => ({
  notify: {
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

describe("useAutosave", () => {
  let queryClient: QueryClient;

  beforeEach(() => {
    // The autosave queue map is module-level, so leftover entries from a
    // previous test (in-flight, pending, or lingering listeners) would bleed
    // into this one and make "first dispatcher wins on coalesce" / eviction
    // assertions non-deterministic. Reset before every case.
    __resetAutosaveQueuesForTests();
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

  it("collapses N rapid saves into at most two mutations (rapid toggle)", async () => {
    // Rapid-toggle scenario: a user mashes a switch (or types very fast). The
    // shared queue should collapse everything into one in-flight + one
    // coalesced follow-up regardless of burst size. Matches the testing
    // checklist in settings/AGENTS.md.
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
          queryKey: ["settings", "test-rapid-toggle"],
          mutationFn,
          applyOptimistic,
          coalesce,
          debounceMs: 0,
        }),
      { wrapper: makeWrapper(queryClient) },
    );

    act(() => {
      result.current.save({ settings: { toggle: true } });
    });
    await waitFor(() => expect(mutationFn).toHaveBeenCalledTimes(1));

    act(() => {
      for (let i = 0; i < 10; i += 1) {
        result.current.save({ settings: { toggle: i % 2 === 0, n: i } });
      }
    });

    // Everything after the in-flight save collapses into one pending payload.
    expect(mutationFn).toHaveBeenCalledTimes(1);

    await act(async () => {
      resolveFirst?.();
      await Promise.resolve();
    });

    await waitFor(() => expect(mutationFn).toHaveBeenCalledTimes(2));
    // Last writer wins for overlapping keys; the tail of the burst set n to 9
    // and toggle to false.
    expect(mutationFn).toHaveBeenNthCalledWith(2, { settings: { toggle: false, n: 9 } });
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

  it("skips dispatch when the optimistic end state is unchanged", async () => {
    const mutationFn = vi.fn().mockResolvedValue(undefined);
    const queryKey = ["settings", "test-noop"];
    queryClient.setQueryData(queryKey, { data: { settings: { foo: "bar" } } });

    const { result } = renderHook(
      () =>
        useAutosave<{ settings: { foo: string } }>({
          queryKey,
          mutationFn,
          applyOptimistic,
          debounceMs: 0,
        }),
      { wrapper: makeWrapper(queryClient) },
    );

    act(() => {
      result.current.save({ settings: { foo: "bar" } });
    });

    await act(async () => {
      await Promise.resolve();
    });

    expect(mutationFn).not.toHaveBeenCalled();
    expect(queryClient.getQueryData(queryKey)).toEqual({ data: { settings: { foo: "bar" } } });
  });

  it("does not queue a follow-up mutation when an in-flight save already matches the requested end state", async () => {
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

    const queryKey = ["settings", "test-inflight-noop"];
    queryClient.setQueryData(queryKey, { data: { settings: { default_agent_type: "codex" } } });

    const { result } = renderHook(
      () =>
        useAutosave<{ settings: { default_agent_type: string } }>({
          queryKey,
          mutationFn,
          applyOptimistic,
          debounceMs: 0,
        }),
      { wrapper: makeWrapper(queryClient) },
    );

    act(() => {
      result.current.save({ settings: { default_agent_type: "claude_code" } });
    });
    await waitFor(() => expect(mutationFn).toHaveBeenCalledTimes(1));

    act(() => {
      result.current.save({ settings: { default_agent_type: "claude_code" } });
    });

    await act(async () => {
      resolveFirst?.();
      await Promise.resolve();
    });

    expect(mutationFn).toHaveBeenCalledTimes(1);
    expect(queryClient.getQueryData(queryKey)).toEqual({
      data: { settings: { default_agent_type: "claude_code" } },
    });
  });

  it("transitions status idle → saving → saved → idle", async () => {
    // Fake timers let us jump past SAVED_LINGER_MS (1500ms) without burning
    // real wall-clock time. `shouldAdvanceTime` keeps waitFor's polling alive
    // by letting real time tick the fake clock forward automatically.
    vi.useFakeTimers({ shouldAdvanceTime: true });
    try {
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

      await act(async () => {
        await vi.advanceTimersByTimeAsync(1700);
      });
      expect(result.current.status).toBe("idle");
    } finally {
      vi.useRealTimers();
    }
  });

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

  it("scopes status transitions to the hook that originated the save", async () => {
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

    await waitFor(() => expect(resultA.current.status).toBe("saving"));
    expect(resultB.current.status).toBe("idle");

    await act(async () => {
      resolveMutation?.();
      await Promise.resolve();
    });

    await waitFor(() => expect(resultA.current.status).toBe("saved"));
    expect(resultB.current.status).toBe("idle");
  });

  it("flushes pending debounced payload on unmount so the edit isn't dropped", async () => {
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
    expect(mutationFn).not.toHaveBeenCalled();

    unmount();

    // Pending debounced payload is dispatched synchronously on unmount; the
    // mutation promise resolves asynchronously through the shared queue.
    await waitFor(() => expect(mutationFn).toHaveBeenCalledTimes(1));
    expect(mutationFn).toHaveBeenCalledWith({ settings: { x: 1 } });
  });

  it("queues pending debounced payload on unmount while a mutation is in flight", async () => {
    // Interleave: a first save is already in flight when the user types again
    // (new debounced payload) and then navigates away, unmounting the hook.
    // The unmount-flush must NOT drop the debounced payload — it should
    // dispatch into the shared queue, where it's held as pending behind the
    // in-flight mutation and drained once that resolves. This proves the
    // module-level queue survives the component teardown.
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

    const { result, unmount } = renderHook(
      () =>
        useAutosave<{ settings: Record<string, unknown> }>({
          queryKey: ["settings", "test-unmount-interleave"],
          mutationFn,
          applyOptimistic,
          coalesce,
          debounceMs: 200,
        }),
      { wrapper: makeWrapper(queryClient) },
    );

    // Kick off an immediate dispatch (flush fires without waiting for the
    // debounce) so the shared queue has a genuine in-flight mutation.
    act(() => {
      result.current.save({ settings: { first: 1 } });
      result.current.flush();
    });
    await waitFor(() => expect(mutationFn).toHaveBeenCalledTimes(1));
    expect(mutationFn).toHaveBeenNthCalledWith(1, { settings: { first: 1 } });

    // A second debounced save lands in the local timer — NOT yet dispatched.
    act(() => {
      result.current.save({ settings: { second: 2 } });
    });
    expect(mutationFn).toHaveBeenCalledTimes(1);

    // Unmount while the first mutation is still in flight and the debounce
    // timer is still pending. The unmount effect must flush the pending
    // payload into the shared queue.
    unmount();
    expect(mutationFn).toHaveBeenCalledTimes(1);

    // Release the in-flight mutation; the queue should drain the payload
    // that was parked during unmount.
    await act(async () => {
      resolveFirst?.();
      await Promise.resolve();
    });

    await waitFor(() => expect(mutationFn).toHaveBeenCalledTimes(2));
    expect(mutationFn).toHaveBeenNthCalledWith(2, { settings: { second: 2 } });
  });
});
