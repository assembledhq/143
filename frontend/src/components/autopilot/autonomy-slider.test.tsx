import { describe, it, expect, vi } from "vitest";
import { renderWithProviders, screen, userEvent } from "@/test/test-utils";
import { AutonomySlider } from "./autonomy-slider";

vi.mock("next/navigation", () => ({
  usePathname: () => "/autopilot",
  useRouter: () => ({
    push: vi.fn(),
    replace: vi.fn(),
  }),
}));

describe("AutonomySlider", () => {
  it("renders all three options", () => {
    const onChange = vi.fn();

    renderWithProviders(
      <AutonomySlider value="manual" onChange={onChange} />
    );

    expect(screen.getByText("Suggest")).toBeInTheDocument();
    expect(screen.getByText("PM recommends, you decide")).toBeInTheDocument();

    expect(screen.getByText("Act on low-risk work")).toBeInTheDocument();
    expect(
      screen.getByText("PM auto-creates sessions for bounded work")
    ).toBeInTheDocument();

    expect(screen.getByText("Operate broadly")).toBeInTheDocument();
    expect(
      screen.getByText(
        "PM acts automatically on most policy-compliant work"
      )
    ).toBeInTheDocument();
  });

  it("calls onChange with correct value when clicked", async () => {
    const user = userEvent.setup();
    const onChange = vi.fn();

    renderWithProviders(
      <AutonomySlider value="manual" onChange={onChange} />
    );

    await user.click(screen.getByText("Act on low-risk work"));
    expect(onChange).toHaveBeenCalledWith("auto_simple");

    await user.click(screen.getByText("Operate broadly"));
    expect(onChange).toHaveBeenCalledWith("auto_all");

    await user.click(screen.getByText("Suggest"));
    expect(onChange).toHaveBeenCalledWith("manual");
  });

  it("highlights the selected level", () => {
    const onChange = vi.fn();

    const { rerender } = renderWithProviders(
      <AutonomySlider value="auto_simple" onChange={onChange} />
    );

    // The selected button should have the primary text color class
    const activeButton = screen.getByText("Act on low-risk work").closest("button")!;
    expect(activeButton).toHaveClass("bg-surface-selected");

    // Non-selected buttons should not have the active background
    const inactiveButton = screen.getByText("Suggest").closest("button")!;
    expect(inactiveButton).not.toHaveClass("bg-surface-selected");

    // Re-render with a different value and verify highlight moves
    rerender(<AutonomySlider value="auto_all" onChange={onChange} />);

    const newActiveButton = screen.getByText("Operate broadly").closest("button")!;
    expect(newActiveButton).toHaveClass("bg-surface-selected");

    const nowInactiveButton = screen.getByText("Act on low-risk work").closest("button")!;
    expect(nowInactiveButton).not.toHaveClass("bg-surface-selected");
  });
});
