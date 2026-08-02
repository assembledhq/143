import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { subDays } from "date-fns";
import { formatReviewTurnaround } from "./code-review-overview";
import { TimeRangePicker, timeRangeLabel } from "./time-range-picker";
import { customTimeRange } from "@/lib/time-range";

function setDesktopMatch(matches: boolean) {
  Object.defineProperty(window, "matchMedia", {
    configurable: true,
    writable: true,
    value: vi.fn().mockImplementation((query: string) => ({
      matches: query === "(min-width: 768px)" ? matches : false,
      media: query,
      onchange: null,
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
      addListener: vi.fn(),
      removeListener: vi.fn(),
      dispatchEvent: vi.fn(),
    })),
  });
}

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
  beforeEach(() => setDesktopMatch(true));

  it.each([
    { desktop: false, expectedMonths: 1 },
    { desktop: true, expectedMonths: 2 },
  ])("renders $expectedMonths calendar month(s) when desktop is $desktop", async ({ desktop, expectedMonths }) => {
    setDesktopMatch(desktop);
    const user = userEvent.setup();
    render(<TimeRangePicker label="Time window" value="30d" onValueChange={vi.fn()} />);

    await user.click(screen.getByRole("button", { name: "Time window" }));

    expect(document.querySelectorAll(".rdp-month")).toHaveLength(expectedMonths);
  });

  it("shows presets and applies one immediately", async () => {
    const user = userEvent.setup();
    const onValueChange = vi.fn();
    render(<TimeRangePicker label="Time window" value="30d" onValueChange={onValueChange} />);

    await user.click(screen.getByRole("button", { name: "Time window" }));
    const dialog = screen.getByRole("dialog", { name: "Choose time range" });
    expect(within(dialog).getByText("Custom range")).toBeInTheDocument();
    expect(within(dialog).getByRole("button", { name: /Last 30 days/ })).toHaveAttribute("data-variant", "secondary");
    for (const label of ["This week", "Last week", "Last 2 weeks", "This month", "Last month"]) {
      expect(within(dialog).getByRole("button", { name: label })).toBeInTheDocument();
    }

    await user.click(within(dialog).getByRole("button", { name: "Last month" }));

    expect(onValueChange).toHaveBeenCalledWith("last_month");
    expect(screen.queryByRole("dialog", { name: "Choose time range" })).not.toBeInTheDocument();
  });

  it("formats a custom range for the trigger", () => {
    expect(timeRangeLabel("custom:2026-07-01:2026-07-31")).toBe("Jul 1, 2026 – Jul 31, 2026");
  });

  it.each([
    { value: "this_week" as const, expected: "This week" },
    { value: "last_week" as const, expected: "Last week" },
    { value: "last_2_weeks" as const, expected: "Last 2 weeks" },
    { value: "this_month" as const, expected: "This month" },
    { value: "last_month" as const, expected: "Last month" },
  ])("formats the $value preset for the trigger", ({ value, expected }) => {
    expect(timeRangeLabel(value)).toBe(expected);
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
    const endButton = document.querySelector<HTMLElement>(`[data-day="${end.toLocaleDateString()}"]`);
    expect(endButton).not.toBeNull();
    await user.click(endButton as HTMLElement);
    await user.click(screen.getByRole("button", { name: "Apply range" }));

    expect(onValueChange).toHaveBeenCalledWith(customTimeRange(start, end));
  });
});
