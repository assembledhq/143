import assert from "node:assert/strict";
import { mkdir, readFile, writeFile } from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { setTimeout } from "node:timers/promises";
import { chromium } from "@playwright/test";
import sharp from "sharp";

const frontendRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const baseURL = new URL(process.env.DEMO_CAPTURE_URL ?? "http://127.0.0.1:3000");
assert(["127.0.0.1", "localhost", "[::1]"].includes(baseURL.hostname), "Capture only an isolated local demo");
assert(!baseURL.username && !baseURL.password, "Capture URL must not contain credentials");
const outputDir = path.resolve(process.env.DEMO_CAPTURE_OUTPUT ?? path.join(frontendRoot, "public/product"));
const demoOrgID = "00000000-0000-4000-a000-000000000001";
const demoSessionID = "00000000-0000-4000-a000-000000000300";
const viewport = { width: 1440, height: 960 };
const browser = await chromium.launch({ headless: true });

try {
  const context = await browser.newContext({ viewport, deviceScaleFactor: 1, colorScheme: "light" });
  let nextRequestAt = Date.now();
  const apiFailures = [];
  // Remote avatars, previews, and integrations are not part of the fixture.
  await context.route("**/*", async (route) => {
    const url = new URL(route.request().url());
    if (url.origin !== baseURL.origin && !["data:", "blob:"].includes(url.protocol)) return route.abort();
    // GitHub credential/health services require provider configuration even in
    // demo mode. These two presentation fixtures represent the seeded PR and
    // synthetic account; they are not evidence of an external connection.
    if (url.pathname === "/api/v1/users/me/github-status") {
      return route.fulfill({ json: {
        connected: true, has_repo_scope: true, github_login: "ada-demo",
        pr_authorship_mode: "user_preferred", pr_draft_default: true,
        account_requirement: "recommended", needs_reconnect: false,
      } });
    }
    if (url.pathname === "/api/v1/pull-requests/00000000-0000-4000-a000-000000000501/health") {
      return route.fulfill({ json: { data: {
        pull_request_id: "00000000-0000-4000-a000-000000000501", pull_request_number: 42,
        repository: "assembledhq/143", url: "https://github.com/assembledhq/143/pull/42",
        status: "open", head_sha: "2222222222222222222222222222222222222222",
        base_sha: "1111111111111111111111111111111111111111", health_version: 1,
        sync_status: "synced", merge_state: "blocked", has_conflicts: false,
        failing_test_count: 1, needs_agent_action: true,
        github_state_synced_at: new Date().toISOString(),
        summary: "1 failing check needs agent action",
        checks: [
          { name: "frontend typecheck", category: "test", status: "failed", provider: "seeded-ci", summary: "Synthetic check: one generated type narrowed too far." },
          { name: "go test", category: "test", status: "passed", provider: "seeded-ci" },
          { name: "gosec", category: "lint", status: "passed", provider: "seeded-ci" },
        ], checks_confirmed: true, can_resolve_conflicts: false,
        can_fix_tests: false, can_merge: false, active_repairs: [],
        enrichment_status: "ready", enrichment_requested: false, enrichment_ready: true,
        conflict_detail_available: true, failing_test_detail_available: true,
        merge_when_ready: { state: "off" },
      } } });
    }
    if (url.pathname.startsWith("/api/") && route.request().method() === "GET"
        && !/(?:stream|events)$/.test(url.pathname)) {
      // Stay below the default local IP limit without changing API settings.
      const startAt = Math.max(Date.now(), nextRequestAt);
      nextRequestAt = startAt + 120;
      await setTimeout(Math.max(0, startAt - Date.now()));
      let response = await route.fetch({ maxRedirects: 0 });
      for (let attempt = 0; response.status() === 429 && attempt < 2; attempt++) {
        await setTimeout(1500);
        response = await route.fetch({ maxRedirects: 0 });
      }
      if (response.status() >= 500 || response.status() === 429) apiFailures.push(url.pathname);
      return route.fulfill({ response });
    }
    return route.continue();
  });
  const providersResponse = await context.request.get(new URL("/api/v1/auth/providers", baseURL).href);
  assert(providersResponse.ok(), "Demo authentication metadata must be available");
  const { data: providers } = await providersResponse.json();
  assert.equal(providers.demo, true, "API must explicitly enable demo mode");
  assert.equal(providers.demo_email, "preview-admin@143.dev", "Use only the canonical synthetic identity");
  assert.equal(providers.demo_password, "preview", "Use only the public fixture login");
  const csrfCookie = (await context.cookies(baseURL.href)).find((cookie) => cookie.name === "csrf_token");
  assert(csrfCookie, "Demo login must use the application's CSRF protection");
  const login = await context.request.post(new URL("/api/v1/auth/login", baseURL).href, {
    headers: { "X-CSRF-Token": csrfCookie.value },
    data: { email: providers.demo_email, password: providers.demo_password },
  });
  assert(login.ok(), "Canonical demo login must succeed");
  const me = await context.request.get(new URL("/api/v1/auth/me", baseURL).href);
  const { data: user } = await me.json();
  assert.equal(user.email, "preview-admin@143.dev");
  assert.equal(user.name, "Ada Lovelace");
  assert.equal(user.org_id, demoOrgID);
  const sessionResponse = await context.request.get(new URL(`/api/v1/sessions/${demoSessionID}`, baseURL).href);
  assert(sessionResponse.ok(), "Canonical demo session must be present");
  const { data: session } = await sessionResponse.json();
  assert.equal(session.org_id, demoOrgID);
  assert.equal(session.id, demoSessionID);

  const page = await context.newPage();
  page.setDefaultTimeout(30000);
  await mkdir(outputDir, { recursive: true });
  const captures = [];
  const forbidden = /\.slack\.com\/archives\/|linear\.app\/[^/]+\/issue\/|github_pat_[\w]+|gh[pousr]_[\w]{20,}|xox[baprs]-[\w-]{20,}|AGE-SECRET-KEY-|-----BEGIN [A-Z ]*PRIVATE KEY-----/;

  async function open(route, readyText) {
    await page.goto(new URL(route, baseURL).href, { waitUntil: "domcontentloaded" });
    await page.getByText(readyText, { exact: false }).first().waitFor({ state: "visible" });
    await page.evaluate(() => document.fonts.ready);
  }

  async function capture(stem, { sourcePNG = false, clip } = {}) {
    assert.deepEqual(apiFailures, [], "Demo data requests must complete before capture");
    await page.locator("img").evaluateAll(async (images) => {
      await Promise.all(images.filter((img) => new URL(img.src).origin === location.origin).map((img) => img.decode()));
    });
    await page.mouse.move(viewport.width - 1, viewport.height - 1);
    await page.evaluate(() => {
      if (document.activeElement instanceof HTMLElement) document.activeElement.blur();
    });
    const text = await page.locator("body").innerText();
    assert(!forbidden.test(text), "Rendered page must not contain private provenance or credential markers");
    assert(!/Application error:|Internal Server Error/.test(text), "Capture must show the application");
    const png = await page.screenshot({ animations: "disabled", caret: "hide", type: "png", clip });
    if (sourcePNG) await writeFile(path.join(outputDir, `${stem}.png`), png);
    await sharp(png).webp({ quality: 90 }).toFile(path.join(outputDir, `${stem}.webp`));
    const { width, height } = await sharp(png).metadata();
    captures.push({ stem, route: new URL(page.url()).pathname, viewport, imageSize: { width, height }, sourcePNG });
  }

  await open("/settings/integrations", "GitHub App");
  await page.getByText("assembledhq/example-service", { exact: true }).waitFor({ state: "visible" });
  await page.getByText("ada-demo", { exact: false }).first().waitFor({ state: "visible" });
  await page.getByText(/^Loading (routing|channel) summary\.\.\.$/).filter({ visible: true }).first().waitFor({ state: "hidden" });
  await capture("product-integrations", { sourcePNG: true });
  const firstGitHubRow = await page.getByTestId("integration-card").nth(0).boundingBox();
  const secondGitHubRow = await page.getByTestId("integration-card").nth(1).boundingBox();
  assert(firstGitHubRow && secondGitHubRow, "Both synthetic GitHub connection rows must be visible");
  await capture("docs-github-app", { clip: {
    x: firstGitHubRow.x, y: firstGitHubRow.y, width: firstGitHubRow.width,
    height: secondGitHubRow.y + secondGitHubRow.height - firstGitHubRow.y,
  } });

  await open("/sessions", "Ship PR preview auto-teardown");
  await page.getByText("Ship PR preview auto-teardown", { exact: true }).click();
  await page.getByText("Plan: inject preview manager", { exact: false }).waitFor({ state: "visible" });
  await capture("product-sessions-list", { sourcePNG: true });
  await capture("docs-sessions-list");

  await open(`/sessions/${demoSessionID}`, "Ship PR preview auto-teardown");
  await page.getByText("Plan: inject preview manager", { exact: false }).waitFor({ state: "visible" });
  await page.getByRole("tab", { name: "Overview", exact: true }).click();
  await page.getByText("Loading PR health...", { exact: true }).waitFor({ state: "hidden" });
  await capture("product-session-overview", { sourcePNG: true });
  await capture("docs-session-overview");
  await page.getByRole("tab", { name: /^Changes/ }).click();
  await page.getByText("recycler.go", { exact: false }).first().waitFor({ state: "visible" });
  await page.getByText("recycler.go", { exact: true }).first().click();
  await page.getByText("StopPreview", { exact: false }).filter({ visible: true }).first().waitFor({ state: "visible" });
  await capture("product-review-diff", { sourcePNG: true });
  await capture("product-review-changes", { sourcePNG: true });
  await capture("docs-session-changes");

  await open("/previews", "Previews");
  await page.getByText("feat/preview-teardown", { exact: false }).first().waitFor({ state: "visible" });
  await capture("product-session-preview", { sourcePNG: true });
  await capture("docs-session-preview");

  // Only synthetic, reusable provenance is written beside the public assets.
  const seed = await readFile(path.join(frontendRoot, "../.143/seed/10_identity.sql"), "utf8");
  assert(seed.includes("Ada Lovelace"), "Capture identity must remain tied to the canonical seed");
  await writeFile(path.join(outputDir, "captures.json"), JSON.stringify({
    source: "isolated DEMO_MODE API and canonical .143/seed data, with synthetic GitHub account and PR-health presentation fixtures",
    synthetic: true,
    identity: "Ada Lovelace",
    captures,
  }, null, 2) + "\n");
  console.log(`Captured ${captures.length} synthetic views and their PNG/WebP variants.`);
} finally {
  await browser.close();
}
