import { useState } from "react";
import { act, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { addDays, endOfMonth, format, startOfMonth, subDays, subMonths } from "date-fns";
import { CodeReviewSummaryCards, formatReviewTurnaround } from "./code-review-overview";
import { TimeRangePicker, timeRangeLabel } from "./time-range-picker";
import { customTimeRange, type TimeRangeFilter } from "@/lib/time-range";

// `matches` is a live getter and listeners are real, so `changeViewport` can
// move a mounted component across the breakpoint the way a rotation does.
let desktopMatches = true;
const queryListeners = new Set<() => void>();

function setDesktopMatch(matches: boolean) {
  desktopMatches = matches;
  queryListeners.clear();
  Object.defineProperty(window, "matchMedia", {
    configurable: true,
    writable: true,
    value: vi.fn().mockImplementation((query: string) => {
      const listeners = new Set<() => void>();
      queryListeners.add(() => listeners.forEach((listener) => listener()));
      return {
        get matches() {
          return { "(min-width: 768px)": desktopMatches, "(max-width: 767px)": !desktopMatches }[query] ?? false;
        },
        media: query,
        onchange: null,
        addEventListener: (_: string, listener: () => void) => listeners.add(listener),
        removeEventListener: (_: string, listener: () => void) => listeners.delete(listener),
        addListener: (listener: () => void) => listeners.add(listener),
        removeListener: (listener: () => void) => listeners.delete(listener),
        dispatchEvent: vi.fn(),
      };
    }),
  });
}

async function changeViewport(matches: boolean) {
  desktopMatches = matches;
  await act(async () => {
    queryListeners.forEach((notify) => notify());
  });
}

describe("CodeReviewSummaryCards", () => {
  it("moves metric definitions from subdescriptions into heading tooltips", async () => {
    const user = userEvent.setup();
    render(
      <CodeReviewSummaryCards
        stats={{
          reviews_completed: 128,
          automatically_approved: 92,
          needs_human_review: 21,
          median_turnaround_seconds: 480,
        }}
        isLoading={false}
        isError={false}
        onRetry={vi.fn()}
      />,
    );

    const summary = screen.getByRole("region", { name: "Code review statistics" });
    expect(within(summary).queryByText("72% of completed reviews")).not.toBeInTheDocument();
    expect(within(summary).queryByText("21 need human review")).not.toBeInTheDocument();
    expect(within(summary).queryByText("Queued to completed")).not.toBeInTheDocument();

    const trigger = within(summary).getByRole("button", { name: "About Approval rate" });
    await user.hover(trigger);
    expect(await screen.findByRole("tooltip")).toHaveTextContent(
      "The percentage of completed review sessions where 143 posted an approval on GitHub.",
    );

    expect(within(summary).getByRole("button", { name: "About Reviews completed" })).toBeInTheDocument();
    expect(within(summary).getByRole("button", { name: "About Automatically approved" })).toBeInTheDocument();
    expect(within(summary).getByRole("button", { name: "About Median turnaround" })).toBeInTheDocument();
  });
});

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

  it("uses the full mobile screen instead of an anchored popover", async () => {
    setDesktopMatch(false);
    const user = userEvent.setup();
    render(<TimeRangePicker label="Time window" value="30d" onValueChange={vi.fn()} />);

    await user.click(screen.getByRole("button", { name: "Time window" }));

    const dialog = screen.getByRole("dialog", { name: "Choose time range" });
    expect(dialog).toHaveAttribute("data-slot", "sheet-content");
    expect(dialog).toHaveClass("inset-0", "h-dvh", "max-h-dvh", "max-w-none", "rounded-none");
    expect(document.querySelector('[data-slot="popover-content"]')).not.toBeInTheDocument();
    expect(within(dialog).getByRole("button", { name: "Last 30 days" })).toBeInTheDocument();
  });

  it("pins the mobile range actions outside the scrolling body", async () => {
    setDesktopMatch(false);
    const user = userEvent.setup();
    render(<TimeRangePicker label="Time window" value="30d" onValueChange={vi.fn()} />);

    await user.click(screen.getByRole("button", { name: "Time window" }));

    const dialog = screen.getByRole("dialog", { name: "Choose time range" });
    const body = dialog.querySelector('[data-slot="time-range-picker-body"]');
    const month = document.querySelector<HTMLElement>(".rdp-month");
    expect(body).not.toBeNull();
    expect(month).not.toBeNull();
    // The calendar scrolls; Cancel/Apply must stay reachable without scrolling.
    expect(body).toContainElement(month);
    for (const name of ["Cancel", "Apply range"]) {
      const action = within(dialog).getByRole("button", { name });
      expect(body).not.toContainElement(action);
    }
  });

  it("leaves the value untouched when the mobile sheet is dismissed", async () => {
    setDesktopMatch(false);
    const user = userEvent.setup();
    const onValueChange = vi.fn();
    const start = subDays(new Date(), 12);
    render(<TimeRangePicker label="Time window" value="30d" onValueChange={onValueChange} />);

    await user.click(screen.getByRole("button", { name: "Time window" }));
    const day = document.querySelector<HTMLElement>(`[data-day="${start.toLocaleDateString()}"]`);
    expect(day).not.toBeNull();
    await user.click(day as HTMLElement);
    await user.click(screen.getByRole("button", { name: "Close" }));

    expect(screen.queryByRole("dialog", { name: "Choose time range" })).not.toBeInTheDocument();
    expect(onValueChange).not.toHaveBeenCalled();
  });

  it.each([
    { desktop: false, surface: "mobile sheet" },
    { desktop: true, surface: "desktop popover" },
  ])("discards a half-selected range when Cancel is clicked in the $surface", async ({ desktop }) => {
    setDesktopMatch(desktop);
    const user = userEvent.setup();
    const onValueChange = vi.fn();
    const start = subDays(new Date(), 12);
    render(<TimeRangePicker label="Time window" value="30d" onValueChange={onValueChange} />);

    await user.click(screen.getByRole("button", { name: "Time window" }));
    const day = document.querySelector<HTMLElement>(`[data-day="${start.toLocaleDateString()}"]`);
    expect(day).not.toBeNull();
    await user.click(day as HTMLElement);
    await user.click(screen.getByRole("button", { name: "Cancel" }));

    expect(screen.queryByRole("dialog", { name: "Choose time range" })).not.toBeInTheDocument();
    expect(onValueChange).not.toHaveBeenCalled();

    // Reopening starts from the applied value again, not the abandoned draft:
    // the summary shows a complete range instead of a dangling start date.
    await user.click(screen.getByRole("button", { name: "Time window" }));
    expect(screen.queryByText(/Select an end date$/)).not.toBeInTheDocument();
    expect(screen.queryByText("Select a start date")).not.toBeInTheDocument();
  });

  it("keeps the picker open and the draft intact when the viewport crosses the breakpoint", async () => {
    setDesktopMatch(false);
    const user = userEvent.setup();
    const start = subDays(new Date(), 12);
    render(<TimeRangePicker label="Time window" value="30d" onValueChange={vi.fn()} />);

    await user.click(screen.getByRole("button", { name: "Time window" }));
    const day = document.querySelector<HTMLElement>(`[data-day="${start.toLocaleDateString()}"]`);
    expect(day).not.toBeNull();
    await user.click(day as HTMLElement);
    expect(screen.getByRole("dialog", { name: "Choose time range" }))
      .toHaveAttribute("data-slot", "sheet-content");

    // e.g. rotating a phone to landscape crosses 768px mid-selection.
    await changeViewport(true);

    const dialog = screen.getByRole("dialog", { name: "Choose time range" });
    expect(dialog).toHaveAttribute("data-slot", "popover-content");
    expect(within(dialog).getByText(`${format(start, "MMM d, yyyy")} – Select an end date`))
      .toBeInTheDocument();
  });

  it("applies a custom range from the mobile sheet", async () => {
    setDesktopMatch(false);
    const user = userEvent.setup();
    const onValueChange = vi.fn();
    const visibleMonth = startOfMonth(subDays(new Date(), 30));
    const start = addDays(visibleMonth, 2);
    const end = addDays(visibleMonth, 8);
    render(<TimeRangePicker label="Time window" value="30d" onValueChange={onValueChange} />);

    await user.click(screen.getByRole("button", { name: "Time window" }));
    for (const date of [start, end]) {
      const day = document.querySelector<HTMLElement>(`[data-day="${date.toLocaleDateString()}"]`);
      expect(day).not.toBeNull();
      await user.click(day as HTMLElement);
    }
    await user.click(screen.getByRole("button", { name: "Apply range" }));

    expect(onValueChange).toHaveBeenCalledWith(customTimeRange(start, end));
    expect(screen.queryByRole("dialog", { name: "Choose time range" })).not.toBeInTheDocument();
  });

  it("shows presets and applies one immediately", async () => {
    const user = userEvent.setup();
    const onValueChange = vi.fn();
    render(<TimeRangePicker label="Time window" value="30d" onValueChange={onValueChange} />);

    await user.click(screen.getByRole("button", { name: "Time window" }));
    const dialog = screen.getByRole("dialog", { name: "Choose time range" });
    expect(dialog).toHaveAttribute("data-slot", "popover-content");
    expect(document.querySelector('[data-slot="sheet-content"]')).not.toBeInTheDocument();
    expect(within(dialog).getByText("Custom range")).toBeInTheDocument();
    expect(within(dialog).getByRole("button", { name: /Last 30 days/ })).toHaveAttribute("data-variant", "secondary");
    for (const label of ["This week", "Last week", "Last 2 weeks", "This month", "Last month"]) {
      expect(within(dialog).getByRole("button", { name: label })).toBeInTheDocument();
    }

    await user.click(within(dialog).getByRole("button", { name: "Last month" }));

    expect(onValueChange).toHaveBeenCalledWith("last_month");
    expect(screen.queryByRole("dialog", { name: "Choose time range" })).not.toBeInTheDocument();
  });

  it("shows the active preset range in the calendar", async () => {
    const user = userEvent.setup();
    const anchor = new Date();
    const expectedStart = startOfMonth(subMonths(anchor, 1));
    const expectedEnd = endOfMonth(subMonths(anchor, 1));
    render(<TimeRangePicker label="Time window" value="last_month" onValueChange={vi.fn()} />);

    await user.click(screen.getByRole("button", { name: "Time window" }));

    expect(document.querySelector(`[data-day="${expectedStart.toLocaleDateString()}"]`))
      .toHaveAttribute("data-range-start", "true");
    expect(document.querySelector(`[data-day="${expectedEnd.toLocaleDateString()}"]`))
      .toHaveAttribute("data-range-end", "true");
    expect(screen.getByText(
      `${format(expectedStart, "MMM d, yyyy")} – ${format(expectedEnd, "MMM d, yyyy")}`,
    )).toBeInTheDocument();
  });

  it("keeps the calendar in sync after applying a preset", async () => {
    const user = userEvent.setup();
    const expectedStart = startOfMonth(subMonths(new Date(), 1));
    const expectedEnd = endOfMonth(subMonths(new Date(), 1));

    function Harness() {
      const [value, setValue] = useState<TimeRangeFilter>("30d");
      return <TimeRangePicker label="Time window" value={value} onValueChange={setValue} />;
    }
    render(<Harness />);

    await user.click(screen.getByRole("button", { name: "Time window" }));
    await user.click(screen.getByRole("button", { name: "Last month" }));
    await user.click(screen.getByRole("button", { name: "Time window" }));

    expect(document.querySelector(`[data-day="${expectedStart.toLocaleDateString()}"]`))
      .toHaveAttribute("data-range-start", "true");
    expect(document.querySelector(`[data-day="${expectedEnd.toLocaleDateString()}"]`))
      .toHaveAttribute("data-range-end", "true");
  });

  it("formats a custom range for the trigger", () => {
    expect(timeRangeLabel("custom:2026-07-01:2026-07-31")).toBe("Jul 1, 2026 – Jul 31, 2026");
  });

  it("explains why an incomplete custom range cannot be applied", async () => {
    const user = userEvent.setup();
    render(<TimeRangePicker label="Time window" value="30d" onValueChange={vi.fn()} />);

    await user.click(screen.getByRole("button", { name: "Time window" }));
    const start = subDays(new Date(), 5);
    const startButton = document.querySelector<HTMLElement>(`[data-day="${start.toLocaleDateString()}"]`);
    expect(startButton).not.toBeNull();
    await user.click(startButton as HTMLElement);
    const applyButton = screen.getByRole("button", { name: "Apply range" });
    expect(applyButton).toBeDisabled();

    await user.hover(applyButton.parentElement as HTMLElement);

    expect(await screen.findByRole("tooltip")).toHaveTextContent(
      "Select an end date to apply this range.",
    );
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
    const endButton = document.querySelector<HTMLElement>(`[data-day="${end.toLocaleDateString()}"]`);
    expect(endButton).not.toBeNull();
    await user.click(endButton as HTMLElement);
    await user.click(screen.getByRole("button", { name: "Apply range" }));

    expect(onValueChange).toHaveBeenCalledWith(customTimeRange(start, end));
  });
});
