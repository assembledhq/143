import { expect, test } from "@playwright/test";
import type { CodeReviewAnalytics, CodeReviewAnalyticsTrendBucket } from "../src/lib/types";

test.use({ timezoneId: "America/New_York" });

function bucket(start: string, end: string, prs: number, approved: number, rounds: number): CodeReviewAnalyticsTrendBucket {
  return {
    start, end, prs_reviewed: prs, approved_by_143: approved,
    median_rounds_to_approval: rounds, average_rounds_to_approval: rounds,
    p95_rounds_to_approval: Math.ceil(rounds), partial: false, overflow_prs: 0,
  };
}

function report(): CodeReviewAnalytics {
  return {
    summary: {
      prs_reviewed: 5, prs_with_completed_round: 5, approved_by_143: 3,
      not_approved: 2, approved_first_round: 1, median_rounds_to_approval: 2,
      average_rounds_to_approval: 2, p95_rounds_to_approval: 3,
      needs_human_review: 2, comment_only: 0, blocked: 0, approval_not_posted: 0,
      prs_with_failed_attempt: 0, prs_with_stale_attempt: 0,
      prs_with_change_breakdown: 0, median_additions: null, median_deletions: null,
      prs_with_findings: 0, prs_with_blocking_findings: 0, total_findings: 0,
    },
    approval_rounds: [
      { bucket: "round_1", prs: 1 }, { bucket: "round_2", prs: 1 },
      { bucket: "round_3", prs: 1 }, { bucket: "round_4_plus", prs: 0 },
      { bucket: "not_yet_approved", prs: 2 },
    ],
    authors: [], non_approval_reasons: [], comment_requests_total: 0, comment_requests_by_user: [],
    trend: {
      status: "available", mode: "finite", generated_at: "2026-10-03T04:00:00Z",
      bucket_width_seconds: 86400,
      current_window: { start: "2026-10-01T04:00:00Z", end: "2026-11-01T04:00:00Z", observed_end: "2026-10-03T04:00:00Z" },
      previous_window: { start: "2026-09-01T04:00:00Z", end: "2026-10-01T04:00:00Z", compared_end: "2026-09-03T04:00:00Z" },
      observed_end_advanced_by_data: false, overflow_prs: 0, latest_included_first_requested_at: "2026-10-02T15:00:00Z",
      points: [
        { index: 0, current: bucket("2026-10-01T04:00:00Z", "2026-10-02T04:00:00Z", 2, 1, 1), previous: bucket("2026-09-01T04:00:00Z", "2026-09-02T04:00:00Z", 2, 1, 2), unequal_exposure: false },
        { index: 1, current: bucket("2026-10-02T04:00:00Z", "2026-10-03T04:00:00Z", 3, 2, 2.5), previous: bucket("2026-09-02T04:00:00Z", "2026-09-03T04:00:00Z", 4, 2, 3), unequal_exposure: false },
      ],
    },
  };
}

test("selects Analytics metrics without another request and exposes comparison data", async ({ context, page }, testInfo) => {
  const pageErrors: string[] = [];
  const analyticsRequests: string[] = [];
  page.on("pageerror", (error) => pageErrors.push(error.message));
  await context.route("**/api/v1/**", async (route) => {
    const url = new URL(route.request().url());
    let body: unknown = { data: [], meta: {} };
    if (url.pathname === "/api/v1/auth/me") {
      body = { data: { id: "fixture-user", org_id: "fixture-org", email: "fixture@example.com", name: "Fixture User", role: "admin", settings: {} } };
    } else if (url.pathname === "/api/v1/auth/memberships") {
      body = { data: { active_org_id: "fixture-org", active_role: "admin", memberships: [{ org_id: "fixture-org", org_name: "Fixture organization", role: "admin" }] } };
    } else if (url.pathname === "/api/v1/settings") {
      body = { data: {} };
    } else if (url.pathname === "/api/v1/code-reviews/policy") {
      body = { data: null };
    } else if (url.pathname === "/api/v1/code-reviews/stats") {
      body = { data: { reviews_completed: 5, automatically_approved: 3, needs_human_review: 2, median_turnaround_seconds: 60 } };
    } else if (url.pathname === "/api/v1/code-reviews/analytics") {
      analyticsRequests.push(route.request().url());
      body = { data: report() };
    }
    await route.fulfill({ contentType: "application/json", body: JSON.stringify(body) });
  });
  await page.goto("/code-reviews?tab=analytics&range=this_month");
  const chart = page.getByLabel("Code review analytics trend", { exact: true });
  const average = page.getByRole("button", { name: /^Show Average rounds to approval trend/ });
  await expect(average).toHaveAttribute("aria-pressed", "true");
  expect((await average.boundingBox())?.height).toBeGreaterThan(80);
  await expect(chart.getByText("Current · Oct 1–2", { exact: true })).toBeVisible();
  await expect(chart.getByText("Previous · Sep 1–2", { exact: true })).toBeVisible();
  await expect(chart.getByText(/Current requested range:/)).not.toBeVisible();
  const before = analyticsRequests.length;
  const p95 = page.getByRole("button", { name: /^Show P95 rounds to approval trend/ });
  await p95.click();
  await expect(p95).toHaveAttribute("aria-pressed", "true");
  await expect(p95).toBeFocused();
  await expect(page).toHaveURL(/analytics_metric=p95_rounds_to_approval/);
  const chartHeading = chart.getByRole("heading", { name: "P95 rounds to approval trend", exact: true });
  await expect(chartHeading).toBeVisible();
  await expect(chartHeading).toBeInViewport();
  expect(analyticsRequests.length).toBe(before);
  if (testInfo.project.use.hasTouch) {
    await chart.locator("circle.recharts-dot").first().tap();
    await expect(chart.locator('[aria-live="polite"]')).toContainText("Current · Oct 1");
    await expect(chart.locator('[aria-live="polite"]')).toContainText("Previous · Sep 1");
    await expect(chart.locator('[aria-live="polite"]')).toContainText("1 approved PR");
  } else {
    const plot = chart.getByRole("application");
    await plot.focus();
    await plot.press("ArrowRight");
    await expect(chart.locator(".recharts-tooltip-wrapper")).toBeVisible();
    await expect(chart.locator(".recharts-tooltip-wrapper")).toContainText("approved PR");
    await expect(chart.locator(".recharts-tooltip-wrapper")).not.toContainText("end exclusive");
  }
  await chart.getByRole("button", { name: "About this chart" }).click();
  await expect(chart.getByText(/Current requested range:/)).toBeVisible();
  await chart.getByRole("button", { name: "About this chart" }).click();
  await chart.getByRole("button", { name: "Show trend data" }).click();
  const table = chart.getByRole("table", { name: "P95 rounds to approval trend data" });
  await expect(table).toBeVisible();
  await expect(table.getByRole("row")).toHaveCount(5);
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
  for (const theme of ["light", "dark"] as const) {
    await page.evaluate((value) => {
      document.documentElement.classList.toggle("dark", value === "dark");
      document.documentElement.style.colorScheme = value;
    }, theme);
    await chart.getByRole("button", { name: "Hide trend data" }).click();
    await chart.screenshot({ path: testInfo.outputPath(`analytics-${theme}.png`), animations: "disabled" });
    await chart.getByRole("button", { name: "Show trend data" }).click();
  }
  await page.reload();
  await expect(page.getByRole("button", { name: /^Show P95 rounds to approval trend/ })).toHaveAttribute("aria-pressed", "true");
  expect(pageErrors).toEqual([]);
});
