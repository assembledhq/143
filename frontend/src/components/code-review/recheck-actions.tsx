"use client";

import { useRef, useState } from "react";
import Link from "next/link";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { MoreHorizontal } from "lucide-react";
import { Button } from "@/components/ui/button";
import { DisabledTooltip } from "@/components/ui/disabled-tooltip";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from "@/components/ui/dropdown-menu";
import { Label } from "@/components/ui/label";
import { Textarea } from "@/components/ui/textarea";
import { api, ApiError } from "@/lib/api";
import { queryKeys } from "@/lib/query-keys";
import type { CodeReviewRequestResponse } from "@/lib/types";

function requestResultLabel(result: CodeReviewRequestResponse, mode: "recheck" | "force_fresh" | undefined): string {
  switch (result.disposition) {
    case "reused": return "Existing assessment reused for captured inputs.";
    case "joined": return "Joined the review already in progress.";
    case "cancelled": return "The request was not scheduled.";
    case "queued": return mode === "force_fresh" ? "Full review requested." : "Re-check requested. Changes to code or review policy require a full review.";
  }
}

export function RecheckActions({ prID, canManage, completed, disabledReason, presentation = "inline", sessionID, onViewAssessment }: { prID: string; canManage: boolean; completed: boolean; disabledReason?: string; presentation?: "inline" | "menu" | "footer"; sessionID?: string; onViewAssessment?: () => void }) {
  const client = useQueryClient();
  const policy = useQuery({ queryKey: queryKeys.codeReviews.policy, queryFn: () => api.codeReviews.getPolicy(), enabled: canManage && completed });
  const [forceOpen, setForceOpen] = useState(false);
  const [reason, setReason] = useState("");
  const requestIDs = useRef<{ recheck?: string; force_fresh?: { id: string; reason: string } }>({});
  const mutation = useMutation({
    mutationFn: ({ mode, reason: requestReason }: { mode: "recheck" | "force_fresh"; reason?: string }) => {
      const normalizedReason = requestReason?.trim() ?? "";
      if (mode === "force_fresh") {
        if (requestIDs.current.force_fresh?.reason !== normalizedReason) requestIDs.current.force_fresh = { id: crypto.randomUUID(), reason: normalizedReason };
      } else {
        requestIDs.current.recheck ??= crypto.randomUUID();
      }
      const requestID = mode === "force_fresh" ? requestIDs.current.force_fresh!.id : requestIDs.current.recheck!;
      return api.codeReviews.requestReview(prID, { request_id: requestID, mode, ...(normalizedReason ? { reason: normalizedReason } : {}) });
    },
    onSuccess: (_response, variables) => {
      requestIDs.current[variables.mode] = undefined;
      if (variables.mode === "force_fresh") { setForceOpen(false); setReason(""); }
      void client.invalidateQueries({ queryKey: ["code-reviews"] });
      void client.invalidateQueries({ queryKey: ["code-review-schedules"] });
    },
    onError: (error, variables) => {
      if (error instanceof ApiError && error.code === "CODE_REVIEW_RECHECK_UNAVAILABLE") {
        requestIDs.current[variables.mode] = undefined;
        void client.invalidateQueries({ queryKey: ["code-review-schedules"] });
      }
    },
  });
  const capability = policy.data?.data.capabilities?.conditional_recheck === true;
  const enabled = policy.data?.data.config.continuation_policy?.enabled === true;
  const showActions = canManage && completed && (policy.isPending || policy.isError || capability);
  if (!showActions && presentation !== "footer") return null;
  const unavailable = disabledReason ?? (mutation.isPending ? "Your request is being recorded." : policy.isError ? "Review settings could not be loaded." : policy.isPending ? "Loading review settings." : !enabled ? "An administrator must enable review continuation." : undefined);
  const result = mutation.data?.data as CodeReviewRequestResponse | undefined;
  if (presentation === "footer") return <div className="space-y-3">
    {result ? <p role="status" className="text-xs text-muted-foreground">{requestResultLabel(result, mutation.variables?.mode)} {result.assessment_id ? <Link className="text-primary hover:underline" href={`/code-reviews?assessment=${result.assessment_id}`} onClick={onViewAssessment}>View assessment</Link> : null}</p> : null}
    {mutation.isError ? <p role="alert" className="text-xs text-destructive">{mutation.error.message}</p> : null}
    {showActions && forceOpen ? <div className="space-y-2">
      <Label htmlFor={`force-reason-${prID}`}>Reason for a fresh review</Label>
      <Textarea id={`force-reason-${prID}`} value={reason} maxLength={2000} onChange={(event) => setReason(event.target.value)} rows={3} className="max-h-32 resize-none" placeholder="What should the new full review revisit?" />
    </div> : null}
    <div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
      {sessionID ? <Button variant="link" size="sm" className="mr-auto px-0" asChild><Link href={`/sessions/${sessionID}`}>Open session</Link></Button> : null}
      {showActions ? <div className="flex flex-wrap items-center justify-end gap-2">{forceOpen ? <>
        <Button variant="outline" size="sm" onClick={() => setForceOpen(false)}>Cancel</Button>
        <DisabledTooltip disabled={!reason.trim() || Boolean(unavailable)} content={!reason.trim() ? "Enter a reason to request a fresh review." : unavailable}>
          <Button size="sm" disabled={!reason.trim() || Boolean(unavailable)} onClick={() => mutation.mutate({ mode: "force_fresh", reason: reason.trim() })}>Request full review</Button>
        </DisabledTooltip>
      </> : <>
        <DisabledTooltip disabled={Boolean(unavailable)} content={unavailable}>
          <Button size="sm" variant="outline" disabled={Boolean(unavailable)} onClick={() => setForceOpen(true)}>Force fresh review</Button>
        </DisabledTooltip>
        <DisabledTooltip disabled={Boolean(unavailable)} content={unavailable}>
          <Button size="sm" disabled={Boolean(unavailable)} onClick={() => mutation.mutate({ mode: "recheck" })}>Re-check evidence</Button>
        </DisabledTooltip>
      </>}</div> : null}
    </div>
    {showActions ? <p className="text-xs text-muted-foreground">{forceOpen ? "Runs a new full review of the current pull request." : "Re-checks the current pull request. Changes to code or policy may require a full review."}</p> : null}
  </div>;
  return <div className="flex items-center gap-1">
    {result ? <span role="status" className="text-xs text-muted-foreground">{requestResultLabel(result, mutation.variables?.mode)} {result.assessment_id ? <Button variant="link" size="sm" className="h-auto p-0 text-xs" asChild><Link href={`/code-reviews?assessment=${result.assessment_id}`}>View assessment</Link></Button> : null}</span> : null}
    {mutation.isError ? <span role="alert" className="text-xs text-destructive">{mutation.error.message}</span> : null}
    {presentation === "inline" ? <DisabledTooltip disabled={Boolean(unavailable)} content={unavailable}>
      <Button size="sm" variant="outline" disabled={Boolean(unavailable)} onClick={() => mutation.mutate({ mode: "recheck" })}>Re-check PR evidence</Button>
    </DisabledTooltip> : null}
    <DropdownMenu>
      <DisabledTooltip disabled={Boolean(unavailable)} content={unavailable}>
        <DropdownMenuTrigger asChild><Button size="icon-sm" variant="ghost" aria-label="More review actions" title={unavailable ? undefined : "More review actions"} disabled={Boolean(unavailable)}><MoreHorizontal className="size-4" /></Button></DropdownMenuTrigger>
      </DisabledTooltip>
      <DropdownMenuContent align="end">
        {presentation === "menu" ? <DropdownMenuItem disabled={Boolean(unavailable)} onSelect={() => mutation.mutate({ mode: "recheck" })}>Re-check PR evidence</DropdownMenuItem> : null}
        <DropdownMenuItem disabled={Boolean(unavailable)} onSelect={() => setForceOpen(true)}>Force fresh review</DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenu>
    <Dialog open={forceOpen} onOpenChange={setForceOpen}>
      <DialogContent>
        <DialogHeader><DialogTitle>Force fresh review</DialogTitle><DialogDescription>Run a new full review of this pull request. Give a reason for the new review.</DialogDescription></DialogHeader>
        <div className="space-y-2"><Label htmlFor={`force-reason-${prID}`}>Reason</Label><Textarea id={`force-reason-${prID}`} value={reason} maxLength={2000} onChange={(event) => setReason(event.target.value)} /></div>
        <DialogFooter><Button variant="outline" onClick={() => setForceOpen(false)}>Cancel</Button><DisabledTooltip disabled={!reason.trim() || Boolean(unavailable)} content={!reason.trim() ? "Enter a reason to request a fresh review." : unavailable}><Button disabled={!reason.trim() || Boolean(unavailable)} onClick={() => mutation.mutate({ mode: "force_fresh", reason: reason.trim() })}>Request full review</Button></DisabledTooltip></DialogFooter>
      </DialogContent>
    </Dialog>
  </div>;
}
