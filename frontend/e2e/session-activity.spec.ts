import { expect, test, type BrowserContext } from "@playwright/test";

let activityDetail: "compact" | "detailed" = "compact";

async function installFixtureRoutes(context: BrowserContext) {
  await context.route("**/api/v1/application-config", async (route) => {
    await route.fulfill({
      contentType: "application/json",
      headers: { "cache-control": "no-store" },
      body: JSON.stringify({ data: { session_activity_capsules_enabled: true, revision: "fixture-on", updated_at: "2026-08-03T12:00:00Z" } }),
    });
  });
  await context.route("**/api/v1/auth/me/settings", async (route) => {
    const request = route.request().postDataJSON() as { session_activity_detail?: "compact" | "detailed" };
    if (request.session_activity_detail) activityDetail = request.session_activity_detail;
    await route.fulfill({
      contentType: "application/json",
      headers: { "cache-control": "no-store" },
      body: JSON.stringify({
        data: {
          id: "fixture-user",
          org_id: "fixture-org",
          email: "fixture@example.com",
          name: "Fixture User",
          settings: { session_activity_detail: activityDetail },
        },
      }),
    });
  });
  await context.route("**/api/v1/auth/me", async (route) => {
    await route.fulfill({
      contentType: "application/json",
      headers: { "cache-control": "no-store" },
      body: JSON.stringify({
        data: {
          id: "fixture-user",
          org_id: "fixture-org",
          email: "fixture@example.com",
          name: "Fixture User",
          settings: { session_activity_detail: activityDetail },
        },
      }),
    });
  });
  await context.route("**/api/v1/session-activity-events", async (route) => {
    await route.fulfill({ status: 204 });
  });
}

test.beforeEach(async ({ context, page }) => {
  activityDetail = "compact";
  await installFixtureRoutes(context);
  await context.addInitScript(() => {
    class FixtureEventSource {
      static instances: FixtureEventSource[] = [];
      static readonly CONNECTING = 0;
      static readonly OPEN = 1;
      static readonly CLOSED = 2;
      readonly CONNECTING = 0;
      readonly OPEN = 1;
      readonly CLOSED = 2;
      readonly url: string;
      readonly withCredentials = false;
      readyState = FixtureEventSource.OPEN;
      onopen: ((event: Event) => void) | null = null;
      onmessage: ((event: MessageEvent) => void) | null = null;
      onerror: ((event: Event) => void) | null = null;
      private listeners = new Map<string, Array<(event: MessageEvent) => void>>();
      constructor(url: string | URL) {
        this.url = String(url);
        FixtureEventSource.instances.push(this);
      }
      addEventListener(event: string, listener: EventListenerOrEventListenerObject) {
        const handler = typeof listener === "function"
          ? listener as (event: MessageEvent) => void
          : (message: MessageEvent) => listener.handleEvent(message);
        this.listeners.set(event, [...(this.listeners.get(event) ?? []), handler]);
      }
      removeEventListener(event: string, listener: EventListenerOrEventListenerObject) {
        const handler = typeof listener === "function" ? listener : listener.handleEvent;
        this.listeners.set(event, (this.listeners.get(event) ?? []).filter((candidate) => candidate !== handler));
      }
      dispatchEvent() { return true; }
      close() { this.readyState = FixtureEventSource.CLOSED; }
      emit(event: string, data: unknown) {
        const message = new MessageEvent(event, { data: JSON.stringify(data) });
        for (const listener of this.listeners.get(event) ?? []) listener(message);
      }
    }
    Object.defineProperty(window, "EventSource", { configurable: true, writable: true, value: FixtureEventSource });
    Object.assign(window, {
      __emitSessionActivityFixtureEvent: (event: string, data: unknown) => {
        for (const source of FixtureEventSource.instances) source.emit(event, data);
      },
      __failSessionActivityFixtureStream: () => {
        for (const source of FixtureEventSource.instances) source.onerror?.(new Event("error"));
      },
    });
  });
  await page.goto("/session-activity-e2e");
});

test("reconciles duplicate, out-of-order, and missed lifecycle SSE events through the durable transcript API", async ({ page }) => {
  const phaseOneID = "10000000-0000-0000-0000-000000000001";
  const phaseTwoID = "10000000-0000-0000-0000-000000000002";
  const toolEntry = (id: number, phaseID: string, createdAt: string) => ({
    kind: "tool_group",
    transcriptEntryId: `tuse_${id}`,
    toolUse: {
      id, session_id: "fixture-session", thread_id: "fixture-thread", level: "tool_use",
      message: "Running npm test", metadata: { type: "tool_use", tool: "shell", input: { command: "npm test" } },
      turn_number: 1, created_at: createdAt, message_bytes: 16, message_chars: 16,
      message_truncated: false, activity_phase_id: phaseID,
    },
  });
  const finalEntry = {
    kind: "message",
    transcriptEntryId: "msg_10",
    data: {
      id: 10, session_id: "fixture-session", org_id: "fixture-org", thread_id: "fixture-thread",
      turn_number: 1, role: "assistant", content: "Durable final response.",
      created_at: "2026-08-03T12:00:06Z", activity_phase_id: phaseOneID,
    },
  };
  let snapshot: { entries: unknown[]; turns: unknown[]; is_running: boolean } = {
    entries: [toolEntry(1, phaseOneID, "2026-08-03T12:00:01Z")],
    turns: [{ turn_number: 1, started_at: "2026-08-03T12:00:00Z", entries: [], phases: [{
      id: phaseOneID, anchor_id: `aph_${phaseOneID}`, phase_number: 1, trigger_kind: "initial",
      status: "running", started_at: "2026-08-03T12:00:00Z", tool_call_count: 1,
    }] }],
    is_running: true,
  };
  let transcriptRequests = 0;
  await page.route("**/api/v1/session-activity-e2e/transcript", async (route) => {
    transcriptRequests += 1;
    await route.fulfill({ contentType: "application/json", body: JSON.stringify(snapshot) });
  });
  await page.goto("/session-activity-e2e?api-lifecycle=1");
  await expect(page.getByRole("button", { name: /Working for .*1 tool call/ })).toBeVisible();
  const initialRequests = transcriptRequests;

  snapshot = {
    entries: [toolEntry(1, phaseOneID, "2026-08-03T12:00:01Z"), finalEntry],
    turns: [{ turn_number: 1, started_at: "2026-08-03T12:00:00Z", entries: [], phases: [{
      id: phaseOneID, anchor_id: `aph_${phaseOneID}`, phase_number: 1, trigger_kind: "initial",
      status: "completed", boundary_reason: "final_response", started_at: "2026-08-03T12:00:00Z",
      completed_at: "2026-08-03T12:00:06Z", tool_call_count: 1,
    }] }],
    is_running: false,
  };
  await page.evaluate(() => {
    const emit = (window as unknown as { __emitSessionActivityFixtureEvent: (event: string, data: unknown) => void }).__emitSessionActivityFixtureEvent;
    const event = { id: "terminal-2", session_id: "fixture-session", org_id: "fixture-org", thread_id: "fixture-thread", emitted_at: new Date().toISOString(), data: {} };
    emit("session_activity_phase.terminal", event);
    emit("session_activity_phase.terminal", event);
  });
  await expect(page.getByText("Durable final response.")).toBeVisible();
  await expect.poll(() => transcriptRequests).toBe(initialRequests + 1);

  await page.evaluate(() => {
    (window as unknown as { __emitSessionActivityFixtureEvent: (event: string, data: unknown) => void }).__emitSessionActivityFixtureEvent(
      "session_activity_phase.started",
      { id: "started-1", session_id: "fixture-session", org_id: "fixture-org", thread_id: "fixture-thread", emitted_at: new Date().toISOString(), data: {} },
    );
  });
  await expect.poll(() => transcriptRequests).toBe(initialRequests + 2);

  snapshot = {
    entries: [finalEntry, toolEntry(2, phaseTwoID, "2026-08-03T12:00:08Z")],
    turns: [{ turn_number: 1, started_at: "2026-08-03T12:00:00Z", entries: [], phases: [{
      id: phaseOneID, anchor_id: `aph_${phaseOneID}`, phase_number: 1, trigger_kind: "initial",
      status: "interrupted", boundary_reason: "runtime_lost", started_at: "2026-08-03T12:00:00Z",
      completed_at: "2026-08-03T12:00:06Z", tool_call_count: 1,
    }, {
      id: phaseTwoID, anchor_id: `aph_${phaseTwoID}`, phase_number: 2, trigger_kind: "recovery",
      status: "running", started_at: "2026-08-03T12:00:08Z", tool_call_count: 1,
    }] }],
    is_running: true,
  };
  await page.evaluate(() => {
    (window as unknown as { __failSessionActivityFixtureStream: () => void }).__failSessionActivityFixtureStream();
  });
  await expect(page.getByText("Runtime recovered and execution resumed.")).toBeVisible();
  await expect.poll(() => transcriptRequests).toBe(initialRequests + 3);
});

test("streams an active phase and collapses only after its final boundary renders", async ({ page }) => {
  const capsule = page.getByRole("button", { name: /Working for .*1 tool call/ });
  await expect(capsule).toHaveAttribute("aria-expanded", "true");
  await expect(page.getByText("Running npm test")).toBeVisible();
  await page.getByRole("button", { name: "Complete phase" }).click();
  await expect(page.getByText("Implemented the transcript fix.")).toBeVisible();
  await expect(page.getByRole("button", { name: /Worked for 6s .*1 tool call/ })).toHaveAttribute("aria-expanded", "false");
});

test("manual inspection protects a terminal phase and disclosure is keyboard operable", async ({ page }) => {
  const capsule = page.getByRole("button", { name: /Working for .*1 tool call/ });
  await capsule.press("Enter");
  await expect(capsule).toHaveAttribute("aria-expanded", "false");
  await capsule.press("Space");
  await expect(capsule).toHaveAttribute("aria-expanded", "true");
  await page.getByText("Running npm test").click();
  await page.getByRole("button", { name: "Complete phase" }).click();
  await expect(page.getByRole("button", { name: /Worked for 6s .*1 tool call/ })).toHaveAttribute("aria-expanded", "true");
});

test("keeps queued steering out of the transcript until it is applied", async ({ page }) => {
  // Anchor on content the fixture does render before asserting absence:
  // toHaveCount(0) resolves immediately against a page that has not painted
  // yet, so unanchored absence checks would pass even if nothing loaded.
  await expect(page.getByText("Fix transcript scrolling")).toBeVisible();
  await expect(page.getByRole("button", { name: /^Working for/ })).toBeVisible();

  await expect(page.getByText("Queued")).toHaveCount(0);
  await expect(page.getByText("Also preserve anchors")).toHaveCount(0);
  await expect(page.getByText("Keep day separators stable")).toHaveCount(0);
  await page.getByRole("button", { name: "Acknowledge steering" }).click();
  await expect(page.getByText("Also preserve anchors")).toBeVisible();
  await expect(page.getByText("Keep day separators stable")).toBeVisible();
  await expect(page.getByRole("button", { name: /Worked for 6s .*1 tool call/ })).toBeVisible();
  await expect(page.getByRole("button", { name: /^Working for/ })).toHaveCount(1);
  await expect(page.getByRole("button", { name: /^Working for/ })).toHaveAttribute("aria-expanded", "true");
});

test("renders separate phases around a visible human-input boundary", async ({ page }) => {
  await page.getByRole("button", { name: "Request human input" }).click();
  await expect(page.getByText("Should restored anchors expand their activity?")).toBeVisible();
  await expect(page.getByRole("button", { name: /Worked for 6s .*1 tool call/ })).toBeVisible();

  await page.getByRole("button", { name: "Answer human input" }).click();
  await expect(page.getByText("Yes, expand before scrolling.")).toBeVisible();
  await expect(page.getByRole("button", { name: /^Working for/ })).toHaveCount(1);
});

test("renders interruption and recovery as separate capsules including a zero-tool phase", async ({ page }) => {
  await page.getByRole("button", { name: "Interrupt phase" }).click();
  await expect(page.getByText("Execution paused for maintenance.")).toBeVisible();
  await expect(page.getByRole("button", { name: /Interrupted after 6s .*1 tool call/ })).toBeVisible();

  await page.getByRole("button", { name: "Resume phase" }).click();
  await expect(page.getByText("Runtime recovered and execution resumed.")).toBeVisible();
  const resumed = page.getByRole("button", { name: /^Working for/ });
  await expect(resumed).toBeVisible();
  await expect(resumed).not.toContainText("tool call");
});

test("historical fallback remains inspectable without invented duration", async ({ page }) => {
  await page.getByRole("button", { name: "Show historical activity" }).click();
  const historical = page.getByRole("button", { name: /Activity .*1 tool call/ });
  await expect(historical).toBeVisible();
  await expect(historical).not.toContainText("Worked for");
  await historical.click();
  await expect(page.getByText("Ran `cat transcript.json`")).toBeVisible();
});

test("prepends an older transcript window without moving the visible position", async ({ page }) => {
  const marker = page.getByTestId("current-transcript-marker");
  const before = await marker.boundingBox();
  expect(before).not.toBeNull();

  await page.getByRole("button", { name: "Load older activity" }).click();
  await expect(page.getByTestId("older-activity-row")).toHaveCount(8);

  const after = await marker.boundingBox();
  expect(after).not.toBeNull();
  expect(Math.abs((after?.y ?? 0) - (before?.y ?? 0))).toBeLessThan(2);
});

test("persists detailed mode across reload, a second browser context, and expands a deep-linked child", async ({ browser, page }) => {
  await Promise.all([
    page.waitForResponse((response) => response.url().includes("/api/v1/auth/me") && response.request().method() === "PATCH"),
    page.getByRole("button", { name: "Activity detail: compact" }).click(),
  ]);
  await page.reload();
  await expect(page.getByRole("button", { name: "Activity detail: detailed" })).toBeVisible();

  const secondContext = await browser.newContext();
  await installFixtureRoutes(secondContext);
  const secondPage = await secondContext.newPage();
  await secondPage.goto("/session-activity-e2e");
  await expect(secondPage.getByRole("button", { name: "Activity detail: detailed" })).toBeVisible();
  await secondContext.close();

  await page.getByRole("button", { name: "Complete phase" }).click();
  await expect(page.getByRole("button", { name: /Worked for/ })).toHaveAttribute("aria-expanded", "true");
  await page.goto("/session-activity-e2e#tuse_1");
  await expect(page.getByText("Ran `npm test`")).toBeVisible();
});

test("restores the legacy flat renderer with the emergency switch", async ({ page }) => {
  await page.getByRole("button", { name: "Capsules: on" }).click();
  await expect(page.getByRole("button", { name: /Working for/ })).toHaveCount(0);
  await expect(page.getByText("Ran `npm test`")).toBeVisible();
});

test("refreshes the emergency switch on initial load, focus, and the freshness interval", async ({ context, page }) => {
  await context.unroute("**/api/v1/application-config");
  let enabled = false;
  await context.route("**/api/v1/application-config", async (route) => {
    await route.fulfill({
      contentType: "application/json",
      headers: { "cache-control": "no-store" },
      body: JSON.stringify({ data: { session_activity_capsules_enabled: enabled, revision: enabled ? "fixture-on" : "fixture-off", updated_at: "2026-08-03T12:00:00Z" } }),
    });
  });
  await page.clock.install();
  await page.reload();
  await expect(page.getByRole("button", { name: /Working for/ })).toHaveCount(0);

  enabled = true;
  await Promise.all([
    page.waitForResponse((response) => response.url().includes("/api/v1/application-config")),
    page.evaluate(() => window.dispatchEvent(new Event("visibilitychange"))),
  ]);
  await expect(page.getByRole("button", { name: /Working for/ })).toBeVisible();

  enabled = false;
  await Promise.all([
    page.waitForResponse((response) => response.url().includes("/api/v1/application-config")),
    page.clock.fastForward(30_001),
  ]);
  await expect(page.getByRole("button", { name: /Working for/ })).toHaveCount(0);
});

test("keeps capsule controls usable in dark theme without horizontal overflow", async ({ page }) => {
  await page.getByRole("button", { name: "Toggle theme" }).click();
  await expect(page.locator("html")).toHaveClass(/dark/);
  const capsule = page.getByRole("button", { name: /Working for .*1 tool call/ });
  await expect(capsule).toBeVisible();
  const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
  expect(overflow).toBeLessThanOrEqual(1);
});
