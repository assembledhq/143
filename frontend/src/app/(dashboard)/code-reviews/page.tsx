"use client";

import Link from "next/link";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import type { ComponentProps, ReactNode } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  AlertTriangle,
  ChevronDown,
  CircleHelp,
  ClipboardCheck,
  ExternalLink,
  FileSearch,
  Plus,
  Settings2,
  ShieldCheck,
  Trash2,
  Users,
} from "lucide-react";
import { PageHeader } from "@/components/page-header";
import { EmptyState } from "@/components/empty-state";
import { Button } from "@/components/ui/button";
import { DisabledTooltip } from "@/components/ui/disabled-tooltip";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "@/components/ui/collapsible";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { Switch } from "@/components/ui/switch";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";
import { Label } from "@/components/ui/label";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { ApiError, api } from "@/lib/api";
import { queryKeys } from "@/lib/query-keys";
import { getActiveOrgId } from "@/lib/active-org";
import { buildCodeReviewStreamURL, SSE_EVENT } from "@/lib/sse";
import { useResourceSSE } from "@/lib/use-resource-sse";
import { pollMs } from "@/lib/poll-intervals";
import { useAutosave, type UseAutosaveResult } from "@/hooks/useAutosave";
import { useAutosaveNumericField } from "@/hooks/useAutosaveNumericField";
import { useDebouncedTextField } from "@/hooks/useDebouncedTextField";
import { AutosaveIndicator } from "@/components/AutosaveIndicator";
import { applyCodeReviewPolicyOptimistic, coalesceCodeReviewPolicy } from "@/lib/code-review-autosave";
import type {
  CodeReviewApprovalMode,
  CodeReviewDecision,
  CodeReviewDescriptionApplicabilityKind,
  CodeReviewEvidence,
  CodeReviewGitHubTriggerResponse,
  CodeReviewListItem,
  CodeReviewPolicyConfig,
  CodeReviewResolvedPolicy,
  CodeReviewSessionStatus,
  SingleResponse,
} from "@/lib/types";

const ALL_REPOSITORIES = "all";
const ALL_DECISIONS = "all";
const ALL_RISKS = "all";
const ALL_STATUSES = "all";
const NO_TEMPLATE = "none";
// Coalesce a burst of SSE lifecycle events into a single list refetch.
const CODE_REVIEW_INVALIDATE_COALESCE_MS = 300;
const APPLICABILITY_KIND_LABELS: Record<CodeReviewDescriptionApplicabilityKind, string> = {
  all: "All PRs",
  nontrivial: "Nontrivial",
  frontend_or_ui_visible: "Frontend/UI",
  paths: "Paths",
  categories: "Categories",
  tests_changed: "Tests changed",
};
const QUALITY_GATE_DESCRIPTIONS = {
  requirePassingChecks:
    "Blocks approval until the PR's required GitHub checks are passing. The reviewer can still leave comments, but it will not approve failing or pending builds.",
  requireMergeable:
    "Blocks approval when GitHub reports merge conflicts or an unknown mergeable state. This keeps approvals from landing on PRs that cannot merge cleanly.",
  excludeSensitivePaths:
    "Treats changes matching sensitive paths as blocking approval. Use this for migrations, auth, billing, and other areas that need a human review.",
  requireUpToDate:
    "Requires the PR branch to be current with its base branch before approval. This prevents approving a stale diff when newer base changes may affect the result.",
  allowPolicyChanges:
    "Allows the bot to approve PRs that modify review policy or automation configuration. Leave this off when those changes should always require a human reviewer.",
  disagreementBlocks:
    "Blocks approval when reviewer agents disagree on whether the PR is acceptable. This makes uncertain reviews resolve to a human decision instead of an approval.",
  allowForks:
    "Allows approval decisions for PRs opened from forks. Turn this off when forked PRs should be comment-only because they run with less trusted context.",
} as const;

function formatDate(value?: string): string {
  if (!value) return "-";
  return new Intl.DateTimeFormat(undefined, {
    month: "short",
    day: "numeric",
    hour: "numeric",
    minute: "2-digit",
  }).format(new Date(value));
}

function decisionLabel(review: CodeReviewListItem): string {
  if (review.decision === "approved") return "Approved";
  if (review.decision === "needs_human_review") return "Needs human";
  if (review.decision === "blocked") return "Blocked";
  if (review.decision === "comment_only") return "Comment only";
  return "Pending";
}

function decisionVariant(review: CodeReviewListItem): "success" | "secondary" | "destructive" | "outline" {
  if (review.decision === "approved") return "success";
  if (review.decision === "blocked") return "destructive";
  if (review.decision === "needs_human_review") return "secondary";
  return "outline";
}

function statusVariant(status: string): "success" | "secondary" | "destructive" | "outline" {
  if (status === "completed") return "success";
  if (status === "failed" || status === "stale") return "destructive";
  if (status === "running" || status === "queued") return "secondary";
  return "outline";
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

export default function CodeReviewsPage() {
  const queryClient = useQueryClient();
  const [repositoryFilter, setRepositoryFilter] = useState(ALL_REPOSITORIES);
  const [decisionFilter, setDecisionFilter] = useState(ALL_DECISIONS);
  const [riskFilter, setRiskFilter] = useState(ALL_RISKS);
  const [statusFilter, setStatusFilter] = useState(ALL_STATUSES);
  const [search, setSearch] = useState("");
  const [selectedTemplateKey, setSelectedTemplateKey] = useState(NO_TEMPLATE);
  const [selectedEvidenceSessionId, setSelectedEvidenceSessionId] = useState<string | null>(null);
  const repositoryId = repositoryFilter === ALL_REPOSITORIES ? undefined : repositoryFilter;
  const reviewFilters = useMemo(
    () => ({
      repository_id: repositoryId,
      decision: decisionFilter === ALL_DECISIONS ? undefined : (decisionFilter as CodeReviewDecision),
      risk: riskFilter === ALL_RISKS ? undefined : (riskFilter as "acceptable" | "needs_review"),
      status: statusFilter === ALL_STATUSES ? undefined : (statusFilter as CodeReviewSessionStatus),
      search: search.trim() || undefined,
      limit: 100,
    }),
    [decisionFilter, repositoryId, riskFilter, search, statusFilter],
  );

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
      void queryClient.invalidateQueries({ queryKey: queryKeys.codeReviews.lists() });
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
    queryKey: queryKeys.codeReviews.policy(repositoryId ?? null),
    queryFn: () => api.codeReviews.getPolicy(repositoryId ?? null),
  });
  const githubTriggerQuery = useQuery({
    queryKey: queryKeys.codeReviews.githubTrigger(repositoryId ?? null),
    queryFn: () => api.codeReviews.getGitHubTrigger(repositoryId as string),
    enabled: Boolean(repositoryId),
  });
  const templatesQuery = useQuery({
    queryKey: queryKeys.codeReviews.templates,
    queryFn: () => api.codeReviews.templates(),
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
  const autosave = useAutosave<CodeReviewPolicyConfig>({
    queryKey: queryKeys.codeReviews.policy(repositoryId ?? null),
    mutationFn: async (next: CodeReviewPolicyConfig) => {
      try {
        return await api.codeReviews.updatePolicy({ repository_id: repositoryId ?? null, config: next });
      } finally {
        // Editing the org default cascades into repo-scoped resolved policies
        // cached under other keys, so invalidate the whole code-reviews
        // namespace (matches the prior manual save).
        void queryClient.invalidateQueries({ queryKey: queryKeys.codeReviews.all });
      }
    },
    applyOptimistic: applyCodeReviewPolicyOptimistic,
    coalesce: coalesceCodeReviewPolicy,
    debounceMs: 0,
  });
  const readLatestConfig = (): CodeReviewPolicyConfig | null =>
    queryClient.getQueryData<SingleResponse<CodeReviewResolvedPolicy>>(
      queryKeys.codeReviews.policy(repositoryId ?? null),
    )?.data?.config ?? config;
  const setupGitHubTrigger = useMutation({
    mutationFn: (targetRepositoryId: string) => api.codeReviews.setupGitHubTrigger(targetRepositoryId),
    onSuccess: (_data, targetRepositoryId) => {
      void queryClient.invalidateQueries({ queryKey: queryKeys.codeReviews.githubTrigger(targetRepositoryId) });
    },
  });
  const deleteGitHubTrigger = useMutation({
    mutationFn: (targetRepositoryId: string) => api.codeReviews.deleteGitHubTrigger(targetRepositoryId),
    onSuccess: (_data, targetRepositoryId) => {
      void queryClient.invalidateQueries({ queryKey: queryKeys.codeReviews.githubTrigger(targetRepositoryId) });
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
  const commitPolicy = (mutate: (next: CodeReviewPolicyConfig) => void) => {
    const next = draftFrom(mutate);
    if (next) autosave.save(next);
  };
  // toPatch builder for numeric fields, which require a non-null payload. Safe
  // because numeric inputs are disabled until the policy has loaded.
  const buildConfig = (mutate: (next: CodeReviewPolicyConfig) => void): CodeReviewPolicyConfig => {
    const next = draftFrom(mutate);
    if (!next) return config as CodeReviewPolicyConfig;
    return next;
  };
  const commitRequirement = (
    index: number,
    updater: (requirement: CodeReviewPolicyConfig["description_policy"]["requirements"][number]) =>
      CodeReviewPolicyConfig["description_policy"]["requirements"][number],
  ) => {
    commitPolicy((next) => {
      next.description_policy.requirements[index] = updater(next.description_policy.requirements[index]);
    });
  };

  return (
    <main className="min-h-full bg-background">
      <div className="mx-auto flex w-full max-w-7xl flex-col gap-5 px-4 py-5 sm:px-6 lg:px-8">
        <PageHeader
          title="Code reviews"
          description="Bot-requested PR reviews, acceptable-risk policy, and review outcomes."
        />

        <div className="grid gap-3 md:grid-cols-[minmax(12rem,18rem)_minmax(10rem,12rem)_minmax(10rem,12rem)_minmax(10rem,12rem)_1fr]">
          <FilterSelect label="Repository" value={repositoryFilter} onValueChange={setRepositoryFilter}>
            <SelectItem value={ALL_REPOSITORIES}>All repositories</SelectItem>
            {repositories.map((repo) => (
              <SelectItem key={repo.id} value={repo.id}>
                {repo.full_name}
              </SelectItem>
            ))}
          </FilterSelect>
          <FilterSelect label="Decision" value={decisionFilter} onValueChange={setDecisionFilter}>
            <SelectItem value={ALL_DECISIONS}>All decisions</SelectItem>
            <SelectItem value="approved">Approved</SelectItem>
            <SelectItem value="comment_only">Comment only</SelectItem>
            <SelectItem value="needs_human_review">Needs human</SelectItem>
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
            <Input
              value={search}
              onChange={(event) => setSearch(event.target.value)}
              placeholder="PR, repo, or title"
              aria-label="Search code reviews"
            />
          </div>
        </div>

        <Tabs defaultValue="reviews" className="space-y-4">
          <TabsList>
            <TabsTrigger value="reviews">
              <ClipboardCheck className="h-4 w-4" />
              Reviews
            </TabsTrigger>
            <TabsTrigger value="config">
              <Settings2 className="h-4 w-4" />
              Configurations
            </TabsTrigger>
          </TabsList>

          <TabsContent value="reviews" className="space-y-3">
            {reviews.length === 0 ? (
              <EmptyState
                icon={ClipboardCheck}
                title="No code review sessions"
                description="Reviews will appear here after the GitHub reviewer bot is requested on a pull request."
              />
            ) : (
              <>
              <Card>
                <CardContent className="p-0">
                  <Table>
                    <TableHeader>
                      <TableRow>
                        <TableHead>PR</TableHead>
                        <TableHead>Repo</TableHead>
                        <TableHead>Risk</TableHead>
                        <TableHead>Decision</TableHead>
                        <TableHead>Status</TableHead>
                        <TableHead>Completed</TableHead>
                        <TableHead className="text-right">Actions</TableHead>
                      </TableRow>
                    </TableHeader>
                    <TableBody>
                      {reviews.map((review) => (
                        <TableRow key={review.id}>
                          <TableCell className="min-w-[18rem]">
                            <div className="font-medium text-foreground">
                              #{review.github_pr_number} {review.pull_request_title}
                            </div>
                            <div className="mt-1 text-xs text-muted-foreground">
                              {review.pull_request_author || "Unknown author"} · {review.head_sha.slice(0, 7)}
                            </div>
                          </TableCell>
                          <TableCell>{review.repository_name || review.github_repo}</TableCell>
                          <TableCell>
                            <Badge variant={review.acceptable ? "success" : "secondary"}>
                              {review.acceptable ? "Acceptable" : "Needs review"}
                            </Badge>
                          </TableCell>
                          <TableCell>
                            <Badge variant={decisionVariant(review)}>{decisionLabel(review)}</Badge>
                          </TableCell>
                          <TableCell>
                            <Badge variant={statusVariant(review.status)}>{review.stale ? "stale" : review.status}</Badge>
                          </TableCell>
                          <TableCell>{formatDate(review.completed_at)}</TableCell>
                          <TableCell>
                            <div className="flex justify-end gap-2">
                              <Button
                                variant={selectedEvidenceSessionId === review.session_id ? "secondary" : "ghost"}
                                size="sm"
                                onClick={() =>
                                  setSelectedEvidenceSessionId((current) =>
                                    current === review.session_id ? null : review.session_id,
                                  )
                                }
                              >
                                <FileSearch className="h-4 w-4" />
                                Evidence
                              </Button>
                              <Button variant="ghost" size="sm" asChild>
                                <Link href={`/sessions/${review.session_id}`}>Session</Link>
                              </Button>
                              <Button variant="ghost" size="icon-sm" asChild aria-label="Open pull request">
                                <Link href={review.github_pr_url} target="_blank" rel="noreferrer">
                                  <ExternalLink className="h-4 w-4" />
                                </Link>
                              </Button>
                              {review.github_review_url ? (
                                <Button variant="ghost" size="icon-sm" asChild aria-label="Open final review">
                                  <Link href={review.github_review_url} target="_blank" rel="noreferrer">
                                    <ClipboardCheck className="h-4 w-4" />
                                  </Link>
                                </Button>
                              ) : null}
                            </div>
                          </TableCell>
                        </TableRow>
                      ))}
                    </TableBody>
                  </Table>
                </CardContent>
              </Card>
              {selectedEvidenceReview ? (
                <CodeReviewEvidencePanel
                  review={selectedEvidenceReview}
                  evidence={evidenceQuery.data?.data}
                  isLoading={evidenceQuery.isLoading}
                  error={evidenceQuery.error}
                />
              ) : null}
              </>
            )}
          </TabsContent>

          <TabsContent value="config" className="space-y-4">
            <Card>
              <CardHeader>
                <div className="flex items-center justify-between gap-3">
                  <CardTitle>Bot behavior</CardTitle>
                  <AutosaveIndicator status={autosave.status} />
                </div>
              </CardHeader>
              <CardContent className="space-y-5">
                <div className="rounded-md border border-border p-4">
                  <div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
                    <div>
                      <div className="text-sm font-medium text-foreground">
                        {repositoryId ? "Repository policy" : "Organization default"}
                      </div>
                      <div className="mt-1 text-xs text-muted-foreground">
                        {policyQuery.data?.data.source === "repository"
                          ? "This repository uses an insert-only override that inherits organization defaults."
                          : "These settings provide the default policy for repositories without their own override."}
                      </div>
                    </div>
                    <Badge variant={policyQuery.data?.data.source === "repository" ? "secondary" : "outline"}>
                      {policyQuery.data?.data.source ?? "loading"}
                    </Badge>
                  </div>
                  {config?.inheritance?.inherit_org_defaults ? (
                    <div className="mt-3 text-xs text-muted-foreground">
                      Inherited from version {policyQuery.data?.data.inherited_policy?.version ?? "default"}.
                      {config.inheritance.override_fields?.length
                        ? ` Override fields: ${config.inheritance.override_fields.join(", ")}.`
                        : " No explicit override fields."}
                    </div>
                  ) : null}
                </div>

                <GitHubTriggerPanel
                  repositorySelected={Boolean(repositoryId)}
                  trigger={githubTriggerQuery.data?.data}
                  isLoading={githubTriggerQuery.isLoading || githubTriggerQuery.isFetching}
                  errorMessage={apiErrorMessage(githubTriggerQuery.error)}
                  setupErrorMessage={apiErrorMessage(setupGitHubTrigger.error)}
                  setupPending={setupGitHubTrigger.isPending}
                  deletePending={deleteGitHubTrigger.isPending}
                  onSetup={() => {
                    if (!repositoryId) return;
                    setupGitHubTrigger.mutate(repositoryId);
                  }}
                  onDelete={() => {
                    if (!repositoryId) return;
                    deleteGitHubTrigger.mutate(repositoryId);
                  }}
                />

                <div className="space-y-3">
                  <div className="text-sm font-medium text-foreground">Essentials</div>

                  <div className="flex flex-col gap-3 rounded-md border border-border p-4 sm:flex-row sm:items-center sm:justify-between">
                    <div>
                      <div className="text-sm font-medium text-foreground">Enable 143 Code Reviewer</div>
                      <div className="mt-1 text-xs text-muted-foreground">
                        When off, reviewer requests are acknowledged but no review session is started.
                      </div>
                    </div>
                    <Switch
                      checked={config?.enabled ?? false}
                      disabled={!config}
                      onCheckedChange={(checked) => commitPolicy((next) => { next.enabled = checked; })}
                    />
                  </div>

                  <div className="flex flex-col gap-3 rounded-md border border-border p-4 sm:flex-row sm:items-center sm:justify-between">
                    <div>
                      <div className="text-sm font-medium text-foreground">Approve acceptable PRs</div>
                      <div className="mt-1 text-xs text-muted-foreground">
                        When off, the bot always submits comment-only GitHub reviews.
                      </div>
                    </div>
                    <Switch
                      checked={config?.approval_mode === "approve_acceptable"}
                      disabled={!config}
                      onCheckedChange={(checked) =>
                        commitPolicy((next) => {
                          next.approval_mode = (checked ? "approve_acceptable" : "comment_only") as CodeReviewApprovalMode;
                        })
                      }
                    />
                  </div>

                  <div className="grid gap-3 rounded-md border border-border p-4 md:grid-cols-[1fr_auto] md:items-end">
                    <FilterSelect label="Starter template" value={selectedTemplateKey} onValueChange={setSelectedTemplateKey}>
                      <SelectItem value={NO_TEMPLATE}>No template selected</SelectItem>
                      {templates.map((template) => (
                        <SelectItem key={template.key} value={template.key}>
                          {template.title}
                        </SelectItem>
                      ))}
                    </FilterSelect>
                    <Button
                      variant="outline"
                      disabled={!selectedTemplate || !config}
                      onClick={() => {
                        if (!selectedTemplate) return;
                        autosave.save(clonePolicy(selectedTemplate.config));
                      }}
                    >
                      Apply template
                    </Button>
                  </div>
                </div>

                <div className="space-y-3">
                  <div className="text-sm font-medium text-foreground">Fine-tuning</div>

                  <FineTuningSection title="Approval criteria" summary="Size thresholds, limits, timeout, and reviewer quorum">
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
                      <NumberPolicyInput
                        label="Timeout seconds"
                        serverValue={config?.agent_roster.timeout_seconds}
                        min={60}
                        disabled={!config}
                        autosave={autosave}
                        buildPatch={(value) => buildConfig((next) => { next.agent_roster.timeout_seconds = value; })}
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

                  <FineTuningSection title="Quality gates" summary="Merge and check requirements before approval">
                    <div className="grid gap-x-6 gap-y-2 md:grid-cols-2">
                      <PolicyToggle
                        label="Require passing checks"
                        description={QUALITY_GATE_DESCRIPTIONS.requirePassingChecks}
                        checked={config?.risk_policy.require_passing_checks ?? false}
                        disabled={!config}
                        onCheckedChange={(checked) => commitPolicy((next) => { next.risk_policy.require_passing_checks = checked; })}
                      />
                      <PolicyToggle
                        label="Require mergeable PR"
                        description={QUALITY_GATE_DESCRIPTIONS.requireMergeable}
                        checked={config?.risk_policy.require_mergeable ?? false}
                        disabled={!config}
                        onCheckedChange={(checked) => commitPolicy((next) => { next.risk_policy.require_mergeable = checked; })}
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

                  <FineTuningSection title="Paths, authors & checks" summary="Path filters, eligible authors, and required checks">
                <div className="grid gap-3 lg:grid-cols-2">
                      <ListTextArea
                        label="Sensitive paths"
                        serverValue={config?.risk_policy.sensitive_paths ?? []}
                        disabled={!config}
                        onCommitItems={(items) => commitPolicy((next) => { next.risk_policy.sensitive_paths = items; })}
                      />
                      <ListTextArea
                        label="Allowed path patterns"
                        serverValue={config?.risk_policy.allowed_path_patterns ?? []}
                        disabled={!config}
                        onCommitItems={(items) => commitPolicy((next) => { next.risk_policy.allowed_path_patterns = items; })}
                      />
                      <ListTextArea
                        label="Blocked path patterns"
                        serverValue={config?.risk_policy.blocked_path_patterns ?? []}
                        disabled={!config}
                        onCommitItems={(items) => commitPolicy((next) => { next.risk_policy.blocked_path_patterns = items; })}
                      />
                      <ListTextArea
                        label="Excluded categories"
                        serverValue={config?.risk_policy.exclude_categories ?? []}
                        disabled={!config}
                        onCommitItems={(items) => commitPolicy((next) => { next.risk_policy.exclude_categories = items; })}
                      />
                      <ListTextArea
                        label="Required checks"
                        serverValue={config?.risk_policy.required_checks ?? []}
                        disabled={!config}
                        onCommitItems={(items) => commitPolicy((next) => { next.risk_policy.required_checks = items; })}
                      />
                      <ListTextArea
                        label="Eligible authors"
                        serverValue={config?.risk_policy.eligible_authors ?? []}
                        disabled={!config}
                        onCommitItems={(items) => commitPolicy((next) => { next.risk_policy.eligible_authors = items; })}
                      />
                    </div>
                  </FineTuningSection>

                  <FineTuningSection title="Reviewers & agents" summary="Reviewer agents and the orchestrating agent">
                    <div className="grid gap-3 lg:grid-cols-2">
                      <ListTextArea
                        label="Reviewer agents"
                        serverValue={config?.agent_roster.reviewers ?? []}
                        disabled={!config}
                        onCommitItems={(items) =>
                          commitPolicy((next) => {
                            const reviewers = items.length > 0 ? items : next.agent_roster.reviewers;
                            next.agent_roster.reviewers = reviewers;
                            next.agent_roster.require_reviewer_quorum = Math.min(
                              next.agent_roster.require_reviewer_quorum,
                              Math.max(1, reviewers.length),
                            );
                          })
                        }
                      />
                      <div className="space-y-2">
                        <Label className="text-xs text-muted-foreground">Orchestrator</Label>
                        <PolicyTextInput
                          serverValue={config?.agent_roster.orchestrator ?? ""}
                          disabled={!config}
                          onCommit={(value) => commitPolicy((next) => { next.agent_roster.orchestrator = value; })}
                        />
                      </div>
                    </div>
                  </FineTuningSection>

                  <FineTuningSection title="Description requirements" summary="Final review template and per-PR description rules">
                    <div className="space-y-4">
                      <div className="space-y-2">
                        <Label className="text-xs text-muted-foreground">Final review template</Label>
                        <PolicyTextarea
                          serverValue={config?.final_review_template ?? ""}
                          disabled={!config}
                          rows={4}
                          onCommit={(value) => commitPolicy((next) => { next.final_review_template = value; })}
                        />
                      </div>

                      <div className="space-y-3">
                        <div className="flex items-center justify-between gap-3">
                          <div className="text-sm font-medium text-foreground">Requirements</div>
                          <Button
                            variant="outline"
                            size="sm"
                            disabled={!config}
                            onClick={() =>
                              commitPolicy((next) => {
                                const nextIndex = next.description_policy.requirements.length + 1;
                                next.description_policy.requirements.push({
                                  key: `custom_${nextIndex}`,
                                  title: "Custom requirement",
                                  prompt: "",
                                  required: true,
                                  applies_when: { kind: "all" },
                                });
                              })
                            }
                          >
                            <Plus className="h-4 w-4" />
                            Add requirement
                          </Button>
                        </div>
                        <div className="grid gap-3 lg:grid-cols-3">
                          {config?.description_policy.requirements.map((requirement, index) => (
                            <div key={requirement.key} className="space-y-2 rounded-md border border-border p-3">
                              <div className="flex items-center gap-2">
                                <PolicyTextInput
                                  serverValue={requirement.title}
                                  disabled={!config}
                                  aria-label={`${requirement.key} title`}
                                  onCommit={(value) => commitRequirement(index, (current) => ({ ...current, title: value }))}
                                />
                                <Button
                                  variant="ghost"
                                  size="icon-sm"
                                  disabled={!config || (config?.description_policy.requirements.length ?? 0) <= 1}
                                  aria-label={`Remove ${requirement.title}`}
                                  onClick={() =>
                                    commitPolicy((next) => {
                                      next.description_policy.requirements = next.description_policy.requirements.filter(
                                        (_, itemIndex) => itemIndex !== index,
                                      );
                                    })
                                  }
                                >
                                  <Trash2 className="h-4 w-4" />
                                </Button>
                              </div>
                              <div className="grid gap-2 sm:grid-cols-[1fr_auto] sm:items-center">
                                <div className="space-y-2">
                                  <Label className="text-xs text-muted-foreground">Applies when</Label>
                                  <Select
                                    value={requirement.applies_when?.kind ?? "all"}
                                    disabled={!config}
                                    onValueChange={(value) =>
                                      commitRequirement(index, (current) => ({
                                        ...current,
                                        applicability: value,
                                        applies_when: {
                                          ...(current.applies_when ?? {}),
                                          kind: value as CodeReviewDescriptionApplicabilityKind,
                                        },
                                      }))
                                    }
                                  >
                                    <SelectTrigger aria-label={`${requirement.key} applicability`}>
                                      <SelectValue />
                                    </SelectTrigger>
                                    <SelectContent>
                                      {Object.entries(APPLICABILITY_KIND_LABELS).map(([kind, label]) => (
                                        <SelectItem key={kind} value={kind}>
                                          {label}
                                        </SelectItem>
                                      ))}
                                    </SelectContent>
                                  </Select>
                                </div>
                                <div className="flex items-center justify-between gap-2 rounded-md border border-border px-3 py-2">
                                  <Label className="text-xs text-muted-foreground">Required</Label>
                                  <Switch
                                    checked={requirement.required}
                                    disabled={!config}
                                    onCheckedChange={(checked) =>
                                      commitRequirement(index, (current) => ({ ...current, required: checked }))
                                    }
                                  />
                                </div>
                              </div>
                              <div className="grid gap-2 sm:grid-cols-2">
                                <NumberPolicyInput
                                  label="Min files"
                                  serverValue={requirement.applies_when?.min_files_changed ?? 0}
                                  min={0}
                                  disabled={!config}
                                  autosave={autosave}
                                  buildPatch={(value) =>
                                    buildConfig((next) => {
                                      const req = next.description_policy.requirements[index];
                                      req.applies_when = { ...(req.applies_when ?? { kind: "all" }), min_files_changed: value };
                                    })
                                  }
                                />
                                <NumberPolicyInput
                                  label="Min lines"
                                  serverValue={requirement.applies_when?.min_lines_changed ?? 0}
                                  min={0}
                                  disabled={!config}
                                  autosave={autosave}
                                  buildPatch={(value) =>
                                    buildConfig((next) => {
                                      const req = next.description_policy.requirements[index];
                                      req.applies_when = { ...(req.applies_when ?? { kind: "all" }), min_lines_changed: value };
                                    })
                                  }
                                />
                              </div>
                              <ListTextArea
                                label="Path patterns"
                                serverValue={requirement.applies_when?.path_patterns ?? []}
                                disabled={!config}
                                onCommitItems={(items) =>
                                  commitRequirement(index, (current) => ({
                                    ...current,
                                    applies_when: { ...(current.applies_when ?? { kind: "paths" }), path_patterns: items },
                                  }))
                                }
                              />
                              <ListTextArea
                                label="Categories"
                                serverValue={requirement.applies_when?.categories ?? []}
                                disabled={!config}
                                onCommitItems={(items) =>
                                  commitRequirement(index, (current) => ({
                                    ...current,
                                    applies_when: { ...(current.applies_when ?? { kind: "categories" }), categories: items },
                                  }))
                                }
                              />
                              <div className="flex items-center justify-between gap-2 rounded-md border border-border px-3 py-2">
                                <Label className="text-xs text-muted-foreground">Require changed test files</Label>
                                <Switch
                                  checked={requirement.applies_when?.require_test_files_changed ?? false}
                                  disabled={!config}
                                  onCheckedChange={(checked) =>
                                    commitRequirement(index, (current) => ({
                                      ...current,
                                      applies_when: {
                                        ...(current.applies_when ?? { kind: "tests_changed" }),
                                        require_test_files_changed: checked,
                                      },
                                    }))
                                  }
                                />
                              </div>
                              <PolicyTextarea
                                serverValue={requirement.prompt}
                                disabled={!config}
                                rows={4}
                                aria-label={`${requirement.key} prompt`}
                                onCommit={(value) => commitRequirement(index, (current) => ({ ...current, prompt: value }))}
                              />
                            </div>
                          ))}
                        </div>
                      </div>
                    </div>
                  </FineTuningSection>
                </div>
              </CardContent>
            </Card>
          </TabsContent>

        </Tabs>
      </div>
    </main>
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
  onSetup: () => void;
  onDelete: () => void;
}) {
  const status = trigger?.status ?? "unconfigured";
  const ready = status === "ready";
  const authRequired = status === "auth_required";
  const permissionRequired = status === "permission_required";
  const reviewer = trigger?.team_reviewer ?? "@org/143-code-reviewer";
  const setupDisabledReason = githubTriggerSetupDisabledReason({
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
            <div className="text-sm font-medium text-foreground">GitHub reviewer trigger</div>
            <Badge variant={githubTriggerStatusVariant(status)}>
              {isLoading ? "Checking" : githubTriggerStatusLabel(status)}
            </Badge>
          </div>
          <div className="mt-1 text-xs text-muted-foreground">
            {repositorySelected
              ? "Humans request this GitHub team on a PR to start a 143 code review."
              : "Select a repository to create or repair its reviewer team trigger."}
          </div>
          {repositorySelected ? (
            <div className="mt-3 grid gap-2 text-xs sm:grid-cols-3">
              <div className="rounded-md bg-muted/40 px-3 py-2">
                <div className="text-muted-foreground">Reviewer</div>
                <div className="mt-1 truncate font-medium text-foreground">{reviewer}</div>
              </div>
              <div className="rounded-md bg-muted/40 px-3 py-2">
                <div className="text-muted-foreground">Repository access</div>
                <div className="mt-1 font-medium text-foreground">Read</div>
              </div>
              <div className="rounded-md bg-muted/40 px-3 py-2">
                <div className="text-muted-foreground">Team slug</div>
                <div className="mt-1 truncate font-medium text-foreground">
                  {trigger?.team_slug ?? "143-code-reviewer"}
                </div>
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
            <Button variant="outline" size="sm" onClick={() => api.githubStatus.connect()}>
              <Users className="h-4 w-4" />
              Connect GitHub
            </Button>
          ) : null}
          <DisabledTooltip disabled={!!setupDisabledReason} content={setupDisabledReason}>
            <Button
              variant={ready ? "outline" : "default"}
              size="sm"
              disabled={!!setupDisabledReason}
              onClick={onSetup}
            >
              {ready ? <ShieldCheck className="h-4 w-4" /> : <Users className="h-4 w-4" />}
              {ready ? "Repair team" : "Create / repair team"}
            </Button>
          </DisabledTooltip>
          {ready ? (
            <Button variant="ghost" size="sm" disabled={setupPending || deletePending} onClick={onDelete}>
              <Trash2 className="h-4 w-4" />
              Disable
            </Button>
          ) : null}
          {permissionRequired ? (
            <Badge variant="destructive">Permission approval needed</Badge>
          ) : null}
        </div>
      </div>
    </div>
  );
}

function githubTriggerSetupDisabledReason({
  repositorySelected,
  authRequired,
  setupPending,
  deletePending,
  isLoading,
}: {
  repositorySelected: boolean;
  authRequired: boolean;
  setupPending: boolean;
  deletePending: boolean;
  isLoading: boolean;
}): string | undefined {
  if (!repositorySelected) {
    return "Select a repository before creating the GitHub reviewer team.";
  }
  if (authRequired) {
    return "Connect your GitHub account first so 143 can create or repair the reviewer team.";
  }
  if (setupPending) {
    return "Team setup is already running. Wait for it to finish before trying again.";
  }
  if (deletePending) {
    return "The reviewer team trigger is being disabled. Wait for that action to finish before repairing it.";
  }
  if (isLoading) {
    return "143 is checking the repository's reviewer team status. Wait for the check to finish.";
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

function FilterSelect({
  label,
  value,
  onValueChange,
  children,
}: {
  label: string;
  value: string;
  onValueChange: (value: string) => void;
  children: ReactNode;
}) {
  return (
    <div className="flex min-w-0 flex-col gap-2">
      <Label className="text-xs text-muted-foreground">{label}</Label>
      <Select value={value} onValueChange={onValueChange}>
        <SelectTrigger aria-label={label}>
          <SelectValue />
        </SelectTrigger>
        <SelectContent>{children}</SelectContent>
      </Select>
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
      <Label className="text-xs text-muted-foreground">{label}</Label>
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
        <TooltipProvider delayDuration={150}>
          <Tooltip>
            <TooltipTrigger asChild>
              <Button
                type="button"
                variant="ghost"
                size="icon"
                className="h-5 w-5 shrink-0 rounded-full text-muted-foreground hover:text-foreground"
                aria-label={`About ${label}`}
              >
                <CircleHelp className="h-3.5 w-3.5" />
              </Button>
            </TooltipTrigger>
            <TooltipContent side="top" sideOffset={6} className="max-w-72 leading-5">
              {description}
            </TooltipContent>
          </Tooltip>
        </TooltipProvider>
      </div>
      <Switch checked={checked} disabled={disabled} onCheckedChange={onCheckedChange} />
    </div>
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
      onCommitItems(text.split(/\r?\n/).map((item) => item.trim()).filter(Boolean)),
  });
  return (
    <div className="space-y-2">
      <Label className="text-xs text-muted-foreground">{label}</Label>
      <Textarea
        value={field.value}
        disabled={disabled}
        rows={4}
        onChange={(event) => field.onChange(event.target.value)}
        onBlur={field.onBlur}
      />
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
  return (
    <Input
      {...props}
      value={field.value}
      disabled={disabled}
      onChange={(event) => field.onChange(event.target.value)}
      onBlur={field.onBlur}
    />
  );
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
  return (
    <Textarea
      {...props}
      value={field.value}
      disabled={disabled}
      onChange={(event) => field.onChange(event.target.value)}
      onBlur={field.onBlur}
    />
  );
}

function FineTuningSection({
  title,
  summary,
  defaultOpen = false,
  children,
}: {
  title: string;
  summary?: string;
  defaultOpen?: boolean;
  children: ReactNode;
}) {
  return (
    <Collapsible defaultOpen={defaultOpen} className="rounded-md border border-border">
      <CollapsibleTrigger className="group flex w-full items-center justify-between gap-3 rounded-md p-4 text-left hover:bg-muted/40">
        <div className="min-w-0">
          <div className="text-sm font-medium text-foreground">{title}</div>
          {summary ? <div className="mt-0.5 text-xs text-muted-foreground">{summary}</div> : null}
        </div>
        <ChevronDown className="h-4 w-4 shrink-0 text-muted-foreground transition-transform group-data-[state=open]:rotate-180" />
      </CollapsibleTrigger>
      <CollapsibleContent className="space-y-3 border-t border-border p-4">
        {children}
      </CollapsibleContent>
    </Collapsible>
  );
}

function CodeReviewEvidencePanel({
  review,
  evidence,
  isLoading,
  error,
}: {
  review: CodeReviewListItem;
  evidence?: CodeReviewEvidence;
  isLoading: boolean;
  error: Error | null;
}) {
  const agentResults = evidence?.agent_results ?? [];
  const findings = evidence?.findings ?? [];
  const artifacts = evidence?.prompt_artifacts ?? [];
  return (
    <Card>
      <CardHeader>
        <div className="flex flex-col gap-2 sm:flex-row sm:items-center sm:justify-between">
          <CardTitle>
            Evidence for #{review.github_pr_number} {review.pull_request_title}
          </CardTitle>
          <Badge variant={decisionVariant(review)}>{decisionLabel(review)}</Badge>
        </div>
      </CardHeader>
      <CardContent className="space-y-4">
        {isLoading ? <div className="text-sm text-muted-foreground">Loading evidence...</div> : null}
        {error ? <div className="text-sm text-destructive">Evidence could not be loaded.</div> : null}
        {!isLoading && !error && !evidence ? (
          <div className="text-sm text-muted-foreground">No evidence recorded for this review.</div>
        ) : null}
        {evidence ? (
          <>
            <div className="grid gap-3 lg:grid-cols-2">
              <div className="space-y-2">
                <div className="text-sm font-medium text-foreground">Agent results</div>
                {agentResults.length === 0 ? (
                  <div className="text-sm text-muted-foreground">No agent results recorded.</div>
                ) : (
                  agentResults.map((result) => (
                    <div key={result.id} className="rounded-md border border-border p-3">
                      <div className="flex items-center justify-between gap-3">
                        <div className="min-w-0">
                          <div className="truncate text-sm font-medium text-foreground">
                            {result.agent_provider} · {result.role}
                          </div>
                          {result.agent_model ? (
                            <div className="mt-1 text-xs text-muted-foreground">{result.agent_model}</div>
                          ) : null}
                        </div>
                        <Badge variant={statusVariant(result.status)}>{result.status}</Badge>
                      </div>
                      {result.raw_output ? (
                        <pre className="mt-3 max-h-32 overflow-auto whitespace-pre-wrap rounded-md bg-muted p-3 text-xs text-muted-foreground">
                          {result.raw_output}
                        </pre>
                      ) : null}
                      {result.structured_result ? (
                        <pre className="mt-3 max-h-32 overflow-auto whitespace-pre-wrap rounded-md bg-muted p-3 text-xs text-muted-foreground">
                          {formatEvidenceJSON(result.structured_result)}
                        </pre>
                      ) : null}
                    </div>
                  ))
                )}
              </div>
              <div className="space-y-2">
                <div className="text-sm font-medium text-foreground">Findings</div>
                {findings.length === 0 ? (
                  <div className="text-sm text-muted-foreground">No findings recorded.</div>
                ) : (
                  findings.map((finding) => (
                    <div key={finding.id} className="rounded-md border border-border p-3">
                      <div className="flex items-start justify-between gap-3">
                        <div className="min-w-0">
                          <div className="text-sm font-medium text-foreground">{finding.summary}</div>
                          <div className="mt-1 text-xs text-muted-foreground">{formatFindingLocation(finding)}</div>
                        </div>
                        <Badge variant={finding.severity === "critical" || finding.severity === "high" ? "destructive" : "outline"}>
                          {finding.severity}
                        </Badge>
                      </div>
                      <div className="mt-2 text-sm text-muted-foreground">{finding.body}</div>
                    </div>
                  ))
                )}
              </div>
            </div>
            <div className="space-y-2">
              <div className="text-sm font-medium text-foreground">Prompt artifacts</div>
              {artifacts.length === 0 ? (
                <div className="text-sm text-muted-foreground">No prompt artifacts recorded.</div>
              ) : (
                <div className="grid gap-3 lg:grid-cols-2">
                  {artifacts.map((artifact) => (
                    <div key={artifact.id} className="rounded-md border border-border p-3">
                      <div className="flex items-center justify-between gap-3">
                        <div className="truncate text-sm font-medium text-foreground">{artifact.artifact_key}</div>
                        <Badge variant="outline">{artifact.role}</Badge>
                      </div>
                      <pre className="mt-3 max-h-32 overflow-auto whitespace-pre-wrap rounded-md bg-muted p-3 text-xs text-muted-foreground">
                        {artifact.content}
                      </pre>
                    </div>
                  ))}
                </div>
              )}
            </div>
          </>
        ) : null}
      </CardContent>
    </Card>
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
