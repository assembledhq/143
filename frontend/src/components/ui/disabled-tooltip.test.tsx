import { afterEach, describe, expect, it, vi } from "vitest";
import { Button } from "@/components/ui/button";
import { DisabledTooltip } from "@/components/ui/disabled-tooltip";
import { renderWithProviders, screen, userEvent } from "@/test/test-utils";

describe("DisabledTooltip", () => {
  afterEach(() => {
    vi.restoreAllMocks();
  });

  it("stays controlled when its disabled reason resolves asynchronously", () => {
    const consoleError = vi.spyOn(console, "error").mockImplementation(() => undefined);
    const { rerender } = renderWithProviders(
      <DisabledTooltip disabled content="Checking GitHub connection">
        <Button disabled>Set up reviewer</Button>
      </DisabledTooltip>,
    );

    rerender(
      <DisabledTooltip disabled={false} content={undefined}>
        <Button>Set up reviewer</Button>
      </DisabledTooltip>,
    );

    expect(consoleError.mock.calls.flat().join(" ")).not.toContain("changing from uncontrolled to controlled");
    expect(consoleError.mock.calls.flat().join(" ")).not.toContain("changing from controlled to uncontrolled");
  });

  it("shows the reason while the wrapped action is disabled", async () => {
    const user = userEvent.setup();
    renderWithProviders(
      <DisabledTooltip disabled content="Connect GitHub first">
        <Button disabled>Set up reviewer</Button>
      </DisabledTooltip>,
    );

    await user.hover(screen.getByRole("button", { name: "Set up reviewer" }).parentElement!);

    expect(await screen.findByRole("tooltip")).toHaveTextContent("Connect GitHub first");
  });
});
