import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import { RadioGroup } from "@/components/ui/radio-group";
import { RadioCard } from "./radio-card";

describe("RadioCard", () => {
  it("uses the selected treatment for the active option", () => {
    render(
      <RadioGroup value="selected">
        <RadioCard value="selected" label="Selected option" selected />
      </RadioGroup>,
    );

    expect(screen.getByText("Selected option").closest("label")).toHaveClass(
      "border-primary/50",
      "bg-accent/55",
      "ring-1",
    );
  });

  it("removes the interactive affordance when disabled", () => {
    render(
      <RadioGroup value="disabled">
        <RadioCard value="disabled" label="Disabled option" selected disabled />
      </RadioGroup>,
    );

    expect(screen.getByRole("radio", { name: "Disabled option" })).toBeDisabled();
    expect(screen.getByText("Disabled option").closest("label")).toHaveClass(
      "cursor-not-allowed",
      "opacity-50",
    );
  });
});
