"use client";

import Link from "next/link";
import { useMemo, useState } from "react";
import type { ReactNode } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ClipboardCheck, ExternalLink, Settings2, BarChart3, RefreshCw, Plus, Trash2, FileSearch, Users, ShieldCheck, AlertTriangle } from "lucide-react";
import { PageHeader } from "@/components/page-header";
import { EmptyState } from "@/components/empty-state";
import { Button } from "@/components/ui/button";
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
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { Switch } from "@/components/ui/switch";
import { Label } from "@/components/ui/label";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { ApiError, api } from "@/lib/api";
import { queryKeys } from "@/lib/query-keys";
import type {
  CodeReviewApprovalMode,
  CodeReviewDecision,
  CodeReviewDescriptionApplicabilityKind,
  CodeReviewEvidence,
  CodeReviewGitHubTriggerResponse,
  CodeReviewListItem,
  CodeReviewPolicyConfig,
  CodeReviewSessionStatus,
} from "@/lib/types";

const ALL_REPOSITORIES = "all";
const ALL_DECISIONS = "all";
const ALL_RISKS = "all";
const ALL_STATUSES = "all";
const NO_TEMPLATE = "none";
const APPLICABILITY_KIND_LABELS: Record<CodeReviewDescriptionApplicabilityKind, string> = {
  all: "All PRs",
  nontrivial: "Nontrivial",
  frontend_or_ui_visible: "Frontend/UI",
  paths: "Paths",
  categories: "Categories",
  tests_changed: "Tests changed",
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

function reviewDurationMinutes(review: CodeReviewListItem): number | null {
  if (!review.completed_at) return null;
  const started = new Date(review.created_at).getTime();
  const completed = new Date(review.completed_at).getTime();
  if (!Number.isFinite(started) || !Number.isFinite(completed) || completed < started) return null;
  return Math.round((completed - started) / 60000);
}

function formatPercent(numerator: number, denominator: number): string {
  if (denominator <= 0) return "0%";
  return `${Math.round((numerator / denominator) * 100)}%`;
}

function formatMinutes(value: number | null): string {
  if (value === null) return "-";
  if (value < 60) return `${value}m`;
  const hours = Math.floor(value / 60);
  const minutes = value % 60;
  return minutes === 0 ? `${hours}h` : `${hours}h ${minutes}m`;
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
  const reviewsQuery = useQuery({
    queryKey: queryKeys.codeReviews.list(reviewFilters),
    queryFn: () => api.codeReviews.list(reviewFilters),
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

  const policyKey = `${repositoryId ?? "org"}:${policyQuery.data?.data.policy?.id ?? policyQuery.data?.data.source ?? "loading"}`;
  const serverPolicy = policyQuery.data?.data.config;
  const baseDraftPolicy = useMemo(
    () => (serverPolicy ? clonePolicy(serverPolicy) : null),
    [serverPolicy],
  );
  const [draftOverride, setDraftOverride] = useState<{ key: string; config: CodeReviewPolicyConfig } | null>(null);
  const draftPolicy = draftOverride?.key === policyKey ? draftOverride.config : baseDraftPolicy;

  const savePolicy = useMutation({
    mutationFn: (config: CodeReviewPolicyConfig) =>
      api.codeReviews.updatePolicy({ repository_id: repositoryId ?? null, config }),
    onSuccess: () => {
      setDraftOverride(null);
      void queryClient.invalidateQueries({ queryKey: queryKeys.codeReviews.all });
    },
  });
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
  const insightCounts = useMemo(() => {
    return reviews.reduce(
      (acc, review) => {
        acc.total += 1;
        if (review.decision === "approved") acc.approved += 1;
        if (review.decision === "needs_human_review" || review.decision === "comment_only") acc.escalated += 1;
        if (review.stale || review.status === "stale") acc.stale += 1;
        const duration = reviewDurationMinutes(review);
        if (duration !== null) {
          acc.completedDurationMinutes += duration;
          acc.completedWithDuration += 1;
        }
        return acc;
      },
      { total: 0, approved: 0, escalated: 0, stale: 0, completedDurationMinutes: 0, completedWithDuration: 0 },
    );
  }, [reviews]);
  const averageReviewMinutes =
    insightCounts.completedWithDuration > 0
      ? Math.round(insightCounts.completedDurationMinutes / insightCounts.completedWithDuration)
      : null;
  const recentEscalations = useMemo(
    () =>
      reviews
        .filter((review) => review.decision === "needs_human_review" || review.decision === "comment_only" || review.decision === "blocked")
        .slice(0, 5),
    [reviews],
  );
  const topRepositories = useMemo(() => {
    const counts = new Map<string, number>();
    for (const review of reviews) {
      const label = review.repository_name || review.github_repo;
      counts.set(label, (counts.get(label) ?? 0) + 1);
    }
    return Array.from(counts.entries())
      .sort((a, b) => b[1] - a[1])
      .slice(0, 5);
  }, [reviews]);
  const updateDraftPolicy = (config: CodeReviewPolicyConfig) => {
    setDraftOverride({ key: policyKey, config });
  };
  const updateDescriptionRequirement = (
    index: number,
    updater: (requirement: CodeReviewPolicyConfig["description_policy"]["requirements"][number]) =>
      CodeReviewPolicyConfig["description_policy"]["requirements"][number],
  ) => {
    if (!draftPolicy) return;
    const requirements = [...draftPolicy.description_policy.requirements];
    requirements[index] = updater(requirements[index]);
    updateDraftPolicy({ ...draftPolicy, description_policy: { requirements } });
  };

  return (
    <main className="min-h-full bg-background">
      <div className="mx-auto flex w-full max-w-7xl flex-col gap-5 px-4 py-5 sm:px-6 lg:px-8">
        <PageHeader
          title="Code reviews"
          description="Bot-requested PR reviews, acceptable-risk policy, and review outcomes."
          action={
            <Button variant="outline" size="sm" onClick={() => reviewsQuery.refetch()}>
              <RefreshCw className="h-4 w-4" />
              Refresh
            </Button>
          }
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
            <TabsTrigger value="insights">
              <BarChart3 className="h-4 w-4" />
              Insights
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
                <CardTitle>Bot behavior</CardTitle>
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
                  {draftPolicy?.inheritance?.inherit_org_defaults ? (
                    <div className="mt-3 text-xs text-muted-foreground">
                      Inherited from version {policyQuery.data?.data.inherited_policy?.version ?? "default"}.
                      {draftPolicy.inheritance.override_fields?.length
                        ? ` Override fields: ${draftPolicy.inheritance.override_fields.join(", ")}.`
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
                    disabled={!selectedTemplate}
                    onClick={() => {
                      if (!selectedTemplate) return;
                      setDraftOverride({ key: policyKey, config: clonePolicy(selectedTemplate.config) });
                    }}
                  >
                    Apply template
                  </Button>
                </div>

                <div className="flex flex-col gap-3 rounded-md border border-border p-4 sm:flex-row sm:items-center sm:justify-between">
                  <div>
                    <div className="text-sm font-medium text-foreground">Enable 143 Code Reviewer</div>
                    <div className="mt-1 text-xs text-muted-foreground">
                      When off, reviewer requests are acknowledged but no review session is started.
                    </div>
                  </div>
                  <Switch
                    checked={draftPolicy?.enabled ?? false}
                    disabled={!draftPolicy}
                    onCheckedChange={(checked) => {
                      if (!draftPolicy) return;
                      setDraftOverride({ key: policyKey, config: { ...draftPolicy, enabled: checked } });
                    }}
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
                    checked={draftPolicy?.approval_mode === "approve_acceptable"}
                    disabled={!draftPolicy}
                    onCheckedChange={(checked) => {
                      if (!draftPolicy) return;
                      setDraftOverride({
                        key: policyKey,
                        config: {
                          ...draftPolicy,
                          approval_mode: (checked ? "approve_acceptable" : "comment_only") as CodeReviewApprovalMode,
                        },
                      });
                    }}
                  />
                </div>

                <div className="grid gap-3 md:grid-cols-3">
                  <NumberPolicyInput
                    label="Files changed"
                    value={draftPolicy?.risk_policy.max_files_changed}
                    min={1}
                    disabled={!draftPolicy}
                    onChange={(value) =>
                      draftPolicy &&
                      setDraftOverride({
                        key: policyKey,
                        config: { ...draftPolicy, risk_policy: { ...draftPolicy.risk_policy, max_files_changed: value } },
                      })
                    }
                  />
                  <NumberPolicyInput
                    label="Lines changed"
                    value={draftPolicy?.risk_policy.max_lines_changed}
                    min={1}
                    disabled={!draftPolicy}
                    onChange={(value) =>
                      draftPolicy &&
                      setDraftOverride({
                        key: policyKey,
                        config: { ...draftPolicy, risk_policy: { ...draftPolicy.risk_policy, max_lines_changed: value } },
                      })
                    }
                  />
                  <NumberPolicyInput
                    label="Inline comments"
                    value={draftPolicy?.inline_comment_limit}
                    min={1}
                    max={10}
                    disabled={!draftPolicy}
                    onChange={(value) =>
                      draftPolicy &&
                      setDraftOverride({ key: policyKey, config: { ...draftPolicy, inline_comment_limit: value } })
                    }
                  />
                  <NumberPolicyInput
                    label="Timeout seconds"
                    value={draftPolicy?.agent_roster.timeout_seconds}
                    min={60}
                    disabled={!draftPolicy}
                    onChange={(value) =>
                      draftPolicy &&
                      setDraftOverride({
                        key: policyKey,
                        config: { ...draftPolicy, agent_roster: { ...draftPolicy.agent_roster, timeout_seconds: value } },
                      })
                    }
                  />
                  <NumberPolicyInput
                    label="Cost ceiling cents"
                    value={draftPolicy?.agent_roster.max_cost_cents}
                    min={0}
                    disabled={!draftPolicy}
                    onChange={(value) =>
                      draftPolicy &&
                      setDraftOverride({
                        key: policyKey,
                        config: { ...draftPolicy, agent_roster: { ...draftPolicy.agent_roster, max_cost_cents: value } },
                      })
                    }
                  />
                  <NumberPolicyInput
                    label="Reviewer quorum"
                    value={draftPolicy?.agent_roster.require_reviewer_quorum}
                    min={1}
                    max={Math.max(1, draftPolicy?.agent_roster.reviewers.length ?? 1)}
                    disabled={!draftPolicy}
                    onChange={(value) =>
                      draftPolicy &&
                      setDraftOverride({
                        key: policyKey,
                        config: { ...draftPolicy, agent_roster: { ...draftPolicy.agent_roster, require_reviewer_quorum: value } },
                      })
                    }
                  />
                  <div className="rounded-md border border-border p-4">
                    <Label className="text-xs text-muted-foreground">Review depth</Label>
                    <Select
                      value={draftPolicy?.agent_roster.review_depth ?? "standard"}
                      disabled={!draftPolicy}
                      onValueChange={(value) =>
                        draftPolicy &&
                        setDraftOverride({
                          key: policyKey,
                          config: { ...draftPolicy, agent_roster: { ...draftPolicy.agent_roster, review_depth: value } },
                        })
                      }
                    >
                      <SelectTrigger className="mt-2" aria-label="Review depth">
                        <SelectValue />
                      </SelectTrigger>
                      <SelectContent>
                        <SelectItem value="shallow">Shallow</SelectItem>
                        <SelectItem value="standard">Standard</SelectItem>
                        <SelectItem value="deep">Deep</SelectItem>
                      </SelectContent>
                    </Select>
                  </div>
                </div>

                <div className="grid gap-3 md:grid-cols-2">
                  <PolicyToggle
                    label="Require passing checks"
                    checked={draftPolicy?.risk_policy.require_passing_checks ?? false}
                    disabled={!draftPolicy}
                    onCheckedChange={(checked) =>
                      draftPolicy &&
                      setDraftOverride({
                        key: policyKey,
                        config: { ...draftPolicy, risk_policy: { ...draftPolicy.risk_policy, require_passing_checks: checked } },
                      })
                    }
                  />
                  <PolicyToggle
                    label="Require mergeable PR"
                    checked={draftPolicy?.risk_policy.require_mergeable ?? false}
                    disabled={!draftPolicy}
                    onCheckedChange={(checked) =>
                      draftPolicy &&
                      setDraftOverride({
                        key: policyKey,
                        config: { ...draftPolicy, risk_policy: { ...draftPolicy.risk_policy, require_mergeable: checked } },
                      })
                    }
                  />
                  <PolicyToggle
                    label="Enforce sensitive paths"
                    checked={draftPolicy?.risk_policy.exclude_sensitive_paths ?? false}
                    disabled={!draftPolicy}
                    onCheckedChange={(checked) =>
                      draftPolicy &&
                      setDraftOverride({
                        key: policyKey,
                        config: { ...draftPolicy, risk_policy: { ...draftPolicy.risk_policy, exclude_sensitive_paths: checked } },
                      })
                    }
                  />
                  <PolicyToggle
                    label="Require up-to-date branch"
                    checked={draftPolicy?.risk_policy.require_up_to_date ?? false}
                    disabled={!draftPolicy}
                    onCheckedChange={(checked) =>
                      draftPolicy &&
                      setDraftOverride({
                        key: policyKey,
                        config: { ...draftPolicy, risk_policy: { ...draftPolicy.risk_policy, require_up_to_date: checked } },
                      })
                    }
                  />
                  <PolicyToggle
                    label="Allow policy changes"
                    checked={draftPolicy?.risk_policy.allow_policy_changes ?? false}
                    disabled={!draftPolicy}
                    onCheckedChange={(checked) =>
                      draftPolicy &&
                      setDraftOverride({
                        key: policyKey,
                        config: { ...draftPolicy, risk_policy: { ...draftPolicy.risk_policy, allow_policy_changes: checked } },
                      })
                    }
                  />
                  <PolicyToggle
                    label="Block reviewer disagreement"
                    checked={draftPolicy?.agent_roster.disagreement_blocks ?? false}
                    disabled={!draftPolicy}
                    onCheckedChange={(checked) =>
                      draftPolicy &&
                      setDraftOverride({
                        key: policyKey,
                        config: { ...draftPolicy, agent_roster: { ...draftPolicy.agent_roster, disagreement_blocks: checked } },
                      })
                    }
                  />
                  <PolicyToggle
                    label="Allow fork PRs"
                    checked={draftPolicy?.risk_policy.allow_forks ?? false}
                    disabled={!draftPolicy}
                    onCheckedChange={(checked) =>
                      draftPolicy &&
                      setDraftOverride({
                        key: policyKey,
                        config: { ...draftPolicy, risk_policy: { ...draftPolicy.risk_policy, allow_forks: checked } },
                      })
                    }
                  />
                </div>

                <div className="grid gap-3 lg:grid-cols-2">
                  <ListTextArea
                    label="Sensitive paths"
                    value={draftPolicy?.risk_policy.sensitive_paths ?? []}
                    disabled={!draftPolicy}
                    onChange={(items) =>
                      draftPolicy &&
                      setDraftOverride({
                        key: policyKey,
                        config: { ...draftPolicy, risk_policy: { ...draftPolicy.risk_policy, sensitive_paths: items } },
                      })
                    }
                  />
                  <ListTextArea
                    label="Allowed path patterns"
                    value={draftPolicy?.risk_policy.allowed_path_patterns ?? []}
                    disabled={!draftPolicy}
                    onChange={(items) =>
                      draftPolicy &&
                      updateDraftPolicy({
                        ...draftPolicy,
                        risk_policy: { ...draftPolicy.risk_policy, allowed_path_patterns: items },
                      })
                    }
                  />
                  <ListTextArea
                    label="Blocked path patterns"
                    value={draftPolicy?.risk_policy.blocked_path_patterns ?? []}
                    disabled={!draftPolicy}
                    onChange={(items) =>
                      draftPolicy &&
                      updateDraftPolicy({
                        ...draftPolicy,
                        risk_policy: { ...draftPolicy.risk_policy, blocked_path_patterns: items },
                      })
                    }
                  />
                  <ListTextArea
                    label="Excluded categories"
                    value={draftPolicy?.risk_policy.exclude_categories ?? []}
                    disabled={!draftPolicy}
                    onChange={(items) =>
                      draftPolicy &&
                      setDraftOverride({
                        key: policyKey,
                        config: { ...draftPolicy, risk_policy: { ...draftPolicy.risk_policy, exclude_categories: items } },
                      })
                    }
                  />
                  <ListTextArea
                    label="Required checks"
                    value={draftPolicy?.risk_policy.required_checks ?? []}
                    disabled={!draftPolicy}
                    onChange={(items) =>
                      draftPolicy &&
                      setDraftOverride({
                        key: policyKey,
                        config: { ...draftPolicy, risk_policy: { ...draftPolicy.risk_policy, required_checks: items } },
                      })
                    }
                  />
                  <ListTextArea
                    label="Eligible authors"
                    value={draftPolicy?.risk_policy.eligible_authors ?? []}
                    disabled={!draftPolicy}
                    onChange={(items) =>
                      draftPolicy &&
                      setDraftOverride({
                        key: policyKey,
                        config: { ...draftPolicy, risk_policy: { ...draftPolicy.risk_policy, eligible_authors: items } },
                      })
                    }
                  />
                  <ListTextArea
                    label="Reviewer agents"
                    value={draftPolicy?.agent_roster.reviewers ?? []}
                    disabled={!draftPolicy}
                    onChange={(items) => {
                      if (!draftPolicy) return;
                      const reviewers = items.length > 0 ? items : draftPolicy.agent_roster.reviewers;
                      const requireReviewerQuorum = Math.min(
                        draftPolicy.agent_roster.require_reviewer_quorum,
                        Math.max(1, reviewers.length),
                      );
                      setDraftOverride({
                        key: policyKey,
                        config: {
                          ...draftPolicy,
                          agent_roster: {
                            ...draftPolicy.agent_roster,
                            reviewers,
                            require_reviewer_quorum: requireReviewerQuorum,
                          },
                        },
                      });
                    }}
                  />
                  <div className="space-y-2">
                    <Label className="text-xs text-muted-foreground">Orchestrator</Label>
                    <Input
                      value={draftPolicy?.agent_roster.orchestrator ?? ""}
                      disabled={!draftPolicy}
                      onChange={(event) =>
                        draftPolicy &&
                        setDraftOverride({
                          key: policyKey,
                          config: { ...draftPolicy, agent_roster: { ...draftPolicy.agent_roster, orchestrator: event.target.value } },
                        })
                      }
                    />
                  </div>
                </div>

                <div className="space-y-3">
                  <Label className="text-xs text-muted-foreground">Final review template</Label>
                  <Textarea
                    value={draftPolicy?.final_review_template ?? ""}
                    disabled={!draftPolicy}
                    rows={4}
                    onChange={(event) =>
                      draftPolicy &&
                      setDraftOverride({
                        key: policyKey,
                        config: { ...draftPolicy, final_review_template: event.target.value },
                      })
                    }
                  />
                </div>

                <div className="space-y-3">
                  <div className="flex items-center justify-between gap-3">
                    <div className="text-sm font-medium text-foreground">Description requirements</div>
                    <Button
                      variant="outline"
                      size="sm"
                      disabled={!draftPolicy}
                      onClick={() => {
                        if (!draftPolicy) return;
                        const nextIndex = draftPolicy.description_policy.requirements.length + 1;
                        setDraftOverride({
                          key: policyKey,
                          config: {
                            ...draftPolicy,
                            description_policy: {
                              requirements: [
                                ...draftPolicy.description_policy.requirements,
                                {
                                  key: `custom_${nextIndex}`,
                                  title: "Custom requirement",
                                  prompt: "",
                                  required: true,
                                  applies_when: { kind: "all" },
                                },
                              ],
                            },
                          },
                        });
                      }}
                    >
                      <Plus className="h-4 w-4" />
                      Add requirement
                    </Button>
                  </div>
                  <div className="grid gap-3 lg:grid-cols-3">
                    {draftPolicy?.description_policy.requirements.map((requirement, index) => (
                      <div key={requirement.key} className="space-y-2 rounded-md border border-border p-3">
                        <div className="flex items-center gap-2">
                          <Input
                            value={requirement.title}
                            disabled={!draftPolicy}
                            aria-label={`${requirement.key} title`}
                            onChange={(event) => {
                              if (!draftPolicy) return;
                              const requirements = [...draftPolicy.description_policy.requirements];
                              requirements[index] = { ...requirement, title: event.target.value };
                              setDraftOverride({
                                key: policyKey,
                                config: { ...draftPolicy, description_policy: { requirements } },
                              });
                            }}
                          />
                          <Button
                            variant="ghost"
                            size="icon-sm"
                            disabled={!draftPolicy || draftPolicy.description_policy.requirements.length <= 1}
                            aria-label={`Remove ${requirement.title}`}
                            onClick={() => {
                              if (!draftPolicy) return;
                              const requirements = draftPolicy.description_policy.requirements.filter((_, itemIndex) => itemIndex !== index);
                              setDraftOverride({
                                key: policyKey,
                                config: { ...draftPolicy, description_policy: { requirements } },
                              });
                            }}
                          >
                            <Trash2 className="h-4 w-4" />
                          </Button>
                        </div>
                        <div className="grid gap-2 sm:grid-cols-[1fr_auto] sm:items-center">
                          <div className="space-y-2">
                            <Label className="text-xs text-muted-foreground">Applies when</Label>
                            <Select
                              value={requirement.applies_when?.kind ?? "all"}
                              disabled={!draftPolicy}
                              onValueChange={(value) =>
                                updateDescriptionRequirement(index, (current) => ({
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
                              disabled={!draftPolicy}
                              onCheckedChange={(checked) => {
                                if (!draftPolicy) return;
                                const requirements = [...draftPolicy.description_policy.requirements];
                                requirements[index] = { ...requirement, required: checked };
                                setDraftOverride({
                                  key: policyKey,
                                  config: { ...draftPolicy, description_policy: { requirements } },
                                });
                              }}
                            />
                          </div>
                        </div>
                        <div className="grid gap-2 sm:grid-cols-2">
                          <NumberPolicyInput
                            label="Min files"
                            value={requirement.applies_when?.min_files_changed}
                            min={0}
                            disabled={!draftPolicy}
                            onChange={(value) =>
                              updateDescriptionRequirement(index, (current) => ({
                                ...current,
                                applies_when: { ...(current.applies_when ?? { kind: "all" }), min_files_changed: value },
                              }))
                            }
                          />
                          <NumberPolicyInput
                            label="Min lines"
                            value={requirement.applies_when?.min_lines_changed}
                            min={0}
                            disabled={!draftPolicy}
                            onChange={(value) =>
                              updateDescriptionRequirement(index, (current) => ({
                                ...current,
                                applies_when: { ...(current.applies_when ?? { kind: "all" }), min_lines_changed: value },
                              }))
                            }
                          />
                        </div>
                        <ListTextArea
                          label="Path patterns"
                          value={requirement.applies_when?.path_patterns ?? []}
                          disabled={!draftPolicy}
                          onChange={(items) =>
                            updateDescriptionRequirement(index, (current) => ({
                              ...current,
                              applies_when: { ...(current.applies_when ?? { kind: "paths" }), path_patterns: items },
                            }))
                          }
                        />
                        <ListTextArea
                          label="Categories"
                          value={requirement.applies_when?.categories ?? []}
                          disabled={!draftPolicy}
                          onChange={(items) =>
                            updateDescriptionRequirement(index, (current) => ({
                              ...current,
                              applies_when: { ...(current.applies_when ?? { kind: "categories" }), categories: items },
                            }))
                          }
                        />
                        <div className="flex items-center justify-between gap-2 rounded-md border border-border px-3 py-2">
                          <Label className="text-xs text-muted-foreground">Require changed test files</Label>
                          <Switch
                            checked={requirement.applies_when?.require_test_files_changed ?? false}
                            disabled={!draftPolicy}
                            onCheckedChange={(checked) =>
                              updateDescriptionRequirement(index, (current) => ({
                                ...current,
                                applies_when: {
                                  ...(current.applies_when ?? { kind: "tests_changed" }),
                                  require_test_files_changed: checked,
                                },
                              }))
                            }
                          />
                        </div>
                        <Textarea
                          value={requirement.prompt}
                          disabled={!draftPolicy}
                          rows={4}
                          aria-label={`${requirement.key} prompt`}
                          onChange={(event) => {
                            if (!draftPolicy) return;
                            const requirements = [...draftPolicy.description_policy.requirements];
                            requirements[index] = { ...requirement, prompt: event.target.value };
                            setDraftOverride({
                              key: policyKey,
                              config: { ...draftPolicy, description_policy: { requirements } },
                            });
                          }}
                        />
                      </div>
                    ))}
                  </div>
                </div>

                <div className="flex justify-end">
                  <Button
                    disabled={!draftPolicy || savePolicy.isPending}
                    onClick={() => draftPolicy && savePolicy.mutate(draftPolicy)}
                  >
                    Save policy
                  </Button>
                </div>
              </CardContent>
            </Card>
          </TabsContent>

          <TabsContent value="insights">
            <div className="grid gap-3 sm:grid-cols-5">
              <InsightCard label="Reviews" value={insightCounts.total} />
              <InsightCard label="Approval rate" value={formatPercent(insightCounts.approved, insightCounts.total)} />
              <InsightCard label="Escalated" value={insightCounts.escalated} />
              <InsightCard label="Avg duration" value={formatMinutes(averageReviewMinutes)} />
              <InsightCard label="Stale" value={insightCounts.stale} />
            </div>
            <div className="mt-3 grid gap-3 lg:grid-cols-2">
              <Card>
                <CardHeader>
                  <CardTitle>Recent escalations</CardTitle>
                </CardHeader>
                <CardContent className="space-y-3">
                  {recentEscalations.length === 0 ? (
                    <div className="text-sm text-muted-foreground">No escalated reviews in the current filter.</div>
                  ) : (
                    recentEscalations.map((review) => (
                      <div key={review.id} className="flex min-w-0 items-center justify-between gap-3 border-b border-border pb-3 last:border-0 last:pb-0">
                        <div className="min-w-0">
                          <div className="truncate text-sm font-medium text-foreground">
                            #{review.github_pr_number} {review.pull_request_title}
                          </div>
                          <div className="mt-1 text-xs text-muted-foreground">{review.repository_name || review.github_repo}</div>
                        </div>
                        <Badge variant={decisionVariant(review)}>{decisionLabel(review)}</Badge>
                      </div>
                    ))
                  )}
                </CardContent>
              </Card>
              <Card>
                <CardHeader>
                  <CardTitle>Top repositories</CardTitle>
                </CardHeader>
                <CardContent className="space-y-3">
                  {topRepositories.length === 0 ? (
                    <div className="text-sm text-muted-foreground">No repository activity in the current filter.</div>
                  ) : (
                    topRepositories.map(([name, count]) => (
                      <div key={name} className="flex items-center justify-between gap-3 border-b border-border pb-3 last:border-0 last:pb-0">
                        <div className="truncate text-sm font-medium text-foreground">{name}</div>
                        <Badge variant="outline">{count}</Badge>
                      </div>
                    ))
                  )}
                </CardContent>
              </Card>
            </div>
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
          <Button
            variant={ready ? "outline" : "default"}
            size="sm"
            disabled={!repositorySelected || authRequired || setupPending || deletePending || isLoading}
            onClick={onSetup}
          >
            {ready ? <ShieldCheck className="h-4 w-4" /> : <Users className="h-4 w-4" />}
            {ready ? "Repair team" : "Create / repair team"}
          </Button>
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
	value,
	min,
	max,
	disabled,
	onChange,
}: {
	label: string;
	value?: number;
	min: number;
	max?: number;
	disabled?: boolean;
	onChange: (value: number) => void;
}) {
	return (
		<div className="rounded-md border border-border p-4">
			<Label className="text-xs text-muted-foreground">{label}</Label>
			<Input
				className="mt-2"
				type="number"
				min={min}
				max={max}
				value={value ?? ""}
				disabled={disabled}
				onChange={(event) => {
					const parsed = Number.parseInt(event.target.value, 10);
					if (Number.isNaN(parsed)) return;
					onChange(Math.max(min, max ? Math.min(max, parsed) : parsed));
				}}
			/>
		</div>
	);
}

function PolicyToggle({
	label,
	checked,
	disabled,
	onCheckedChange,
}: {
	label: string;
	checked: boolean;
	disabled?: boolean;
	onCheckedChange: (checked: boolean) => void;
}) {
	return (
		<div className="flex items-center justify-between rounded-md border border-border p-4">
			<Label className="text-sm text-foreground">{label}</Label>
			<Switch checked={checked} disabled={disabled} onCheckedChange={onCheckedChange} />
		</div>
	);
}

function ListTextArea({
  label,
  value,
  disabled,
  onChange,
}: {
  label: string;
  value: string[];
  disabled?: boolean;
  onChange: (items: string[]) => void;
}) {
  return (
    <div className="space-y-2">
      <Label className="text-xs text-muted-foreground">{label}</Label>
      <Textarea
        value={value.join("\n")}
        disabled={disabled}
        rows={4}
        onChange={(event) =>
          onChange(event.target.value.split(/\r?\n/).map((item) => item.trim()).filter(Boolean))
        }
      />
    </div>
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

function InsightCard({ label, value }: { label: string; value: number | string }) {
  return (
    <Card>
      <CardContent className="p-4">
        <div className="text-xs text-muted-foreground">{label}</div>
        <div className="mt-2 text-2xl font-semibold text-foreground">{value}</div>
      </CardContent>
    </Card>
  );
}
