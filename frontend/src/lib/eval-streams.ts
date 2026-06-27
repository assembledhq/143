import type { EvalBatchStatus, EvalBootstrapStatus } from "./types";

// Eval-specific gating for the shared SSE hook (useResourceSSE): only keep a
// stream open while the batch/bootstrap run is still in flight. Terminal runs
// emit no further events, so the calling page drops to its polling cadence.

export function shouldSubscribeToEvalBatchStream(status: EvalBatchStatus | undefined): boolean {
  return status === "pending" || status === "running";
}

export function shouldSubscribeToEvalBootstrapStream(status: EvalBootstrapStatus | undefined): boolean {
  return status === "pending" || status === "running";
}
