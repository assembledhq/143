import { describe, expect, it } from "vitest";
import {
  buildDocumentTitle,
  resolvePageTitle,
  sanitizePageTitle,
} from "./page-title";

describe("buildDocumentTitle", () => {
  it("prefixes page titles with the product name", () => {
    expect(buildDocumentTitle("Sessions")).toBe("143 | Sessions");
  });

  it("falls back to the product name when no page title is available", () => {
    expect(buildDocumentTitle("")).toBe("143");
    expect(buildDocumentTitle(null)).toBe("143");
  });

  it("normalizes whitespace before composing the browser title", () => {
    expect(buildDocumentTitle("  Fix   mobile\nsession title  ")).toBe(
      "143 | Fix mobile session title",
    );
  });
});

describe("resolvePageTitle", () => {
  it("returns explicit titles for primary routes", () => {
    expect(resolvePageTitle("/sessions")).toBe("Sessions");
    expect(resolvePageTitle("/sessions/new")).toBe("New session");
    expect(resolvePageTitle("/autopilot/decisions")).toBe("Autopilot decisions");
    expect(resolvePageTitle("/settings/audit-log")).toBe("Audit log");
    expect(resolvePageTitle("/settings/runtime")).toBe("Runtime settings");
    expect(resolvePageTitle("/settings/integrations/github/setup")).toBe("GitHub setup");
  });

  it("returns stable entity fallbacks for dynamic detail routes", () => {
    expect(resolvePageTitle("/sessions/session-abcdef12-3456")).toBe("Session");
    expect(resolvePageTitle("/projects/proj-1")).toBe("Project");
    expect(resolvePageTitle("/repositories/repo-1")).toBe("Repository");
    expect(resolvePageTitle("/settings/evals/eval-1")).toBe("Eval");
    expect(resolvePageTitle("/settings/evals/batch/batch-1")).toBe("Eval batch");
  });

  it("derives a readable fallback for future routes", () => {
    expect(resolvePageTitle("/settings/billing-profiles")).toBe("Billing profiles");
    expect(resolvePageTitle("/ops/release-gates/gate-1")).toBe("Release gates");
  });

  it("handles empty and root paths", () => {
    expect(resolvePageTitle("/")).toBe("Home");
    expect(resolvePageTitle("")).toBe("Home");
  });
});

describe("sanitizePageTitle", () => {
  it("keeps titles concise enough for mobile browser tabs", () => {
    expect(
      sanitizePageTitle(
        "Investigate the production checkout regression affecting enterprise customers after deploy",
      ),
    ).toBe("Investigate the production checkout regression affecting enterprise...");
  });
});
