import { renderHook } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

import { useReconcileOptimisticAction, type OptimisticActionPhase } from "./use-optimistic-pr-action";
import type { SessionPublishState } from "@/lib/types";

type ServerState = SessionPublishState | null | undefined;

function setup(initial: { phase: OptimisticActionPhase; serverState: ServerState }) {
  const onResolved = vi.fn();
  const view = renderHook(
    ({ phase, serverState }) => useReconcileOptimisticAction({ phase, serverState, onResolved }),
    { initialProps: initial },
  );
  return { ...view, onResolved };
}

describe("useReconcileOptimisticAction", () => {
  it("clears once the server settles after being observed in flight", () => {
    const { rerender, onResolved } = setup({ phase: "submitting", serverState: "idle" });

    rerender({ phase: "queued", serverState: "pushing" }); // arms the gate
    expect(onResolved).not.toHaveBeenCalled();

    rerender({ phase: "queued", serverState: "failed" }); // terminal -> resolve
    expect(onResolved).toHaveBeenCalledTimes(1);
  });

  it("clears even if the in-flight edge was never observed, once armed (missed event)", () => {
    const { rerender, onResolved } = setup({ phase: "submitting", serverState: "idle" });

    // Caller optimistically marks the cached server state in flight on ack.
    rerender({ phase: "queued", serverState: "queued" }); // arms
    // Jump straight to terminal (SSE missed + single delayed poll).
    rerender({ phase: "queued", serverState: "failed" });
    expect(onResolved).toHaveBeenCalledTimes(1);
  });

  it("does NOT clear a fresh submission against a stale prior terminal state", () => {
    // Retry of a previously-failed action: cached state still reads "failed".
    const { rerender, onResolved } = setup({ phase: "idle", serverState: "failed" });

    rerender({ phase: "submitting", serverState: "failed" });
    expect(onResolved).not.toHaveBeenCalled();

    rerender({ phase: "queued", serverState: "failed" }); // not yet observed in flight
    expect(onResolved).not.toHaveBeenCalled();

    rerender({ phase: "queued", serverState: "pushing" }); // now armed
    rerender({ phase: "queued", serverState: "failed" });
    expect(onResolved).toHaveBeenCalledTimes(1);
  });

  it("never resolves while idle (fresh load of a settled session)", () => {
    const { rerender, onResolved } = setup({ phase: "idle", serverState: "failed" });
    rerender({ phase: "idle", serverState: "failed" });
    rerender({ phase: "idle", serverState: "succeeded" });
    expect(onResolved).not.toHaveBeenCalled();
  });

  it("re-arms fresh for each submission after returning to idle", () => {
    const { rerender, onResolved } = setup({ phase: "submitting", serverState: "idle" });
    rerender({ phase: "queued", serverState: "pushing" });
    rerender({ phase: "queued", serverState: "succeeded" });
    expect(onResolved).toHaveBeenCalledTimes(1);

    // Caller cleared to idle; the gate disarms. A new submission must observe
    // in-flight again before a terminal state can resolve it.
    rerender({ phase: "idle", serverState: "idle" });
    rerender({ phase: "submitting", serverState: "idle" });
    rerender({ phase: "queued", serverState: "failed" }); // stale-ish, not armed
    expect(onResolved).toHaveBeenCalledTimes(1);

    rerender({ phase: "queued", serverState: "pushing" });
    rerender({ phase: "queued", serverState: "failed" });
    expect(onResolved).toHaveBeenCalledTimes(2);
  });
});
