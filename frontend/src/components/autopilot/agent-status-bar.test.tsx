import { describe, it, expect, vi } from "vitest";
import { renderWithProviders, screen } from "@/test/test-utils";
import {
  AgentStatusBar,
  deriveAgentStatus,
  agentStatusLabel,
  agentStatusDotColors,
} from "./agent-status-bar";
import type { PMStatus } from "@/lib/types";

vi.mock("next/navigation", () => ({
  usePathname: () => "/autopilot",
  useRouter: () => ({
    push: vi.fn(),
    replace: vi.fn(),
  }),
}));

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

describe("AgentStatusBar", () => {
  it("renders the label", () => {
    renderWithProviders(
      <AgentStatusBar
        label="Autopilot"
        pmStatus={undefined}
        agentStatus="idle"
        isRunning={false}
      />
    );

    expect(screen.getByText("Autopilot")).toBeInTheDocument();
  });

  it.each([
    { status: "running" as const, expected: "Running" },
    { status: "completed" as const, expected: "Active" },
    { status: "failed" as const, expected: "Attention needed" },
    { status: "idle" as const, expected: "Idle" },
  ])("shows status label '$expected' for $status state", ({ status, expected }) => {
    renderWithProviders(
      <AgentStatusBar
        label="Autopilot"
        pmStatus={makePMStatus()}
        agentStatus={status}
        isRunning={status === "running"}
      />
    );

    expect(screen.getByText(expected)).toBeInTheDocument();
  });

  it('shows "Analyzing issues" text when running', () => {
    renderWithProviders(
      <AgentStatusBar
        label="Autopilot"
        pmStatus={makePMStatus({ is_running: true })}
        agentStatus="running"
        isRunning={true}
      />
    );

    expect(
      screen.getByText("Analyzing issues and generating a plan...")
    ).toBeInTheDocument();
  });

  it("shows success rate when pmStatus has delegated tasks", () => {
    renderWithProviders(
      <AgentStatusBar
        label="Autopilot"
        pmStatus={makePMStatus({
          total_delegated: 4,
          success_rate: 75,
          last_run_status: "completed",
        })}
        agentStatus="completed"
        isRunning={false}
      />
    );

    expect(screen.getByText("75% success")).toBeInTheDocument();
  });

  it('shows "reviewed" count', () => {
    renderWithProviders(
      <AgentStatusBar
        label="Autopilot"
        pmStatus={makePMStatus({
          issues_reviewed: 14,
          last_run_status: "completed",
        })}
        agentStatus="completed"
        isRunning={false}
      />
    );

    expect(screen.getByText("14 reviewed")).toBeInTheDocument();
  });

  it("shows last run time", () => {
    const twoHoursAgo = new Date(Date.now() - 2 * 60 * 60 * 1000).toISOString();
    renderWithProviders(
      <AgentStatusBar
        label="Autopilot"
        pmStatus={makePMStatus({
          last_run_at: twoHoursAgo,
          last_run_status: "completed",
        })}
        agentStatus="completed"
        isRunning={false}
      />
    );

    expect(screen.getByText("2h ago")).toBeInTheDocument();
  });

  it("renders children in the actions area", () => {
    renderWithProviders(
      <AgentStatusBar
        label="Autopilot"
        pmStatus={undefined}
        agentStatus="idle"
        isRunning={false}
      >
        <button>Run now</button>
      </AgentStatusBar>
    );

    expect(
      screen.getByRole("button", { name: "Run now" })
    ).toBeInTheDocument();
  });
});
