import { describe, expect, it } from "vitest";
import { deriveAutopilotViewModel, formatFreshness } from "./autopilot-helpers";
import type { OrgSettings, PMDocument, PMPlan, PMStatus } from "@/lib/types";

function buildSettings(overrides: Partial<OrgSettings> = {}): OrgSettings {
  return {
    autonomy_level: "auto_simple",
    product_direction: "",
    product_context: {
      philosophy: "",
      direction: "",
      focus_areas: [],
      avoid_areas: [],
    },
    priority_weights: {
      customer_impact: 0.35,
      severity: 0.25,
      recency: 0.2,
      revenue_risk: 0.2,
    },
    ...overrides,
  };
}

function buildStatus(overrides: Partial<PMStatus> = {}): PMStatus {
  return {
    is_running: false,
    issues_reviewed: 0,
    success_rate: 0,
    success_count: 0,
    total_delegated: 0,
    ...overrides,
  };
}

function buildPlan(overrides: Partial<PMPlan> = {}): PMPlan {
  return {
    id: "plan-1",
    org_id: "org-1",
    status: "completed",
    analysis: "Focus on payment reliability before broadening scope.",
    tasks: [],
    clusters: [],
    skipped_issues: [],
    issues_reviewed: 14,
    triggered_by: "manual",
    created_at: "2026-03-23T18:00:00Z",
    completed_at: "2026-03-23T18:02:00Z",
    ...overrides,
  };
}

function buildDoc(overrides: Partial<PMDocument> = {}): PMDocument {
  return {
    id: "doc-1",
    org_id: "org-1",
    title: "Q2 roadmap",
    content: "Content",
    doc_type: "roadmap",
    sort_order: 0,
    source_type: "manual",
    created_at: "2026-03-20T12:00:00Z",
    updated_at: "2026-03-21T12:00:00Z",
    ...overrides,
  };
}

describe("formatFreshness", () => {
  it("returns 'No analysis yet' when no timestamp", () => {
    expect(formatFreshness(undefined)).toBe("No analysis yet");
  });

  it("returns 'Analyzed just now' for < 1 minute", () => {
    const now = new Date("2026-04-08T12:00:00Z").getTime();
    expect(formatFreshness("2026-04-08T12:00:00Z", now)).toBe("Analyzed just now");
    expect(formatFreshness("2026-04-08T11:59:30Z", now)).toBe("Analyzed just now");
  });

  it("returns minutes for < 1 hour", () => {
    const now = new Date("2026-04-08T12:00:00Z").getTime();
    expect(formatFreshness("2026-04-08T11:45:00Z", now)).toBe("Analyzed 15m ago");
  });

  it("returns hours for < 24 hours", () => {
    const now = new Date("2026-04-08T12:00:00Z").getTime();
    expect(formatFreshness("2026-04-08T10:00:00Z", now)).toBe("Analyzed 2h ago");
  });

  it("returns date for >= 24 hours", () => {
    const now = new Date("2026-04-10T12:00:00Z").getTime();
    expect(formatFreshness("2026-04-08T10:00:00Z", now)).toBe("Last analyzed Apr 8");
  });
});

describe("deriveAutopilotViewModel", () => {
  it("returns first-analysis mode when no plan exists", () => {
    const viewModel = deriveAutopilotViewModel({
      settings: buildSettings({
        product_context: {
          philosophy: "Ship reliability first.",
          direction: "",
          focus_areas: [],
          avoid_areas: [],
        },
      }),
      pmStatus: buildStatus(),
      latestPlan: null,
      documents: [],
    });

    expect(viewModel.heroMode).toBe("first_analysis");
    expect(viewModel.primaryActionLabel).toBe("Run first analysis");
    expect(viewModel.heroTitle).toBe("Ready for your first analysis");
  });

  it("returns recommendation mode and extracts headline from analysis", () => {
    const viewModel = deriveAutopilotViewModel({
      settings: buildSettings({
        product_context: {
          philosophy: "Ship reliability first.",
          direction: "Payments hardening this quarter.",
          focus_areas: ["auth", "incidents"],
          avoid_areas: ["redesigns"],
        },
      }),
      pmStatus: buildStatus({
        last_run_at: "2026-03-23T18:02:00Z",
        last_run_status: "completed",
        issues_reviewed: 14,
        success_rate: 84,
        total_delegated: 3,
        next_run_in: "in 2h",
      }),
      latestPlan: buildPlan(),
      documents: [buildDoc()],
    });

    expect(viewModel.heroMode).toBe("recommendation");
    expect(viewModel.directionSummary).toBe("Payments hardening this quarter.");
    expect(viewModel.autonomyLabel).toBe("Act on low-risk");
    expect(viewModel.weightsSummary).toBe("Impact 35 · Severity 25 · Recency 20 · Revenue 20");
    expect(viewModel.documentsSummary).toContain("1 attached");
    expect(viewModel.hasEvidence).toBe(true);
    expect(viewModel.evidence).toHaveLength(3);
  });

  it("returns attention mode when the PM status reports an error", () => {
    const viewModel = deriveAutopilotViewModel({
      settings: buildSettings(),
      pmStatus: buildStatus({
        last_error: "The last analysis failed.",
        last_run_status: "failed",
      }),
      latestPlan: buildPlan(),
      documents: [],
    });

    expect(viewModel.heroMode).toBe("attention");
    expect(viewModel.heroBody).toContain("The last analysis failed.");
  });

  it("falls back to legacy product_direction when product_context.direction is empty", () => {
    const viewModel = deriveAutopilotViewModel({
      settings: buildSettings({
        product_direction: "Legacy direction",
        product_context: {
          philosophy: "",
          direction: "",
          focus_areas: [],
          avoid_areas: [],
        },
      }),
      pmStatus: buildStatus(),
      latestPlan: buildPlan(),
      documents: [],
    });

    expect(viewModel.directionSummary).toBe("Legacy direction");
  });

  it("falls back to philosophy when direction is empty", () => {
    const viewModel = deriveAutopilotViewModel({
      settings: buildSettings({
        product_context: {
          philosophy: "Ship reliability first.",
          direction: "",
          focus_areas: [],
          avoid_areas: [],
        },
      }),
      pmStatus: buildStatus(),
      latestPlan: buildPlan(),
      documents: [],
    });

    expect(viewModel.directionSummary).toBe("Ship reliability first.");
  });

  it("hides evidence when all metrics are zero", () => {
    const viewModel = deriveAutopilotViewModel({
      settings: buildSettings(),
      pmStatus: buildStatus(),
      latestPlan: null,
      documents: [],
    });

    expect(viewModel.hasEvidence).toBe(false);
  });

  it("builds a status line with autonomy, freshness, and next run", () => {
    const viewModel = deriveAutopilotViewModel({
      settings: buildSettings({ autonomy_level: "auto_simple" }),
      pmStatus: buildStatus({ next_run_in: "in 2h" }),
      latestPlan: null,
      documents: [],
    });

    expect(viewModel.statusLine).toContain("Act on low-risk");
    expect(viewModel.statusLine).toContain("No analysis yet");
    expect(viewModel.statusLine).toContain("Next in 2h");
    expect(viewModel.statusLine).not.toContain("Next in in");
  });

  it("strips leading 'in' from next_run_in to avoid duplication", () => {
    const viewModel = deriveAutopilotViewModel({
      settings: buildSettings(),
      pmStatus: buildStatus({ next_run_in: "30m" }),
      latestPlan: null,
      documents: [],
    });

    expect(viewModel.statusLine).toContain("Next in 30m");
  });

  it("extracts first sentence as headline when analysis has multiple sentences", () => {
    const viewModel = deriveAutopilotViewModel({
      settings: buildSettings(),
      pmStatus: buildStatus(),
      latestPlan: buildPlan({
        analysis: "Auth tokens are the top priority. Three issues share a root cause in session middleware. Fix this first.",
      }),
      documents: [],
    });

    expect(viewModel.heroTitle).toBe("Auth tokens are the top priority.");
    expect(viewModel.heroBody).toContain("Three issues share a root cause");
  });

  it("does not split headline on abbreviations like 'e.g.'", () => {
    const viewModel = deriveAutopilotViewModel({
      settings: buildSettings(),
      pmStatus: buildStatus(),
      latestPlan: buildPlan({
        analysis: "Use standard patterns e.g. retry with backoff. This improves reliability.",
      }),
      documents: [],
    });

    expect(viewModel.heroTitle).toBe("Use standard patterns e.g. retry with backoff.");
    expect(viewModel.heroBody).toContain("This improves reliability.");
  });
});
