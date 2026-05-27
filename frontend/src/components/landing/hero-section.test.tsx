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
});
