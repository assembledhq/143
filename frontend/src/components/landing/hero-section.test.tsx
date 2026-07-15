import { render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import HeroSection from "./hero-section";

vi.mock("./hero-canvas", () => ({
  default: () => <div data-testid="hero-canvas" />,
  DARK: { bg: "#08080f" },
  LIGHT: { bg: "#FAFAFB" },
}));

describe("HeroSection", () => {
  it("links to the public docs from the homepage navigation", () => {
    render(<HeroSection isDark={false} />);

    const docsLink = screen.getByRole("link", { name: "Docs" });

    expect(docsLink).toHaveAttribute("href", "/docs");
  });

  it("keeps signup as the homepage CTA instead of linking to the demo", () => {
    render(<HeroSection isDark={false} />);

    expect(screen.queryByRole("link", { name: /try demo/i })).not.toBeInTheDocument();
    expect(screen.getByRole("link", { name: /get started/i })).toHaveAttribute(
      "href",
      "/login?tab=signup",
    );
  });

  it("preserves the plane-led homepage content while applying the flight-blue visual system", () => {
    render(<HeroSection isDark={false} />);

    expect(screen.getByTestId("hero-canvas")).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Where your whole team builds software together" })).toBeInTheDocument();
    expect(screen.getByText(/Run Codex, Claude Code, and OpenCode in an open-source cloud/)).toBeInTheDocument();
    expect(screen.getByRole("link", { name: /get started/i })).toHaveClass("bg-[#315ce8]");
    expect(screen.queryByText(/mission control/i)).not.toBeInTheDocument();
  });
});
