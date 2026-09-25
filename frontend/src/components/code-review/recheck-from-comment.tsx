"use client";

import { useQuery } from "@tanstack/react-query";
import { parseAsString, useQueryState } from "nuqs";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { api } from "@/lib/api";
import { RecheckActions } from "./recheck-actions";
import { AssessmentStatus } from "./assessment-status";

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
  const close = () => { void setAssessmentID(null); };
  const target = assessment.data?.data;
  return <Dialog open={Boolean(assessmentID)} onOpenChange={(open) => { if (!open) close(); }}>
    <DialogContent>
      <DialogHeader><DialogTitle>Re-check PR evidence</DialogTitle><DialogDescription>Check updated evidence using the previous code review. Changes to code or review policy require a full review.</DialogDescription></DialogHeader>
      {!validID ? <p role="alert">This re-check link is invalid.</p> : assessment.isPending ? <p role="status">Loading pull request…</p> : assessment.isError ? <p role="alert">This assessment could not be loaded. Check that you have the correct organization selected and access to this PR.</p> : target ? <p className="text-sm font-medium">{target.github_repo && target.github_pr_number ? `${target.github_repo}#${target.github_pr_number}: ` : ""}{target.pull_request_title ?? "Pull request"}</p> : null}
      {target ? <AssessmentStatus assessment={target} /> : null}
      {!canManage ? <p className="text-sm text-muted-foreground">An organization member or admin must request the review.</p> : null}
      <DialogFooter><Button variant="outline" onClick={close}>Close</Button>{target && canManage && target.status === "completed" ? <RecheckActions prID={target.pull_request_id} canManage completed /> : null}</DialogFooter>
    </DialogContent>
  </Dialog>;
}
