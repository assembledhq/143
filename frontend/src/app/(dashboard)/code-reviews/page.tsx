"use client";

import Link from "next/link";
import { useMemo, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ClipboardCheck, ExternalLink, Settings2, BarChart3, RefreshCw } from "lucide-react";
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
import { api } from "@/lib/api";
import { queryKeys } from "@/lib/query-keys";
import type { CodeReviewApprovalMode, CodeReviewListItem, CodeReviewPolicyConfig } from "@/lib/types";

const ALL_REPOSITORIES = "all";

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

export default function CodeReviewsPage() {
  const queryClient = useQueryClient();
  const [repositoryFilter, setRepositoryFilter] = useState(ALL_REPOSITORIES);
  const repositoryId = repositoryFilter === ALL_REPOSITORIES ? undefined : repositoryFilter;

  const repositoriesQuery = useQuery({
    queryKey: queryKeys.repositories.all,
    queryFn: () => api.repositories.list(),
  });
  const reviewsQuery = useQuery({
    queryKey: queryKeys.codeReviews.list(repositoryId ?? null),
    queryFn: () => api.codeReviews.list({ repository_id: repositoryId, limit: 100 }),
  });
  const policyQuery = useQuery({
    queryKey: queryKeys.codeReviews.policy(repositoryId ?? null),
    queryFn: () => api.codeReviews.getPolicy(repositoryId ?? null),
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

  const reviews = useMemo(() => reviewsQuery.data?.data ?? [], [reviewsQuery.data?.data]);
  const repositories = repositoriesQuery.data?.data ?? [];
  const insightCounts = useMemo(() => {
    return reviews.reduce(
      (acc, review) => {
        acc.total += 1;
        if (review.decision === "approved") acc.approved += 1;
        if (review.decision === "needs_human_review" || review.decision === "comment_only") acc.escalated += 1;
        if (review.stale || review.status === "stale") acc.stale += 1;
        return acc;
      },
      { total: 0, approved: 0, escalated: 0, stale: 0 },
    );
  }, [reviews]);

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

        <div className="flex w-full flex-col gap-2 sm:w-72">
          <Label className="text-xs text-muted-foreground">Repository</Label>
          <Select value={repositoryFilter} onValueChange={setRepositoryFilter}>
            <SelectTrigger>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value={ALL_REPOSITORIES}>All repositories</SelectItem>
              {repositories.map((repo) => (
                <SelectItem key={repo.id} value={repo.id}>
                  {repo.full_name}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
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
                              <Button variant="ghost" size="sm" asChild>
                                <Link href={`/sessions/${review.session_id}`}>Session</Link>
                              </Button>
                              <Button variant="ghost" size="icon-sm" asChild aria-label="Open pull request">
                                <Link href={review.github_pr_url} target="_blank" rel="noreferrer">
                                  <ExternalLink className="h-4 w-4" />
                                </Link>
                              </Button>
                            </div>
                          </TableCell>
                        </TableRow>
                      ))}
                    </TableBody>
                  </Table>
                </CardContent>
              </Card>
            )}
          </TabsContent>

          <TabsContent value="config" className="space-y-4">
            <Card>
              <CardHeader>
                <CardTitle>Bot behavior</CardTitle>
              </CardHeader>
              <CardContent className="space-y-5">
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
                  <ConfigMetric label="Reviewer quorum" value={draftPolicy?.agent_roster.require_reviewer_quorum ?? "-"} />
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
                  <PolicyList
                    title="Description requirements"
                    items={draftPolicy?.description_policy.requirements.map((item) => item.title) ?? []}
                  />
                  <PolicyList
                    title="Excluded categories"
                    items={draftPolicy?.risk_policy.exclude_categories ?? []}
                  />
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
            <div className="grid gap-3 sm:grid-cols-4">
              <InsightCard label="Reviews" value={insightCounts.total} />
              <InsightCard label="Approved" value={insightCounts.approved} />
              <InsightCard label="Escalated" value={insightCounts.escalated} />
              <InsightCard label="Stale" value={insightCounts.stale} />
            </div>
          </TabsContent>
        </Tabs>
      </div>
    </main>
  );
}

function ConfigMetric({ label, value }: { label: string; value: string | number }) {
	return (
		<div className="rounded-md border border-border p-4">
			<div className="text-xs text-muted-foreground">{label}</div>
			<div className="mt-2 text-lg font-semibold text-foreground">{value}</div>
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

function PolicyList({ title, items }: { title: string; items: string[] }) {
  return (
    <div className="rounded-md border border-border p-4">
      <div className="text-sm font-medium text-foreground">{title}</div>
      <div className="mt-3 flex flex-wrap gap-2">
        {items.length === 0 ? (
          <span className="text-xs text-muted-foreground">None configured</span>
        ) : (
          items.map((item) => (
            <Badge key={item} variant="secondary">
              {item}
            </Badge>
          ))
        )}
      </div>
    </div>
  );
}

function InsightCard({ label, value }: { label: string; value: number }) {
  return (
    <Card>
      <CardContent className="p-4">
        <div className="text-xs text-muted-foreground">{label}</div>
        <div className="mt-2 text-2xl font-semibold text-foreground">{value}</div>
      </CardContent>
    </Card>
  );
}
