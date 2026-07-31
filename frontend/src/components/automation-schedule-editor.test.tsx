import { describe, expect, it, vi } from "vitest";
import { http, HttpResponse } from "msw";
import {
  renderWithProviders,
  screen,
  userEvent,
  waitFor,
} from "@/test/test-utils";
import { server } from "@/test/mocks/server";
import { AutomationScheduleEditor } from "./automation-schedule-editor";

describe("AutomationScheduleEditor", () => {
  it("adds a calendar schedule with a valid preview", async () => {
    const user = userEvent.setup();
    const onChange = vi.fn();

    renderWithProviders(
      <AutomationScheduleEditor
        value={null}
        onChange={onChange}
        detectedTimezone="UTC"
      />,
    );

    await user.click(screen.getByRole("button", { name: "Add schedule" }));

    expect(onChange).toHaveBeenCalledWith(
      expect.objectContaining({
        frequency: "weekly",
        time: "09:00",
        timezone: "UTC",
      }),
    );
  });

  it("previews a weekly schedule and reports validity", async () => {
    const onValidityChange = vi.fn();
    server.use(
      http.post("/api/v1/automations/schedule-preview", async ({ request }) => {
        expect(await request.json()).toEqual({
          schedule_type: "cron",
          cron_expression: "0 9 * * 1,4",
          timezone: "UTC",
        });
        return HttpResponse.json({
          data: { next_run_at: "2026-07-30T09:00:00Z" },
        });
      }),
    );

    renderWithProviders(
      <AutomationScheduleEditor
        value={{
          frequency: "weekly",
          weekdays: ["thursday", "monday"],
          time: "09:00",
          timezone: "UTC",
        }}
        onChange={vi.fn()}
        detectedTimezone="UTC"
        onValidityChange={onValidityChange}
      />,
    );

    expect(await screen.findByText(/Next run:/)).toBeInTheDocument();
    await waitFor(() =>
      expect(onValidityChange).toHaveBeenLastCalledWith(true, {
        serverRejected: false,
      }),
    );
  });

  it("keeps the form saveable when the preview is unreachable", async () => {
    const onValidityChange = vi.fn();
    server.use(
      http.post("/api/v1/automations/schedule-preview", () =>
        HttpResponse.json(
          { error: { code: "PREVIEW_FAILED", message: "Unavailable" } },
          { status: 503 },
        ),
      ),
    );

    renderWithProviders(
      <AutomationScheduleEditor
        value={{ frequency: "daily", time: "09:00", timezone: "UTC" }}
        onChange={vi.fn()}
        detectedTimezone="UTC"
        onValidityChange={onValidityChange}
      />,
    );

    expect(
      await screen.findByText("Could not preview this schedule. Try again."),
    ).toBeInTheDocument();
    // The API revalidates the schedule on write, so a dead preview must not
    // block saving — on the detail page it would block unrelated fields too.
    await waitFor(() =>
      expect(onValidityChange).toHaveBeenLastCalledWith(true, {
        serverRejected: false,
      }),
    );
  });

  it("blocks saving when the preview rejects the schedule", async () => {
    const onValidityChange = vi.fn();
    server.use(
      http.post("/api/v1/automations/schedule-preview", () =>
        HttpResponse.json(
          { error: { code: "INVALID_SCHEDULE", message: "No future occurrence." } },
          { status: 400 },
        ),
      ),
    );

    renderWithProviders(
      <AutomationScheduleEditor
        value={{
          frequency: "advanced",
          cronExpression: "0 0 31 2 *",
          timezone: "UTC",
        }}
        onChange={vi.fn()}
        detectedTimezone="UTC"
        onValidityChange={onValidityChange}
      />,
    );

    expect(await screen.findByText("No future occurrence.")).toBeInTheDocument();
    await waitFor(() =>
      expect(onValidityChange).toHaveBeenLastCalledWith(false, {
        serverRejected: true,
      }),
    );
  });

  it("offers to add a run time to an interval saved without one", async () => {
    const onChange = vi.fn();
    const user = userEvent.setup();

    renderWithProviders(
      <AutomationScheduleEditor
        value={{ frequency: "interval", value: 3, unit: "days", timezone: "UTC" }}
        onChange={onChange}
        detectedTimezone="UTC"
      />,
    );

    // No time is stored, so none is claimed — but adding one must not require
    // first changing the cadence.
    expect(
      screen.queryByRole("combobox", { name: "Run time" }),
    ).not.toBeInTheDocument();
    expect(screen.queryByText("at")).not.toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Set a run time" }));

    expect(onChange).toHaveBeenLastCalledWith({
      frequency: "interval",
      value: 3,
      unit: "days",
      time: "09:00",
      timezone: "UTC",
    });
  });

  it("edits an anchored interval's run time in place", async () => {
    const onChange = vi.fn();
    const user = userEvent.setup();

    renderWithProviders(
      <AutomationScheduleEditor
        value={{
          frequency: "interval",
          value: 3,
          unit: "days",
          time: "09:00",
          timezone: "UTC",
        }}
        onChange={onChange}
        detectedTimezone="UTC"
      />,
    );

    await user.click(screen.getByRole("combobox", { name: "Run time" }));
    await user.click(await screen.findByRole("option", { name: "10:00 AM" }));

    expect(onChange).toHaveBeenLastCalledWith({
      frequency: "interval",
      value: 3,
      unit: "days",
      time: "10:00",
      timezone: "UTC",
    });
  });

  it("hides the run time for sub-day hourly intervals", () => {
    renderWithProviders(
      <AutomationScheduleEditor
        value={{ frequency: "interval", value: 6, unit: "hours", timezone: "UTC" }}
        onChange={vi.fn()}
        detectedTimezone="UTC"
      />,
    );

    expect(
      screen.queryByRole("combobox", { name: "Run time" }),
    ).not.toBeInTheDocument();
    // Elapsed durations have no wall clock to add, either.
    expect(
      screen.queryByRole("button", { name: "Set a run time" }),
    ).not.toBeInTheDocument();
  });

  it("does not rebuild the draft when blurring an unchanged interval", async () => {
    const onChange = vi.fn();
    const user = userEvent.setup();

    renderWithProviders(
      <AutomationScheduleEditor
        value={{ frequency: "interval", value: 3, unit: "days", timezone: "UTC" }}
        onChange={onChange}
        detectedTimezone="UTC"
      />,
    );

    await user.click(screen.getByLabelText("Interval value"));
    await user.tab();

    // A blur that emitted a fresh draft would restart the debounced preview and
    // disable the save button, swallowing a click made straight after typing.
    expect(onChange).not.toHaveBeenCalled();
  });

  it("clears to an empty box instead of snapping the interval to zero", async () => {
    const onChange = vi.fn();
    const user = userEvent.setup();

    renderWithProviders(
      <AutomationScheduleEditor
        value={{ frequency: "interval", value: 3, unit: "days", timezone: "UTC" }}
        onChange={onChange}
        detectedTimezone="UTC"
      />,
    );

    const intervalInput = screen.getByLabelText("Interval value");
    await user.clear(intervalInput);

    expect(intervalInput).toHaveValue(null);
  });

  it("rejects a weekly schedule without a selected day", () => {
    renderWithProviders(
      <AutomationScheduleEditor
        value={{
          frequency: "weekly",
          weekdays: [],
          time: "09:00",
          timezone: "UTC",
        }}
        onChange={vi.fn()}
        detectedTimezone="UTC"
      />,
    );

    expect(screen.getByText("Select at least one day.")).toBeInTheDocument();
  });

  it("labels the timezone control and retries transport failures", async () => {
    const user = userEvent.setup();
    let attempts = 0;
    server.use(
      http.post("/api/v1/automations/schedule-preview", () => {
        attempts += 1;
        if (attempts === 1) {
          return HttpResponse.json(
            { error: { code: "PREVIEW_FAILED", message: "Unavailable" } },
            { status: 503 },
          );
        }
        return HttpResponse.json({
          data: { next_run_at: "2026-07-30T09:00:00Z" },
        });
      }),
    );

    renderWithProviders(
      <AutomationScheduleEditor
        value={{ frequency: "daily", time: "09:00", timezone: "UTC" }}
        onChange={vi.fn()}
        detectedTimezone="UTC"
      />,
    );

    expect(screen.getByRole("combobox", { name: "Time zone" })).toBeInTheDocument();
    await user.click(await screen.findByRole("button", { name: "Retry" }));
    expect(await screen.findByText(/Next run:/)).toBeInTheDocument();
    expect(attempts).toBe(2);
  });

  it("renders preview validation failures without offering a transport retry", async () => {
    server.use(
      http.post("/api/v1/automations/schedule-preview", () =>
        HttpResponse.json(
          { error: { code: "INVALID_SCHEDULE", message: "Cron expression has no future occurrence." } },
          { status: 400 },
        ),
      ),
    );

    renderWithProviders(
      <AutomationScheduleEditor
        value={{
          frequency: "advanced",
          cronExpression: "0 0 31 2 *",
          timezone: "UTC",
        }}
        onChange={vi.fn()}
        detectedTimezone="UTC"
      />,
    );

    expect(
      await screen.findByText("Cron expression has no future occurrence."),
    ).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Retry" })).not.toBeInTheDocument();
  });

  it("uses a compact visual summary while preserving the full accessible weekday name", () => {
    renderWithProviders(
      <AutomationScheduleEditor
        value={{
          frequency: "weekly",
          weekdays: ["monday", "wednesday", "friday"],
          time: "09:00",
          timezone: "UTC",
        }}
        onChange={vi.fn()}
        detectedTimezone="UTC"
      />,
    );

    expect(screen.getByText("3 days")).toHaveClass("sm:hidden");
    expect(
      screen.getByRole("button", {
        name: "Days of week: Monday, Wednesday, and Friday",
      }),
    ).toBeInTheDocument();
  });
});

