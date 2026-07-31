import { render, screen, within } from "@testing-library/react";
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
    expect(screen.queryByText("Expanded controls")).not.toBeInTheDocument();

    await user.click(trigger);

    expect(onOpenChange).toHaveBeenCalledWith(true);
    expect(
      screen.getByRole("button", { name: "Hide Advanced controls" }),
    ).toBeInTheDocument();
    expect(screen.getByText("Expanded controls")).toBeInTheDocument();
  });

  it("describes the trigger with the collapsed description and summary", () => {
    render(
      <DisclosureCard
        title="Advanced controls"
        description="Configure rarely changed behavior."
        summary="Three limits configured"
      >
        <p>Expanded controls</p>
      </DisclosureCard>,
    );

    const trigger = screen.getByRole("button", {
      name: "Show Advanced controls",
    });
    const describedBy = trigger.getAttribute("aria-describedby");
    expect(describedBy).toBeTruthy();

    const description = document.getElementById(describedBy as string);
    expect(description).toHaveTextContent("Configure rarely changed behavior.");
    expect(description).toHaveTextContent("Three limits configured");
  });

  it("renders status beside the trigger so live updates stay announceable", () => {
    render(
      <DisclosureCard
        title="Advanced controls"
        description="Configure rarely changed behavior."
        status={
          <span role="status" aria-live="polite">
            Saved
          </span>
        }
      >
        <p>Expanded controls</p>
      </DisclosureCard>,
    );

    const trigger = screen.getByRole("button", {
      name: "Show Advanced controls",
    });
    const status = screen.getByRole("status");

    expect(status).toHaveTextContent("Saved");
    expect(trigger).not.toContainElement(status);

    // Status chips reserve width even when idle, so the header stacks below
    // `sm` instead of squeezing the title column to nothing.
    expect(trigger.parentElement).toHaveClass("flex-col", "sm:flex-row");
  });

  it("reserves header height so a late-arriving summary does not shift layout", () => {
    const { rerender } = render(
      <DisclosureCard
        title="Advanced controls"
        description="Configure rarely changed behavior."
      >
        <p>Expanded controls</p>
      </DisclosureCard>,
    );

    expect(
      screen.getByRole("button", { name: "Show Advanced controls" }),
    ).toHaveClass("min-h-20");

    rerender(
      <DisclosureCard
        title="Advanced controls"
        description="Configure rarely changed behavior."
        summary="Three limits configured"
      >
        <p>Expanded controls</p>
      </DisclosureCard>,
    );

    expect(
      screen.getByRole("button", { name: "Show Advanced controls" }),
    ).toHaveClass("min-h-20");
  });

  it("honors a controlled open prop instead of internal state", async () => {
    const onOpenChange = vi.fn();
    const user = userEvent.setup();

    const { rerender } = render(
      <DisclosureCard
        title="Advanced controls"
        description="Configure rarely changed behavior."
        open={false}
        onOpenChange={onOpenChange}
      >
        <p>Expanded controls</p>
      </DisclosureCard>,
    );

    await user.click(
      screen.getByRole("button", { name: "Show Advanced controls" }),
    );

    expect(onOpenChange).toHaveBeenCalledWith(true);
    expect(screen.queryByText("Expanded controls")).not.toBeInTheDocument();

    rerender(
      <DisclosureCard
        title="Advanced controls"
        description="Configure rarely changed behavior."
        open
        onOpenChange={onOpenChange}
      >
        <p>Expanded controls</p>
      </DisclosureCard>,
    );

    expect(screen.getByText("Expanded controls")).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: "Hide Advanced controls" }),
    ).toBeInTheDocument();
  });

  it("renders an inset disclosure without standalone card gutters or a duplicate rule", async () => {
    const user = userEvent.setup();

    render(
      <div data-testid="host">
        <div className="border-b border-border/70">Preceding row</div>
        <DisclosureCard
          title="Nested controls"
          description="Controls related to the parent section."
          variant="inset"
        >
          <p>Nested content</p>
        </DisclosureCard>
      </div>,
    );

    const trigger = screen.getByRole("button", {
      name: "Show Nested controls",
    });
    expect(trigger).toHaveClass("px-0");

    const card = trigger.closest('[data-slot="card"]');
    expect(card).toHaveClass("rounded-none");
    // The preceding row already draws a bottom border; a top border here would
    // stack into a double rule.
    expect(card).not.toHaveClass("border-t");

    await user.click(trigger);
    expect(
      within(screen.getByTestId("host")).getByText("Nested content"),
    ).toBeInTheDocument();
  });
});
