import { describe, it, expect } from "vitest";
import { renderWithProviders, screen } from "@/test/test-utils";
import { AgentKeyRequiredBanner } from "./agent-key-required-banner";

describe("AgentKeyRequiredBanner", () => {
  it("links to /settings/agent for provider-backed agents", () => {
    renderWithProviders(<AgentKeyRequiredBanner agentType="claude_code" />);

    const link = screen.getByRole("link", { name: "Configure keys" });
    expect(link).toHaveAttribute("href", "/settings/agent");
    expect(screen.getByText(/No API key configured for Claude Code/)).toBeInTheDocument();
  });

  it("links to /settings/account for inherited agents like Pi", () => {
    renderWithProviders(<AgentKeyRequiredBanner agentType="pi" />);

    const link = screen.getByRole("link", { name: "Configure keys" });
    expect(link).toHaveAttribute("href", "/settings/account");
    expect(
      screen.getByText(/Pi needs an Anthropic, OpenAI, or Gemini key to run/),
    ).toBeInTheDocument();
  });

  it("falls back to the raw agent type for unknown keys", () => {
    renderWithProviders(<AgentKeyRequiredBanner agentType="mystery" />);

    expect(screen.getByText(/No API key configured for mystery/)).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Configure keys" })).toHaveAttribute(
      "href",
      "/settings/agent",
    );
  });
});
