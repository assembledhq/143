"use client";

import { useQuery } from "@tanstack/react-query";
import { parseAsString, useQueryState } from "nuqs";
import { api } from "@/lib/api";
import { queryKeys } from "@/lib/query-keys";
import { ReviewRequestDialog } from "./review-request-dialog";

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
  const error = !validID ? "This review link is invalid."
    : review.isError ? "This review could not be loaded. Check that you have the correct organization selected and access to this PR."
    : policy.isError ? "Review settings could not be loaded." : undefined;
  const unavailable = !canManage ? "An organization member or admin must request the review."
    : !available ? "Review scheduling is not enabled for this installation." : undefined;

  return <ReviewRequestDialog
    key={sessionID}
    open={Boolean(sessionID)}
    onClose={close}
    mode="review_now"
    target={target}
    loading={validID && (review.isPending || policy.isPending)}
    error={error}
    unavailable={unavailable}
  />;
}
