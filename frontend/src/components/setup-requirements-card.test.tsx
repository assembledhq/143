import { describe, it, expect, vi } from "vitest";
import { renderWithProviders, screen } from "@/test/test-utils";
import { SetupRequirementsCard } from "./setup-requirements-card";

// The card is purely presentational; stub the rows so the test doesn't depend
// on NoReposWarning's network queries.
vi.mock("./no-repos-warning", () => ({
  NoReposWarning: () => <div data-testid="repo-row" />,
}));
vi.mock("./agent-key-required-banner", () => ({
  AgentKeyRequiredBanner: ({ agentType }: { agentType: string }) => (
    <div data-testid="agent-row">{agentType}</div>
  ),
}));

describe("SetupRequirementsCard", () => {
  it("renders nothing when no rows are needed", () => {
    const { container } = renderWithProviders(
      <SetupRequirementsCard showAgentRow={false} agentType="codex" showRepoRow={false} />,
    );
    expect(container).toBeEmptyDOMElement();
  });

  it("renders only the agent row when flagged", () => {
    renderWithProviders(
      <SetupRequirementsCard showAgentRow agentType="codex" showRepoRow={false} />,
    );
    expect(screen.getByTestId("setup-requirements-card")).toBeInTheDocument();
    expect(screen.getByTestId("agent-row")).toHaveTextContent("codex");
    expect(screen.queryByTestId("repo-row")).not.toBeInTheDocument();
  });

  it("renders only the repo row when flagged", () => {
    renderWithProviders(
      <SetupRequirementsCard showAgentRow={false} agentType="codex" showRepoRow />,
    );
    expect(screen.getByTestId("repo-row")).toBeInTheDocument();
    expect(screen.queryByTestId("agent-row")).not.toBeInTheDocument();
  });

  it("renders both rows in one card when both are flagged", () => {
    renderWithProviders(
      <SetupRequirementsCard showAgentRow agentType="claude_code" showRepoRow />,
    );
    expect(screen.getAllByTestId("setup-requirements-card")).toHaveLength(1);
    expect(screen.getByTestId("agent-row")).toHaveTextContent("claude_code");
    expect(screen.getByTestId("repo-row")).toBeInTheDocument();
  });
});
