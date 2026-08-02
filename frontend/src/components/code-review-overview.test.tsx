import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { subDays } from "date-fns";
import { formatReviewTurnaround } from "./code-review-overview";
import { TimeRangePicker, timeRangeLabel } from "./time-range-picker";
import { customTimeRange } from "@/lib/time-range";

describe("formatReviewTurnaround", () => {
  it.each([
    { seconds: null, expected: "—" },
    { seconds: 45, expected: "45s" },
    { seconds: 8 * 60, expected: "8m" },
    { seconds: 90 * 60, expected: "1h 30m" },
    { seconds: 48 * 60 * 60, expected: "48h" },
  ])("formats $seconds seconds as $expected", ({ seconds, expected }) => {
    expect(formatReviewTurnaround(seconds)).toBe(expected);
  });
});

describe("TimeRangePicker", () => {
  it("shows presets and applies one immediately", async () => {
    const user = userEvent.setup();
    const onValueChange = vi.fn();
    render(<TimeRangePicker label="Time window" value="30d" onValueChange={onValueChange} />);

    await user.click(screen.getByRole("button", { name: "Time window" }));
    const dialog = screen.getByRole("dialog", { name: "Choose time range" });
    expect(within(dialog).getByText("Custom range")).toBeInTheDocument();
    expect(within(dialog).getByRole("button", { name: /Last 30 days/ })).toHaveAttribute("data-variant", "secondary");

    await user.click(within(dialog).getByRole("button", { name: /Last 7 days/ }));

    expect(onValueChange).toHaveBeenCalledWith("7d");
    expect(screen.queryByRole("dialog", { name: "Choose time range" })).not.toBeInTheDocument();
  });

  it("formats a custom range for the trigger", () => {
    expect(timeRangeLabel("custom:2026-07-01:2026-07-31")).toBe("Jul 1, 2026 – Jul 31, 2026");
  });

  it("shows boundary dates only in their owning month", async () => {
    const user = userEvent.setup();
    render(
      <TimeRangePicker
        label="Time window"
        value="custom:2026-07-30:2026-08-01"
        onValueChange={vi.fn()}
      />,
    );

    await user.click(screen.getByRole("button", { name: "Time window" }));

    for (const date of [new Date(2026, 6, 30), new Date(2026, 6, 31), new Date(2026, 7, 1)]) {
      expect(document.querySelectorAll(`[data-day="${date.toLocaleDateString()}"]`)).toHaveLength(1);
    }
  });

  it("applies a custom range after both dates are selected", async () => {
    const user = userEvent.setup();
    const onValueChange = vi.fn();
    const end = subDays(new Date(), 6);
    const start = subDays(new Date(), 12);
    render(<TimeRangePicker label="Time window" value="30d" onValueChange={onValueChange} />);

    await user.click(screen.getByRole("button", { name: "Time window" }));
    const startButton = document.querySelector<HTMLElement>(`[data-day="${start.toLocaleDateString()}"]`);
    expect(startButton).not.toBeNull();
    await user.click(startButton as HTMLElement);
    const applyButton = screen.getByRole("button", { name: "Apply range" });
    expect(applyButton).toBeDisabled();
    await user.hover(applyButton.parentElement as HTMLElement);
    expect(await screen.findByRole("tooltip")).toHaveTextContent(
      "Select a start and end date to apply the range.",
    );
    const endButton = document.querySelector<HTMLElement>(`[data-day="${end.toLocaleDateString()}"]`);
    expect(endButton).not.toBeNull();
    await user.click(endButton as HTMLElement);
    await user.click(applyButton);

    expect(onValueChange).toHaveBeenCalledWith(customTimeRange(start, end));
  });
});
