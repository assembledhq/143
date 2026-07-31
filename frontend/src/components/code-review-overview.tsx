"use client";

import { ChevronDown, SlidersHorizontal } from "lucide-react";
import type { ReactNode } from "react";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { ErrorNotice } from "@/components/ui/error-notice";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import type { CodeReviewListOutcome, CodeReviewStats, Repository } from "@/lib/types";

export const ALL_REPOSITORIES = "all";
export const ALL_OUTCOMES = "all";
export const ALL_RISKS = "all";
export const AUTOMATICALLY_APPROVED = "automatically_approved" satisfies CodeReviewListOutcome;
export const COMPLETED_NOT_APPROVED = "completed_not_approved" satisfies CodeReviewListOutcome;

function percentage(value: number, total: number): string {
  return total > 0 ? `${Math.round((value / total) * 100)}%` : "0%";
}

export function formatReviewTurnaround(seconds: number | null): string {
  if (seconds === null || !Number.isFinite(seconds)) return "—";
  if (seconds < 60) return `${Math.round(seconds)}s`;
  const minutes = Math.round(seconds / 60);
  if (minutes < 60) return `${minutes}m`;
  const hours = Math.floor(minutes / 60);
  const remainingMinutes = minutes % 60;
  return remainingMinutes === 0 ? `${hours}h` : `${hours}h ${remainingMinutes}m`;
}

export function CodeReviewSummaryCards({
  stats,
  isLoading,
  isError,
  onRetry,
}: {
  stats?: CodeReviewStats;
  isLoading: boolean;
  isError: boolean;
  onRetry: () => void;
}) {
  const unavailable = isError ? "Metrics unavailable" : "Loading selected time window";
  const cards = stats
    ? [
        { label: "Reviews completed", value: stats.reviews_completed.toLocaleString(), context: "In selected time window" },
        {
          label: "Automatically approved",
          value: stats.automatically_approved.toLocaleString(),
          context: `${percentage(stats.automatically_approved, stats.reviews_completed)} of completed reviews`,
        },
        {
          label: "Approval rate",
          value: percentage(stats.automatically_approved, stats.reviews_completed),
          context: `${stats.needs_human_review.toLocaleString()} need human review`,
        },
        { label: "Median turnaround", value: formatReviewTurnaround(stats.median_turnaround_seconds), context: "Queued to completed" },
      ]
    : ["Reviews completed", "Automatically approved", "Approval rate", "Median turnaround"].map((label) => ({
        label,
        value: "—",
        context: unavailable,
      }));

  return (
    <div className="space-y-3" role="region" aria-label="Code review statistics" aria-busy={isLoading}>
      {isError ? (
        <ErrorNotice
          title={stats ? "Metrics may be out of date" : "Metrics unavailable"}
          description={stats ? "Showing the last successful result because the latest refresh failed." : "The selected review metrics could not be loaded."}
          action={{ label: "Retry", onClick: onRetry }}
        />
      ) : null}
      <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-4">
        {cards.map((card) => (
          <Card key={card.label}>
            <CardContent className="space-y-1.5">
              <p className="text-xs font-medium text-muted-foreground">{card.label}</p>
              <p className="text-2xl font-semibold tabular-nums text-foreground">{card.value}</p>
              <p className="text-xs text-muted-foreground">{card.context}</p>
            </CardContent>
          </Card>
        ))}
      </div>
    </div>
  );
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
    <div className="flex flex-col gap-2">
      <Label className="text-xs text-muted-foreground">{label}</Label>
      <Select value={value} onValueChange={onValueChange}>
        <SelectTrigger aria-label={label}><SelectValue /></SelectTrigger>
        <SelectContent>{children}</SelectContent>
      </Select>
    </div>
  );
}

export interface CodeReviewFilterValues {
  repository: string;
  outcome: string;
  risk: string;
  status: string;
  search: string;
  timeRange: string;
}

export function CodeReviewFilters({
  values,
  repositories,
  mobileOpen,
  onMobileOpenChange,
  onChange,
  id,
}: {
  values: CodeReviewFilterValues;
  repositories: Repository[];
  mobileOpen: boolean;
  onMobileOpenChange: (open: boolean) => void;
  onChange: (field: keyof CodeReviewFilterValues, value: string) => void;
  id: string;
}) {
  return (
    <>
      <Button type="button" variant="outline" size="sm" className="w-full justify-between md:hidden" aria-expanded={mobileOpen} aria-controls={id} onClick={() => onMobileOpenChange(!mobileOpen)}>
        <span className="flex items-center gap-2"><SlidersHorizontal className="h-4 w-4" />Filter reviews</span>
        <ChevronDown className={`h-4 w-4 transition-transform ${mobileOpen ? "rotate-180" : ""}`} />
      </Button>
      <div id={id} className={`${mobileOpen ? "grid" : "hidden"} gap-3 rounded-xl border border-border bg-card p-3 shadow-sm md:grid md:grid-cols-2 md:rounded-none md:border-0 md:bg-transparent md:p-0 md:shadow-none lg:grid-cols-3 xl:grid-cols-[minmax(12rem,18rem)_repeat(3,minmax(9rem,11rem))_minmax(12rem,1fr)_minmax(9rem,11rem)]`}>
        <FilterSelect label="Repository" value={values.repository} onValueChange={(value) => onChange("repository", value)}>
          <SelectItem value={ALL_REPOSITORIES}>All repositories</SelectItem>
          {repositories.map((repo) => <SelectItem key={repo.id} value={repo.id}>{repo.full_name}</SelectItem>)}
        </FilterSelect>
        <FilterSelect label="Outcome" value={values.outcome} onValueChange={(value) => onChange("outcome", value)}>
          <SelectItem value={ALL_OUTCOMES}>All outcomes</SelectItem>
          <SelectItem value={AUTOMATICALLY_APPROVED}>Automatically approved</SelectItem>
          <SelectItem value={COMPLETED_NOT_APPROVED}>Ran successfully — not approved</SelectItem>
          <SelectItem value="needs_human_review">Needs human review</SelectItem>
          <SelectItem value="comment_only">Comment-only decision</SelectItem>
          <SelectItem value="blocked">Blocked</SelectItem>
        </FilterSelect>
        <FilterSelect label="Risk" value={values.risk} onValueChange={(value) => onChange("risk", value)}>
          <SelectItem value={ALL_RISKS}>All risk</SelectItem>
          <SelectItem value="acceptable">Acceptable</SelectItem>
          <SelectItem value="needs_review">Needs review</SelectItem>
        </FilterSelect>
        <FilterSelect label="Status" value={values.status} onValueChange={(value) => onChange("status", value)}>
          <SelectItem value="current">Current reviews</SelectItem>
          <SelectItem value="completed">Completed</SelectItem>
          <SelectItem value="in_progress">In progress</SelectItem>
          <SelectItem value="failed">Failed</SelectItem>
          <SelectItem value="cancelled">Cancelled</SelectItem>
          <SelectItem value="superseded">Superseded history</SelectItem>
          <SelectItem value="all">All attempts</SelectItem>
        </FilterSelect>
        <div className="flex flex-col gap-2">
          <Label className="text-xs text-muted-foreground">Search</Label>
          <Input value={values.search} onChange={(event) => onChange("search", event.target.value)} placeholder="PR, repo, or title" aria-label="Search code reviews" />
        </div>
        <FilterSelect label="Time window" value={values.timeRange} onValueChange={(value) => onChange("timeRange", value)}>
          <SelectItem value="7d">Last 7 days</SelectItem>
          <SelectItem value="30d">Last 30 days</SelectItem>
          <SelectItem value="90d">Last 90 days</SelectItem>
          <SelectItem value="all">All time</SelectItem>
        </FilterSelect>
      </div>
    </>
  );
}
