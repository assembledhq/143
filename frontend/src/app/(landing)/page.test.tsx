import { render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

import LandingPage from "./page";

vi.mock("@/hooks/use-prefers-dark", () => ({
  usePrefersDark: () => false,
}));

vi.mock("@/components/landing/hero-canvas", () => ({
  default: () => <canvas data-testid="hero-canvas" />,
  DARK: { bg: "#11110f" },
  LIGHT: { bg: "#f6f5f0" },
}));

describe("LandingPage", () => {
  it("preserves the existing homepage story and plane-led hero", () => {
    render(<LandingPage />);

    expect(screen.getByTestId("hero-canvas")).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Where your whole team builds software together" })).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Run any coding agent." })).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Connect your engineering tools." })).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Put your agents to work." })).toBeInTheDocument();
    expect(screen.queryByText(/mission control/i)).not.toBeInTheDocument();
  });
});
