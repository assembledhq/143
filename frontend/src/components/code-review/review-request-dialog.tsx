"use client";

import { useRef } from "react";
import Link from "next/link";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { StatusLabel } from "@/components/status-label";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { DisabledTooltip } from "@/components/ui/disabled-tooltip";
import { ErrorNotice } from "@/components/ui/error-notice";
import { api, ApiError } from "@/lib/api";
import type { CodeReviewRequestResponse } from "@/lib/types";

type RequestMode = "review_now" | "recheck";
interface ReviewRequestTarget {
  pull_request_id: string;
  github_repo?: string;
  github_pr_number?: number;
  pull_request_title?: string | null;
}
interface ReviewRequestDialogProps {
  open: boolean;
  onClose: () => void;
  mode: RequestMode;
  target?: ReviewRequestTarget;
  loading: boolean;
  error?: string;
  unavailable?: string;
  assessmentID?: string;
}

function resultCopy(result: CodeReviewRequestResponse, mode: RequestMode) {
  switch (result.disposition) {
    case "queued": return {
      title: mode === "recheck" ? "Evidence re-check requested" : "Full review requested",
      description: mode === "recheck"
        ? "Your request is queued. If the code or review policy has changed, a full review may be needed."
        : "Your request is queued. Follow its progress in the review queue.",
      tone: "success" as const,
      status: "Queued",
    };
    case "joined": return { title: "Review already in progress", description: "Your request joined the review already covering these changes.", tone: "info" as const, status: "In progress" };
    case "reused": return { title: "Existing review reused", description: "An existing assessment already covers these inputs. No new review was started.", tone: "success" as const, status: "Reused" };
    case "cancelled": return { title: "Review not scheduled", description: "This request was cancelled. Check the review queue for the current status.", tone: "warning" as const, status: "Cancelled" };
  }
}

/** Shared confirmation and receipt for the read-only GitHub action links. */
export function ReviewRequestDialog({ open, onClose, mode, target, loading, error, unavailable, assessmentID }: ReviewRequestDialogProps) {
  const client = useQueryClient();
  const requestID = useRef<string | null>(null);
  const mutation = useMutation({
    mutationFn: () => {
      if (!target) throw new Error("The pull request is not available.");
      requestID.current ??= crypto.randomUUID();
      return api.codeReviews.requestReview(target.pull_request_id, { request_id: requestID.current, mode });
    },
    onSuccess: () => {
      void client.invalidateQueries({ queryKey: ["code-reviews"] });
      void client.invalidateQueries({ queryKey: ["code-review-schedules"] });
    },
    onError: (requestError) => {
      // A rejected recheck has a durable cancelled receipt. A new attempt needs
      // a new ID; uncertain failures keep the ID so retries cannot duplicate work.
      if (requestError instanceof ApiError && requestError.code === "CODE_REVIEW_RECHECK_UNAVAILABLE") {
        requestID.current = null;
        void client.invalidateQueries({ queryKey: ["code-review-schedules"] });
      }
    },
  });
  const result = mutation.data?.data;
  const receipt = result ? resultCopy(result, mode) : undefined;
  const isRecheck = mode === "recheck";
  const title = isRecheck ? "Re-check evidence" : "Request full review";
  const disabledReason = mutation.isPending ? "Your request is being recorded." : loading ? "Loading review settings and pull request." : error ?? unavailable;
  const canRequest = Boolean(target) && !loading && !error && !unavailable;
  const resultHref = result?.assessment_id ? `/code-reviews?assessment=${result.assessment_id}`
    : result?.session_id ? `/sessions/${result.session_id}` : "/code-reviews?tab=queue";
  const resultLinkLabel = result?.assessment_id ? "View assessment" : result?.session_id ? "Open session" : "View review queue";

  return <Dialog open={open} onOpenChange={(nextOpen) => { if (!nextOpen) onClose(); }}>
    <DialogContent className="flex max-h-[85dvh] w-[calc(100%-2rem)] flex-col gap-0 overflow-hidden p-0 sm:max-w-lg">
      <DialogHeader className="shrink-0 px-6 py-5 pr-12 text-left">
        <DialogTitle>{receipt?.title ?? title}</DialogTitle>
        <DialogDescription className={receipt ? "sr-only" : undefined}>{receipt ? "Review request status." : isRecheck ? "Check updated evidence against the previous review." : "Review the latest pushed changes without waiting for automatic timers."}</DialogDescription>
      </DialogHeader>
      <div className="min-h-0 space-y-4 overflow-y-auto border-t px-6 py-5">
        {target ? <div className="min-w-0 space-y-1">
          {target.github_repo ? <p className="break-words text-xs text-muted-foreground">{target.github_repo}{target.github_pr_number ? ` #${target.github_pr_number}` : ""}</p> : null}
          <p className="break-words text-sm font-medium">{target.pull_request_title || "Pull request"}</p>
        </div> : null}
        {receipt ? <div role="status" className="space-y-2">
          <StatusLabel label={receipt.status} tone={receipt.tone} />
          <p className="text-sm text-muted-foreground">{receipt.description}</p>
        </div> : <>
          {loading ? <p role="status" className="text-sm text-muted-foreground">Loading pull request and review settings…</p> : null}
          {error ? <ErrorNotice title="Unable to load review" description={error} /> : null}
          {target && !loading && !error ? <p className="text-sm text-muted-foreground">{isRecheck
            ? "Previous findings stay in place. Changes to code or review policy require a full review."
            : "A running or completed review may be reused if it already covers these changes."}</p> : null}
          {unavailable && !loading && !error ? <p role="status" className="text-sm text-muted-foreground">{unavailable}</p> : null}
          {assessmentID && target ? <Button variant="link" size="sm" className="h-auto p-0" asChild><Link href={`/code-reviews?assessment=${assessmentID}`}>View previous assessment</Link></Button> : null}
          {mutation.isError ? <ErrorNotice title="Request could not be confirmed" description={mutation.error.message} /> : null}
        </>}
      </div>
      <DialogFooter className="shrink-0 flex-row items-center justify-end border-t px-6 py-4">
        {receipt ? <>
          <Button variant="outline" asChild><Link href={resultHref}>{resultLinkLabel}</Link></Button>
          <Button onClick={onClose}>Done</Button>
        </> : <>
          <Button variant="outline" onClick={onClose}>{canRequest ? "Cancel" : "Close"}</Button>
          {target && !error ? <DisabledTooltip disabled={Boolean(disabledReason)} content={disabledReason}>
            <Button disabled={Boolean(disabledReason)} onClick={() => mutation.mutate()}>{mutation.isPending ? "Requesting…" : mutation.isError ? "Try again" : title}</Button>
          </DisabledTooltip> : null}
        </>}
      </DialogFooter>
    </DialogContent>
  </Dialog>;
}
