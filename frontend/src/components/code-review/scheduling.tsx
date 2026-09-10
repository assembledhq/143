"use client";

import { useRef } from "react";
import { useInfiniteQuery, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Button } from "@/components/ui/button";
import { DisabledTooltip } from "@/components/ui/disabled-tooltip";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { queryKeys } from "@/lib/query-keys";
import { api } from "@/lib/api";
import { pollMs } from "@/lib/poll-intervals";
import type { CodeReviewSchedule } from "@/lib/types";

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
      <Button size="sm" variant="outline" disabled={Boolean(reason)} onClick={() => mutation.mutate()}>{mutation.isPending ? "Requesting…" : "Review now"}</Button>
    </DisabledTooltip>
    {mutation.isSuccess ? <p role="status" className="text-xs text-muted-foreground">Review requested.</p> : null}
    {mutation.isError ? <p role="alert" className="text-xs text-destructive">{mutation.error.message}</p> : null}
  </div>;
}
const waitLabels: Record<string, string> = {
 quiet_period: "Waiting for changes to settle", minimum_interval: "Waiting for review interval",
 draft: "Waiting for the PR to be ready", manual_pause: "Automatic reviews paused",
 policy_disabled: "Review policy disabled", already_approved: "Automatic review stopped after approval",
 active_review: "Waiting for the active review", context_unavailable: "Waiting for GitHub",
};
function ScheduleActions({ schedule }: { schedule: CodeReviewSchedule }) {
  const client = useQueryClient();
  const pause = useMutation({
    mutationFn: () => api.codeReviews.pauseSchedule(schedule.pull_request_id, !schedule.automatic_paused),
    onSuccess: () => { void client.invalidateQueries({ queryKey: scheduleKey }); },
  });
  const ineligible = schedule.is_draft ? "Mark this PR ready before requesting review." : schedule.state === "closed" ? "This PR is closed." : schedule.wait_reason === "policy_disabled" ? "An administrator must enable the review policy." : undefined;
  return <div className="flex flex-wrap items-start gap-2">
    <ReviewNowButton prID={schedule.pull_request_id} disabledReason={ineligible} />
    <DisabledTooltip disabled={pause.isPending} content="Wait for the scheduling change to finish.">
      <Button size="sm" variant="ghost" disabled={pause.isPending} onClick={() => pause.mutate()}>{schedule.automatic_paused ? "Resume automatic reviews" : "Pause automatic reviews"}</Button>
    </DisabledTooltip>
    {pause.isError ? <p role="alert" className="text-xs text-destructive">{pause.error.message}</p> : null}
  </div>;
}
export function ScheduledReviews({ canManage, enabled }: { canManage: boolean; enabled: boolean }) {
  const query = useInfiniteQuery({
    queryKey: scheduleKey, initialPageParam: "", enabled,
    queryFn: ({ pageParam }) => api.codeReviews.pendingSchedules(pageParam || undefined),
    getNextPageParam: (page) => page.meta.next_cursor || undefined,
    refetchInterval: pollMs(15_000),
  });
  const targets = query.data?.pages.flatMap((page) => page.data) ?? [];
  if (!enabled) return null;
  if (query.isPending) return <p className="text-sm text-muted-foreground">Loading scheduled reviews…</p>;
  if (query.isError) return <div role="alert"><p className="text-sm text-destructive">Scheduled reviews could not be loaded.</p><Button variant="outline" size="sm" onClick={() => void query.refetch()}>Retry</Button></div>;
  if (targets.length === 0) return null;
  return <Card>
    <CardHeader><CardTitle className="text-sm">Scheduled reviews</CardTitle><p className="text-xs text-muted-foreground">Pending requests across repositories. Review activity filters below apply to executed reviews.</p></CardHeader>
    <CardContent className="divide-y divide-border">
      {targets.map((target) => {
        const s = target.schedule;
        const due = s.eligible_at;
        return <div key={s.id} className="flex flex-wrap items-center justify-between gap-3 py-3">
          <div className="min-w-0 space-y-1"><a className="text-sm font-medium hover:underline" href={target.github_pr_url} target="_blank" rel="noreferrer">{target.github_repo}#{target.github_pr_number}: {target.title}</a>
            <p className="text-xs text-muted-foreground">{waitLabels[s.wait_reason] ?? "Review queued"}{due ? ` · Eligible ${new Date(due).toLocaleTimeString()}` : ""}</p>
            {s.first_pending_at ? <p className="text-xs text-muted-foreground">Waiting since {new Date(s.first_pending_at).toLocaleString()}</p> : null}
          </div>
          {canManage ? <ScheduleActions schedule={s} /> : null}
        </div>;
      })}
      {query.hasNextPage ? <Button size="sm" variant="outline" onClick={() => void query.fetchNextPage()}>Load more</Button> : null}
    </CardContent>
  </Card>;
}
