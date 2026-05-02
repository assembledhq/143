import { describe, expect, it } from "vitest";
import {
  shouldSubscribeToEvalBatchStream,
  shouldSubscribeToEvalBootstrapStream,
} from "./use-eval-sse";

describe("eval SSE stream subscription gating", () => {
  it("keeps batch streams only for active batch statuses", () => {
    expect(shouldSubscribeToEvalBatchStream("pending")).toBe(true);
    expect(shouldSubscribeToEvalBatchStream("running")).toBe(true);
    expect(shouldSubscribeToEvalBatchStream("completed")).toBe(false);
    expect(shouldSubscribeToEvalBatchStream("failed")).toBe(false);
    expect(shouldSubscribeToEvalBatchStream(undefined)).toBe(false);
  });

  it("keeps bootstrap streams only for active bootstrap statuses", () => {
    expect(shouldSubscribeToEvalBootstrapStream("pending")).toBe(true);
    expect(shouldSubscribeToEvalBootstrapStream("running")).toBe(true);
    expect(shouldSubscribeToEvalBootstrapStream("completed")).toBe(false);
    expect(shouldSubscribeToEvalBootstrapStream("failed")).toBe(false);
    expect(shouldSubscribeToEvalBootstrapStream(undefined)).toBe(false);
  });
});
