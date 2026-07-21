"use client";

import Link from "next/link";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import type { ClipboardEvent, ComponentProps, KeyboardEvent, ReactNode } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { parseAsStringLiteral, useQueryState } from "nuqs";
import {
  AlertTriangle,
  ChevronDown,
  CircleHelp,
  ClipboardCheck,
  ExternalLink,
  FileSearch,
  Plus,
  PowerOff,
  Settings2,
  SlidersHorizontal,
  Trash2,
  Users,
} from "lucide-react";
import { PageHeader } from "@/components/page-header";
import { EmptyState } from "@/components/empty-state";
import { ResourceRow } from "@/components/resource-row";
import { SectionGroup } from "@/components/section-group";
import { StatusLabel, type StatusTone } from "@/components/status-label";
import { Button } from "@/components/ui/button";
import { DisabledTooltip } from "@/components/ui/disabled-tooltip";
import { ErrorNotice } from "@/components/ui/error-notice";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Select, SelectContent, SelectGroup, SelectItem, SelectLabel, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "@/components/ui/collapsible";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Switch } from "@/components/ui/switch";
import { RadioGroup, RadioGroupItem } from "@/components/ui/radio-group";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
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
import { buildCodeReviewStreamURL, SSE_EVENT } from "@/lib/sse";
import { useResourceSSE } from "@/lib/use-resource-sse";
import { pollMs } from "@/lib/poll-intervals";
import { useAutosave, type UseAutosaveResult } from "@/hooks/useAutosave";
import { useAutosaveNumericField } from "@/hooks/useAutosaveNumericField";
import { useDebouncedTextField } from "@/hooks/useDebouncedTextField";
import { useOpenCodeAvailability, type OpenCodeModelAvailability } from "@/hooks/use-opencode-models";
import { useAuth } from "@/hooks/use-auth";
import { AutosaveIndicator } from "@/components/AutosaveIndicator";
import { applyCodeReviewPolicyOptimistic, coalesceCodeReviewPolicy } from "@/lib/code-review-autosave";
import { AGENTS_BY_KEY, availableAgentModelGroups, modelOptionLabel, pmUsableResolvedCredentials, type AgentModelGroup } from "@/lib/agents";
import type {
  CodingCredentialSummary,
  CodeReviewApprovalMode,
  CodeReviewDecision,
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

const ALL_REPOSITORIES = "all";
const NO_REPOSITORY = "none";
const ALL_OUTCOMES = "all";
const ALL_RISKS = "all";
const ALL_STATUSES = "all";
const AUTOMATICALLY_APPROVED = "automatically_approved" satisfies CodeReviewListOutcome;
const COMPLETED_NOT_APPROVED = "completed_not_approved" satisfies CodeReviewListOutcome;
const OUTCOME_FILTER_VALUES = [ALL_OUTCOMES, AUTOMATICALLY_APPROVED, COMPLETED_NOT_APPROVED, "needs_human_review", "comment_only", "blocked"] as const;
type OutcomeFilter = (typeof OUTCOME_FILTER_VALUES)[number];
const NO_TEMPLATE = "none";
// Coalesce a burst of SSE lifecycle events into a single list refetch.
const CODE_REVIEW_INVALIDATE_COALESCE_MS = 300;
const MAX_REVIEWER_MODELS = 3;
const CODE_REVIEW_PROMPT_MAX_LENGTH = 8000;
const DEFAULT_AUTOMATED_APPROVAL_POLICY = `Automatically approve routine, well-tested changes when:
- the intent is clear and the change has a small, understandable scope
- there are no blocking findings
- the implementation follows established repository patterns
- the available testing evidence is appropriate for the change

Require human review when:
- the change affects authentication, billing, permissions, infrastructure, or production data
- the change introduces a new architectural pattern or crosses unclear ownership boundaries
- reviewers disagree or the risk cannot be evaluated confidently
- the intended behavior cannot be determined from the pull request and repository context

Evaluate the pull request independently. Disregard existing human review comments, review decisions, and review threads, whether open or resolved. Unresolved human review threads must not count against approval.`;
const APPLICABILITY_KIND_LABELS: Record<CodeReviewDescriptionApplicabilityKind, string> = {
  all: "All PRs",
  nontrivial: "Nontrivial",
  frontend_or_ui_visible: "Frontend/UI",
  paths: "Paths",
  categories: "Categories",
  tests_changed: "Tests changed",
};
const DEFAULT_NONTRIVIAL_MIN_FILES = 2;
const DEFAULT_NONTRIVIAL_MIN_LINES = 31;
type DescriptionRequirement = CodeReviewPolicyConfig["description_policy"]["requirements"][number];
type DescriptionApplicability = NonNullable<DescriptionRequirement["applies_when"]>;
const QUALITY_GATE_DESCRIPTIONS = {
  requirePassingChecks:
    "Blocks approval until the PR's required GitHub checks are passing. When off, checks do not independently block approval; reviews can still leave comments either way.",
  excludeSensitivePaths:
    "Treats changes matching sensitive paths as blocking approval. When off, sensitive-path matches do not independently require human review.",
  requireUpToDate:
    "Requires the PR branch to be current with its base branch before approval. When off, branch freshness does not independently block approval.",
  allowPolicyChanges:
    "Allows approval of PRs that modify review policy or automation configuration. The safer default is off, which always requires a human for those changes.",
  disagreementBlocks:
    "Blocks approval when reviewer agents disagree. When off, disagreement is still visible but does not independently veto approval unless another safeguard does.",
  allowForks: "Allows approval decisions for PRs opened from forks. The safer default is off, which keeps forked PRs comment-only.",
} as const;
const NUMBER_POLICY_DESCRIPTIONS: Record<string, string> = {
  "Files changed": "Maximum number of changed files eligible for automatic approval. Reviews still leave comments above this deterministic limit.",
  "Lines changed": "Maximum total changed lines eligible for automatic approval. Reviews still leave comments above this deterministic limit.",
  "Inline comments":
    "Maximum inline findings posted to GitHub. Extra findings remain in review evidence; this limit does not make a pull request eligible for approval.",
  "Reviewer quorum":
    "Minimum configured reviewer agents that must return usable results before automatic approval is eligible. It cannot exceed the reviewer count.",
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

function decisionLabel(review: CodeReviewListItem): string {
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
  if (wasAutomaticallyApproved(review)) return "success";
  if (review.decision === "blocked") return "destructive";
  if (review.decision === "needs_human_review") return "warning";
  return "neutral";
}

function reviewStatusTone(status: string): StatusTone {
  if (status === "completed") return "success";
  if (status === "failed" || status === "stale") return "destructive";
  if (status === "running" || status === "queued") return "primary";
  return "neutral";
}

function reviewRiskTone(review: CodeReviewListItem): StatusTone {
  return review.acceptable ? "success" : "warning";
}

function ReviewTitle({ review }: { review: CodeReviewListItem }) {
  const title = `#${review.github_pr_number} ${review.pull_request_title}`;

  if (!review.github_review_url) return title;

  return (
    <Link
      href={review.github_review_url}
      target="_blank"
      rel="noreferrer"
      title="Open final review"
      className="underline-offset-4 hover:underline focus-visible:rounded-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
    >
      {title}
    </Link>
  );
}

function EvidenceButton({
  selected,
  onToggleEvidence,
}: {
  selected: boolean;
  onToggleEvidence: () => void;
}) {
  return (
    <Button
      variant={selected ? "secondary" : "ghost"}
      size="sm"
      className="-ml-2 min-h-8 px-2 text-muted-foreground"
      onClick={onToggleEvidence}
    >
      <FileSearch className="h-4 w-4" />
      Evidence
    </Button>
  );
}

function ReviewOutcome({
  review,
  selected,
  onToggleEvidence,
}: {
  review: CodeReviewListItem;
  selected: boolean;
  onToggleEvidence: () => void;
}) {
  return (
    <div className="space-y-1">
      <StatusLabel label={decisionLabel(review)} tone={reviewDecisionTone(review)} indicator={false} />
      <EvidenceButton selected={selected} onToggleEvidence={onToggleEvidence} />
    </div>
  );
}

function ReviewActions({ review }: { review: CodeReviewListItem }) {
  return (
    <TooltipProvider>
      <div className="grid w-full grid-cols-2 gap-2 md:flex md:w-auto md:flex-wrap md:justify-end">
        <Button className="min-h-11 justify-center md:min-h-0" variant="ghost" size="sm" asChild>
          <Link href={`/sessions/${review.session_id}`}>
            <ExternalLink className="h-4 w-4 md:hidden" />
            Session
          </Link>
        </Button>
        <Tooltip>
          <TooltipTrigger asChild>
            <Button
              className="min-h-11 justify-center md:size-7 md:min-h-0"
              variant="ghost"
              size="sm"
              asChild
              aria-label="Open pull request"
            >
              <Link href={review.github_pr_url} target="_blank" rel="noreferrer">
                <ExternalLink className="h-4 w-4" />
                <span className="md:sr-only">Pull request</span>
              </Link>
            </Button>
          </TooltipTrigger>
          <TooltipContent className="hidden md:block">Open pull request</TooltipContent>
        </Tooltip>
      </div>
    </TooltipProvider>
  );
}

function reviewStatusLabel(review: CodeReviewListItem): string {
  if (review.stale || review.status === "stale") return "Stale";
  if (review.status === "completed") return "Completed";
  if (review.status === "failed") return "Failed";
  if (review.status === "running") return "Running";
  if (review.status === "queued") return "Queued";
  if (review.status === "cancelled") return "Cancelled";
  return review.status;
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

export default function CodeReviewsPage() {
  const queryClient = useQueryClient();
  const { user } = useAuth();
  const canManagePolicy = user?.role === "admin";
  const [repositoryFilter, setRepositoryFilter] = useState(ALL_REPOSITORIES);
  const [githubRepositoryId, setGitHubRepositoryId] = useState(NO_REPOSITORY);
  const [outcomeFilter, setOutcomeParam] = useQueryState(
    "outcome",
    parseAsStringLiteral(OUTCOME_FILTER_VALUES).withDefault(ALL_OUTCOMES),
  );
  const [riskFilter, setRiskFilter] = useState(ALL_RISKS);
  const [statusFilter, setStatusFilter] = useState(ALL_STATUSES);
  const [search, setSearch] = useState("");
  const [selectedTemplateKey, setSelectedTemplateKey] = useState(NO_TEMPLATE);
  const [pendingTemplateApply, setPendingTemplateApply] = useState<{ key: string; title: string } | null>(null);
  const [selectedEvidenceSessionId, setSelectedEvidenceSessionId] = useState<string | null>(null);
  const [mobileFiltersOpen, setMobileFiltersOpen] = useState(false);
  const [editingRequirementKey, setEditingRequirementKey] = useState<string | null>(null);
  const [promptExample, setPromptExample] = useState<{ field: "review_instructions" | "automated_approval_policy"; example: CodeReviewPromptExampleOption | CodeReviewAutomatedApprovalExampleOption } | null>(null);
  const [invalidPolicyField, setInvalidPolicyField] = useState<string | null>(null);
  const promptDraftsRef = useRef<Partial<Record<"review_instructions" | "automated_approval_policy", PromptDraftHandle>>>({});
  const saveSourceByConfigRef = useRef(new WeakMap<CodeReviewPolicyConfig, CodeReviewPolicyEditSource>());
  const persistedPromptsRef = useRef({ scope: "", review_instructions: "", automated_approval_policy: DEFAULT_AUTOMATED_APPROVAL_POLICY });
  const setOutcomeFilter = useCallback(
    (value: string) => {
      void setOutcomeParam(value as OutcomeFilter);
    },
    [setOutcomeParam],
  );
  const registerPromptDraft = useCallback((field: "review_instructions" | "automated_approval_policy", handle: PromptDraftHandle) => {
    promptDraftsRef.current[field] = handle;
  }, []);
  const reviewRepositoryId = repositoryFilter === ALL_REPOSITORIES ? undefined : repositoryFilter;
  const reviewFilters = useMemo(
    () => ({
      repository_id: reviewRepositoryId,
      decision:
        outcomeFilter !== ALL_OUTCOMES && outcomeFilter !== AUTOMATICALLY_APPROVED && outcomeFilter !== COMPLETED_NOT_APPROVED
          ? (outcomeFilter as CodeReviewDecision)
          : undefined,
      outcome: outcomeFilter === AUTOMATICALLY_APPROVED || outcomeFilter === COMPLETED_NOT_APPROVED ? (outcomeFilter as CodeReviewListOutcome) : undefined,
      risk: riskFilter === ALL_RISKS ? undefined : (riskFilter as "acceptable" | "needs_review"),
      status: statusFilter === ALL_STATUSES ? undefined : (statusFilter as CodeReviewSessionStatus),
      search: search.trim() || undefined,
      limit: 100,
    }),
    [outcomeFilter, reviewRepositoryId, riskFilter, search, statusFilter],
  );
  const githubRepositorySelected = githubRepositoryId !== NO_REPOSITORY;

  const repositoriesQuery = useQuery({
    queryKey: queryKeys.repositories.all,
    queryFn: () => api.repositories.list(),
  });
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
    if (invalidateTimerRef.current) return;
    invalidateTimerRef.current = setTimeout(() => {
      invalidateTimerRef.current = null;
      void queryClient.invalidateQueries({
        queryKey: queryKeys.codeReviews.lists(),
      });
    }, pollMs(CODE_REVIEW_INVALIDATE_COALESCE_MS));
  }, [queryClient]);
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
    queryKey: queryKeys.codeReviews.list(reviewFilters),
    queryFn: () => api.codeReviews.list(reviewFilters),
    refetchInterval: codeReviewStreamHealthy ? pollMs(30_000) : pollMs(5_000),
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
  const githubTriggerQuery = useQuery({
    queryKey: queryKeys.codeReviews.githubTrigger(githubRepositorySelected ? githubRepositoryId : null),
    queryFn: () => api.codeReviews.getGitHubTrigger(githubRepositoryId),
    enabled: githubRepositorySelected,
  });
  const templatesQuery = useQuery({
    queryKey: queryKeys.codeReviews.templates,
    queryFn: () => api.codeReviews.templates(),
  });
  const promptExamplesQuery = useQuery({
    queryKey: queryKeys.codeReviews.promptExamples,
    queryFn: () => api.codeReviews.promptExamples(),
  });
  const evidenceQuery = useQuery({
    queryKey: queryKeys.codeReviews.evidence(selectedEvidenceSessionId ?? ""),
    queryFn: () => api.codeReviews.evidence(selectedEvidenceSessionId ?? ""),
    enabled: Boolean(selectedEvidenceSessionId),
  });

  // The policy is autosaved as a single whole-config PUT. Each control reads
  // the live config straight from the query cache and commits a fully-merged
  // config built from the freshest cache value (per settings/AGENTS.md), so
  // back-to-back edits never clobber one another.
  const config = policyQuery.data?.data.config ?? null;
  if (config && persistedPromptsRef.current.scope !== "organization") {
    persistedPromptsRef.current = { scope: "organization", review_instructions: config.review_instructions, automated_approval_policy: config.automated_approval_policy };
  }
  const viewedScopeRef = useRef<string | null>(null);
  useEffect(() => {
    if (!config) return;
    if (viewedScopeRef.current === "organization") return;
    viewedScopeRef.current = "organization";
    trackCodeReviewPolicyEvent({ event: "code_review_policy_viewed", scope: "organization", configured: policyQuery.data?.data.source !== "default" });
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
      trackCodeReviewPolicyEvent({ event: "code_review_github_setup_completed", scope: "repository", configured: true });
    },
    onError: () => trackCodeReviewPolicyEvent({ event: "code_review_github_setup_failed", scope: "repository", configured: false }),
  });
  const deleteGitHubTrigger = useMutation({
    mutationFn: (targetRepositoryId: string) => api.codeReviews.deleteGitHubTrigger(targetRepositoryId),
    onSuccess: (_data, targetRepositoryId) => {
      void queryClient.invalidateQueries({
        queryKey: queryKeys.codeReviews.githubTrigger(targetRepositoryId),
      });
    },
  });
  const reviews = useMemo(() => reviewsQuery.data?.data ?? [], [reviewsQuery.data?.data]);
  const selectedEvidenceReview = useMemo(
    () => reviews.find((review) => review.session_id === selectedEvidenceSessionId) ?? null,
    [reviews, selectedEvidenceSessionId],
  );
  const repositories = repositoriesQuery.data?.data ?? [];
  const templates = templatesQuery.data?.data ?? [];
  const selectedTemplate = templates.find((template) => template.key === selectedTemplateKey);
  const orgSettings = (settingsQuery.data?.data?.settings ?? {}) as OrgSettings;
  const orgCodingCredentials = useMemo(() => orgCodingCredentialsQuery.data?.data ?? [], [orgCodingCredentialsQuery.data?.data]);
  const codeReviewResolvedCredentials = useMemo(
    () => pmUsableResolvedCredentials(resolvedCredentialsQuery.data?.data ?? []),
    [resolvedCredentialsQuery.data?.data],
  );
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
  const selectedTemplateAlreadyApplied = useMemo(() => {
    if (!selectedTemplate || !config) return false;
    return JSON.stringify(config) === JSON.stringify(selectedTemplate.config);
  }, [config, selectedTemplate]);
  useEffect(() => {
    setPendingTemplateApply(null);
  }, [selectedTemplateKey]);
  useEffect(() => {
    if (!pendingTemplateApply) return;
    if (autosave.status === "saved") {
      toast.success(`Applied ${pendingTemplateApply.title}`);
      setPendingTemplateApply(null);
    } else if (autosave.status === "error") {
      setPendingTemplateApply(null);
    }
  }, [autosave.status, pendingTemplateApply]);
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
    if (next) { saveSourceByConfigRef.current.set(next, source); setInvalidPolicyField(null); autosave.save(next); }
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
    updater: (
      requirement: CodeReviewPolicyConfig["description_policy"]["requirements"][number],
    ) => CodeReviewPolicyConfig["description_policy"]["requirements"][number],
  ) => {
    commitPolicy((next) => {
      const index = next.description_policy.requirements.findIndex((requirement) => requirement.key === key);
      if (index === -1) return;
      next.description_policy.requirements[index] = updater(next.description_policy.requirements[index]);
    });
  };

  return (
    <main className="min-h-full bg-background">
      <div className="mx-auto flex w-full max-w-7xl flex-col gap-5 px-4 py-5 sm:px-6 lg:px-8">
        <PageHeader title="Code reviews" description="Bot-requested PR reviews, acceptable-risk policy, and review outcomes." />

        <Tabs defaultValue="reviews" className="space-y-4">
          <TabsList>
            <TabsTrigger value="reviews">
              <ClipboardCheck className="h-4 w-4" />
              Reviews
            </TabsTrigger>
            <TabsTrigger value="config">
              <Settings2 className="h-4 w-4" />
              Policy
            </TabsTrigger>
          </TabsList>

          <TabsContent value="reviews" className="space-y-3">
            <Button
              type="button"
              variant="outline"
              size="sm"
              className="w-full justify-between md:hidden"
              aria-expanded={mobileFiltersOpen}
              aria-controls="code-review-filters"
              onClick={() => setMobileFiltersOpen((open) => !open)}
            >
              <span className="flex items-center gap-2">
                <SlidersHorizontal className="h-4 w-4" />
                Filter reviews
              </span>
              <ChevronDown className={`h-4 w-4 transition-transform ${mobileFiltersOpen ? "rotate-180" : ""}`} />
            </Button>
            <div
              id="code-review-filters"
              className={`${mobileFiltersOpen ? "grid" : "hidden"} gap-3 rounded-xl border border-border bg-card p-3 shadow-sm md:grid md:grid-cols-[minmax(12rem,18rem)_minmax(10rem,12rem)_minmax(10rem,12rem)_minmax(10rem,12rem)_1fr] md:rounded-none md:border-0 md:bg-transparent md:p-0 md:shadow-none`}
            >
              <FilterSelect label="Repository" value={repositoryFilter} onValueChange={setRepositoryFilter}>
                <SelectItem value={ALL_REPOSITORIES}>All repositories</SelectItem>
                {repositories.map((repo) => (
                  <SelectItem key={repo.id} value={repo.id}>
                    {repo.full_name}
                  </SelectItem>
                ))}
              </FilterSelect>
              <FilterSelect label="Outcome" value={outcomeFilter} onValueChange={setOutcomeFilter}>
                <SelectItem value={ALL_OUTCOMES}>All outcomes</SelectItem>
                <SelectItem value={AUTOMATICALLY_APPROVED}>Automatically approved</SelectItem>
                <SelectItem value={COMPLETED_NOT_APPROVED}>Ran successfully — not approved</SelectItem>
                <SelectItem value="needs_human_review">Needs human review</SelectItem>
                <SelectItem value="comment_only">Comment-only decision</SelectItem>
                <SelectItem value="blocked">Blocked</SelectItem>
              </FilterSelect>
              <FilterSelect label="Risk" value={riskFilter} onValueChange={setRiskFilter}>
                <SelectItem value={ALL_RISKS}>All risk</SelectItem>
                <SelectItem value="acceptable">Acceptable</SelectItem>
                <SelectItem value="needs_review">Needs review</SelectItem>
              </FilterSelect>
              <FilterSelect label="Status" value={statusFilter} onValueChange={setStatusFilter}>
                <SelectItem value={ALL_STATUSES}>All statuses</SelectItem>
                <SelectItem value="queued">Queued</SelectItem>
                <SelectItem value="running">Running</SelectItem>
                <SelectItem value="completed">Completed</SelectItem>
                <SelectItem value="failed">Failed</SelectItem>
                <SelectItem value="stale">Stale</SelectItem>
                <SelectItem value="cancelled">Cancelled</SelectItem>
              </FilterSelect>
              <div className="flex flex-col gap-2">
                <Label className="text-xs text-muted-foreground">Search</Label>
                <Input value={search} onChange={(event) => setSearch(event.target.value)} placeholder="PR, repo, or title" aria-label="Search code reviews" />
              </div>
            </div>
            <SectionGroup title="Review activity" description="Pull requests reviewed by the team policy and their current outcome.">
            {reviews.length === 0 ? (
              <EmptyState
                icon={ClipboardCheck}
                title="No code review sessions"
                description="Reviews will appear here after the GitHub reviewer bot is requested on a pull request."
              />
            ) : (
              <>
              <Card className="hidden md:flex">
                <CardContent className="p-0">
                  <Table>
                    <TableHeader>
                      <TableRow>
                        <TableHead>PR</TableHead>
                        <TableHead>Repo</TableHead>
                        <TableHead>Risk</TableHead>
                        <TableHead>Outcome</TableHead>
                        <TableHead>Run status</TableHead>
                        <TableHead>Completed</TableHead>
                        <TableHead className="text-right">Actions</TableHead>
                      </TableRow>
                    </TableHeader>
                    <TableBody>
                      {reviews.map((review) => (
                        <TableRow key={review.id}>
                          <TableCell className="min-w-[18rem]">
                            <div className="font-medium text-foreground">
                              <ReviewTitle review={review} />
                            </div>
                            <div className="mt-1 text-xs text-muted-foreground">
                              {review.pull_request_author || "Unknown author"} · {review.head_sha.slice(0, 7)}
                            </div>
                          </TableCell>
                          <TableCell>{review.repository_name || review.github_repo}</TableCell>
                          <TableCell>
                            <StatusLabel
                              label={review.acceptable ? "Acceptable" : "Review needed"}
                              tone={reviewRiskTone(review)}
                              indicator={false}
                            />
                          </TableCell>
                          <TableCell>
                            <ReviewOutcome
                              review={review}
                              selected={selectedEvidenceSessionId === review.session_id}
                                  onToggleEvidence={() => setSelectedEvidenceSessionId((current) => (current === review.session_id ? null : review.session_id))}
                            />
                          </TableCell>
                          <TableCell>
                            <StatusLabel
                              label={reviewStatusLabel(review)}
                              tone={reviewStatusTone(review.stale ? "stale" : review.status)}
                              indicator={false}
                            />
                          </TableCell>
                          <TableCell>{formatDate(review.completed_at)}</TableCell>
                          <TableCell>
                            <ReviewActions review={review} />
                          </TableCell>
                        </TableRow>
                      ))}
                    </TableBody>
                  </Table>
                </CardContent>
              </Card>
              <Card className="divide-y divide-border/70 md:hidden" aria-label="Code review activity">
                {reviews.map((review) => (
                  <ResourceRow
                    key={review.id}
                        title={
                      <span className="break-words text-sm">
                        <ReviewTitle review={review} />
                      </span>
                        }
                        metadata={
                      <span>
                        {review.repository_name || review.github_repo} · {review.pull_request_author || "Unknown author"} · {review.head_sha.slice(0, 7)}
                      </span>
                        }
                        status={
                          <StatusLabel
                            label={reviewStatusLabel(review)}
                            tone={reviewStatusTone(review.stale ? "stale" : review.status)}
                            indicator={false}
                          />
                        }
                        detail={
                      <div className="space-y-2">
                        <div className="flex flex-wrap items-center gap-x-4 gap-y-2 text-foreground">
                          <StatusLabel
                            label={review.acceptable ? "Acceptable" : "Review needed"}
                            tone={reviewRiskTone(review)}
                            indicator={false}
                          />
                              <span>Completed {formatDate(review.completed_at)}</span>
                            </div>
                            <ReviewOutcome
                              review={review}
                              selected={selectedEvidenceSessionId === review.session_id}
                              onToggleEvidence={() => setSelectedEvidenceSessionId((current) => (current === review.session_id ? null : review.session_id))}
                      />
                    </div>
                        }
                        actions={<ReviewActions review={review} />}
                        className="[&_[data-slot=resource-row-actions]]:ml-0"
                      />
                    ))}
                  </Card>
                  <CodeReviewEvidenceSheet
                    review={selectedEvidenceReview}
                    evidence={evidenceQuery.data?.data}
                    isLoading={evidenceQuery.isLoading}
                    error={evidenceQuery.error}
                    onRetry={() => void evidenceQuery.refetch()}
                    open={Boolean(selectedEvidenceReview)}
                    onOpenChange={(open) => {
                      if (!open) setSelectedEvidenceSessionId(null);
                    }}
                  />
                </>
              )}
            </SectionGroup>
          </TabsContent>

          <TabsContent value="config" className="space-y-4">
            <Card>
              <CardHeader className="space-y-1">
                <div className="flex items-center justify-between gap-3">
                  <CardTitle>Review policy</CardTitle>
                  <AutosaveIndicator status={autosave.status} />
                </div>
                <OrganizationPolicyNotice />
              </CardHeader>
              <CardContent className="space-y-5">
                {!canManagePolicy ? <div className="rounded-md border border-border bg-muted/30 p-3 text-sm text-muted-foreground">You have view-only access to this policy. An organization administrator can change review behavior and GitHub setup.</div> : null}
                <fieldset disabled={!canManagePolicy} className="space-y-5">
                <PolicyBehaviorSection
                      config={config}
                  onChange={(outcome) => {
                    const prior = policyOutcome(config);
                    commitPolicy((next) => {
                      if (outcome === "disabled") next.enabled = false;
                      else {
                        next.enabled = true;
                        next.approval_mode = (outcome === "approve" ? "approve_acceptable" : "comment_only") as CodeReviewApprovalMode;
                      }
                    });
                    if ((prior === "disabled") !== (outcome === "disabled")) trackCodeReviewPolicyEvent({ event: "code_review_policy_enabled", scope: "organization", configured: outcome !== "disabled" });
                    if (outcome !== "disabled" && outcome !== prior) trackCodeReviewPolicyEvent({ event: "code_review_approval_mode_changed", scope: "organization", configured: true });
                  }}
                    />

                <PolicyPromptComposers
                  config={config}
                  autosave={autosave}
                  commitPolicy={commitPolicy}
                  examples={promptExamplesQuery.data?.data}
                  examplesError={apiErrorMessage(promptExamplesQuery.error) ?? undefined}
                  onRetryExamples={() => void promptExamplesQuery.refetch()}
                  onChooseExample={(field, example) => { setPromptExample({ field, example }); trackCodeReviewPolicyEvent({ event: "code_review_prompt_example_previewed", scope: "organization", example_key: example.key, configured: true }); }}
                  onDraftHandle={registerPromptDraft}
                  invalidPolicyField={invalidPolicyField}
                />
                </fieldset>

                <div className="space-y-2">
                  <div className="w-full sm:max-w-sm">
                    <FilterSelect
                      label="GitHub reviewer repository"
                      value={githubRepositoryId}
                      onValueChange={setGitHubRepositoryId}
                      info="Choose a repository to configure its GitHub reviewer entry point. The review policy above remains the same for every repository."
                    >
                      <SelectItem value={NO_REPOSITORY}>Select a repository</SelectItem>
                      {repositories.map((repository) => (
                        <SelectItem key={repository.id} value={repository.id}>{repository.full_name}</SelectItem>
                      ))}
                    </FilterSelect>
                  </div>
                  <GitHubTriggerPanel
                    repositorySelected={githubRepositorySelected}
                    trigger={githubTriggerQuery.data?.data}
                    isLoading={githubTriggerQuery.isLoading || githubTriggerQuery.isFetching}
                    errorMessage={apiErrorMessage(githubTriggerQuery.error)}
                    setupErrorMessage={apiErrorMessage(setupGitHubTrigger.error)}
                    setupPending={setupGitHubTrigger.isPending}
                    deletePending={deleteGitHubTrigger.isPending}
                    canManage={canManagePolicy}
                    onSetup={() => githubRepositorySelected && setupGitHubTrigger.mutate(githubRepositoryId)}
                    onDelete={() => githubRepositorySelected && deleteGitHubTrigger.mutate(githubRepositoryId)}
                  />
                </div>

                <PolicySummary config={config} repositorySelected={githubRepositorySelected} githubTriggerStatus={githubTriggerQuery.data?.data?.status} />

                <fieldset disabled={!canManagePolicy}>
                  <AdvancedPolicySettings
                    selectedTemplateKey={selectedTemplateKey}
                    setSelectedTemplateKey={setSelectedTemplateKey}
                    templates={templates}
                    selectedTemplate={selectedTemplate}
                    selectedTemplateAlreadyApplied={selectedTemplateAlreadyApplied}
                    config={config}
                    readLatestConfig={readLatestConfig}
                    setPendingTemplateApply={setPendingTemplateApply}
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
              </CardContent>
            </Card>
            <CodeReviewPromptExampleDialog
              selection={promptExample}
              currentConfig={config}
              currentDraftValue={promptExample ? promptDraftsRef.current[promptExample.field]?.value : undefined}
              persistedValue={promptExample ? persistedPromptsRef.current[promptExample.field] : undefined}
              onOpenChange={(open) => { if (!open) setPromptExample(null); }}
              onApply={() => {
                if (!promptExample) return;
                const value = "instructions" in promptExample.example ? promptExample.example.instructions : promptExample.example.policy;
                promptDraftsRef.current[promptExample.field]?.replace(value);
                commitPolicy((next) => { next[promptExample.field] = value; }, "example");
                trackCodeReviewPolicyEvent({ event: "code_review_prompt_example_applied", scope: "organization", source: "example", example_key: promptExample.example.key, character_bucket: promptCharacterBucket(value), configured: true });
                setPromptExample(null);
              }}
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
          </TabsContent>
        </Tabs>
      </div>
    </main>
  );
}

type PromptDraftHandle = { value: string; dirty: boolean; flush(): void; replace(value: string): void };

function PolicyPromptComposers({
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
  config: CodeReviewPolicyConfig | null;
  autosave: UseAutosaveResult<CodeReviewPolicyConfig>;
  commitPolicy: (mutate: (next: CodeReviewPolicyConfig) => void, source?: CodeReviewPolicyEditSource) => void;
  examples?: { review_instructions: CodeReviewPromptExampleOption[]; automated_approval_policies: CodeReviewAutomatedApprovalExampleOption[] };
  examplesError?: string;
  onRetryExamples: () => void;
  onChooseExample: (field: "review_instructions" | "automated_approval_policy", example: CodeReviewPromptExampleOption | CodeReviewAutomatedApprovalExampleOption) => void;
  onDraftHandle: (field: "review_instructions" | "automated_approval_policy", handle: PromptDraftHandle) => void;
  invalidPolicyField: string | null;
}) {
  return (
    <div className="space-y-4">
      {examplesError ? <ErrorNotice title="Could not load prompt examples" description={examplesError} action={{ label: "Retry", onClick: onRetryExamples }} /> : null}
      <CodeReviewAutomatedApprovalPolicyComposer
        value={config?.automated_approval_policy ?? ""}
        disabled={!config}
        hidden={config?.approval_mode !== "approve_acceptable"}
        autosave={autosave}
        onCommit={(value) => { commitPolicy((next) => { next.automated_approval_policy = value.trim(); }); trackCodeReviewPolicyEvent({ event: "code_review_prompt_edited", scope: "organization", source: "manual", character_bucket: promptCharacterBucket(value.trim()), configured: true }); }}
        resetValue={DEFAULT_AUTOMATED_APPROVAL_POLICY}
        onReset={() => { const resetValue = DEFAULT_AUTOMATED_APPROVAL_POLICY; commitPolicy((next) => { next.automated_approval_policy = resetValue; }, "reset"); trackCodeReviewPolicyEvent({ event: "code_review_prompt_edited", scope: "organization", source: "reset", character_bucket: promptCharacterBucket(resetValue), configured: true }); }}
        resetLabel="Reset to default"
        examples={examples?.automated_approval_policies ?? []}
        onChooseExample={(example) => onChooseExample("automated_approval_policy", example)}
        onDraftHandle={(handle) => onDraftHandle("automated_approval_policy", handle)}
        focusOnError={invalidPolicyField === "automated_approval_policy"}
      />
      <div className="rounded-md border border-border bg-muted/30 px-4 py-3 text-sm">
        <div className="font-medium text-foreground">Hard safeguards</div>
        <p className="mt-1 text-muted-foreground">Passing checks, sensitive paths, size limits, quorum, and disagreement rules remain deterministic and can veto approval.</p>
      </div>
      <CodeReviewInstructionsComposer
        value={config?.review_instructions ?? ""}
        disabled={!config}
        autosave={autosave}
        onCommit={(value) => { commitPolicy((next) => { next.review_instructions = value.trim(); }); trackCodeReviewPolicyEvent({ event: "code_review_prompt_edited", scope: "organization", source: "manual", character_bucket: promptCharacterBucket(value.trim()), configured: true }); }}
        resetValue=""
        onReset={() => { const resetValue = ""; commitPolicy((next) => { next.review_instructions = resetValue; }, "reset"); trackCodeReviewPolicyEvent({ event: "code_review_prompt_edited", scope: "organization", source: "reset", character_bucket: promptCharacterBucket(resetValue), configured: true }); }}
        resetLabel="Clear instructions"
        examples={examples?.review_instructions ?? []}
        onChooseExample={(example) => onChooseExample("review_instructions", example)}
        onDraftHandle={(handle) => onDraftHandle("review_instructions", handle)}
        focusOnError={invalidPolicyField === "review_instructions"}
      />
    </div>
  );
}

type CodeReviewPromptComposerProps = {
  value: string; disabled: boolean; hidden?: boolean; autosave: UseAutosaveResult<CodeReviewPolicyConfig>;
  onCommit: (value: string) => void; onReset: () => void; resetValue: string; resetLabel: string;
  examples: Array<CodeReviewPromptExampleOption | CodeReviewAutomatedApprovalExampleOption>;
  onChooseExample: (example: CodeReviewPromptExampleOption | CodeReviewAutomatedApprovalExampleOption) => void;
  onDraftHandle: (handle: PromptDraftHandle) => void;
  focusOnError: boolean;
};

function CodeReviewAutomatedApprovalPolicyComposer(props: CodeReviewPromptComposerProps) {
  return <CodeReviewPromptComposerBase {...props} title="Automated approval policy" description="Guides the orchestrator's approve-or-escalate recommendation. Deterministic hard safeguards can still block approval." tooltip="Used only by the orchestrator when automatic approval is enabled. It never replaces /review instructions and cannot bypass hard safeguards. A non-empty value is required for automatic approval." required />;
}

function CodeReviewInstructionsComposer(props: CodeReviewPromptComposerProps) {
  return <CodeReviewPromptComposerBase {...props} title="Additional review instructions (optional)" description="Add team-specific priorities or comment style. Empty means every reviewer uses its native /review behavior without extra guidance." tooltip="Optional guidance appended after each reviewer's native /review command and also supplied to the orchestrator. Leave empty for built-in review behavior; it does not grant approval authority." secondary />;
}

function CodeReviewPromptComposerBase({ title, description, tooltip, value, disabled, hidden, required, autosave, onCommit, onReset, resetValue, resetLabel, secondary, examples, onChooseExample, onDraftHandle, focusOnError }: {
  title: string; description: string; tooltip: string; value: string; disabled: boolean; hidden?: boolean; required?: boolean;
  autosave: UseAutosaveResult<CodeReviewPolicyConfig>; onCommit: (value: string) => void; onReset: () => void; resetValue: string; resetLabel: string; secondary?: boolean;
  examples: Array<CodeReviewPromptExampleOption | CodeReviewAutomatedApprovalExampleOption>; onChooseExample: (example: CodeReviewPromptExampleOption | CodeReviewAutomatedApprovalExampleOption) => void;
  onDraftHandle: (handle: PromptDraftHandle) => void;
  focusOnError: boolean;
}) {
  // Gate and count on the trimmed value: that is exactly what onCommit persists
  // (`value.trim()`) and what the backend validates, so basing the length check
  // on the raw value would reject content that fits the limit once trailing
  // whitespace (e.g. a pasted trailing newline) is stripped.
  const invalidValue = (next: string) => [...next.trim()].length > CODE_REVIEW_PROMPT_MAX_LENGTH || Boolean(required && !next.trim());
  const field = useDebouncedTextField({ serverValue: value, onCommit: (next) => { if (!invalidValue(next)) onCommit(next); }, preserveLocalOnServerChange: autosave.status === "error" });
  const textareaRef = useRef<HTMLTextAreaElement>(null);
  useEffect(() => { onDraftHandle({ value: field.value, dirty: field.dirty, flush: field.flush, replace: field.replace }); }, [field.value, field.dirty, field.flush, field.replace, onDraftHandle]);
  useEffect(() => { if (focusOnError) textareaRef.current?.focus(); }, [focusOnError]);
  const count = [...field.value.trim()].length;
  const invalid = count > CODE_REVIEW_PROMPT_MAX_LENGTH || Boolean(required && !field.value.trim());
  return (
    <section className={`${hidden ? "hidden" : ""} space-y-2 rounded-md border border-border p-4 ${secondary ? "bg-muted/10" : "bg-card shadow-sm"}`} aria-label={title}>
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div className="flex items-center gap-1.5"><Label htmlFor={`prompt-${title.replaceAll(" ", "-")}`}>{title}</Label><SettingInfoTooltip label={title} description={tooltip} /></div>
        <AutosaveIndicator status={autosave.status} />
      </div>
      <p className="text-xs text-muted-foreground">{description}</p>
      <Textarea ref={textareaRef} id={`prompt-${title.replaceAll(" ", "-")}`} value={field.value} disabled={disabled} rows={secondary ? 6 : 10} onChange={(event) => field.onChange(event.target.value)} onBlur={field.onBlur} aria-invalid={invalid || focusOnError} aria-describedby={`prompt-count-${title.replaceAll(" ", "-")}`} />
      <div className="flex flex-wrap items-center justify-between gap-2">
        <span id={`prompt-count-${title.replaceAll(" ", "-")}`} className={`text-xs ${invalid ? "text-destructive" : "text-muted-foreground"}`}>{count} / {CODE_REVIEW_PROMPT_MAX_LENGTH}</span>
        <div className="flex flex-wrap gap-1">
          {examples.length > 0 ? <span className="mr-1 inline-flex items-center gap-1 text-xs text-muted-foreground">Prompt examples <SettingInfoTooltip label={`${title} prompt examples`} description="Previews editable starter text for this prompt only. Applying an example creates a normal policy version and never changes safeguards, agents, enablement, or outcome." /></span> : null}
          {examples.length > 0 ? <Select value="" disabled={disabled} onValueChange={(key) => { const example = examples.find((candidate) => candidate.key === key); if (example) onChooseExample(example); }}><SelectTrigger className="h-8 w-full sm:w-56" aria-label={`${title} prompt example`}><SelectValue placeholder="Choose example" /></SelectTrigger><SelectContent>{examples.map((example) => <SelectItem key={example.key} value={example.key}>{example.title}</SelectItem>)}</SelectContent></Select> : null}
          <Button type="button" variant="ghost" size="sm" disabled={disabled} onClick={() => { field.replace(resetValue); onReset(); }}>{resetLabel}</Button>
        </div>
      </div>
      {invalid ? <p className="text-xs text-destructive">{count > CODE_REVIEW_PROMPT_MAX_LENGTH ? "Prompt is too long." : "An automated approval policy is required while approval is enabled."}</p> : null}
    </section>
  );
}

function OrganizationPolicyNotice() {
  return (
    <div className="flex items-start gap-2 rounded-md border border-border bg-muted/30 px-4 py-3 text-sm">
      <Users className="mt-0.5 h-4 w-4 shrink-0 text-muted-foreground" />
      <div>
        <div className="font-medium text-foreground">One policy for every repository</div>
        <p className="mt-1 text-xs text-muted-foreground">Changes apply to new code reviews across the organization. Repository-specific overrides are not available.</p>
      </div>
    </div>
  );
}

function CodeReviewPromptExampleDialog({ selection, currentConfig, currentDraftValue, persistedValue, onOpenChange, onApply }: {
  selection: { field: "review_instructions" | "automated_approval_policy"; example: CodeReviewPromptExampleOption | CodeReviewAutomatedApprovalExampleOption } | null;
  currentConfig: CodeReviewPolicyConfig | null;
  currentDraftValue?: string;
  persistedValue?: string;
  onOpenChange: (open: boolean) => void;
  onApply: () => void;
}) {
  if (!selection) return null;
  const value = "instructions" in selection.example ? selection.example.instructions : selection.example.policy;
  const dirty = currentDraftValue !== undefined && currentDraftValue !== (persistedValue ?? currentConfig?.[selection.field] ?? "");
  return <Dialog open onOpenChange={onOpenChange}><DialogContent>
    <DialogHeader><DialogTitle>{selection.example.title}</DialogTitle><DialogDescription>{selection.example.description} Only {selection.field === "review_instructions" ? "additional review instructions" : "the automated approval policy"} will be replaced.</DialogDescription></DialogHeader>
    <div className="max-h-80 overflow-auto whitespace-pre-wrap rounded-md border border-border bg-muted/30 p-3 text-sm">{value}</div>
    {selection.field === "automated_approval_policy" && currentConfig?.approval_mode !== "approve_acceptable" ? <p className="text-sm text-muted-foreground">This example does not enable automatic approval. Choose “Leave comments and approve when acceptable” separately.</p> : null}
    {dirty ? <p className="text-sm text-amber-700 dark:text-amber-300">Your currently saved value differs. Applying this example replaces only this prompt field and creates a new policy version.</p> : null}
    <DialogFooter><Button type="button" variant="outline" onClick={() => onOpenChange(false)}>Cancel</Button><Button type="button" onClick={onApply}>Use example</Button></DialogFooter>
  </DialogContent></Dialog>;
}

type CodeReviewPolicyTemplate = Awaited<ReturnType<typeof api.codeReviews.templates>>["data"][number];

type AdvancedPolicySettingsProps = {
  selectedTemplateKey: string;
  setSelectedTemplateKey: (value: string) => void;
  templates: CodeReviewPolicyTemplate[];
  selectedTemplate?: CodeReviewPolicyTemplate;
  selectedTemplateAlreadyApplied: boolean;
  config: CodeReviewPolicyConfig | null;
  readLatestConfig: () => CodeReviewPolicyConfig | null;
  setPendingTemplateApply: (value: { key: string; title: string }) => void;
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
  selectedTemplateKey,
  setSelectedTemplateKey,
  templates,
  selectedTemplate,
  selectedTemplateAlreadyApplied,
  config,
  readLatestConfig,
  setPendingTemplateApply,
  autosave,
  buildConfig,
  commitPolicy,
  codeReviewModelGroups,
  codeReviewOpenCodeAvailability,
  setEditingRequirementKey,
  invalidPolicyField,
  analyticsScope,
}: AdvancedPolicySettingsProps) {
  return (
                <AdvancedPolicyControls forceOpen={Boolean(invalidPolicyField && ["risk_policy", "inline_comment_limit", "agent_roster", "description_policy"].includes(invalidPolicyField))} onOpened={() => trackCodeReviewPolicyEvent({ event: "code_review_advanced_opened", scope: analyticsScope, subsection: "all", configured: true })}>
                  {invalidPolicyField ? <ErrorNotice title="Could not save this policy setting" description={`Correct the highlighted ${invalidPolicyField.replaceAll("_", " ")} setting and try again.`} /> : null}
                  <div className="grid gap-3 rounded-md border border-border p-4 md:grid-cols-[1fr_auto] md:items-end">
                    <FilterSelect
                      label="Advanced policy preset"
                      value={selectedTemplateKey}
                      onValueChange={setSelectedTemplateKey}
                      info="Selects a whole-policy replacement covering safety controls, thresholds, and agent settings. No selection makes no change; applying a selection autosaves a new policy version."
                    >
                      <SelectItem value={NO_TEMPLATE}>No template selected</SelectItem>
                      {templates.map((template) => (
                        <SelectItem key={template.key} value={template.key}>
                          {template.title}
                        </SelectItem>
                      ))}
                    </FilterSelect>
                    <div className="flex items-center gap-1.5">
                      <Button
                        variant="outline"
                        disabled={!selectedTemplate || !config}
                        onClick={() => {
                          if (!selectedTemplate) return;
                          const latestConfig = readLatestConfig();
                          const alreadyApplied =
                            selectedTemplateAlreadyApplied ||
                            (latestConfig
                              ? JSON.stringify(latestConfig) === JSON.stringify(selectedTemplate.config)
                              : false);
                          if (alreadyApplied) {
                            toast.info(`${selectedTemplate.title} is already applied`);
                            return;
                          }
                          setPendingTemplateApply({
                            key: selectedTemplate.key,
                            title: selectedTemplate.title,
                          });
                          autosave.save(clonePolicy(selectedTemplate.config));
                        }}
                      >
                        Apply preset
                      </Button>
                      <SettingInfoTooltip
                        label="Apply advanced policy preset"
                        description="Replaces the complete effective policy with the selected preset and autosaves a new policy version. This changes safety controls, thresholds, and agent settings—not just visible text."
                      />
                    </div>
                    <p className="text-xs text-muted-foreground md:col-span-2">
                      Applying a preset replaces safety controls, thresholds, and agent settings across the whole policy.
                    </p>
                  </div>

                <div className="space-y-3">
                  <div className="text-sm font-medium text-foreground">Fine-tuning</div>

                  <FineTuningSection title="Approval criteria" summary="Size thresholds, limits, timeout, and reviewer quorum" forceOpen={invalidPolicyField === "risk_policy" || invalidPolicyField === "inline_comment_limit"} onOpened={() => trackCodeReviewPolicyEvent({ event: "code_review_advanced_opened", scope: analyticsScope, subsection: "approval_criteria", configured: true })}>
                    <div className="grid gap-3 md:grid-cols-3">
                      <NumberPolicyInput
                        label="Files changed"
                        serverValue={config?.risk_policy.max_files_changed}
                        min={1}
                        disabled={!config}
                        autosave={autosave}
                        buildPatch={(value) => buildConfig((next) => { next.risk_policy.max_files_changed = value; })}
                      />
                      <NumberPolicyInput
                        label="Lines changed"
                        serverValue={config?.risk_policy.max_lines_changed}
                        min={1}
                        disabled={!config}
                        autosave={autosave}
                        buildPatch={(value) => buildConfig((next) => { next.risk_policy.max_lines_changed = value; })}
                      />
                      <NumberPolicyInput
                        label="Inline comments"
                        serverValue={config?.inline_comment_limit}
                        min={1}
                        max={10}
                        disabled={!config}
                        autosave={autosave}
                        buildPatch={(value) => buildConfig((next) => { next.inline_comment_limit = value; })}
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
                          autosave.save(buildConfig((next) => { next.agent_roster.timeout_seconds = seconds; }))
                        }
                      />
                      <NumberPolicyInput
                        label="Reviewer quorum"
                        serverValue={config?.agent_roster.require_reviewer_quorum}
                        min={1}
                        max={Math.max(1, config?.agent_roster.reviewers.length ?? 1)}
                        disabled={!config}
                        autosave={autosave}
                        buildPatch={(value) => buildConfig((next) => { next.agent_roster.require_reviewer_quorum = value; })}
                      />
                    </div>
                  </FineTuningSection>

                  <FineTuningSection title="Quality gates" summary="Merge and check requirements before approval" onOpened={() => trackCodeReviewPolicyEvent({ event: "code_review_advanced_opened", scope: analyticsScope, subsection: "quality_gates", configured: true })}>
                    <div className="grid gap-x-6 gap-y-2 md:grid-cols-2">
                      <PolicyToggle
                        label="Require passing checks"
                        description={QUALITY_GATE_DESCRIPTIONS.requirePassingChecks}
                        checked={config?.risk_policy.require_passing_checks ?? false}
                        disabled={!config}
                        onCheckedChange={(checked) => commitPolicy((next) => { next.risk_policy.require_passing_checks = checked; })}
                      />
                      <PolicyToggle
                        label="Enforce sensitive paths"
                        description={QUALITY_GATE_DESCRIPTIONS.excludeSensitivePaths}
                        checked={config?.risk_policy.exclude_sensitive_paths ?? false}
                        disabled={!config}
                        onCheckedChange={(checked) => commitPolicy((next) => { next.risk_policy.exclude_sensitive_paths = checked; })}
                      />
                      <PolicyToggle
                        label="Require up-to-date branch"
                        description={QUALITY_GATE_DESCRIPTIONS.requireUpToDate}
                        checked={config?.risk_policy.require_up_to_date ?? false}
                        disabled={!config}
                        onCheckedChange={(checked) => commitPolicy((next) => { next.risk_policy.require_up_to_date = checked; })}
                      />
                      <PolicyToggle
                        label="Allow policy changes"
                        description={QUALITY_GATE_DESCRIPTIONS.allowPolicyChanges}
                        checked={config?.risk_policy.allow_policy_changes ?? false}
                        disabled={!config}
                        onCheckedChange={(checked) => commitPolicy((next) => { next.risk_policy.allow_policy_changes = checked; })}
                      />
                      <PolicyToggle
                        label="Block reviewer disagreement"
                        description={QUALITY_GATE_DESCRIPTIONS.disagreementBlocks}
                        checked={config?.agent_roster.disagreement_blocks ?? false}
                        disabled={!config}
                        onCheckedChange={(checked) => commitPolicy((next) => { next.agent_roster.disagreement_blocks = checked; })}
                      />
                      <PolicyToggle
                        label="Allow fork PRs"
                        description={QUALITY_GATE_DESCRIPTIONS.allowForks}
                        checked={config?.risk_policy.allow_forks ?? false}
                        disabled={!config}
                        onCheckedChange={(checked) => commitPolicy((next) => { next.risk_policy.allow_forks = checked; })}
                      />
                    </div>
                  </FineTuningSection>

                  <FineTuningSection title="Paths, authors & checks" summary="Path filters, eligible authors, and required checks" onOpened={() => trackCodeReviewPolicyEvent({ event: "code_review_advanced_opened", scope: analyticsScope, subsection: "paths_authors_checks", configured: true })}>
                    <div className="grid gap-3 lg:grid-cols-2">
                      <PolicyStringListEditor
                        label="Sensitive paths"
                        description="Paths that should be treated as higher-risk changes."
                        placeholder="Add glob pattern, e.g. src/auth/**"
                        emptyText="No sensitive paths configured."
                        monospace
                        serverValue={config?.risk_policy.sensitive_paths ?? []}
                        disabled={!config}
                        onCommitItems={(items) => commitPolicy((next) => { next.risk_policy.sensitive_paths = items; })}
                      />
                      <PolicyStringListEditor
                        label="Allowed path patterns"
                        description="When set, only matching paths are eligible for automated approval."
                        placeholder="Add allowed glob pattern"
                        emptyText="No allowlist configured. All paths are eligible unless blocked."
                        monospace
                        serverValue={config?.risk_policy.allowed_path_patterns ?? []}
                        disabled={!config}
                        onCommitItems={(items) => commitPolicy((next) => { next.risk_policy.allowed_path_patterns = items; })}
                      />
                      <PolicyStringListEditor
                        label="Blocked path patterns"
                        description="Matching paths prevent automated approval."
                        placeholder="Add blocked glob pattern"
                        emptyText="No blocked paths configured."
                        monospace
                        serverValue={config?.risk_policy.blocked_path_patterns ?? []}
                        disabled={!config}
                        onCommitItems={(items) => commitPolicy((next) => { next.risk_policy.blocked_path_patterns = items; })}
                      />
                      <PolicyStringListEditor
                        label="Excluded categories"
                        description="Review categories to ignore for this policy."
                        placeholder="Add category"
                        emptyText="No excluded categories configured."
                        serverValue={config?.risk_policy.exclude_categories ?? []}
                        disabled={!config}
                        onCommitItems={(items) => commitPolicy((next) => { next.risk_policy.exclude_categories = items; })}
                      />
                      <PolicyStringListEditor
                        label="Required checks"
                        description="Check names that must pass before approval."
                        placeholder="Add required check"
                        emptyText="No required checks configured."
                        monospace
                        serverValue={config?.risk_policy.required_checks ?? []}
                        disabled={!config}
                        onCommitItems={(items) => commitPolicy((next) => { next.risk_policy.required_checks = items; })}
                      />
                      <PolicyStringListEditor
                        label="Eligible authors"
                        description="Authors allowed by this policy. Leave empty to allow any author."
                        placeholder="Add GitHub handle or author"
                        emptyText="Any author is eligible."
                        serverValue={config?.risk_policy.eligible_authors ?? []}
                        disabled={!config}
                        onCommitItems={(items) => commitPolicy((next) => { next.risk_policy.eligible_authors = items; })}
                      />
                    </div>
                  </FineTuningSection>

                  <FineTuningSection title="Reviewers & agents" summary="Reviewer agents and the orchestrating agent" forceOpen={invalidPolicyField === "agent_roster"} onOpened={() => trackCodeReviewPolicyEvent({ event: "code_review_advanced_opened", scope: analyticsScope, subsection: "reviewers_agents", configured: true })}>
                    <AgentRosterControls
                      config={config}
                      disabled={!config}
                      modelGroups={codeReviewModelGroups}
                      openCodeAvailability={codeReviewOpenCodeAvailability}
                      commitPolicy={commitPolicy}
                    />
                  </FineTuningSection>

                  <FineTuningSection title="Structured PR-description checks" summary="PR description rules checked before approval" forceOpen={invalidPolicyField === "description_policy"} onOpened={() => trackCodeReviewPolicyEvent({ event: "code_review_advanced_opened", scope: analyticsScope, subsection: "structured_description_checks", configured: true })}>
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

                </div>
                </AdvancedPolicyControls>
  );
}

function AdvancedPolicyControls({ children, forceOpen, onOpened }: { children: ReactNode; forceOpen: boolean; onOpened: () => void }) {
  const [open, setOpen] = useState(false);
  return (
    <Collapsible open={open || forceOpen} onOpenChange={(next) => { setOpen(next); if (next) onOpened(); }} className="rounded-md border border-border">
      <div className="flex items-center gap-1 border-border pr-3">
        <CollapsibleTrigger asChild>
          <Button variant="ghost" className="group h-auto min-w-0 flex-1 justify-between rounded-md p-4 text-left" aria-label="Advanced controls">
            <span className="min-w-0">
              <span className="block text-sm font-medium text-foreground">Advanced controls</span>
              <span className="mt-0.5 block text-xs font-normal text-muted-foreground">Safety gates, paths, agents, limits, and structured checks</span>
            </span>
            <ChevronDown className="h-4 w-4 shrink-0 text-muted-foreground transition-transform group-data-[state=open]:rotate-180" />
          </Button>
        </CollapsibleTrigger>
        <SettingInfoTooltip
          label="Advanced controls"
          description="Contains deterministic approval safeguards, reviewer configuration, limits, structured PR-description checks, and whole-policy presets. Defaults remain enforced while this section is closed."
        />
    </div>
      <CollapsibleContent className="space-y-4 border-t border-border p-4">{children}</CollapsibleContent>
    </Collapsible>
  );
}

function policyOutcome(config: CodeReviewPolicyConfig | null): "disabled" | "comment" | "approve" {
  if (!config?.enabled) return "disabled";
  return config.approval_mode === "approve_acceptable" ? "approve" : "comment";
}

function PolicySummary({
  config,
  repositorySelected,
  githubTriggerStatus,
}: {
  config: CodeReviewPolicyConfig | null;
  repositorySelected: boolean;
  githubTriggerStatus?: CodeReviewGitHubTriggerResponse["status"];
}) {
  if (!config) {
    return <div className="rounded-md border border-border bg-muted/30 px-4 py-3 text-sm text-muted-foreground">Loading review policy...</div>;
  }

  const outcome = policyOutcome(config);
  const reviewers = config.agent_roster.reviewers.length;
  const summaryItems = [
    outcome === "disabled" ? "Reviews paused" : outcome === "approve" ? "Comments + eligible approval" : "Comments only",
    repositorySelected
      ? `GitHub reviewer ${githubTriggerStatusLabel(githubTriggerStatus ?? "unconfigured").toLowerCase()}`
      : "Select a repository for GitHub setup",
    `${reviewers} ${reviewers === 1 ? "reviewer" : "reviewers"}`,
    `quorum ${config.agent_roster.require_reviewer_quorum}`,
  ];

  if (config.risk_policy.require_passing_checks) summaryItems.push("passing checks required");
  if (config.agent_roster.disagreement_blocks) summaryItems.push("disagreement blocks approval");
  if (config.risk_policy.exclude_sensitive_paths) summaryItems.push("sensitive paths need human review");

  return (
    <div className="rounded-md border border-border bg-muted/30 px-4 py-3">
      <div className="text-sm font-medium text-foreground">Current behavior</div>
      <div className="mt-2 flex flex-wrap gap-2">
        {summaryItems.map((item) => (
          <Badge key={item} variant="outline">
            {item}
          </Badge>
        ))}
      </div>
    </div>
  );
}

function PolicyBehaviorSection({
  config,
  onChange,
}: {
  config: CodeReviewPolicyConfig | null;
  onChange: (outcome: "disabled" | "comment" | "approve") => void;
}) {
  return (
    <section className="space-y-3" aria-labelledby="review-behavior-heading">
      <div id="review-behavior-heading" className="text-sm font-medium text-foreground">
        Review behavior
      </div>
      <OutcomeControl config={config} disabled={!config} onChange={onChange} />
    </section>
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
    <div className="space-y-3">
      <div className="flex items-center justify-between gap-4 rounded-md border border-border p-4">
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
        <Label key={option.value} className="flex cursor-pointer items-start gap-3 rounded-md border border-border p-3">
          <RadioGroupItem value={option.value} aria-label={option.title} className="mt-0.5" />
          <span className="flex min-w-0 flex-col gap-1">
            <span className="text-sm font-medium text-foreground">{option.title}</span>
            <span className="text-xs font-normal leading-5 text-muted-foreground">{option.description}</span>
          </span>
        </Label>
      ))}
    </RadioGroup>
      {config?.approval_mode === "approve_acceptable" ? (
        <p className="text-xs text-muted-foreground">
          Automatic approval is eligible only when all hard safeguards pass; uncertain or blocked changes still require a human.
        </p>
      ) : null}
    </div>
  );
}

function GitHubTriggerPanel({
  repositorySelected,
  trigger,
  isLoading,
  errorMessage,
  setupErrorMessage,
  setupPending,
  deletePending,
  canManage,
  onSetup,
  onDelete,
}: {
  repositorySelected: boolean;
  trigger?: CodeReviewGitHubTriggerResponse;
  isLoading: boolean;
  errorMessage: string | null;
  setupErrorMessage: string | null;
  setupPending: boolean;
  deletePending: boolean;
  canManage: boolean;
  onSetup: () => void;
  onDelete: () => void;
}) {
  const status = trigger?.status ?? "unconfigured";
  const ready = status === "ready";
  const authRequired = status === "auth_required";
  const permissionRequired = status === "permission_required";
  const needsRepair = status === "error" || permissionRequired;
  const reviewer = trigger?.team_reviewer ?? "@org/143-code-reviewer";
  const setupDisabledReason = githubTriggerSetupDisabledReason({
    canManage,
    repositorySelected,
    authRequired,
    setupPending,
    deletePending,
    isLoading,
  });

  return (
    <div className="rounded-md border border-border p-4">
      <div className="flex flex-col gap-3 lg:flex-row lg:items-start lg:justify-between">
        <div className="min-w-0">
          <div className="flex flex-wrap items-center gap-2">
            <div className="text-sm font-medium text-foreground">GitHub reviewer</div>
            <SettingInfoTooltip
              label="GitHub reviewer"
              description="Adds a repository reviewer in GitHub that starts 143 reviews when requested. When unconfigured, no GitHub reviewer request can start a review; organization policy remains stored but inactive for that repository trigger."
            />
            <Badge variant={githubTriggerStatusVariant(status)}>{isLoading ? "Checking" : githubTriggerStatusLabel(status)}</Badge>
            {ready ? <span className="text-xs font-medium text-foreground">{reviewer}</span> : null}
          </div>
          <div className="mt-1 text-xs text-muted-foreground">
            {repositorySelected
              ? "People select this team from GitHub's Reviewers menu on a PR to start a 143 code review."
              : "Select a repository to set up the reviewer that appears in GitHub's Reviewers menu."}
          </div>
          {repositorySelected && !ready ? (
            <div className="mt-3 grid gap-2 text-xs sm:grid-cols-3">
              <div className="rounded-md bg-muted/40 px-3 py-2">
                <div className="text-muted-foreground">Menu reviewer</div>
                <div className="mt-1 truncate font-medium text-foreground">{reviewer}</div>
              </div>
              <div className="rounded-md bg-muted/40 px-3 py-2">
                <div className="text-muted-foreground">Repository access</div>
                <div className="mt-1 font-medium text-foreground">Read</div>
              </div>
              <div className="rounded-md bg-muted/40 px-3 py-2">
                <div className="text-muted-foreground">Team slug</div>
                <div className="mt-1 truncate font-medium text-foreground">{trigger?.team_slug ?? "143-code-reviewer"}</div>
              </div>
            </div>
          ) : null}
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
            <Button variant="outline" size="sm" disabled={!canManage} onClick={() => api.githubStatus.connect()}>
              <Users className="h-4 w-4" />
              Connect GitHub
            </Button>
          ) : null}
          {!ready ? (
            <DisabledTooltip disabled={!!setupDisabledReason} content={setupDisabledReason}>
              <Button variant="default" size="sm" disabled={!!setupDisabledReason} onClick={onSetup}>
                <Users className="h-4 w-4" />
                {needsRepair ? "Repair GitHub reviewer" : "Set up GitHub reviewer"}
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
  repositorySelected,
  authRequired,
  setupPending,
  deletePending,
  isLoading,
}: {
  canManage: boolean;
  repositorySelected: boolean;
  authRequired: boolean;
  setupPending: boolean;
  deletePending: boolean;
  isLoading: boolean;
}): string | undefined {
  if (!canManage) {
    return "Only organization administrators can configure the GitHub reviewer menu option.";
  }
  if (!repositorySelected) {
    return "Select a repository before setting up the GitHub reviewer menu option.";
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
    case "error":
      return "Needs attention";
    default:
      return "Not configured";
  }
}

function githubTriggerStatusVariant(status: CodeReviewGitHubTriggerResponse["status"]): "success" | "secondary" | "destructive" | "outline" {
  if (status === "ready") return "success";
  if (status === "permission_required" || status === "error") return "destructive";
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
    case "frontend_or_ui_visible":
      return `Frontend/UI: ${summarizeItems(appliesWhen?.path_patterns, "default UI paths")}`;
    case "paths":
      return `Paths: ${summarizeItems(appliesWhen?.path_patterns, "no paths set")}`;
    case "categories":
      return `Categories: ${summarizeItems(appliesWhen?.categories, "no categories set")}`;
    case "tests_changed":
      return appliesWhen?.require_test_files_changed ? "When test files changed" : "Tests changed";
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
    case "frontend_or_ui_visible":
    case "paths":
      return {
        kind,
        path_patterns: previous?.path_patterns ?? [],
      };
    case "categories":
      return {
        kind,
        categories: previous?.categories ?? [],
      };
    case "tests_changed":
      return {
        kind,
        require_test_files_changed: previous?.require_test_files_changed ?? true,
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
                    <Button
                      variant="ghost"
                      size="sm"
                      disabled={disabled}
                      aria-label={`Edit ${requirement.title || "requirement"}`}
                      onClick={() => onEdit(requirement.key)}
                    >
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
              <SettingLabel
                label="Applies to"
                info="Controls which pull requests receive this structured description check. The default applies it to every pull request."
              />
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

            {kind === "frontend_or_ui_visible" || kind === "paths" ? (
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

            {kind === "categories" ? (
              <ListTextArea
                label="Categories"
                serverValue={requirement.applies_when?.categories ?? []}
                disabled={disabled}
                onCommitItems={(items) =>
                  onCommit((current) => ({
                    ...current,
                    applies_when: { kind, categories: items },
                  }))
                }
              />
            ) : null}

            {kind === "tests_changed" ? (
              <div className="flex items-center justify-between gap-3 rounded-md border border-border px-3 py-2">
                <div>
                  <div className="flex items-center gap-1.5">
                  <Label className="text-sm text-foreground">Require changed test files</Label>
                    <SettingInfoTooltip
                      label="Require changed test files"
                      description="When on, this description check applies only when test files changed. Turning it off uses the broader tests-changed applicability rule."
                    />
                  </div>
                  <div className="mt-1 text-xs text-muted-foreground">Applies this rule only when the pull request changes test files.</div>
                </div>
                <Switch
                  aria-label="Require changed test files"
                  checked={requirement.applies_when?.require_test_files_changed ?? true}
                  disabled={disabled}
                  onCheckedChange={(checked) =>
                    onCommit((current) => ({
                      ...current,
                      applies_when: {
                        kind: "tests_changed",
                        require_test_files_changed: checked,
                      },
                    }))
                  }
                />
              </div>
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

function FilterSelect({
  label,
  info,
  value,
  onValueChange,
  children,
}: {
  label: string;
  info?: string;
  value: string;
  onValueChange: (value: string) => void;
  children: ReactNode;
}) {
  return (
    <div className="flex min-w-0 flex-col gap-2">
      <div className="flex items-center gap-1.5">
      <Label className="text-xs text-muted-foreground">{label}</Label>
        {info ? <SettingInfoTooltip label={label} description={info} /> : null}
      </div>
      <Select value={value} onValueChange={onValueChange}>
        <SelectTrigger aria-label={label}>
          <SelectValue />
        </SelectTrigger>
        <SelectContent>{children}</SelectContent>
      </Select>
    </div>
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
  const canAddReviewer = Boolean(config) && reviewers.length < MAX_REVIEWER_MODELS && modelGroups.length > 0;
  const fallbackGroup = modelGroups[0];
  const orchestratorModel =
    config?.agent_roster.orchestrator_model && modelBelongsToAgent(config.agent_roster.orchestrator, config.agent_roster.orchestrator_model)
      ? config.agent_roster.orchestrator_model
      : defaultModelForAgent(config?.agent_roster.orchestrator ?? "", modelGroups);

  return (
    <div className="grid gap-5 lg:grid-cols-[minmax(0,1fr)_minmax(18rem,22rem)]">
      <div className="space-y-3">
        <div className="flex items-center justify-between gap-3">
          <div>
            <SettingLabel
              label="Reviewer models"
              info="Selects one to three agents that independently review each pull request. At least one is required; the built-in roster remains until changed, and removing one may lower the maximum valid quorum."
            />
            <p className="mt-1 text-xs text-muted-foreground">Run one to three independent reviewers. Quorum stays in Approval criteria.</p>
          </div>
          <Button
            type="button"
            size="sm"
            variant="outline"
            disabled={!canAddReviewer}
            onClick={() =>
              commitPolicy((next) => {
                if (!fallbackGroup || next.agent_roster.reviewers.length >= MAX_REVIEWER_MODELS) return;
                const reviewerModels = ensureReviewerModels(next, modelGroups);
                next.agent_roster.reviewers = [...next.agent_roster.reviewers, fallbackGroup.key];
                next.agent_roster.reviewer_models = [...reviewerModels, fallbackGroup.models[0] ?? ""];
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

        <div className="space-y-2">
          {reviewers.map((agent, index) => (
            <div key={`${agent}-${index}`} className="grid gap-2 rounded-md border border-border p-3 sm:grid-cols-[1fr_auto]">
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
                    next.agent_roster.reviewers[index] = selection.agent;
                    reviewerModels[index] = selection.model;
                    next.agent_roster.reviewer_models = reviewerModels;
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
                    next.agent_roster.reviewers = next.agent_roster.reviewers.filter((_, i) => i !== index);
                    next.agent_roster.reviewer_models = reviewerModels.filter((_, i) => i !== index);
                    next.agent_roster.require_reviewer_quorum = Math.min(
                      next.agent_roster.require_reviewer_quorum,
                      Math.max(1, next.agent_roster.reviewers.length),
                    );
                  })
                }
              >
                <Trash2 className="h-4 w-4" />
              </Button>
            </div>
          ))}
        </div>
      </div>

      <div className="space-y-3">
        <div>
          <Label className="text-xs text-muted-foreground">Orchestrator model</Label>
          <p className="mt-1 text-xs text-muted-foreground">Synthesizes reviewer evidence and decides the final review outcome.</p>
        </div>
        <AgentModelSelect
          ariaLabel="Orchestrator model"
          infoDescription="Chooses the agent and model that combines reviewer evidence. A selection is required; the resolved default remains until changed, and its recommendation remains subject to every deterministic safeguard."
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
            })
          }
        />
      </div>
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
        <ModelOptionGroups
          modelGroups={modelGroups}
          getOptionValue={(group, model) => selectionValue(group.key, model)}
          openCodeAvailability={openCodeAvailability}
        />
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
      <Input
        className="mt-2"
        type="number"
        aria-label={label}
        min={min}
        max={max}
        value={field.value}
        disabled={disabled}
        onChange={field.onChange}
        onBlur={field.onBlur}
      />
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

function ListTextArea({
  label,
  serverValue,
  disabled,
  onCommitItems,
}: {
  label: string;
  serverValue: string[];
  disabled?: boolean;
  onCommitItems: (items: string[]) => void;
}) {
  const field = useDebouncedTextField({
    serverValue: serverValue.join("\n"),
    onCommit: (text) =>
      onCommitItems(
        text
          .split(/\r?\n/)
          .map((item) => item.trim())
          .filter(Boolean),
      ),
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
  const field = useDebouncedTextField({ serverValue, onCommit });
  return <Textarea {...props} value={field.value} disabled={disabled} onChange={(event) => field.onChange(event.target.value)} onBlur={field.onBlur} />;
}

function FineTuningSection({ title, summary, defaultOpen = false, forceOpen = false, onOpened, children }: { title: string; summary?: string; defaultOpen?: boolean; forceOpen?: boolean; onOpened?: () => void; children: ReactNode }) {
  const [open, setOpen] = useState(defaultOpen);
  const triggerRef = useRef<HTMLButtonElement>(null);
  useEffect(() => { if (forceOpen) triggerRef.current?.focus(); }, [forceOpen]);
  return (
    <Collapsible open={open || forceOpen} onOpenChange={(next) => { setOpen(next); if (next) onOpened?.(); }} className="rounded-md border border-border">
      <CollapsibleTrigger ref={triggerRef} className="group flex w-full items-center justify-between gap-3 rounded-md p-4 text-left hover:bg-muted/40">
        <div className="min-w-0">
          <div className="text-sm font-medium text-foreground">{title}</div>
          {summary ? <div className="mt-0.5 text-xs text-muted-foreground">{summary}</div> : null}
        </div>
        <ChevronDown className="h-4 w-4 shrink-0 text-muted-foreground transition-transform group-data-[state=open]:rotate-180" />
      </CollapsibleTrigger>
      <CollapsibleContent className="space-y-3 border-t border-border p-4">{children}</CollapsibleContent>
    </Collapsible>
  );
}

function CodeReviewEvidenceSheet({
  review,
  evidence,
  isLoading,
  error,
  onRetry,
  open,
  onOpenChange,
}: {
  review: CodeReviewListItem | null;
  evidence?: CodeReviewEvidence;
  isLoading: boolean;
  error: Error | null;
  onRetry: () => void;
  open: boolean;
  onOpenChange: (open: boolean) => void;
}) {
  const agentResults = evidence?.agent_results ?? [];
  const findings = evidence?.findings ?? [];
  const artifacts = evidence?.prompt_artifacts ?? [];
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
          {isLoading ? <div className="text-sm text-muted-foreground">Loading evidence...</div> : null}
          {error ? (
            <ErrorNotice
              title="Evidence could not be loaded"
              description="Retry the request to view this review's evidence."
              action={{ label: "Retry", onClick: onRetry }}
            />
          ) : null}
          {!isLoading && !error && !evidence ? <div className="text-sm text-muted-foreground">No evidence recorded for this review.</div> : null}
          {evidence ? (
            <>
              <div className="grid grid-cols-3 gap-3">
                <EvidenceMetric label="Agents" value={agentResults.length} />
                <EvidenceMetric label="Findings" value={findings.length} />
                <EvidenceMetric label="Prompts" value={artifacts.length} />
              </div>

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
                          active={result.status === "running" || result.status === "queued"}
                        />
                      </div>
                      {result.raw_output ? (
                        <pre className="max-h-40 overflow-auto whitespace-pre-wrap rounded-md bg-muted/60 p-3 text-xs leading-5 text-muted-foreground">
                          {result.raw_output}
                        </pre>
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
                  findings.map((finding) => (
                    <div key={finding.id} className="space-y-2 border-t border-border pt-3 first:border-t-0 first:pt-0">
                      <div className="flex items-start justify-between gap-3">
                        <div className="min-w-0 space-y-1">
                          <div className="text-sm font-medium text-foreground">{finding.summary}</div>
                          <div className="text-xs text-muted-foreground">{formatFindingLocation(finding)}</div>
                        </div>
                        <Badge variant={finding.severity === "critical" || finding.severity === "high" ? "destructive" : "outline"}>
                          {statusLabel(finding.severity)}
                        </Badge>
                      </div>
                      <div className="text-sm leading-6 text-muted-foreground">{finding.body}</div>
                    </div>
                  ))
                )}
              </section>

              <section className="space-y-3">
                <EvidenceSectionHeader title="Prompt artifacts" empty={artifacts.length === 0} />
                {artifacts.length === 0 ? (
                  <div className="text-sm text-muted-foreground">No prompt artifacts recorded.</div>
                ) : (
                  artifacts.map((artifact) => (
                    <div key={artifact.id} className="space-y-3 border-t border-border pt-3 first:border-t-0 first:pt-0">
                      <div className="flex items-start justify-between gap-3">
                        <div className="min-w-0 space-y-1">
                          <div className="truncate text-sm font-medium text-foreground">{artifact.artifact_key}</div>
                          {artifact.agent_provider ? <div className="text-xs text-muted-foreground">{artifact.agent_provider}</div> : null}
                        </div>
                        <Badge variant="outline">{artifact.role}</Badge>
                      </div>
                      <pre className="max-h-40 overflow-auto whitespace-pre-wrap rounded-md bg-muted/60 p-3 text-xs leading-5 text-muted-foreground">
                        {artifact.content}
                      </pre>
                    </div>
                  ))
                )}
              </section>
            </>
          ) : null}
        </div>
      </SheetContent>
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
