"use client";

import { useMemo, useRef, useState, type ReactNode } from "react";
import dynamic from "next/dynamic";
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import {
  ChevronDown,
  Play,
  Pause,
  Loader2,
  Minus,
  Plus,
  Settings2,
} from "lucide-react";
import { useParams, useRouter } from "next/navigation";
import Link from "next/link";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from "@/components/ui/collapsible";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from "@/components/ui/sheet";
import { MobileBackButton } from "@/components/mobile-back-button";
import { PageContainer } from "@/components/page-container";
import { PageHeader } from "@/components/page-header";
import { MarkdownContent } from "@/components/markdown";
import { AutomationGoalEditor } from "@/components/automation-goal-editor";
import { AutomationGoalImprovementControl } from "@/components/automation-goal-improvement";
import {
  AutomationCapabilitiesEditor,
  capabilitySummary,
  normalizeCapabilityGrants,
} from "@/components/automation-capabilities-editor";
import { BranchPicker } from "@/components/branch-picker";
import { AutomationModelSelect } from "@/components/automation-model-select";
import { api } from "@/lib/api";
import { parseAutomationIntervalInput } from "@/lib/automation-draft";
import {
  removeAutomationFromListCaches,
  upsertAutomationInListCaches,
} from "@/lib/automation-list-cache";
import { queryKeys } from "@/lib/query-keys";
import { agentTypeForModel } from "@/lib/agents";
import {
  automationProductTriggerOptions,
  githubEventsToAutomationProductTriggers,
  type AutomationProductTrigger,
} from "@/lib/automation-triggers";
import { automationGoalLengthState } from "@/lib/automation-validation";
import { useAuth } from "@/hooks/use-auth";
import { usePageTitle } from "@/hooks/use-page-title";
import { useAutosave, type AutosaveStatus } from "@/hooks/useAutosave";
import { useDebouncedTextField } from "@/hooks/useDebouncedTextField";
import type {
  AgentCapabilityDefinition,
  AgentCapabilityGrant,
  Automation,
  AutomationGitHubEventFilters,
  AutomationRun,
  ListResponse,
} from "@/lib/types";
import { cn, formatDateTime, formatTimeAgo } from "@/lib/utils";
import {
  getCodingAgentReasoningOptions,
  supportsReasoningEffort,
  toCodingAgentReasoningEffort,
  type CodingAgentReasoningEffort,
} from "@/lib/coding-agent-reasoning";
import { DecisionHistory } from "./decision-history";
import {
  browserTimezone,
  formatAutomationSchedule,
  hourOptions,
  minuteOptions,
  splitRunAt,
} from "../schedule-time";
import { TimezonePicker } from "../timezone-picker";
import { AutomationEmojiPicker } from "@/components/automation-emoji-picker";

// Defer recharts (the only dep here that's expensive) into its own chunk.
const AutomationStatsCard = dynamic(
  () =>
    import("./automation-stats-card").then((m) => ({
      default: m.AutomationStatsCard,
    })),
  {
    ssr: false,
    loading: () => (
      <div className="h-48 bg-muted/20 animate-pulse rounded-lg" />
    ),
  },
);

// Single source of truth for interval unit values. Kept as a tuple so we can
// derive the union type for state AND runtime-validate incoming Select values
// without an unsafe `as` cast. Adding a unit means updating this tuple only.
const INTERVAL_UNITS = ["hours", "days", "weeks"] as const;
type IntervalUnit = (typeof INTERVAL_UNITS)[number];
const toIntervalUnit = (v: string, fallback: IntervalUnit): IntervalUnit =>
  (INTERVAL_UNITS as readonly string[]).includes(v)
    ? (v as IntervalUnit)
    : fallback;

function commaList(value: string): string[] {
  return value
    .split(",")
    .map((item) => item.trim())
    .filter(Boolean);
}

function SettingsTab({
  automation,
  canManage,
}: {
  automation: Automation;
  canManage: boolean;
}) {
  const queryClient = useQueryClient();
  const [scope, setScope] = useState(automation.scope ?? "");
  const [intervalValue, setIntervalValue] = useState(
    String(automation.interval_value ?? 1),
  );
  const [intervalUnit, setIntervalUnit] = useState<IntervalUnit>(
    toIntervalUnit(automation.interval_unit ?? "days", "days"),
  );
  // Form state is seeded from the automation prop on first mount only. The
  // parent polls every 10s and may refetch into a new `automation` object.
  // Keep this local draft mounted while the sheet is open so unrelated inline
  // title/goal autosaves cannot discard mechanics edits in progress.
  const initialRunAt = splitRunAt(automation.interval_run_at ?? "09:00");
  const [intervalRunHour, setIntervalRunHour] = useState(initialRunAt.hour);
  const [intervalRunMinute, setIntervalRunMinute] = useState(
    initialRunAt.minute,
  );
  const [timezone, setTimezone] = useState<string>(
    automation.timezone || "UTC",
  );
  const [scheduleEnabled, setScheduleEnabled] = useState(
    automation.schedule_type !== "none",
  );
  const [productTriggers, setProductTriggers] = useState<
    AutomationProductTrigger[]
  >(() =>
    githubEventsToAutomationProductTriggers(
      automation.github_event_triggers ?? [],
    ),
  );
  const [triggerBaseBranches, setTriggerBaseBranches] = useState(
    (automation.github_event_filters?.base_branches ?? []).join(", "),
  );
  const [triggerAuthors, setTriggerAuthors] = useState(
    (automation.github_event_filters?.authors ?? []).join(", "),
  );
  const [triggerPaths, setTriggerPaths] = useState(
    (automation.github_event_filters?.paths ?? []).join(", "),
  );
  const [triggerFeedbackTypes, setTriggerFeedbackTypes] = useState(
    (automation.github_event_filters?.feedback_types ?? []).join(", "),
  );
  const [triggerReviewStates, setTriggerReviewStates] = useState(
    (automation.github_event_filters?.review_states ?? []).join(", "),
  );
  // Memoised per mount: Intl.DateTimeFormat() is cheap but there's no reason
  // to re-evaluate it on every render, and stability prevents the
  // TimezonePicker's `detected` prop from changing identity.
  const detectedTimezone = useMemo(() => browserTimezone(), []);
  const [baseBranch, setBaseBranch] = useState(automation.base_branch);
  const [model, setModel] = useState<string | undefined>(
    automation.model_override,
  );
  const [identityScope, setIdentityScope] = useState<"org" | "personal">(
    automation.identity_scope ?? "org",
  );
  const [publishPolicy, setPublishPolicy] = useState<"pull_request" | "none">(
    automation.publish_policy ?? "pull_request",
  );
  const [prePRReviewLoops, setPrePRReviewLoops] = useState(
    automation.pre_pr_review_loops ?? 0,
  );
  const [reasoningEffort, setReasoningEffort] =
    useState<CodingAgentReasoningEffort>(automation.reasoning_effort ?? "");
  const [capabilityDraft, setCapabilityDraft] = useState<
    AgentCapabilityGrant[] | null
  >(null);

  const { data: settingsResponse } = useQuery({
    queryKey: ["settings"],
    queryFn: () => api.settings.get(),
  });
  const settings = (settingsResponse?.data?.settings ?? {}) as {
    default_agent_type?: string;
  };
  const defaultAgentType = settings.default_agent_type ?? "codex";
  const effectiveAgentType = model
    ? (agentTypeForModel(model) ?? automation.agent_type ?? defaultAgentType)
    : (automation.agent_type ?? defaultAgentType);
  const supportsNativeReviewLoop = [
    "codex",
    "claude_code",
    "amp",
    "pi",
    "opencode",
  ].includes(effectiveAgentType);
  const effectivePrePRReviewLoops = supportsNativeReviewLoop
    ? prePRReviewLoops
    : 0;
  let prePRReviewDescription = "Off for agents without review-loop support.";
  if (supportsNativeReviewLoop) {
    prePRReviewDescription =
      effectivePrePRReviewLoops === 0
        ? "Off"
        : "Runs the coding agent's review/fix loop before opening a PR.";
  }
  const showReasoningSelector = supportsReasoningEffort(effectiveAgentType);
  const reasoningOptions = getCodingAgentReasoningOptions(effectiveAgentType);
  const { data: capabilityCatalogResponse } = useQuery<
    ListResponse<AgentCapabilityDefinition>
  >({
    queryKey: ["agent-capabilities"],
    queryFn: () => api.settings.getAgentCapabilities(),
  });
  const capabilityCatalog = useMemo(
    () => capabilityCatalogResponse?.data ?? [],
    [capabilityCatalogResponse?.data],
  );
  const { data: automationCapabilityResponse } = useQuery({
    queryKey: ["automation-capabilities", automation.id],
    queryFn: () => api.automations.getCapabilities(automation.id),
  });
  const savedCapabilityGrants = useMemo(
    () =>
      normalizeCapabilityGrants(
        capabilityCatalog,
        automationCapabilityResponse?.data?.capabilities ?? [],
      ),
    [automationCapabilityResponse?.data?.capabilities, capabilityCatalog],
  );
  const capabilityGrants = capabilityDraft ?? savedCapabilityGrants;
  const parsedIntervalValue = Number(intervalValue.trim());
  const intervalValueIsValid =
    intervalValue.trim() !== "" &&
    Number.isInteger(parsedIntervalValue) &&
    parsedIntervalValue >= 1 &&
    parsedIntervalValue <= 365;
  const githubEventFilters: AutomationGitHubEventFilters = useMemo(
    () => ({
      base_branches: commaList(triggerBaseBranches),
      authors: commaList(triggerAuthors),
      paths: commaList(triggerPaths),
      feedback_types: commaList(triggerFeedbackTypes),
      review_states: commaList(triggerReviewStates),
    }),
    [
      triggerAuthors,
      triggerBaseBranches,
      triggerFeedbackTypes,
      triggerPaths,
      triggerReviewStates,
    ],
  );
  const hasTrigger = scheduleEnabled || productTriggers.length > 0;

  const toggleProductTrigger = (
    trigger: AutomationProductTrigger,
    checked: boolean,
  ) => {
    setProductTriggers((current) => {
      if (checked) {
        return current.includes(trigger) ? current : [...current, trigger];
      }
      return current.filter((item) => item !== trigger);
    });
  };

  const updateMutation = useMutation({
    mutationFn: () =>
      api.automations.update(automation.id, {
        scope: scope.trim() || undefined,
        schedule_type: scheduleEnabled ? "interval" : "none",
        ...(scheduleEnabled
          ? {
              interval_value: parsedIntervalValue,
              interval_unit: intervalUnit,
              interval_run_at: `${intervalRunHour}:${intervalRunMinute}`,
            }
          : {}),
        timezone,
        triggers: productTriggers,
        github_event_filters: githubEventFilters,
        model: model ?? "",
        identity_scope: identityScope,
        publish_policy: publishPolicy,
        pre_pr_review_loops: effectivePrePRReviewLoops,
        reasoning_effort:
          showReasoningSelector && reasoningEffort ? reasoningEffort : "",
        base_branch: baseBranch.trim() || undefined,
      }),
    onSuccess: (res) => {
      upsertAutomationInListCaches(queryClient, res.data);
      queryClient.setQueryData(queryKeys.automations.detail(res.data.id), res);
      queryClient.invalidateQueries({
        queryKey: queryKeys.automations.detail(res.data.id),
      });
      queryClient.invalidateQueries({ queryKey: queryKeys.automations.all });
    },
  });
  const capabilityMutation = useMutation({
    mutationFn: (capabilities: AgentCapabilityGrant[]) =>
      api.automations.updateCapabilities(automation.id, capabilities),
    onSuccess: () => {
      setCapabilityDraft(null);
      queryClient.invalidateQueries({
        queryKey: ["automation-capabilities", automation.id],
      });
    },
  });

  return (
    <div className="space-y-4 rounded-lg border border-border bg-card p-5">
      <div className="space-y-1.5">
        <Label htmlFor="scope">
          Scope{" "}
          <span className="text-muted-foreground font-normal">(optional)</span>
        </Label>
        <Input
          id="scope"
          value={scope}
          onChange={(e) => setScope(e.target.value)}
        />
      </div>
      <div className="space-y-1.5">
        <Label>Run as</Label>
        <Select
          value={identityScope}
          onValueChange={(value: "org" | "personal") => setIdentityScope(value)}
        >
          <SelectTrigger aria-label="Run as">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="org">Organization automation</SelectItem>
            <SelectItem value="personal">Personal automation</SelectItem>
          </SelectContent>
        </Select>
        <p className="text-xs text-muted-foreground">
          Organization automations use team credentials and publish as 143-bot.
          Personal automations use the creator&apos;s coding-agent preferences and
          GitHub identity.
        </p>
      </div>
      <div className="space-y-2">
        <Label>Triggers</Label>
        <div className="space-y-3 rounded-md border border-border p-3">
          <Label className="flex min-h-7 cursor-pointer items-start gap-2 text-sm font-normal">
            <Checkbox
              checked={scheduleEnabled}
              onCheckedChange={(checked) =>
                setScheduleEnabled(checked === true)
              }
              aria-label="On a schedule"
              disabled={!canManage}
            />
            <span className="pt-0.5">On a schedule</span>
          </Label>
          {scheduleEnabled ? (
            <div className="grid gap-3 pl-6 md:grid-cols-2">
              <div className="flex items-center gap-2">
                <span className="text-xs font-medium leading-none text-muted-foreground">
                  Run every
                </span>
                <Input
                  id="interval-value"
                  aria-label="Interval value"
                  type="number"
                  min={1}
                  max={365}
                  value={intervalValue}
                  onChange={(e) => setIntervalValue(e.target.value)}
                  onBlur={() =>
                    setIntervalValue(
                      String(parseAutomationIntervalInput(intervalValue)),
                    )
                  }
                  aria-invalid={!intervalValueIsValid}
                  className="w-20"
                />
                <Select
                  value={intervalUnit}
                  onValueChange={(v) =>
                    setIntervalUnit(toIntervalUnit(v, intervalUnit))
                  }
                >
                  <SelectTrigger
                    className="h-9 w-28"
                    aria-label="Interval unit"
                  >
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="hours">hours</SelectItem>
                    <SelectItem value="days">days</SelectItem>
                    <SelectItem value="weeks">weeks</SelectItem>
                  </SelectContent>
                </Select>
              </div>
              <div className="grid grid-cols-[auto_5rem_auto_5rem_minmax(0,12.5rem)] items-center gap-2">
                <span className="text-xs font-medium leading-none text-muted-foreground">
                  At
                </span>
                <Select
                  value={intervalRunHour}
                  onValueChange={setIntervalRunHour}
                >
                  <SelectTrigger className="h-9 w-20" aria-label="Run at hour">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {hourOptions.map((h) => (
                      <SelectItem key={h} value={h}>
                        {h}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
                <span className="text-sm text-muted-foreground">:</span>
                <Select
                  value={intervalRunMinute}
                  onValueChange={setIntervalRunMinute}
                >
                  <SelectTrigger
                    className="h-9 w-20"
                    aria-label="Run at minute"
                  >
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {minuteOptions.map((m) => (
                      <SelectItem key={m} value={m}>
                        {m}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
                <TimezonePicker
                  value={timezone}
                  onChange={setTimezone}
                  detected={detectedTimezone}
                  className="w-[12.5rem] max-w-full"
                />
              </div>
            </div>
          ) : null}
          <div className="space-y-2">
            <span className="text-xs font-medium leading-none text-muted-foreground">
              Pull requests
            </span>
            <div className="grid gap-2 md:grid-cols-2">
              {automationProductTriggerOptions.map((option) => (
                <Label
                  key={option.value}
                  className="flex min-h-7 cursor-pointer items-center gap-2 text-sm font-normal"
                >
                  <Checkbox
                    checked={productTriggers.includes(option.value)}
                    onCheckedChange={(checked) =>
                      toggleProductTrigger(option.value, checked === true)
                    }
                    aria-label={option.label}
                    disabled={!canManage}
                  />
                  <span>{option.label}</span>
                </Label>
              ))}
            </div>
          </div>
          {!hasTrigger ? (
            <p className="text-xs text-destructive">
              Select at least one trigger.
            </p>
          ) : null}
        </div>
      </div>
      <Collapsible className="rounded-md border border-border">
        <CollapsibleTrigger asChild>
          <Button
            type="button"
            variant="ghost"
            className="group h-10 w-full justify-between rounded-md px-3 text-left"
          >
            <span>Advanced settings</span>
            <ChevronDown className="h-4 w-4 text-muted-foreground transition-transform group-data-[state=open]:rotate-180" />
          </Button>
        </CollapsibleTrigger>
        <CollapsibleContent className="space-y-4 border-t border-border p-3">
          <div className="space-y-1.5">
            <Label htmlFor="automation-model">Model</Label>
            <AutomationModelSelect
              id="automation-model"
              ariaLabel="Model"
              value={model}
              onValueChange={setModel}
            />
          </div>
          {showReasoningSelector ? (
            <div className="space-y-1.5">
              <Label htmlFor="automation-reasoning">Reasoning</Label>
              <Select
                value={reasoningEffort || "__default__"}
                onValueChange={(value) =>
                  setReasoningEffort(
                    value === "__default__"
                      ? ""
                      : toCodingAgentReasoningEffort(value),
                  )
                }
              >
                <SelectTrigger id="automation-reasoning" aria-label="Reasoning">
                  <SelectValue placeholder="Default reasoning" />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="__default__">Default reasoning</SelectItem>
                  {reasoningOptions.map((option) => (
                    <SelectItem key={option.value} value={option.value}>
                      {option.label}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
          ) : null}
          <div className="space-y-1.5">
            <Label>Base branch</Label>
            <BranchPicker
              repositoryId={automation.repository_id ?? ""}
              value={baseBranch}
              defaultBranch={automation.base_branch}
              onValueChange={setBaseBranch}
              label="Base branch"
              buttonClassName="w-full justify-between"
              contentClassName="w-[var(--radix-popover-trigger-width)]"
            />
          </div>
          <div className="space-y-1.5">
            <Label>After a successful run</Label>
            <Select
              value={publishPolicy}
              onValueChange={(value) => {
                if (value === "pull_request" || value === "none") {
                  setPublishPolicy(value);
                }
              }}
              disabled={!canManage}
            >
              <SelectTrigger aria-label="After a successful run">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="pull_request">Open a pull request</SelectItem>
                <SelectItem value="none">Do not publish</SelectItem>
              </SelectContent>
            </Select>
            <p className="text-xs text-muted-foreground">
              Pull requests are also skipped when the run produces no diff.
            </p>
          </div>
          <div className="space-y-2">
            <div className="flex items-center justify-between gap-3">
              <Label>Capabilities</Label>
              <span className="truncate text-xs text-muted-foreground">
                {capabilitySummary(capabilityCatalog, capabilityGrants)}
              </span>
            </div>
            <AutomationCapabilitiesEditor
              catalog={capabilityCatalog}
              grants={capabilityGrants}
              onChange={setCapabilityDraft}
              disabled={!canManage}
            />
            {capabilityDraft ? (
              <div className="flex items-center gap-2">
                <Button
                  type="button"
                  size="sm"
                  onClick={() => capabilityMutation.mutate(capabilityDraft)}
                  disabled={capabilityMutation.isPending}
                >
                  {capabilityMutation.isPending && (
                    <Loader2 className="mr-2 h-4 w-4 animate-spin" />
                  )}
                  Save capabilities
                </Button>
                <Button
                  type="button"
                  size="sm"
                  variant="outline"
                  onClick={() => setCapabilityDraft(null)}
                >
                  Reset
                </Button>
                {capabilityMutation.isError ? (
                  <span className="text-xs text-destructive">
                    Failed to save capabilities.
                  </span>
                ) : null}
              </div>
            ) : null}
          </div>
          <div className="space-y-3 rounded-md border border-border p-3">
            <div className="space-y-1">
              <Label>Trigger filters</Label>
              <p className="text-xs text-muted-foreground">
                Comma-separated filters applied when GitHub sends matching
                context.
              </p>
            </div>
            <div className="grid gap-3 sm:grid-cols-2">
              <div className="space-y-1.5">
                <Label htmlFor="trigger-base-branches">Target branches</Label>
                <Input
                  id="trigger-base-branches"
                  value={triggerBaseBranches}
                  onChange={(e) => setTriggerBaseBranches(e.target.value)}
                  disabled={!canManage}
                />
              </div>
              <div className="space-y-1.5">
                <Label htmlFor="trigger-authors">Authors</Label>
                <Input
                  id="trigger-authors"
                  value={triggerAuthors}
                  onChange={(e) => setTriggerAuthors(e.target.value)}
                  disabled={!canManage}
                />
              </div>
              <div className="space-y-1.5">
                <Label htmlFor="trigger-paths">Paths</Label>
                <Input
                  id="trigger-paths"
                  value={triggerPaths}
                  onChange={(e) => setTriggerPaths(e.target.value)}
                  disabled={!canManage}
                />
              </div>
              <div className="space-y-1.5">
                <Label htmlFor="trigger-feedback-types">Feedback types</Label>
                <Input
                  id="trigger-feedback-types"
                  value={triggerFeedbackTypes}
                  onChange={(e) => setTriggerFeedbackTypes(e.target.value)}
                  disabled={!canManage}
                />
              </div>
              <div className="space-y-1.5 sm:col-span-2">
                <Label htmlFor="trigger-review-states">Review states</Label>
                <Input
                  id="trigger-review-states"
                  value={triggerReviewStates}
                  onChange={(e) => setTriggerReviewStates(e.target.value)}
                  disabled={!canManage}
                />
              </div>
            </div>
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="pre-pr-review-loops">Pre-PR review</Label>
            <div className="flex items-center gap-2">
              <Button
                type="button"
                variant="outline"
                size="icon"
                aria-label="Decrease review passes"
                onClick={() =>
                  setPrePRReviewLoops((value) => Math.max(0, value - 1))
                }
                disabled={!canManage || !supportsNativeReviewLoop}
              >
                <Minus className="h-4 w-4" />
              </Button>
              <Input
                id="pre-pr-review-loops"
                aria-label="Review passes"
                type="number"
                min={0}
                max={5}
                value={effectivePrePRReviewLoops}
                onChange={(e) => {
                  const parsed = parseInt(e.target.value, 10);
                  setPrePRReviewLoops(
                    Number.isNaN(parsed) ? 0 : Math.min(5, Math.max(0, parsed)),
                  );
                }}
                disabled={!canManage || !supportsNativeReviewLoop}
                className="w-20 text-center"
              />
              <Button
                type="button"
                variant="outline"
                size="icon"
                aria-label="Increase review passes"
                onClick={() =>
                  setPrePRReviewLoops((value) => Math.min(5, value + 1))
                }
                disabled={!canManage || !supportsNativeReviewLoop}
              >
                <Plus className="h-4 w-4" />
              </Button>
            </div>
            <p className="text-xs text-muted-foreground">
              {prePRReviewDescription}
            </p>
          </div>
        </CollapsibleContent>
      </Collapsible>
      {canManage && (
        <div className="flex items-center gap-3 pt-2">
          <Button
            onClick={() => updateMutation.mutate()}
            disabled={
              updateMutation.isPending ||
              !hasTrigger ||
              (scheduleEnabled && !intervalValueIsValid)
            }
          >
            {updateMutation.isPending && (
              <Loader2 className="h-4 w-4 mr-2 animate-spin" />
            )}
            Save changes
          </Button>
          {updateMutation.isError && (
            <p className="text-xs text-destructive">Failed to save changes.</p>
          )}
          {updateMutation.isSuccess && !updateMutation.isPending && (
            <p className="text-xs text-muted-foreground">Saved.</p>
          )}
        </div>
      )}
    </div>
  );
}

type AutomationTextPatch = Partial<Pick<Automation, "name" | "goal">>;
const coalesceAutomationTextPatches = (
  queued: AutomationTextPatch,
  incoming: AutomationTextPatch,
): AutomationTextPatch => ({ ...queued, ...incoming });

function AutosaveIndicator({ status }: { status: AutosaveStatus }) {
  if (status === "idle") return null;

  return (
    <span
      role="status"
      className={cn(
        "text-xs",
        status === "error" ? "text-destructive" : "text-muted-foreground",
      )}
    >
      {status === "saving"
        ? "Saving…"
        : status === "saved"
          ? "Saved"
          : "Couldn’t save"}
    </span>
  );
}

function InlineAutomationText({
  automation,
  canManage,
  field,
}: {
  automation: Automation;
  canManage: boolean;
  field: "name" | "goal";
}) {
  const queryClient = useQueryClient();
  const detailKey = queryKeys.automations.detail(automation.id);
  const autosave = useAutosave<AutomationTextPatch>({
    queryKey: detailKey,
    debounceMs: 0,
    mutationFn: async (patch) => {
      const response = await api.automations.update(automation.id, patch);
      upsertAutomationInListCaches(queryClient, response.data);
      return response;
    },
    applyOptimistic: (previous, patch) => {
      const response = previous as { data?: Automation } | undefined;
      if (!response?.data) return previous;
      return { ...response, data: { ...response.data, ...patch } };
    },
    coalesce: coalesceAutomationTextPatches,
    errorMessage: "Couldn’t save automation text. Your change was reverted.",
  });
  const nameField = useDebouncedTextField({
    serverValue: automation.name,
    onCommit: (name) => autosave.save({ name: name.trim() }),
    // A required field: an empty title is rejected (never saved) and reverts to
    // the last saved name on blur rather than being left silently blank.
    rejectValue: (name) => name.trim() === "",
  });
  const goalField = useDebouncedTextField({
    serverValue: automation.goal,
    onCommit: (goal) => {
      if (!automationGoalLengthState(goal).isTooLong) autosave.save({ goal });
    },
  });
  const goalLength = automationGoalLengthState(goalField.value);

  if (field === "name") {
    return (
      <span className="flex min-w-0 flex-1 items-center gap-2">
        {canManage ? (
          <>
            <span className="sr-only">{automation.name}</span>
            <Input
              aria-label="Automation title"
              value={nameField.value}
              onChange={(event) => nameField.onChange(event.target.value)}
              onBlur={nameField.onBlur}
              className="h-auto border-transparent bg-transparent px-1 py-0 text-2xl font-semibold tracking-tight shadow-none hover:border-border focus-visible:border-border md:text-3xl"
            />
          </>
        ) : (
          <span className="min-w-0 truncate">{automation.name}</span>
        )}
        <AutosaveIndicator status={autosave.status} />
      </span>
    );
  }

  return (
    <section className="rounded-lg border border-border bg-card p-5">
      <div className="mb-3 flex flex-wrap items-center justify-between gap-2">
        <h2 className="text-sm font-semibold text-foreground">Goal</h2>
        <div className="flex items-center gap-2">
          {canManage ? (
            <AutomationGoalImprovementControl
              automationId={automation.id}
              name={nameField.value}
              goal={goalField.value}
              repositoryId={automation.repository_id ?? undefined}
              scope={automation.scope ?? undefined}
              onSavedApply={(updated) => {
                upsertAutomationInListCaches(queryClient, updated);
                queryClient.setQueryData(detailKey, { data: updated });
                queryClient.invalidateQueries({ queryKey: detailKey });
                queryClient.invalidateQueries({
                  queryKey: queryKeys.automations.all,
                });
              }}
            />
          ) : null}
          <AutosaveIndicator status={autosave.status} />
        </div>
      </div>
      {canManage ? (
        <>
          <Label htmlFor="automation-goal" className="sr-only">
            Goal
          </Label>
          <AutomationGoalEditor
            id="automation-goal"
            value={goalField.value}
            onChange={goalField.onChange}
            onBlur={goalField.onBlur}
            repositoryId={automation.repository_id ?? undefined}
            branch={automation.base_branch || undefined}
            agentType={automation.agent_type ?? "codex"}
            rows={9}
            ariaInvalid={goalLength.isTooLong}
            className="border-transparent bg-transparent px-1 shadow-none hover:border-border focus-within:border-border"
          />
          <p
            className={cn(
              "mt-2 text-xs",
              goalLength.isTooLong
                ? "text-destructive"
                : "text-muted-foreground",
            )}
          >
            {goalLength.message ? (
              <span className="mr-2">{goalLength.message}</span>
            ) : null}
            <span className="tabular-nums">{goalLength.countText}</span>
          </p>
        </>
      ) : (
        <MarkdownContent
          content={automation.goal}
          className="text-sm leading-6 text-foreground [&_h1]:text-lg [&_h2]:text-base [&_h3]:text-sm"
        />
      )}
    </section>
  );
}

function AutomationDetailPageSkeleton() {
  return (
    <PageContainer size="wide">
      <div className="space-y-6" aria-busy="true" aria-label="Loading automation">
        <MobileBackButton to="/automations" label="Back to automations" />
        <div className="flex flex-col gap-4 sm:flex-row sm:items-start sm:justify-between">
          <div className="flex min-w-0 items-center gap-3">
            <div className="h-9 w-9 shrink-0 animate-pulse rounded-lg bg-muted" />
            <div
              className="min-w-0 flex-1 space-y-2"
              data-testid="automation-detail-header-skeleton-copy"
            >
              <div className="h-6 w-full max-w-56 animate-pulse rounded bg-muted sm:max-w-72" />
              <div className="h-4 w-full max-w-72 animate-pulse rounded bg-muted/70 sm:max-w-96" />
            </div>
          </div>
          <div className="flex gap-2">
            <div className="h-8 w-16 animate-pulse rounded-lg bg-muted" />
            <div className="h-8 w-20 animate-pulse rounded-lg bg-muted" />
          </div>
        </div>
        <div className="grid gap-6 lg:grid-cols-[minmax(0,1fr)_20rem]">
          <div className="space-y-4">
            <div className="h-9 w-64 animate-pulse rounded-lg bg-muted" />
            <div className="space-y-3">
              <div className="h-28 animate-pulse rounded-xl border border-border bg-muted/30" />
              <div className="h-20 animate-pulse rounded-xl border border-border bg-muted/30" />
              <div className="h-20 animate-pulse rounded-xl border border-border bg-muted/30" />
            </div>
          </div>
          <div className="hidden space-y-4 rounded-xl border border-border p-4 lg:block">
            <div className="h-4 w-28 animate-pulse rounded bg-muted" />
            {[0, 1, 2, 3].map((row) => (
              <div key={row} className="space-y-2">
                <div className="h-3 w-20 animate-pulse rounded bg-muted/70" />
                <div className="h-4 w-4/5 animate-pulse rounded bg-muted" />
              </div>
            ))}
          </div>
        </div>
      </div>
    </PageContainer>
  );
}

export default function AutomationDetailPage() {
  const params = useParams();
  const router = useRouter();
  const queryClient = useQueryClient();
  const { user } = useAuth();
  const automationId = params?.id as string;
  const canManage = user?.role === "admin" || user?.role === "member";
  const [settingsOpen, setSettingsOpen] = useState(false);
  const [detailsOpen, setDetailsOpen] = useState(false);

  const { data, isLoading } = useQuery({
    queryKey: queryKeys.automations.detail(automationId),
    queryFn: () => api.automations.get(automationId),
    refetchInterval: 10000,
  });

  const automation = data?.data;
  usePageTitle(automation?.name, "Automation");

  const { data: repositoryResponse } = useQuery({
    queryKey: ["repository", automation?.repository_id],
    queryFn: () => api.repositories.get(automation?.repository_id ?? ""),
    enabled: !!automation?.repository_id,
  });

  const pauseMutation = useMutation({
    mutationFn: () => api.automations.pause(automationId),
    onSuccess: (res) => {
      upsertAutomationInListCaches(queryClient, res.data);
      queryClient.setQueryData(queryKeys.automations.detail(res.data.id), res);
      return Promise.all([
        queryClient.invalidateQueries({
          queryKey: queryKeys.automations.detail(res.data.id),
        }),
        queryClient.invalidateQueries({ queryKey: queryKeys.automations.all }),
      ]);
    },
  });

  const resumeMutation = useMutation({
    mutationFn: () => api.automations.resume(automationId),
    onSuccess: (res) => {
      upsertAutomationInListCaches(queryClient, res.data);
      queryClient.setQueryData(queryKeys.automations.detail(res.data.id), res);
      return Promise.all([
        queryClient.invalidateQueries({
          queryKey: queryKeys.automations.detail(res.data.id),
        }),
        queryClient.invalidateQueries({ queryKey: queryKeys.automations.all }),
      ]);
    },
  });

  // runNowInFlight guards against rapid double-clicks that can slip through
  // `disabled={runNowMutation.isPending}`: React updates `isPending` on its
  // next render tick, so two clicks in the same tick both see `isPending=false`
  // and both fire mutate(). A synchronous ref flipped inside the click handler
  // closes that window without waiting for a render.
  const runNowInFlight = useRef(false);
  const runNowMutation = useMutation({
    mutationFn: () => api.automations.runNow(automationId),
    onSuccess: () =>
      queryClient.invalidateQueries({
        queryKey: ["automation-runs", automationId],
      }),
    onSettled: () => {
      runNowInFlight.current = false;
    },
  });
  const handleRunNow = () => {
    if (runNowInFlight.current || runNowMutation.isPending) return;
    runNowInFlight.current = true;
    runNowMutation.mutate();
  };

  const deleteMutation = useMutation({
    mutationFn: () => api.automations.del(automationId),
    onSuccess: () => {
      removeAutomationFromListCaches(queryClient, automationId);
      queryClient.removeQueries({
        queryKey: queryKeys.automations.detail(automationId),
      });
      queryClient.invalidateQueries({ queryKey: queryKeys.automations.all });
      router.push("/automations");
    },
  });

  const iconMutation = useMutation({
    mutationFn: (iconValue: string) =>
      api.automations.update(automationId, {
        icon_type: "emoji",
        icon_value: iconValue,
      }),
    onMutate: async (iconValue: string) => {
      await queryClient.cancelQueries({
        queryKey: queryKeys.automations.detail(automationId),
      });
      const previous = queryClient.getQueryData<typeof data>(
        queryKeys.automations.detail(automationId),
      );
      queryClient.setQueryData<typeof data>(
        queryKeys.automations.detail(automationId),
        (current) => {
          if (!current?.data) return current;
          return {
            ...current,
            data: {
              ...current.data,
              icon_type: "emoji",
              icon_value: iconValue,
            },
          };
        },
      );
      return { previous };
    },
    onError: (_err, _iconValue, context) => {
      if (context?.previous) {
        queryClient.setQueryData(
          queryKeys.automations.detail(automationId),
          context.previous,
        );
      }
    },
    onSuccess: (updated) => {
      upsertAutomationInListCaches(queryClient, updated.data);
      queryClient.setQueryData(
        queryKeys.automations.detail(automationId),
        updated,
      );
      queryClient.invalidateQueries({ queryKey: queryKeys.automations.all });
    },
    onSettled: () => {
      queryClient.invalidateQueries({
        queryKey: queryKeys.automations.detail(automationId),
      });
    },
  });

  if (isLoading) {
    return <AutomationDetailPageSkeleton />;
  }

  if (!automation) {
    return (
      <PageContainer size="default">
        <div className="space-y-6">
          <MobileBackButton to="/automations" label="Back to automations" />
          <PageHeader
            title="Automation not found"
            description="This automation does not exist or has been deleted."
          />
        </div>
      </PageContainer>
    );
  }

  const schedule = formatAutomationSchedule(automation);

  const headerDescription = automation.enabled
    ? automation.next_run_at
      ? `${schedule} · Next: ${formatDateTime(automation.next_run_at)}`
      : schedule
    : `${schedule} · Paused`;

  // Surface the most recent failure across the header mutations. These are
  // user-initiated actions (pause/resume/run now/delete) so silent failure is
  // worse than a potentially stale banner — the user needs to know the click
  // did not take effect before deciding whether to retry.
  const headerError = pauseMutation.isError
    ? "Failed to pause automation."
    : resumeMutation.isError
      ? "Failed to resume automation."
      : runNowMutation.isError
        ? "Failed to trigger run."
        : iconMutation.isError
          ? "Failed to update automation emoji."
          : deleteMutation.isError
            ? "Failed to delete automation."
            : null;

  const runActions = canManage ? (
    <div className="flex flex-wrap items-center gap-2">
      {automation.enabled ? (
        <Button
          variant="outline"
          size="sm"
          onClick={() => pauseMutation.mutate()}
          disabled={pauseMutation.isPending}
        >
          {pauseMutation.isPending ? (
            <Loader2 className="h-3.5 w-3.5 mr-1.5 animate-spin" />
          ) : (
            <Pause className="h-3.5 w-3.5 mr-1.5" />
          )}
          Pause
        </Button>
      ) : (
        <Button
          variant="outline"
          size="sm"
          onClick={() => resumeMutation.mutate()}
          disabled={resumeMutation.isPending}
        >
          {resumeMutation.isPending ? (
            <Loader2 className="h-3.5 w-3.5 mr-1.5 animate-spin" />
          ) : (
            <Play className="h-3.5 w-3.5 mr-1.5" />
          )}
          Resume
        </Button>
      )}
      <Button
        size="sm"
        onClick={handleRunNow}
        disabled={runNowMutation.isPending}
      >
        {runNowMutation.isPending ? (
          <Loader2 className="h-3.5 w-3.5 mr-1.5 animate-spin" />
        ) : (
          <Play className="h-3.5 w-3.5 mr-1.5" />
        )}
        Run now
      </Button>
    </div>
  ) : undefined;
  const headerActions = canManage ? (
    <div className="flex flex-wrap items-center gap-2">
      <Button variant="outline" size="sm" onClick={() => setSettingsOpen(true)}>
        <Settings2 className="mr-1.5 h-3.5 w-3.5" />
        Edit
      </Button>
      <Button
        variant="outline"
        size="sm"
        className="lg:hidden"
        onClick={() => setDetailsOpen(true)}
      >
        Details
      </Button>
    </div>
  ) : (
    <Button
      variant="outline"
      size="sm"
      className="lg:hidden"
      onClick={() => setDetailsOpen(true)}
    >
      Details
    </Button>
  );
  const repositoryName =
    repositoryResponse?.data.full_name ?? automation.repository_id ?? "-";

  return (
    <PageContainer size="wide">
      <div className="space-y-6">
        <MobileBackButton to="/automations" label="Back to automations" />
        <Sheet modal={false} open={settingsOpen} onOpenChange={setSettingsOpen}>
          <SheetContent className="sm:max-w-2xl">
            <SheetHeader>
              <SheetTitle>Automation settings</SheetTitle>
              <SheetDescription>
                Update triggers and recurring execution defaults.
              </SheetDescription>
            </SheetHeader>
            <div className="mt-6">
              <SettingsTab
                key={automation.id}
                automation={automation}
                canManage={canManage}
              />
            </div>
          </SheetContent>
        </Sheet>
        <Sheet open={detailsOpen} onOpenChange={setDetailsOpen}>
          <SheetContent className="sm:max-w-md">
            <SheetHeader>
              <SheetTitle>Automation details</SheetTitle>
              <SheetDescription>
                Schedule, identity, model, and recent run controls.
              </SheetDescription>
            </SheetHeader>
            <div className="mt-6">
              <AutomationDetailRail
                automation={automation}
                schedule={schedule}
                repositoryName={repositoryName}
                runActions={runActions}
              />
            </div>
          </SheetContent>
        </Sheet>
        <PageHeader
          title={
            <span className="inline-flex min-w-0 items-center gap-2">
              {canManage ? (
                <AutomationEmojiPicker
                  value={automation.icon_value || "⚙️"}
                  onChange={(iconValue) => iconMutation.mutate(iconValue)}
                  trigger="inline"
                  triggerLabel="Change automation emoji"
                  disabled={iconMutation.isPending}
                />
              ) : (
                <span
                  className="shrink-0 align-baseline text-[0.95em] leading-none"
                  aria-label={`Automation icon for ${automation.name}`}
                >
                  {automation.icon_value || "⚙️"}
                </span>
              )}
              <InlineAutomationText
                automation={automation}
                canManage={canManage}
                field="name"
              />
            </span>
          }
          description={headerDescription}
          action={headerActions}
        />

        {headerError && (
          <div className="rounded-md border border-destructive/30 bg-destructive/5 px-3 py-2 text-xs text-destructive">
            {headerError}
          </div>
        )}

        <div className="grid gap-6 lg:grid-cols-[minmax(0,1fr)_22rem] lg:items-start">
          <main className="min-w-0 space-y-6">
            <InlineAutomationText
              automation={automation}
              canManage={canManage}
              field="goal"
            />

            <LatestRunSummary automationId={automationId} />

            <DecisionHistory automationId={automationId} />
          </main>

          <aside className="hidden space-y-4 lg:sticky lg:top-4 lg:block">
            <AutomationDetailRail
              automation={automation}
              schedule={schedule}
              repositoryName={repositoryName}
              runActions={runActions}
            />
            <AutomationStatsCard automationId={automationId} />
          </aside>
        </div>
      </div>
    </PageContainer>
  );
}

function AutomationDetailRail({
  automation,
  schedule,
  repositoryName,
  runActions,
}: {
  automation: Automation;
  schedule: string;
  repositoryName: string;
  runActions?: ReactNode;
}) {
  const prTriggerLabels = githubEventsToAutomationProductTriggers(
    automation.github_event_triggers ?? [],
  )
    .map(
      (trigger) =>
        automationProductTriggerOptions.find(
          (option) => option.value === trigger,
        )?.label,
    )
    .filter((label): label is string => Boolean(label));
  const triggerSummary =
    [automation.schedule_type === "none" ? null : schedule, ...prTriggerLabels]
      .filter((value): value is string => Boolean(value))
      .join(", ") || "-";

  return (
    <section className="rounded-lg border border-border bg-card p-4">
      <div className="space-y-4">
        <div className="flex items-center justify-between gap-3">
          <h2 className="text-sm font-semibold text-foreground">Status</h2>
          <Badge variant={automation.enabled ? "default" : "secondary"}>
            {automation.enabled ? "Active" : "Paused"}
          </Badge>
        </div>
        {runActions}
        <DetailList
          items={[
            [
              "Next run",
              automation.next_run_at
                ? formatDateTime(automation.next_run_at)
                : "-",
            ],
            [
              "Last ran",
              automation.last_run_at
                ? formatDateTime(automation.last_run_at)
                : "-",
            ],
            ["Repository", repositoryName],
            ["Triggers", triggerSummary],
            [
              "Runs as",
              automation.identity_scope === "personal"
                ? "Personal"
                : "Organization",
            ],
            [
              "Model",
              automation.model_override || automation.agent_type || "Auto",
            ],
            ["Reasoning", automation.reasoning_effort || "Default"],
            ["Base branch", automation.base_branch || "-"],
            [
              "After success",
              automation.publish_policy === "none"
                ? "Do not publish"
                : "Open a pull request",
            ],
            ["Priority", priorityLabel(automation.priority)],
            ["Scope", automation.scope || "-"],
          ]}
        />
      </div>
    </section>
  );
}

function DetailList({ items }: { items: Array<[string, string]> }) {
  return (
    <dl className="space-y-3 text-sm">
      {items.map(([label, value]) => (
        <div
          key={label}
          className="grid grid-cols-[6.5rem_minmax(0,1fr)] gap-3"
        >
          <dt className="text-xs font-medium text-muted-foreground">{label}</dt>
          <dd className="min-w-0 break-words text-xs text-foreground">
            {value}
          </dd>
        </div>
      ))}
    </dl>
  );
}

function priorityLabel(priority?: number): string {
  if (priority === undefined) return "Medium";
  if (priority <= 0) return "Critical";
  if (priority <= 25) return "High";
  if (priority <= 50) return "Medium";
  return "Low";
}

function LatestRunSummary({ automationId }: { automationId: string }) {
  const { data, isLoading } = useQuery({
    queryKey: ["automation-runs", automationId, "recent"],
    queryFn: () => api.automations.listRuns(automationId, { limit: 5 }),
    refetchInterval: 10_000,
  });
  const latest = data?.data?.[0];

  return (
    <section className="rounded-lg border border-border bg-card p-5">
      <h2 className="text-sm font-semibold text-foreground">
        Latest execution
      </h2>
      <p className="mt-1 text-xs text-muted-foreground">
        Operational status only. Review outcomes are shown in PR decisions.
      </p>
      {isLoading ? (
        <p className="mt-3 text-sm text-muted-foreground">
          Loading latest run...
        </p>
      ) : latest ? (
        <LatestRunBody run={latest} />
      ) : (
        <p className="mt-3 text-sm text-muted-foreground">
          No runs yet. The first run will appear here after the schedule fires
          or when you run it manually.
        </p>
      )}
    </section>
  );
}

function LatestRunBody({ run }: { run: AutomationRun }) {
  const summary =
    run.result_summary || run.session?.title || statusLabel(run.status);
  return (
    <div className="mt-3 space-y-2">
      <div className="flex flex-wrap items-center gap-2">
        <Badge variant={run.status === "failed" ? "destructive" : "secondary"}>
          Execution: {statusLabel(run.status)}
        </Badge>
        <span className="text-xs text-muted-foreground">
          {formatTimeAgo(run.triggered_at)}
          {run.completed_at ? ` · ${formatDateTime(run.completed_at)}` : ""}
        </span>
      </div>
      <p className="text-sm text-foreground">{summary}</p>
      {run.session?.id ? (
        <Button asChild variant="outline" size="sm">
          <Link href={`/sessions/${run.session.id}`}>Open session</Link>
        </Button>
      ) : null}
    </div>
  );
}

function statusLabel(status: AutomationRun["status"]): string {
  switch (status) {
    case "completed_noop":
      return "No-op";
    default:
      return status
        .replaceAll("_", " ")
        .replace(/^./, (letter) => letter.toUpperCase());
  }
}
