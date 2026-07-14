import { describe, expect, it } from "vitest";
import type { Automation } from "@/lib/types";
import { formatAutomationSchedule } from "./schedule-time";

function automation(overrides: Partial<Automation>): Automation {
  return {
    id: "auto-1",
    live_version: 1,
    org_id: "org-1",
    name: "Automation",
    goal: "Do work",
    icon_type: "emoji",
    icon_value: "⚙️",
    execution_mode: "async",
    max_concurrent: 1,
    base_branch: "main",
    identity_scope: "org",
    pre_pr_review_loops: 0,
    schedule_type: "interval",
    enabled: true,
    timezone: "UTC",
    priority: 50,
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
    ...overrides,
  };
}

describe("formatAutomationSchedule", () => {
  it.each([
    {
      name: "formats event-only automations as having no schedule",
      input: automation({
        schedule_type: "none",
        interval_value: undefined,
        interval_unit: undefined,
        interval_run_at: undefined,
      }),
      expected: "No schedule",
    },
    {
      name: "hides the anchor time for hourly automations",
      input: automation({
        interval_value: 1,
        interval_unit: "hours",
        interval_run_at: "10:00",
        timezone: "America/New_York",
      }),
      expected: "Every hour",
    },
    {
      name: "hides the anchor time for multi-hour automations",
      input: automation({
        interval_value: 6,
        interval_unit: "hours",
        interval_run_at: "10:00",
        timezone: "America/New_York",
      }),
      expected: "Every 6 hours",
    },
    {
      name: "treats a 24 hour interval as daily and keeps the run time",
      input: automation({
        interval_value: 24,
        interval_unit: "hours",
        interval_run_at: "10:00",
        timezone: "America/New_York",
      }),
      expected: "Daily at 10:00 AM (America/New_York)",
    },
    {
      name: "formats one day as daily with the run time",
      input: automation({
        interval_value: 1,
        interval_unit: "days",
        interval_run_at: "09:15",
        timezone: "America/Los_Angeles",
      }),
      expected: "Daily at 9:15 AM (America/Los_Angeles)",
    },
    {
      name: "formats multi-day intervals with the run time",
      input: automation({
        interval_value: 2,
        interval_unit: "days",
        interval_run_at: "09:15",
        timezone: "America/Los_Angeles",
      }),
      expected: "Every 2 days at 9:15 AM (America/Los_Angeles)",
    },
    {
      name: "formats weekly intervals with the run time",
      input: automation({
        interval_value: 1,
        interval_unit: "weeks",
        interval_run_at: "09:15",
        timezone: "UTC",
      }),
      expected: "Every week at 9:15 AM (UTC)",
    },
    {
      name: "keeps cron expressions explicit",
      input: automation({
        schedule_type: "cron",
        cron_expression: "0 9 * * 1",
        timezone: "UTC",
      }),
      expected: "cron: 0 9 * * 1 (UTC)",
    },
  ])("$name", ({ input, expected }) => {
    expect(formatAutomationSchedule(input)).toBe(expected);
  });
});
