import { defineConfig, devices } from "@playwright/test";

export default defineConfig({
  testDir: "./e2e",
  timeout: 30_000,
  fullyParallel: true,
  retries: process.env.CI ? 1 : 0,
  reporter: process.env.CI ? [["github"], ["html", { open: "never" }]] : "list",
  use: {
    baseURL: "http://127.0.0.1:3210",
    trace: "retain-on-failure",
    screenshot: "only-on-failure",
    video: "retain-on-failure",
  },
  projects: [
    { name: "chromium-desktop", use: { ...devices["Desktop Chrome"] } },
    { name: "chromium-mobile", use: { ...devices["Pixel 7"] } },
    ...(process.env.SESSION_ACTIVITY_BROWSER_MATRIX === "1" ? [
      { name: "webkit-desktop", use: { ...devices["Desktop Safari"] } },
      { name: "webkit-mobile", use: { ...devices["iPhone 15"] } },
    ] : []),
  ],
  webServer: {
    // The repository-wide Turbopack root intentionally sits above the
    // frontend package for standalone preview builds, but local Next dev then
    // resolves `next` from that root instead of frontend/node_modules. Webpack
    // keeps this deterministic fixture package-local in CI and sandboxes.
    command: "npm run dev -- --webpack --hostname 127.0.0.1 --port 3210",
    url: "http://127.0.0.1:3210/session-activity-e2e",
    reuseExistingServer: !process.env.CI,
    env: { SESSION_ACTIVITY_E2E_FIXTURE: "1" },
  },
});
