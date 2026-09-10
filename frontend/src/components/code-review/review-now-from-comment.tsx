"use client";

import { useQuery } from "@tanstack/react-query";
import { parseAsString, useQueryState } from "nuqs";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { api } from "@/lib/api";
import { queryKeys } from "@/lib/query-keys";
import { ReviewNowButton } from "./scheduling";

const sessionIDPattern = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

/** A GitHub comment link opens a read-only confirmation, never an automatic POST. */
export function ReviewNowFromComment({ canManage }: { canManage: boolean }) {
  const [sessionID, setSessionID] = useQueryState("review_now", parseAsString);
  const validID = Boolean(sessionID && sessionIDPattern.test(sessionID));
  const review = useQuery({
    queryKey: ["code-review-link", sessionID],
    queryFn: () => api.codeReviews.get(sessionID!),
    enabled: validID,
    retry: false,
  });
  const policy = useQuery({
    queryKey: queryKeys.codeReviews.policy,
    queryFn: () => api.codeReviews.getPolicy(),
    enabled: Boolean(sessionID),
  });
  const close = () => { void setSessionID(null); };
  const target = review.data?.data;
  const available = policy.data?.data.capabilities?.scheduling === true;
  const ready = validID && target && available && canManage && !review.isError && !policy.isError;

  return (
    <Dialog open={Boolean(sessionID)} onOpenChange={(open) => { if (!open) close(); }}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Review now</DialogTitle>
          <DialogDescription>
            Request review of the latest revision. This skips the automatic timing delays and can join or reuse existing work.
          </DialogDescription>
        </DialogHeader>
        {!validID ? <p role="alert">This review link is invalid.</p> :
          review.isPending ? <p role="status">Loading pull request…</p> :
          review.isError ? <p role="alert">This review could not be loaded. Check that you have the correct organization selected and access to this PR.</p> :
          target ? <p className="text-sm font-medium">{target.github_repo}#{target.github_pr_number}: {target.pull_request_title}</p> : null}
        {policy.isError ? <p role="alert">Review settings could not be loaded.</p> :
          policy.isSuccess && !available ? <p role="status">Review scheduling is not enabled for this installation.</p> : null}
        {!canManage ? <p className="text-sm text-muted-foreground">An organization member or admin must request the review.</p> : null}
        <DialogFooter>
          <Button variant="outline" onClick={close}>Close</Button>
          {ready ? <ReviewNowButton key={target.pull_request_id} prID={target.pull_request_id} /> : null}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
