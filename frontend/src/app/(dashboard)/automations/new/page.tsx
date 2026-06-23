"use client";

import Link from "next/link";
import { useEffect, useMemo, useRef, useState } from "react";
import { useMutation, useQuery } from "@tanstack/react-query";
import {
  ChevronDown,
  Loader2,
  Minus,
  Plus,
  Settings2,
  Sparkles,
} from "lucide-react";
import { useRouter, useSearchParams } from "next/navigation";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import {
  Command,
  CommandEmpty,
  CommandGroup,
  CommandInput,
  CommandItem,
  CommandList,
} from "@/components/ui/command";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from "@/components/ui/popover";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import {
  Sheet,
  SheetClose,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
  SheetTrigger,
} from "@/components/ui/sheet";
import { api } from "@/lib/api";
import { agentTypeForModel } from "@/lib/agents";
import { automationGoalLengthState } from "@/lib/automation-validation";
import {
  automationProductTriggerOptions,
  type AutomationProductTrigger,
} from "@/lib/automation-triggers";
import { useAuth } from "@/hooks/use-auth";
import { BranchPicker } from "@/components/branch-picker";
import { AutomationComposer } from "@/components/automation-composer";
import { AutomationGoalImprovementControl } from "@/components/automation-goal-improvement";
import {
  AutomationCapabilitiesEditor,
  capabilitySummary,
  normalizeCapabilityGrants,
} from "@/components/automation-capabilities-editor";
import { AutomationModelSelect } from "@/components/automation-model-select";
import { NoReposWarning } from "@/components/no-repos-warning";
import { DisabledTooltip } from "@/components/ui/disabled-tooltip";
import { PageContainer } from "@/components/page-container";
import { PageHeader } from "@/components/page-header";
import {
  automationTemplates,
  featuredAutomationTemplateIDs,
  getAutomationTemplate,
  type AutomationTemplate,
} from "@/lib/automation-templates";
import {
  getCodingAgentReasoningOptions,
  supportsReasoningEffort,
  toCodingAgentReasoningEffort,
  type CodingAgentReasoningEffort,
} from "@/lib/coding-agent-reasoning";
import type {
  AgentCapabilityDefinition,
  AgentCapabilityGrant,
  AutomationEventTriggerInput,
  AutomationGitHubEventFilters,
  ListResponse,
  PagerDutyEventType,
  PagerDutyEventTriggerFilter,
} from "@/lib/types";
import { queryKeys } from "@/lib/query-keys";
import { browserTimezone, hourOptions, minuteOptions } from "../schedule-time";
import { TimezonePicker } from "../timezone-picker";

const pagerDutyEventTypeOptions: Array<{
  value: PagerDutyEventType;
  label: string;
  ariaLabel: string;
}> = [
  {
    value: "incident.triggered",
    label: "Triggered",
    ariaLabel: "PagerDuty triggered events",
  },
  {
    value: "incident.annotated",
    label: "Annotated",
    ariaLabel: "PagerDuty annotated events",
  },
  {
    value: "incident.priority_updated",
    label: "Priority updated",
    ariaLabel: "PagerDuty priority updated events",
  },
  {
    value: "incident.acknowledged",
    label: "Acknowledged",
    ariaLabel: "PagerDuty acknowledged events",
  },
  {
    value: "incident.resolved",
    label: "Resolved",
    ariaLabel: "PagerDuty resolved events",
  },
];

function formatWeeklyRunHint(
  intervalValue: number,
  intervalRunHour: string,
  intervalRunMinute: string,
  timezone: string,
  now = new Date(),
): string {
  const weeks = Math.max(1, intervalValue);
  const localParts = new Intl.DateTimeFormat(undefined, {
    timeZone: timezone || "UTC",
    weekday: "long",
    hour: "2-digit",
    minute: "2-digit",
    hourCycle: "h23",
  }).formatToParts(now);
  const weekday = localParts.find((part) => part.type === "weekday")?.value;
  const localHour = Number(
    localParts.find((part) => part.type === "hour")?.value ?? "0",
  );
  const localMinute = Number(
    localParts.find((part) => part.type === "minute")?.value ?? "0",
  );
  const selectedHour = Number(intervalRunHour);
  const selectedMinute = Number(intervalRunMinute);
  const selectedBeforeNow =
    selectedHour < localHour ||
    (selectedHour === localHour && selectedMinute < localMinute);
  const nextCalendarDay = new Date(now);
  nextCalendarDay.setDate(nextCalendarDay.getDate() + 1);
  const anchor = selectedBeforeNow
    ? new Intl.DateTimeFormat(undefined, {
        timeZone: timezone || "UTC",
        weekday: "long",
      }).format(nextCalendarDay)
    : weekday;
  const unitLabel = weeks === 1 ? "week" : "weeks";

  return `First run anchors on ${anchor ?? "the selected weekday"}, then repeats every ${weeks} ${unitLabel}.`;
}

export default function NewAutomationPage() {
  const router = useRouter();
  const searchParams = useSearchParams();
  const { user, isLoading } = useAuth();
  const canManage = user?.role === "admin" || user?.role === "member";
  const initialTemplate = getAutomationTemplate(
    searchParams.get("template") ?? "",
  );
  const goalEditorRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!isLoading && !canManage) {
      router.replace("/automations");
    }
  }, [canManage, isLoading, router]);

  const [name, setName] = useState(initialTemplate?.name ?? "");
  const [goal, setGoal] = useState(initialTemplate?.goal ?? "");
  const [iconValue, setIconValue] = useState("⚙️");
  const [scope, setScope] = useState("");
  const [selectedRepoId, setSelectedRepoId] = useState("");
  const [intervalValue, setIntervalValue] = useState(
    initialTemplate?.defaultInterval ?? 1,
  );
  const [intervalUnit, setIntervalUnit] = useState<"hours" | "days" | "weeks">(
    initialTemplate?.defaultUnit ?? "days",
  );
  const [intervalRunHour, setIntervalRunHour] = useState("09");
  const [intervalRunMinute, setIntervalRunMinute] = useState("00");
  const [detectedTimezone] = useState<string>(() => browserTimezone());
  const [timezone, setTimezone] = useState<string>(detectedTimezone);
  const [scheduleEnabled, setScheduleEnabled] = useState(true);
  const [productTriggers, setProductTriggers] = useState<
    AutomationProductTrigger[]
  >([]);
  const [triggerBaseBranches, setTriggerBaseBranches] = useState("");
  const [triggerAuthors, setTriggerAuthors] = useState("");
  const [triggerPaths, setTriggerPaths] = useState("");
  const [triggerFeedbackTypes, setTriggerFeedbackTypes] = useState("");
  const [triggerReviewStates, setTriggerReviewStates] = useState("");
  const [pagerDutyEnabled, setPagerDutyEnabled] = useState(false);
  const [pagerDutyEventTypes, setPagerDutyEventTypes] = useState<
    PagerDutyEventType[]
  >(["incident.triggered"]);
  const [pagerDutyServiceIDs, setPagerDutyServiceIDs] = useState("");
  const [pagerDutyTeamIDs, setPagerDutyTeamIDs] = useState("");
  const [pagerDutyStatuses, setPagerDutyStatuses] = useState("");
  const [pagerDutyUrgency, setPagerDutyUrgency] = useState<"high" | "low">("high");
  const [pagerDutyPriorityNames, setPagerDutyPriorityNames] = useState("");
  const [pagerDutyIncidentTypes, setPagerDutyIncidentTypes] = useState("");
  const [pagerDutyTitleContains, setPagerDutyTitleContains] = useState("");
  const [pagerDutyCustomFields, setPagerDutyCustomFields] = useState("");
  const [pagerDutyCooldownMinutes, setPagerDutyCooldownMinutes] = useState("0");
  const [advancedOpen, setAdvancedOpen] = useState(false);
  const [templateOpen, setTemplateOpen] = useState(false);
  const [baseBranchByRepoId, setBaseBranchByRepoId] = useState<
    Record<string, string>
  >({});
  const [model, setModel] = useState<string | undefined>(undefined);
  const [identityScope, setIdentityScope] = useState<"org" | "personal">("org");
  const [prePRReviewLoops, setPrePRReviewLoops] = useState(1);
  const [reasoningEffort, setReasoningEffort] =
    useState<CodingAgentReasoningEffort>("");
  const [priority, setPriority] = useState(50);
  const [capabilityOverride, setCapabilityOverride] = useState<
    AgentCapabilityGrant[] | null
  >(null);
  const [redirecting, setRedirecting] = useState(false);

  const { data: settingsResponse } = useQuery({
    queryKey: ["settings"],
    queryFn: () => api.settings.get(),
  });
  const settings = (settingsResponse?.data?.settings ?? {}) as {
    default_agent_type?: string;
  };

  const { data: reposData } = useQuery({
    queryKey: ["repositories"],
    queryFn: () => api.repositories.list(),
  });
  const repos = reposData?.data ?? [];

  const { data: pagerDutyResp } = useQuery({
    queryKey: queryKeys.integrations.pagerDuty,
    queryFn: () => api.integrations.listPagerDuty(),
  });
  const pagerDutyConnected = (pagerDutyResp?.data ?? []).some(
    (integration) => integration.status === "active",
  );

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
  const capabilityGrants = useMemo(
    () =>
      capabilityOverride ?? normalizeCapabilityGrants(capabilityCatalog, []),
    [capabilityCatalog, capabilityOverride],
  );

  const repoId = selectedRepoId || repos[0]?.id || "";
  const selectedRepo = repos.find((repo) => repo.id === repoId);
  const selectedBaseBranch = repoId
    ? (baseBranchByRepoId[repoId] ?? selectedRepo?.default_branch ?? "")
    : "";
  const defaultAgentType = settings.default_agent_type ?? "codex";
  const effectiveAgentType = model
    ? (agentTypeForModel(model) ?? defaultAgentType)
    : defaultAgentType;
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
  const prePRReviewDescription = supportsNativeReviewLoop
    ? effectivePrePRReviewLoops === 0
      ? "Off"
      : "Runs the coding agent's review/fix loop before opening a PR."
    : "Off for agents without review-loop support.";
  const showReasoningSelector = supportsReasoningEffort(effectiveAgentType);
  const reasoningOptions = getCodingAgentReasoningOptions(effectiveAgentType);

  const applyTemplate = (template: AutomationTemplate) => {
    setName(template.name);
    setGoal(template.goal);
    setIntervalValue(template.defaultInterval);
    setIntervalUnit(template.defaultUnit);
    setTemplateOpen(false);
    requestAnimationFrame(() => {
      goalEditorRef.current?.querySelector("textarea")?.focus();
    });
  };

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
  const togglePagerDutyEventType = (
    eventType: PagerDutyEventType,
    checked: boolean,
  ) => {
    setPagerDutyEventTypes((current) => {
      if (checked) {
        return current.includes(eventType) ? current : [...current, eventType];
      }
      return current.filter((item) => item !== eventType);
    });
  };

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
  const pagerDutyEventTriggers = buildPagerDutyEventTriggers(
    pagerDutyEnabled,
    pagerDutyEventTypes,
    pagerDutyServiceIDs,
    pagerDutyTeamIDs,
    pagerDutyStatuses,
    pagerDutyUrgency,
    pagerDutyPriorityNames,
    pagerDutyIncidentTypes,
    pagerDutyTitleContains,
    pagerDutyCustomFields,
    pagerDutyCooldownMinutes,
    repoId,
  );
  const pagerDutyTriggerValid =
    !pagerDutyEnabled ||
    (pagerDutyEventTypes.length > 0 &&
      commaList(pagerDutyServiceIDs).length > 0 &&
      (pagerDutyUrgency.length > 0 ||
        commaList(pagerDutyPriorityNames).length > 0));
  const hasEventTriggers =
    productTriggers.length > 0 || pagerDutyEventTriggers.length > 0;

  const createMutation = useMutation({
    mutationFn: () =>
      api.automations.create({
        name: name.trim(),
        goal: goal.trim(),
        icon_type: "emoji",
        icon_value: iconValue,
        repository_id: repoId,
        scope: scope.trim() || undefined,
        schedule_type: scheduleEnabled ? "interval" : "none",
        ...(scheduleEnabled
          ? {
              interval_value: intervalValue,
              interval_unit: intervalUnit,
              interval_run_at: `${intervalRunHour}:${intervalRunMinute}`,
            }
          : {}),
        timezone,
        triggers: productTriggers,
        github_event_filters: githubEventFilters,
        ...(pagerDutyEventTriggers.length > 0
          ? { event_triggers: pagerDutyEventTriggers }
          : {}),
        model,
        identity_scope: identityScope,
        pre_pr_review_loops: effectivePrePRReviewLoops,
        ...(showReasoningSelector && reasoningEffort
          ? { reasoning_effort: reasoningEffort }
          : {}),
        base_branch: selectedBaseBranch.trim() || undefined,
        priority,
        ...(capabilityOverride ? { capabilities: capabilityOverride } : {}),
      }),
    onSuccess: (res) => {
      setRedirecting(true);
      router.push(`/automations/${res.data.id}`);
    },
  });

  if (!isLoading && !canManage) {
    return null;
  }

  if (repos.length === 0 && reposData) {
    return (
      <PageContainer size="default">
        <div className="space-y-6">
          <PageHeader
            title="New automation"
            description="Create a recurring agent for this team."
          />
          <NoReposWarning />
        </div>
      </PageContainer>
    );
  }

  const goalLength = automationGoalLengthState(goal);
  const canSubmit =
    name.trim().length > 0 &&
    goal.trim().length > 0 &&
    !goalLength.isTooLong &&
    repoId.length > 0 &&
    pagerDutyTriggerValid &&
    (scheduleEnabled || hasEventTriggers);
  const submitDisabledReason = createMutation.isPending || redirecting
    ? undefined
    : getCreateDisabledReason({
        name: name.trim(),
        goal: goal.trim(),
        goalTooLong: goalLength.isTooLong,
        hasRepository: repoId.length > 0,
        pagerDutyTriggerValid,
        scheduleEnabled,
        hasEventTriggers,
      });

  return (
    <PageContainer size="default">
      <div className="space-y-6">
        <PageHeader
          title="New automation"
          description="Create a recurring agent for this team."
        />

        <AutomationComposer
          name={name}
          onNameChange={setName}
          iconValue={iconValue}
          onIconChange={setIconValue}
          goal={goal}
          onGoalChange={setGoal}
          repositoryId={repoId || undefined}
          branch={
            selectedBaseBranch || selectedRepo?.default_branch || undefined
          }
          agentType={effectiveAgentType}
          goalEditorContainerRef={goalEditorRef}
          goalImprovementControls={
            <AutomationGoalImprovementControl
              name={name}
              goal={goal}
              repositoryId={repoId || undefined}
              scope={scope.trim() || undefined}
              config={{
                schedule_type: scheduleEnabled ? "interval" : "none",
                triggers: productTriggers,
                github_event_filters: githubEventFilters,
                event_triggers: pagerDutyEventTriggers,
                base_branch: selectedBaseBranch.trim() || undefined,
                agent_type: effectiveAgentType,
                model,
                reasoning_effort:
                  showReasoningSelector && reasoningEffort
                    ? reasoningEffort
                    : undefined,
                pre_pr_review_loops: effectivePrePRReviewLoops,
              }}
              disabled={createMutation.isPending || redirecting}
              onDraftApply={setGoal}
            />
          }
          footerControls={
            <>
                <Select value={repoId} onValueChange={setSelectedRepoId}>
                  <SelectTrigger
                    className="h-8 w-full border-transparent bg-muted/25 shadow-none hover:bg-muted/50 sm:w-[210px]"
                    aria-label="Repository"
                  >
                    <SelectValue placeholder="Select repo" />
                  </SelectTrigger>
                  <SelectContent>
                    {repos.map((repo) => (
                      <SelectItem key={repo.id} value={repo.id}>
                        {repo.full_name}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>

                <div
                  role="group"
                  aria-label="Automation triggers"
                  className="flex w-full flex-col gap-2 rounded-lg bg-muted/25 px-3 py-2"
                >
                  <div className="flex flex-wrap items-center gap-x-3 gap-y-2">
                    <span className="text-sm font-medium leading-none text-muted-foreground">
                      Triggers
                    </span>
                    <Label className="flex min-h-7 cursor-pointer items-center gap-2 text-sm font-normal">
                      <Checkbox
                        checked={scheduleEnabled}
                        onCheckedChange={(checked) =>
                          setScheduleEnabled(checked === true)
                        }
                        aria-label="On a schedule"
                      />
                      <span className="block">on a schedule</span>
                    </Label>
                  </div>
                  {scheduleEnabled ? (
                    <div className="flex flex-wrap items-center gap-2">
                      <span className="text-sm font-medium leading-none text-muted-foreground">
                        Run every
                      </span>
                      <Input
                        id="interval-value"
                        aria-label="Interval value"
                        type="number"
                        min={1}
                        max={365}
                        value={intervalValue}
                        onChange={(e) => {
                          const parsed = parseInt(e.target.value, 10);
                          setIntervalValue(
                            Number.isNaN(parsed) ? 1 : Math.max(1, parsed),
                          );
                        }}
                        className="h-8 w-20 px-2 text-base sm:text-xs"
                      />
                      <Select
                        value={intervalUnit}
                        onValueChange={(v) => {
                          if (v === "hours" || v === "days" || v === "weeks") {
                            setIntervalUnit(v);
                          }
                        }}
                      >
                        <SelectTrigger
                          className="h-8 w-24 text-base sm:text-xs"
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
                      <span className="text-sm font-medium leading-none text-muted-foreground">
                        at
                      </span>
                      <Select
                        value={intervalRunHour}
                        onValueChange={setIntervalRunHour}
                      >
                        <SelectTrigger
                          className="h-8 w-20 text-base sm:text-xs"
                          aria-label="Run at hour"
                        >
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
                          className="h-8 w-20 text-base sm:text-xs"
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
                        className="w-full sm:w-auto"
                      />
                      {intervalUnit === "weeks" ? (
                        <p className="basis-full text-xs text-muted-foreground">
                          {formatWeeklyRunHint(
                            intervalValue,
                            intervalRunHour,
                            intervalRunMinute,
                            timezone,
                          )}
                        </p>
                      ) : null}
                    </div>
                  ) : null}
                  <div className="space-y-1">
                    <span className="text-xs font-medium text-muted-foreground">
                      Pull request events
                    </span>
                    <div className="grid gap-x-4 gap-y-1 sm:grid-cols-2">
                      {automationProductTriggerOptions.map((option) => (
                        <Label
                          key={option.value}
                          className="flex min-h-6 cursor-pointer items-center gap-2 text-sm font-normal"
                        >
                          <Checkbox
                            checked={productTriggers.includes(option.value)}
                            onCheckedChange={(checked) =>
                              toggleProductTrigger(
                                option.value,
                                checked === true,
                              )
                            }
                            aria-label={option.label}
                          />
                          <span className="min-w-0 leading-snug">
                            {option.label}
                          </span>
                        </Label>
                      ))}
                    </div>
                  </div>
                  <div className="space-y-2">
                    <span className="text-xs font-medium text-muted-foreground">
                      Incident events
                    </span>
                    <Label className="flex min-h-6 cursor-pointer items-center gap-2 text-sm font-normal">
                      <Checkbox
                        checked={pagerDutyEnabled}
                        disabled={!pagerDutyConnected}
                        onCheckedChange={(checked) =>
                          setPagerDutyEnabled(checked === true)
                        }
                        aria-label="PagerDuty incidents"
                      />
                      <span className="min-w-0 leading-snug">
                        PagerDuty incidents
                      </span>
                    </Label>
                    {!pagerDutyConnected ? (
                      <p className="text-xs text-muted-foreground">
                        Connect PagerDuty in settings to use incident triggers.
                      </p>
                    ) : null}
                    {pagerDutyEnabled ? (
                      <div className="space-y-3">
                        <div className="grid gap-x-4 gap-y-1 sm:grid-cols-2">
                          {pagerDutyEventTypeOptions.map((option) => (
                            <Label
                              key={option.value}
                              className="flex min-h-6 cursor-pointer items-center gap-2 text-sm font-normal"
                            >
                              <Checkbox
                                checked={pagerDutyEventTypes.includes(
                                  option.value,
                                )}
                                onCheckedChange={(checked) =>
                                  togglePagerDutyEventType(
                                    option.value,
                                    checked === true,
                                  )
                                }
                                aria-label={option.ariaLabel}
                              />
                              <span className="min-w-0 leading-snug">
                                {option.label}
                              </span>
                            </Label>
                          ))}
                        </div>
                        <div className="grid gap-2 sm:grid-cols-[minmax(0,1fr)_9rem]">
                          <Input
                            aria-label="PagerDuty service IDs"
                            placeholder="Service IDs, comma-separated"
                            value={pagerDutyServiceIDs}
                            onChange={(event) =>
                              setPagerDutyServiceIDs(event.target.value)
                            }
                            className="h-8"
                          />
                          <Select
                            value={pagerDutyUrgency}
                            onValueChange={(value) => {
                              if (value === "high" || value === "low") {
                                setPagerDutyUrgency(value);
                              }
                            }}
                          >
                            <SelectTrigger
                              className="h-8"
                              aria-label="PagerDuty urgency"
                            >
                              <SelectValue />
                            </SelectTrigger>
                            <SelectContent>
                              <SelectItem value="high">High</SelectItem>
                              <SelectItem value="low">Low</SelectItem>
                            </SelectContent>
                          </Select>
                        </div>
                        <div className="grid gap-2 sm:grid-cols-2">
                          <Input
                            aria-label="PagerDuty team IDs"
                            placeholder="Team IDs, comma-separated"
                            value={pagerDutyTeamIDs}
                            onChange={(event) =>
                              setPagerDutyTeamIDs(event.target.value)
                            }
                            className="h-8"
                          />
                          <Input
                            aria-label="PagerDuty statuses"
                            placeholder="Statuses: triggered, acknowledged"
                            value={pagerDutyStatuses}
                            onChange={(event) =>
                              setPagerDutyStatuses(event.target.value)
                            }
                            className="h-8"
                          />
                          <Input
                            aria-label="PagerDuty priority names"
                            placeholder="Priority names, comma-separated"
                            value={pagerDutyPriorityNames}
                            onChange={(event) =>
                              setPagerDutyPriorityNames(event.target.value)
                            }
                            className="h-8"
                          />
                          <Input
                            aria-label="PagerDuty incident types"
                            placeholder="Incident types, comma-separated"
                            value={pagerDutyIncidentTypes}
                            onChange={(event) =>
                              setPagerDutyIncidentTypes(event.target.value)
                            }
                            className="h-8"
                          />
                          <Input
                            aria-label="PagerDuty title contains"
                            placeholder="Title contains"
                            value={pagerDutyTitleContains}
                            onChange={(event) =>
                              setPagerDutyTitleContains(event.target.value)
                            }
                            className="h-8"
                          />
                          <Input
                            aria-label="PagerDuty cooldown minutes"
                            type="number"
                            min={0}
                            max={10080}
                            value={pagerDutyCooldownMinutes}
                            onChange={(event) =>
                              setPagerDutyCooldownMinutes(event.target.value)
                            }
                            className="h-8"
                          />
                          <Input
                            aria-label="PagerDuty custom fields"
                            placeholder="field=value, field=other"
                            value={pagerDutyCustomFields}
                            onChange={(event) =>
                              setPagerDutyCustomFields(event.target.value)
                            }
                            className="h-8 sm:col-span-2"
                          />
                        </div>
                      </div>
                    ) : null}
                    {pagerDutyEnabled && !pagerDutyTriggerValid ? (
                      <p className="text-xs text-destructive">
                        Add at least one PagerDuty service ID.
                      </p>
                    ) : null}
                  </div>
                  {!scheduleEnabled && !hasEventTriggers ? (
                    <p className="text-xs text-destructive">
                      Select at least one trigger.
                    </p>
                  ) : null}
                </div>
              </>
            }
            secondaryControls={
              <>
                <TemplatePicker
                  open={templateOpen}
                  onOpenChange={setTemplateOpen}
                  onSelect={applyTemplate}
                />
                <Button asChild variant="ghost" size="sm">
                  <Link href="/automations/templates">
                    Browse all templates
                  </Link>
                </Button>
                <Sheet
                  modal={false}
                  open={advancedOpen}
                  onOpenChange={setAdvancedOpen}
                >
                  <SheetTrigger asChild>
                    <Button type="button" variant="outline" size="sm">
                      <Settings2 className="mr-2 h-4 w-4" />
                      Advanced options
                    </Button>
                  </SheetTrigger>
                  <SheetContent className="sm:max-w-lg">
                    <SheetHeader>
                      <SheetTitle>Advanced settings</SheetTitle>
                      <SheetDescription>
                        Tune lower-frequency defaults for identity, model,
                        branch, scope, priority, and review.
                      </SheetDescription>
                    </SheetHeader>
                    <div className="mt-6 space-y-5">
                      <div className="space-y-1.5">
                        <Label htmlFor="scope">
                          Scope{" "}
                          <span className="font-normal text-muted-foreground">
                            (optional)
                          </span>
                        </Label>
                        <Input
                          id="scope"
                          value={scope}
                          onChange={(e) => setScope(e.target.value)}
                          placeholder="e.g. src/payments/, tests/"
                        />
                      </div>
                      <div className="space-y-1.5">
                        <Label>Run as</Label>
                        <Select
                          value={identityScope}
                          onValueChange={(value: "org" | "personal") =>
                            setIdentityScope(value)
                          }
                        >
                          <SelectTrigger aria-label="Run as">
                            <SelectValue />
                          </SelectTrigger>
                          <SelectContent>
                            <SelectItem value="org">Organization</SelectItem>
                            <SelectItem value="personal">Personal</SelectItem>
                          </SelectContent>
                        </Select>
                      </div>
                      <div className="space-y-1.5">
                        <Label htmlFor="advanced-model">Model</Label>
                        <AutomationModelSelect
                          id="advanced-model"
                          ariaLabel="Model"
                          value={model}
                          onValueChange={setModel}
                        />
                      </div>
                      {showReasoningSelector ? (
                        <div className="space-y-1.5">
                          <Label>Reasoning</Label>
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
                            <SelectTrigger aria-label="Reasoning">
                              <SelectValue placeholder="Default reasoning" />
                            </SelectTrigger>
                            <SelectContent>
                              <SelectItem value="__default__">
                                Default reasoning
                              </SelectItem>
                              {reasoningOptions.map((option) => (
                                <SelectItem
                                  key={option.value}
                                  value={option.value}
                                >
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
                          repositoryId={repoId}
                          value={selectedBaseBranch}
                          defaultBranch={selectedRepo?.default_branch}
                          onValueChange={(branch) =>
                            setBaseBranchByRepoId((prev) => ({
                              ...prev,
                              [repoId]: branch,
                            }))
                          }
                          label="Base branch"
                          disabled={!repoId}
                          buttonClassName="w-full justify-between"
                          contentClassName="w-[var(--radix-popover-trigger-width)]"
                        />
                      </div>
                      <div className="space-y-1.5">
                        <Label>Priority</Label>
                        <Select
                          value={String(priority)}
                          onValueChange={(v) => setPriority(parseInt(v, 10))}
                        >
                          <SelectTrigger aria-label="Priority">
                            <SelectValue />
                          </SelectTrigger>
                          <SelectContent>
                            <SelectItem value="0">Critical</SelectItem>
                            <SelectItem value="25">High</SelectItem>
                            <SelectItem value="50">Medium</SelectItem>
                            <SelectItem value="75">Low</SelectItem>
                          </SelectContent>
                        </Select>
                      </div>
                      <div className="space-y-2">
                        <div className="flex items-center justify-between gap-3">
                          <Label>Capabilities</Label>
                          <span className="truncate text-xs text-muted-foreground">
                            {capabilityOverride
                              ? capabilitySummary(
                                  capabilityCatalog,
                                  capabilityOverride,
                                )
                              : "Org defaults"}
                          </span>
                        </div>
                        <AutomationCapabilitiesEditor
                          catalog={capabilityCatalog}
                          grants={capabilityGrants}
                          onChange={setCapabilityOverride}
                        />
                      </div>
                      <div className="space-y-3 rounded-md border border-border p-3">
                        <div className="space-y-1">
                          <Label>Trigger filters</Label>
                          <p className="text-xs text-muted-foreground">
                            Comma-separated filters applied when GitHub sends
                            matching context.
                          </p>
                        </div>
                        <div className="grid gap-3 sm:grid-cols-2">
                          <div className="space-y-1.5">
                            <Label htmlFor="trigger-base-branches">
                              Target branches
                            </Label>
                            <Input
                              id="trigger-base-branches"
                              value={triggerBaseBranches}
                              onChange={(e) =>
                                setTriggerBaseBranches(e.target.value)
                              }
                              placeholder="main, release"
                            />
                          </div>
                          <div className="space-y-1.5">
                            <Label htmlFor="trigger-authors">Authors</Label>
                            <Input
                              id="trigger-authors"
                              value={triggerAuthors}
                              onChange={(e) =>
                                setTriggerAuthors(e.target.value)
                              }
                              placeholder="octocat, dependabot[bot]"
                            />
                          </div>
                          <div className="space-y-1.5">
                            <Label htmlFor="trigger-paths">Paths</Label>
                            <Input
                              id="trigger-paths"
                              value={triggerPaths}
                              onChange={(e) => setTriggerPaths(e.target.value)}
                              placeholder="src/, package.json"
                            />
                          </div>
                          <div className="space-y-1.5">
                            <Label htmlFor="trigger-feedback-types">
                              Feedback types
                            </Label>
                            <Input
                              id="trigger-feedback-types"
                              value={triggerFeedbackTypes}
                              onChange={(e) =>
                                setTriggerFeedbackTypes(e.target.value)
                              }
                              placeholder="Inline review comment"
                            />
                          </div>
                          <div className="space-y-1.5 sm:col-span-2">
                            <Label htmlFor="trigger-review-states">
                              Review states
                            </Label>
                            <Input
                              id="trigger-review-states"
                              value={triggerReviewStates}
                              onChange={(e) =>
                                setTriggerReviewStates(e.target.value)
                              }
                              placeholder="approved, changes_requested, commented"
                            />
                          </div>
                        </div>
                      </div>
                      <div className="space-y-1.5">
                        <Label htmlFor="pre-pr-review-loops">
                          Pre-PR review
                        </Label>
                        <div className="flex items-center gap-2">
                          <Button
                            type="button"
                            variant="outline"
                            size="icon"
                            aria-label="Decrease review passes"
                            onClick={() =>
                              setPrePRReviewLoops((value) =>
                                Math.max(0, value - 1),
                              )
                            }
                            disabled={!supportsNativeReviewLoop}
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
                                Number.isNaN(parsed)
                                  ? 0
                                  : Math.min(5, Math.max(0, parsed)),
                              );
                            }}
                            disabled={!supportsNativeReviewLoop}
                            className="w-20 text-center"
                          />
                          <Button
                            type="button"
                            variant="outline"
                            size="icon"
                            aria-label="Increase review passes"
                            onClick={() =>
                              setPrePRReviewLoops((value) =>
                                Math.min(5, value + 1),
                              )
                            }
                            disabled={!supportsNativeReviewLoop}
                          >
                            <Plus className="h-4 w-4" />
                          </Button>
                        </div>
                        <p className="text-xs text-muted-foreground">
                          {prePRReviewDescription}
                        </p>
                      </div>
                      <div className="flex justify-end gap-2 pt-2">
                        <SheetClose asChild>
                          <Button type="button" variant="outline">
                            Cancel
                          </Button>
                        </SheetClose>
                        <SheetClose asChild>
                          <Button type="button">Apply</Button>
                        </SheetClose>
                      </div>
                    </div>
                  </SheetContent>
                </Sheet>
            </>
          }
          submitArea={
            <div className="flex items-center gap-3">
              {createMutation.isError && (
                <p className="text-xs text-destructive">
                  Failed to create automation. Please try again.
                </p>
              )}
              <DisabledTooltip
                disabled={!canSubmit && !!submitDisabledReason}
                content={submitDisabledReason}
              >
                <Button
                  onClick={() => createMutation.mutate()}
                  disabled={
                    !canSubmit || createMutation.isPending || redirecting
                  }
                >
                  {createMutation.isPending || redirecting ? (
                    <>
                      <Loader2 className="mr-2 h-4 w-4 animate-spin" />
                      Creating...
                    </>
                  ) : (
                    "Create automation"
                  )}
                </Button>
              </DisabledTooltip>
            </div>
          }
        />
      </div>
    </PageContainer>
  );
}

function getCreateDisabledReason({
  name,
  goal,
  goalTooLong,
  hasRepository,
  pagerDutyTriggerValid,
  scheduleEnabled,
  hasEventTriggers,
}: {
  name: string;
  goal: string;
  goalTooLong: boolean;
  hasRepository: boolean;
  pagerDutyTriggerValid: boolean;
  scheduleEnabled: boolean;
  hasEventTriggers: boolean;
}): string | undefined {
  if (!name && !goal) {
    return "Add an automation name and goal to create this automation.";
  }
  if (!name) {
    return "Add an automation name before creating it.";
  }
  if (!goal) {
    return "Describe what the automation should do before creating it.";
  }
  if (goalTooLong) {
    return "Shorten the automation goal before creating it.";
  }
  if (!hasRepository) {
    return "Select a repository before creating the automation.";
  }
  if (!scheduleEnabled && !hasEventTriggers) {
    return "Select at least one trigger before creating the automation.";
  }
  if (!pagerDutyTriggerValid) {
    return "Add at least one PagerDuty service ID before creating the automation.";
  }
  return undefined;
}

function TemplatePicker({
  open,
  onOpenChange,
  onSelect,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onSelect: (template: AutomationTemplate) => void;
}) {
  const featured = useMemo(
    () =>
      automationTemplates.filter((template) =>
        featuredAutomationTemplateIDs.includes(template.id),
      ),
    [],
  );
  const remaining = useMemo(
    () =>
      automationTemplates.filter(
        (template) => !featuredAutomationTemplateIDs.includes(template.id),
      ),
    [],
  );

  return (
    <Popover open={open} onOpenChange={onOpenChange}>
      <PopoverTrigger asChild>
        <Button type="button" variant="outline" size="sm">
          <Sparkles className="mr-2 h-4 w-4" />
          Templates
          <ChevronDown className="ml-2 h-3.5 w-3.5" />
        </Button>
      </PopoverTrigger>
      <PopoverContent
        className="w-[min(36rem,calc(100vw-2rem))] p-0"
        align="start"
      >
        <Command>
          <CommandInput placeholder="Search templates..." />
          <CommandList className="max-h-[420px]">
            <CommandEmpty>No templates found.</CommandEmpty>
            <TemplateGroup
              heading="Featured"
              templates={featured}
              onSelect={onSelect}
            />
            <TemplateGroup
              heading="All templates"
              templates={remaining}
              onSelect={onSelect}
            />
          </CommandList>
        </Command>
      </PopoverContent>
    </Popover>
  );
}

function commaList(value: string): string[] {
  return value
    .split(",")
    .map((item) => item.trim())
    .filter(Boolean);
}

function statusList(
  value: string,
): Array<"triggered" | "acknowledged" | "resolved"> {
  const allowed = new Set(["triggered", "acknowledged", "resolved"]);
  return commaList(value)
    .map((item) => item.toLowerCase())
    .filter(
      (item): item is "triggered" | "acknowledged" | "resolved" =>
        allowed.has(item),
    );
}

function parseCooldownMinutes(value: string): number | undefined {
  const trimmed = value.trim();
  if (trimmed.length === 0) return undefined;
  const parsed = Number.parseInt(trimmed, 10);
  if (!Number.isFinite(parsed) || parsed <= 0) return undefined;
  return Math.min(parsed, 10080);
}

function parsePagerDutyCustomFields(value: string): Record<string, string[]> {
  return commaList(value).reduce<Record<string, string[]>>((fields, item) => {
    const separator = item.includes("=") ? "=" : item.includes(":") ? ":" : "";
    if (!separator) return fields;

    const [rawKey, ...rawValueParts] = item.split(separator);
    const key = rawKey.trim();
    const fieldValue = rawValueParts.join(separator).trim();
    if (!key || !fieldValue) return fields;

    fields[key] = [...(fields[key] ?? []), fieldValue];
    return fields;
  }, {});
}

function buildPagerDutyEventTriggers(
  enabled: boolean,
  eventTypes: PagerDutyEventType[],
  serviceIDInput: string,
  teamIDInput: string,
  statusInput: string,
  urgency: "high" | "low",
  priorityNameInput: string,
  incidentTypeInput: string,
  titleContainsInput: string,
  customFieldsInput: string,
  cooldownMinutesInput: string,
  repositoryID: string,
): AutomationEventTriggerInput[] {
  if (!enabled) return [];

  const serviceIDs = commaList(serviceIDInput);
  const normalizedEventTypes = eventTypes.filter((eventType, index) =>
    eventTypes.indexOf(eventType) === index,
  );
  if (serviceIDs.length === 0 || normalizedEventTypes.length === 0) {
    return [];
  }
  const teamIDs = commaList(teamIDInput);
  const statuses = statusList(statusInput);
  const priorityNames = commaList(priorityNameInput);
  const incidentTypes = commaList(incidentTypeInput);
  const titleContains = titleContainsInput.trim();
  const customFields = parsePagerDutyCustomFields(customFieldsInput);
  const cooldownMinutes = parseCooldownMinutes(cooldownMinutesInput);

  const filter: PagerDutyEventTriggerFilter = {
    service_ids: serviceIDs,
    urgencies: [urgency],
  };
  if (teamIDs.length > 0) filter.team_ids = teamIDs;
  if (statuses.length > 0) filter.statuses = statuses;
  if (priorityNames.length > 0) filter.priority_names = priorityNames;
  if (incidentTypes.length > 0) filter.incident_types = incidentTypes;
  if (titleContains.length > 0) filter.title_contains = titleContains;
  if (Object.keys(customFields).length > 0) filter.custom_fields = customFields;
  if (cooldownMinutes !== undefined) filter.cooldown_minutes = cooldownMinutes;

  return [
    {
      provider: "pagerduty",
      event_types: normalizedEventTypes,
      filter,
      repository_id: repositoryID || undefined,
      enabled: true,
    },
  ];
}

function TemplateGroup({
  heading,
  templates,
  onSelect,
}: {
  heading: string;
  templates: AutomationTemplate[];
  onSelect: (template: AutomationTemplate) => void;
}) {
  return (
    <CommandGroup heading={heading}>
      {templates.map((template) => {
        const Icon = template.icon;
        return (
          <CommandItem
            key={template.id}
            value={`${template.name} ${template.summary} ${template.tags.join(" ")}`}
            onSelect={() => onSelect(template)}
            className="items-start gap-3 py-3"
          >
            <span className="mt-0.5 rounded-md border border-border bg-muted/50 p-1.5">
              <Icon className="h-4 w-4" />
            </span>
            <span className="min-w-0 space-y-1">
              <span className="block text-sm font-medium">{template.name}</span>
              <span className="line-clamp-2 block text-xs text-muted-foreground">
                {template.summary}
              </span>
            </span>
          </CommandItem>
        );
      })}
    </CommandGroup>
  );
}
