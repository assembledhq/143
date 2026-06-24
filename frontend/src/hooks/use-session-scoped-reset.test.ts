import { renderHook } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { useSessionScopedReset } from "./use-session-scoped-reset";

describe("useSessionScopedReset", () => {
  it("runs named reset groups only when the session id changes", () => {
    const resetChrome = vi.fn();
    const resetComposer = vi.fn();

    const { rerender } = renderHook(
      ({ sessionId }) =>
        useSessionScopedReset(sessionId, [
          { name: "chrome", reset: resetChrome },
          { name: "composer", reset: resetComposer },
        ]),
      { initialProps: { sessionId: "session-1" } },
    );

    expect(resetChrome).not.toHaveBeenCalled();
    expect(resetComposer).not.toHaveBeenCalled();

    rerender({ sessionId: "session-1" });

    expect(resetChrome).not.toHaveBeenCalled();
    expect(resetComposer).not.toHaveBeenCalled();

    rerender({ sessionId: "session-2" });

    expect(resetChrome).toHaveBeenCalledTimes(1);
    expect(resetComposer).toHaveBeenCalledTimes(1);
  });

  it("uses the latest reset callback for a group after rerender", () => {
    const staleReset = vi.fn();
    const latestReset = vi.fn();
    let reset = staleReset;

    const { rerender } = renderHook(
      ({ sessionId }) =>
        useSessionScopedReset(sessionId, [
          { name: "composer", reset },
        ]),
      { initialProps: { sessionId: "session-1" } },
    );

    reset = latestReset;
    rerender({ sessionId: "session-1" });
    rerender({ sessionId: "session-2" });

    expect(staleReset).not.toHaveBeenCalled();
    expect(latestReset).toHaveBeenCalledTimes(1);
  });
});
