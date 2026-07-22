"use client";

import { useMemo, useState } from "react";
import { useMutation, useQuery } from "@tanstack/react-query";
import Link from "next/link";
import {
  AlertTriangle,
  CheckCircle2,
  CircleHelp,
  ExternalLink,
  LoaderCircle,
  MessageSquareWarning,
  MinusCircle,
  XCircle,
} from "lucide-react";
import { parseAsString, useQueryState } from "nuqs";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { ApiError, api } from "@/lib/api";
import type {
  AutomationDecision,
  AutomationDecisionStats,
  AutomationOutcomeDecision,
  AutomationRunStatus,
  ListResponse,
} from "@/lib/types";
import { formatDateTime, formatTimeAgo } from "@/lib/utils";

import { RunsTab } from "./runs-tab";

const PAGE_SIZE = 25;
const POLL_MS = 10_000;

type DecisionView = "decisions" | "runs";
type DecisionDisplayState =
  | AutomationOutcomeDecision
  | "evaluating"
  | "execution_failed"
  | "outcome_not_reported";

const OUTCOME_FILTERS = [
  "all",
  "passed",
  "changes_requested",
  "advisory",
  "not_applicable",
  "outcome_not_reported",
] as const;

type OutcomeFilter = (typeof OUTCOME_FILTERS)[number];

function toDecisionView(value: string): DecisionView {
  return value === "runs" ? "runs" : "decisions";
}

function toOutcomeFilter(value: string): OutcomeFilter {
  return OUTCOME_FILTERS.includes(value as OutcomeFilter)
    ? (value as OutcomeFilter)
    : "all";
}

function decisionsUnavailable(error: unknown): boolean {
  return error instanceof ApiError && (error.status === 404 || error.status === 501);
}

export function DecisionHistory({ automationId }: { automationId: string }) {
  const [viewParam, setViewParam] = useQueryState(
    "view",
    parseAsString.withDefault("decisions"),
  );
  const [outcomeParam, setOutcomeParam] = useQueryState(
    "outcome",
    parseAsString.withDefault("all"),
  );
  const [prParam, setPRParam] = useQueryState(
    "pr",
    parseAsString.withDefault(""),
  );

  const view = toDecisionView(viewParam);
  const outcome = toOutcomeFilter(outcomeParam);
  const normalizedPR = /^\d+$/.test(prParam) && Number(prParam) > 0 ? prParam : "";
  const filterParams = {
    outcome: outcome === "all" ? undefined : outcome,
    pr: normalizedPR || undefined,
  };

  const decisionsQuery = useQuery({
    queryKey: ["automation-decisions", automationId, filterParams],
    queryFn: () =>
      api.automations.listDecisions(automationId, {
        limit: PAGE_SIZE,
        ...filterParams,
      }),
    refetchInterval: POLL_MS,
    retry: false,
  });
  const statsQuery = useQuery({
    queryKey: ["automation-decision-stats", automationId],
    queryFn: () => api.automations.decisionStats(automationId),
    refetchInterval: 60_000,
    retry: false,
  });

  if (decisionsUnavailable(decisionsQuery.error)) {
    return (
      <div className="space-y-3">
        <div>
          <h2 className="text-sm font-semibold text-foreground">
            Execution history
          </h2>
          <p className="mt-1 text-xs text-muted-foreground">
            Structured PR decisions are not available yet. Run status below
            describes execution only; it does not mean a PR passed review.
          </p>
        </div>
        <RunsTab automationId={automationId} />
      </div>
    );
  }

  if (decisionsQuery.isLoading) {
    return (
      <div className="space-y-4">
        <div>
          <h2 className="text-sm font-semibold text-foreground">PR decisions</h2>
          <p className="mt-1 text-xs text-muted-foreground">
            Loading review outcomes separately from execution status.
          </p>
        </div>
        <DecisionSkeleton />
      </div>
    );
  }

  return (
    <div className="space-y-4">
      <div>
        <h2 className="text-sm font-semibold text-foreground">PR decisions</h2>
        <p className="mt-1 text-xs text-muted-foreground">
          One row per PR revision. Decisions describe the review outcome;
          execution status only describes whether the automation ran.
        </p>
      </div>

      <DecisionSummary
        stats={statsQuery.data?.data}
        isLoading={statsQuery.isLoading}
        isError={statsQuery.isError}
      />

      <Tabs
        value={view}
        onValueChange={(value) => void setViewParam(toDecisionView(value))}
      >
        <TabsList variant="line" size="sm" aria-label="Automation history view">
          <TabsTrigger value="decisions">Decisions</TabsTrigger>
          <TabsTrigger value="runs">Raw runs</TabsTrigger>
        </TabsList>
        <TabsContent value="decisions" className="mt-3 space-y-3">
          <DecisionFilters
            outcome={outcome}
            pr={prParam}
            onOutcomeChange={(value) => void setOutcomeParam(value)}
            onPRChange={(value) => void setPRParam(value)}
          />
          {prParam !== "" && normalizedPR === "" ? (
            <p className="text-xs text-destructive">
              PR number must be a positive whole number.
            </p>
          ) : null}
          <DecisionList
            key={`${outcome}:${normalizedPR}`}
            automationId={automationId}
            response={decisionsQuery.data}
            isLoading={decisionsQuery.isLoading}
            isError={decisionsQuery.isError}
            filterParams={filterParams}
          />
        </TabsContent>
        <TabsContent value="runs" className="mt-3 space-y-3">
          <Card variant="recessed">
            <CardContent className="p-3 text-xs text-muted-foreground">
              Raw runs show every execution attempt, including retries and
              no-ops. Use this view to debug execution, not to infer whether a
              PR was accepted or rejected.
            </CardContent>
          </Card>
          <RunsTab automationId={automationId} />
        </TabsContent>
      </Tabs>
    </div>
  );
}

function DecisionSummary({
  stats,
  isLoading,
  isError,
}: {
  stats?: AutomationDecisionStats;
  isLoading: boolean;
  isError: boolean;
}) {
  if (isLoading) {
    return <div className="h-28 animate-pulse rounded-xl bg-muted/25" />;
  }
  if (isError || !stats) {
    return (
      <Card variant="recessed">
        <CardContent className="p-3 text-xs text-muted-foreground">
          Decision totals are temporarily unavailable. Individual decisions
          are still shown below.
        </CardContent>
      </Card>
    );
  }

  const counts: Array<[string, number, DecisionDisplayState]> = [
    ["Passed", stats.passed, "passed"],
    ["Changes requested", stats.changes_requested, "changes_requested"],
    ["Advisory", stats.advisory, "advisory"],
    ["Not applicable", stats.not_applicable, "not_applicable"],
    ["Evaluating", stats.evaluating, "evaluating"],
    ["Execution failed", stats.execution_failed, "execution_failed"],
    ["Outcome not reported", stats.outcome_not_reported, "outcome_not_reported"],
  ];

  return (
    <Card>
      <CardContent className="space-y-4 p-4">
        <div className="grid grid-cols-3 gap-3">
          <SummaryMetric label="PRs" value={stats.unique_pull_requests} />
          <SummaryMetric label="Revisions" value={stats.unique_revisions} />
          <SummaryMetric label="Attempts" value={stats.total_runs} />
        </div>
        <div className="flex flex-wrap gap-2">
          {counts.map(([label, count, state]) => (
            <OutcomeBadge key={state} state={state} label={`${label} ${count}`} />
          ))}
        </div>
      </CardContent>
    </Card>
  );
}

function SummaryMetric({ label, value }: { label: string; value: number }) {
  return (
    <div>
      <p className="text-lg font-semibold tabular-nums text-foreground">{value}</p>
      <p className="text-xs text-muted-foreground">{label}</p>
    </div>
  );
}

function DecisionFilters({
  outcome,
  pr,
  onOutcomeChange,
  onPRChange,
}: {
  outcome: OutcomeFilter;
  pr: string;
  onOutcomeChange: (value: OutcomeFilter) => void;
  onPRChange: (value: string) => void;
}) {
  return (
    <div className="flex flex-col gap-2 sm:flex-row">
      <Select
        value={outcome}
        onValueChange={(value) => onOutcomeChange(toOutcomeFilter(value))}
      >
        <SelectTrigger className="w-full sm:w-52" aria-label="Filter by outcome">
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          <SelectItem value="all">All outcomes</SelectItem>
          <SelectItem value="passed">Passed</SelectItem>
          <SelectItem value="changes_requested">Changes requested</SelectItem>
          <SelectItem value="advisory">Advisory</SelectItem>
          <SelectItem value="not_applicable">Not applicable</SelectItem>
          <SelectItem value="outcome_not_reported">Outcome not reported</SelectItem>
        </SelectContent>
      </Select>
      <Input
        value={pr}
        onChange={(event) => onPRChange(event.target.value.trim())}
        inputMode="numeric"
        placeholder="PR number"
        aria-label="Filter by PR number"
        className="w-full sm:w-40"
      />
      {outcome !== "all" || pr !== "" ? (
        <Button
          variant="ghost"
          size="sm"
          onClick={() => {
            onOutcomeChange("all");
            onPRChange("");
          }}
        >
          Clear filters
        </Button>
      ) : null}
    </div>
  );
}

function DecisionList({
  automationId,
  response,
  isLoading,
  isError,
  filterParams,
}: {
  automationId: string;
  response?: ListResponse<AutomationDecision>;
  isLoading: boolean;
  isError: boolean;
  filterParams: { outcome?: string; pr?: string };
}) {
  const [extraPages, setExtraPages] = useState<AutomationDecision[][]>([]);
  const [cursor, setCursor] = useState(response?.meta?.next_cursor || "");
  const firstCursor = response?.meta?.next_cursor || "";
  const nextCursor = extraPages.length > 0 ? cursor : firstCursor;
  const decisions = useMemo(
    () => [response?.data ?? [], ...extraPages].flat(),
    [extraPages, response?.data],
  );
  const loadMore = useMutation({
    mutationFn: () =>
      api.automations.listDecisions(automationId, {
        limit: PAGE_SIZE,
        cursor: nextCursor,
        ...filterParams,
      }),
    onSuccess: (next) => {
      if (next.data.length > 0) {
        setExtraPages((pages) => [...pages, next.data]);
      }
      setCursor(next.meta?.next_cursor || "");
    },
  });

  if (isLoading) return <DecisionSkeleton />;
  if (isError) {
    return (
      <Card variant="recessed">
        <CardContent className="p-5 text-sm text-destructive">
          Failed to load PR decisions. Raw runs remain available in the next
          tab.
        </CardContent>
      </Card>
    );
  }
  if (decisions.length === 0) {
    return (
      <Card variant="recessed">
        <CardContent className="p-8 text-center">
          <p className="text-sm font-medium text-foreground">
            No matching PR decisions
          </p>
          <p className="mt-1 text-xs text-muted-foreground">
            Decisions appear after a GitHub-triggered run evaluates a pull
            request revision. Try clearing filters or inspect Raw runs.
          </p>
        </CardContent>
      </Card>
    );
  }

  return (
    <div className="space-y-3">
      {decisions.map((decision) => (
        <DecisionCard key={decision.run_id} decision={decision} />
      ))}
      {loadMore.isError ? (
        <p className="text-center text-xs text-destructive">
          Failed to load more decisions. Please try again.
        </p>
      ) : null}
      {nextCursor ? (
        <Button
          variant="ghost"
          size="sm"
          className="w-full rounded-xl border border-dashed border-border/70"
          onClick={() => loadMore.mutate()}
          disabled={loadMore.isPending}
        >
          {loadMore.isPending ? "Loading…" : "Load more decisions"}
        </Button>
      ) : null}
    </div>
  );
}

export function DecisionCard({ decision }: { decision: AutomationDecision }) {
  const state = decisionDisplayState(decision);
  const title =
    decision.target.pull_request_title ||
    `${decision.target.repository} #${decision.target.pull_request_number}`;
  const action = decision.outcome?.external_action;

  return (
    <Card>
      <CardContent className="space-y-3 p-4">
        <div className="flex flex-col justify-between gap-3 sm:flex-row sm:items-start">
          <div className="min-w-0">
            <div className="flex flex-wrap items-center gap-2">
              <OutcomeBadge state={state} />
              {decision.attempt_count > 1 ? (
                <Badge variant="outline">
                  Evaluated {decision.attempt_count} times
                </Badge>
              ) : null}
              {decision.outcome?.source === "legacy_inferred" ? (
                <Badge variant="outline">Inferred from legacy summary</Badge>
              ) : null}
            </div>
            <p className="mt-2 break-words text-sm font-medium text-foreground">
              {title}
            </p>
            <p className="mt-1 text-xs text-muted-foreground">
              {decision.target.repository} #{decision.target.pull_request_number}
              {decision.target.head_sha
                ? ` · revision ${decision.target.head_sha.slice(0, 8)}`
                : ""}
            </p>
          </div>
          <Button asChild variant="outline" size="sm">
            <a
              href={decision.target.pull_request_url}
              target="_blank"
              rel="noreferrer"
            >
              Open PR
              <ExternalLink className="ml-1 h-3.5 w-3.5" />
            </a>
          </Button>
        </div>

        {decision.outcome?.reason ? (
          <p className="text-sm leading-6 text-foreground">
            {decision.outcome.reason}
          </p>
        ) : (
          <p className="text-sm text-muted-foreground">
            {missingOutcomeExplanation(state)}
          </p>
        )}

        <div className="flex flex-wrap items-center gap-x-3 gap-y-2 text-xs text-muted-foreground">
          <span>{formatTimeAgo(decision.triggered_at)}</span>
          <span>Execution: {executionLabel(decision.execution_status)}</span>
          {decision.completed_at ? (
            <span>Completed {formatDateTime(decision.completed_at)}</span>
          ) : null}
        </div>

        {action || decision.session_id ? (
          <div className="flex flex-wrap gap-2 border-t border-border/70 pt-3">
            {action ? (
              <Button asChild variant="outline" size="sm">
                <a href={action.url} target="_blank" rel="noreferrer">
                  {externalActionLabel(action.action_type)}
                  <ExternalLink className="ml-1 h-3.5 w-3.5" />
                </a>
              </Button>
            ) : null}
            {decision.session_id ? (
              <Button asChild variant="ghost" size="sm">
                <Link href={`/sessions/${decision.session_id}`}>Open session</Link>
              </Button>
            ) : null}
          </div>
        ) : null}
      </CardContent>
    </Card>
  );
}

function decisionDisplayState(decision: AutomationDecision): DecisionDisplayState {
  if (decision.outcome) return decision.outcome.decision;
  if (
    decision.execution_status === "pending" ||
    decision.execution_status === "running"
  ) {
    return "evaluating";
  }
  if (decision.execution_status === "failed") return "execution_failed";
  return "outcome_not_reported";
}

function OutcomeBadge({
  state,
  label,
}: {
  state: DecisionDisplayState;
  label?: string;
}) {
  const config = {
    passed: { label: "Passed", variant: "success" as const, icon: CheckCircle2 },
    changes_requested: {
      label: "Changes requested",
      variant: "destructive" as const,
      icon: XCircle,
    },
    advisory: {
      label: "Advisory",
      variant: "warning" as const,
      icon: MessageSquareWarning,
    },
    not_applicable: {
      label: "Not applicable",
      variant: "secondary" as const,
      icon: MinusCircle,
    },
    evaluating: {
      label: "Evaluating",
      variant: "info" as const,
      icon: LoaderCircle,
    },
    execution_failed: {
      label: "Execution failed",
      variant: "destructive" as const,
      icon: AlertTriangle,
    },
    outcome_not_reported: {
      label: "Outcome not reported",
      variant: "outline" as const,
      icon: CircleHelp,
    },
  }[state];
  const Icon = config.icon;
  return (
    <Badge variant={config.variant}>
      <Icon className={state === "evaluating" ? "animate-spin" : undefined} />
      {label ?? config.label}
    </Badge>
  );
}

function missingOutcomeExplanation(state: DecisionDisplayState): string {
  switch (state) {
    case "evaluating":
      return "This PR revision is still being evaluated.";
    case "execution_failed":
      return "The automation failed before it reported a review decision.";
    case "outcome_not_reported":
      return "The run finished without a structured review decision. Do not infer that the PR passed.";
    default:
      return "No outcome reason was reported.";
  }
}

function executionLabel(status: AutomationRunStatus): string {
  return status === "completed_noop" ? "no-op" : status.replaceAll("_", " ");
}

function externalActionLabel(actionType: string): string {
  switch (actionType) {
    case "github_review_changes_requested":
      return "View requested changes";
    case "github_review_approved":
      return "View approval";
    default:
      return "View GitHub comment";
  }
}

function DecisionSkeleton() {
  return (
    <div
      className="space-y-3"
      aria-busy
      aria-live="polite"
      aria-label="Loading PR decisions"
    >
      <div className="h-36 animate-pulse rounded-xl bg-muted/25" />
      <div className="h-28 animate-pulse rounded-xl bg-muted/25" />
    </div>
  );
}
