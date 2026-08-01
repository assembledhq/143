import { describe, expect, it } from "vitest";

import { renderWithProviders, screen } from "@/test/test-utils";

import { controlDensities } from "./control-sizing";
import { ControlGallery } from "./control-gallery";

describe("ControlGallery", () => {
  it("renders every control family at every supported density", () => {
    renderWithProviders(<ControlGallery />);

    for (const density of controlDensities) {
      const label = density[0].toUpperCase() + density.slice(1);
      expect(screen.getByRole("textbox", { name: `${label} text input` })).toHaveAttribute(
        "data-density",
        density,
      );
      expect(screen.getByRole("combobox", { name: `${label} select` })).toHaveAttribute(
        "data-density",
        density,
      );
      expect(screen.getByRole("combobox", { name: `${label} custom picker` })).toHaveAttribute(
        "data-density",
        density,
      );
    }

    expect(document.querySelectorAll('[data-slot="command-input"][data-density]')).toHaveLength(
      controlDensities.length,
    );
  });
});
