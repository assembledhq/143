import { describe, expect, it } from "vitest";
import { deriveAutopilotViewModel } from "./autopilot-helpers";
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

describe("deriveAutopilotViewModel", () => {
  it("returns setup mode when required setup is incomplete", () => {
    const viewModel = deriveAutopilotViewModel({
      settings: buildSettings(),
      pmStatus: buildStatus(),
      latestPlan: null,
      documents: [],
      setup: {
        agentConnected: false,
        githubReady: false,
        connectedCount: 0,
        totalCount: 2,
      },
    });

    expect(viewModel.heroMode).toBe("setup");
    expect(viewModel.primaryActionLabel).toBe("Complete setup");
  });

  it("returns first-analysis mode when setup is complete but no plan exists", () => {
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
      setup: {
        agentConnected: true,
        githubReady: true,
        connectedCount: 2,
        totalCount: 2,
      },
    });

    expect(viewModel.heroMode).toBe("first_analysis");
    expect(viewModel.primaryActionLabel).toBe("Run first analysis");
    expect(viewModel.philosophySummary).toBe("Ship reliability first.");
  });

  it("returns recommendation mode and summarizes the current direction from product context", () => {
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
      setup: {
        agentConnected: true,
        githubReady: true,
        connectedCount: 2,
        totalCount: 2,
      },
    });

    expect(viewModel.heroMode).toBe("recommendation");
    expect(viewModel.directionSummary).toBe("Payments hardening this quarter.");
    expect(viewModel.autonomyLabel).toBe("Act on low-risk");
    expect(viewModel.weightsSummary).toBe("Impact 35 · Severity 25 · Recency 20 · Revenue 20");
    expect(viewModel.documentsSummary).toContain("1 attached");
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
      setup: {
        agentConnected: true,
        githubReady: true,
        connectedCount: 2,
        totalCount: 2,
      },
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
      setup: {
        agentConnected: true,
        githubReady: true,
        connectedCount: 2,
        totalCount: 2,
      },
    });

    expect(viewModel.directionSummary).toBe("Legacy direction");
  });
});
