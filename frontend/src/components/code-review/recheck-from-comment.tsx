"use client";

import { useQuery } from "@tanstack/react-query";
import { parseAsString, useQueryState } from "nuqs";
import { api } from "@/lib/api";
import { queryKeys } from "@/lib/query-keys";
import { ReviewRequestDialog } from "./review-request-dialog";

const uuidPattern = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

/** A GitHub link opens a read-only confirmation; only the button sends a request. */
export function RecheckFromComment({ canManage }: { canManage: boolean }) {
  const [assessmentID, setAssessmentID] = useQueryState("recheck", parseAsString);
  const validID = Boolean(assessmentID && uuidPattern.test(assessmentID));
  const assessment = useQuery({
    queryKey: ["code-review-assessment-link", assessmentID],
    queryFn: () => api.codeReviews.assessment(assessmentID!),
    enabled: validID,
    retry: false,
  });
  const policy = useQuery({
    queryKey: queryKeys.codeReviews.policy,
    queryFn: () => api.codeReviews.getPolicy(),
    enabled: Boolean(assessmentID) && canManage,
  });
  const close = () => { void setAssessmentID(null); };
  const target = assessment.data?.data;
  const error = !validID ? "This re-check link is invalid."
    : assessment.isError ? "This assessment could not be loaded. Check that you have the correct organization selected and access to this PR."
    : policy.isError ? "Review settings could not be loaded." : undefined;
  const unavailable = !canManage ? "An organization member or admin must request the review."
    : target?.status !== "completed" ? "Evidence can be re-checked after the review is completed."
    : !policy.data?.data.capabilities?.conditional_recheck ? "Evidence re-checks are not enabled for this installation."
    : !policy.data?.data.config.continuation_policy?.enabled ? "An administrator must enable review continuation." : undefined;

  return <ReviewRequestDialog
    key={assessmentID}
    open={Boolean(assessmentID)}
    onClose={close}
    mode="recheck"
    target={target}
    loading={validID && (assessment.isPending || (canManage && policy.isPending))}
    error={error}
    unavailable={unavailable}
    assessmentID={validID ? assessmentID! : undefined}
  />;
}
