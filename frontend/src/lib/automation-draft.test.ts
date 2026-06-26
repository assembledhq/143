import { beforeEach, describe, expect, it } from "vitest";
import {
  automationFormStateFromDraft,
  automationFormStateToDraft,
  clearAutomationDraft,
  defaultAutomationFormState,
  loadAutomationDraft,
  saveAutomationDraft,
  type AutomationFormState,
} from "./automation-draft";

const STORAGE_KEY = "143:new-automation-draft";

const FORM_STATE_KEYS = [
  "baseBranchByRepoId",
  "capabilityOverride",
  "goal",
  "iconValue",
  "identityScope",
  "intervalRunHour",
  "intervalRunMinute",
  "intervalUnit",
  "intervalValue",
  "linearCooldownMinutes",
  "linearEnabled",
  "linearEventTypes",
  "linearIssueTypes",
  "linearLabels",
  "linearPriorities",
  "linearStateTypes",
  "linearTeamKeys",
  "linearTitleContains",
  "model",
  "name",
  "pagerDutyCooldownMinutes",
  "pagerDutyCustomFields",
  "pagerDutyEnabled",
  "pagerDutyEventTypes",
  "pagerDutyIncidentTypes",
  "pagerDutyPriorityNames",
  "pagerDutyServiceIDs",
  "pagerDutyStatuses",
  "pagerDutyTeamIDs",
  "pagerDutyTitleContains",
  "pagerDutyUrgency",
  "prePRReviewLoops",
  "priority",
  "productTriggers",
  "reasoningEffort",
  "scheduleEnabled",
  "scope",
  "selectedRepoId",
  "timezone",
  "triggerAuthors",
  "triggerBaseBranches",
  "triggerFeedbackTypes",
  "triggerPaths",
  "triggerReviewStates",
].sort();

function populatedDraft(overrides: Partial<AutomationFormState> = {}): AutomationFormState {
  return defaultAutomationFormState({
    name: "Nightly triage",
    goal: "Review new production errors and open focused fixes.",
    iconValue: "✨",
    scope: "src/",
    selectedRepoId: "repo-1",
    intervalValue: 2,
    intervalUnit: "days",
    intervalRunHour: "10",
    intervalRunMinute: "30",
    timezone: "UTC",
    scheduleEnabled: true,
    productTriggers: ["github.pr.opened"],
    triggerBaseBranches: "main",
    triggerAuthors: "dependabot[bot]",
    triggerPaths: "src/",
    triggerFeedbackTypes: "Inline review comment",
    triggerReviewStates: "changes_requested",
    pagerDutyEnabled: true,
    pagerDutyEventTypes: ["incident.triggered"],
    pagerDutyServiceIDs: "P123",
    pagerDutyTeamIDs: "T123",
    pagerDutyStatuses: "triggered",
    pagerDutyUrgency: "high",
    pagerDutyPriorityNames: "P1",
    pagerDutyIncidentTypes: "incident",
    pagerDutyTitleContains: "checkout",
    pagerDutyCustomFields: "service=checkout",
    pagerDutyCooldownMinutes: "30",
    linearEnabled: true,
    linearEventTypes: ["issue.created"],
    linearTeamKeys: "ENG, OPS",
    linearLabels: "bug, customer",
    linearIssueTypes: "Bug",
    linearStateTypes: "unstarted, started",
    linearPriorities: "1, 2",
    linearTitleContains: "checkout",
    linearCooldownMinutes: "15",
    baseBranchByRepoId: { "repo-1": "main" },
    model: "gpt-5.4",
    identityScope: "org",
    prePRReviewLoops: 2,
    reasoningEffort: "high",
    priority: 25,
    capabilityOverride: [
      {
        capability_id: "publishing",
        access_level: "write",
        enabled: true,
      },
    ],
    ...overrides,
  });
}

describe("automation-draft storage", () => {
  beforeEach(() => {
    window.sessionStorage.clear();
  });

  it("keeps the canonical form state keys explicit", () => {
    expect(Object.keys(defaultAutomationFormState()).sort()).toEqual(FORM_STATE_KEYS);
  });

  it("round-trips a populated draft", () => {
    const draft = populatedDraft();

    saveAutomationDraft(draft);

    expect(loadAutomationDraft()).toEqual(draft);
  });

  it("does not persist an empty draft and clears existing storage", () => {
    saveAutomationDraft(populatedDraft());

    saveAutomationDraft(defaultAutomationFormState());

    expect(window.sessionStorage.getItem(STORAGE_KEY)).toBeNull();
  });

  it("normalizes the detected timezone out of stored drafts", () => {
    const state = defaultAutomationFormState({ timezone: "America/New_York" });

    expect(automationFormStateToDraft(state, { defaultTimezone: "America/New_York" })).toEqual(
      defaultAutomationFormState({ timezone: "" }),
    );
    expect(
      automationFormStateFromDraft(
        { __v: 1, timezone: "" },
        { defaultTimezone: "America/New_York" },
      ),
    ).toEqual(defaultAutomationFormState({ timezone: "America/New_York" }));
  });

  it("discards drafts with a mismatched schema version", () => {
    window.sessionStorage.setItem(
      STORAGE_KEY,
      JSON.stringify({ __v: 999, name: "Old draft" }),
    );

    expect(loadAutomationDraft()).toBeNull();
  });

  it("sanitizes malformed field values", () => {
    window.sessionStorage.setItem(
      STORAGE_KEY,
      JSON.stringify({
        __v: 1,
        name: "Partly valid",
        goal: "Keep the strings",
        intervalValue: -10,
        intervalUnit: "months",
        pagerDutyUrgency: "medium",
        productTriggers: ["github.pr.opened", 123],
        pagerDutyEventTypes: ["incident.resolved", false],
        linearEnabled: true,
        linearEventTypes: ["issue.updated", "not-real", false],
        baseBranchByRepoId: { "repo-1": "release", bad: 42 },
        identityScope: "robot",
        prePRReviewLoops: 10,
        reasoningEffort: "impossible",
        priority: 999,
        capabilityOverride: [
          { capability_id: "publishing", access_level: "write", enabled: true },
          { capability_id: 42, access_level: "write", enabled: true },
        ],
      }),
    );

    expect(loadAutomationDraft()).toEqual(
      populatedDraft({
        name: "Partly valid",
        goal: "Keep the strings",
        iconValue: "⚙️",
        scope: "",
        selectedRepoId: "",
        intervalValue: 1,
        intervalUnit: "days",
        intervalRunHour: "09",
        intervalRunMinute: "00",
        timezone: "",
        scheduleEnabled: true,
        productTriggers: ["github.pr.opened"],
        triggerBaseBranches: "",
        triggerAuthors: "",
        triggerPaths: "",
        triggerFeedbackTypes: "",
        triggerReviewStates: "",
        pagerDutyEnabled: false,
        pagerDutyEventTypes: ["incident.resolved"],
        pagerDutyServiceIDs: "",
        pagerDutyTeamIDs: "",
        pagerDutyStatuses: "",
        pagerDutyUrgency: "high",
        pagerDutyPriorityNames: "",
        pagerDutyIncidentTypes: "",
        pagerDutyTitleContains: "",
        pagerDutyCustomFields: "",
        pagerDutyCooldownMinutes: "0",
        linearEnabled: true,
        linearEventTypes: ["issue.updated"],
        linearTeamKeys: "",
        linearLabels: "",
        linearIssueTypes: "",
        linearStateTypes: "",
        linearPriorities: "",
        linearTitleContains: "",
        linearCooldownMinutes: "0",
        baseBranchByRepoId: {},
        model: undefined,
        identityScope: "org",
        prePRReviewLoops: 5,
        reasoningEffort: "",
        priority: 50,
        capabilityOverride: [
          { capability_id: "publishing", access_level: "write", enabled: true },
        ],
      }),
    );
  });

  it("clears the stored draft", () => {
    saveAutomationDraft(populatedDraft());

    clearAutomationDraft();

    expect(window.sessionStorage.getItem(STORAGE_KEY)).toBeNull();
  });
});
