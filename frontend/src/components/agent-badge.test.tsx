import { describe, expect, it } from "vitest";

import { renderWithProviders, screen } from "@/test/test-utils";
import { AgentBadge } from "./agent-badge";

describe("AgentBadge", () => {
  it("renders a known agent icon and label", () => {
    const { container } = renderWithProviders(<AgentBadge agentType="codex" />);

    expect(screen.getByText("Codex")).toBeInTheDocument();
    const icon = container.querySelector('[aria-hidden="true"]');
    expect(icon).toHaveStyle({
      maskImage: "url(/agents/codex.svg)",
    });
  });

  it("can hide the label for compact surfaces", () => {
    renderWithProviders(<AgentBadge agentType="codex" hideLabel />);

    expect(screen.queryByText("Codex")).not.toBeInTheDocument();
  });

  it("falls back to a monogram for known agents without a brand icon", () => {
    renderWithProviders(<AgentBadge agentType="pi" />);

    expect(screen.getByText("Pi")).toBeInTheDocument();
    expect(screen.getByText("PI")).toBeInTheDocument();
  });

  it("falls back to display labels or raw agent type for unknown agents", () => {
    const { rerender } = renderWithProviders(<AgentBadge agentType="pm_agent" />);

    expect(screen.getByText("PM Agent")).toBeInTheDocument();

    rerender(<AgentBadge agentType="custom_agent" />);
    expect(screen.getByText("custom_agent")).toBeInTheDocument();
  });
});
