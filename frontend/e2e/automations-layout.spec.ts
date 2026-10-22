import { expect, test } from "@playwright/test";
import type { Automation } from "../src/lib/types";

const automationNames = [
  "Weekly maintenance",
  `Maintenance${"WithoutSpaces".repeat(20)}`,
];

function automations(): Automation[] {
  return automationNames.map((name, index) => ({
    id: `fixture-automation-${index}`,
    org_id: "fixture-org",
    repository_id: "fixture-repo",
    name,
    goal: index === 0
      ? "Review the repository for maintenance opportunities and report the results. ".repeat(80)
      : `Review ${"UnbrokenReference".repeat(100)}`,
    scope: "",
    icon_type: "emoji",
    icon_value: "⚙️",
    execution_mode: "async",
    max_concurrent: 1,
    base_branch: "main",
    identity_scope: "org",
    publish_policy: "none",
    pre_pr_review_loops: 0,
    schedule_type: "interval",
    interval_value: 1,
    interval_unit: "days",
    interval_run_at: "09:00",
    timezone: "America/Argentina/Buenos_Aires",
    next_run_at: "2026-10-23T12:00:00Z",
    last_run_at: "2026-10-22T12:00:00Z",
    enabled: true,
    priority: 50,
    created_at: "2026-10-01T00:00:00Z",
    updated_at: "2026-10-01T00:00:00Z",
  }));
}

test("long automation text keeps metadata and actions within the list", async ({ context, page }, testInfo) => {
  await context.route("**/api/v1/**", async (route) => {
    const pathname = new URL(route.request().url()).pathname;
    let body: unknown = { data: [], meta: {} };
    if (pathname === "/api/v1/auth/me") {
      body = { data: { id: "fixture-user", org_id: "fixture-org", email: "fixture@example.com", name: "Fixture User", role: "admin", settings: {} } };
    } else if (pathname === "/api/v1/auth/memberships") {
      body = { data: { active_org_id: "fixture-org", active_role: "admin", memberships: [{ org_id: "fixture-org", org_name: "Fixture organization", role: "admin" }] } };
    } else if (pathname === "/api/v1/settings") {
      body = { data: {} };
    } else if (pathname === "/api/v1/automations") {
      body = { data: automations(), meta: {} };
    }
    await route.fulfill({ contentType: "application/json", body: JSON.stringify(body) });
  });
  await page.goto("/automations");
  const workspace = page.getByRole("region", { name: "Your automations", exact: true });
  await expect(workspace.getByRole("tab", { name: "All 2" })).toBeVisible();

  const widths = testInfo.project.use.isMobile ? [390] : [768, 1024, 1280, 1920];
  for (const width of widths) {
    await page.setViewportSize({ width, height: 1000 });
    const list = width < 768
      ? workspace.getByRole("list", { name: "Automations mobile list" })
      : workspace.getByRole("table", { name: "Automations", exact: true });
    await expect(list).toBeVisible();
    expect(await list.evaluate((element) => {
      const wrapper = element.closest('[data-slot="table-wrapper"]') ?? element;
      return wrapper.scrollWidth - wrapper.clientWidth;
    }), `List should fit without horizontal scrolling at ${width}px`).toBeLessThanOrEqual(1);
    for (const name of automationNames) {
      const row = width < 768
        ? list.getByRole("listitem").filter({ has: page.getByRole("link", { name, exact: true }) })
        : list.getByRole("row").filter({ has: page.getByRole("link", { name: new RegExp(name) }) });
      const actions = row.getByRole("button", { name: `More options for ${name}`, exact: true });
      await expect(actions).toBeInViewport();
      await actions.click({ trial: true });
      const bounds = await actions.boundingBox();
      expect(bounds?.x).toBeGreaterThanOrEqual(0);
      expect(bounds!.x + bounds!.width).toBeLessThanOrEqual(width);
      if (width >= 768) {
        const description = row.locator("p");
        expect(await description.evaluate((element) =>
          element.clientHeight <= 2 * Number.parseFloat(getComputedStyle(element).lineHeight) + 1),
        "Long descriptions should remain clamped to two lines").toBe(true);
      }
    }
    expect(await page.locator("main").evaluate((element) => element.scrollWidth - element.clientWidth),
      `Page should fit without horizontal scrolling at ${width}px`).toBeLessThanOrEqual(1);
    await workspace.screenshot({ path: testInfo.outputPath(`automations-${width}.png`), animations: "disabled" });
  }
});
