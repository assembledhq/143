import { describe, it, expect } from "vitest";
import { deriveAgentStatus, agentStatusLabel } from "./agent-status-bar";
import type { PMStatus } from "@/lib/types";

function makePMStatus(overrides: Partial<PMStatus> = {}): PMStatus {
  return {
    is_running: false,
    issues_reviewed: 0,
    success_rate: 0,
    success_count: 0,
    total_delegated: 0,
    ...overrides,
  };
}

describe("deriveAgentStatus", () => {
  it("returns 'running' when isAnalyzing is true", () => {
    expect(deriveAgentStatus(undefined, true)).toBe("running");
    expect(deriveAgentStatus(makePMStatus(), true)).toBe("running");
  });

  it("returns 'running' when pm is_running", () => {
    expect(deriveAgentStatus(makePMStatus({ is_running: true }), false)).toBe("running");
  });

  it("returns 'failed' when last_error exists", () => {
    expect(deriveAgentStatus(makePMStatus({ last_error: "something broke" }), false)).toBe("failed");
  });

  it("returns 'idle' when no last_run_status", () => {
    expect(deriveAgentStatus(makePMStatus(), false)).toBe("idle");
  });

  it("returns 'completed' for completed last_run_status", () => {
    expect(deriveAgentStatus(makePMStatus({ last_run_status: "completed" }), false)).toBe("completed");
  });

  it("returns 'completed' for executing last_run_status", () => {
    expect(deriveAgentStatus(makePMStatus({ last_run_status: "executing" }), false)).toBe("completed");
  });

  it("returns 'failed' for failed last_run_status", () => {
    expect(deriveAgentStatus(makePMStatus({ last_run_status: "failed" }), false)).toBe("failed");
  });

  it("returns 'idle' when pmStatus is undefined", () => {
    expect(deriveAgentStatus(undefined, false)).toBe("idle");
  });

  it("prioritizes isAnalyzing over last_error", () => {
    expect(deriveAgentStatus(makePMStatus({ last_error: "error" }), true)).toBe("running");
  });

  it("prioritizes is_running over last_error", () => {
    expect(deriveAgentStatus(makePMStatus({ is_running: true, last_error: "error" }), false)).toBe("running");
  });
});

describe("agentStatusLabel", () => {
  it("maps each status to the expected label", () => {
    expect(agentStatusLabel("running")).toBe("Running");
    expect(agentStatusLabel("completed")).toBe("Active");
    expect(agentStatusLabel("failed")).toBe("Attention needed");
    expect(agentStatusLabel("idle")).toBe("Idle");
  });
});
