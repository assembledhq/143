import { useEffect, useRef } from "react";

import type { SessionPublishState } from "@/lib/types";

// Optimistic client phase for a PR-level lifecycle action (create PR, push
// changes, create branch). It bridges the gap between the user's click and the
// server reflecting the action as in flight:
//   - "submitting": request sent, server hasn't acknowledged (202) yet
//   - "queued":     server acknowledged; worker is (or will be) running it
//   - "idle":       no action in flight, or the server has settled
export type OptimisticActionPhase = "idle" | "submitting" | "queued";

type LifecycleServerState = SessionPublishState | null | undefined;

function isInFlight(state: LifecycleServerState): boolean {
  return state === "queued" || state === "pushing";
}

function isTerminal(state: LifecycleServerState): boolean {
  return state === "failed" || state === "succeeded";
}

export interface ReconcileOptimisticActionParams {
  /** The current optimistic client phase. */
  phase: OptimisticActionPhase;
  /** Authoritative server lifecycle column for this action. */
  serverState: LifecycleServerState;
  /**
   * Clears the optimistic phase (e.g. setLocalPushState("idle")). Should be a
   * stable reference (useCallback) so the reconcile effect doesn't re-run every
   * render.
   */
  onResolved: () => void;
}

/**
 * Reconciles a client-side optimistic action phase against the authoritative
 * server lifecycle column (pr_creation_state / pr_push_state /
 * branch_creation_state), clearing the optimistic phase once the server
 * settles.
 *
 * The reconciliation is *level-triggered*: it clears the optimistic phase
 * whenever the server row reaches a terminal state (after the action was
 * observed in flight), rather than requiring the client to witness the exact
 * `queued -> terminal` transition edge. A missed SSE event or a paused
 * background poll therefore cannot strand the button on a spinner — the next
 * authoritative read of a terminal state resolves it.
 *
 * The `seenInFlight` gate prevents a stale *prior* terminal state from
 * instantly clearing a fresh optimistic phase: when the user clicks Retry on a
 * previously-failed action, the cached server state may still read "failed"
 * until the next fetch. We only honor a terminal state once we've observed the
 * server actually running our action (its state went in-flight). Callers should
 * optimistically mark the cached server state in-flight on ack so this arms
 * deterministically rather than depending on a poll landing.
 */
export function useReconcileOptimisticAction({
  phase,
  serverState,
  onResolved,
}: ReconcileOptimisticActionParams): void {
  const seenInFlightRef = useRef(false);

  // Disarm whenever the optimistic flow ends so the next submission starts
  // fresh and a stale terminal state can't resolve it prematurely.
  useEffect(() => {
    if (phase === "idle") {
      seenInFlightRef.current = false;
    }
  }, [phase]);

  useEffect(() => {
    if (phase === "idle") return;
    if (isInFlight(serverState)) {
      seenInFlightRef.current = true;
      return;
    }
    if (isTerminal(serverState) && seenInFlightRef.current) {
      seenInFlightRef.current = false;
      onResolved();
    }
  }, [serverState, phase, onResolved]);
}
