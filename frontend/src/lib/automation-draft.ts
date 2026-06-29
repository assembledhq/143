import { toCodingAgentReasoningEffort, type CodingAgentReasoningEffort } from "@/lib/coding-agent-reasoning";
import type { AutomationProductTrigger } from "@/lib/automation-triggers";
import type { AgentCapabilityGrant, LinearEventType, PagerDutyEventType } from "@/lib/types";

const STORAGE_KEY = "143:new-automation-draft";
const SCHEMA_VERSION = 1;

export type AutomationFormState = {
  name: string;
  goal: string;
  iconValue: string;
  scope: string;
  selectedRepoId: string;
  intervalValue: number;
  intervalUnit: "hours" | "days" | "weeks";
  intervalRunHour: string;
  intervalRunMinute: string;
  timezone: string;
  scheduleEnabled: boolean;
  productTriggers: AutomationProductTrigger[];
  triggerBaseBranches: string;
  triggerAuthors: string;
  triggerPaths: string;
  triggerFeedbackTypes: string;
  triggerReviewStates: string;
  pagerDutyEnabled: boolean;
  pagerDutyEventTypes: PagerDutyEventType[];
  pagerDutyServiceIDs: string;
  pagerDutyTeamIDs: string;
  pagerDutyStatuses: string;
  pagerDutyUrgency: "high" | "low";
  pagerDutyPriorityNames: string;
  pagerDutyIncidentTypes: string;
  pagerDutyTitleContains: string;
  pagerDutyCustomFields: string;
  pagerDutyCooldownMinutes: string;
  linearEnabled: boolean;
  linearEventTypes: LinearEventType[];
  linearTeamKeys: string;
  linearLabels: string;
  linearIssueTypes: string;
  linearStateTypes: string;
  linearPriorities: string;
  linearTitleContains: string;
  linearCooldownMinutes: string;
  baseBranchByRepoId: Record<string, string>;
  model: string | undefined;
  identityScope: "org" | "personal";
  prePRReviewLoops: number;
  reasoningEffort: CodingAgentReasoningEffort;
  priority: number;
  capabilityOverride: AgentCapabilityGrant[] | null;
};

export type AutomationDraft = AutomationFormState;

type StoredAutomationDraft = AutomationDraft & { __v: number };

type DraftStorageOptions = {
  defaultTimezone?: string;
};

const productTriggerValues = new Set<AutomationProductTrigger>([
  "github.pr.opened",
  "github.pr.updated",
  "github.pr.feedback",
  "github.checks.completed",
  "github.pr.merged",
]);

const pagerDutyEventTypeValues = new Set<PagerDutyEventType>([
  "incident.triggered",
  "incident.annotated",
  "incident.priority_updated",
  "incident.acknowledged",
  "incident.resolved",
]);

const linearEventTypeValues = new Set<LinearEventType>([
  "issue.created",
  "issue.updated",
]);

function getStorage(): Storage | null {
  if (typeof window === "undefined") return null;
  try {
    return window.sessionStorage;
  } catch {
    return null;
  }
}

export function parseAutomationIntervalInput(value: string): number {
  const parsed = parseInt(value, 10);
  return Number.isNaN(parsed) ? 1 : clampInteger(parsed, 1, 365, 1);
}

export function defaultAutomationFormState(
  overrides: Partial<AutomationFormState> = {},
): AutomationFormState {
  return {
    name: "",
    goal: "",
    iconValue: "⚙️",
    scope: "",
    selectedRepoId: "",
    intervalValue: 1,
    intervalUnit: "days",
    intervalRunHour: "09",
    intervalRunMinute: "00",
    timezone: "",
    scheduleEnabled: true,
    productTriggers: [],
    triggerBaseBranches: "",
    triggerAuthors: "",
    triggerPaths: "",
    triggerFeedbackTypes: "",
    triggerReviewStates: "",
    pagerDutyEnabled: false,
    pagerDutyEventTypes: ["incident.triggered"],
    pagerDutyServiceIDs: "",
    pagerDutyTeamIDs: "",
    pagerDutyStatuses: "",
    pagerDutyUrgency: "high",
    pagerDutyPriorityNames: "",
    pagerDutyIncidentTypes: "",
    pagerDutyTitleContains: "",
    pagerDutyCustomFields: "",
    pagerDutyCooldownMinutes: "0",
    linearEnabled: false,
    linearEventTypes: ["issue.created"],
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
    prePRReviewLoops: 1,
    reasoningEffort: "",
    priority: 50,
    capabilityOverride: null,
    ...overrides,
  };
}

export function automationFormStateFromDraft(
  parsed: Partial<StoredAutomationDraft>,
  options: DraftStorageOptions = {},
): AutomationFormState {
  return defaultAutomationFormState({
    name: stringOr(parsed.name, ""),
    goal: stringOr(parsed.goal, ""),
    iconValue: stringOr(parsed.iconValue, "⚙️"),
    scope: stringOr(parsed.scope, ""),
    selectedRepoId: stringOr(parsed.selectedRepoId, ""),
    intervalValue: clampInteger(parsed.intervalValue, 1, 365, 1),
    intervalUnit: isIntervalUnit(parsed.intervalUnit) ? parsed.intervalUnit : "days",
    intervalRunHour: stringOr(parsed.intervalRunHour, "09"),
    intervalRunMinute: stringOr(parsed.intervalRunMinute, "00"),
    timezone:
      typeof parsed.timezone === "string" && parsed.timezone.length > 0
        ? parsed.timezone
        : (options.defaultTimezone ?? ""),
    scheduleEnabled: parsed.scheduleEnabled !== false,
    productTriggers: validArray(parsed.productTriggers, productTriggerValues),
    triggerBaseBranches: stringOr(parsed.triggerBaseBranches, ""),
    triggerAuthors: stringOr(parsed.triggerAuthors, ""),
    triggerPaths: stringOr(parsed.triggerPaths, ""),
    triggerFeedbackTypes: stringOr(parsed.triggerFeedbackTypes, ""),
    triggerReviewStates: stringOr(parsed.triggerReviewStates, ""),
    pagerDutyEnabled: parsed.pagerDutyEnabled === true,
    pagerDutyEventTypes:
      parsed.pagerDutyEventTypes === undefined
        ? ["incident.triggered"]
        : validArray(parsed.pagerDutyEventTypes, pagerDutyEventTypeValues),
    pagerDutyServiceIDs: stringOr(parsed.pagerDutyServiceIDs, ""),
    pagerDutyTeamIDs: stringOr(parsed.pagerDutyTeamIDs, ""),
    pagerDutyStatuses: stringOr(parsed.pagerDutyStatuses, ""),
    pagerDutyUrgency: parsed.pagerDutyUrgency === "low" ? "low" : "high",
    pagerDutyPriorityNames: stringOr(parsed.pagerDutyPriorityNames, ""),
    pagerDutyIncidentTypes: stringOr(parsed.pagerDutyIncidentTypes, ""),
    pagerDutyTitleContains: stringOr(parsed.pagerDutyTitleContains, ""),
    pagerDutyCustomFields: stringOr(parsed.pagerDutyCustomFields, ""),
    pagerDutyCooldownMinutes: stringOr(parsed.pagerDutyCooldownMinutes, "0"),
    linearEnabled: parsed.linearEnabled === true,
    linearEventTypes:
      parsed.linearEventTypes === undefined
        ? ["issue.created"]
        : validArray(parsed.linearEventTypes, linearEventTypeValues),
    linearTeamKeys: stringOr(parsed.linearTeamKeys, ""),
    linearLabels: stringOr(parsed.linearLabels, ""),
    linearIssueTypes: stringOr(parsed.linearIssueTypes, ""),
    linearStateTypes: stringOr(parsed.linearStateTypes, ""),
    linearPriorities: stringOr(parsed.linearPriorities, ""),
    linearTitleContains: stringOr(parsed.linearTitleContains, ""),
    linearCooldownMinutes: stringOr(parsed.linearCooldownMinutes, "0"),
    baseBranchByRepoId: isStringRecord(parsed.baseBranchByRepoId)
      ? parsed.baseBranchByRepoId
      : {},
    model: typeof parsed.model === "string" ? parsed.model : undefined,
    identityScope: parsed.identityScope === "personal" ? "personal" : "org",
    prePRReviewLoops: clampInteger(parsed.prePRReviewLoops, 0, 5, 1),
    reasoningEffort: toCodingAgentReasoningEffort(
      typeof parsed.reasoningEffort === "string" ? parsed.reasoningEffort : "",
    ),
    priority: [0, 25, 50, 75].includes(Number(parsed.priority))
      ? Number(parsed.priority)
      : 50,
    capabilityOverride: Array.isArray(parsed.capabilityOverride)
      ? parsed.capabilityOverride.filter(isCapabilityGrant)
      : null,
  });
}

export function loadAutomationDraft(
  options: DraftStorageOptions = {},
): AutomationDraft | null {
  const storage = getStorage();
  if (!storage) return null;

  let raw: string | null;
  try {
    raw = storage.getItem(STORAGE_KEY);
  } catch {
    return null;
  }
  if (!raw) return null;

  let parsed: Partial<StoredAutomationDraft>;
  try {
    parsed = JSON.parse(raw) as Partial<StoredAutomationDraft>;
  } catch {
    return null;
  }
  if (parsed.__v !== SCHEMA_VERSION) return null;

  return automationFormStateFromDraft(parsed, options);
}

export function automationFormStateToDraft(
  state: AutomationFormState,
  options: DraftStorageOptions = {},
): AutomationDraft {
  return {
    ...state,
    timezone: state.timezone === options.defaultTimezone ? "" : state.timezone,
  };
}

export function saveAutomationDraft(
  state: AutomationFormState,
  options: DraftStorageOptions = {},
): void {
  const storage = getStorage();
  if (!storage) return;

  const draft = automationFormStateToDraft(state, options);
  if (isEmptyDraft(draft)) {
    try { storage.removeItem(STORAGE_KEY); } catch {}
    return;
  }

  const stored: StoredAutomationDraft = { __v: SCHEMA_VERSION, ...draft };
  try {
    storage.setItem(STORAGE_KEY, JSON.stringify(stored));
  } catch {
    // Best-effort draft persistence should never block automation creation.
  }
}

export function clearAutomationDraft(): void {
  const storage = getStorage();
  if (!storage) return;
  try { storage.removeItem(STORAGE_KEY); } catch {}
}

function isEmptyDraft(draft: AutomationDraft): boolean {
  return (
    draft.name.length === 0
    && draft.goal.length === 0
    && draft.iconValue === "⚙️"
    && draft.scope.length === 0
    && draft.selectedRepoId.length === 0
    && draft.intervalValue === 1
    && draft.intervalUnit === "days"
    && draft.intervalRunHour === "09"
    && draft.intervalRunMinute === "00"
    && draft.timezone.length === 0
    && draft.scheduleEnabled
    && draft.productTriggers.length === 0
    && draft.triggerBaseBranches.length === 0
    && draft.triggerAuthors.length === 0
    && draft.triggerPaths.length === 0
    && draft.triggerFeedbackTypes.length === 0
    && draft.triggerReviewStates.length === 0
    && !draft.pagerDutyEnabled
    && draft.pagerDutyEventTypes.length === 1
    && draft.pagerDutyEventTypes[0] === "incident.triggered"
    && draft.pagerDutyServiceIDs.length === 0
    && draft.pagerDutyTeamIDs.length === 0
    && draft.pagerDutyStatuses.length === 0
    && draft.pagerDutyUrgency === "high"
    && draft.pagerDutyPriorityNames.length === 0
    && draft.pagerDutyIncidentTypes.length === 0
    && draft.pagerDutyTitleContains.length === 0
    && draft.pagerDutyCustomFields.length === 0
    && draft.pagerDutyCooldownMinutes === "0"
    && !draft.linearEnabled
    && draft.linearEventTypes.length === 1
    && draft.linearEventTypes[0] === "issue.created"
    && draft.linearTeamKeys.length === 0
    && draft.linearLabels.length === 0
    && draft.linearIssueTypes.length === 0
    && draft.linearStateTypes.length === 0
    && draft.linearPriorities.length === 0
    && draft.linearTitleContains.length === 0
    && draft.linearCooldownMinutes === "0"
    && Object.keys(draft.baseBranchByRepoId).length === 0
    && draft.model === undefined
    && draft.identityScope === "org"
    && draft.prePRReviewLoops === 1
    && draft.reasoningEffort === ""
    && draft.priority === 50
    && draft.capabilityOverride === null
  );
}

function stringOr(value: unknown, fallback: string): string {
  return typeof value === "string" ? value : fallback;
}

function clampInteger(
  value: unknown,
  min: number,
  max: number,
  fallback: number,
): number {
  if (typeof value !== "number" || !Number.isInteger(value)) return fallback;
  return Math.min(max, Math.max(min, value));
}

function isIntervalUnit(value: unknown): value is AutomationDraft["intervalUnit"] {
  return value === "hours" || value === "days" || value === "weeks";
}

function validArray<T extends string>(value: unknown, allowed: Set<T>): T[] {
  if (!Array.isArray(value)) return [];
  return value.filter((item): item is T => typeof item === "string" && allowed.has(item as T));
}

function isStringRecord(value: unknown): value is Record<string, string> {
  if (!value || typeof value !== "object" || Array.isArray(value)) return false;
  return Object.values(value as Record<string, unknown>).every((item) => typeof item === "string");
}

function isCapabilityGrant(value: unknown): value is AgentCapabilityGrant {
  if (!value || typeof value !== "object" || Array.isArray(value)) return false;
  const grant = value as Record<string, unknown>;
  return (
    (grant.id === undefined || typeof grant.id === "string")
    && typeof grant.capability_id === "string"
    && typeof grant.access_level === "string"
    && typeof grant.enabled === "boolean"
    && (grant.config === undefined || (!!grant.config && typeof grant.config === "object" && !Array.isArray(grant.config)))
  );
}
