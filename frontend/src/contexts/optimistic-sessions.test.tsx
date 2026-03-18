import { describe, expect, it } from "vitest";
import { renderHook, act } from "@testing-library/react";
import {
  OptimisticSessionsProvider,
  useOptimisticSessions,
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

  it("throws when used outside the provider", () => {
    expect(() => {
      renderHook(() => useOptimisticSessions());
    }).toThrow("useOptimisticSessions must be used within an OptimisticSessionsProvider");
  });
});
