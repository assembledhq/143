"use client";

import {
  useCallback,
  useEffect,
  useId,
  useMemo,
  useRef,
  useState,
  type ReactNode,
} from "react";
import dynamic from "next/dynamic";
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { ChevronDown, Play, Pause, Loader2, Minus, Plus } from "lucide-react";
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
  Popover,
  PopoverContent,
  PopoverTrigger,
} from "@/components/ui/popover";
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from "@/components/ui/sheet";
import { AutosaveIndicator } from "@/components/AutosaveIndicator";
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
import { AutomationScheduleEditor } from "@/components/automation-schedule-editor";
import { ApiError, api } from "@/lib/api";
import {
  automationScheduleTimezone,
  automationToScheduleDraft,
  formatAutomationSchedule,
  sameScheduleDraft,
  scheduleDraftToAPI,
  validateScheduleDraft,
  type ScheduleDraft,
} from "@/lib/automation-schedule";
import {
  removeAutomationFromListCaches,
  upsertAutomationInListCaches,
} from "@/lib/automation-list-cache";
import { queryKeys } from "@/lib/query-keys";
import { agentTypeForModel } from "@/lib/agents";
import {
  automationProductTriggerOptions,
  automationProductTriggersToGitHubEvents,
  githubEventsToAutomationProductTriggers,
  type AutomationProductTrigger,
} from "@/lib/automation-triggers";
import { automationGoalLengthState } from "@/lib/automation-validation";
import { useAuth } from "@/hooks/use-auth";
import { usePageTitle } from "@/hooks/use-page-title";
import { useAutosave, type UseAutosaveResult } from "@/hooks/useAutosave";
import { useAutosaveNumericField } from "@/hooks/useAutosaveNumericField";
import { useDebouncedTextField } from "@/hooks/useDebouncedTextField";
import type {
  AgentCapabilityDefinition,
  AgentCapabilityGrant,
  Automation,
  AutomationGitHubEventFilters,
  AutomationRun,
  ListResponse,
  Repository,
} from "@/lib/types";
import { cn, formatDateTime, formatTimeAgo } from "@/lib/utils";
import {
  getCodingAgentReasoningOptions,
  supportsReasoningEffort,
  toCodingAgentReasoningEffort,
  type CodingAgentReasoningEffort,
} from "@/lib/coding-agent-reasoning";
import { DecisionHistory } from "./decision-history";
import { browserTimezone } from "../schedule-time";
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

function commaList(value: string): string[] {
  return value
    .split(",")
    .map((item) => item.trim())
    .filter(Boolean);
}

// ---------------------------------------------------------------------------
// Autosave scope
// ---------------------------------------------------------------------------

/**
 * Every editable control on this page — the title, the goal, and each property
 * row — commits through one autosave scope keyed by the automation detail
 * query.
 *
 * `body` is what PATCH receives; `optimistic` is how the cached `Automation`
 * should look once it lands. The two are kept separate because a few API field
 * names differ from the model's (`model` vs `model_override`, `triggers` vs
 * `github_event_triggers`), and because `useAutosave` diffs the optimistic
 * result against the cache to skip saves that would change nothing.
 */
type AutomationPatch = {
  body: Record<string, unknown>;
  optimistic: Partial<Automation>;
  onError?: () => void;
};

// Module-level so every caller on this queryKey passes one identity —
// `useAutosave` throws in dev when two components sharing a scope disagree
// about how to merge.
const coalesceAutomationPatch = (
  queued: AutomationPatch,
  incoming: AutomationPatch,
): AutomationPatch => ({
  body: { ...queued.body, ...incoming.body },
  optimistic: { ...queued.optimistic, ...incoming.optimistic },
  onError:
    queued.onError || incoming.onError
      ? () => {
          queued.onError?.();
          incoming.onError?.();
        }
      : undefined,
});

const applyAutomationPatch = (
  previous: unknown,
  patch: AutomationPatch,
): unknown => {
  const response = previous as { data?: Automation } | undefined;
  if (!response?.data) return previous;
  return { ...response, data: { ...response.data, ...patch.optimistic } };
};

type AutomationAutosave = UseAutosaveResult<AutomationPatch>;

const GENERIC_SAVE_ERROR = "Couldn’t save automation. Your change was reverted.";

// Without a Save button every property fails on its own, and the API refuses
// these writes for reasons the user can actually act on — a model that needs a
// credential, a cron expression it won't accept, a personal identity scope on
// an automation with no creator. Collapsing all of that into one fixed string
// leaves no way to tell which row was refused or what to do about it.
const automationSaveError = (error: unknown): string => {
  if (error instanceof ApiError && error.message.trim()) {
    return `Couldn’t save automation: ${error.message}`;
  }
  return GENERIC_SAVE_ERROR;
};

function useAutomationAutosave(automationId: string): AutomationAutosave {
  const queryClient = useQueryClient();
  return useAutosave<AutomationPatch>({
    queryKey: queryKeys.automations.detail(automationId),
    debounceMs: 0,
    mutationFn: async (patch) => {
      const response = await api.automations.update(automationId, patch.body);
      upsertAutomationInListCaches(queryClient, response.data);
      return response;
    },
    applyOptimistic: applyAutomationPatch,
    coalesce: coalesceAutomationPatch,
    errorMessage: automationSaveError,
    onError: (_error, patch) => patch.onError?.(),
  });
}

// Capabilities live behind their own endpoint and cache entry, so they get
// their own scope. A later grant list always supersedes an earlier one.
const coalesceCapabilityGrants = (
  _queued: AgentCapabilityGrant[],
  incoming: AgentCapabilityGrant[],
): AgentCapabilityGrant[] => incoming;

const applyCapabilityGrants = (
  previous: unknown,
  grants: AgentCapabilityGrant[],
): unknown => {
  const response = previous as
    { data?: { capabilities?: AgentCapabilityGrant[] } } | undefined;
  if (!response?.data) return previous;
  return { ...response, data: { ...response.data, capabilities: grants } };
};

// ---------------------------------------------------------------------------
// Inline property rows
// ---------------------------------------------------------------------------

// A property reads as plain text until it is hovered or focused, at which point
// it reveals that it was the control all along — so there is no separate "edit
// mode" to enter and no second place to look for the value. Heights follow the
// app's `h-<mobile> sm:h-<desktop>` convention: full-size touch targets on
// small screens, a tight 28px rhythm in the desktop rail.
const inlineControlClass =
  "h-9 sm:h-7 w-full justify-between rounded-md border border-transparent bg-transparent px-1.5 text-xs font-normal shadow-none hover:border-border hover:bg-muted/40 focus-visible:border-border data-[state=open]:border-border";

// SelectTrigger sizes itself through `data-[size]` variants, which
// tailwind-merge scopes separately from a bare `h-*`, so the same heights have
// to be restated against those variants to win.
const inlineSelectTriggerClass = cn(
  inlineControlClass,
  "data-[size=default]:h-9 sm:data-[size=default]:h-7",
);

// `htmlFor` is required, not optional: a `<Label>` with nothing bound to it is
// a dead click target next to rows that do respond, and emits a `<label>`
// pointing at no control. Every row here has an addressable control, so the
// type makes forgetting one a compile error rather than a silent nit.
function PropertyRow({
  label,
  htmlFor,
  children,
}: {
  label: string;
  htmlFor: string;
  children: ReactNode;
}) {
  return (
    <div className="grid grid-cols-[6.5rem_minmax(0,1fr)] items-center gap-2">
      <Label
        htmlFor={htmlFor}
        className="text-xs font-medium text-muted-foreground"
      >
        {label}
      </Label>
      <div className="min-w-0">{children}</div>
    </div>
  );
}

/** Read-only counterpart to `PropertyRow`, on the same optical grid. */
function StaticPropertyRow({ label, value }: { label: string; value: string }) {
  return (
    <div className="grid grid-cols-[6.5rem_minmax(0,1fr)] items-center gap-2">
      <span className="text-xs font-medium text-muted-foreground">{label}</span>
      <span className="min-w-0 break-words px-1.5 text-xs text-foreground">
        {value}
      </span>
    </div>
  );
}

function triggerSummaryText(automation: Automation, schedule: string): string {
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

  return (
    [automation.schedule_type === "none" ? null : schedule, ...prTriggerLabels]
      .filter((value): value is string => Boolean(value))
      .join(", ") || "No triggers"
  );
}

function TriggersProperty({
  automation,
  schedule,
  canManage,
  autosave,
}: {
  automation: Automation;
  schedule: string;
  canManage: boolean;
  autosave: AutomationAutosave;
}) {
  const [open, setOpen] = useState(false);
  // Two rails can be mounted at once (see AutomationDetailRail), so the id has
  // to be per-instance for the label to bind to its own trigger.
  const uid = useId();
  const summary = triggerSummaryText(automation, schedule);

  if (!canManage) {
    return <StaticPropertyRow label="Triggers" value={summary} />;
  }

  return (
    <PropertyRow label="Triggers" htmlFor={`automation-triggers-${uid}`}>
      <Popover open={open} onOpenChange={setOpen}>
        <PopoverTrigger asChild>
          <Button
            type="button"
            id={`automation-triggers-${uid}`}
            variant="outline"
            size="sm"
            aria-label="Triggers"
            className={cn(inlineControlClass, "text-left")}
          >
            <span className="min-w-0 truncate">{summary}</span>
            <ChevronDown className="h-3 w-3 shrink-0 text-muted-foreground" />
          </Button>
        </PopoverTrigger>
        {/* Radix unmounts closed content, so every open re-seeds the schedule
            draft from the freshest automation instead of holding a long-lived
            local copy that the page's 10s poll could stale out. */}
        <PopoverContent
          align="start"
          className="w-[22rem] max-w-[calc(100vw-2rem)] p-3"
        >
          <TriggersEditor automation={automation} autosave={autosave} />
        </PopoverContent>
      </Popover>
    </PropertyRow>
  );
}

function TriggersEditor({
  automation,
  autosave,
}: {
  automation: Automation;
  autosave: AutomationAutosave;
}) {
  const queryClient = useQueryClient();
  const { save } = autosave;
  // Memoised per mount: stability keeps the TimezonePicker's `detected` prop
  // from changing identity mid-edit.
  const detectedTimezone = useMemo(() => browserTimezone(), []);
  const [scheduleDraft, setScheduleDraft] = useState<ScheduleDraft | null>(() =>
    automationToScheduleDraft(automation),
  );
  // Validity is tracked as "which draft got which verdict", not as a bare
  // boolean. The editor reports validity from an effect, one render after the
  // draft it describes — so a boolean flag is briefly true for a draft that has
  // already been replaced by an invalid one, and an autosave reading that flag
  // would persist the invalid draft. Holding the draft reference alongside the
  // verdict makes the pairing explicit.
  //
  // "rejected" is recorded as well as "valid" because the two failure modes
  // pull in opposite directions on unmount: a draft that simply hasn't settled
  // yet must still be committed (otherwise closing the popover drops the edit),
  // while one the API has already refused must not be (that write cannot
  // succeed, so sending it only buys a failed request and an error toast).
  // Anything else — still previewing — records nothing and leaves the previous
  // verdict alone.
  const [verdict, setVerdict] = useState<{
    draft: ScheduleDraft | null;
    status: "valid" | "rejected";
  } | null>(null);
  const [productTriggers, setProductTriggers] = useState<
    AutomationProductTrigger[]
  >(() =>
    githubEventsToAutomationProductTriggers(
      automation.github_event_triggers ?? [],
    ),
  );
  const hasTrigger = scheduleDraft !== null || productTriggers.length > 0;

  // The schedule is a compound control — an interval, a unit, a wall clock and
  // a timezone that only mean anything together — so it commits as one unit
  // once the editor reports a settled, server-previewed draft rather than on
  // each keystroke. The editor's own 300ms preview debounce is what paces this.
  const committedScheduleRef = useRef(scheduleDraft);
  const restoreTriggerDraft = useCallback((restored: Automation) => {
    // These controls keep a local draft while the popover is open, so restore
    // it explicitly on failure; otherwise the rejected values would remain
    // visible and the advanced committed snapshot would prevent a retry of the
    // same schedule.
    const restoredSchedule = automationToScheduleDraft(restored);
    committedScheduleRef.current = restoredSchedule;
    setScheduleDraft(restoredSchedule);
    setVerdict(null);
    setProductTriggers(
      githubEventsToAutomationProductTriggers(
        restored.github_event_triggers ?? [],
      ),
    );
  }, []);

  const commitSchedule = useCallback(
    (draft: ScheduleDraft | null, restoreOnError: boolean) => {
      committedScheduleRef.current = draft;
      const payload = scheduleDraftToAPI(draft, automation.timezone);
      let onError: (() => void) | undefined;
      if (restoreOnError) {
        // Read the pre-optimistic snapshot now, while it is still the truth to
        // restore to. Skipped entirely on the unmount path, where there is no
        // local draft left to put back.
        const savedAutomation =
          queryClient.getQueryData<{ data?: Automation }>(
            queryKeys.automations.detail(automation.id),
          )?.data ?? automation;
        onError = () => restoreTriggerDraft(savedAutomation);
      }
      save({
        body: payload,
        optimistic: payload as Partial<Automation>,
        onError,
      });
    },
    [automation, queryClient, restoreTriggerDraft, save],
  );

  useEffect(() => {
    if (verdict?.draft !== scheduleDraft || verdict.status !== "valid") return;
    // Dropping the last trigger would leave an automation that can never fire
    // again; refuse the commit and let the inline notice below explain why.
    // Once a trigger is restored this effect re-runs and the held-back schedule
    // change lands.
    if (!hasTrigger) return;
    if (sameScheduleDraft(scheduleDraft, committedScheduleRef.current)) return;
    commitSchedule(scheduleDraft, true);
  }, [commitSchedule, hasTrigger, scheduleDraft, verdict]);

  // One dep-less effect mirrors everything the unmount cleanup needs, matching
  // how the autosave hooks keep their refs fresh. Intentionally no dep array:
  // the cleanup below cannot read render state, so these must track every
  // commit.
  const latestScheduleRef = useRef({
    scheduleDraft,
    hasTrigger,
    verdict,
    commitSchedule,
  });
  useEffect(() => {
    latestScheduleRef.current = {
      scheduleDraft,
      hasTrigger,
      verdict,
      commitSchedule,
    };
  });

  // Closing the popover unmounts this editor, and the schedule has no blur to
  // fall back on: it commits only from the effect above, which waits on the
  // editor's 300ms debounce AND a server preview round-trip. Picking a run time
  // and clicking away would otherwise drop the change with no toast, no
  // indicator, and no visible difference.
  useEffect(() => {
    return () => {
      const {
        scheduleDraft: draft,
        hasTrigger: has,
        verdict: lastVerdict,
        commitSchedule: commit,
      } = latestScheduleRef.current;
      // Same guards as the settled path. `sameScheduleDraft` is also what keeps
      // StrictMode's simulated cleanup inert: the seed draft always matches the
      // committed snapshot until the user actually edits something.
      if (!has) return;
      if (sameScheduleDraft(draft, committedScheduleRef.current)) return;
      if (draft && validateScheduleDraft(draft)) return;
      // The API already refused this exact draft during preview, so sending it
      // buys a guaranteed-failed request and an error toast for a schedule the
      // user watched the editor reject and then walked away from.
      if (lastVerdict?.draft === draft && lastVerdict.status === "rejected") {
        return;
      }
      commit(draft, false);
    };
  }, []);

  const toggleProductTrigger = (
    trigger: AutomationProductTrigger,
    checked: boolean,
  ) => {
    const next = checked
      ? productTriggers.includes(trigger)
        ? productTriggers
        : [...productTriggers, trigger]
      : productTriggers.filter((item) => item !== trigger);
    setProductTriggers(next);
    if (next.length === 0 && scheduleDraft === null) return;
    const savedAutomation =
      queryClient.getQueryData<{ data?: Automation }>(
        queryKeys.automations.detail(automation.id),
      )?.data ?? automation;
    save({
      body: { triggers: next },
      optimistic: {
        github_event_triggers: automationProductTriggersToGitHubEvents(next),
      },
      onError: () => restoreTriggerDraft(savedAutomation),
    });
  };

  const saveFilter = useCallback(
    (key: keyof AutomationGitHubEventFilters, value: string) => {
      // Read the base from the cache rather than a render closure: two filters
      // edited inside one coalesce window would otherwise clobber each other,
      // and the cache is advanced synchronously by each optimistic apply.
      const cached = queryClient.getQueryData<{ data?: Automation }>(
        queryKeys.automations.detail(automation.id),
      );
      const latest = cached?.data?.github_event_filters ?? {};
      const merged: AutomationGitHubEventFilters = {
        ...latest,
        [key]: commaList(value),
      };
      save({
        body: { github_event_filters: merged },
        optimistic: { github_event_filters: merged },
      });
    },
    [automation.id, queryClient, save],
  );

  const filters = automation.github_event_filters ?? {};

  return (
    <div className="space-y-3">
      <AutomationScheduleEditor
        value={scheduleDraft}
        onChange={setScheduleDraft}
        detectedTimezone={detectedTimezone}
        // Deliberately an inline closure: the editor re-runs this on every
        // render, so `scheduleDraft` here is always the same draft the verdict
        // was computed from. Returning `prev` unchanged keeps that per-render
        // call from looping.
        onValidityChange={(valid, { serverRejected }) => {
          // Still previewing: record nothing rather than overwriting an older
          // verdict with "we don't know yet".
          const status = valid ? "valid" : serverRejected ? "rejected" : null;
          if (!status) return;
          setVerdict((prev) =>
            prev?.draft === scheduleDraft && prev.status === status
              ? prev
              : { draft: scheduleDraft, status },
          );
        }}
      />
      <div className="space-y-2">
        <span className="text-xs font-medium leading-none text-muted-foreground">
          Pull requests
        </span>
        <div className="space-y-1.5">
          {automationProductTriggerOptions.map((option) => (
            <Label
              key={option.value}
              className="flex min-h-7 cursor-pointer items-center gap-2 text-xs font-normal"
            >
              <Checkbox
                checked={productTriggers.includes(option.value)}
                onCheckedChange={(checked) =>
                  toggleProductTrigger(option.value, checked === true)
                }
                aria-label={option.label}
              />
              <span>{option.label}</span>
            </Label>
          ))}
        </div>
      </div>
      {!hasTrigger ? (
        <p className="text-xs text-destructive">
          Select at least one trigger. Nothing is saved until you do.
        </p>
      ) : null}
      <Collapsible className="rounded-md border border-border">
        <CollapsibleTrigger asChild>
          <Button
            type="button"
            variant="ghost"
            className="group h-8 w-full justify-between rounded-md px-2 text-left text-xs font-normal"
          >
            <span>Trigger filters</span>
            <ChevronDown className="h-3.5 w-3.5 text-muted-foreground transition-transform group-data-[state=open]:rotate-180" />
          </Button>
        </CollapsibleTrigger>
        <CollapsibleContent className="space-y-2.5 border-t border-border p-2.5">
          <p className="text-xs text-muted-foreground">
            Comma-separated filters applied when GitHub sends matching context.
          </p>
          <TriggerFilterField
            id="trigger-base-branches"
            label="Target branches"
            serverValue={(filters.base_branches ?? []).join(", ")}
            onCommit={(value) => saveFilter("base_branches", value)}
          />
          <TriggerFilterField
            id="trigger-authors"
            label="Authors"
            serverValue={(filters.authors ?? []).join(", ")}
            onCommit={(value) => saveFilter("authors", value)}
          />
          <TriggerFilterField
            id="trigger-paths"
            label="Paths"
            serverValue={(filters.paths ?? []).join(", ")}
            onCommit={(value) => saveFilter("paths", value)}
          />
          <TriggerFilterField
            id="trigger-feedback-types"
            label="Feedback types"
            serverValue={(filters.feedback_types ?? []).join(", ")}
            onCommit={(value) => saveFilter("feedback_types", value)}
          />
          <TriggerFilterField
            id="trigger-review-states"
            label="Review states"
            serverValue={(filters.review_states ?? []).join(", ")}
            onCommit={(value) => saveFilter("review_states", value)}
          />
        </CollapsibleContent>
      </Collapsible>
    </div>
  );
}

function TriggerFilterField({
  id,
  label,
  serverValue,
  onCommit,
}: {
  id: string;
  label: string;
  serverValue: string;
  onCommit: (value: string) => void;
}) {
  // Lives inside the Triggers popover, so it can be unmounted mid-edit.
  const field = useDebouncedTextField({
    serverValue,
    onCommit,
    flushOnUnmount: true,
  });

  return (
    <div className="space-y-1">
      <Label htmlFor={id} className="text-xs font-normal text-muted-foreground">
        {label}
      </Label>
      <Input
        id={id}
        value={field.value}
        onChange={(event) => field.onChange(event.target.value)}
        onBlur={field.onBlur}
        className="h-9 text-xs sm:h-7"
      />
    </div>
  );
}

function PrePRReviewProperty({
  automation,
  autosave,
  supported,
  canManage,
}: {
  automation: Automation;
  autosave: AutomationAutosave;
  supported: boolean;
  canManage: boolean;
}) {
  const uid = useId();
  const field = useAutosaveNumericField<AutomationPatch>({
    serverValue: supported ? (automation.pre_pr_review_loops ?? 0) : 0,
    autosave,
    // Lives inside the Advanced collapsible, so it can be unmounted mid-edit.
    flushOnUnmount: true,
    clamp: (raw) => Math.min(5, Math.max(0, raw)),
    toPatch: (loops) => ({
      body: { pre_pr_review_loops: loops },
      optimistic: { pre_pr_review_loops: loops },
    }),
  });
  const current = Number(field.value) || 0;
  const disabled = !canManage || !supported;

  let description = "Off for agents without review-loop support.";
  if (supported) {
    description =
      current === 0
        ? "Off"
        : "Runs the coding agent's review/fix loop before opening a PR.";
  }

  return (
    <div className="space-y-1.5">
      <Label
        htmlFor={`pre-pr-review-loops-${uid}`}
        className="text-xs font-medium text-muted-foreground"
      >
        Pre-PR review
      </Label>
      <div className="flex items-center gap-1.5">
        <Button
          type="button"
          variant="outline"
          size="icon-sm"
          aria-label="Decrease review passes"
          onClick={() => field.setValueAndSave(Math.max(0, current - 1))}
          disabled={disabled}
        >
          <Minus className="h-3.5 w-3.5" />
        </Button>
        <Input
          id={`pre-pr-review-loops-${uid}`}
          aria-label="Review passes"
          type="number"
          min={0}
          max={5}
          value={field.value}
          onChange={field.onChange}
          onBlur={field.onBlur}
          disabled={disabled}
          className="h-9 w-14 text-center text-xs sm:h-7"
        />
        <Button
          type="button"
          variant="outline"
          size="icon-sm"
          aria-label="Increase review passes"
          onClick={() => field.setValueAndSave(Math.min(5, current + 1))}
          disabled={disabled}
        >
          <Plus className="h-3.5 w-3.5" />
        </Button>
      </div>
      <p className="text-xs text-muted-foreground">{description}</p>
    </div>
  );
}

function CapabilitiesProperty({
  automation,
  canManage,
}: {
  automation: Automation;
  canManage: boolean;
}) {
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
  const autosave = useAutosave<AgentCapabilityGrant[]>({
    queryKey: ["automation-capabilities", automation.id],
    debounceMs: 0,
    mutationFn: (grants) =>
      api.automations.updateCapabilities(automation.id, grants),
    applyOptimistic: applyCapabilityGrants,
    coalesce: coalesceCapabilityGrants,
    errorMessage: "Couldn’t save capabilities. Your change was reverted.",
  });
  const grants = useMemo(
    () =>
      normalizeCapabilityGrants(
        capabilityCatalog,
        automationCapabilityResponse?.data?.capabilities ?? [],
      ),
    [automationCapabilityResponse?.data?.capabilities, capabilityCatalog],
  );

  return (
    <div className="space-y-1.5">
      <div className="flex items-center justify-between gap-2">
        <Label className="text-xs font-medium text-muted-foreground">
          Capabilities
        </Label>
        <AutosaveIndicator status={autosave.status} className="min-w-0" />
      </div>
      <p className="truncate text-xs text-muted-foreground">
        {capabilitySummary(capabilityCatalog, grants)}
      </p>
      <AutomationCapabilitiesEditor
        catalog={capabilityCatalog}
        grants={grants}
        onChange={(next) => autosave.save(next)}
        disabled={!canManage}
      />
    </div>
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
  const autosave = useAutomationAutosave(automation.id);
  // Both flush on unmount so a title or goal typed right before navigating away
  // isn't dropped inside the 400ms window — the autosave scope outlives this
  // component, so the dispatch still lands.
  const nameField = useDebouncedTextField({
    serverValue: automation.name,
    flushOnUnmount: true,
    onCommit: (rawName) => {
      const name = rawName.trim();
      autosave.save({ body: { name }, optimistic: { name } });
    },
    // A required field: an empty title is rejected (never saved) and reverts to
    // the last saved name on blur rather than being left silently blank.
    rejectValue: (name) => name.trim() === "",
  });
  const goalField = useDebouncedTextField({
    serverValue: automation.goal,
    flushOnUnmount: true,
    onCommit: (goal) => {
      if (automationGoalLengthState(goal).isTooLong) return;
      autosave.save({ body: { goal }, optimistic: { goal } });
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

  // The sentence renders a wall clock in the automation's zone using the
  // reader's locale, so the IANA zone has to travel with it here — a reader in
  // another zone would otherwise read "9:00 AM" as their own local time.
  const scheduleTimezone = automationScheduleTimezone(automation);
  const schedule = `${formatAutomationSchedule(automation)}${
    scheduleTimezone ? ` (${scheduleTimezone})` : ""
  }`;

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
  // No "Edit" button: every property is already editable where it is displayed,
  // so the only header affordance left is reaching the rail on small screens.
  const headerActions = (
    <Button
      variant="outline"
      size="sm"
      className="lg:hidden"
      onClick={() => setDetailsOpen(true)}
    >
      Properties
    </Button>
  );
  const repositoryName =
    repositoryResponse?.data.full_name ?? automation.repository_id ?? "-";

  return (
    <PageContainer size="wide">
      <div className="space-y-6">
        <MobileBackButton to="/automations" label="Back to automations" />
        <Sheet open={detailsOpen} onOpenChange={setDetailsOpen}>
          <SheetContent className="sm:max-w-md">
            <SheetHeader>
              <SheetTitle>Automation properties</SheetTitle>
              <SheetDescription>
                Triggers, identity, model, and recent run controls. Changes save
                as you make them.
              </SheetDescription>
            </SheetHeader>
            <div className="mt-6">
              <AutomationDetailRail
                automation={automation}
                schedule={schedule}
                repositoryName={repositoryName}
                canManage={canManage}
                runActions={runActions}
              />
            </div>
          </SheetContent>
        </Sheet>
        <PageHeader
          title={
            <span className="flex w-full min-w-0 items-center gap-2">
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
              key={automation.id}
              automation={automation}
              schedule={schedule}
              repositoryName={repositoryName}
              canManage={canManage}
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
  canManage,
  runActions,
}: {
  automation: Automation;
  schedule: string;
  repositoryName: string;
  canManage: boolean;
  runActions?: ReactNode;
}) {
  const autosave = useAutomationAutosave(automation.id);
  const { save } = autosave;
  // The desktop aside is `hidden lg:block` rather than unmounted, so opening the
  // mobile properties sheet puts two copies of this rail in the DOM at once.
  // Scope the control ids per instance to keep every label bound to its own
  // control instead of silently pointing at the other copy's.
  const uid = useId();

  const { data: repositoriesResponse } = useQuery<ListResponse<Repository>>({
    queryKey: queryKeys.repositories.all,
    queryFn: () => api.repositories.list(),
    enabled: canManage,
  });
  const repositories = useMemo(
    () => repositoriesResponse?.data ?? [],
    [repositoriesResponse?.data],
  );
  const repositoryId = automation.repository_id ?? "";
  const selectedRepository = repositories.find(
    (repository) => repository.id === repositoryId,
  );

  const { data: settingsResponse } = useQuery({
    queryKey: ["settings"],
    queryFn: () => api.settings.get(),
  });
  const settings = (settingsResponse?.data?.settings ?? {}) as {
    default_agent_type?: string;
  };
  const defaultAgentType = settings.default_agent_type ?? "codex";
  const model = automation.model_override;
  // Shared by the row that renders the current model and by the handler that
  // patches a new one — the two have to agree on which agent a model implies,
  // or the reasoning reset stops matching what the rail is showing.
  const effectiveAgentTypeFor = (candidate: string | undefined) =>
    candidate
      ? (agentTypeForModel(candidate) ??
        automation.agent_type ??
        defaultAgentType)
      : (automation.agent_type ?? defaultAgentType);
  const effectiveAgentType = effectiveAgentTypeFor(model);
  const supportsNativeReviewLoop = [
    "codex",
    "claude_code",
    "amp",
    "pi",
    "opencode",
  ].includes(effectiveAgentType);
  const showReasoningSelector = supportsReasoningEffort(effectiveAgentType);
  const reasoningOptions = getCodingAgentReasoningOptions(effectiveAgentType);

  const scopeField = useDebouncedTextField({
    serverValue: automation.scope ?? "",
    // The mobile properties sheet mounts a second copy of this rail and
    // unmounts it on close, so this field can go away mid-edit.
    flushOnUnmount: true,
    onCommit: (raw) => {
      const scope = raw.trim();
      save({ body: { scope }, optimistic: { scope } });
    },
  });

  return (
    <section className="rounded-lg border border-border bg-card p-4">
      <div className="space-y-4">
        <div className="flex items-center justify-between gap-2">
          <h2 className="text-sm font-semibold text-foreground">Properties</h2>
          <div className="flex items-center gap-2">
            <AutosaveIndicator status={autosave.status} className="min-w-0" />
            <Badge variant={automation.enabled ? "default" : "secondary"}>
              {automation.enabled ? "Active" : "Paused"}
            </Badge>
          </div>
        </div>
        {runActions}

        <div className="space-y-2">
          <StaticPropertyRow
            label="Next run"
            value={
              automation.next_run_at
                ? formatDateTime(automation.next_run_at)
                : "-"
            }
          />
          <StaticPropertyRow
            label="Last ran"
            value={
              automation.last_run_at
                ? formatDateTime(automation.last_run_at)
                : "-"
            }
          />

          <TriggersProperty
            automation={automation}
            schedule={schedule}
            canManage={canManage}
            autosave={autosave}
          />

          {canManage ? (
            <PropertyRow
              label="Repository"
              htmlFor={`automation-repository-${uid}`}
            >
              <Select
                value={repositoryId}
                onValueChange={(nextRepositoryId) => {
                  const nextRepository = repositories.find(
                    (repository) => repository.id === nextRepositoryId,
                  );
                  // Changing repositories invalidates the stored base branch,
                  // so both fields move as one patch rather than leaving a
                  // branch that does not exist on the new repo.
                  save({
                    body: {
                      repository_id: nextRepositoryId,
                      ...(nextRepository
                        ? { base_branch: nextRepository.default_branch }
                        : {}),
                    },
                    optimistic: {
                      repository_id: nextRepositoryId,
                      ...(nextRepository
                        ? { base_branch: nextRepository.default_branch }
                        : {}),
                    },
                  });
                }}
                disabled={repositories.length === 0}
              >
                <SelectTrigger
                  id={`automation-repository-${uid}`}
                  aria-label="Repository"
                  className={inlineSelectTriggerClass}
                >
                  <SelectValue placeholder={repositoryName} />
                </SelectTrigger>
                <SelectContent>
                  {/* The repository list loads separately from the automation,
                      so keep an entry for the current value until it arrives —
                      otherwise Radix has no item to render and the row reads as
                      blank on first paint. */}
                  {repositoryId && !selectedRepository ? (
                    <SelectItem value={repositoryId}>
                      {repositoryName}
                    </SelectItem>
                  ) : null}
                  {repositories.map((repository) => (
                    <SelectItem key={repository.id} value={repository.id}>
                      {repository.full_name}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </PropertyRow>
          ) : (
            <StaticPropertyRow label="Repository" value={repositoryName} />
          )}

          {canManage ? (
            <PropertyRow
              label="Runs as"
              htmlFor={`automation-identity-scope-${uid}`}
            >
              <Select
                value={automation.identity_scope ?? "org"}
                onValueChange={(value: "org" | "personal") =>
                  save({
                    body: { identity_scope: value },
                    optimistic: { identity_scope: value },
                  })
                }
              >
                <SelectTrigger
                  id={`automation-identity-scope-${uid}`}
                  aria-label="Run as"
                  className={inlineSelectTriggerClass}
                >
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="org">Organization</SelectItem>
                  <SelectItem value="personal">Personal</SelectItem>
                </SelectContent>
              </Select>
            </PropertyRow>
          ) : (
            <StaticPropertyRow
              label="Runs as"
              value={
                automation.identity_scope === "personal"
                  ? "Personal"
                  : "Organization"
              }
            />
          )}

          {canManage ? (
            <PropertyRow label="Model" htmlFor={`automation-model-${uid}`}>
              <AutomationModelSelect
                id={`automation-model-${uid}`}
                ariaLabel="Model"
                value={model}
                triggerClassName={inlineSelectTriggerClass}
                onValueChange={(value) => {
                  // The API re-validates the STORED reasoning override against
                  // the model's agent, so a lone `model` patch is rejected
                  // outright when the new agent can't accept it. The old batch
                  // save cleared it in the same request; a per-field patch has
                  // to carry that reset too, or the switch fails and the row
                  // the user would need to clear is the one that disappears.
                  const nextAgentType = effectiveAgentTypeFor(value);
                  const clearsReasoning =
                    Boolean(automation.reasoning_effort) &&
                    !supportsReasoningEffort(nextAgentType);
                  save({
                    body: {
                      model: value ?? "",
                      ...(clearsReasoning ? { reasoning_effort: "" } : {}),
                    },
                    optimistic: {
                      model_override: value ?? "",
                      ...(clearsReasoning
                        ? { reasoning_effort: undefined }
                        : {}),
                    },
                  });
                }}
              />
            </PropertyRow>
          ) : (
            <StaticPropertyRow
              label="Model"
              value={model || automation.agent_type || "Auto"}
            />
          )}

          {canManage && showReasoningSelector ? (
            <PropertyRow
              label="Reasoning"
              htmlFor={`automation-reasoning-${uid}`}
            >
              <Select
                value={automation.reasoning_effort || "__default__"}
                onValueChange={(value) => {
                  const reasoning_effort: CodingAgentReasoningEffort =
                    value === "__default__"
                      ? ""
                      : toCodingAgentReasoningEffort(value);
                  save({
                    // The API clears the override with "", but the model types
                    // "no override" as absent — so the cache gets `undefined`.
                    body: { reasoning_effort },
                    optimistic: {
                      reasoning_effort: reasoning_effort || undefined,
                    },
                  });
                }}
              >
                <SelectTrigger
                  id={`automation-reasoning-${uid}`}
                  aria-label="Reasoning"
                  className={inlineSelectTriggerClass}
                >
                  <SelectValue placeholder="Default" />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="__default__">Default</SelectItem>
                  {reasoningOptions.map((option) => (
                    <SelectItem key={option.value} value={option.value}>
                      {option.label}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </PropertyRow>
          ) : showReasoningSelector ? (
            <StaticPropertyRow
              label="Reasoning"
              value={automation.reasoning_effort || "Default"}
            />
          ) : null}

          {canManage ? (
            <PropertyRow
              label="Base branch"
              htmlFor={`automation-base-branch-${uid}`}
            >
              <BranchPicker
                id={`automation-base-branch-${uid}`}
                repositoryId={repositoryId}
                value={automation.base_branch}
                defaultBranch={
                  selectedRepository?.default_branch ?? automation.base_branch
                }
                onValueChange={(base_branch) =>
                  save({
                    body: { base_branch },
                    optimistic: { base_branch },
                  })
                }
                label="Base branch"
                buttonClassName={inlineControlClass}
                contentClassName="w-[var(--radix-popover-trigger-width)]"
              />
            </PropertyRow>
          ) : (
            <StaticPropertyRow
              label="Base branch"
              value={automation.base_branch || "-"}
            />
          )}

          {canManage ? (
            <PropertyRow
              label="After success"
              htmlFor={`automation-publish-policy-${uid}`}
            >
              <Select
                value={automation.publish_policy ?? "pull_request"}
                onValueChange={(value) => {
                  if (value !== "pull_request" && value !== "none") return;
                  save({
                    body: { publish_policy: value },
                    optimistic: { publish_policy: value },
                  });
                }}
              >
                <SelectTrigger
                  id={`automation-publish-policy-${uid}`}
                  aria-label="After a successful run"
                  className={inlineSelectTriggerClass}
                >
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="pull_request">Open a PR</SelectItem>
                  <SelectItem value="none">Do not publish</SelectItem>
                </SelectContent>
              </Select>
            </PropertyRow>
          ) : (
            <StaticPropertyRow
              label="After success"
              value={
                automation.publish_policy === "none"
                  ? "Do not publish"
                  : "Open a pull request"
              }
            />
          )}

          <StaticPropertyRow
            label="Priority"
            value={priorityLabel(automation.priority)}
          />

          {canManage ? (
            <PropertyRow label="Scope" htmlFor={`automation-scope-${uid}`}>
              <Input
                id={`automation-scope-${uid}`}
                aria-label="Scope"
                placeholder="Optional"
                value={scopeField.value}
                onChange={(event) => scopeField.onChange(event.target.value)}
                onBlur={scopeField.onBlur}
                className={inlineControlClass}
              />
            </PropertyRow>
          ) : (
            <StaticPropertyRow label="Scope" value={automation.scope || "-"} />
          )}
        </div>

        <Collapsible className="rounded-md border border-border">
          <CollapsibleTrigger asChild>
            <Button
              type="button"
              variant="ghost"
              className="group h-8 w-full justify-between rounded-md px-2 text-left text-xs font-normal"
            >
              <span>Advanced</span>
              <ChevronDown className="h-3.5 w-3.5 text-muted-foreground transition-transform group-data-[state=open]:rotate-180" />
            </Button>
          </CollapsibleTrigger>
          <CollapsibleContent className="space-y-3 border-t border-border p-2.5">
            <PrePRReviewProperty
              automation={automation}
              autosave={autosave}
              supported={supportsNativeReviewLoop}
              canManage={canManage}
            />
            <CapabilitiesProperty
              automation={automation}
              canManage={canManage}
            />
          </CollapsibleContent>
        </Collapsible>
      </div>
    </section>
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
