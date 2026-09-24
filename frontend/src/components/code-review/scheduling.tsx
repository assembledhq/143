"use client";

import { useRef, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Button } from "@/components/ui/button";
import { DisabledTooltip } from "@/components/ui/disabled-tooltip";
import { CalendarClock } from "lucide-react";
import { EmptyState } from "@/components/empty-state";
import { ResponsiveResourceList, type ResponsiveResourceListColumn } from "@/components/responsive-resource-list";
import { SectionGroup } from "@/components/section-group";
import { StatusLabel } from "@/components/status-label";
import { ErrorNotice } from "@/components/ui/error-notice";
import { ExternalLink } from "@/components/ui/external-link";
import { queryKeys } from "@/lib/query-keys";
import { api } from "@/lib/api";
import { pollMs } from "@/lib/poll-intervals";
import type { CodeReviewSchedule, CodeReviewScheduledTarget } from "@/lib/types";

const scheduleKey = ["code-review-schedules"];
export function ReviewNowButton({ prID, disabledReason }: { prID: string; disabledReason?: string }) {
  const client = useQueryClient();
  const policy = useQuery({ queryKey: queryKeys.codeReviews.policy, queryFn: () => api.codeReviews.getPolicy() });
  const requestID = useRef<string | null>(null);
  const mutation = useMutation({
    mutationFn: () => {
      requestID.current ??= crypto.randomUUID();
      return api.codeReviews.reviewNow(prID, requestID.current);
    },
    onSuccess: () => {
      requestID.current = null;
      void client.invalidateQueries({ queryKey: scheduleKey });
      void client.invalidateQueries({ queryKey: ["code-reviews"] });
    },
  });
  if (!policy.data?.data.capabilities?.scheduling) return null;
  const reason = disabledReason ?? (mutation.isPending ? "Your request is being recorded." : undefined);
  return <div className="space-y-1">
    <DisabledTooltip disabled={Boolean(reason)} content={reason}>
      <Button size="sm" variant="outline" disabled={Boolean(reason)} onClick={() => mutation.mutate()}>{mutation.isPending ? "Requesting…" : "Request Full Re-Review Now"}</Button>
    </DisabledTooltip>
    {mutation.isSuccess ? <p role="status" className="text-xs text-muted-foreground">Review requested.</p> : null}
    {mutation.isError ? <p role="alert" className="text-xs text-destructive">{mutation.error.message}</p> : null}
  </div>;
}
const waitLabels: Record<string, string> = {
 quiet_period: "Waiting for changes to settle", minimum_interval: "Waiting for review interval",
 draft: "Review queued", manual_pause: "Automatic reviews paused",
 policy_disabled: "Review policy disabled", already_approved: "Automatic review stopped after approval",
 active_review: "Waiting for the active review", context_unavailable: "Waiting for GitHub",
};
function ScheduleActions({ schedule }: { schedule: CodeReviewSchedule }) {
  const client = useQueryClient();
  const pause = useMutation({
    mutationFn: () => api.codeReviews.pauseSchedule(schedule.pull_request_id, !schedule.automatic_paused),
    onSuccess: () => { void client.invalidateQueries({ queryKey: scheduleKey }); },
  });
  const ineligible = schedule.state === "closed" ? "This PR is closed." : schedule.wait_reason === "policy_disabled" ? "An administrator must enable the review policy." : undefined;
  return <div className="flex flex-wrap items-start gap-2">
    <ReviewNowButton prID={schedule.pull_request_id} disabledReason={ineligible} />
    <DisabledTooltip disabled={pause.isPending} content="Wait for the scheduling change to finish.">
      <Button size="sm" variant="ghost" disabled={pause.isPending} onClick={() => pause.mutate()}>{schedule.automatic_paused ? "Resume automatic reviews" : "Pause automatic reviews"}</Button>
    </DisabledTooltip>
    {pause.isError ? <p role="alert" className="text-xs text-destructive">{pause.error.message}</p> : null}
  </div>;
}
function QueuePullRequest({ target }: { target: CodeReviewScheduledTarget }) {
  return <div className="min-w-0 space-y-1">
    <ExternalLink href={target.github_pr_url} className="text-sm">#{target.github_pr_number} {target.title}</ExternalLink>
    <p className="text-xs text-muted-foreground">{target.github_repo}</p>
  </div>;
}

function QueueWaitReason({ schedule }: { schedule: CodeReviewSchedule }) {
  return <StatusLabel label={waitLabels[schedule.wait_reason] ?? "Review queued"}
    tone={schedule.wait_reason === "context_unavailable" ? "warning" : "neutral"}
    stateKey={schedule.wait_reason || "queued"} />;
}

function queueTime(value: string | null | undefined) {
  return value ? new Date(value).toLocaleString() : "—";
}

export function ScheduledReviews({ canManage, enabled }: { canManage: boolean; enabled: boolean }) {
  const [cursors, setCursors] = useState([""]);
  const cursor = cursors[cursors.length - 1];
  const query = useQuery({
    queryKey: [...scheduleKey, cursor], enabled,
    queryFn: () => api.codeReviews.pendingSchedules(cursor || undefined),
    refetchInterval: pollMs(15_000),
  });
  const targets = query.data?.data ?? [];
  const nextCursor = query.data?.meta.next_cursor;
  const columns: ResponsiveResourceListColumn<CodeReviewScheduledTarget>[] = [
    { id: "pr", header: "Pull request", render: (target) => <QueuePullRequest target={target} /> },
    { id: "reason", header: "Wait reason", render: (target) => <QueueWaitReason schedule={target.schedule} /> },
    { id: "waiting", header: "Waiting since", className: "text-right", cellClassName: "text-right tabular-nums text-xs text-muted-foreground", render: (target) => queueTime(target.schedule.first_pending_at) },
    { id: "eligible", header: "Earliest start", className: "text-right", cellClassName: "text-right tabular-nums text-xs text-muted-foreground", render: (target) => queueTime(target.schedule.eligible_at) },
    ...(canManage ? [{ id: "actions", header: "Actions", render: (target: CodeReviewScheduledTarget) => <ScheduleActions schedule={target.schedule} /> }] : []),
  ];
  if (!enabled) return <EmptyState icon={CalendarClock} title="Review queue unavailable" description="The GitHub review service must be configured before reviews can be scheduled." />;
  return <SectionGroup title="Review queue" description="Pending review requests across repositories. Request Full Re-Review Now skips timing delays; pause holds automatic reviews for that PR.">
    {query.isPending ? <p role="status" className="text-sm text-muted-foreground">Loading review queue…</p> : query.isError ? (
      <ErrorNotice title="Review queue could not be loaded" action={{ label: "Retry", onClick: () => void query.refetch() }} />
    ) : <ResponsiveResourceList
      ariaLabel="Review queue" items={targets} getItemKey={(target) => target.schedule.id} columns={columns}
      emptyState={<EmptyState variant="inline" icon={CalendarClock} title={cursor ? "No pending reviews on this page" : "No reviews waiting"} description={cursor ? "Pending work may have changed. Go back to see earlier requests." : "PRs waiting for an automatic or manual review will appear here."} />}
      renderMobileItem={(target) => <div className="space-y-3 px-4 py-3.5">
        <QueuePullRequest target={target} />
        <QueueWaitReason schedule={target.schedule} />
        <div className="space-y-1 text-xs text-muted-foreground tabular-nums">
          <p>Waiting since {queueTime(target.schedule.first_pending_at)}</p>
          <p>Earliest start {queueTime(target.schedule.eligible_at)}</p>
        </div>
        {canManage ? <ScheduleActions schedule={target.schedule} /> : null}
      </div>}
    />}
    <div className="flex flex-wrap items-center justify-between gap-3">
      <p className="text-xs text-muted-foreground tabular-nums">Page {cursors.length} · 25 per page</p>
      <div className="flex gap-2">
        <DisabledTooltip disabled={cursors.length === 1 || query.isFetching} content={cursors.length === 1 ? "You are on the first page." : "Wait for the queue to finish loading."}>
          <Button size="sm" variant="outline" disabled={cursors.length === 1 || query.isFetching} onClick={() => setCursors((previous) => previous.slice(0, -1))}>Previous</Button>
        </DisabledTooltip>
        <DisabledTooltip disabled={!nextCursor || query.isFetching || query.isError} content={query.isFetching ? "Wait for the queue to finish loading." : query.isError ? "Retry loading this page first." : "There are no more pending reviews."}>
          <Button size="sm" variant="outline" disabled={!nextCursor || query.isFetching || query.isError} onClick={() => { if (nextCursor) setCursors((previous) => [...previous, nextCursor]); }}>Next</Button>
        </DisabledTooltip>
      </div>
    </div>
  </SectionGroup>;
}
