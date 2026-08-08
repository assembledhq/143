"use client";

import Link from "next/link";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import type { ClipboardEvent, ComponentProps, KeyboardEvent, ReactNode } from "react";
import { useInfiniteQuery, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { createParser, parseAsString, parseAsStringLiteral, useQueryState } from "nuqs";
import {
  AlertTriangle,
  ChartNoAxesColumnIncreasing,
  ChevronDown,
  ChevronRight,
  CircleHelp,
  ClipboardCheck,
  FileSearch,
  Github,
  MessageSquareText,
  Plus,
  PowerOff,
  RefreshCw,
  Settings2,
  SquareArrowOutUpRight,
  Trash2,
  Users,
} from "lucide-react";
import { EmptyState } from "@/components/empty-state";
import { ListPage } from "@/components/list-page";
import { PageTabContent } from "@/components/page-tab-content";
import { ResourceRow } from "@/components/resource-row";
import { ResponsiveResourceList, type ResponsiveResourceListColumn } from "@/components/responsive-resource-list";
import { SectionGroup } from "@/components/section-group";
import { StatusLabel, type StatusTone } from "@/components/status-label";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { ExternalLink } from "@/components/ui/external-link";
import { DisabledTooltip } from "@/components/ui/disabled-tooltip";
import { ErrorNotice } from "@/components/ui/error-notice";
import { Badge } from "@/components/ui/badge";
import { Select, SelectContent, SelectGroup, SelectItem, SelectLabel, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "@/components/ui/collapsible";
import { Checkbox } from "@/components/ui/checkbox";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Switch } from "@/components/ui/switch";
import { RadioGroup, RadioGroupItem } from "@/components/ui/radio-group";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";
import { Label } from "@/components/ui/label";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { DurationInput } from "@/components/duration-input";
import { ModelOptionGroups } from "@/components/model-option-groups";
import { ApiError, api } from "@/lib/api";
import { notify as toast } from "@/lib/notify";
import { queryKeys } from "@/lib/query-keys";
import { getActiveOrgId } from "@/lib/active-org";
import { ALL_CODE_REVIEW_REASONS, CODE_REVIEW_REASON_CODES, codeReviewReasonDescription, type CodeReviewReasonCode } from "@/lib/code-review-reasons";
import { buildCodeReviewStreamURL, SSE_EVENT } from "@/lib/sse";
import { useResourceSSE } from "@/lib/use-resource-sse";
import { pollMs } from "@/lib/poll-intervals";
import { cn } from "@/lib/utils";
import { useAutosave, type UseAutosaveResult } from "@/hooks/useAutosave";
import { useAutosaveNumericField } from "@/hooks/useAutosaveNumericField";
import { useDebouncedTextField } from "@/hooks/useDebouncedTextField";
import { useOpenCodeAvailability, type OpenCodeModelAvailability } from "@/hooks/use-opencode-models";
import { useAuth } from "@/hooks/use-auth";
import { DEFAULT_TIME_RANGE, parseTimeRange, timeRangeBounds, timeRangeRefreshDelayMs, type TimeRangeFilter } from "@/lib/time-range";
import { AutosaveIndicator } from "@/components/AutosaveIndicator";
import { AuditLogTrigger } from "@/components/audit/audit-log-trigger";
import { CodeReviewAnalyticsReport } from "@/components/code-review-analytics";
import { GitHubReviewerConnectionSheet } from "@/components/code-review/github-reviewer-connection-sheet";
import { SortableTableHeader } from "@/components/sortable-table-header";
import {
  ALL_OUTCOMES,
  ALL_REPOSITORIES,
  ALL_RISKS,
  AUTOMATICALLY_APPROVED,
  COMPLETED_NOT_APPROVED,
  CodeReviewFilters,
  CodeReviewSummaryCards,
  type CodeReviewFilterValues,
} from "@/components/code-review-overview";
import { applyCodeReviewPolicyOptimistic, coalesceCodeReviewPolicy } from "@/lib/code-review-autosave";
import { getCodingAgentReasoningOptions } from "@/lib/coding-agent-reasoning";
import { AGENTS_BY_KEY, availableAgentModelGroups, modelOptionLabel, pmUsableResolvedCredentials, type AgentModelGroup } from "@/lib/agents";
import type {
  CodingCredentialSummary,
  CodeReviewAnalytics,
  CodeReviewApprovalMode,
  CodeReviewActivityStatus,
  CodeReviewDecision,
  CodeReviewDispute,
  CodeReviewDescriptionApplicabilityKind,
  CodeReviewEvidence,
  CodeReviewGitHubTriggerResponse,
  CodeReviewListItem,
  CodeReviewListOutcome,
  CodeReviewPolicyConfig,
  CodeReviewPolicyEditSource,
  CodeReviewPolicyAnalyticsEvent,
  CodeReviewPromptExampleOption,
  CodeReviewAutomatedApprovalExampleOption,
  CodeReviewResolvedPolicy,
  CodeReviewSessionStatus,
  ListResponse,
  OrgSettings,
  SingleResponse,
} from "@/lib/types";

const CODE_REVIEW_TAB_VALUES = ["reviews", "analytics", "disputes", "policy"] as const;
type CodeReviewTab = (typeof CODE_REVIEW_TAB_VALUES)[number];
const OUTCOME_FILTER_VALUES = [ALL_OUTCOMES, AUTOMATICALLY_APPROVED, COMPLETED_NOT_APPROVED, "needs_human_review", "comment_only", "blocked"] as const;
type OutcomeFilter = (typeof OUTCOME_FILTER_VALUES)[number];
const RISK_FILTER_VALUES = [ALL_RISKS, "acceptable", "needs_review"] as const;
const STATUS_FILTER_VALUES = ["current", "completed", "in_progress", "failed", "cancelled", "superseded", "all"] as const;
type StatusFilter = (typeof STATUS_FILTER_VALUES)[number];
const DEFAULT_STATUS_FILTER = "current" satisfies StatusFilter;
const REVIEW_SORT_VALUES = ["pull_request", "outcome", "run_status", "completed"] as const;
type ReviewSort = (typeof REVIEW_SORT_VALUES)[number];
// "reviews" now orders the PR count: the column became unique PRs, but the
// parameter keeps its name so shared analytics links stay valid.
const AUTHOR_SORT_VALUES = ["author", "reviews", "approved", "not_approved", "approval_rate", "first_round", "median_rounds", "median_additions", "median_deletions"] as const;
type AuthorSort = (typeof AUTHOR_SORT_VALUES)[number];
const STATUS_FILTER_PARSER = createParser<StatusFilter>({
  parse: (value) => {
    if ((STATUS_FILTER_VALUES as readonly string[]).includes(value)) return value as StatusFilter;
    if (value === "queued" || value === "running") return "in_progress";
    if (value === "stale") return "superseded";
    return null;
  },
  serialize: (value) => value,
}).withDefault(DEFAULT_STATUS_FILTER);
const TIME_RANGE_FILTER_PARSER = createParser<TimeRangeFilter>({
  parse: parseTimeRange,
  serialize: (value) => value,
}).withDefault(DEFAULT_TIME_RANGE);
// Coalesce a burst of SSE lifecycle events into a single list refetch.
const CODE_REVIEW_INVALIDATE_COALESCE_MS = 300;
const CODE_REVIEW_PAGE_SIZE = 50;
const CODE_REVIEW_SEARCH_DEBOUNCE_MS = 300;
const CODE_REVIEW_TIME_WINDOW_REFRESH_MS = 60_000;
const MAX_BROWSER_TIMEOUT_MS = 2_147_000_000;
const MAX_REVIEWER_MODELS = 3;
const CODE_REVIEW_REASONING_OPTIONS = [
  { value: "low", label: "Low" },
  { value: "medium", label: "Medium" },
  { value: "high", label: "High" },
  { value: "xhigh", label: "Extra high" },
  { value: "max", label: "Max" },
] as const;
const CODE_REVIEW_PROMPT_MAX_LENGTH = 8000;
// The character count only becomes useful near the limit, so it stays hidden
// until a prompt is long enough for the ceiling to matter.
const CODE_REVIEW_PROMPT_COUNT_VISIBLE_AT = Math.round(CODE_REVIEW_PROMPT_MAX_LENGTH * 0.75);
// Policy textareas create a new version on every commit. Give authors enough
// time to pause while composing without turning ordinary typing into a stream
// of short-lived versions; leaving the field still flushes immediately.
const CODE_REVIEW_TEXTAREA_DEBOUNCE_MS = 5_000;
const codeReviewPromptValuesEqual = (left: string, right: string) => left.trim() === right.trim();
const DEFAULT_AUTOMATED_APPROVAL_POLICY = `Automatically approve routine changes when:
- the intent is clear and the change has a small, understandable scope
- there are no blocking findings
- the implementation follows established repository patterns
- the test coverage visible in the code is appropriate for the change

Require human review when:
- the change affects authentication, billing, permissions, infrastructure, or production data
- the change introduces a new architectural pattern or crosses unclear ownership boundaries
- reviewers disagree or the risk cannot be evaluated confidently
- the intended behavior cannot be determined from the pull request and repository context

Evaluate the pull request independently based on the code itself. Disregard GitHub checks, CI results, build statuses, and other external validation signals, whether passing, failing, or pending; they must not count for or against approval. Also disregard existing human review comments, review decisions, and review threads, whether open or resolved. Unresolved human review threads must not count against approval.`;
const APPLICABILITY_KIND_LABELS: Record<CodeReviewDescriptionApplicabilityKind, string> = {
  all: "All PRs",
  nontrivial: "Nontrivial",
  paths: "Paths",
};
const DEFAULT_NONTRIVIAL_MIN_FILES = 2;
const DEFAULT_NONTRIVIAL_MIN_LINES = 31;
type DescriptionRequirement = CodeReviewPolicyConfig["description_policy"]["requirements"][number];
type DescriptionApplicability = NonNullable<DescriptionRequirement["applies_when"]>;
const QUALITY_GATE_DESCRIPTIONS = {
  requirePassingChecks:
    "Blocks approval until the PR's required GitHub checks are passing. When off, checks do not independently block approval; reviews can still leave comments either way.",
  excludeSensitivePaths: "Treats changes matching sensitive paths as blocking approval. When off, sensitive-path matches do not independently require human review.",
  requireUpToDate: "Requires the PR branch to be current with its base branch before approval. When off, branch freshness does not independently block approval.",
  disagreementBlocks:
    "Blocks approval when reviewer agents disagree. When off, disagreement is still visible but does not independently veto approval unless another safeguard does.",
  allowForks: "Allows approval decisions for PRs opened from forks. The safer default is off, which keeps forked PRs comment-only.",
  stopAfterDeterministicFailure:
    "Skips reviewer agents when a commit already fails a stable size, path, fork, or author rule. The rolling comment publishes those blockers immediately; leave this off to continue the substantive review and collect findings.",
} as const;
const NUMBER_POLICY_DESCRIPTIONS: Record<string, string> = {
  "Files changed": "Maximum number of changed files eligible for automatic approval. Reviews still leave comments above this deterministic limit.",
  "Lines changed": "Maximum total changed lines eligible for automatic approval. Reviews still leave comments above this deterministic limit.",
  "Inline comments": "Maximum inline findings posted to GitHub. Extra findings remain in review evidence; this limit does not make a pull request eligible for approval.",
  "Reviewer quorum": "Minimum configured reviewer agents that must return usable results before automatic approval is eligible. It cannot exceed the reviewer count.",
  "Files changed at least": "Minimum changed-file count that makes this structured PR-description check apply. The default remains in effect until changed.",
  "Lines changed at least": "Minimum changed-line count that makes this structured PR-description check apply. The default remains in effect until changed.",
};

function formatDate(value?: string): string {
  if (!value) return "-";
  return new Intl.DateTimeFormat(undefined, {
    month: "short",
    day: "numeric",
    hour: "numeric",
    minute: "2-digit",
  }).format(new Date(value));
}

function trackCodeReviewPolicyEvent(event: CodeReviewPolicyAnalyticsEvent): void {
  void api.codeReviews.policyEvent(event).catch((error) => console.error("Failed to record code review policy event", error));
}

function promptCharacterBucket(value: string): string {
  const length = [...value].length;
  if (length === 0) return "0";
  if (length <= 250) return "1-250";
  if (length <= 1000) return "251-1000";
  if (length <= 4000) return "1001-4000";
  return "4001-8000";
}

function wasAutomaticallyApproved(review: CodeReviewListItem): boolean {
  return review.status === "completed" && review.decision === "approved" && Boolean(review.github_review_id);
}

function isSupersededReview(review: CodeReviewListItem): boolean {
  return review.stale || review.status === "stale" || Boolean(review.superseded_by_session_id);
}

function decisionLabel(review: CodeReviewListItem): string {
  if (isSupersededReview(review)) return "No outcome";
  if (wasAutomaticallyApproved(review)) return "Approved";
  if (review.decision === "approved") return "Approval not posted";
  if (review.decision === "needs_human_review") return "Review needed";
  if (review.decision === "blocked") return "Blocked";
  if (review.decision === "comment_only") return "Comment only";
  return "Pending";
}

function statusLabel(status: string): string {
  return status
    .split("_")
    .map((part) => (part ? part.charAt(0).toUpperCase() + part.slice(1) : part))
    .join(" ");
}

function reviewDecisionTone(review: CodeReviewListItem): StatusTone {
  if (isSupersededReview(review)) return "neutral";
  if (wasAutomaticallyApproved(review)) return "success";
  if (review.decision === "blocked") return "destructive";
  if (review.decision === "needs_human_review") return "warning";
  return "neutral";
}

function reviewStatusTone(status: string): StatusTone {
  if (status === "completed") return "success";
  if (status === "failed") return "destructive";
  if (status === "running" || status === "queued") return "primary";
  return "neutral";
}

// Path-based risk reasons are recorded once per changed path, so a broad PR can
// produce a very long list. Show a readable slice and summarize the remainder.
const WHY_NOT_APPROVED_REASON_LIMIT = 10;

function whyNotApprovedReasons(review: CodeReviewListItem): string[] {
  if (isSupersededReview(review) || review.status !== "completed" || wasAutomaticallyApproved(review)) return [];

  const structuredReasons = (review.risk_reason_details ?? []).map(codeReviewReasonDescription);
  if (structuredReasons.length > 0) return structuredReasons;

  if (review.decision === "comment_only") return ["Configured for comment-only reviews"];
  if (review.decision === "approved") return ["Approval could not be posted"];
  if (review.decision === "needs_human_review") return ["Human review was required"];
  if (review.decision === "blocked") return ["Approval was blocked"];
  return ["No approval decision was recorded"];
}

function WhyNotApproved({ reasons, compact = false }: { reasons: string[]; compact?: boolean }) {
  if (reasons.length === 0) return <span className="text-muted-foreground">—</span>;

  return (
    <div className="max-w-[18rem] space-y-1">
      <p className={cn("line-clamp-2 leading-5 text-foreground", compact ? "text-xs" : "text-sm")} title={reasons[0]}>
        {reasons[0]}
      </p>
      {reasons.length > 1 ? (
        <Popover>
          <PopoverTrigger asChild>
            <Button variant="link" size="sm" className="h-auto p-0 text-xs font-normal text-muted-foreground">
              +{reasons.length - 1} more
            </Button>
          </PopoverTrigger>
          {/* Path-based reasons are emitted per changed file, so this list can run
              to hundreds of entries; cap it and let the rest scroll. */}
          <PopoverContent className="max-h-[60vh] w-80 overflow-y-auto p-4">
            <div className="space-y-2">
              <p className="text-sm font-medium text-foreground">Why this review was not approved</p>
              <ul className="list-disc space-y-2 pl-4 text-sm leading-5 text-muted-foreground">
                {reasons.slice(0, WHY_NOT_APPROVED_REASON_LIMIT).map((reason, index) => (
                  <li key={`${reason}-${index}`}>{reason}</li>
                ))}
              </ul>
              {reasons.length > WHY_NOT_APPROVED_REASON_LIMIT ? (
                <p className="text-xs text-muted-foreground">
                  and {reasons.length - WHY_NOT_APPROVED_REASON_LIMIT} more
                </p>
              ) : null}
            </div>
          </PopoverContent>
        </Popover>
      ) : null}
    </div>
  );
}

function MobileWhyNotApproved({ review }: { review: CodeReviewListItem }) {
  const reasons = whyNotApprovedReasons(review);
  if (reasons.length === 0) return null;

  return (
    <div className="space-y-1 text-xs">
      <span className="text-muted-foreground">Why not approved</span>
      <WhyNotApproved reasons={reasons} compact />
    </div>
  );
}

function ReviewTitle({ review }: { review: CodeReviewListItem }) {
  const title = `#${review.github_pr_number} ${review.pull_request_title}`;

  if (!review.github_review_url) return title;

  return (
    <ExternalLink href={review.github_review_url} title="Open final review">
      {title}
    </ExternalLink>
  );
}

function EvidenceButton({ selected, onToggleEvidence }: { selected: boolean; onToggleEvidence: () => void }) {
  return (
    <Button variant={selected ? "secondary" : "ghost"} size="sm" className="h-7 px-2 text-muted-foreground hover:text-foreground" onClick={onToggleEvidence}>
      <FileSearch className="h-4 w-4" />
      Evidence
    </Button>
  );
}

function reviewCanBeRetried(review: CodeReviewListItem): boolean {
  return review.retry_eligible;
}

function ReviewActions({
  review,
  canRetry,
  isRetrying,
  evidenceSelected,
  onRetry,
  onToggleEvidence,
  className,
}: {
  review: CodeReviewListItem;
  canRetry: boolean;
  isRetrying: boolean;
  evidenceSelected: boolean;
  onRetry: () => void;
  onToggleEvidence: () => void;
  className?: string;
}) {
  return (
    <div className={cn("flex w-full items-center gap-1 md:w-auto md:justify-end", className)}>
      <EvidenceButton selected={evidenceSelected} onToggleEvidence={onToggleEvidence} />
      {canRetry && reviewCanBeRetried(review) ? (
        <Button className="min-h-11 flex-1 justify-center md:min-h-0 md:flex-none" variant="outline" size="sm" disabled={isRetrying} onClick={onRetry}>
          <RefreshCw className={isRetrying ? "animate-spin" : undefined} />
          {isRetrying ? "Retrying…" : "Retry review"}
        </Button>
      ) : null}
      <Button className="h-7 min-h-11 flex-1 justify-center px-2 text-muted-foreground hover:text-foreground md:min-h-0 md:flex-none" variant="ghost" size="sm" asChild>
        <Link href={`/sessions/${review.session_id}`}>
          <MessageSquareText className="h-3.5 w-3.5" />
          Session
        </Link>
      </Button>
    </div>
  );
}

const CODE_REVIEW_PHASE_LABELS: Record<NonNullable<CodeReviewListItem["phase"]>, string> = {
  syncing_github: "Syncing GitHub",
  waiting_for_github: "Waiting for GitHub",
  reviewing: "Reviewing",
  synthesizing: "Synthesizing",
  publishing: "Publishing",
};

function reviewStatusLabel(review: CodeReviewListItem): string {
  if (isSupersededReview(review)) return "Superseded";
  if (review.status === "completed") return "Completed";
  if (review.status === "failed") return "Failed";
  if ((review.status === "running" || review.status === "queued") && review.phase) return CODE_REVIEW_PHASE_LABELS[review.phase];
  if (review.status === "running") return "Running";
  if (review.status === "queued") return "Queued";
  if (review.status === "cancelled") return "Cancelled";
  return review.status;
}

function reviewStatusMessage(review: CodeReviewListItem): string | null {
  const message = review.status_message?.trim();
  return message || null;
}

function retryCountdown(retryAt: string | undefined, nowMs: number): string | null {
  if (!retryAt) return null;
  const retryAtMs = Date.parse(retryAt);
  if (!Number.isFinite(retryAtMs)) return null;
  const seconds = Math.ceil((retryAtMs - nowMs) / 1000);
  if (seconds <= 0) return "Retrying now…";
  if (seconds < 60) return `Retrying in ${seconds}s`;
  const minutes = Math.floor(seconds / 60);
  const remainingSeconds = seconds % 60;
  if (minutes < 60) return `Retrying in ${minutes}m ${remainingSeconds}s`;
  const hours = Math.floor(minutes / 60);
  return `Retrying in ${hours}h ${minutes % 60}m`;
}

function ReviewOperationalStatus({ review, nowMs }: { review: CodeReviewListItem; nowMs: number }) {
  const waitingForGitHub = review.phase === "waiting_for_github";
  const active = (review.status === "running" || review.status === "queued") && !waitingForGitHub;
  const message = reviewStatusMessage(review);
  const countdown = retryCountdown(review.retry_at, nowMs);
  return (
    <div className="max-w-sm space-y-1">
      <StatusLabel
        label={reviewStatusLabel(review)}
        tone={waitingForGitHub ? "warning" : reviewStatusTone(isSupersededReview(review) ? "superseded" : review.status)}
        activity={active ? (review.status === "queued" ? "indeterminate" : "breathing") : "none"}
        stateKey={`${review.status}:${review.phase ?? ""}`}
      />
      {message ? <p className="text-xs leading-5 text-muted-foreground">{message}</p> : null}
      {countdown ? <p className="text-xs font-medium text-warning">{countdown}</p> : null}
    </div>
  );
}

function clonePolicy(config: CodeReviewPolicyConfig): CodeReviewPolicyConfig {
  return JSON.parse(JSON.stringify(config)) as CodeReviewPolicyConfig;
}

function apiErrorMessage(error: unknown): string | null {
  if (!error) return null;
  if (error instanceof ApiError) return error.message;
  if (error instanceof Error) return error.message;
  return "Request failed";
}

function selectionValue(agent: string, model: string): string {
  return `${agent}::${model}`;
}

function parseSelectionValue(value: string): { agent: string; model: string } {
  const [agent, ...modelParts] = value.split("::");
  return { agent, model: modelParts.join("::") };
}

function defaultModelForAgent(agent: string, modelGroups: AgentModelGroup[]): string {
  return modelGroups.find((group) => group.key === agent)?.models[0] ?? AGENTS_BY_KEY[agent]?.models[0] ?? "";
}

function modelBelongsToAgent(agent: string, model: string): boolean {
  return AGENTS_BY_KEY[agent]?.models.includes(model) ?? false;
}

function ensureReviewerModels(config: CodeReviewPolicyConfig, modelGroups: AgentModelGroup[]): string[] {
  return config.agent_roster.reviewers.map((agent, index) => {
    const configured = config.agent_roster.reviewer_models?.[index] ?? "";
    if (configured && modelBelongsToAgent(agent, configured)) return configured;
    return defaultModelForAgent(agent, modelGroups);
  });
}

type CodeReviewReasoningEffort = NonNullable<CodeReviewPolicyConfig["agent_roster"]["reasoning_effort"]>;

function reasoningOptionsForAgent(agent: string) {
  const supported = getCodingAgentReasoningOptions(agent);
  return supported.length > 0
    ? CODE_REVIEW_REASONING_OPTIONS.filter((option) => supported.some((effort) => effort.value === option.value))
    : CODE_REVIEW_REASONING_OPTIONS.filter((option) => option.value !== "max");
}

function normalizeReasoningEffortForAgent(agent: string, effort: CodeReviewReasoningEffort | undefined): CodeReviewReasoningEffort {
  const current = effort ?? "high";
  return reasoningOptionsForAgent(agent).some((option) => option.value === current) ? current : "high";
}

function ensureReviewerReasoningEfforts(config: CodeReviewPolicyConfig): CodeReviewReasoningEffort[] {
  return config.agent_roster.reviewers.map((agent, index) =>
    normalizeReasoningEffortForAgent(agent, config.agent_roster.reviewer_reasoning_efforts?.[index] ?? config.agent_roster.reasoning_effort),
  );
}

function normalizeOrchestratorReasoningEffort(config: CodeReviewPolicyConfig): void {
  config.agent_roster.reasoning_effort = normalizeReasoningEffortForAgent(config.agent_roster.orchestrator, config.agent_roster.reasoning_effort);
}

export default function CodeReviewsPage() {
  const queryClient = useQueryClient();
  const { user } = useAuth();
  const canManagePolicy = user?.role === "admin";
  const canFileDisputes = user?.role === "admin" || user?.role === "member";
  const canRetryReviews = canFileDisputes;
  const [tabParam, setTabParam] = useQueryState("tab", parseAsStringLiteral(CODE_REVIEW_TAB_VALUES).withDefault("reviews").withOptions({ history: "push" }));
  const activeTab: CodeReviewTab = tabParam === "disputes" && !canManagePolicy ? "reviews" : tabParam;
  const [addRepositoryParam, setAddRepositoryParam] = useQueryState("add_repository", parseAsString);
  const [githubConnectionResult, setGitHubConnectionResult] = useQueryState("github_pr", parseAsString);
  const [githubAppConnectionResult, setGitHubAppConnectionResult] = useQueryState("github", parseAsString);
  const [addRepositoryOpen, setAddRepositoryOpen] = useState(addRepositoryParam === "1");
  useEffect(() => {
    if (addRepositoryParam === "1") setAddRepositoryOpen(true);
  }, [addRepositoryParam]);
  const openAddRepository = useCallback(() => {
    setAddRepositoryOpen(true);
    void setAddRepositoryParam("1");
  }, [setAddRepositoryParam]);
  const changeAddRepositoryOpen = useCallback(
    (open: boolean) => {
    setAddRepositoryOpen(open);
    void setAddRepositoryParam(open ? "1" : null);
    },
    [setAddRepositoryParam],
  );
  const setActiveTab = useCallback(
    (value: string) => {
      void setTabParam(value as CodeReviewTab);
    },
    [setTabParam],
  );
  const [repositoryFilter, setRepositoryFilter] = useQueryState("repository", parseAsString.withDefault(ALL_REPOSITORIES));
  const [outcomeFilter, setOutcomeParam] = useQueryState("outcome", parseAsStringLiteral(OUTCOME_FILTER_VALUES).withDefault(ALL_OUTCOMES));
  const [timeRangeFilter, setTimeRangeParam] = useQueryState("range", TIME_RANGE_FILTER_PARSER);
  const timeRangeAnchorMsRef = useRef(Date.now());
  const [riskFilter, setRiskFilter] = useQueryState("risk", parseAsStringLiteral(RISK_FILTER_VALUES).withDefault(ALL_RISKS));
  const [reasonFilter, setReasonFilter] = useQueryState(
    "reason",
    parseAsStringLiteral([ALL_CODE_REVIEW_REASONS, ...CODE_REVIEW_REASON_CODES] as const).withDefault(ALL_CODE_REVIEW_REASONS),
  );
  const [statusFilter, setStatusFilter] = useQueryState("status", STATUS_FILTER_PARSER);
  const [authorFilter, setAuthorFilter] = useQueryState("author", parseAsString.withDefault(""));
  const [searchParam, setSearchParam] = useQueryState("search", parseAsString.withDefault(""));
  const [reviewSort, setReviewSort] = useQueryState("sort", parseAsStringLiteral(REVIEW_SORT_VALUES));
  const [reviewSortOrder, setReviewSortOrder] = useQueryState("order", parseAsStringLiteral(["asc", "desc"] as const).withDefault("asc"));
  const [authorSort, setAuthorSort] = useQueryState("author_sort", parseAsStringLiteral(AUTHOR_SORT_VALUES).withDefault("reviews"));
  const [authorSortOrder, setAuthorSortOrder] = useQueryState("author_order", parseAsStringLiteral(["asc", "desc"] as const).withDefault("desc"));
  const [search, setSearch] = useState(searchParam);
  useEffect(() => {
    setSearch(searchParam);
  }, [searchParam]);
  useEffect(() => {
    const timer = window.setTimeout(() => {
      void setSearchParam(search.trim() || null);
    }, CODE_REVIEW_SEARCH_DEBOUNCE_MS);
    return () => window.clearTimeout(timer);
  }, [search, setSearchParam]);
  // The dispute queue deep-links here with ?evidence=<session id>, so the open
  // sheet is URL state. Unlike the filter params this one writes the URL
  // directly rather than through the nuqs setter: the sheet is driven by local
  // state, and the setter's optimistic-then-settle update would re-run the sync
  // effect and close the sheet on the click that just opened it. nuqs patches
  // history.replaceState and syncs from it, so evidenceParam still tracks the
  // write and back/forward still work.
  const [evidenceParam] = useQueryState("evidence", parseAsString);
  const [selectedEvidenceSessionId, setSelectedEvidenceSessionId] = useState<string | null>(evidenceParam);
  useEffect(() => {
    setSelectedEvidenceSessionId(evidenceParam);
  }, [evidenceParam]);
  const selectEvidenceSession = useCallback((sessionID: string | null) => {
    setSelectedEvidenceSessionId(sessionID);
    const url = new URL(window.location.href);
    if (sessionID) url.searchParams.set("evidence", sessionID);
    else url.searchParams.delete("evidence");
    window.history.replaceState(window.history.state, "", `${url.pathname}${url.search}${url.hash}`);
  }, []);
  const [mobileFiltersOpen, setMobileFiltersOpen] = useState(false);
  const [editingRequirementKey, setEditingRequirementKey] = useState<string | null>(null);
  const [promptExample, setPromptExample] = useState<{
    field: "review_instructions" | "automated_approval_policy";
    example: CodeReviewPromptExampleOption | CodeReviewAutomatedApprovalExampleOption;
  } | null>(null);
  const [invalidPolicyField, setInvalidPolicyField] = useState<string | null>(null);
  const promptDraftsRef = useRef<Partial<Record<"review_instructions" | "automated_approval_policy", PromptDraftHandle>>>({});
  const saveSourceByConfigRef = useRef(new WeakMap<CodeReviewPolicyConfig, CodeReviewPolicyEditSource>());
  const persistedPromptsRef = useRef({
    scope: "",
    review_instructions: "",
    automated_approval_policy: DEFAULT_AUTOMATED_APPROVAL_POLICY,
  });
  const setOutcomeFilter = useCallback(
    (value: string) => {
      void setOutcomeParam(value as OutcomeFilter);
    },
    [setOutcomeParam],
  );
  const setTimeRangeFilter = useCallback(
    (value: string) => {
      timeRangeAnchorMsRef.current = Date.now();
      void setTimeRangeParam(value as TimeRangeFilter);
    },
    [setTimeRangeParam],
  );
  const registerPromptDraft = useCallback((field: "review_instructions" | "automated_approval_policy", handle: PromptDraftHandle) => {
    promptDraftsRef.current[field] = handle;
  }, []);
  const reviewRepositoryId = repositoryFilter === ALL_REPOSITORIES ? undefined : repositoryFilter;
  const baseReviewFilters = useMemo(
    () => ({
      repository_id: reviewRepositoryId,
      decision:
        outcomeFilter !== ALL_OUTCOMES && outcomeFilter !== AUTOMATICALLY_APPROVED && outcomeFilter !== COMPLETED_NOT_APPROVED ? (outcomeFilter as CodeReviewDecision) : undefined,
      outcome: outcomeFilter === AUTOMATICALLY_APPROVED || outcomeFilter === COMPLETED_NOT_APPROVED ? (outcomeFilter as CodeReviewListOutcome) : undefined,
      risk: riskFilter === ALL_RISKS ? undefined : (riskFilter as "acceptable" | "needs_review"),
      reason: reasonFilter === ALL_CODE_REVIEW_REASONS ? undefined : (reasonFilter as CodeReviewReasonCode),
      author: authorFilter.trim() || undefined,
      search: searchParam.trim() || undefined,
    }),
    [authorFilter, outcomeFilter, reasonFilter, reviewRepositoryId, riskFilter, searchParam],
  );
  // Which review attempts are in scope for the Reviews tab. Analytics selects
  // PR cohorts only by repository and first-request time.
  const scopedReviewFilters = useMemo(
    () =>
      statusFilter === "cancelled"
      ? {
          ...baseReviewFilters,
          activity_status: DEFAULT_STATUS_FILTER as CodeReviewActivityStatus,
          status: "cancelled" as CodeReviewSessionStatus,
        }
      : {
          ...baseReviewFilters,
          activity_status: statusFilter as CodeReviewActivityStatus,
        },
    [baseReviewFilters, statusFilter],
  );
  const listReviewFilters = useMemo(
    () => ({
      ...scopedReviewFilters,
      sort_by: reviewSort ?? undefined,
      sort_order: reviewSort ? reviewSortOrder : undefined,
    }),
    [reviewSort, reviewSortOrder, scopedReviewFilters],
  );
  const statsReviewFilters = useMemo(
    () => ({
      ...baseReviewFilters,
      activity_status: DEFAULT_STATUS_FILTER as CodeReviewActivityStatus,
    }),
    [baseReviewFilters],
  );
  const reviewScopeQueryKey = useMemo(
    () => ({
      ...listReviewFilters,
      time_range: timeRangeFilter,
    }),
    [listReviewFilters, timeRangeFilter],
  );
  // Keyed on the scope it actually sends: including the list sort would mint a
  // fresh cache entry for an identical request every time the table is re-sorted.
  const analyticsScopeQueryKey = useMemo(
    () => ({
      repository_id: reviewRepositoryId,
      time_range: timeRangeFilter,
      author_sort_by: authorSort,
      author_sort_order: authorSortOrder,
    }),
    [authorSort, authorSortOrder, reviewRepositoryId, timeRangeFilter],
  );
  const statsScopeQueryKey = useMemo(
    () => ({
      ...statsReviewFilters,
      time_range: timeRangeFilter,
    }),
    [statsReviewFilters, timeRangeFilter],
  );
  const currentListReviewFilters = useCallback(
    () => ({
      ...listReviewFilters,
      ...timeRangeBounds(timeRangeFilter, new Date(timeRangeAnchorMsRef.current)),
    }),
    [listReviewFilters, timeRangeFilter],
  );
  const currentStatsReviewFilters = useCallback(
    () => ({
      ...statsReviewFilters,
      ...timeRangeBounds(timeRangeFilter, new Date(timeRangeAnchorMsRef.current)),
    }),
    [statsReviewFilters, timeRangeFilter],
  );
  const currentAnalyticsFilters = useCallback(
    () => ({
      repository_id: reviewRepositoryId,
      author_sort_by: authorSort,
      author_sort_order: authorSortOrder,
      ...timeRangeBounds(timeRangeFilter, new Date(timeRangeAnchorMsRef.current)),
    }),
    [authorSort, authorSortOrder, reviewRepositoryId, timeRangeFilter],
  );
  const reviewFiltersQueryKey = useMemo(
    () => ({
      ...reviewScopeQueryKey,
      limit: CODE_REVIEW_PAGE_SIZE,
    }),
    [reviewScopeQueryKey],
  );
  const hasActiveReviewFilters = Boolean(
    reviewRepositoryId ||
    outcomeFilter !== ALL_OUTCOMES ||
    riskFilter !== ALL_RISKS ||
    reasonFilter !== ALL_CODE_REVIEW_REASONS ||
    statusFilter !== DEFAULT_STATUS_FILTER ||
    authorFilter.trim() ||
    searchParam.trim() ||
    timeRangeFilter !== "all",
  );
  const clearReviewFilters = useCallback(() => {
    void setRepositoryFilter(null);
    void setOutcomeParam(null);
    void setRiskFilter(null);
    void setReasonFilter(null);
    void setStatusFilter(null);
    void setAuthorFilter(null);
    setSearch("");
    void setSearchParam(null);
    setTimeRangeFilter("all");
  }, [setAuthorFilter, setOutcomeParam, setReasonFilter, setRepositoryFilter, setRiskFilter, setSearchParam, setStatusFilter, setTimeRangeFilter]);
  const reviewScopeKey = JSON.stringify(reviewFiltersQueryKey);
  const [extraReviewPages, setExtraReviewPages] = useState<CodeReviewListItem[][]>([]);
  const [loadMoreCursor, setLoadMoreCursor] = useState<string | undefined>();
  const [newReviewsAvailable, setNewReviewsAvailable] = useState(false);
  const isViewingReviewHistory = extraReviewPages.length > 0;
  const [previousReviewScopeKey, setPreviousReviewScopeKey] = useState(reviewScopeKey);
  if (previousReviewScopeKey !== reviewScopeKey) {
    setPreviousReviewScopeKey(reviewScopeKey);
    setExtraReviewPages([]);
    setLoadMoreCursor(undefined);
    setNewReviewsAvailable(false);
    setSelectedEvidenceSessionId(null);
  }
  const repositoriesQuery = useQuery({
    queryKey: queryKeys.repositories.all,
    queryFn: () => api.repositories.list(),
  });
  const integrationsQuery = useQuery({
    queryKey: queryKeys.integrations.all,
    queryFn: () => api.integrations.list(),
  });
  const githubAccountQuery = useQuery({
    queryKey: ["github-status"],
    queryFn: () => api.githubStatus.get(),
  });
  const githubTriggerStatusesQuery = useQuery({
    queryKey: queryKeys.codeReviews.githubTriggers,
    queryFn: () => api.codeReviews.listGitHubTriggers(),
  });
  const refreshRelativeReviewWindow = useCallback(() => {
    timeRangeAnchorMsRef.current = Date.now();
    void queryClient.invalidateQueries({
      queryKey: queryKeys.codeReviews.lists(),
    });
    void queryClient.invalidateQueries({
      queryKey: queryKeys.codeReviews.stats(),
    });
    void queryClient.invalidateQueries({
      queryKey: queryKeys.codeReviews.analytics(),
    });
  }, [queryClient]);
  useEffect(() => {
    if (isViewingReviewHistory && activeTab === "reviews") return;

    let timer: number | undefined;
    const waitUntil = (refreshAtMs: number) => {
      const remainingMs = refreshAtMs - Date.now();
      if (remainingMs <= 0) {
        refreshRelativeReviewWindow();
        scheduleRefresh();
        return;
      }

      timer = window.setTimeout(() => waitUntil(refreshAtMs), Math.min(remainingMs, MAX_BROWSER_TIMEOUT_MS));
    };
    const scheduleRefresh = () => {
      const anchor = new Date(timeRangeAnchorMsRef.current);
      const delay = timeRangeRefreshDelayMs(timeRangeFilter, anchor, CODE_REVIEW_TIME_WINDOW_REFRESH_MS);
      if (delay === null) return;
      waitUntil(anchor.getTime() + delay);
    };

    scheduleRefresh();
    return () => {
      if (timer !== undefined) window.clearTimeout(timer);
    };
  }, [activeTab, isViewingReviewHistory, refreshRelativeReviewWindow, timeRangeFilter]);
  // The reviews list refreshes live via the org-scoped SSE stream below; the
  // polling backstop only kicks in (faster) while the stream is unhealthy so a
  // Redis hiccup still surfaces new reviews. Replaces the old manual Refresh
  // button — mirrors the eval batch/bootstrap stream pattern.
  //
  // The URL is pinned to the org active at mount (empty deps) on purpose: the
  // only org→org switch path (org-switcher) navigates away to /sessions and
  // replaces the QueryClient (see providers.tsx), so this page never stays
  // mounted across an org change — there's nothing to react to here.
  const codeReviewStreamURL = useMemo(() => {
    const apiBase = process.env.NEXT_PUBLIC_API_URL || "";
    return buildCodeReviewStreamURL(apiBase, getActiveOrgId());
  }, []);
  // A single review lifecycle emits several events (queued → running →
  // completed), and a batch-stale transition can fan out across the org — so
  // coalesce bursts into one refetch per window rather than one per event.
  const invalidateTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const onCodeReviewEvent = useCallback(() => {
    if (isViewingReviewHistory) {
      setNewReviewsAvailable(true);
      if (activeTab === "reviews") return;
    }
    if (invalidateTimerRef.current) return;
    invalidateTimerRef.current = setTimeout(() => {
      invalidateTimerRef.current = null;
      refreshRelativeReviewWindow();
    }, pollMs(CODE_REVIEW_INVALIDATE_COALESCE_MS));
  }, [activeTab, isViewingReviewHistory, refreshRelativeReviewWindow]);
  useEffect(
    () => () => {
      if (invalidateTimerRef.current) clearTimeout(invalidateTimerRef.current);
    },
    [],
  );
  const { healthy: codeReviewStreamHealthy } = useResourceSSE({
    url: codeReviewStreamURL,
    event: SSE_EVENT.CODE_REVIEW_UPDATED,
    onEvent: onCodeReviewEvent,
  });
  const reviewsQuery = useQuery({
    queryKey: queryKeys.codeReviews.list(reviewFiltersQueryKey),
    queryFn: () =>
      api.codeReviews.list({
      ...currentListReviewFilters(),
      limit: CODE_REVIEW_PAGE_SIZE,
    }),
    // A loaded history window must remain a coherent cursor snapshot. Disabling
    // the observer blocks reconnects and unrelated broad invalidations from
    // replacing page one while the older pages remain appended.
    enabled: !isViewingReviewHistory,
    refetchInterval: isViewingReviewHistory ? false : codeReviewStreamHealthy ? pollMs(30_000) : pollMs(5_000),
  });
  const statsQuery = useQuery({
    queryKey: queryKeys.codeReviews.stat(statsScopeQueryKey),
    queryFn: () => api.codeReviews.stats(currentStatsReviewFilters()),
    refetchInterval: isViewingReviewHistory ? false : codeReviewStreamHealthy ? pollMs(30_000) : pollMs(5_000),
  });
  const analyticsQuery = useQuery<SingleResponse<CodeReviewAnalytics>>({
    queryKey: queryKeys.codeReviews.analyticsReport(analyticsScopeQueryKey),
    queryFn: () => api.codeReviews.analytics(currentAnalyticsFilters()),
    enabled: activeTab === "analytics",
    refetchInterval: activeTab !== "analytics" ? false : codeReviewStreamHealthy ? pollMs(30_000) : pollMs(5_000),
  });
  const policyQuery = useQuery({
    queryKey: queryKeys.codeReviews.policy,
    queryFn: () => api.codeReviews.getPolicy(),
  });
  const settingsQuery = useQuery({
    queryKey: queryKeys.settings.all,
    queryFn: () => api.settings.get(),
  });
  const resolvedCredentialsQuery = useQuery<ListResponse<CodingCredentialSummary>>({
    queryKey: queryKeys.codingCredentials.list("resolved"),
    queryFn: () => api.codingCredentials.list("resolved"),
  });
  const orgCodingCredentialsQuery = useQuery<ListResponse<CodingCredentialSummary>>({
    queryKey: queryKeys.codingCredentials.list("org"),
    queryFn: () => api.codingCredentials.list("org"),
  });
  const codexAuthQuery = useQuery({
    queryKey: queryKeys.codexAuth.status,
    queryFn: () => api.codexAuth.status(),
  });
  const repositories = repositoriesQuery.data?.data ?? [];
  const githubIntegration = integrationsQuery.data?.data.find((integration) => integration.provider === "github" && integration.status === "active");
  const githubTriggerStatuses = githubTriggerStatusesQuery.data?.data ?? [];
  const visibleGitHubTriggerStatuses = githubTriggerStatuses
    .filter((trigger) => trigger.repository_status !== "disconnected" || !!trigger.trigger)
    .slice()
    .sort((left, right) => {
      const priority = (status: CodeReviewGitHubTriggerResponse["status"]) => {
        if (status === "disconnected" || status === "permission_required" || status === "error") return 0;
        if (status === "ready") return 1;
        return 2;
      };
      return priority(left.status) - priority(right.status) || (left.repository_full_name ?? "").localeCompare(right.repository_full_name ?? "");
    });

  useEffect(() => {
    if (githubConnectionResult !== "connected") return;
    toast.success("GitHub account connected", {
      description: "Choose a repository to connect and set up its reviewer.",
    });
    void queryClient.invalidateQueries({ queryKey: ["github-status"] });
    void queryClient.invalidateQueries({
      queryKey: queryKeys.codeReviews.githubTriggers,
    });
    void queryClient.invalidateQueries({
      queryKey: queryKeys.integrations.all,
    });
    void setGitHubConnectionResult(null);
  }, [githubConnectionResult, queryClient, setGitHubConnectionResult]);
  useEffect(() => {
    if (githubAppConnectionResult !== "connected") return;
    toast.success("GitHub App connected", {
      description: "Choose a repository to connect and set up its reviewer.",
    });
    void queryClient.invalidateQueries({
      queryKey: queryKeys.integrations.all,
    });
    void setGitHubAppConnectionResult(null);
  }, [githubAppConnectionResult, queryClient, setGitHubAppConnectionResult]);
  const promptExamplesQuery = useQuery({
    queryKey: queryKeys.codeReviews.promptExamples,
    queryFn: () => api.codeReviews.promptExamples(),
  });
  const evidenceQuery = useQuery({
    queryKey: queryKeys.codeReviews.evidence(selectedEvidenceSessionId ?? ""),
    queryFn: () => api.codeReviews.evidence(selectedEvidenceSessionId ?? ""),
    enabled: Boolean(selectedEvidenceSessionId),
  });
  const disputeQueueQuery = useInfiniteQuery({
    queryKey: queryKeys.codeReviews.disputeQueue({
      adjudication_status: "pending",
    }),
    queryFn: ({ pageParam }) =>
      api.codeReviews.disputeQueue({
        adjudication_status: "pending",
        cursor: pageParam,
      }),
    enabled: canManagePolicy,
    refetchInterval: activeTab === "disputes" && canManagePolicy ? pollMs(30_000) : false,
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (lastPage) => lastPage.meta?.next_cursor || undefined,
  });
  useEffect(() => {
    if (activeTab !== "disputes" || !canManagePolicy) return;
    void queryClient.invalidateQueries({
      queryKey: ["code-reviews", "dispute-queue"],
    });
  }, [activeTab, canManagePolicy, queryClient]);
  const pendingDisputes = useMemo(() => disputeQueueQuery.data?.pages.flatMap((page) => page.data ?? []) ?? [], [disputeQueueQuery.data]);
  const adjudicateDispute = useMutation({
    mutationFn: ({ dispute, status, note, activeSeconds }: { dispute: CodeReviewDispute; status: "upheld" | "rejected" | "needs_context"; note?: string; activeSeconds: number }) =>
      api.codeReviews.adjudicateDispute(dispute.id, {
        expected_version: dispute.version,
        adjudication_status: status,
        adjudication_note: note?.trim() || undefined,
        policy_owner_active_seconds: activeSeconds,
      }),
    onSuccess: () => {
      void queryClient.invalidateQueries({
        queryKey: ["code-reviews", "dispute-queue"],
      });
      toast.success("Dispute adjudication saved");
    },
    onError: () => toast.error("Dispute adjudication could not be saved"),
  });

  // The policy is autosaved as a single whole-config PUT. Each control reads
  // the live config straight from the query cache and commits a fully-merged
  // config built from the freshest cache value (per settings/AGENTS.md), so
  // back-to-back edits never clobber one another.
  const config = policyQuery.data?.data.config ?? null;
  if (config && persistedPromptsRef.current.scope !== "organization") {
    persistedPromptsRef.current = {
      scope: "organization",
      review_instructions: config.review_instructions,
      automated_approval_policy: config.automated_approval_policy,
    };
  }
  const viewedScopeRef = useRef<string | null>(null);
  useEffect(() => {
    if (!config) return;
    if (viewedScopeRef.current === "organization") return;
    viewedScopeRef.current = "organization";
    trackCodeReviewPolicyEvent({
      event: "code_review_policy_viewed",
      scope: "organization",
      configured: policyQuery.data?.data.source !== "default",
    });
  }, [config, policyQuery.data?.data.source]);
  const coalescePolicy = useCallback((queued: CodeReviewPolicyConfig, incoming: CodeReviewPolicyConfig) => {
    const merged = coalesceCodeReviewPolicy(queued, incoming);
    saveSourceByConfigRef.current.set(merged, saveSourceByConfigRef.current.get(incoming) ?? "manual");
    return merged;
  }, []);
  const autosave = useAutosave<CodeReviewPolicyConfig>({
    queryKey: queryKeys.codeReviews.policy,
    mutationFn: async (next: CodeReviewPolicyConfig) => {
      try {
        return await api.codeReviews.updatePolicy({
          config: next,
          source: saveSourceByConfigRef.current.get(next) ?? "manual",
        });
      } finally {
        // Refetch the single resolved policy so the optimistic config is
        // reconciled with the newly persisted version.
        void queryClient.invalidateQueries({
          queryKey: queryKeys.codeReviews.policy,
        });
      }
    },
    applyOptimistic: applyCodeReviewPolicyOptimistic,
    coalesce: coalescePolicy,
    debounceMs: 0,
    onError: (error) => {
      if (error instanceof ApiError && error.details && typeof error.details === "object" && "field" in error.details) {
        setInvalidPolicyField(String((error.details as { field: unknown }).field));
      }
    },
    onSuccess: (saved) => {
      if (persistedPromptsRef.current.scope === "organization") {
        persistedPromptsRef.current = {
          scope: "organization",
          review_instructions: saved.review_instructions,
          automated_approval_policy: saved.automated_approval_policy,
        };
      }
      setInvalidPolicyField(null);
      void queryClient.invalidateQueries({ queryKey: ["audit-logs"] });
    },
  });
  const readLatestConfig = (): CodeReviewPolicyConfig | null =>
    queryClient.getQueryData<SingleResponse<CodeReviewResolvedPolicy>>(queryKeys.codeReviews.policy)?.data?.config ?? config;
  const setupGitHubTrigger = useMutation({
    mutationFn: (targetRepositoryId: string) => api.codeReviews.setupGitHubTrigger(targetRepositoryId),
    onSuccess: (_data, targetRepositoryId) => {
      void queryClient.invalidateQueries({
        queryKey: queryKeys.codeReviews.githubTrigger(targetRepositoryId),
      });
      void queryClient.invalidateQueries({
        queryKey: queryKeys.codeReviews.githubTriggers,
      });
      trackCodeReviewPolicyEvent({
        event: "code_review_github_setup_completed",
        scope: "repository",
        configured: true,
      });
    },
    onError: () =>
      trackCodeReviewPolicyEvent({
        event: "code_review_github_setup_failed",
        scope: "repository",
        configured: false,
      }),
  });
  const deleteGitHubTrigger = useMutation({
    mutationFn: (targetRepositoryId: string) => api.codeReviews.deleteGitHubTrigger(targetRepositoryId),
    onSuccess: (_data, targetRepositoryId) => {
      void queryClient.invalidateQueries({
        queryKey: queryKeys.codeReviews.githubTrigger(targetRepositoryId),
      });
      void queryClient.invalidateQueries({
        queryKey: queryKeys.codeReviews.githubTriggers,
      });
    },
  });
  const [retryingReviewSessionIds, setRetryingReviewSessionIds] = useState<Set<string>>(() => new Set());
  const retryReview = useMutation({
    mutationFn: (sessionId: string) => api.codeReviews.retry(sessionId),
    onMutate: (sessionId) => {
      setRetryingReviewSessionIds((current) => new Set(current).add(sessionId));
    },
    onSuccess: (_result, sessionId) => {
      if (selectedEvidenceSessionId === sessionId) selectEvidenceSession(null);
      if (isViewingReviewHistory) setNewReviewsAvailable(true);
      else {
        void queryClient.invalidateQueries({
          queryKey: queryKeys.codeReviews.lists(),
        });
        void queryClient.invalidateQueries({
          queryKey: queryKeys.codeReviews.stats(),
        });
      }
      toast.success("Code review retry started");
    },
    onError: (error) => {
      if (isViewingReviewHistory) setNewReviewsAvailable(true);
      else {
        void queryClient.invalidateQueries({
          queryKey: queryKeys.codeReviews.lists(),
        });
        void queryClient.invalidateQueries({
          queryKey: queryKeys.codeReviews.stats(),
        });
      }
      toast.error("Code review could not be retried", {
        description: apiErrorMessage(error) ?? "Try again after checking the review failure details.",
      });
    },
    onSettled: (_result, _error, sessionId) => {
      setRetryingReviewSessionIds((current) => {
        const next = new Set(current);
        next.delete(sessionId);
        return next;
      });
    },
  });
  const firstReviewPage = useMemo(() => reviewsQuery.data?.data ?? [], [reviewsQuery.data?.data]);
  const reviews = useMemo(() => {
    const byID = new Map<string, CodeReviewListItem>();
    for (const review of [firstReviewPage, ...extraReviewPages].flat()) byID.set(review.id, review);
    return [...byID.values()];
  }, [extraReviewPages, firstReviewPage]);
  const firstReviewCursor = reviewsQuery.data?.meta?.next_cursor || undefined;
  const nextReviewCursor = isViewingReviewHistory ? loadMoreCursor : firstReviewCursor;
  const totalReviewCount = reviewsQuery.data?.meta?.total_count;
  const loadMoreReviews = useMutation({
    mutationFn: (request: { filters: ReturnType<typeof currentListReviewFilters> & { limit: number }; cursor: string; scopeKey: string }) =>
      api.codeReviews.list({ ...request.filters, cursor: request.cursor }),
    onSuccess: (response, request) => {
      if (request.scopeKey !== reviewScopeKey) return;
      setExtraReviewPages((pages) => [...pages, response.data ?? []]);
      setLoadMoreCursor(response.meta?.next_cursor || undefined);
    },
  });
  useEffect(() => {
    loadMoreReviews.reset();
  // reset is stable for the lifetime of the mutation observer.
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [reviewScopeKey]);
  const refreshNewestReviews = useCallback(() => {
    timeRangeAnchorMsRef.current = Date.now();
    setExtraReviewPages([]);
    setLoadMoreCursor(undefined);
    setNewReviewsAvailable(false);
    void reviewsQuery.refetch();
    void statsQuery.refetch();
  }, [reviewsQuery, statsQuery]);
  const [countdownNowMs, setCountdownNowMs] = useState(() => Date.now());
  const hasScheduledReviewRetry = reviews.some((item) => Boolean(item.retry_at));
  useEffect(() => {
    if (!hasScheduledReviewRetry) return;
    const timer = window.setInterval(() => setCountdownNowMs(Date.now()), 1_000);
    return () => window.clearInterval(timer);
  }, [hasScheduledReviewRetry]);
  const listedEvidenceReview = useMemo(() => reviews.find((review) => review.session_id === selectedEvidenceSessionId) ?? null, [reviews, selectedEvidenceSessionId]);
  // The list is windowed (default 30d) and paginated, but ?evidence=<session id>
  // is deep-linked from the dispute queue and from every GitHub dispute reply.
  // Without this fallback the sheet silently never opens for a review that has
  // scrolled out of the loaded page or the selected time range.
  const evidenceReviewQuery = useQuery({
    queryKey: queryKeys.codeReviews.detail(selectedEvidenceSessionId ?? ""),
    queryFn: () => api.codeReviews.get(selectedEvidenceSessionId ?? ""),
    enabled: Boolean(selectedEvidenceSessionId) && !listedEvidenceReview,
    // A deep link to a deleted or wrong session is a permanent 404, and
    // retrying it only delays the error notice below. Transient failures still
    // get one retry.
    retry: (failureCount, error) => !(error instanceof ApiError && error.status === 404) && failureCount < 1,
  });
  const selectedEvidenceReview =
    listedEvidenceReview ?? (selectedEvidenceSessionId && evidenceReviewQuery.data?.data?.session_id === selectedEvidenceSessionId ? evidenceReviewQuery.data.data : null);
  // A deep link that cannot be resolved must say so. Leaving the sheet closed
  // reproduces the silent no-op this fallback exists to remove.
  const evidenceDeepLinkError = Boolean(selectedEvidenceSessionId) && !listedEvidenceReview ? evidenceReviewQuery.error : null;
  const orgSettings = (settingsQuery.data?.data?.settings ?? {}) as OrgSettings;
  const orgCodingCredentials = useMemo(() => orgCodingCredentialsQuery.data?.data ?? [], [orgCodingCredentialsQuery.data?.data]);
  const codeReviewResolvedCredentials = useMemo(() => pmUsableResolvedCredentials(resolvedCredentialsQuery.data?.data ?? []), [resolvedCredentialsQuery.data?.data]);
  const codeReviewModelGroups = useMemo(
    () =>
      availableAgentModelGroups(codeReviewResolvedCredentials, codexAuthQuery.data?.data, orgCodingCredentials, orgSettings.default_agent_type || "codex", {
        orgAgentConfig: orgSettings.agent_config,
      }),
    [codeReviewResolvedCredentials, codexAuthQuery.data?.data, orgCodingCredentials, orgSettings.default_agent_type, orgSettings.agent_config],
  );
  const codeReviewOpenCodeAvailability = useOpenCodeAvailability(orgCodingCredentials, orgSettings.opencode_routing?.require_openrouter ?? false);
  const editingRequirementIndex = useMemo(
    () => (editingRequirementKey && config ? config.description_policy.requirements.findIndex((requirement) => requirement.key === editingRequirementKey) : -1),
    [config, editingRequirementKey],
  );
  const editingRequirement = editingRequirementIndex >= 0 && config ? config.description_policy.requirements[editingRequirementIndex] : null;
  // Build a fully-merged config from the freshest cache value. Returns null
  // only before the policy has loaded (controls are disabled until then).
  const draftFrom = (mutate: (next: CodeReviewPolicyConfig) => void): CodeReviewPolicyConfig | null => {
    const base = readLatestConfig();
    if (!base) return null;
    const next = clonePolicy(base);
    mutate(next);
    return next;
  };
  // Instant commit for toggles/selects/buttons.
  const commitPolicy = (mutate: (next: CodeReviewPolicyConfig) => void, source: CodeReviewPolicyEditSource = "manual") => {
    const next = draftFrom(mutate);
    if (next) {
      saveSourceByConfigRef.current.set(next, source);
      setInvalidPolicyField(null);
      autosave.save(next);
    }
  };
  // toPatch builder for numeric fields, which require a non-null payload. Safe
  // because numeric inputs are disabled until the policy has loaded.
  const buildConfig = (mutate: (next: CodeReviewPolicyConfig) => void): CodeReviewPolicyConfig => {
    const next = draftFrom(mutate);
    if (!next) return config as CodeReviewPolicyConfig;
    return next;
  };
  const commitRequirementByKey = (
    key: string,
    updater: (requirement: CodeReviewPolicyConfig["description_policy"]["requirements"][number]) => CodeReviewPolicyConfig["description_policy"]["requirements"][number],
  ) => {
    commitPolicy((next) => {
      const index = next.description_policy.requirements.findIndex((requirement) => requirement.key === key);
      if (index === -1) return;
      next.description_policy.requirements[index] = updater(next.description_policy.requirements[index]);
    });
  };
  const sortHeader = (label: string, sort: ReviewSort) => {
    const active = reviewSort === sort;
    return (
      <SortableTableHeader
        label={label}
        direction={active ? reviewSortOrder : false}
        // The list has a default order (newest first), so the third click on a
        // column returns to it rather than cycling back to ascending.
        allowUnsorted
        onSort={(nextOrder) => {
          void setReviewSort(nextOrder === false ? null : sort);
          void setReviewSortOrder(nextOrder === false ? null : nextOrder);
        }}
      />
    );
  };

  const reviewColumns: ResponsiveResourceListColumn<CodeReviewListItem>[] = [
    {
      id: "pull-request",
      header: sortHeader("PR", "pull_request"),
      sortDirection: reviewSort === "pull_request" ? reviewSortOrder : false,
      cellClassName: "min-w-[18rem]",
      render: (review) => (
        <>
          <div className="font-medium text-foreground">
            <ReviewTitle review={review} />
          </div>
          <div className="mt-1 text-xs text-muted-foreground">
            {review.repository_name || review.github_repo} · {review.pull_request_author || "Unknown author"} · {review.head_sha.slice(0, 7)}
          </div>
        </>
      ),
    },
    {
      id: "outcome",
      header: sortHeader("Outcome", "outcome"),
      sortDirection: reviewSort === "outcome" ? reviewSortOrder : false,
      render: (review) => <StatusLabel label={decisionLabel(review)} tone={reviewDecisionTone(review)} indicator="none" />,
    },
    {
      id: "why-not-approved",
      header: "Why not approved",
      cellClassName: "min-w-[14rem]",
      render: (review) => <WhyNotApproved reasons={whyNotApprovedReasons(review)} />,
    },
    {
      id: "run-status",
      header: sortHeader("Run status", "run_status"),
      sortDirection: reviewSort === "run_status" ? reviewSortOrder : false,
      render: (review) => <ReviewOperationalStatus review={review} nowMs={countdownNowMs} />,
    },
    {
      id: "completed",
      header: sortHeader("Completed", "completed"),
      sortDirection: reviewSort === "completed" ? reviewSortOrder : false,
      render: (review) => formatDate(review.completed_at),
    },
    {
      id: "actions",
      header: <span className="sr-only">Actions</span>,
      className: "text-right",
      cellClassName: "text-right",
      render: (review) => (
        <ReviewActions
          review={review}
          canRetry={canRetryReviews}
          isRetrying={retryingReviewSessionIds.has(review.session_id)}
          evidenceSelected={selectedEvidenceSessionId === review.session_id}
          onRetry={() => retryReview.mutate(review.session_id)}
          onToggleEvidence={() => selectEvidenceSession(selectedEvidenceSessionId === review.session_id ? null : review.session_id)}
        />
      ),
    },
  ];
  const sharedFilterValues: CodeReviewFilterValues = {
    repository: repositoryFilter,
    outcome: outcomeFilter,
    risk: riskFilter,
    reason: reasonFilter,
    status: statusFilter,
    author: authorFilter,
    search,
    timeRange: timeRangeFilter,
  };
  const changeSharedFilter = (field: keyof CodeReviewFilterValues, value: string) => {
    switch (field) {
      case "repository":
        void setRepositoryFilter(value === ALL_REPOSITORIES ? null : value);
        break;
      case "outcome":
        setOutcomeFilter(value);
        break;
      case "risk":
        void setRiskFilter(value === ALL_RISKS ? null : (value as (typeof RISK_FILTER_VALUES)[number]));
        break;
      case "reason":
        void setReasonFilter(value === ALL_CODE_REVIEW_REASONS ? null : (value as CodeReviewReasonCode));
        break;
      case "status":
        void setStatusFilter(value === DEFAULT_STATUS_FILTER ? null : (value as StatusFilter));
        break;
      case "author":
        void setAuthorFilter(value || null);
        break;
      case "search":
        setSearch(value);
        break;
      case "timeRange":
        setTimeRangeFilter(value);
        break;
    }
  };

  return (
    <ListPage title="Code reviews" description="Bot-requested PR reviews, acceptable-risk policy, and review outcomes.">
      <Tabs value={activeTab} onValueChange={setActiveTab} className="space-y-4">
        <TabsList>
          <TabsTrigger value="reviews">
            <ClipboardCheck className="h-4 w-4" />
            Reviews
          </TabsTrigger>
          <TabsTrigger value="analytics">
            <ChartNoAxesColumnIncreasing className="h-4 w-4" />
            Analytics
          </TabsTrigger>
          {canManagePolicy ? (
            <TabsTrigger value="disputes">
              <MessageSquareText className="h-4 w-4" />
              Disputes
              {pendingDisputes.length > 0 ? (
                <Badge variant="secondary" aria-hidden="true">
                  {disputeQueueQuery.hasNextPage ? `${pendingDisputes.length}+` : pendingDisputes.length}
                </Badge>
              ) : null}
            </TabsTrigger>
          ) : null}
          <TabsTrigger value="policy">
            <Settings2 className="h-4 w-4" />
            Policy
          </TabsTrigger>
        </TabsList>

        <PageTabContent value="reviews">
          <CodeReviewSummaryCards stats={statsQuery.data?.data} isLoading={statsQuery.isLoading} isError={statsQuery.isError} onRetry={() => void statsQuery.refetch()} />
          <CodeReviewFilters
            id="code-review-filters"
            values={sharedFilterValues}
            repositories={repositories}
            mobileOpen={mobileFiltersOpen}
            onMobileOpenChange={setMobileFiltersOpen}
            onChange={changeSharedFilter}
            />
          <SectionGroup title="Review activity" description="Pull requests reviewed by the team policy and their current outcome.">
              {newReviewsAvailable ? (
                <div className="flex items-center justify-between gap-3 rounded-md border border-border bg-muted/30 px-3 py-2 text-sm">
                  <span>New reviews are available.</span>
                <Button variant="outline" size="sm" onClick={refreshNewestReviews}>
                  Refresh
                </Button>
                </div>
              ) : null}
              {reviewsQuery.isLoading ? (
                <div className="py-12 text-center text-sm text-muted-foreground">Loading code reviews…</div>
              ) : reviewsQuery.isError ? (
                <ErrorNotice
                  title="Code reviews could not be loaded"
                  description="Try again to load review activity."
                action={{
                  label: "Retry",
                  onClick: () => void reviewsQuery.refetch(),
                }}
                />
              ) : reviews.length === 0 ? (
                <EmptyState
                  icon={ClipboardCheck}
                  title={hasActiveReviewFilters ? "No reviews match these filters" : "No code review sessions"}
                description={
                  hasActiveReviewFilters
                    ? "Adjust or clear the filters to see more review activity."
                    : "Reviews will appear here after the GitHub reviewer bot is requested on a pull request."
                }
                  action={hasActiveReviewFilters ? { label: "Clear filters", onClick: clearReviewFilters } : undefined}
                />
              ) : (
                <>
                <ResponsiveResourceList
                  ariaLabel="Code reviews"
                  mobileAriaLabel="Code review activity"
                  items={reviews}
                  getItemKey={(review) => review.id}
                  columns={reviewColumns}
                  emptyState="No code review sessions."
                  footer={
                    <div className="flex flex-wrap items-center justify-between gap-3 border-t border-border/50 bg-muted/20 px-4 py-2.5">
                      <span className="text-xs tabular-nums text-muted-foreground" aria-live="polite">
                        {totalReviewCount !== undefined ? `Showing ${reviews.length} of ${totalReviewCount}` : `${reviews.length} review${reviews.length === 1 ? "" : "s"}`}
                      </span>
                      <div className="flex items-center gap-3">
                        {loadMoreReviews.isError ? <span className="text-xs text-destructive">Couldn&apos;t load more reviews.</span> : null}
                        {nextReviewCursor ? (
                          <Button
                            variant="ghost"
                            size="sm"
                            onClick={() =>
                              loadMoreReviews.mutate({
                              filters: {
                                ...currentListReviewFilters(),
                                limit: CODE_REVIEW_PAGE_SIZE,
                              },
                              cursor: nextReviewCursor,
                              scopeKey: reviewScopeKey,
                              })
                            }
                            disabled={loadMoreReviews.isPending}
                          >
                            {loadMoreReviews.isPending ? "Loading…" : "Show 50 more"}
                          </Button>
                        ) : null}
                      </div>
                    </div>
                  }
                  renderMobileItem={(review) => (
                  <ResourceRow
                    title={
                      <span className="break-words text-sm leading-5">
                        <ReviewTitle review={review} />
                      </span>
                    }
                    metadata={
                      <span>
                        {review.repository_name || review.github_repo} · {review.pull_request_author || "Unknown author"} · {review.head_sha.slice(0, 7)}
                      </span>
                    }
                    detail={
                      <div className="space-y-2.5 pt-1">
                        <ReviewOperationalStatus review={review} nowMs={countdownNowMs} />
                        <div className="flex flex-wrap items-center gap-x-3 gap-y-2">
                            <StatusLabel label={decisionLabel(review)} tone={reviewDecisionTone(review)} indicator="none" />
                            {review.completed_at ? <span className="text-foreground">{formatDate(review.completed_at)}</span> : null}
                        </div>
                        <MobileWhyNotApproved review={review} />
                      </div>
                    }
                    actions={
                      <div className="flex w-full flex-wrap items-center gap-2">
                        <ReviewActions
                          review={review}
                          canRetry={canRetryReviews}
                          isRetrying={retryingReviewSessionIds.has(review.session_id)}
                          evidenceSelected={selectedEvidenceSessionId === review.session_id}
                          onRetry={() => retryReview.mutate(review.session_id)}
                          onToggleEvidence={() => selectEvidenceSession(selectedEvidenceSessionId === review.session_id ? null : review.session_id)}
                          className="ml-auto w-auto"
                        />
                      </div>
                    }
                    className="px-4 py-3.5 [&_[data-slot=resource-row-actions]]:ml-0"
                  />
                  )}
                />
                {evidenceDeepLinkError ? (
                  <ErrorNotice
                    title="That code review could not be opened"
                    description="The link may point at a review that no longer exists. Retry, or clear it to return to the list."
                    action={{
                      label: "Retry",
                      onClick: () => void evidenceReviewQuery.refetch(),
                    }}
                    onDismiss={() => selectEvidenceSession(null)}
                    dismissLabel="Clear the evidence link"
                  />
                ) : null}
                <CodeReviewEvidenceSheet
                  key={selectedEvidenceReview?.session_id ?? "no-review"}
                  review={selectedEvidenceReview}
                  evidence={evidenceQuery.data?.data}
                  isLoading={evidenceQuery.isLoading}
                  error={evidenceQuery.error}
                  nowMs={countdownNowMs}
                  canRetryReview={canRetryReviews}
                  canFileDisputes={canFileDisputes}
                  canManagePolicy={canManagePolicy}
                  isRetryingReview={Boolean(selectedEvidenceReview && retryingReviewSessionIds.has(selectedEvidenceReview.session_id))}
                  onRetryEvidence={() => void evidenceQuery.refetch()}
                  onRetryReview={() => {
                    if (selectedEvidenceReview) retryReview.mutate(selectedEvidenceReview.session_id);
                  }}
                  open={Boolean(selectedEvidenceReview)}
                  onOpenChange={(open) => {
                    if (!open) selectEvidenceSession(null);
                  }}
                />
                </>
              )}
            </SectionGroup>
          </PageTabContent>

          {canManagePolicy ? (
            <PageTabContent value="disputes">
              <CodeReviewDisputeQueue
                disputes={pendingDisputes}
                isLoading={disputeQueueQuery.isLoading}
                error={disputeQueueQuery.error}
                isSaving={adjudicateDispute.isPending}
                hasMore={disputeQueueQuery.hasNextPage}
                isLoadingMore={disputeQueueQuery.isFetchingNextPage}
                onLoadMore={() => void disputeQueueQuery.fetchNextPage()}
                onRetry={() => void disputeQueueQuery.refetch()}
              onAdjudicate={(dispute, status, note, activeSeconds, onSaved, onFailed) =>
                adjudicateDispute.mutate({ dispute, status, note, activeSeconds }, { onSuccess: onSaved, onError: onFailed })
              }
              />
            </PageTabContent>
          ) : null}

          <PageTabContent value="analytics">
            <CodeReviewAnalyticsReport
              analytics={analyticsQuery.data?.data}
              isLoading={analyticsQuery.isLoading}
              isError={analyticsQuery.isError}
              onRetry={() => void analyticsQuery.refetch()}
              authorSort={authorSort}
              authorSortOrder={authorSortOrder}
              onAuthorSort={(sort: AuthorSort, order) => {
                void setAuthorSort(sort);
                void setAuthorSortOrder(order);
              }}
              reviewLinkFilters={{
                repository: reviewRepositoryId,
                range: timeRangeFilter,
              }}
            filters={
                <CodeReviewFilters
                  id="code-review-analytics-filters"
                  values={sharedFilterValues}
                  repositories={repositories}
                  mobileOpen={mobileFiltersOpen}
                  onMobileOpenChange={setMobileFiltersOpen}
                  onChange={changeSharedFilter}
                  timeRangeLabel="PRs first sent to 143 during this period"
                  analyticsMode
                  mobileLabel="Filter analytics"
                />
            }
            />
          </PageTabContent>

          <PageTabContent value="policy">
            <SectionGroup
              title="Organization review policy"
              description="How the reviewer bot handles pull requests across this organization."
              action={<AutosaveIndicator status={autosave.status} />}
              className="space-y-6"
            >
              {!canManagePolicy ? (
              <p className="text-sm text-muted-foreground">You have view-only access to this policy. An organization administrator can change review behavior and GitHub setup.</p>
              ) : null}
              <fieldset disabled={!canManagePolicy} className="space-y-6">
                <SectionGroup title="Review behavior" variant="bordered" headingLevel={3}>
                  <OutcomeControl
                    config={config}
                    disabled={!config}
                    onChange={(outcome) => {
                      const prior = policyOutcome(config);
                      commitPolicy((next) => {
                        if (outcome === "disabled") next.enabled = false;
                        else {
                          next.enabled = true;
                          next.approval_mode = (outcome === "approve" ? "approve_acceptable" : "comment_only") as CodeReviewApprovalMode;
                        }
                      });
                    if ((prior === "disabled") !== (outcome === "disabled"))
                      trackCodeReviewPolicyEvent({
                        event: "code_review_policy_enabled",
                        scope: "organization",
                        configured: outcome !== "disabled",
                      });
                    if (outcome !== "disabled" && outcome !== prior)
                      trackCodeReviewPolicyEvent({
                        event: "code_review_approval_mode_changed",
                        scope: "organization",
                        configured: true,
                      });
                    }}
                  />
                  <PolicySummary config={config} />
                </SectionGroup>
                <PolicyPromptComposer
                  field="automated_approval_policy"
                  config={config}
                  autosave={autosave}
                  commitPolicy={commitPolicy}
                  examples={promptExamplesQuery.data?.data}
                  examplesError={apiErrorMessage(promptExamplesQuery.error) ?? undefined}
                  onRetryExamples={() => void promptExamplesQuery.refetch()}
                onChooseExample={(field, example) => {
                  setPromptExample({ field, example });
                  trackCodeReviewPolicyEvent({
                    event: "code_review_prompt_example_previewed",
                    scope: "organization",
                    example_key: example.key,
                    configured: true,
                  });
                }}
                  onDraftHandle={registerPromptDraft}
                  invalidPolicyField={invalidPolicyField}
                />
                <PolicyPromptComposer
                  field="review_instructions"
                  config={config}
                  autosave={autosave}
                  commitPolicy={commitPolicy}
                  examples={promptExamplesQuery.data?.data}
                  onRetryExamples={() => void promptExamplesQuery.refetch()}
                onChooseExample={(field, example) => {
                  setPromptExample({ field, example });
                  trackCodeReviewPolicyEvent({
                    event: "code_review_prompt_example_previewed",
                    scope: "organization",
                    example_key: example.key,
                    configured: true,
                  });
                }}
                  onDraftHandle={registerPromptDraft}
                  invalidPolicyField={invalidPolicyField}
                />
                <AdvancedPolicySettings
                  config={config}
                  autosave={autosave}
                  buildConfig={buildConfig}
                  commitPolicy={commitPolicy}
                  codeReviewModelGroups={codeReviewModelGroups}
                  codeReviewOpenCodeAvailability={codeReviewOpenCodeAvailability}
                  setEditingRequirementKey={setEditingRequirementKey}
                  invalidPolicyField={invalidPolicyField}
                  analyticsScope="organization"
                />
              </fieldset>
              <SectionGroup
                title="GitHub reviewer connections"
                description="Connect repositories and manage where teammates can request the 143 reviewer from GitHub."
                variant="bordered"
                headingLevel={3}
              action={
                <DisabledTooltip disabled={!canManagePolicy} content={!canManagePolicy ? "Only organization administrators can add GitHub reviewer connections." : undefined}>
                  <Button size="sm" variant="outline" disabled={!canManagePolicy} onClick={openAddRepository}>
                      <Plus className="h-4 w-4" />
                      Add repository
                    </Button>
                  </DisabledTooltip>
              }
              >
                <div className="space-y-3" aria-label="GitHub reviewer repositories">
                  {githubTriggerStatusesQuery.isLoading ? (
                    <div className="rounded-md border border-border p-4 text-sm text-muted-foreground">Loading repositories…</div>
                  ) : githubTriggerStatusesQuery.isError ? (
                    <ErrorNotice
                      title="GitHub reviewer connections could not be loaded"
                      description={apiErrorMessage(githubTriggerStatusesQuery.error) ?? "Try again in a moment."}
                    action={{
                      label: "Retry",
                      onClick: () => void githubTriggerStatusesQuery.refetch(),
                    }}
                    />
                  ) : visibleGitHubTriggerStatuses.length === 0 ? (
                    <EmptyState
                      variant="inline"
                      icon={Github}
                      title="No GitHub reviewer connections"
                      description="Add a repository to make the 143 reviewer available from its pull requests."
                    action={
                      canManagePolicy
                        ? {
                            label: "Add your first repository",
                            onClick: openAddRepository,
                          }
                        : undefined
                    }
                    />
                ) : (
                  visibleGitHubTriggerStatuses.map((trigger) => {
                    const setupPending = setupGitHubTrigger.isPending && setupGitHubTrigger.variables === trigger.repository_id;
                    const deletePending = deleteGitHubTrigger.isPending && deleteGitHubTrigger.variables === trigger.repository_id;
                    return (
                      <GitHubTriggerPanel
                        key={trigger.repository_id}
                        repositoryName={trigger.repository_full_name ?? "Unknown repository"}
                        trigger={trigger}
                        isLoading={false}
                        errorMessage={null}
                        setupErrorMessage={setupGitHubTrigger.variables === trigger.repository_id ? apiErrorMessage(setupGitHubTrigger.error) : null}
                        setupPending={setupPending}
                        deletePending={deletePending}
                        canManage={canManagePolicy}
                        onSetup={() => setupGitHubTrigger.mutate(trigger.repository_id)}
                        onReconnect={openAddRepository}
                        onDelete={() => deleteGitHubTrigger.mutate(trigger.repository_id)}
                      />
                    );
                  })
                )}
                  {!githubTriggerStatusesQuery.isLoading && !githubTriggerStatusesQuery.isError && visibleGitHubTriggerStatuses.length > 0 ? (
                    <div className="flex flex-wrap items-center justify-between gap-3 border-t border-border pt-3">
                      <p className="text-xs tabular-nums text-muted-foreground">
                      {visibleGitHubTriggerStatuses.filter((trigger) => trigger.status === "ready").length} ready of {visibleGitHubTriggerStatuses.length} repositor
                      {visibleGitHubTriggerStatuses.length === 1 ? "y" : "ies"}
                      </p>
                      <Button asChild variant="link" size="sm">
                        <Link href="/settings/integrations">Manage all GitHub settings</Link>
                      </Button>
                    </div>
                  ) : null}
                </div>
              </SectionGroup>
            <AuditLogTrigger filters={{ resource_type: "code_review_policy" }} title="Review policy history" variant="footer" />
            </SectionGroup>
            <CodeReviewPromptExampleDialog
              selection={promptExample}
              currentConfig={config}
              currentDraftValue={promptExample ? promptDraftsRef.current[promptExample.field]?.value : undefined}
              persistedValue={promptExample ? persistedPromptsRef.current[promptExample.field] : undefined}
            onOpenChange={(open) => {
              if (!open) setPromptExample(null);
            }}
              onApply={() => {
                if (!promptExample) return;
                const value = "instructions" in promptExample.example ? promptExample.example.instructions : promptExample.example.policy;
                promptDraftsRef.current[promptExample.field]?.replace(value);
              commitPolicy((next) => {
                next[promptExample.field] = value;
              }, "example");
              trackCodeReviewPolicyEvent({
                event: "code_review_prompt_example_applied",
                scope: "organization",
                source: "example",
                example_key: promptExample.example.key,
                character_bucket: promptCharacterBucket(value),
                configured: true,
              });
                setPromptExample(null);
              }}
            />
            <GitHubReviewerConnectionSheet
              open={addRepositoryOpen}
              onOpenChange={changeAddRepositoryOpen}
              canManage={canManagePolicy}
              githubConnected={!!githubIntegration}
              githubStatusLoading={integrationsQuery.isLoading}
              githubStatusError={apiErrorMessage(integrationsQuery.error) ?? undefined}
              onRetryGithubStatus={() => void integrationsQuery.refetch()}
              installationId={githubIntegration?.github_installation_id}
              accountConnected={githubAccountQuery.data?.connected ?? false}
              accountNeedsReconnect={githubAccountQuery.data?.needs_reconnect ?? false}
              accountStatusLoading={githubAccountQuery.isLoading}
              accountStatusError={apiErrorMessage(githubAccountQuery.error) ?? undefined}
              onRetryAccountStatus={() => void githubAccountQuery.refetch()}
              triggerStatuses={githubTriggerStatuses}
            />
            <DescriptionRequirementSheet
              requirement={editingRequirement}
              canDelete={(config?.description_policy.requirements.length ?? 0) > 1}
              disabled={!config}
              autosave={autosave}
              buildConfig={buildConfig}
              open={Boolean(editingRequirement)}
              onOpenChange={(open) => {
                if (!open) setEditingRequirementKey(null);
              }}
              onCommit={(updater) => {
                if (!editingRequirementKey) return;
                commitRequirementByKey(editingRequirementKey, updater);
              }}
              onDelete={() => {
                if (!editingRequirementKey) return;
                const key = editingRequirementKey;
                commitPolicy((next) => {
                  if (next.description_policy.requirements.length <= 1) return;
                  next.description_policy.requirements = next.description_policy.requirements.filter((requirement) => requirement.key !== key);
                });
                setEditingRequirementKey(null);
              }}
            />
          </PageTabContent>
      </Tabs>
    </ListPage>
  );
}

type PromptDraftHandle = {
  value: string;
  dirty: boolean;
  flush(): void;
  replace(value: string): void;
};

function PolicyPromptComposer({
  field,
  config,
  autosave,
  commitPolicy,
  examples,
  examplesError,
  onRetryExamples,
  onChooseExample,
  onDraftHandle,
  invalidPolicyField,
}: {
  field: "review_instructions" | "automated_approval_policy";
  config: CodeReviewPolicyConfig | null;
  autosave: UseAutosaveResult<CodeReviewPolicyConfig>;
  commitPolicy: (mutate: (next: CodeReviewPolicyConfig) => void, source?: CodeReviewPolicyEditSource) => void;
  examples?: {
    review_instructions: CodeReviewPromptExampleOption[];
    automated_approval_policies: CodeReviewAutomatedApprovalExampleOption[];
  };
  examplesError?: string;
  onRetryExamples: () => void;
  onChooseExample: (field: "review_instructions" | "automated_approval_policy", example: CodeReviewPromptExampleOption | CodeReviewAutomatedApprovalExampleOption) => void;
  onDraftHandle: (field: "review_instructions" | "automated_approval_policy", handle: PromptDraftHandle) => void;
  invalidPolicyField: string | null;
}) {
  if (field === "automated_approval_policy") {
    return (
      <>
        {examplesError ? <ErrorNotice title="Could not load prompt examples" description={examplesError} action={{ label: "Retry", onClick: onRetryExamples }} /> : null}
        <CodeReviewAutomatedApprovalPolicyComposer
          value={config?.automated_approval_policy ?? ""}
          disabled={!config}
          inactive={Boolean(config && config.approval_mode !== "approve_acceptable")}
          autosave={autosave}
          onCommit={(value) => {
            commitPolicy((next) => {
              next.automated_approval_policy = value;
            });
            trackCodeReviewPolicyEvent({
              event: "code_review_prompt_edited",
              scope: "organization",
              source: "manual",
              character_bucket: promptCharacterBucket(value.trim()),
              configured: true,
            });
          }}
          resetValue={DEFAULT_AUTOMATED_APPROVAL_POLICY}
          onReset={(previousValue) => {
            const resetValue = DEFAULT_AUTOMATED_APPROVAL_POLICY;
            commitPolicy((next) => {
              next.automated_approval_policy = resetValue;
            }, "reset");
            trackCodeReviewPolicyEvent({
              event: "code_review_prompt_edited",
              scope: "organization",
              source: "reset",
              character_bucket: promptCharacterBucket(resetValue),
              configured: true,
            });
            toast.info("Recommended approval policy restored", {
              description: "The previous policy can be restored while you continue editing.",
              action: {
                label: "Undo",
                onClick: () => {
                  commitPolicy((next) => {
                    next.automated_approval_policy = previousValue;
                  });
                  trackCodeReviewPolicyEvent({
                    event: "code_review_prompt_edited",
                    scope: "organization",
                    source: "manual",
                    character_bucket: promptCharacterBucket(previousValue.trim()),
                    configured: true,
                  });
                },
              },
            });
          }}
          resetLabel="Restore recommended policy"
          examples={examples?.automated_approval_policies ?? []}
          onChooseExample={(example) => onChooseExample("automated_approval_policy", example)}
          onDraftHandle={(handle) => onDraftHandle("automated_approval_policy", handle)}
          focusOnError={invalidPolicyField === "automated_approval_policy"}
        />
      </>
    );
  }

  return (
    <>
      <CodeReviewInstructionsComposer
        value={config?.review_instructions ?? ""}
        disabled={!config}
        autosave={autosave}
        onCommit={(value) => {
          commitPolicy((next) => {
            next.review_instructions = value;
          });
          trackCodeReviewPolicyEvent({
            event: "code_review_prompt_edited",
            scope: "organization",
            source: "manual",
            character_bucket: promptCharacterBucket(value.trim()),
            configured: true,
          });
        }}
        resetValue=""
        onReset={(previousValue) => {
          const resetValue = "";
          commitPolicy((next) => {
            next.review_instructions = resetValue;
          }, "reset");
          trackCodeReviewPolicyEvent({
            event: "code_review_prompt_edited",
            scope: "organization",
            source: "reset",
            character_bucket: promptCharacterBucket(resetValue),
            configured: true,
          });
          toast.info("Additional instructions cleared", {
            description: "Reviewers will use their native review behavior.",
            action: {
              label: "Undo",
              onClick: () => {
                commitPolicy((next) => {
                  next.review_instructions = previousValue;
                });
                trackCodeReviewPolicyEvent({
                  event: "code_review_prompt_edited",
                  scope: "organization",
                  source: "manual",
                  character_bucket: promptCharacterBucket(previousValue.trim()),
                  configured: true,
                });
              },
            },
          });
        }}
        resetLabel="Clear instructions"
        examples={examples?.review_instructions ?? []}
        onChooseExample={(example) => onChooseExample("review_instructions", example)}
        onDraftHandle={(handle) => onDraftHandle("review_instructions", handle)}
        focusOnError={invalidPolicyField === "review_instructions"}
      />
    </>
  );
}

type CodeReviewPromptComposerProps = {
  value: string;
  disabled: boolean;
  inactive?: boolean;
  autosave: UseAutosaveResult<CodeReviewPolicyConfig>;
  onCommit: (value: string) => void;
  onReset: (previousValue: string) => void;
  resetValue: string;
  resetLabel: string;
  examples: Array<CodeReviewPromptExampleOption | CodeReviewAutomatedApprovalExampleOption>;
  onChooseExample: (example: CodeReviewPromptExampleOption | CodeReviewAutomatedApprovalExampleOption) => void;
  onDraftHandle: (handle: PromptDraftHandle) => void;
  focusOnError: boolean;
};

function CodeReviewAutomatedApprovalPolicyComposer(props: CodeReviewPromptComposerProps) {
  return (
    <CodeReviewPromptComposerBase
      {...props}
      title="Automated approval policy"
      description="Describe when 143 may approve a pull request. The safeguards below always take precedence."
      tooltip="Used only by the orchestrator when automatic approval is enabled. The backend derives the decision from explicit evidence, and the prompt cannot bypass hard safeguards. A non-empty value is required for automatic approval."
      required
    />
  );
}

function CodeReviewInstructionsComposer(props: CodeReviewPromptComposerProps) {
  return (
    <CodeReviewPromptComposerBase
      {...props}
      title="Additional review instructions (optional)"
      description="Add team-specific priorities or comment style. Empty means every reviewer uses its native /review behavior without extra guidance."
      tooltip="Optional guidance appended after each reviewer's native /review command and also supplied to the orchestrator. Leave empty for built-in review behavior; it does not grant approval authority."
      secondary
    />
  );
}

function CodeReviewPromptComposerBase({
  title,
  description,
  tooltip,
  value,
  disabled,
  inactive,
  required,
  autosave,
  onCommit,
  onReset,
  resetValue,
  resetLabel,
  secondary,
  examples,
  onChooseExample,
  onDraftHandle,
  focusOnError,
}: {
  title: string;
  description: string;
  tooltip: string;
  value: string;
  disabled: boolean;
  inactive?: boolean;
  required?: boolean;
  autosave: UseAutosaveResult<CodeReviewPolicyConfig>;
  onCommit: (value: string) => void;
  onReset: (previousValue: string) => void;
  resetValue: string;
  resetLabel: string;
  secondary?: boolean;
  examples: Array<CodeReviewPromptExampleOption | CodeReviewAutomatedApprovalExampleOption>;
  onChooseExample: (example: CodeReviewPromptExampleOption | CodeReviewAutomatedApprovalExampleOption) => void;
  onDraftHandle: (handle: PromptDraftHandle) => void;
  focusOnError: boolean;
}) {
  // Gate and count on the trimmed value: that is exactly what the backend
  // persists and validates, so basing the length check on the raw value would
  // reject content that fits the limit once trailing whitespace (e.g. a pasted
  // trailing newline) is stripped.
  const requiresValue = Boolean(required && !inactive && !disabled);
  const invalidValue = (next: string) => [...next.trim()].length > CODE_REVIEW_PROMPT_MAX_LENGTH || Boolean(requiresValue && !next.trim());
  const field = useDebouncedTextField({
    serverValue: value,
    onCommit: (next) => {
      if (!invalidValue(next)) onCommit(next);
    },
    debounceMs: CODE_REVIEW_TEXTAREA_DEBOUNCE_MS,
    preserveLocalOnServerChange: autosave.status === "error",
    valuesEqual: codeReviewPromptValuesEqual,
  });
  const textareaRef = useRef<HTMLTextAreaElement>(null);
  useEffect(() => {
    onDraftHandle({
      value: field.value,
      dirty: field.dirty,
      flush: field.flush,
      replace: field.replace,
    });
  }, [field.value, field.dirty, field.flush, field.replace, onDraftHandle]);
  useEffect(() => {
    if (focusOnError) textareaRef.current?.focus();
  }, [focusOnError]);
  const count = [...field.value.trim()].length;
  const invalid = count > CODE_REVIEW_PROMPT_MAX_LENGTH || Boolean(requiresValue && !field.value.trim());
  // A running count is only worth reading as the limit approaches; showing it
  // permanently puts a line of noise under every editor on the page.
  const showCount = invalid || count >= CODE_REVIEW_PROMPT_COUNT_VISIBLE_AT;
  const fieldId = `prompt-${title.replaceAll(" ", "-")}`;
  const countId = `prompt-count-${title.replaceAll(" ", "-")}`;
  const optionalEmpty = Boolean(secondary && !value.trim());
  const [optionalEditorOpen, setOptionalEditorOpen] = useState(false);
  // An invalid local draft has not reached the policy query yet. Keep it
  // visible so collapsing the optional editor cannot conceal the validation
  // error and make the unsaved draft easy to lose on navigation.
  const editorOpen = !optionalEmpty || invalid || focusOnError || optionalEditorOpen;
  const draftMatchesReset = codeReviewPromptValuesEqual(field.value, resetValue);
  const policyMatchesReset = codeReviewPromptValuesEqual(value, resetValue);
  const resetUnavailable = draftMatchesReset && policyMatchesReset;
  const resetDisabled = disabled || resetUnavailable;
  const resetDisabledReason = disabled
    ? "Policy settings are still loading."
    : secondary
      ? "There are no additional instructions to clear."
      : "This policy already uses the recommended instructions.";
  return (
    <Collapsible open={editorOpen} onOpenChange={setOptionalEditorOpen}>
      <section className="space-y-4 rounded-xl border border-border bg-card p-4 sm:p-5" aria-label={title}>
        <div className="flex items-start justify-between gap-4">
          <div className="min-w-0 space-y-1">
            <div className="flex items-center gap-1.5">
              <h3>
                <Label htmlFor={fieldId} className="font-display text-lg leading-6 font-semibold tracking-[-0.025em] text-foreground">
                  {title}
                </Label>
              </h3>
              <SettingInfoTooltip label={title} description={tooltip} />
            </div>
            <p className="max-w-2xl text-sm leading-6 text-muted-foreground">{description}</p>
            {inactive ? (
              <p className="max-w-2xl text-sm leading-6 text-muted-foreground">This policy is saved and ready, but it is only used when “Approve acceptable PRs” is selected above.</p>
            ) : null}
          </div>
          {optionalEmpty && !invalid ? (
            <CollapsibleTrigger asChild>
              <Button type="button" variant="outline" size="sm" disabled={disabled}>
                {editorOpen ? "Hide editor" : "Add instructions"}
                <ChevronDown className={cn("transition-transform", editorOpen && "rotate-180")} aria-hidden="true" />
              </Button>
            </CollapsibleTrigger>
          ) : null}
        </div>
        <CollapsibleContent className="space-y-3">
          <div
            className={cn(
              "overflow-hidden rounded-md border border-border-strong bg-surface-raised focus-within:border-ring focus-within:ring-2 focus-within:ring-ring/18",
              (invalid || focusOnError) && "border-destructive",
            )}
          >
            <Textarea
              ref={textareaRef}
              id={fieldId}
              className={`w-full rounded-none border-0 bg-transparent shadow-none focus-visible:border-transparent focus-visible:ring-0 ${secondary ? "min-h-32" : "min-h-72"} resize-y text-sm leading-6`}
              value={field.value}
              disabled={disabled}
              rows={secondary ? 5 : 12}
              onChange={(event) => field.onChange(event.target.value)}
              onBlur={field.onBlur}
              aria-invalid={invalid || focusOnError}
              aria-describedby={showCount ? countId : undefined}
            />
            <div className="flex min-h-10 w-full flex-wrap items-center justify-between gap-2 border-t border-border bg-muted/20 px-2 py-1">
              <span id={countId} className={`text-xs ${invalid ? "text-destructive" : "text-muted-foreground"}`}>
                {showCount ? `${count} / ${CODE_REVIEW_PROMPT_MAX_LENGTH}` : null}
              </span>
              <div className="flex flex-wrap items-center justify-end gap-1" role="group" aria-label={`${title} actions`}>
                <span aria-hidden="true">
                  <AutosaveIndicator status={autosave.status} />
                </span>
                {examples.length > 0 ? (
                  <Select
                    value=""
                    disabled={disabled}
                    onValueChange={(key) => {
                      const example = examples.find((candidate) => candidate.key === key);
                      if (example) onChooseExample(example);
                    }}
                  >
                    <SelectTrigger
                      density="compact"
                      className="w-auto min-w-0 border-0 bg-transparent px-2 text-xs text-muted-foreground shadow-none hover:bg-accent hover:text-accent-foreground"
                      aria-label={`${title} prompt example`}
                    >
                      <SelectValue placeholder="Use an example…" />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectGroup>
                        <SelectLabel>Prompt examples</SelectLabel>
                        {examples.map((example) => (
                          <SelectItem key={example.key} value={example.key}>
                            {example.title}
                          </SelectItem>
                        ))}
                      </SelectGroup>
                    </SelectContent>
                  </Select>
                ) : null}
                <DisabledTooltip disabled={resetDisabled} content={resetDisabledReason}>
                  <Button
                    type="button"
                    variant="ghost"
                    size="sm"
                    className="text-xs text-muted-foreground sm:h-8"
                    disabled={resetDisabled}
                    onClick={() => {
                      // Undo restores the policy snapshot, never an arbitrary
                      // local draft that may be empty or over the length limit.
                      const previousValue = value;
                      field.replace(resetValue);
                      onReset(previousValue);
                    }}
                  >
                    {resetLabel}
                  </Button>
                </DisabledTooltip>
              </div>
            </div>
          </div>
          {invalid ? (
            <p className="text-xs text-destructive">
              {count > CODE_REVIEW_PROMPT_MAX_LENGTH ? "Prompt is too long." : "An automated approval policy is required while approval is enabled."}
            </p>
          ) : null}
        </CollapsibleContent>
      </section>
    </Collapsible>
  );
}

function CodeReviewPromptExampleDialog({
  selection,
  currentConfig,
  currentDraftValue,
  persistedValue,
  onOpenChange,
  onApply,
}: {
  selection: {
    field: "review_instructions" | "automated_approval_policy";
    example: CodeReviewPromptExampleOption | CodeReviewAutomatedApprovalExampleOption;
  } | null;
  currentConfig: CodeReviewPolicyConfig | null;
  currentDraftValue?: string;
  persistedValue?: string;
  onOpenChange: (open: boolean) => void;
  onApply: () => void;
}) {
  if (!selection) return null;
  const value = "instructions" in selection.example ? selection.example.instructions : selection.example.policy;
  const dirty = currentDraftValue !== undefined && currentDraftValue !== (persistedValue ?? currentConfig?.[selection.field] ?? "");
  return (
    <Dialog open onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{selection.example.title}</DialogTitle>
          <DialogDescription>
            {selection.example.description} Only {selection.field === "review_instructions" ? "additional review instructions" : "the automated approval policy"} will be replaced.
          </DialogDescription>
        </DialogHeader>
    <div className="max-h-80 overflow-auto whitespace-pre-wrap rounded-md border border-border bg-muted/30 p-3 text-sm">{value}</div>
        {selection.field === "automated_approval_policy" && currentConfig?.approval_mode !== "approve_acceptable" ? (
          <p className="text-sm text-muted-foreground">This example does not enable automatic approval. Choose “Leave comments and approve when acceptable” separately.</p>
        ) : null}
        {dirty ? (
          <p className="text-sm text-warning">Your currently saved value differs. Applying this example replaces only this prompt field and creates a new policy version.</p>
        ) : null}
        <DialogFooter>
          <Button type="button" variant="outline" onClick={() => onOpenChange(false)}>
            Cancel
          </Button>
          <Button type="button" onClick={onApply}>
            Use example
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

type AdvancedPolicySettingsProps = {
  config: CodeReviewPolicyConfig | null;
  autosave: UseAutosaveResult<CodeReviewPolicyConfig>;
  buildConfig: (mutate: (next: CodeReviewPolicyConfig) => void) => CodeReviewPolicyConfig;
  commitPolicy: (mutate: (next: CodeReviewPolicyConfig) => void) => void;
  codeReviewModelGroups: AgentModelGroup[];
  codeReviewOpenCodeAvailability: Map<string, OpenCodeModelAvailability>;
  setEditingRequirementKey: (value: string) => void;
  invalidPolicyField: string | null;
  analyticsScope: "organization" | "repository";
};

function AdvancedPolicySettings({
  config,
  autosave,
  buildConfig,
  commitPolicy,
  codeReviewModelGroups,
  codeReviewOpenCodeAvailability,
  setEditingRequirementKey,
  invalidPolicyField,
  analyticsScope,
}: AdvancedPolicySettingsProps) {
  const [limitDeepLinkOpen, setLimitDeepLinkOpen] = useState(false);
  useEffect(() => {
    const revealLimitSetting = () => {
      const settingID = window.location.hash.slice(1);
      if (settingID !== "policy-max-files-changed" && settingID !== "policy-max-lines-changed") return;
      setLimitDeepLinkOpen(true);
      window.setTimeout(() => document.getElementById(settingID)?.scrollIntoView?.({ block: "center" }), 0);
    };
    revealLimitSetting();
    window.addEventListener("hashchange", revealLimitSetting);
    return () => window.removeEventListener("hashchange", revealLimitSetting);
  }, []);
  return (
    <AdvancedPolicyControls
      notice={
        invalidPolicyField ? (
          <ErrorNotice title="Could not save this policy setting" description={`Correct the highlighted ${invalidPolicyField.replaceAll("_", " ")} setting and try again.`} />
        ) : null
      }
    >
          <FineTuningSection
            title="Approval criteria"
            summary="Set limits for change size, review time, and reviewer agreement."
            forceOpen={limitDeepLinkOpen || invalidPolicyField === "risk_policy" || invalidPolicyField === "inline_comment_limit"}
            onOpened={() =>
              trackCodeReviewPolicyEvent({
                event: "code_review_advanced_opened",
                scope: analyticsScope,
                subsection: "approval_criteria",
                configured: true,
              })
            }
          >
                    <div className="grid gap-3 md:grid-cols-3">
              <div id="policy-max-files-changed" className="scroll-mt-24">
                      <NumberPolicyInput
                        label="Files changed"
                        serverValue={config?.risk_policy.max_files_changed}
                        min={1}
                        disabled={!config}
                        autosave={autosave}
                  buildPatch={(value) =>
                    buildConfig((next) => {
                      next.risk_policy.max_files_changed = value;
                    })
                  }
                      />
              </div>
              <div id="policy-max-lines-changed" className="scroll-mt-24">
                      <NumberPolicyInput
                        label="Lines changed"
                        serverValue={config?.risk_policy.max_lines_changed}
                        min={1}
                        disabled={!config}
                        autosave={autosave}
                  buildPatch={(value) =>
                    buildConfig((next) => {
                      next.risk_policy.max_lines_changed = value;
                    })
                  }
                      />
              </div>
                      <NumberPolicyInput
                        label="Inline comments"
                        serverValue={config?.inline_comment_limit}
                        min={1}
                        max={10}
                        disabled={!config}
                        autosave={autosave}
                buildPatch={(value) =>
                  buildConfig((next) => {
                    next.inline_comment_limit = value;
                  })
                }
                      />
                      <DurationInput
                        label="Reassessment cooldown"
                        labelAction={
                          <SettingInfoTooltip
                            label="Reassessment cooldown"
                            description="Deduplicates semantically unchanged reconsideration requests for the same pull request. It prevents duplicate work without limiting distinct objections."
                          />
                        }
                        valueSeconds={config?.risk_policy.semantic_dedupe_cooldown_seconds ?? 900}
                        minSeconds={60}
                        disabled={!config}
                        defaultUnit="minutes"
                        onChangeSeconds={(seconds) =>
                  autosave.save(
                    buildConfig((next) => {
                      next.risk_policy.semantic_dedupe_cooldown_seconds = Math.min(86400, seconds);
                    }),
                  )
                        }
                      />
                      <DurationInput
                        label="Timeout"
                        labelAction={
                          <SettingInfoTooltip
                            label="Timeout"
                            description="Maximum time reviewer agents may run before the review is treated as incomplete. The default remains active until changed, and a timeout prevents automatic approval when quorum cannot be reached."
                          />
                        }
                        valueSeconds={config?.agent_roster.timeout_seconds ?? 60}
                        minSeconds={60}
                        disabled={!config}
                        defaultUnit="minutes"
                        onChangeSeconds={(seconds) =>
                  autosave.save(
                    buildConfig((next) => {
                      next.agent_roster.timeout_seconds = seconds;
                    }),
                  )
                        }
                      />
                      <NumberPolicyInput
                        label="Reviewer quorum"
                        serverValue={config?.agent_roster.require_reviewer_quorum}
                        min={1}
                        max={Math.max(1, config?.agent_roster.reviewers.length ?? 1)}
                        disabled={!config}
                        autosave={autosave}
                buildPatch={(value) =>
                  buildConfig((next) => {
                    next.agent_roster.require_reviewer_quorum = value;
                  })
                }
                      />
                    </div>
                  </FineTuningSection>

          <FineTuningSection
            title="Quality gates"
            summary="Choose what a pull request must pass before approval."
            onOpened={() =>
              trackCodeReviewPolicyEvent({
                event: "code_review_advanced_opened",
                scope: analyticsScope,
                subsection: "quality_gates",
                configured: true,
              })
            }
          >
                    <div className="grid gap-x-6 gap-y-2 md:grid-cols-2">
                      <PolicyToggle
                        label="Require passing checks"
                        description={QUALITY_GATE_DESCRIPTIONS.requirePassingChecks}
                        checked={config?.risk_policy.require_passing_checks ?? false}
                        disabled={!config}
                onCheckedChange={(checked) =>
                  commitPolicy((next) => {
                    next.risk_policy.require_passing_checks = checked;
                  })
                }
                      />
                      <PolicyToggle
                        label="Enforce sensitive paths"
                        description={QUALITY_GATE_DESCRIPTIONS.excludeSensitivePaths}
                        checked={config?.risk_policy.exclude_sensitive_paths ?? false}
                        disabled={!config}
                onCheckedChange={(checked) =>
                  commitPolicy((next) => {
                    next.risk_policy.exclude_sensitive_paths = checked;
                  })
                }
                      />
                      <PolicyToggle
                        label="Require up-to-date branch"
                        description={QUALITY_GATE_DESCRIPTIONS.requireUpToDate}
                        checked={config?.risk_policy.require_up_to_date ?? false}
                        disabled={!config}
                onCheckedChange={(checked) =>
                  commitPolicy((next) => {
                    next.risk_policy.require_up_to_date = checked;
                  })
                }
                      />
                      <PolicyToggle
                        label="Block reviewer disagreement"
                        description={QUALITY_GATE_DESCRIPTIONS.disagreementBlocks}
                        checked={config?.agent_roster.disagreement_blocks ?? false}
                        disabled={!config}
                onCheckedChange={(checked) =>
                  commitPolicy((next) => {
                    next.agent_roster.disagreement_blocks = checked;
                  })
                }
                      />
                      <PolicyToggle
                        label="Allow fork PRs"
                        description={QUALITY_GATE_DESCRIPTIONS.allowForks}
                        checked={config?.risk_policy.allow_forks ?? false}
                        disabled={!config}
                onCheckedChange={(checked) =>
                  commitPolicy((next) => {
                    next.risk_policy.allow_forks = checked;
                  })
                        }
                      />
                      <PolicyToggle
                        label="Stop after stable policy blockers"
                        description={QUALITY_GATE_DESCRIPTIONS.stopAfterDeterministicFailure}
                        checked={config?.risk_policy.stop_after_deterministic_failure ?? false}
                        disabled={!config}
                        onCheckedChange={(checked) =>
                          commitPolicy((next) => {
                            next.risk_policy.stop_after_deterministic_failure = checked;
                          })
                        }
                      />
                    </div>
                  </FineTuningSection>

          <FineTuningSection
            title="Paths, authors & checks"
            summary="Choose which changes are eligible for approval."
            onOpened={() =>
              trackCodeReviewPolicyEvent({
                event: "code_review_advanced_opened",
                scope: analyticsScope,
                subsection: "paths_authors_checks",
                configured: true,
              })
            }
          >
                    <div className="grid gap-3 lg:grid-cols-2">
                      <PolicyStringListEditor
                        label="Sensitive paths"
                        description="Paths that should be treated as higher-risk changes."
                        placeholder="Add glob pattern, e.g. src/auth/**"
                        emptyText="No sensitive paths configured."
                        monospace
                        serverValue={config?.risk_policy.sensitive_paths ?? []}
                        disabled={!config}
                onCommitItems={(items) =>
                  commitPolicy((next) => {
                    next.risk_policy.sensitive_paths = items;
                  })
                }
                      />
                      <PolicyStringListEditor
                        label="Allowed path patterns"
                        description="When set, only matching paths are eligible for automated approval."
                        placeholder="Add allowed glob pattern"
                        emptyText="No allowlist configured. All paths are eligible unless blocked."
                        monospace
                        serverValue={config?.risk_policy.allowed_path_patterns ?? []}
                        disabled={!config}
                onCommitItems={(items) =>
                  commitPolicy((next) => {
                    next.risk_policy.allowed_path_patterns = items;
                  })
                }
                      />
                      <PolicyStringListEditor
                        label="Blocked path patterns"
                        description="Matching paths prevent automated approval."
                        placeholder="Add blocked glob pattern"
                        emptyText="No blocked paths configured."
                        monospace
                        serverValue={config?.risk_policy.blocked_path_patterns ?? []}
                        disabled={!config}
                onCommitItems={(items) =>
                  commitPolicy((next) => {
                    next.risk_policy.blocked_path_patterns = items;
                  })
                }
                      />
                      <PolicyStringListEditor
                        label="Required checks"
                        description="Check names that must pass before approval."
                        placeholder="Add required check"
                        emptyText="No required checks configured."
                        monospace
                        serverValue={config?.risk_policy.required_checks ?? []}
                        disabled={!config}
                onCommitItems={(items) =>
                  commitPolicy((next) => {
                    next.risk_policy.required_checks = items;
                  })
                }
                      />
                      <PolicyStringListEditor
                        label="Eligible authors"
                        description="Authors allowed by this policy. Leave empty to allow any author."
                        placeholder="Add GitHub handle or author"
                        emptyText="Any author is eligible."
                        serverValue={config?.risk_policy.eligible_authors ?? []}
                        disabled={!config}
                onCommitItems={(items) =>
                  commitPolicy((next) => {
                    next.risk_policy.eligible_authors = items;
                  })
                }
                      />
                    </div>
                  </FineTuningSection>

          <FineTuningSection
            title="Reviewers & agents"
            summary="Choose who reviews the code and weighs the evidence."
            forceOpen={invalidPolicyField === "agent_roster"}
            onOpened={() =>
              trackCodeReviewPolicyEvent({
                event: "code_review_advanced_opened",
                scope: analyticsScope,
                subsection: "reviewers_agents",
                configured: true,
              })
            }
          >
                    <AgentRosterControls
                      config={config}
                      disabled={!config}
                      modelGroups={codeReviewModelGroups}
                      openCodeAvailability={codeReviewOpenCodeAvailability}
                      commitPolicy={commitPolicy}
                    />
                  </FineTuningSection>

          <FineTuningSection
            title="Structured PR-description checks"
            summary="Define what a pull request description must include."
            forceOpen={invalidPolicyField === "description_policy"}
            onOpened={() =>
              trackCodeReviewPolicyEvent({
                event: "code_review_advanced_opened",
                scope: analyticsScope,
                subsection: "structured_description_checks",
                configured: true,
              })
            }
          >
                    <DescriptionRequirementsList
                      requirements={config?.description_policy.requirements ?? []}
                      disabled={!config}
                      onEdit={setEditingRequirementKey}
                      onAdd={() => {
                        const key = `custom_${Date.now()}`;
                        commitPolicy((next) => {
                          next.description_policy.requirements.push({
                            key,
                            title: "Custom requirement",
                            prompt: "",
                            required: true,
                            applies_when: { kind: "all" },
                          });
                        });
                        setEditingRequirementKey(key);
                      }}
                    />
                  </FineTuningSection>
    </AdvancedPolicyControls>
  );
}

function AdvancedPolicyControls({ children, notice }: { children: ReactNode; notice?: ReactNode }) {
  return (
    // Standard bordered policy card, same as the sibling sections, with the help
    // icon in the header's action slot.
    <SectionGroup
      title="Safeguards"
      description="Reviewer setup and the rules that gate automatic approval."
      variant="bordered"
      headingLevel={3}
      action={
        <SettingInfoTooltip
          label="Safeguards"
          description="Reviewer models, limits, and the deterministic rules that are always enforced. These rules can require human review even when the reviewers recommend approval."
        />
      }
    >
      {/* Save errors stay outside the divided list: an ErrorNotice is a bordered
          card of its own, so a divider hairline against its edge reads as a seam. */}
      {notice}
      <div className="divide-y divide-border border-y border-border">{children}</div>
    </SectionGroup>
  );
}

function policyOutcome(config: CodeReviewPolicyConfig | null): "disabled" | "comment" | "approve" {
  if (!config?.enabled) return "disabled";
  return config.approval_mode === "approve_acceptable" ? "approve" : "comment";
}

function PolicySummary({ config }: { config: CodeReviewPolicyConfig | null }) {
  if (!config) {
    return <p className="text-sm text-muted-foreground">Loading review policy...</p>;
  }

  const outcome = policyOutcome(config);
  const reviewers = config.agent_roster.reviewers.length;
  const quorum = config.agent_roster.require_reviewer_quorum;
  let summary: string;
  if (outcome === "disabled") {
    summary = "Reviews are paused. The selected outcome and policy are preserved.";
  } else if (outcome === "comment") {
    summary = `Reviews use ${reviewers} ${reviewers === 1 ? "reviewer" : "reviewers"} with a quorum of ${quorum} and leave comments without approving.`;
  } else {
    const approvalRequirements = [`Approval requires ${quorum} of ${reviewers} ${reviewers === 1 ? "reviewer" : "reviewers"}.`];
    if (config.risk_policy.require_passing_checks) approvalRequirements.push("Checks must pass.");
    const humanReviewReasons = [
      ...(config.agent_roster.disagreement_blocks ? ["reviewer disagreement"] : []),
      ...(config.risk_policy.exclude_sensitive_paths ? ["sensitive-path changes"] : []),
    ];
    if (humanReviewReasons.length > 0) {
      approvalRequirements.push(`${humanReviewReasons.join(" and ")} ${humanReviewReasons.length === 1 ? "requires" : "require"} human review.`);
    }
    summary = approvalRequirements.join(" ");
  }

  return (
    <div className="rounded-md bg-surface-recessed px-3 py-2.5 text-sm leading-6 text-muted-foreground">
      <span className="font-medium text-foreground">Effective policy:</span> {summary}
    </div>
  );
}

function OutcomeControl({
  config,
  disabled,
  onChange,
}: {
  config: CodeReviewPolicyConfig | null;
  disabled?: boolean;
  onChange: (outcome: "disabled" | "comment" | "approve") => void;
}) {
  const selected = policyOutcome(config);
  const options: Array<{
    value: "comment" | "approve";
    title: string;
    description: string;
  }> = [
    {
      value: "comment",
      title: "Comment only",
      description: "The bot reviews PRs and leaves feedback without approving.",
    },
    {
      value: "approve",
      title: "Approve acceptable PRs",
      description: "The bot can approve when the PR passes this policy.",
    },
  ];

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between gap-4">
        <div>
          <div className="flex items-center gap-1.5">
            <Label htmlFor="code-reviews-enabled" className="text-sm text-foreground">
              Code reviews enabled
            </Label>
            <SettingInfoTooltip
              label="Code reviews enabled"
              description="Controls whether GitHub reviewer requests start review sessions. The built-in default is on. Turning it off pauses new reviews and preserves the selected outcome for re-enablement."
            />
          </div>
          <p className="mt-1 text-xs text-muted-foreground">Turn off to pause new review sessions without changing the selected outcome.</p>
        </div>
        <Switch
          id="code-reviews-enabled"
          checked={selected !== "disabled"}
          disabled={disabled}
          onCheckedChange={(checked) => onChange(checked ? (config?.approval_mode === "approve_acceptable" ? "approve" : "comment") : "disabled")}
        />
      </div>
      <div className="flex items-center gap-1.5 text-sm font-medium text-foreground">
        Review outcome
        <SettingInfoTooltip
          label="Review outcome"
          description="Chooses whether enabled reviews only leave comments or may approve an acceptable pull request. The built-in default is comment-only, and every deterministic safeguard retains veto power."
        />
      </div>
      <RadioGroup
        value={config?.approval_mode === "approve_acceptable" ? "approve" : "comment"}
        disabled={disabled || selected === "disabled"}
        aria-label="Review outcome"
        className="grid gap-3 md:grid-cols-2"
        onValueChange={(value) => onChange(value as "comment" | "approve")}
      >
        {options.map((option) => (
          <Label
            key={option.value}
            className="flex cursor-pointer items-start gap-3 rounded-md border border-border p-3 transition-colors hover:bg-muted/40 has-[[data-state=checked]]:border-primary has-[[data-state=checked]]:bg-primary/5"
          >
            <RadioGroupItem value={option.value} aria-label={option.title} className="mt-0.5" />
            <span className="flex min-w-0 flex-col gap-1">
              <span className="text-sm font-medium text-foreground">{option.title}</span>
              <span className="text-xs font-normal leading-5 text-muted-foreground">{option.description}</span>
            </span>
          </Label>
        ))}
      </RadioGroup>
      {config?.approval_mode === "approve_acceptable" ? (
        <p className="text-xs text-muted-foreground">Automatic approval is eligible only when all hard safeguards pass; uncertain or blocked changes still require a human.</p>
      ) : null}
    </div>
  );
}

function GitHubTriggerPanel({
  repositoryName,
  trigger,
  isLoading,
  errorMessage,
  setupErrorMessage,
  setupPending,
  deletePending,
  canManage,
  onSetup,
  onReconnect,
  onDelete,
}: {
  repositoryName: string;
  trigger?: CodeReviewGitHubTriggerResponse;
  isLoading: boolean;
  errorMessage: string | null;
  setupErrorMessage: string | null;
  setupPending: boolean;
  deletePending: boolean;
  canManage: boolean;
  onSetup: () => void;
  onReconnect: () => void;
  onDelete: () => void;
}) {
  const status = trigger?.status ?? "unconfigured";
  const ready = status === "ready";
  const authRequired = status === "auth_required";
  const permissionRequired = status === "permission_required";
  const disconnected = status === "disconnected";
  const needsRepair = status === "error" || permissionRequired;
  const reviewer = trigger?.team_reviewer ?? "@org/143-code-reviewer";
  const setupDisabledReason = githubTriggerSetupDisabledReason({
    canManage,
    authRequired,
    setupPending,
    deletePending,
    isLoading,
  });

  return (
    <div className="rounded-md border border-border p-4" role="region" aria-label={`${repositoryName} GitHub reviewer`}>
      <div className="flex flex-col gap-3 lg:flex-row lg:items-start lg:justify-between">
        <div className="min-w-0">
          <div className="flex flex-wrap items-center gap-2">
            <div className="text-sm font-medium text-foreground">{repositoryName}</div>
            <SettingInfoTooltip
              label={`${repositoryName} GitHub reviewer`}
              description="Adds a repository reviewer in GitHub that starts 143 reviews when requested. When unconfigured, no GitHub reviewer request can start a review; organization policy remains stored but inactive for that repository trigger."
            />
            <Badge variant={githubTriggerStatusVariant(status)}>{isLoading ? "Checking" : githubTriggerStatusLabel(status)}</Badge>
            {ready ? <span className="text-xs font-medium text-foreground">{reviewer}</span> : null}
          </div>
          <div className="mt-1 text-xs text-muted-foreground">People select this team from GitHub&apos;s Reviewers menu on a PR to start a 143 code review.</div>
          {trigger?.message ? (
            <div className="mt-3 flex items-start gap-2 text-xs text-muted-foreground">
              <AlertTriangle className="mt-0.5 h-3.5 w-3.5 shrink-0" />
              <span>{trigger.message}</span>
            </div>
          ) : null}
          {errorMessage || setupErrorMessage ? (
            <div className="mt-3 flex items-start gap-2 text-xs text-destructive">
              <AlertTriangle className="mt-0.5 h-3.5 w-3.5 shrink-0" />
              <span>{setupErrorMessage ?? errorMessage}</span>
            </div>
          ) : null}
        </div>
        <div className="flex shrink-0 flex-wrap gap-2">
          {authRequired ? (
            <Button variant="outline" size="sm" disabled={!canManage} onClick={() => api.githubStatus.connect(undefined, "/code-reviews?tab=policy&add_repository=1")}>
              <Users className="h-4 w-4" />
              Connect GitHub
            </Button>
          ) : null}
          {!ready ? (
            <DisabledTooltip disabled={!!setupDisabledReason} content={setupDisabledReason}>
              <Button variant="default" size="sm" disabled={!!setupDisabledReason} onClick={disconnected ? onReconnect : onSetup}>
                <Users className="h-4 w-4" />
                {disconnected ? "Reconnect repository" : needsRepair ? "Repair GitHub reviewer" : "Set up GitHub reviewer"}
              </Button>
            </DisabledTooltip>
          ) : null}
          {ready ? (
            <GitHubReviewerManage reviewer={reviewer} teamSlug={trigger?.team_slug} deleteDisabled={!canManage || setupPending || deletePending} onDelete={onDelete} />
          ) : null}
          {permissionRequired ? <Badge variant="destructive">Permission approval needed</Badge> : null}
        </div>
        </div>
      </div>
  );
}

function GitHubReviewerManage({ reviewer, teamSlug, deleteDisabled, onDelete }: { reviewer: string; teamSlug?: string; deleteDisabled: boolean; onDelete: () => void }) {
  return (
    <Collapsible>
      <CollapsibleTrigger asChild>
        <Button variant="outline" size="sm">
          Manage
        </Button>
      </CollapsibleTrigger>
      <CollapsibleContent className="mt-2 w-[min(18rem,calc(100vw-2rem))] space-y-3 rounded-md border border-border bg-card p-3 text-xs">
        <div>
          <span className="text-muted-foreground">Reviewer</span>
          <div className="font-medium text-foreground">{reviewer}</div>
        </div>
        <div>
          <span className="text-muted-foreground">Team slug</span>
          <div className="font-medium text-foreground">{teamSlug ?? "143-code-reviewer"}</div>
        </div>
        <div>
          <span className="text-muted-foreground">Repository access</span>
          <div className="font-medium text-foreground">Read</div>
    </div>
        <Button variant="ghost" size="sm" className="text-destructive hover:text-destructive" disabled={deleteDisabled} onClick={onDelete}>
          <PowerOff className="h-4 w-4" />
          Disable reviewer
        </Button>
      </CollapsibleContent>
    </Collapsible>
  );
}

function githubTriggerSetupDisabledReason({
  canManage,
  authRequired,
  setupPending,
  deletePending,
  isLoading,
}: {
  canManage: boolean;
  authRequired: boolean;
  setupPending: boolean;
  deletePending: boolean;
  isLoading: boolean;
}): string | undefined {
  if (!canManage) {
    return "Only organization administrators can configure the GitHub reviewer menu option.";
  }
  if (authRequired) {
    return "Connect your GitHub account first so 143 can set up the GitHub reviewer menu option.";
  }
  if (setupPending) {
    return "GitHub reviewer setup is already running. Wait for it to finish before trying again.";
  }
  if (deletePending) {
    return "The GitHub reviewer menu option is being disabled. Wait for that action to finish before repairing it.";
  }
  if (isLoading) {
    return "143 is checking the repository's GitHub reviewer menu option. Wait for the check to finish.";
  }
  return undefined;
}

function githubTriggerStatusLabel(status: CodeReviewGitHubTriggerResponse["status"]): string {
  switch (status) {
    case "ready":
      return "Ready";
    case "auth_required":
      return "Needs GitHub account";
    case "permission_required":
      return "Needs app permissions";
    case "disconnected":
      return "Repository disconnected";
    case "error":
      return "Needs attention";
    default:
      return "Not configured";
  }
}

function githubTriggerStatusVariant(status: CodeReviewGitHubTriggerResponse["status"]): "success" | "secondary" | "destructive" | "outline" {
  if (status === "ready") return "success";
  if (status === "permission_required" || status === "disconnected" || status === "error") return "destructive";
  if (status === "auth_required") return "secondary";
  return "outline";
}

function requirementKind(requirement: DescriptionRequirement): CodeReviewDescriptionApplicabilityKind {
  return (requirement.applies_when?.kind || requirement.applicability || "all") as CodeReviewDescriptionApplicabilityKind;
}

function summarizeItems(items: string[] | undefined, emptyLabel: string): string {
  if (!items?.length) return emptyLabel;
  const visible = items.slice(0, 2).join(", ");
  const hiddenCount = items.length - 2;
  return hiddenCount > 0 ? `${visible} + ${hiddenCount} more` : visible;
}

function formatRequirementApplicability(requirement: DescriptionRequirement): string {
  const appliesWhen = requirement.applies_when;
  switch (requirementKind(requirement)) {
    case "nontrivial": {
      const minFiles = appliesWhen?.min_files_changed ?? DEFAULT_NONTRIVIAL_MIN_FILES;
      const minLines = appliesWhen?.min_lines_changed ?? DEFAULT_NONTRIVIAL_MIN_LINES;
      return `Nontrivial: ${minFiles}+ files or ${minLines}+ lines`;
    }
    case "paths":
      return `Paths: ${summarizeItems(appliesWhen?.path_patterns, "no paths set")}`;
    default:
      return "Every PR";
  }
}

function appliesWhenForKind(kind: CodeReviewDescriptionApplicabilityKind, previous?: DescriptionApplicability): DescriptionApplicability {
  switch (kind) {
    case "nontrivial":
      return {
        kind,
        min_files_changed: previous?.min_files_changed ?? DEFAULT_NONTRIVIAL_MIN_FILES,
        min_lines_changed: previous?.min_lines_changed ?? DEFAULT_NONTRIVIAL_MIN_LINES,
      };
    case "paths":
      return {
        kind,
        path_patterns: previous?.path_patterns ?? [],
      };
    default:
      return { kind: "all" };
  }
}

function DescriptionRequirementsList({
  requirements,
  disabled,
  onEdit,
  onAdd,
}: {
  requirements: DescriptionRequirement[];
  disabled?: boolean;
  onEdit: (key: string) => void;
  onAdd: () => void;
}) {
  return (
    <div className="space-y-3">
      <div className="flex items-center justify-between gap-3">
        <div>
          <div className="text-sm font-medium text-foreground">Requirements</div>
          <div className="mt-1 text-xs text-muted-foreground">143 checks these items in the pull request description before approving.</div>
        </div>
        <div className="flex items-center gap-1.5">
        <Button variant="outline" size="sm" disabled={disabled} onClick={onAdd}>
          <Plus className="h-4 w-4" />
          Add requirement
        </Button>
          <SettingInfoTooltip
            label="Add structured PR-description check"
            description="Adds another deterministic PR-description check. A required check can block automatic approval when its requested evidence is missing."
          />
        </div>
      </div>
      <div className="overflow-x-auto rounded-md border border-border">
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead className="w-24">Required</TableHead>
              <TableHead>Requirement</TableHead>
              <TableHead>Applies to</TableHead>
              <TableHead className="w-24 text-right">Action</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {requirements.map((requirement) => (
              <TableRow key={requirement.key}>
                <TableCell>
                  <Badge variant={requirement.required ? "success" : "outline"}>{requirement.required ? "On" : "Off"}</Badge>
                </TableCell>
                <TableCell>
                  <div className="font-medium text-foreground">{requirement.title || "Untitled requirement"}</div>
                  {requirement.prompt ? <div className="mt-1 line-clamp-1 text-xs text-muted-foreground">{requirement.prompt}</div> : null}
                </TableCell>
                <TableCell className="text-sm text-muted-foreground">{formatRequirementApplicability(requirement)}</TableCell>
                <TableCell>
                  <div className="flex justify-end">
                    <Button variant="ghost" size="sm" disabled={disabled} aria-label={`Edit ${requirement.title || "requirement"}`} onClick={() => onEdit(requirement.key)}>
                      Edit
                    </Button>
                  </div>
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </div>
    </div>
  );
}

function DescriptionRequirementSheet({
  requirement,
  canDelete,
  disabled,
  autosave,
  buildConfig,
  open,
  onOpenChange,
  onCommit,
  onDelete,
}: {
  requirement: DescriptionRequirement | null;
  canDelete: boolean;
  disabled?: boolean;
  autosave: UseAutosaveResult<CodeReviewPolicyConfig>;
  buildConfig: (mutate: (next: CodeReviewPolicyConfig) => void) => CodeReviewPolicyConfig;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onCommit: (updater: (requirement: DescriptionRequirement) => DescriptionRequirement) => void;
  onDelete: () => void;
}) {
  const kind = requirement ? requirementKind(requirement) : "all";

  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent className="w-full sm:max-w-xl">
        <SheetHeader>
          <SheetTitle>Edit structured PR-description check</SheetTitle>
          <SheetDescription>Configure when this PR description requirement applies and what the reviewer checks.</SheetDescription>
        </SheetHeader>
        {requirement ? (
          <div className="mt-6 space-y-6">
            <div className="space-y-2">
              <SettingLabel
                label="Title"
                info="Names this structured PR-description check. The title is shown in policy summaries and review evidence; leaving it blank makes the check harder to identify."
              />
              <PolicyTextInput
                serverValue={requirement.title}
                disabled={disabled}
                aria-label="Requirement title"
                onCommit={(value) => onCommit((current) => ({ ...current, title: value }))}
              />
            </div>

            <div className="flex items-center justify-between gap-3 rounded-md border border-border px-3 py-2">
              <div>
                <div className="flex items-center gap-1.5">
                <Label className="text-sm text-foreground">Required</Label>
                  <SettingInfoTooltip
                    label="Required description check"
                    description="When on, missing evidence for this check prevents automatic approval. When off, the check remains advisory."
                  />
                </div>
                <div className="mt-1 text-xs text-muted-foreground">Blocks approval when this item is missing.</div>
              </div>
              <Switch
                aria-label="Required description check"
                checked={requirement.required}
                disabled={disabled}
                onCheckedChange={(checked) => onCommit((current) => ({ ...current, required: checked }))}
              />
            </div>

            <div className="space-y-2">
              <SettingLabel label="Applies to" info="Controls which pull requests receive this structured description check. The default applies it to every pull request." />
              <Select
                value={kind}
                disabled={disabled}
                onValueChange={(value) =>
                  onCommit((current) => ({
                    ...current,
                    applicability: value,
                    applies_when: appliesWhenForKind(value as CodeReviewDescriptionApplicabilityKind, current.applies_when),
                  }))
                }
              >
                <SelectTrigger aria-label="Requirement applicability">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {Object.entries(APPLICABILITY_KIND_LABELS).map(([value, label]) => (
                    <SelectItem key={value} value={value}>
                      {label}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>

            {kind === "nontrivial" ? (
              <div className="grid gap-3 sm:grid-cols-2">
                <NumberPolicyInput
                  label="Files changed at least"
                  serverValue={requirement.applies_when?.min_files_changed ?? DEFAULT_NONTRIVIAL_MIN_FILES}
                  min={0}
                  disabled={disabled}
                  autosave={autosave}
                  buildPatch={(value) =>
                    buildConfig((next) => {
                      const req = next.description_policy.requirements.find((item) => item.key === requirement.key);
                      if (!req) return;
                      req.applies_when = {
                        ...appliesWhenForKind("nontrivial", req.applies_when),
                        min_files_changed: value,
                      };
                    })
                  }
                />
                <NumberPolicyInput
                  label="Lines changed at least"
                  serverValue={requirement.applies_when?.min_lines_changed ?? DEFAULT_NONTRIVIAL_MIN_LINES}
                  min={0}
                  disabled={disabled}
                  autosave={autosave}
                  buildPatch={(value) =>
                    buildConfig((next) => {
                      const req = next.description_policy.requirements.find((item) => item.key === requirement.key);
                      if (!req) return;
                      req.applies_when = {
                        ...appliesWhenForKind("nontrivial", req.applies_when),
                        min_lines_changed: value,
                      };
                    })
                  }
                />
              </div>
            ) : null}

            {kind === "paths" ? (
              <ListTextArea
                label="Path patterns"
                serverValue={requirement.applies_when?.path_patterns ?? []}
                disabled={disabled}
                onCommitItems={(items) =>
                  onCommit((current) => ({
                    ...current,
                    applies_when: { kind, path_patterns: items },
                  }))
                }
              />
            ) : null}

            <div className="space-y-2">
              <div className="flex items-center gap-1.5">
                <Label className="text-xs text-muted-foreground">Description check instruction</Label>
                <SettingInfoTooltip
                  label="Description check instruction"
                  description="Guides only this structured PR-description check. Leave empty to use the check title and applicability without extra instructions; it never changes general reviewer behavior or bypasses safeguards."
                />
              </div>
              <PolicyTextarea
                serverValue={requirement.prompt}
                disabled={disabled}
                rows={5}
                aria-label="Description check instruction"
                onCommit={(value) => onCommit((current) => ({ ...current, prompt: value }))}
              />
            </div>

            <div className="border-t border-border pt-4">
              <div className="flex items-center gap-1.5">
                <Button variant="ghost" size="sm" className="text-destructive hover:text-destructive" disabled={disabled || !canDelete} onClick={onDelete}>
                <Trash2 className="h-4 w-4" />
                Delete requirement
              </Button>
                <SettingInfoTooltip
                  label="Delete structured PR-description check"
                  description="Removes this check from the next policy version. Other structured checks and safeguards are unchanged."
                />
              </div>
            </div>
          </div>
        ) : null}
      </SheetContent>
    </Sheet>
  );
}

function AgentRosterControls({
  config,
  disabled,
  modelGroups,
  openCodeAvailability,
  commitPolicy,
}: {
  config: CodeReviewPolicyConfig | null;
  disabled?: boolean;
  modelGroups: AgentModelGroup[];
  openCodeAvailability?: Map<string, OpenCodeModelAvailability>;
  commitPolicy: (mutate: (next: CodeReviewPolicyConfig) => void) => void;
}) {
  const reviewers = config?.agent_roster.reviewers ?? [];
  const reviewerModels = config ? ensureReviewerModels(config, modelGroups) : [];
  const reviewerReasoningEfforts = config ? ensureReviewerReasoningEfforts(config) : [];
  const canAddReviewer = Boolean(config) && reviewers.length < MAX_REVIEWER_MODELS && modelGroups.length > 0;
  const fallbackGroup = modelGroups[0];
  const orchestratorModel =
    config?.agent_roster.orchestrator_model && modelBelongsToAgent(config.agent_roster.orchestrator, config.agent_roster.orchestrator_model)
      ? config.agent_roster.orchestrator_model
      : defaultModelForAgent(config?.agent_roster.orchestrator ?? "", modelGroups);

  return (
    <div className="space-y-5">
      <div className="space-y-3">
        <div className="grid items-end gap-3 sm:grid-cols-[minmax(0,1fr)_minmax(10rem,14rem)_auto]">
          <div>
            <SettingLabel
              label="Reviewer models"
              info="Selects one to three agents that independently review each pull request. At least one is required; the built-in roster remains until changed, and removing one may lower the maximum valid quorum."
            />
            <p className="mt-1 text-xs text-muted-foreground">Run one to three independent reviewers. Quorum stays in Approval criteria.</p>
          </div>
          <SettingLabel
            label="Reasoning level"
            info="Sets reasoning independently for each reviewer. Available levels follow the agent selected in that row; High is the default."
          />
          <div className="flex items-center gap-1.5">
            <Button
              type="button"
              size="sm"
              variant="outline"
              disabled={!canAddReviewer}
              onClick={() =>
                commitPolicy((next) => {
                  if (!fallbackGroup || next.agent_roster.reviewers.length >= MAX_REVIEWER_MODELS) return;
                  const reviewerModels = ensureReviewerModels(next, modelGroups);
                  const reasoningEfforts = ensureReviewerReasoningEfforts(next);
                  next.agent_roster.reviewers = [...next.agent_roster.reviewers, fallbackGroup.key];
                  next.agent_roster.reviewer_models = [...reviewerModels, fallbackGroup.models[0] ?? ""];
                  next.agent_roster.reviewer_reasoning_efforts = [...reasoningEfforts, normalizeReasoningEffortForAgent(fallbackGroup.key, "high")];
                })
              }
            >
              <Plus className="mr-2 h-4 w-4" />
              Add
            </Button>
            <SettingInfoTooltip
              label="Add reviewer model"
              description="Adds another independent reviewer agent, up to three. Automatic approval still requires the configured reviewer quorum."
            />
          </div>
        </div>

        <div className="space-y-2">
          {reviewers.map((agent, index) => (
            <div key={`${agent}-${index}`} className="grid gap-2 rounded-md border border-border p-3 sm:grid-cols-[minmax(0,1fr)_minmax(10rem,14rem)_auto]">
              <AgentModelSelect
                ariaLabel={`Reviewer ${index + 1} model`}
                infoDescription="Chooses the agent and model for this independent review slot. Each slot must have a selection; the current resolved default remains until changed and contributes to quorum and disagreement handling."
                value={selectionValue(agent, reviewerModels[index] ?? defaultModelForAgent(agent, modelGroups))}
                modelGroups={modelGroups}
                openCodeAvailability={openCodeAvailability}
                currentAgent={agent}
                currentModel={reviewerModels[index]}
                disabled={disabled}
                onValueChange={(value) =>
                  commitPolicy((next) => {
                    const selection = parseSelectionValue(value);
                    const reviewerModels = ensureReviewerModels(next, modelGroups);
                    const reasoningEfforts = ensureReviewerReasoningEfforts(next);
                    next.agent_roster.reviewers[index] = selection.agent;
                    reviewerModels[index] = selection.model;
                    reasoningEfforts[index] = normalizeReasoningEffortForAgent(selection.agent, reasoningEfforts[index]);
                    next.agent_roster.reviewer_models = reviewerModels;
                    next.agent_roster.reviewer_reasoning_efforts = reasoningEfforts;
                  })
                }
              />
              <ReasoningEffortSelect
                ariaLabel={`Reviewer ${index + 1} reasoning level`}
                agent={agent}
                value={reviewerReasoningEfforts[index] ?? "high"}
                disabled={disabled}
                infoDescription="Sets the reasoning effort for this reviewer only. The available levels depend on the agent selected in this row."
                onValueChange={(value) =>
                  commitPolicy((next) => {
                    const reasoningEfforts = ensureReviewerReasoningEfforts(next);
                    reasoningEfforts[index] = value;
                    next.agent_roster.reviewer_reasoning_efforts = reasoningEfforts;
                  })
                }
              />
              <Button
                type="button"
                size="icon"
                variant="ghost"
                aria-label={`Remove reviewer ${index + 1}`}
                disabled={disabled || reviewers.length <= 1}
                onClick={() =>
                  commitPolicy((next) => {
                    const reviewerModels = ensureReviewerModels(next, modelGroups);
                    const reasoningEfforts = ensureReviewerReasoningEfforts(next);
                    next.agent_roster.reviewers = next.agent_roster.reviewers.filter((_, i) => i !== index);
                    next.agent_roster.reviewer_models = reviewerModels.filter((_, i) => i !== index);
                    next.agent_roster.reviewer_reasoning_efforts = reasoningEfforts.filter((_, i) => i !== index);
                    next.agent_roster.require_reviewer_quorum = Math.min(next.agent_roster.require_reviewer_quorum, Math.max(1, next.agent_roster.reviewers.length));
                  })
                }
              >
                <Trash2 className="h-4 w-4" />
              </Button>
            </div>
          ))}
        </div>
      </div>

      <div className="space-y-3 border-t border-border pt-4">
        <div className="grid items-end gap-2 sm:grid-cols-[minmax(0,1fr)_minmax(10rem,14rem)]">
          <div>
            <Label className="text-xs text-muted-foreground">Orchestrator model</Label>
            <p className="mt-1 text-xs text-muted-foreground">Synthesizes structured evidence; the backend applies severity rules and safeguards to decide the outcome.</p>
          </div>
          <Label className="text-xs text-muted-foreground">Reasoning level</Label>
        </div>
        <div className="grid gap-2 sm:grid-cols-[minmax(0,1fr)_minmax(10rem,14rem)]">
          <AgentModelSelect
            ariaLabel="Orchestrator model"
            infoDescription="Chooses the agent and model that combines reviewer evidence into findings and explicit human-review reasons. A selection is required; the backend applies severity rules and every deterministic safeguard."
            value={selectionValue(config?.agent_roster.orchestrator ?? "", orchestratorModel)}
            modelGroups={modelGroups}
            openCodeAvailability={openCodeAvailability}
            currentAgent={config?.agent_roster.orchestrator}
            currentModel={orchestratorModel}
            disabled={disabled}
            onValueChange={(value) =>
              commitPolicy((next) => {
                const selection = parseSelectionValue(value);
                next.agent_roster.orchestrator = selection.agent;
                next.agent_roster.orchestrator_model = selection.model;
                normalizeOrchestratorReasoningEffort(next);
              })
            }
          />
          <ReasoningEffortSelect
            ariaLabel="Orchestrator reasoning level"
            agent={config?.agent_roster.orchestrator ?? ""}
            value={normalizeReasoningEffortForAgent(config?.agent_roster.orchestrator ?? "", config?.agent_roster.reasoning_effort)}
            disabled={disabled}
            infoDescription="Sets reasoning for the orchestrator only; reviewer reasoning is configured independently in each reviewer row."
            onValueChange={(value) =>
              commitPolicy((next) => {
                next.agent_roster.reasoning_effort = value;
              })
            }
          />
        </div>
      </div>
    </div>
  );
}

function ReasoningEffortSelect({
  ariaLabel,
  agent,
  value,
  disabled,
  infoDescription,
  onValueChange,
}: {
  ariaLabel: string;
  agent: string;
  value: CodeReviewReasoningEffort;
  disabled?: boolean;
  infoDescription: string;
  onValueChange: (value: CodeReviewReasoningEffort) => void;
}) {
  return (
    <div className="flex min-w-0 items-center gap-1.5">
      <Select value={value} disabled={disabled} onValueChange={(next) => onValueChange(next as CodeReviewReasoningEffort)}>
        <SelectTrigger aria-label={ariaLabel}>
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          {reasoningOptionsForAgent(agent).map((option) => (
            <SelectItem key={option.value} value={option.value}>
              {option.label}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
      <SettingInfoTooltip label={ariaLabel} description={infoDescription} />
    </div>
  );
}

function AgentModelSelect({
  value,
  modelGroups,
  openCodeAvailability,
  currentAgent,
  currentModel,
  disabled,
  ariaLabel,
  infoDescription,
  onValueChange,
}: {
  value: string;
  modelGroups: AgentModelGroup[];
  openCodeAvailability?: Map<string, OpenCodeModelAvailability>;
  currentAgent?: string;
  currentModel?: string;
  disabled?: boolean;
  ariaLabel: string;
  infoDescription: string;
  onValueChange: (value: string) => void;
}) {
  const currentValueAvailable = modelGroups.some((group) => group.models.some((model) => selectionValue(group.key, model) === value));

  return (
    <div className="flex min-w-0 items-center gap-1.5">
    <Select value={value} onValueChange={onValueChange} disabled={disabled || modelGroups.length === 0}>
      <SelectTrigger aria-label={ariaLabel}>
        <SelectValue placeholder="Select model" />
      </SelectTrigger>
      <SelectContent>
        {!currentValueAvailable && currentAgent && currentModel ? (
          <SelectGroup>
            <SelectLabel>Current selection</SelectLabel>
            <SelectItem value={selectionValue(currentAgent, currentModel)}>
              {AGENTS_BY_KEY[currentAgent]?.label ?? currentAgent} · {modelOptionLabel(currentModel)}
            </SelectItem>
          </SelectGroup>
        ) : null}
          <ModelOptionGroups modelGroups={modelGroups} getOptionValue={(group, model) => selectionValue(group.key, model)} openCodeAvailability={openCodeAvailability} />
      </SelectContent>
    </Select>
      <SettingInfoTooltip label={ariaLabel} description={infoDescription} />
    </div>
  );
}

function NumberPolicyInput({
  label,
  serverValue,
  min,
  max,
  disabled,
  autosave,
  buildPatch,
}: {
  label: string;
  serverValue?: number;
  min: number;
  max?: number;
  disabled?: boolean;
  autosave: UseAutosaveResult<CodeReviewPolicyConfig>;
  buildPatch: (value: number) => CodeReviewPolicyConfig;
}) {
  const field = useAutosaveNumericField<CodeReviewPolicyConfig>({
    serverValue: serverValue ?? min,
    autosave,
    toPatch: buildPatch,
    clamp: (value) => Math.max(min, max !== undefined ? Math.min(max, value) : value),
  });
  return (
    <div className="rounded-md border border-border p-4">
      <div className="flex items-center gap-1.5">
      <Label className="text-xs text-muted-foreground">{label}</Label>
        <SettingInfoTooltip
          label={label}
          description={NUMBER_POLICY_DESCRIPTIONS[label] ?? `${label} is a deterministic policy control. Its current default remains active until changed.`}
        />
      </div>
      <Input className="mt-2" type="number" aria-label={label} min={min} max={max} value={field.value} disabled={disabled} onChange={field.onChange} onBlur={field.onBlur} />
    </div>
  );
}

function PolicyToggle({
  label,
  description,
  checked,
  disabled,
  onCheckedChange,
}: {
  label: string;
  description: string;
  checked: boolean;
  disabled?: boolean;
  onCheckedChange: (checked: boolean) => void;
}) {
  return (
    <div className="flex min-w-0 items-center justify-between gap-3 py-2">
      <div className="flex min-w-0 items-center gap-1.5">
        <Label className="truncate text-sm text-foreground">{label}</Label>
        <SettingInfoTooltip label={label} description={description} />
      </div>
      <Switch aria-label={label} checked={checked} disabled={disabled} onCheckedChange={onCheckedChange} />
    </div>
  );
}

function SettingInfoTooltip({ label, description }: { label: string; description: string }) {
  const [open, setOpen] = useState(false);
  return (
    <TooltipProvider delayDuration={150}>
      <Tooltip open={open} onOpenChange={setOpen}>
        <TooltipTrigger asChild>
          <Button
            type="button"
            variant="ghost"
            size="icon"
            className="h-5 w-5 shrink-0 rounded-full text-muted-foreground hover:text-foreground"
            aria-label={`About ${label}`}
            aria-expanded={open}
            onClick={() => setOpen((current) => !current)}
          >
            <CircleHelp className="h-3.5 w-3.5" />
          </Button>
        </TooltipTrigger>
        <TooltipContent side="top" sideOffset={6} className="max-w-72 leading-5">
          {description}
        </TooltipContent>
      </Tooltip>
    </TooltipProvider>
  );
}

function SettingLabel({ label, info, className = "text-xs text-muted-foreground" }: { label: string; info: string; className?: string }) {
  return (
    <div className="flex items-center gap-1.5">
      <Label className={className}>{label}</Label>
      <SettingInfoTooltip label={label} description={info} />
    </div>
  );
}

function normalizeListItems(items: string[]): string[] {
  const seen = new Set<string>();
  const normalized: string[] = [];
  for (const item of items) {
    const trimmed = item.trim();
    if (!trimmed || seen.has(trimmed)) continue;
    seen.add(trimmed);
    normalized.push(trimmed);
  }
  return normalized;
}

function PolicyStringListEditor({
  label,
  description,
  placeholder,
  emptyText,
  serverValue,
  disabled,
  monospace = false,
  onCommitItems,
}: {
  label: string;
  description?: string;
  placeholder: string;
  emptyText: string;
  serverValue: string[];
  disabled?: boolean;
  monospace?: boolean;
  onCommitItems: (items: string[]) => void;
}) {
  const [draft, setDraft] = useState("");
  const items = normalizeListItems(serverValue);
  const addLabel = `Add ${label.toLowerCase().replace(/ies$/, "y").replace(/s$/, "")}`;
  const countLabel = `${items.length} ${items.length === 1 ? "item" : "items"}`;

  const commitNext = (nextItems: string[]) => {
    onCommitItems(normalizeListItems(nextItems));
  };

  const addItems = (rawItems: string[]) => {
    const nextItems = normalizeListItems([...items, ...rawItems]);
    if (nextItems.length === items.length) return;
    commitNext(nextItems);
    setDraft("");
  };

  const handleAdd = () => addItems([draft]);

  const handleKeyDown = (event: KeyboardEvent<HTMLInputElement>) => {
    if (event.key !== "Enter") return;
    event.preventDefault();
    handleAdd();
  };

  const handlePaste = (event: ClipboardEvent<HTMLInputElement>) => {
    const text = event.clipboardData.getData("text");
    if (!text.includes("\n")) return;
    event.preventDefault();
    addItems(text.split(/\r?\n/));
  };

  const removeItem = (item: string) => {
    commitNext(items.filter((current) => current !== item));
  };

  return (
    <section className="rounded-md border border-border bg-background">
      <div className="space-y-1 p-4">
        <div className="flex items-center justify-between gap-3">
          <div className="flex items-center gap-1.5">
          <Label htmlFor={`${label.toLowerCase().replace(/[^a-z0-9]+/g, "-")}-input`} className="text-sm font-medium text-foreground">
            {label}
          </Label>
            <SettingInfoTooltip
              label={label}
              description={`${description ?? `Controls the ${label.toLowerCase()} policy list.`} ${emptyText} Matching entries act as deterministic policy controls and do not alter reviewer instructions.`}
            />
          </div>
          <Badge variant="outline" className="shrink-0 text-xs">
            {countLabel}
          </Badge>
        </div>
        {description ? <p className="text-xs text-muted-foreground">{description}</p> : null}
      </div>
      <div className="divide-y divide-border border-t border-border">
        {items.length === 0 ? (
          <div className="px-4 py-3 text-xs text-muted-foreground">{emptyText}</div>
        ) : (
          items.map((item) => (
            <div key={item} className="flex min-h-10 items-center gap-3 px-4 py-2">
              <span className={`min-w-0 flex-1 truncate text-sm text-foreground ${monospace ? "font-mono" : ""}`}>{item}</span>
              <Button type="button" variant="ghost" size="icon-sm" disabled={disabled} aria-label={`Remove ${item}`} onClick={() => removeItem(item)}>
                <Trash2 className="h-4 w-4" />
              </Button>
            </div>
          ))
        )}
        <div className="grid gap-2 p-3 sm:grid-cols-[1fr_auto]">
          <Input
            id={`${label.toLowerCase().replace(/[^a-z0-9]+/g, "-")}-input`}
            value={draft}
            disabled={disabled}
            placeholder={placeholder}
            aria-label={label}
            onChange={(event) => setDraft(event.target.value)}
            onKeyDown={handleKeyDown}
            onPaste={handlePaste}
          />
          <Button type="button" variant="outline" disabled={disabled || !draft.trim()} onClick={handleAdd}>
            <Plus className="h-4 w-4" />
            {addLabel}
          </Button>
        </div>
      </div>
    </section>
  );
}

function ListTextArea({ label, serverValue, disabled, onCommitItems }: { label: string; serverValue: string[]; disabled?: boolean; onCommitItems: (items: string[]) => void }) {
  const field = useDebouncedTextField({
    serverValue: serverValue.join("\n"),
    onCommit: (text) =>
      onCommitItems(
        text
          .split(/\r?\n/)
          .map((item) => item.trim())
          .filter(Boolean),
      ),
    debounceMs: CODE_REVIEW_TEXTAREA_DEBOUNCE_MS,
  });
  return (
    <div className="space-y-2">
      <SettingLabel
        label={label}
        info={`Limits this structured PR-description check to the listed ${label.toLowerCase()}. Leave empty to use the applicability rule's default matching behavior.`}
      />
      <Textarea value={field.value} disabled={disabled} rows={4} onChange={(event) => field.onChange(event.target.value)} onBlur={field.onBlur} />
    </div>
  );
}

function PolicyTextInput({
  serverValue,
  disabled,
  onCommit,
  ...props
}: {
  serverValue: string;
  onCommit: (value: string) => void;
} & Omit<ComponentProps<typeof Input>, "value" | "onChange" | "onBlur">) {
  const field = useDebouncedTextField({ serverValue, onCommit });
  return <Input {...props} value={field.value} disabled={disabled} onChange={(event) => field.onChange(event.target.value)} onBlur={field.onBlur} />;
}

function PolicyTextarea({
  serverValue,
  disabled,
  onCommit,
  ...props
}: {
  serverValue: string;
  onCommit: (value: string) => void;
} & Omit<ComponentProps<typeof Textarea>, "value" | "onChange" | "onBlur">) {
  const field = useDebouncedTextField({
    serverValue,
    onCommit,
    debounceMs: CODE_REVIEW_TEXTAREA_DEBOUNCE_MS,
  });
  return <Textarea {...props} value={field.value} disabled={disabled} onChange={(event) => field.onChange(event.target.value)} onBlur={field.onBlur} />;
}

function FineTuningSection({
  title,
  summary,
  defaultOpen = false,
  forceOpen = false,
  onOpened,
  children,
}: {
  title: string;
  summary?: string;
  defaultOpen?: boolean;
  forceOpen?: boolean;
  onOpened?: () => void;
  children: ReactNode;
}) {
  const [open, setOpen] = useState(defaultOpen);
  const triggerRef = useRef<HTMLButtonElement>(null);
  useEffect(() => {
    if (forceOpen) triggerRef.current?.focus();
  }, [forceOpen]);
  return (
    <Collapsible
      open={open || forceOpen}
      onOpenChange={(next) => {
        setOpen(next);
        if (next) onOpened?.();
      }}
    >
      <CollapsibleTrigger ref={triggerRef} className="group flex w-full items-center justify-between gap-3 px-1 py-4 text-left hover:bg-muted/40">
        <div className="min-w-0">
          <div className="text-sm font-medium text-foreground">{title}</div>
          {summary ? <div className="mt-0.5 text-xs text-muted-foreground">{summary}</div> : null}
        </div>
        <ChevronDown className="h-4 w-4 shrink-0 text-muted-foreground transition-transform group-data-[state=open]:rotate-180" />
      </CollapsibleTrigger>
      <CollapsibleContent className="space-y-3 border-t border-border px-1 py-4">{children}</CollapsibleContent>
    </Collapsible>
  );
}

const policyOwnerActivityIdleMs = 30_000;

function CodeReviewDisputeQueue({
  disputes,
  isLoading,
  error,
  isSaving,
  hasMore,
  isLoadingMore,
  onLoadMore,
  onRetry,
  onAdjudicate,
}: {
  disputes: CodeReviewDispute[];
  isLoading: boolean;
  error: Error | null;
  isSaving: boolean;
  hasMore: boolean;
  isLoadingMore: boolean;
  onLoadMore: () => void;
  onRetry: () => void;
  onAdjudicate: (
    dispute: CodeReviewDispute,
    status: "upheld" | "rejected" | "needs_context",
    note: string | undefined,
    activeSeconds: number,
    onSaved: () => void,
    onFailed: () => void,
  ) => void;
}) {
  const [notes, setNotes] = useState<Record<string, string>>({});
  // Hold the id, not the object. The queue polls while this tab is open, so a
  // snapshot taken at click time would keep showing pre-reassessment context
  // and would submit a stale expected_version once the row is refetched.
  const [selectedDisputeID, setSelectedDisputeID] = useState<string | null>(null);
  const selectedDispute = useMemo(() => disputes.find((dispute) => dispute.id === selectedDisputeID) ?? null, [disputes, selectedDisputeID]);
  // Active queue time is still measured and sent on adjudication even though the
  // Insights report that charted it was retired: the value is only observable
  // while the owner works the dispute, so pausing collection would leave an
  // unrecoverable gap if owner-time reporting returns.
  const activeTimers = useRef<Record<string, { startedAt: number | null; accumulatedMs: number }>>({});
  const activeInteractions = useRef<Record<string, { pointer: boolean; focus: boolean }>>({});
  const activeDisputeID = useRef<string | null>(null);
  const activityIdleTimer = useRef<number | null>(null);
  const pageIsActive = useRef(true);
  const startActiveTimer = useCallback((disputeID: string) => {
    if (!pageIsActive.current) return;
    const timer = activeTimers.current[disputeID] ?? {
      startedAt: null,
      accumulatedMs: 0,
    };
    if (timer.startedAt === null) timer.startedAt = performance.now();
    activeTimers.current[disputeID] = timer;
  }, []);
  const pauseActiveTimer = useCallback((disputeID: string) => {
    const timer = activeTimers.current[disputeID];
    if (!timer || timer.startedAt === null) return;
    timer.accumulatedMs += performance.now() - timer.startedAt;
    timer.startedAt = null;
  }, []);
  const clearActivityIdleTimer = useCallback(() => {
    if (activityIdleTimer.current !== null) {
      window.clearTimeout(activityIdleTimer.current);
      activityIdleTimer.current = null;
    }
  }, []);
  const pauseCurrentActivity = useCallback(() => {
    clearActivityIdleTimer();
    if (activeDisputeID.current !== null) pauseActiveTimer(activeDisputeID.current);
    activeDisputeID.current = null;
  }, [clearActivityIdleTimer, pauseActiveTimer]);
  const recordActiveInteraction = useCallback(
    (disputeID: string) => {
      if (!pageIsActive.current) return;
      if (activeDisputeID.current !== null && activeDisputeID.current !== disputeID) {
        pauseActiveTimer(activeDisputeID.current);
      }
      activeDisputeID.current = disputeID;
      startActiveTimer(disputeID);
      clearActivityIdleTimer();
      activityIdleTimer.current = window.setTimeout(() => {
        if (activeDisputeID.current === disputeID) pauseCurrentActivity();
      }, policyOwnerActivityIdleMs);
    },
    [clearActivityIdleTimer, pauseActiveTimer, pauseCurrentActivity, startActiveTimer],
  );
  const setActiveInteraction = useCallback(
    (disputeID: string, kind: "pointer" | "focus", active: boolean) => {
      const current = activeInteractions.current[disputeID] ?? {
        pointer: false,
        focus: false,
      };
      const next = { ...current, [kind]: active };
      activeInteractions.current[disputeID] = next;
      if (active) {
        recordActiveInteraction(disputeID);
      } else if (activeDisputeID.current === disputeID && !next.pointer && !next.focus) {
        pauseCurrentActivity();
      }
    },
    [pauseCurrentActivity, recordActiveInteraction],
  );
  const resumeActiveInteraction = useCallback(
    (disputeID: string) => {
      const interaction = activeInteractions.current[disputeID];
      if (interaction && (interaction.pointer || interaction.focus)) recordActiveInteraction(disputeID);
    },
    [recordActiveInteraction],
  );
  useEffect(() => {
    const pauseAll = () => {
      pageIsActive.current = false;
      pauseCurrentActivity();
    };
    const resumePage = () => {
      pageIsActive.current = document.visibilityState !== "hidden";
    };
    const handleVisibilityChange = () => {
      if (document.visibilityState === "hidden") pauseAll();
      else resumePage();
    };
    pageIsActive.current = document.visibilityState !== "hidden";
    document.addEventListener("visibilitychange", handleVisibilityChange);
    window.addEventListener("blur", pauseAll);
    window.addEventListener("focus", resumePage);
    return () => {
      document.removeEventListener("visibilitychange", handleVisibilityChange);
      window.removeEventListener("blur", pauseAll);
      window.removeEventListener("focus", resumePage);
      pauseAll();
    };
  }, [pauseCurrentActivity]);
  const clearNote = useCallback((disputeID: string) => {
    setNotes((current) => {
      if (current[disputeID] === undefined) return current;
      const next = { ...current };
      delete next[disputeID];
      return next;
    });
  }, []);
  // Drop the draft only once the adjudication is durably saved. Clearing it
  // eagerly loses the admin's typed reasoning whenever the PATCH fails.
  const adjudicate = (dispute: CodeReviewDispute, status: "upheld" | "rejected" | "needs_context") => {
    pauseActiveTimer(dispute.id);
    if (activeDisputeID.current === dispute.id) pauseCurrentActivity();
    const activeSeconds = Math.min(3600, Math.max(0, Math.ceil((activeTimers.current[dispute.id]?.accumulatedMs ?? 0) / 1000)));
    onAdjudicate(
      dispute,
      status,
      notes[dispute.id],
      activeSeconds,
      () => {
        delete activeTimers.current[dispute.id];
        delete activeInteractions.current[dispute.id];
        clearNote(dispute.id);
        setSelectedDisputeID(null);
      },
      () => resumeActiveInteraction(dispute.id),
    );
  };
  const openDispute = (dispute: CodeReviewDispute) => {
    setSelectedDisputeID(dispute.id);
    recordActiveInteraction(dispute.id);
  };
  // Radix unmounts the sheet without always firing pointerleave/blur, so a
  // leftover pointer:true would stop the next pointer-leave from pausing the
  // timer and inflate policy_owner_active_seconds.
  const releaseDispute = useCallback(
    (disputeID: string) => {
      pauseCurrentActivity();
      delete activeInteractions.current[disputeID];
    },
    [pauseCurrentActivity],
  );
  const closeDispute = (disputeID: string | null) => {
    if (disputeID !== null) releaseDispute(disputeID);
    else pauseCurrentActivity();
    setSelectedDisputeID(null);
  };
  // A dispute adjudicated elsewhere drops out of the pending queue on the next
  // poll, which closes the sheet on its own because `selectedDispute` is
  // derived. Stop its timer too, rather than billing a row that is gone.
  useEffect(() => {
    if (selectedDisputeID !== null && selectedDispute === null) releaseDispute(selectedDisputeID);
  }, [releaseDispute, selectedDispute, selectedDisputeID]);
  const disputeColumns: ResponsiveResourceListColumn<CodeReviewDispute>[] = [
    {
      id: "objection",
      header: "Objection",
      cellClassName: "min-w-[22rem] max-w-xl",
      render: (dispute) => (
        <div className="space-y-1">
          <div className="line-clamp-2 font-medium leading-5 text-foreground">{dispute.body}</div>
          <div className="text-xs text-muted-foreground">
            {codeReviewDisputePullRequestLabel(dispute)} · {dispute.filed_by_login || "143 user"} · {formatDate(dispute.created_at)}
          </div>
        </div>
      ),
    },
    {
      id: "decision",
      header: "Original decision",
      render: (dispute) => (
        <div className="space-y-1">
          <StatusLabel label={decisionLabelText(dispute.decision)} tone={codeReviewDecisionTone(dispute.decision)} indicator="none" />
          <div className="text-xs text-muted-foreground">{codeReviewDisputeDirectionLabel(dispute.direction)}</div>
        </div>
      ),
    },
    {
      id: "reassessment",
      header: "Reassessment",
      render: (dispute) => <CodeReviewDisputeReassessment dispute={dispute} />,
    },
    {
      id: "actions",
      header: <span className="sr-only">Actions</span>,
      className: "text-right",
      cellClassName: "text-right",
      render: (dispute) => (
        <Button variant="ghost" size="sm" aria-label={`Review dispute on ${codeReviewDisputePullRequestLabel(dispute)}`} onClick={() => openDispute(dispute)}>
          Review
          <ChevronRight />
        </Button>
      ),
    },
  ];
  return (
    <SectionGroup
      title="Disputes"
      description="Objections to code review decisions that need a policy owner."
    >
      {isLoading ? <div className="py-12 text-center text-sm text-muted-foreground">Loading disputes…</div> : null}
      {error ? (
        <ErrorNotice title="Disputes could not be loaded" description="Retry the request to view the adjudication list." action={{ label: "Retry", onClick: onRetry }} />
      ) : null}
      {!isLoading && !error && disputes.length === 0 ? (
        <EmptyState icon={MessageSquareText} title="No disputes need adjudication" description="New trusted objections will appear here after intake." />
      ) : null}
      {disputes.length > 0 ? (
        <ResponsiveResourceList
          ariaLabel="Code review disputes"
          mobileAriaLabel="Code review dispute queue"
          items={disputes}
          getItemKey={(dispute) => dispute.id}
          columns={disputeColumns}
          emptyState="No disputes need adjudication."
          footer={
            <div className="flex items-center justify-between gap-3 border-t border-border/50 bg-muted/20 px-4 py-2.5">
              <span className="text-xs tabular-nums text-muted-foreground" aria-live="polite">
                {disputes.length}{hasMore ? "+" : ""} pending
              </span>
              {hasMore ? (
                <Button variant="ghost" size="sm" disabled={isLoadingMore} onClick={onLoadMore}>
                  {isLoadingMore ? "Loading…" : "Show more"}
                </Button>
              ) : null}
            </div>
          }
          getDesktopRowProps={(dispute) => ({ "data-state": selectedDispute?.id === dispute.id ? "selected" : undefined })}
          renderMobileItem={(dispute) => (
            <ResourceRow
              selected={selectedDispute?.id === dispute.id}
              title={<span className="line-clamp-2 text-sm leading-5">{dispute.body}</span>}
              metadata={
                <span>
                  {codeReviewDisputePullRequestLabel(dispute)} · {dispute.filed_by_login || "143 user"} · {formatDate(dispute.created_at)}
                </span>
              }
              detail={
                <div className="flex flex-wrap items-center gap-x-4 gap-y-2 pt-1">
                  <StatusLabel label={decisionLabelText(dispute.decision)} tone={codeReviewDecisionTone(dispute.decision)} indicator="none" />
                  <CodeReviewDisputeReassessment dispute={dispute} />
                </div>
              }
              actions={
                <Button variant="outline" size="sm" aria-label={`Review dispute on ${codeReviewDisputePullRequestLabel(dispute)}`} onClick={() => openDispute(dispute)}>
                  Review dispute
                </Button>
              }
            />
          )}
        />
      ) : null}
      <Sheet open={selectedDispute !== null} onOpenChange={(open) => !open && closeDispute(selectedDisputeID)}>
        <SheetContent
          className="flex w-[calc(100vw-1rem)] flex-col gap-0 overflow-hidden p-0 sm:max-w-xl"
          onPointerEnter={() => selectedDispute && setActiveInteraction(selectedDispute.id, "pointer", true)}
          onPointerMove={() => selectedDispute && recordActiveInteraction(selectedDispute.id)}
          onPointerLeave={() => selectedDispute && setActiveInteraction(selectedDispute.id, "pointer", false)}
          onFocusCapture={() => selectedDispute && setActiveInteraction(selectedDispute.id, "focus", true)}
          onKeyDownCapture={() => selectedDispute && recordActiveInteraction(selectedDispute.id)}
          onBlurCapture={(event) => {
            if (selectedDispute && (!event.relatedTarget || !event.currentTarget.contains(event.relatedTarget as Node))) {
              setActiveInteraction(selectedDispute.id, "focus", false);
            }
          }}
        >
          {selectedDispute ? (
            <>
              <SheetHeader className="border-b border-border px-6 py-5 pr-12">
                <SheetTitle>Review dispute</SheetTitle>
                <SheetDescription>
                  {codeReviewDisputePullRequestLabel(selectedDispute)} · filed by {selectedDispute.filed_by_login || "143 user"} · {formatDate(selectedDispute.created_at)}
                </SheetDescription>
              </SheetHeader>
              <div className="flex-1 space-y-6 overflow-y-auto px-6 py-5">
                <SectionGroup title="Objection" headingLevel={3} className="space-y-3">
                  <p className="text-sm leading-6 text-foreground">{selectedDispute.body}</p>
                  <CodeReviewDisputeContextLinks dispute={selectedDispute} />
                  <div className="text-xs text-muted-foreground">
                    {codeReviewDisputeStatusLabel(selectedDispute.source)} · reviewed {selectedDispute.reviewed_head_sha.slice(0, 7)}
                  </div>
                  {selectedDispute.contested_reason_codes.length > 0 ? (
                    <div className="flex flex-wrap gap-1.5">
                      {selectedDispute.contested_reason_codes.map((code) => (
                        <Badge key={code} variant="outline">
                          {codeReviewDisputeStatusLabel(code)}
                        </Badge>
                      ))}
                    </div>
                  ) : null}
                </SectionGroup>

                <SectionGroup title="Decision context" headingLevel={3}>
                  <div className="grid gap-3 sm:grid-cols-2">
                    <Card variant="recessed">
                      <CardContent className="space-y-1 p-3.5">
                        <div className="text-xs text-muted-foreground">Original decision</div>
                        <StatusLabel label={decisionLabelText(selectedDispute.decision)} tone={codeReviewDecisionTone(selectedDispute.decision)} indicator="none" />
                      </CardContent>
                    </Card>
                    <Card variant="recessed">
                      <CardContent className="space-y-1 p-3.5">
                        <div className="text-xs text-muted-foreground">Dispute direction</div>
                        <div className="text-sm font-medium text-foreground">{codeReviewDisputeDirectionLabel(selectedDispute.direction)}</div>
                      </CardContent>
                    </Card>
                    <Card variant="recessed">
                      <CardContent className="space-y-1 p-3.5">
                        <div className="text-xs text-muted-foreground">Reassessment</div>
                        <CodeReviewDisputeReassessment dispute={selectedDispute} />
                      </CardContent>
                    </Card>
                    <Card variant="recessed">
                      <CardContent className="space-y-1 p-3.5">
                        <div className="text-xs text-muted-foreground">Filer trust</div>
                        <StatusLabel label={selectedDispute.trusted ? "Trusted" : "Untrusted"} tone={selectedDispute.trusted ? "success" : "warning"} />
                      </CardContent>
                    </Card>
                  </div>
                </SectionGroup>

                <SectionGroup
                  title="Queue context"
                  description="Signals explain this dispute's position in the queue; they do not determine the outcome."
                  headingLevel={3}
                >
                  <CodeReviewDisputeQueueSignals dispute={selectedDispute} />
                </SectionGroup>
              </div>
              <div className="space-y-3 border-t border-border bg-background px-6 py-4">
                <div className="space-y-2">
                  <Label htmlFor={`adjudication-note-${selectedDispute.id}`}>Decision note <span className="font-normal text-muted-foreground">(optional)</span></Label>
                  <Textarea
                    id={`adjudication-note-${selectedDispute.id}`}
                    value={notes[selectedDispute.id] ?? ""}
                    rows={3}
                    maxLength={2000}
                    placeholder="Add context for the decision"
                    disabled={isSaving}
                    onChange={(event) =>
                      setNotes((current) => ({
                        ...current,
                        [selectedDispute.id]: event.target.value,
                      }))
                    }
                  />
                </div>
                <div className="flex flex-wrap justify-end gap-2">
                  <DisabledTooltip disabled={isSaving} content="Wait for the current adjudication to finish.">
                    <Button size="sm" variant="outline" disabled={isSaving} onClick={() => adjudicate(selectedDispute, "needs_context")}>
                      Needs context
                    </Button>
                  </DisabledTooltip>
                  <DisabledTooltip disabled={isSaving} content="Wait for the current adjudication to finish.">
                    <Button size="sm" variant="outline" disabled={isSaving} onClick={() => adjudicate(selectedDispute, "rejected")}>
                      Reject
                    </Button>
                  </DisabledTooltip>
                  <DisabledTooltip disabled={isSaving} content="Wait for the current adjudication to finish.">
                    <Button size="sm" disabled={isSaving} onClick={() => adjudicate(selectedDispute, "upheld")}>
                      Uphold
                    </Button>
                  </DisabledTooltip>
                </div>
              </div>
            </>
          ) : null}
        </SheetContent>
      </Sheet>
    </SectionGroup>
  );
}

function CodeReviewDisputeReassessment({ dispute }: { dispute: CodeReviewDispute }) {
  const label = dispute.reassessment_status === "not_requested"
    ? "Not run"
    : dispute.reassessment_status === "completed" && dispute.reassessment_decision
      ? decisionLabelText(dispute.reassessment_decision)
      : codeReviewDisputeStatusLabel(dispute.reassessment_status);
  // A failed reassessment means the policy owner is adjudicating without
  // evidence that was meant to be there, so it cannot read as quietly as the
  // deliberate "Not run".
  const failed = dispute.reassessment_status === "failed";
  const attention = dispute.reassessment_flipped || failed;
  return (
    <StatusLabel
      label={label}
      detail={dispute.reassessment_flipped ? "Decision changed" : failed ? "Evidence unavailable" : undefined}
      tone={attention ? "warning" : "neutral"}
      indicator={attention ? "dot" : "none"}
    />
  );
}

function CodeReviewDisputeQueueSignals({ dispute }: { dispute: CodeReviewDispute }) {
  const pullRequestAuthor = typeof dispute.queue_signals.pull_request_author === "string" ? dispute.queue_signals.pull_request_author.trim() : "";
  const trustedAtFiling = typeof dispute.queue_signals.trusted_at_filing === "boolean" ? dispute.queue_signals.trusted_at_filing : null;
  const filerIsAuthor = typeof dispute.queue_signals.filer_is_pr_author === "boolean" ? dispute.queue_signals.filer_is_pr_author : null;
  const contradiction = dispute.queue_signals.independent_human_contradiction === true;
  const unchanged = dispute.queue_signals.reassessment_unchanged === true;
  const flipped = dispute.queue_signals.reassessment_flipped === true;
  const filerIsNotAuthor = dispute.queue_signals.filer_is_not_pr_author === true;
  const repeats = typeof dispute.queue_signals.repeat_reason_disputes_14_days === "number" ? dispute.queue_signals.repeat_reason_disputes_14_days : 0;
  const superseded = dispute.queue_signals.base_policy_superseded === true;
  const rankingEnabled = dispute.queue_signals.ranking_enabled === true;
  const hasQueueSignal = contradiction || unchanged || flipped || repeats > 0 || superseded || (rankingEnabled && dispute.queue_priority > 0)
    || Boolean(pullRequestAuthor) || filerIsAuthor !== null || filerIsNotAuthor || trustedAtFiling !== null;
  return (
    <div className="flex flex-wrap gap-1.5">
      {contradiction ? <Badge variant="destructive">Human reviewer disagreed</Badge> : null}
      {unchanged ? <Badge variant="secondary">Same result after reassessment</Badge> : null}
      {flipped ? <Badge variant="secondary">Changed after reassessment</Badge> : null}
      {repeats > 0 ? <Badge variant="secondary">{repeats} similar objections</Badge> : null}
      {superseded ? <Badge variant="outline">Policy has changed</Badge> : null}
      {rankingEnabled && dispute.queue_priority > 0 ? <Badge variant="outline">Queue priority {dispute.queue_priority}</Badge> : null}
      {pullRequestAuthor ? <Badge variant="outline">PR author: {pullRequestAuthor}</Badge> : null}
      {filerIsAuthor !== null || filerIsNotAuthor ? <Badge variant="outline">{filerIsAuthor === true ? "Filed by PR author" : "Filed by another contributor"}</Badge> : null}
      {trustedAtFiling !== null ? <Badge variant="outline">{trustedAtFiling ? "Trusted at filing" : "Untrusted at filing"}</Badge> : null}
      {!hasQueueSignal ? <span className="text-xs text-muted-foreground">No queue signals</span> : null}
    </div>
  );
}

function CodeReviewDisputeContextLinks({ dispute }: { dispute: CodeReviewDispute }) {
  const { repository, number, title, url } = codeReviewDisputePullRequestContext(dispute);
  return (
    <div className="flex flex-wrap items-center gap-1 text-xs text-muted-foreground">
      {url ? (
        <Button size="sm" variant="link" className="h-auto p-0 text-xs" asChild>
          <a href={url} target="_blank" rel="noreferrer">
            {repository && number ? `${repository} #${number}` : title || "Open pull request"}
          </a>
        </Button>
      ) : null}
      {/*
        Opens in a new tab deliberately, and says so: the evidence sheet lives in
        the reviews tab, so navigating in place unmounts the disputes tab and
        discards the open sheet, the typed decision note, and the active time.
      */}
      <Button size="sm" variant="link" className="h-auto p-0 text-xs" asChild>
        <Link href={`/code-reviews?evidence=${dispute.session_id}`} target="_blank" rel="noreferrer">
          View evidence
          <SquareArrowOutUpRight aria-hidden="true" className="size-3" />
          <span className="sr-only">(opens in a new tab)</span>
        </Link>
      </Button>
    </div>
  );
}

function decisionLabelText(decision: CodeReviewDecision): string {
  switch (decision) {
    case "approved":
      return "Approved";
    case "needs_human_review":
      return "Needs human review";
    case "comment_only":
      return "Comment only";
    case "blocked":
      return "Blocked";
  }
}

function codeReviewDecisionTone(decision: CodeReviewDecision): StatusTone {
  if (decision === "approved") return "success";
  if (decision === "blocked") return "destructive";
  if (decision === "needs_human_review") return "warning";
  return "neutral";
}

// Self-describing on purpose: this sits directly under the original decision in
// the queue, where a bare "Approve" reads like a second, contradictory verdict.
function codeReviewDisputeDirectionLabel(direction?: CodeReviewDispute["direction"]): string {
  if (direction === "should_have_approved") return "Asks to approve";
  if (direction === "should_not_have_approved") return "Asks not to approve";
  return "Classification pending";
}

// queue_signals is an untyped bag, so the guards live in one place rather than
// being repeated by every caller that needs the PR context.
function codeReviewDisputePullRequestContext(dispute: CodeReviewDispute): {
  repository: string;
  number: number | null;
  title: string;
  url: string;
} {
  return {
    repository: typeof dispute.queue_signals.github_repository === "string" ? dispute.queue_signals.github_repository.trim() : "",
    number: typeof dispute.queue_signals.github_pr_number === "number" ? dispute.queue_signals.github_pr_number : null,
    title: typeof dispute.queue_signals.pull_request_title === "string" ? dispute.queue_signals.pull_request_title.trim() : "",
    url: typeof dispute.queue_signals.github_pr_url === "string" ? dispute.queue_signals.github_pr_url.trim() : "",
  };
}

function codeReviewDisputePullRequestLabel(dispute: CodeReviewDispute): string {
  const { repository, number, title } = codeReviewDisputePullRequestContext(dispute);
  if (repository && number) return `${repository} #${number}`;
  if (title) return title;
  // Reads as a subject in both the row metadata line and the "Review dispute
  // on …" button label, which "Review <sha>" did not.
  return `commit ${dispute.reviewed_head_sha.slice(0, 7)}`;
}

function codeReviewDisputeStatusLabel(value: string): string {
  const normalized = value.replaceAll("_", " ");
  return normalized.charAt(0).toUpperCase() + normalized.slice(1);
}

function CodeReviewEvidenceSheet({
  review,
  evidence,
  isLoading,
  error,
  nowMs,
  canRetryReview,
  canFileDisputes,
  canManagePolicy,
  isRetryingReview,
  onRetryEvidence,
  onRetryReview,
  open,
  onOpenChange,
}: {
  review: CodeReviewListItem | null;
  evidence?: CodeReviewEvidence;
  isLoading: boolean;
  error: Error | null;
  nowMs: number;
  canRetryReview: boolean;
  canFileDisputes: boolean;
  canManagePolicy: boolean;
  isRetryingReview: boolean;
  onRetryEvidence: () => void;
  onRetryReview: () => void;
  open: boolean;
  onOpenChange: (open: boolean) => void;
}) {
  const queryClient = useQueryClient();
  const [disputeDialogOpen, setDisputeDialogOpen] = useState(false);
  const [disputeBody, setDisputeBody] = useState("");
  const [selectedReasonCodes, setSelectedReasonCodes] = useState<string[]>([]);
  const agentResults = evidence?.agent_results ?? [];
  const findings = evidence?.findings ?? [];
  const records = evidence?.prompt_records ?? evidence?.prompt_artifacts ?? [];
  const reasonCodes = evidence?.risk_reason_codes ?? [];
  const approvalReasons = review ? whyNotApprovedReasons(review) : [];
  const disputesQuery = useInfiniteQuery({
    queryKey: queryKeys.codeReviews.disputes(review?.session_id ?? ""),
    queryFn: ({ pageParam }) => api.codeReviews.disputes(review?.session_id ?? "", pageParam),
    enabled: open && canFileDisputes && Boolean(review?.session_id),
    // Intake and reassessment states move on their own, so the timeline polls.
    // React Query refetches *every* loaded page on each interval, so stretch
    // the interval by the number of loaded pages: the request rate stays flat
    // as the admin pages into history, and the newest page — the one whose
    // state is still moving — keeps updating instead of going stale.
    refetchInterval: (query) => (open && canFileDisputes ? 5000 * Math.max(1, query.state.data?.pages.length ?? 1) : false),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (lastPage) => lastPage.meta?.next_cursor || undefined,
  });
  const createDispute = useMutation({
    mutationFn: () =>
      api.codeReviews.createDispute(review?.session_id ?? "", {
        body: disputeBody,
        contested_reason_codes: selectedReasonCodes,
      }),
    onSuccess: () => {
      if (review)
        void queryClient.invalidateQueries({
          queryKey: queryKeys.codeReviews.disputes(review.session_id),
        });
      setDisputeDialogOpen(false);
      setDisputeBody("");
      setSelectedReasonCodes([]);
      toast.success("Reconsideration request recorded");
    },
    onError: () => toast.error("Reconsideration request could not be recorded"),
  });
  const escalateDispute = useMutation({
    mutationFn: (disputeID: string) => api.codeReviews.escalateDispute(disputeID),
    onSuccess: () => {
      if (review)
        void queryClient.invalidateQueries({
          queryKey: queryKeys.codeReviews.disputes(review.session_id),
        });
      toast.success("Dispute sent to a policy owner");
    },
    onError: () => toast.error("Dispute could not be escalated"),
  });
  const promoteDispute = useMutation({
    mutationFn: (dispute: CodeReviewDispute) =>
      api.codeReviews.adjudicateDispute(dispute.id, {
      expected_version: dispute.version,
      trust_override: true,
    }),
    onSuccess: () => {
      if (review)
        void queryClient.invalidateQueries({
          queryKey: queryKeys.codeReviews.disputes(review.session_id),
        });
      void queryClient.invalidateQueries({
        queryKey: ["code-reviews", "dispute-queue"],
      });
      toast.success("Dispute promoted to the policy queue");
    },
    onError: () => toast.error("Dispute could not be promoted"),
  });
  const disputes = disputesQuery.data?.pages.flatMap((page) => page.data ?? []) ?? [];
  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent className="w-[calc(100vw-1rem)] p-0 sm:max-w-xl">
        <SheetHeader className="border-b border-border px-6 py-5">
          <div className="flex items-start justify-between gap-4 pr-8">
            <div className="min-w-0 space-y-1">
              <SheetTitle>Evidence for #{review?.github_pr_number}</SheetTitle>
              <SheetDescription className="line-clamp-2">{review?.pull_request_title ?? "Review evidence"}</SheetDescription>
            </div>
            {review ? <StatusLabel label={decisionLabel(review)} tone={reviewDecisionTone(review)} /> : null}
          </div>
        </SheetHeader>
        <div className="space-y-6 px-6 py-5">
          {review?.status === "failed" && !isSupersededReview(review) ? (
            <div className="space-y-3">
              <ErrorNotice title="Code review failed" description={reviewStatusMessage(review) ?? "The review stopped before it could finish."} />
              {canRetryReview && reviewCanBeRetried(review) ? (
                <Button variant="outline" disabled={isRetryingReview} onClick={onRetryReview}>
                  <RefreshCw className={isRetryingReview ? "animate-spin" : undefined} />
                  {isRetryingReview ? "Retrying…" : "Retry review"}
                </Button>
              ) : null}
            </div>
          ) : review && (review.status === "queued" || review.status === "running") ? (
            <ReviewOperationalStatus review={review} nowMs={nowMs} />
          ) : null}
          {isLoading ? <div className="text-sm text-muted-foreground">Loading evidence...</div> : null}
          {error ? (
            <ErrorNotice
              title="Evidence could not be loaded"
              description="Retry the request to view this review's evidence."
              action={{ label: "Retry", onClick: onRetryEvidence }}
            />
          ) : null}
          {!isLoading && !error && !evidence ? <div className="text-sm text-muted-foreground">No evidence recorded for this review.</div> : null}
          {/* Derived from the review row rather than the evidence payload, so it
              stays available when the evidence request is empty or failed. */}
          {approvalReasons.length > 0 ? (
            <section className="space-y-3">
              <EvidenceSectionHeader title="Why not approved" empty={false} />
              <div className="space-y-2">
                {approvalReasons.map((reason, index) => (
                  <div key={`${reason}-${index}`} className="border-t border-border pt-2 text-sm leading-6 text-muted-foreground first:border-t-0 first:pt-0">
                    {reason}
                  </div>
                ))}
              </div>
            </section>
          ) : null}
          {evidence ? (
            <>
              <div className="grid grid-cols-3 gap-3">
                <EvidenceMetric label="Agents" value={agentResults.length} />
                <EvidenceMetric label="Findings" value={findings.length} />
                <EvidenceMetric label="Prompts" value={records.length} />
              </div>

              {canFileDisputes && review?.status === "completed" && review.decision ? (
                <section className="space-y-3">
                  <div className="flex items-center justify-between gap-3">
                    <EvidenceSectionHeader title="Decision feedback" empty={disputes.length === 0} />
                    <Button size="sm" variant="outline" onClick={() => setDisputeDialogOpen(true)}>
                      <MessageSquareText className="h-4 w-4" />
                      {review.decision === "approved" ? "Report an unsafe approval" : "Ask for reconsideration"}
                    </Button>
                  </div>
                  {disputesQuery.isLoading ? <div className="text-sm text-muted-foreground">Loading decision feedback…</div> : null}
                  {disputesQuery.error ? (
                    <ErrorNotice
                      title="Decision feedback could not be loaded"
                      description="Retry to view the dispute timeline."
                      action={{
                        label: "Retry",
                        onClick: () => void disputesQuery.refetch(),
                      }}
                    />
                  ) : null}
                  {!disputesQuery.isLoading && !disputesQuery.error && disputes.length === 0 ? (
                    <div className="text-sm text-muted-foreground">No one has challenged this decision.</div>
                  ) : null}
                  {disputes.map((dispute) => (
                    <div key={dispute.id} className="space-y-2 border-t border-border pt-3 first:border-t-0 first:pt-0">
                      <div className="flex items-start justify-between gap-3">
                        <div className="min-w-0">
                          <div className="text-sm font-medium text-foreground">{dispute.filed_by_login || "143 user"}</div>
                          <div className="text-xs text-muted-foreground">
                            {formatDate(dispute.created_at)} · {codeReviewDisputeStatusLabel(dispute.source)}
                          </div>
                        </div>
                        <StatusLabel
                          label={dispute.routing === "review_request" && dispute.intake_status === "discarded" ? "Review requested" : codeReviewDisputeStatusLabel(dispute.intake_status)}
                          tone={dispute.intake_status === "failed" ? "destructive" : "neutral"}
                        />
                      </div>
                      <div className="text-sm leading-6 text-muted-foreground">{dispute.body}</div>
                      <div className="flex flex-wrap items-center gap-2">
                        <Badge variant="outline">{dispute.routing === "review_request" ? "Ordinary review request" : dispute.direction ? codeReviewDisputeStatusLabel(dispute.direction) : "Classifying"}</Badge>
                        <Badge variant="outline">{codeReviewDisputeStatusLabel(dispute.reassessment_status)}</Badge>
                        {dispute.reassessment_status === "completed" && dispute.reassessment_flipped !== undefined ? (
                          <Badge variant="outline">{dispute.reassessment_flipped ? "Decision changed" : "Decision unchanged"}</Badge>
                        ) : null}
                        {dispute.adjudication_status ? <Badge variant="outline">Policy owner: {codeReviewDisputeStatusLabel(dispute.adjudication_status)}</Badge> : null}
                        {dispute.reply_status === "failed" ? <Badge variant="destructive">GitHub reply failed</Badge> : null}
                        {/* Editing a GitHub comment files a new dispute, so the
                            timeline shows both. Say which one is live. */}
                        {dispute.superseded_by_dispute_id ? <Badge variant="secondary">Replaced by a later edit</Badge> : null}
                        <Badge variant="outline">{dispute.trusted ? "Trusted" : "Untrusted"}</Badge>
                        {dispute.reassessment_session_id ? (
                          <Button size="sm" variant="ghost" asChild>
                            <Link href={`/sessions/${dispute.reassessment_session_id}`}>View reassessment</Link>
                          </Button>
                        ) : null}
                        {dispute.routing === "policy_signal_only" && canManagePolicy ? (
                          <Button size="sm" variant="ghost" asChild>
                            <Link href="/code-reviews?tab=policy">Review policy</Link>
                          </Button>
                        ) : null}
                        {canManagePolicy &&
                        !dispute.trusted &&
                        !dispute.superseded_by_dispute_id &&
                        dispute.intake_status === "triaged" &&
                        (dispute.routing === "reassess" || dispute.routing === "policy_signal_only") ? (
                          <DisabledTooltip disabled={promoteDispute.isPending} content="Wait for this promotion to finish.">
                            <Button size="sm" variant="outline" disabled={promoteDispute.isPending} onClick={() => promoteDispute.mutate(dispute)}>
                              Promote to policy queue
                            </Button>
                          </DisabledTooltip>
                        ) : null}
                        {dispute.routing === "policy_signal_only" && !dispute.escalated_at && !dispute.superseded_by_dispute_id ? (
                          <DisabledTooltip disabled={escalateDispute.isPending} content="Wait for this escalation to finish.">
                            <Button size="sm" variant="ghost" disabled={escalateDispute.isPending} onClick={() => escalateDispute.mutate(dispute.id)}>
                              Send to policy owner
                            </Button>
                          </DisabledTooltip>
                        ) : null}
                      </div>
                      {dispute.status_detail ? <div className="text-xs leading-5 text-muted-foreground">{dispute.status_detail}</div> : null}
                    </div>
                  ))}
                  {disputesQuery.hasNextPage ? (
                    <Button size="sm" variant="ghost" disabled={disputesQuery.isFetchingNextPage} onClick={() => void disputesQuery.fetchNextPage()}>
                      {disputesQuery.isFetchingNextPage ? "Loading…" : "Show earlier feedback"}
                    </Button>
                  ) : null}
                </section>
              ) : null}

              <section className="space-y-3">
                <EvidenceSectionHeader title="Agent results" empty={agentResults.length === 0} />
                {agentResults.length === 0 ? (
                  <div className="text-sm text-muted-foreground">No agent results recorded.</div>
                ) : (
                  agentResults.map((result) => (
                    <div key={result.id} className="space-y-3 border-t border-border pt-3 first:border-t-0 first:pt-0">
                      <div className="flex items-start justify-between gap-3">
                        <div className="min-w-0 space-y-1">
                          <div className="truncate text-sm font-medium text-foreground">{result.agent_provider}</div>
                          <div className="text-xs text-muted-foreground">
                            {result.role}
                            {result.agent_model ? ` · ${result.agent_model}` : ""}
                          </div>
                        </div>
                        <StatusLabel
                          label={statusLabel(result.status)}
                          tone={reviewStatusTone(result.status)}
                          activity={result.status === "queued" ? "indeterminate" : result.status === "running" ? "breathing" : "none"}
                          stateKey={result.status}
                        />
                      </div>
                      {result.raw_output ? (
                        <pre className="max-h-40 overflow-auto whitespace-pre-wrap rounded-md bg-muted/60 p-3 text-xs leading-5 text-muted-foreground">{result.raw_output}</pre>
                      ) : null}
                      {result.structured_result ? (
                        <pre className="max-h-40 overflow-auto whitespace-pre-wrap rounded-md bg-muted/60 p-3 text-xs leading-5 text-muted-foreground">
                          {formatEvidenceJSON(result.structured_result)}
                        </pre>
                      ) : null}
                    </div>
                  ))
                )}
              </section>

              <section className="space-y-3">
                <EvidenceSectionHeader title="Findings" empty={findings.length === 0} />
                {findings.length === 0 ? (
                  <div className="text-sm text-muted-foreground">No findings recorded.</div>
                ) : (
                  <>
                    <div className="text-xs leading-5 text-muted-foreground">
                      P0 and P1 findings block approval. P2 and P3 findings are advisory and are not posted as inline GitHub comments.
                    </div>
                    {findings.map((finding) => (
                      <div key={finding.id} className="space-y-2 border-t border-border pt-3 first:border-t-0 first:pt-0">
                        <div className="flex items-start justify-between gap-3">
                          <div className="min-w-0 space-y-1">
                            <div className="text-sm font-medium text-foreground">{finding.summary}</div>
                            <div className="text-xs text-muted-foreground">{formatFindingLocation(finding)}</div>
                          </div>
                          <Badge variant={findingBlocksApproval(finding.severity) ? "destructive" : "outline"}>{findingPriorityLabel(finding.severity)}</Badge>
                        </div>
                        <div className="text-sm leading-6 text-muted-foreground">{finding.body}</div>
                      </div>
                    ))}
                  </>
                )}
              </section>

              <section className="space-y-3">
                <EvidenceSectionHeader title="Prompt records" empty={records.length === 0} />
                {records.length === 0 ? (
                  <div className="text-sm text-muted-foreground">No prompt records recorded.</div>
                ) : (
                  records.map((record) => (
                    <div key={record.id} className="space-y-3 border-t border-border pt-3 first:border-t-0 first:pt-0">
                      <div className="flex items-start justify-between gap-3">
                        <div className="min-w-0 space-y-1">
                          <div className="truncate text-sm font-medium text-foreground">{record.record_key ?? record.artifact_key}</div>
                          {record.agent_provider ? <div className="text-xs text-muted-foreground">{record.agent_provider}</div> : null}
                        </div>
                        <Badge variant="outline">{record.role}</Badge>
                      </div>
                      <pre className="max-h-40 overflow-auto whitespace-pre-wrap rounded-md bg-muted/60 p-3 text-xs leading-5 text-muted-foreground">{record.content}</pre>
                    </div>
                  ))
                )}
              </section>
            </>
          ) : null}
        </div>
      </SheetContent>
      <Dialog open={disputeDialogOpen} onOpenChange={setDisputeDialogOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{review?.decision === "approved" ? "Report an unsafe approval" : "Ask for reconsideration"}</DialogTitle>
            <DialogDescription>
              Explain what the reviewer got wrong or what evidence it missed. Reconsideration cannot waive deterministic safeguards; a policy owner must change those rules. The
              original decision remains part of the record.
            </DialogDescription>
          </DialogHeader>
          <div className="space-y-4">
            <div className="space-y-2">
              <Label htmlFor="code-review-dispute-body">What should be reconsidered?</Label>
              <Textarea
                id="code-review-dispute-body"
                value={disputeBody}
                maxLength={8000}
                rows={5}
                placeholder="Describe the part of the decision you disagree with…"
                onChange={(event) => setDisputeBody(event.target.value)}
              />
            </div>
            {reasonCodes.length > 0 ? (
              <div className="space-y-2">
                <Label>Contested policy reasons</Label>
                <div className="space-y-2">
                  {reasonCodes.map((code) => (
                    <Label key={code} className="flex items-center gap-2 font-normal">
                      <Checkbox
                        checked={selectedReasonCodes.includes(code)}
                        onCheckedChange={(checked) => setSelectedReasonCodes((current) => (checked ? [...current, code] : current.filter((value) => value !== code)))}
                      />
                      {codeReviewDisputeStatusLabel(code)}
                    </Label>
                  ))}
                </div>
              </div>
            ) : null}
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={() => setDisputeDialogOpen(false)}>
              Cancel
            </Button>
            <DisabledTooltip
              disabled={createDispute.isPending || disputeBody.trim().length === 0}
              content={createDispute.isPending ? "Wait for the feedback to be recorded." : "Describe what should be reconsidered."}
            >
              <Button disabled={createDispute.isPending || disputeBody.trim().length === 0} onClick={() => createDispute.mutate()}>
                {createDispute.isPending ? "Recording…" : "Record feedback"}
              </Button>
            </DisabledTooltip>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </Sheet>
  );
}

function EvidenceMetric({ label, value }: { label: string; value: number }) {
  return (
    <div className="rounded-md border border-border px-3 py-2">
      <div className="text-lg font-medium text-foreground">{value}</div>
      <div className="text-xs text-muted-foreground">{label}</div>
    </div>
  );
}

function EvidenceSectionHeader({ title, empty }: { title: string; empty: boolean }) {
  return (
    <div className="flex items-center justify-between gap-3">
      <div className="text-sm font-medium text-foreground">{title}</div>
      {empty ? <div className="text-xs text-muted-foreground">None</div> : null}
    </div>
  );
}

function formatEvidenceJSON(value: unknown): string {
  if (typeof value === "string") return value;
  try {
    return JSON.stringify(value, null, 2);
  } catch {
    return String(value);
  }
}

function formatFindingLocation(finding: NonNullable<CodeReviewEvidence["findings"]>[number]): string {
  if (!finding.path) return "General finding";
  if (finding.start_line && finding.end_line && finding.end_line !== finding.start_line) {
    return `${finding.path}:${finding.start_line}-${finding.end_line}`;
  }
  if (finding.start_line) return `${finding.path}:${finding.start_line}`;
  return finding.path;
}

function findingBlocksApproval(severity: NonNullable<CodeReviewEvidence["findings"]>[number]["severity"]): boolean {
  return severity === "critical" || severity === "high";
}

function findingPriorityLabel(severity: NonNullable<CodeReviewEvidence["findings"]>[number]["severity"]): string {
  switch (severity) {
    case "critical":
      return "P0 · Blocking";
    case "high":
      return "P1 · Blocking";
    case "medium":
      return "P2 · Advisory";
    case "low":
    case "info":
      return "P3 · Advisory";
  }
}
