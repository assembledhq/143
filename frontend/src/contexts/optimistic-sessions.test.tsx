import { describe, expect, it } from "vitest";
import { renderHook, act } from "@testing-library/react";
import {
  OptimisticSessionsProvider,
  useOptimisticSessions,
  useOptimisticSessionsSafe,
} from "./optimistic-sessions";

function wrapper({ children }: { children: React.ReactNode }) {
  return <OptimisticSessionsProvider>{children}</OptimisticSessionsProvider>;
}

describe("OptimisticSessionsProvider", () => {
  it("starts with an empty list", () => {
    const { result } = renderHook(() => useOptimisticSessions(), { wrapper });
    expect(result.current.optimisticSessions).toEqual([]);
  });

  it("adds an optimistic session and returns its id", () => {
    const { result } = renderHook(() => useOptimisticSessions(), { wrapper });

    let id: string;
    act(() => {
      id = result.current.addOptimisticSession("Fix bug");
    });

    expect(id!).toMatch(/^optimistic-/);
    expect(result.current.optimisticSessions).toHaveLength(1);
    expect(result.current.optimisticSessions[0]).toMatchObject({
      id: id!,
      title: "Fix bug",
      status: "pending",
    });
    expect(result.current.optimisticSessions[0].created_at).toBeTruthy();
  });

  it("prepends new sessions (newest first)", () => {
    const { result } = renderHook(() => useOptimisticSessions(), { wrapper });

    act(() => {
      result.current.addOptimisticSession("First");
      result.current.addOptimisticSession("Second");
    });

    expect(result.current.optimisticSessions).toHaveLength(2);
    expect(result.current.optimisticSessions[0].title).toBe("Second");
    expect(result.current.optimisticSessions[1].title).toBe("First");
  });

  it("removes a session by id", () => {
    const { result } = renderHook(() => useOptimisticSessions(), { wrapper });

    let id: string;
    act(() => {
      id = result.current.addOptimisticSession("To remove");
      result.current.addOptimisticSession("To keep");
    });

    act(() => {
      result.current.removeOptimisticSession(id!);
    });

    expect(result.current.optimisticSessions).toHaveLength(1);
    expect(result.current.optimisticSessions[0].title).toBe("To keep");
  });

  it("does nothing when removing a non-existent id", () => {
    const { result } = renderHook(() => useOptimisticSessions(), { wrapper });

    act(() => {
      result.current.addOptimisticSession("Stay");
    });

    act(() => {
      result.current.removeOptimisticSession("does-not-exist");
    });

    expect(result.current.optimisticSessions).toHaveLength(1);
  });

  it("markOptimisticResolved stamps the real session id onto a placeholder", () => {
    const { result } = renderHook(() => useOptimisticSessions(), { wrapper });

    let id: string;
    act(() => {
      id = result.current.addOptimisticSession("Pending");
    });

    act(() => {
      result.current.markOptimisticResolved(id!, "real-123");
    });

    expect(result.current.optimisticSessions).toHaveLength(1);
    expect(result.current.optimisticSessions[0].resolvedId).toBe("real-123");
  });

  it("markOptimisticResolved is a no-op for unknown ids", () => {
    const { result } = renderHook(() => useOptimisticSessions(), { wrapper });

    act(() => {
      result.current.addOptimisticSession("Only one");
    });

    act(() => {
      result.current.markOptimisticResolved("does-not-exist", "real-xyz");
    });

    expect(result.current.optimisticSessions).toHaveLength(1);
    expect(result.current.optimisticSessions[0].resolvedId).toBeUndefined();
  });

  it("throws when used outside the provider", () => {
    expect(() => {
      renderHook(() => useOptimisticSessions());
    }).toThrow("useOptimisticSessions must be used within an OptimisticSessionsProvider");
  });
});

describe("useOptimisticSessionsSafe", () => {
  it("returns no-op stubs when used outside the provider", () => {
    const { result } = renderHook(() => useOptimisticSessionsSafe());

    expect(result.current.optimisticSessions).toEqual([]);
    expect(typeof result.current.addOptimisticSession).toBe("function");
    expect(typeof result.current.removeOptimisticSession).toBe("function");
  });

  it("returns a temporary id from addOptimisticSession stub", () => {
    const { result } = renderHook(() => useOptimisticSessionsSafe());

    let id: string;
    act(() => {
      id = result.current.addOptimisticSession("test");
    });

    expect(id!).toMatch(/^optimistic-/);
    // The stub does not actually store sessions
    expect(result.current.optimisticSessions).toEqual([]);
  });

  it("removeOptimisticSession stub does not throw", () => {
    const { result } = renderHook(() => useOptimisticSessionsSafe());

    act(() => {
      result.current.removeOptimisticSession("nonexistent");
    });

    expect(result.current.optimisticSessions).toEqual([]);
  });

  it("delegates to real provider when inside one", () => {
    const { result } = renderHook(() => useOptimisticSessionsSafe(), { wrapper });

    let id: string;
    act(() => {
      id = result.current.addOptimisticSession("Real session");
    });

    expect(result.current.optimisticSessions).toHaveLength(1);
    expect(result.current.optimisticSessions[0]).toMatchObject({
      id: id!,
      title: "Real session",
      status: "pending",
    });
  });
});
