import { describe, it, expect } from "vitest";
import { renderWithProviders, screen } from "@/test/test-utils";
import { AgentKeyRequiredBanner } from "./agent-key-required-banner";

describe("AgentKeyRequiredBanner", () => {
  it("links to /settings/account for provider-backed agents", () => {
    renderWithProviders(<AgentKeyRequiredBanner agentType="claude_code" />);

    const link = screen.getByRole("link", { name: "Configure keys" });
    expect(link).toHaveAttribute("href", "/settings/account");
    expect(screen.getByText(/Claude Code isn't connected yet/)).toBeInTheDocument();
  });

  it("links to /settings/account for Pi", () => {
    renderWithProviders(<AgentKeyRequiredBanner agentType="pi" />);

    const link = screen.getByRole("link", { name: "Configure keys" });
    expect(link).toHaveAttribute("href", "/settings/account");
    expect(screen.getByText(/Pi isn't connected yet/)).toBeInTheDocument();
  });

  it("falls back to the raw agent type for unknown keys", () => {
    renderWithProviders(<AgentKeyRequiredBanner agentType="mystery" />);

    expect(screen.getByText(/mystery isn't connected yet/)).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Configure keys" })).toHaveAttribute(
      "href",
      "/settings/agent",
    );
  });

  it("renders as a labeled setup row with asRow", () => {
    renderWithProviders(<AgentKeyRequiredBanner agentType="codex" asRow />);

    expect(screen.getByText("Coding agent")).toBeInTheDocument();
    expect(screen.getByText(/Codex isn't connected yet/)).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Configure keys" })).toHaveAttribute(
      "href",
      "/settings/agent",
    );
  });
});
