import { describe, expect, it } from "vitest";

import { renderWithProviders } from "@/test/test-utils";
import { AnimatedEllipsis } from "./animated-ellipsis";

describe("AnimatedEllipsis", () => {
  it("renders three hidden animation dots with custom classes", () => {
    const { container } = renderWithProviders(<AnimatedEllipsis className="text-muted" />);

    const wrapper = container.firstElementChild;
    expect(wrapper).toHaveAttribute("aria-hidden", "true");
    expect(wrapper).toHaveClass("text-muted");
    expect(container.querySelectorAll(".ellipsis-dot")).toHaveLength(3);
    expect(container).toHaveTextContent("...");
  });
});
