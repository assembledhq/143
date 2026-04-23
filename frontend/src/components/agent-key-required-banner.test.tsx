import { describe, it, expect } from "vitest";
import { renderWithProviders, screen } from "@/test/test-utils";
import { AgentKeyRequiredBanner } from "./agent-key-required-banner";

describe("AgentKeyRequiredBanner", () => {
  it("links to /settings/agent for provider-backed agents", () => {
    renderWithProviders(<AgentKeyRequiredBanner agentType="claude_code" />);

    const link = screen.getByRole("link", { name: "Configure keys" });
    expect(link).toHaveAttribute("href", "/settings/account");
    expect(screen.getByText(/No API key configured for Claude Code/)).toBeInTheDocument();
  });

  it("links to /settings/account for Pi", () => {
    renderWithProviders(<AgentKeyRequiredBanner agentType="pi" />);

    const link = screen.getByRole("link", { name: "Configure keys" });
    expect(link).toHaveAttribute("href", "/settings/account");
    expect(screen.getByText(/No API key configured for Pi/)).toBeInTheDocument();
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
