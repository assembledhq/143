"use client";

import Link from "next/link";
import { useState } from "react";
import { useInfiniteQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { MessageSquareText } from "lucide-react";
import { StatusLabel } from "@/components/status-label";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { DisabledTooltip } from "@/components/ui/disabled-tooltip";
import { ErrorNotice } from "@/components/ui/error-notice";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Label } from "@/components/ui/label";
import { Textarea } from "@/components/ui/textarea";
import { api } from "@/lib/api";
import { notify as toast } from "@/lib/notify";
import { queryKeys } from "@/lib/query-keys";
import type { CodeReviewDispute, CodeReviewListItem } from "@/lib/types";

function codeReviewDisputeStatusLabel(value: string): string {
  const normalized = value.replaceAll("_", " ");
  return normalized.charAt(0).toUpperCase() + normalized.slice(1);
}

function formatDate(value: string): string {
  return new Date(value).toLocaleString(undefined, { month: "short", day: "numeric", hour: "numeric", minute: "2-digit" });
}

export function DecisionFeedback({ review, reasonCodes, canManagePolicy }: {
  review: CodeReviewListItem;
  reasonCodes: string[];
  canManagePolicy: boolean;
}) {
  const queryClient = useQueryClient();
  const [disputeDialogOpen, setDisputeDialogOpen] = useState(false);
  const [disputeBody, setDisputeBody] = useState("");
  const [selectedReasonCodes, setSelectedReasonCodes] = useState<string[]>([]);
  const disputesQuery = useInfiniteQuery({
    queryKey: queryKeys.codeReviews.disputes(review?.session_id ?? ""),
    queryFn: ({ pageParam }) => api.codeReviews.disputes(review?.session_id ?? "", pageParam),
    enabled: Boolean(review.session_id),
    // Intake and reassessment states move on their own, so the timeline polls.
    // React Query refetches *every* loaded page on each interval, so stretch
    // the interval by the number of loaded pages: the request rate stays flat
    // as the admin pages into history, and the newest page — the one whose
    // state is still moving — keeps updating instead of going stale.
    refetchInterval: (query) => (5000 * Math.max(1, query.state.data?.pages.length ?? 1)),
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
  return <>
                <section className="space-y-3">
                  <div className="flex items-center justify-between gap-3">
                    <h4 className="font-medium">Decision feedback</h4>
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
  </>;
}
