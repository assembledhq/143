import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { describe, expect, it, vi } from "vitest";
import { CodeReviewAnalyticsTrend } from "@/components/code-review-analytics-trend";
import type { CodeReviewAnalyticsTrend as Trend, CodeReviewAnalyticsTrendBucket } from "@/lib/types";

const chart = vi.hoisted(() => ({ render: vi.fn(), line: vi.fn() }));
vi.mock("recharts", () => ({
  ResponsiveContainer: ({ children }: { children: ReactNode }) => children,
  LineChart: (props: { children: ReactNode; onClick: (state: { activeTooltipIndex: number }) => void }) => {
    chart.render(props);
    return <div>{props.children}<button onClick={() => props.onClick({ activeTooltipIndex: 0 })}>Tap chart interval</button></div>;
  },
  Line: (props: unknown) => { chart.line(props); return null; },
  Tooltip: () => null,
  CartesianGrid: () => null,
  XAxis: () => null,
  YAxis: () => null,
}));

function bucket(overrides: Partial<CodeReviewAnalyticsTrendBucket> = {}): CodeReviewAnalyticsTrendBucket {
  return {
    start: "2026-10-01T04:00:00Z",
    end: "2026-10-02T04:00:00Z",
    prs_reviewed: 4,
    approved_by_143: 2,
    median_rounds_to_approval: 1.5,
    average_rounds_to_approval: 2.333333333,
    p95_rounds_to_approval: 4,
    partial: false,
    overflow_prs: 0,
    ...overrides,
  };
}

function trend(): Extract<Trend, { status: "available" }> {
  return {
    status: "available", mode: "finite", generated_at: "2026-10-09T18:00:00Z", bucket_width_seconds: 86400,
    current_window: { start: "2026-10-01T04:00:00Z", end: "2026-11-01T04:00:00Z", observed_end: "2026-10-09T18:00:00Z" },
    previous_window: { start: "2026-09-01T04:00:00Z", end: "2026-10-01T04:00:00Z", compared_end: "2026-09-09T18:00:00Z" },
    observed_end_advanced_by_data: false, overflow_prs: 0, latest_included_first_requested_at: null,
    points: [{ index: 0, current: bucket(), previous: bucket({ start: "2026-09-01T04:00:00Z", end: "2026-09-02T04:00:00Z" }), unequal_exposure: false }],
  };
}

describe("CodeReviewAnalyticsTrend", () => {
  it("keeps the legend compact and full range and methodology details collapsed until requested", async () => {
    const user = userEvent.setup();
    render(<CodeReviewAnalyticsTrend trend={trend()} metric="average_rounds_to_approval" />);
    const legend = screen.getByLabelText("Compared periods");
    expect(legend).toHaveTextContent(/Current · Oct .*Previous · Sep/);
    expect(legend).not.toHaveTextContent(/2026|exclusive|Full range|00:00|EDT/);
    const about = screen.getByRole("button", { name: "About this chart" });
    expect(about).toHaveAttribute("aria-expanded", "false");
    expect(about).toHaveClass("group");
    expect(about.querySelector("svg")).toHaveClass("transition-transform", "group-data-[state=open]:rotate-180");
    expect(screen.queryByText(/Current requested range/)).not.toBeInTheDocument();
    expect(screen.queryByText(/Outcomes include later completed reviews/)).not.toBeInTheDocument();
    expect(screen.queryByText(/Hover or tap/)).not.toBeInTheDocument();
    about.focus();
    await user.keyboard("{Enter}");
    expect(about).toHaveAttribute("aria-expanded", "true");
    expect(screen.getByText(/Current requested range/).parentElement).toHaveTextContent(/2026.*exclusive/);
    expect(screen.getByText(/Current observed range/)).toBeInTheDocument();
    expect(screen.getByText(/Previous requested range/)).toBeInTheDocument();
    expect(screen.getByText(/Previous compared range/)).toBeInTheDocument();
    expect(screen.getByText(/Intervals align by elapsed time/)).toBeInTheDocument();
    expect(screen.getByText(/Outcomes include later completed reviews/)).toBeInTheDocument();
  });

  it.each([
    { metric: "average_rounds_to_approval", sample: "1 approved PR", value: "1.0" },
    { metric: "prs_reviewed", sample: "1 PR", value: "1" },
    { metric: "approval_rate", sample: "1 of 1 PR approved", value: "100%" },
  ] as const)("shows concise day labels and singular samples for $metric", async ({ metric, sample, value }) => {
    const user = userEvent.setup();
    const data = trend();
    const single = { prs_reviewed: 1, approved_by_143: 1, average_rounds_to_approval: 1 };
    data.points[0]!.current = bucket({ ...single, start: new Date(2026, 9, 1).toISOString(), end: new Date(2026, 9, 2).toISOString() });
    data.points[0]!.previous = bucket({ ...single, start: new Date(2026, 8, 1).toISOString(), end: new Date(2026, 8, 2).toISOString() });
    render(<CodeReviewAnalyticsTrend trend={data} metric={metric} />);
    await user.click(screen.getByRole("button", { name: "Tap chart interval" }));
    const details = screen.getAllByText(sample)[0]!.closest('[aria-live="polite"]')!;
    expect(details).toHaveTextContent(/Current · Oct 1/);
    expect(details).toHaveTextContent(/Previous · Sep 1/);
    expect(within(details as HTMLElement).getAllByText(sample)).toHaveLength(2);
    expect(within(details as HTMLElement).getAllByText(value)).toHaveLength(2);
    expect(details).not.toHaveTextContent(/hours|exclusive|2026|12:00|PRs/);
  });

  it("shows concise actual times for a subday interval", async () => {
    const user = userEvent.setup();
    const data = trend();
    data.points[0]!.current = bucket({ start: new Date(2026, 9, 1).toISOString(), end: new Date(2026, 9, 1, 12).toISOString() });
    render(<CodeReviewAnalyticsTrend trend={data} metric="average_rounds_to_approval" />);
    await user.click(screen.getByRole("button", { name: "Tap chart interval" }));
    const details = screen.getAllByText("2 approved PRs")[0]!.closest('[aria-live="polite"]')!;
    expect(details).toHaveTextContent("Oct 1, 12:00 AM–12:00 PM");
    expect(details).not.toHaveTextContent(/hours|exclusive|2026/);
  });

  it("keeps actual local times and timezone changes accessible for a fixed-day bucket crossing DST", async () => {
    const user = userEvent.setup();
    const data = trend();
    const start = new Date(2026, 10, 1);
    const end = new Date(start.getTime() + 86_400_000);
    data.points[0]!.current = bucket({ start: start.toISOString(), end: end.toISOString() });
    render(<CodeReviewAnalyticsTrend trend={data} metric="average_rounds_to_approval" />);
    await user.click(screen.getByRole("button", { name: "Tap chart interval" }));
    const details = screen.getAllByText("2 approved PRs")[0]!.closest('[aria-live="polite"]')!;
    if (start.getTimezoneOffset() !== end.getTimezoneOffset()) {
      expect(details).toHaveTextContent(start.toLocaleTimeString(undefined, { hour: "numeric", minute: "2-digit", timeZoneName: "short" }));
      expect(details).toHaveTextContent(end.toLocaleTimeString(undefined, { hour: "numeric", minute: "2-digit", timeZoneName: "short" }));
    } else {
      expect(details).toHaveTextContent("Current · Nov 1");
    }
    await user.click(screen.getByRole("button", { name: "Show trend data" }));
    const table = screen.getByRole("table");
    const exact = (value: Date) => value.toLocaleString(undefined, { year: "numeric", month: "short", day: "numeric", hour: "numeric", minute: "2-digit", second: "2-digit", timeZoneName: "short" });
    expect(within(table).getByText(`${exact(start)} – ${exact(end)} (end exclusive)`)).toBeInTheDocument();
  });

  it.each([
    { data: undefined, copy: "Trend data is unavailable from this server." },
    { data: { status: "unavailable", generated_at: "2026-10-09T18:00:00Z", unavailable_reason: "range_too_large" } as Trend, copy: "Choose a range of ten years or less to view trends." },
    { data: { status: "unavailable", generated_at: "2026-10-09T18:00:00Z", unavailable_reason: "unrepresentable_range" } as Trend, copy: "This range cannot be charted. Choose another range." },
    { data: { status: "unavailable", generated_at: "2026-10-09T18:00:00Z", unavailable_reason: "inconsistent_geometry" } as Trend, copy: "This range cannot be charted. Choose another range." },
  ])("explains unavailable trends: $copy", ({ data, copy }) => {
    render(<CodeReviewAnalyticsTrend trend={data} metric="average_rounds_to_approval" />);
    expect(screen.getByText(copy)).toBeInTheDocument();
    expect(screen.getByText("The current report remains available above and below this chart.")).toBeInTheDocument();
  });

  it("shows both actual intervals, durations, exact round values and sample counts in an expandable keyboard-accessible table", async () => {
    const user = userEvent.setup();
    render(<CodeReviewAnalyticsTrend trend={trend()} metric="median_rounds_to_approval" />);
    const toggle = screen.getByRole("button", { name: "Show trend data" });
    expect(toggle).toHaveAttribute("aria-expanded", "false");
    toggle.focus();
    await user.keyboard("{Enter}");
    const table = screen.getByRole("table", { name: "Median rounds to approval trend data" });
    expect(screen.getByRole("button", { name: "Hide trend data" })).toHaveAttribute("aria-expanded", "true");
    const local = (instant: string) => new Date(instant).toLocaleString(undefined, { year: "numeric", month: "short", day: "numeric", hour: "numeric", minute: "2-digit", second: "2-digit", timeZoneName: "short" });
    expect(within(table).getByText(`${local("2026-10-01T04:00:00Z")} – ${local("2026-10-02T04:00:00Z")} (end exclusive)`)).toBeInTheDocument();
    expect(within(table).getByText(`${local("2026-09-01T04:00:00Z")} – ${local("2026-09-02T04:00:00Z")} (end exclusive)`)).toBeInTheDocument();
    expect(within(table).getAllByText("24 hours")).toHaveLength(2);
    expect(within(table).getAllByText("1.5")).toHaveLength(2);
    expect(within(table).getAllByText("4")).toHaveLength(2);
    expect(within(table).getAllByText("2")).toHaveLength(2);
  });

  it("retains the previous rounds series when the current cohort has no approvals", async () => {
    const user = userEvent.setup();
    const data = trend();
    data.points[0]!.current = bucket({ prs_reviewed: 0, approved_by_143: 0, median_rounds_to_approval: null, average_rounds_to_approval: null, p95_rounds_to_approval: null });
    render(<CodeReviewAnalyticsTrend trend={data} metric="average_rounds_to_approval" />);
    expect(screen.getByText("No approved PRs in this period.")).toBeInTheDocument();
    expect(chart.render).toHaveBeenLastCalledWith(expect.objectContaining({
      accessibilityLayer: true,
      data: [{ elapsedDays: 0, current: null, previous: 2.333333333, point: data.points[0] }],
    }));
    await user.click(screen.getByRole("button", { name: "About this chart" }));
    expect(screen.getByText(/Previous-period PRs have had longer/)).toBeInTheDocument();
  });

  it("derives bucket percentages from raw counts and preserves gaps and straight comparison lines", () => {
    const data = trend();
    data.points = [data.points[0]!, {
      index: 1,
      current: bucket({ start: "2026-10-02T04:00:00Z", end: "2026-10-03T04:00:00Z", prs_reviewed: 0, approved_by_143: 0 }),
      previous: null,
      unequal_exposure: false,
    }];
    render(<CodeReviewAnalyticsTrend trend={data} metric="approval_rate" />);
    expect(chart.render).toHaveBeenLastCalledWith(expect.objectContaining({ data: [
      { elapsedDays: 0, current: 50, previous: 50, point: data.points[0] },
      { elapsedDays: 1, current: null, previous: null, point: data.points[1] },
    ] }));
    expect(chart.line).toHaveBeenCalledWith(expect.objectContaining({ dataKey: "current", type: "linear", connectNulls: false, isAnimationActive: false }));
    expect(chart.line).toHaveBeenCalledWith(expect.objectContaining({ dataKey: "previous", type: "linear", strokeDasharray: "5 4", connectNulls: false }));
  });

  it("shows one concise exception notice and leaves exact comparison detail in the table", async () => {
    const user = userEvent.setup();
    const data = trend();
    data.points[0]!.current = bucket({ end: "2026-10-01T16:00:00Z", partial: true, overflow_prs: 2 });
    data.points[0]!.unequal_exposure = true;
    data.observed_end_advanced_by_data = true;
    data.overflow_prs = 2;
    render(<CodeReviewAnalyticsTrend trend={data} metric="average_rounds_to_approval" />);
    expect(screen.getByText("Some intervals are partial. Some comparison intervals have different durations. Latest PRs are included; the final comparison uses nominal dates.")).toBeInTheDocument();
    await user.pointer([{ keys: "[TouchA>]", target: screen.getByRole("button", { name: "Tap chart interval" }) }, { keys: "[/TouchA]" }]);
    expect(await screen.findAllByText("2 approved PRs")).toHaveLength(2);
    expect(screen.queryByText(/hours/)).not.toBeInTheDocument();
    expect(screen.queryByText("This point includes the latest PRs captured after the chart's range anchor.")).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Show trend data" }));
    const table = screen.getByRole("table", { name: "Average rounds to approval trend data" });
    expect(within(table).getByText("12 hours")).toBeInTheDocument();
    expect(within(table).getByText("24 hours")).toBeInTheDocument();
    expect(within(table).getByText(/Partial bucket interval.*slightly different durations.*latest PRs captured/)).toBeInTheDocument();
  });

  it("shows missing comparison for a shorter previous window", async () => {
    const user = userEvent.setup();
    const data = trend();
    data.points[0]!.previous = null;
    render(<CodeReviewAnalyticsTrend trend={data} metric="prs_reviewed" />);
    await user.click(screen.getByRole("button", { name: "Show trend data" }));
    expect(screen.getByText("No comparison")).toBeInTheDocument();
    expect(screen.getByText("The previous period is shorter; no comparison is available for this portion.")).toBeInTheDocument();
  });

  it("describes a completed historical partial bucket without implying unfinished or future time", async () => {
    const user = userEvent.setup();
    const data = trend();
    data.current_window = { start: "2026-09-01T04:00:00Z", end: "2026-10-01T04:00:00Z", observed_end: "2026-10-01T03:59:59.999001Z" };
    data.previous_window = { start: "2026-08-01T04:00:00Z", end: "2026-09-01T04:00:00Z", compared_end: "2026-08-31T03:59:59.999001Z" };
    data.points = [{
      index: 0,
      current: bucket({ start: "2026-09-30T04:00:00Z", end: "2026-10-01T03:59:59.999001Z", partial: true }),
      previous: bucket({ start: "2026-08-30T04:00:00Z", end: "2026-08-31T03:59:59.999001Z", partial: true }),
      unequal_exposure: false,
    }];
    render(<CodeReviewAnalyticsTrend trend={data} metric="prs_reviewed" />);
    expect(screen.getByText("Some intervals are partial.")).toBeInTheDocument();
    expect(screen.queryByText(/partially observed|Future time|latest interval/i)).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Show trend data" }));
    expect(within(screen.getByRole("table")).getAllByText("Partial bucket interval.")).toHaveLength(2);
  });

  it("describes overflow-only pairs as nominal context without claiming their equal intervals differ in duration", async () => {
    const user = userEvent.setup();
    const data = trend();
    data.points[0]!.current = bucket({ overflow_prs: 2 });
    data.points[0]!.unequal_exposure = true;
    data.observed_end_advanced_by_data = true;
    data.overflow_prs = 2;
    render(<CodeReviewAnalyticsTrend trend={data} metric="average_rounds_to_approval" />);
    expect(screen.getByText("Latest PRs are included; the final comparison uses nominal dates.")).toBeInTheDocument();
    expect(screen.queryByText(/slightly different durations/)).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Tap chart interval" }));
    expect(screen.queryByText("This point includes the latest PRs captured after the chart's range anchor.")).not.toBeInTheDocument();
    expect(screen.queryByText("This pair shows nominal-period context; it does not have matched exposure.")).not.toBeInTheDocument();
    expect(screen.getAllByText("2 approved PRs")).toHaveLength(2);
    expect(screen.queryByText(/slightly different durations/)).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Show trend data" }));
    const table = screen.getByRole("table", { name: "Average rounds to approval trend data" });
    expect(within(table).getAllByText("24 hours")).toHaveLength(2);
    expect(within(table).getAllByText(/nominal-period context; it does not have matched exposure/)).toHaveLength(2);
    expect(within(table).queryByText(/slightly different durations/)).not.toBeInTheDocument();
  });

  it.each([
    { state: "future", copy: "This period has not started yet." },
    { state: "both_empty", copy: "No PRs were first sent in either period." },
    { state: "no_approvals", copy: "No approved PRs in this period." },
    { state: "all_time_empty", copy: "No PRs were first sent in this period." },
  ])("distinguishes the $state state", ({ state, copy }) => {
    const data = trend();
    if (state === "future") data.points = [];
    if (state === "all_time_empty") {
      data.points = []; data.mode = "all_time"; data.current_window = null; data.previous_window = null;
    }
    if (state === "both_empty" || state === "no_approvals") {
      data.points = [{ index: 0, unequal_exposure: false, current: bucket({ prs_reviewed: state === "both_empty" ? 0 : 3, approved_by_143: 0, average_rounds_to_approval: null }), previous: bucket({ prs_reviewed: state === "both_empty" ? 0 : 3, approved_by_143: 0, average_rounds_to_approval: null }) }];
    }
    render(<CodeReviewAnalyticsTrend trend={data} metric="average_rounds_to_approval" />);
    expect(screen.getByText(copy)).toBeInTheDocument();
  });

  it("shows one All time series without treating it as a shorter comparison", async () => {
    const user = userEvent.setup();
    const data = trend();
    data.mode = "all_time"; data.previous_window = null; data.points[0]!.previous = null;
    render(<CodeReviewAnalyticsTrend trend={data} metric="prs_reviewed" />);
    expect(screen.getByText("All time has no previous period to compare.")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Tap chart interval" }));
    expect(screen.queryByText(/previous period is shorter/)).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Show trend data" }));
    expect(within(screen.getByRole("table")).queryByText("Previous")).not.toBeInTheDocument();
  });
});
