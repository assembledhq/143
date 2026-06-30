import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { AlphaBadge, BetaBadge } from "./release-stage-badge";

describe("release stage badges", () => {
  it("renders reusable alpha and beta badges", () => {
    render(
      <div>
        <AlphaBadge />
        <BetaBadge />
      </div>,
    );

    expect(screen.getByText("Alpha")).toHaveAttribute("data-release-stage", "alpha");
    expect(screen.getByText("Beta")).toHaveAttribute("data-release-stage", "beta");
  });

  it("supports decorative badges without contributing label text", () => {
    const { container } = render(
      <label>
        <input type="radio" />
        Amp
        <BetaBadge decorative />
      </label>,
    );

    const badge = container.querySelector("[data-release-stage='beta']");
    expect(badge).toHaveAttribute("aria-hidden", "true");
    expect(badge).toHaveAttribute("data-badge", "Beta");
    expect(screen.getByLabelText("Amp")).toBeInTheDocument();
  });
});
