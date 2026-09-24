"use client";

import { useState } from "react";
import Link from "next/link";
import Image from "next/image";
import { useQuery } from "@tanstack/react-query";
import { parseAsString, useQueryState } from "nuqs";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { api } from "@/lib/api";
import type { CodeReviewAssessmentSummary } from "@/lib/types";

function assessmentLabel(assessment: CodeReviewAssessmentSummary): string {
  if (assessment.status === "reserved" || assessment.status === "running" || assessment.status === "publishing") return "Review in progress";
  if (assessment.status === "failed") return "Latest attempt failed";
  if (assessment.status === "superseded") return "Latest attempt superseded";
  if (assessment.status === "cancelled") return "Review cancelled";
  return assessment.review_scope === "evidence_only" ? "Visual evidence re-checked" : "Full review completed";
}

export function AssessmentStatus({ assessment }: { assessment: CodeReviewAssessmentSummary | null | undefined }) {
  const [open, setOpen] = useState(false);
  if (!assessment) return null;
  return <>
    <Button variant="ghost" size="sm" className="h-auto px-1 text-xs" onClick={() => setOpen(true)}>{assessmentLabel(assessment)}</Button>
    <AssessmentDialog assessmentID={assessment.id} summary={assessment} open={open} onOpenChange={setOpen} />
  </>;
}

const uuidPattern = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

export function AssessmentFromLink() {
  const [id, setID] = useQueryState("assessment", parseAsString);
  if (!id) return null;
  return <AssessmentDialog assessmentID={id} open onOpenChange={(open) => { if (!open) void setID(null); }} />;
}

function AssessmentDialog({ assessmentID, summary, open, onOpenChange }: { assessmentID: string; summary?: CodeReviewAssessmentSummary; open: boolean; onOpenChange: (open: boolean) => void }) {
  const validID = uuidPattern.test(assessmentID);
  const detail = useQuery({ queryKey: ["code-reviews", "assessment", assessmentID], queryFn: () => api.codeReviews.assessment(assessmentID), enabled: open && validID, retry: false });
  const evidence = useQuery({ queryKey: ["code-reviews", "assessment", assessmentID, "evidence"], queryFn: () => api.codeReviews.assessmentEvidence(assessmentID), enabled: open && validID, retry: false });
  const assessment = detail.data?.data ?? summary;
  const value = assessment;
  return <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[85vh] overflow-y-auto">
        <DialogHeader><DialogTitle>Review assessment</DialogTitle><DialogDescription>{value ? `${assessmentLabel(value)} for commit ${value.head_sha.slice(0, 7)}.` : "Recorded review outcome and evidence."}</DialogDescription></DialogHeader>
        {!validID ? <p role="alert" className="text-sm text-destructive">This assessment link is invalid.</p> : null}
        {detail.isError || evidence.isError ? <p role="alert" className="text-sm text-destructive">Assessment details could not be loaded.</p> : null}
        {validID && (detail.isPending || evidence.isPending) ? <p role="status" className="text-sm text-muted-foreground">Loading assessment…</p> : null}
        {detail.data && value ? <div className="space-y-3 text-sm">
          <div className="flex flex-wrap gap-2"><Badge variant="outline">{value.review_scope === "evidence_only" ? "Visual evidence" : "Full review"}</Badge><Badge variant="secondary">{value.status}</Badge>{value.decision ? <Badge variant="outline">{value.decision.replaceAll("_", " ")}</Badge> : null}</div>
          {value.status === "failed" || value.status === "superseded" ? <p>This attempt did not replace the completed assessment. Open that assessment separately for its recorded result.</p> : null}
          {value.review_scope === "evidence_only" ? <p>Reviewer findings came from the completed full assessment linked below. This assessment checked the updated visual evidence and recorded a new decision.</p> : null}
          {value.source_assessment_id ? <p className="text-muted-foreground">Source full assessment: <span className="font-mono">{value.source_assessment_id}</span></p> : null}
          {value.session_id ? <Button variant="link" size="sm" className="h-auto p-0" asChild><Link href={`/sessions/${value.session_id}`}>Open review session</Link></Button> : null}
          {evidence.data ? <p className="text-muted-foreground">{evidence.data.data.agent_results.length} reviewer result(s), {evidence.data.data.findings.length} finding(s), {evidence.data.data.visual_evidence?.evidence.length ?? 0} captured image(s).</p> : null}
          {evidence.data?.data.cited_visual_evidence_ids?.length ? <p className="text-muted-foreground">Cited images: {evidence.data.data.cited_visual_evidence_ids.join(", ")}</p> : null}
          {evidence.data?.data.visual_evidence?.evidence.map((item) => <Card key={item.evidence_id}><CardContent className="flex gap-3 p-3">
            {item.status === "available" && item.stored_url ? <Image src={item.stored_url} alt={`Captured evidence ${item.evidence_id}`} width={96} height={96} unoptimized className="size-24 shrink-0 rounded-md object-contain" /> : null}
            <div className="min-w-0 space-y-1"><div className="flex flex-wrap gap-2"><Badge variant="outline">{item.evidence_id}</Badge><Badge variant="secondary">{item.status}</Badge>{evidence.data.data.cited_visual_evidence_ids?.includes(item.evidence_id) ? <Badge>Cited</Badge> : null}</div>
              <p className="text-xs text-muted-foreground">{(item.source?.surface ?? "Unknown source").replaceAll("_", " ")} · untrusted PR content</p>
              {item.source?.source_url ? <Button variant="link" size="sm" className="h-auto p-0" asChild><a href={item.source.source_url} target="_blank" rel="noreferrer">Open source</a></Button> : null}
            </div>
          </CardContent></Card>)}
          {detail.data.data.failure_detail ? <p role="alert" className="text-destructive">{detail.data.data.failure_detail}</p> : null}
        </div> : null}
      </DialogContent>
    </Dialog>;
}
