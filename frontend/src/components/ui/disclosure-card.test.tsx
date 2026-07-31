import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { DisclosureCard } from "@/components/ui/disclosure-card";

describe("DisclosureCard", () => {
  it("reveals its content and reports state changes", async () => {
    const onOpenChange = vi.fn();
    const user = userEvent.setup();

    render(
      <DisclosureCard
        title="Advanced controls"
        description="Configure rarely changed behavior."
        summary="Three limits configured"
        status={<span>Saved</span>}
        onOpenChange={onOpenChange}
        actionTestId="disclosure-action"
      >
        <p>Expanded controls</p>
      </DisclosureCard>,
    );

    const trigger = screen.getByRole("button", {
      name: "Show Advanced controls",
    });
    expect(trigger).toHaveTextContent("Advanced controls");
    expect(trigger).toHaveTextContent("Three limits configured");
    expect(trigger).toHaveTextContent("Saved");
    expect(screen.getByTestId("disclosure-action")).not.toHaveTextContent(
      "Saved",
    );
    expect(screen.queryByText("Expanded controls")).not.toBeInTheDocument();

    await user.click(trigger);

    expect(onOpenChange).toHaveBeenCalledWith(true);
    expect(
      screen.getByRole("button", { name: "Hide Advanced controls" }),
    ).toBeInTheDocument();
    expect(screen.getByText("Expanded controls")).toBeInTheDocument();
  });

  it("renders an inset disclosure without standalone card gutters", () => {
    render(
      <DisclosureCard
        title="Nested controls"
        description="Controls related to the parent section."
        variant="inset"
      >
        <p>Nested content</p>
      </DisclosureCard>,
    );

    const trigger = screen.getByRole("button", {
      name: "Show Nested controls",
    });
    expect(trigger).toHaveClass("px-0");
    expect(trigger.closest('[data-slot="card"]')).toHaveClass(
      "border-t",
      "rounded-none",
    );
  });
});
