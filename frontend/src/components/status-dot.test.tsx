import { describe, expect, it } from "vitest";

import { renderWithProviders } from "@/test/test-utils";
import { StatusDot } from "./status-dot";

describe("StatusDot", () => {
  it("renders a static dot with the requested color and wrapper class", () => {
    const { container } = renderWithProviders(
      <StatusDot color="bg-success" className="custom-dot" />,
    );

    const dot = container.firstElementChild;
    expect(dot).toHaveClass("bg-success");
    expect(dot).toHaveClass("custom-dot");
    expect(dot).not.toHaveClass("relative");
  });

  it("renders an animated dot with halo and shimmer layers", () => {
    const { container } = renderWithProviders(
      <StatusDot animate color="bg-primary" pingColor="bg-primary/40" />,
    );

    const wrapper = container.firstElementChild;
    expect(wrapper).toHaveClass("relative");
    expect(container.querySelector(".ai-pulse-halo")).toHaveClass("bg-primary/40");
    expect(container.querySelector(".ai-shimmer")).toBeInTheDocument();
  });
});
