"use client";

import { useState, type ReactNode } from "react";
import Link from "next/link";
import Image from "next/image";
import { ChevronDown } from "lucide-react";
import { useQuery } from "@tanstack/react-query";
import { parseAsString, useQueryState } from "nuqs";
import { StatusLabel, type StatusTone } from "@/components/status-label";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "@/components/ui/collapsible";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { api } from "@/lib/api";
import { useAuth } from "@/hooks/use-auth";
import { queryKeys } from "@/lib/query-keys";
import { ErrorNotice } from "@/components/ui/error-notice";
import { ExternalLink } from "@/components/ui/external-link";
import { DecisionFeedback } from "./decision-feedback";
import { RecheckActions } from "./recheck-actions";
import { codeReviewReasonDescription } from "@/lib/code-review-reasons";
import { activeAssessmentState } from "@/lib/code-review-assessment-state";
import type { CodeReviewAssessmentSummary, CodeReviewDecision, CodeReviewEvidenceCitation, CodeReviewEvidence, CodeReviewListItem } from "@/lib/types";

function assessmentLabel(assessment: CodeReviewAssessmentSummary): string {
  const active = activeAssessmentState(assessment);
  if (active) return active.label;
  if (assessment.status === "failed") return "Latest attempt failed";
  if (assessment.status === "superseded") return "Latest attempt superseded";
  if (assessment.status === "cancelled") return "Review cancelled";
  return assessment.review_scope === "evidence_only" ? "Evidence re-check completed" : "Full review completed";
}

function sentenceCase(value: string): string {
  const label = value.replaceAll("_", " ");
  return label.charAt(0).toUpperCase() + label.slice(1);
}

const decisionPresentation: Record<CodeReviewDecision, { label: string; tone: StatusTone }> = {
  approved: { label: "Approved", tone: "success" },
  needs_human_review: { label: "Needs human review", tone: "warning" },
  blocked: { label: "Blocked", tone: "destructive" },
  comment_only: { label: "Comment only", tone: "neutral" },
};

function assessmentVerdict(assessment: CodeReviewAssessmentSummary): { label: string; tone: StatusTone } {
  if (assessment.status !== "completed") {
    return { label: assessmentLabel(assessment), tone: activeAssessmentState(assessment)?.tone ?? (assessment.status === "failed" ? "destructive" : "neutral") };
  }
  return assessment.decision ? decisionPresentation[assessment.decision] : { label: "No verdict recorded", tone: "neutral" };
}

function AssessmentSection({ title, count, children }: { title: string; count?: number; children: ReactNode }) {
  return (
    <Collapsible className="min-w-0 border-t border-border">
      <h3>
        <CollapsibleTrigger asChild>
          <Button variant="ghost" className="group h-auto w-full sm:h-auto justify-between gap-3 rounded-none px-0 py-3 text-left whitespace-normal hover:bg-transparent">
            <span className="flex min-w-0 items-center gap-2">
              {title}{" "}
              {count !== undefined ? <Badge variant="secondary" className="shrink-0 tabular-nums">{count}</Badge> : null}
            </span>
            <span><ChevronDown aria-hidden="true" className="size-4 shrink-0 text-muted-foreground transition-transform group-data-[state=open]:rotate-180" /></span>
          </Button>
        </CollapsibleTrigger>
      </h3>
      <CollapsibleContent>
        <div className="min-w-0 space-y-4 pb-4">{children}</div>
      </CollapsibleContent>
    </Collapsible>
  );
}

function EvidenceCitations({ citations }: { citations: CodeReviewEvidenceCitation[] | null | undefined }) {
  if (!citations?.length) return <p className="text-xs text-muted-foreground">No evidence citations recorded.</p>;
  return <div className="space-y-1 text-xs text-muted-foreground">{citations.map((citation) => <p key={citation.evidence_id}><span className="font-mono">{citation.evidence_id}</span>{citation.quote ? <>: “{citation.quote}”</> : null}</p>)}</div>;
}

function safeSourceURL(value: string): string | null {
  try {
    const parsed = new URL(value);
    return parsed.protocol === "https:" ? parsed.toString() : null;
  } catch {
    return null;
  }
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
const visibleReasonLimit = 3;

export function AssessmentFromLink() {
  const [id, setID] = useQueryState("assessment", parseAsString);
  if (!id) return null;
  return <AssessmentDialog key={id} assessmentID={id} open onOpenChange={(open) => { if (!open) void setID(null); }} />;
}

export function assessmentForReview(review: CodeReviewListItem) {
  // A PR can have several review sessions and commits. Never borrow another
  // row's assessment just because it is currently attached to the PR.
  const matches = (assessment: CodeReviewAssessmentSummary | null | undefined) => assessment?.session_id === review.session_id && assessment.head_sha === review.head_sha;
  if (matches(review.active_assessment)) return review.active_assessment;
  if (matches(review.current_assessment)) return review.current_assessment;
  return null;
}

type ReviewDialogProps = {
  review: CodeReviewListItem;
  evidence?: CodeReviewEvidence;
  isLoading: boolean;
  error: Error | null;
  onRetryEvidence: () => void;
  operationalStatus?: ReactNode;
  retryAction?: ReactNode;
  open: boolean;
  onOpenChange: (open: boolean) => void;
};

export function ReviewAssessmentDialog(props: ReviewDialogProps) {
  const assessment = assessmentForReview(props.review);
  return <AssessmentDialog key={assessment?.id ?? props.review.session_id} {...props} summary={assessment ?? undefined} assessmentID={assessment?.id} />;
}

function legacyAssessment(review: CodeReviewListItem): CodeReviewAssessmentSummary {
  return {
    id: review.id,
    status: review.stale || review.status === "stale" || review.superseded_by_session_id ? "superseded" : review.status === "queued" ? "reserved" : review.status,
    review_scope: "full",
    route_reason: "legacy",
    session_id: review.session_id,
    head_sha: review.head_sha,
    decision: review.decision,
    risk_reason_details: review.risk_reason_details,
  };
}

function formatEvidenceJSON(value: unknown): string {
  return typeof value === "string" ? value : JSON.stringify(value, null, 2);
}

function AssessmentDialog({ assessmentID, summary, open, onOpenChange, review, evidence: legacyEvidence, isLoading, error, onRetryEvidence, operationalStatus, retryAction }: {
  assessmentID?: string;
  summary?: CodeReviewAssessmentSummary;
  open: boolean;
  onOpenChange: (open: boolean) => void;
} & Partial<Omit<ReviewDialogProps, "open" | "onOpenChange">>) {
  const { user } = useAuth();
  const canManage = user?.role === "admin" || user?.role === "member";
  const validID = assessmentID ? uuidPattern.test(assessmentID) : Boolean(review);
  const detail = useQuery({ queryKey: ["code-reviews", "assessment", assessmentID], queryFn: () => api.codeReviews.assessment(assessmentID!), enabled: open && validID && Boolean(assessmentID), retry: false });
  const evidence = useQuery({ queryKey: ["code-reviews", "assessment", assessmentID, "evidence"], queryFn: () => api.codeReviews.assessmentEvidence(assessmentID!), enabled: open && validID && Boolean(assessmentID), retry: false });
  const value = detail.data?.data ?? summary ?? (review ? legacyAssessment(review) : undefined);
  const sessionID = value?.session_id;
  const reviewQuery = useQuery({
    queryKey: queryKeys.codeReviews.detail(sessionID ?? ""),
    queryFn: () => api.codeReviews.get(sessionID!),
    enabled: open && validID && Boolean(sessionID) && !review,
    retry: false,
  });
  const sessionReview = review ?? reviewQuery.data?.data;
  // Assessment snapshots are authoritative. Session evidence is only the
  // fallback for legacy rows that have no assessment record.
  const evidenceDetail = assessmentID ? evidence.data?.data : legacyEvidence;
  const assessmentEvidence = assessmentID ? evidence.data?.data : undefined;
  const evidenceError = assessmentID ? evidence.error : error;
  const loadingEvidence = assessmentID ? evidence.isPending : isLoading;
  const sourceFindings = evidenceDetail?.source_findings ?? evidenceDetail?.findings ?? [];
  const reassessments = new Map(evidenceDetail?.finding_reassessments?.map((item) => [item.finding_id, item]) ?? []);
  const requirements = assessmentEvidence?.requirement_reassessments ?? [];
  const textSources = assessmentEvidence?.text_evidence?.items ?? [];
  const images = evidenceDetail?.visual_evidence?.evidence ?? [];
  const reasons = [...new Set(value?.risk_reason_details?.map(codeReviewReasonDescription) ?? [])];
  const verdict = !assessmentID && value?.status === "completed" && value.decision === "approved" && !review?.github_review_id
    ? { label: "Approval not posted", tone: "neutral" as const }
    : value ? assessmentVerdict(value) : null;
  const agentResults = evidenceDetail?.agent_results ?? [];
  const promptRecords = evidenceDetail?.prompt_records ?? (!assessmentID ? legacyEvidence?.prompt_artifacts : undefined) ?? [];
  const omittedImageCount = evidenceDetail?.visual_evidence?.omitted_source_count ?? 0;
  const prID = detail.data?.data.pull_request_id ?? sessionReview?.pull_request_id;
  const failedAssessment = sessionReview?.latest_failed_assessment;
  const relatedFailedAssessment = failedAssessment && failedAssessment.id !== assessmentID && failedAssessment.session_id === sessionID && failedAssessment.head_sha === value?.head_sha ? failedAssessment : null;

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="flex max-h-[85dvh] w-[calc(100%-2rem)] flex-col gap-0 overflow-hidden p-0 sm:max-w-3xl">
        <DialogHeader className="shrink-0 border-b border-border px-6 py-5 pr-12 text-left">
          <DialogTitle>{sessionReview ? `Review for #${sessionReview.github_pr_number}` : "Review assessment"}</DialogTitle>
          <DialogDescription>
            {sessionReview ? sessionReview.pull_request_title : value ? `${assessmentLabel(value)} for commit ${value.head_sha.slice(0, 7)}.` : "Recorded review outcome and evidence."}
          </DialogDescription>
        </DialogHeader>
        <div className="min-h-0 min-w-0 overflow-y-auto overscroll-contain px-6 pt-4 pb-2 text-sm [overflow-wrap:anywhere]">
          {!validID ? <p role="alert" className="text-destructive">This assessment link is invalid.</p> : null}
          {detail.isError ? <p role="alert" className="mb-4 text-destructive">Assessment details could not be loaded.</p> : null}
          {evidenceError ? <ErrorNotice title="Evidence could not be loaded" description="Retry to load the supporting review details." action={{ label: "Retry", onClick: () => { if (assessmentID) void evidence.refetch(); else onRetryEvidence?.(); } }} /> : null}
          {validID && ((Boolean(assessmentID) && detail.isPending) || loadingEvidence) ? <p role="status" className="mb-4 text-muted-foreground">Loading assessment…</p> : null}
          {value && verdict && validID ? (
            <>
              <section aria-label="Assessment verdict" className="space-y-4 pb-4">
                <div className="space-y-2">
                  <p className="text-xs font-medium text-muted-foreground">Verdict</p>
                  <h2 className="text-lg font-semibold">
                    <StatusLabel size="md" label={verdict.label} tone={verdict.tone} className="text-lg [&>span]:font-semibold" />
                  </h2>
                </div>
                {reasons.length > 0 ? (
                  <div className="space-y-2">
                    <h3 className="font-medium">Why this verdict</h3>
                    <ul className="list-disc space-y-1.5 pl-5 text-muted-foreground">
                      {reasons.slice(0, visibleReasonLimit).map((reason) => <li key={reason}>{reason}</li>)}
                    </ul>
                    {reasons.length > visibleReasonLimit ? <p className="text-xs text-muted-foreground">{reasons.length - visibleReasonLimit} more {reasons.length - visibleReasonLimit === 1 ? "reason" : "reasons"} in Decision details below.</p> : null}
                  </div>
                ) : value.status === "completed" ? (
                  <p className="text-muted-foreground">No decision reasoning was recorded for this assessment.</p>
                ) : null}
                {value.status === "failed" || value.status === "superseded" ? <p className="text-muted-foreground">This attempt did not replace the completed assessment. Open that assessment separately for its recorded result.</p> : null}
                {detail.data?.data.failure_detail ? <p role="alert" className="text-destructive">{detail.data.data.failure_detail}</p> : null}
                {operationalStatus}
                {retryAction}
              </section>

              {reasons.length > visibleReasonLimit ? (
                <AssessmentSection title="Decision details" count={reasons.length}>
                  <ul className="list-disc space-y-2 pl-5 text-muted-foreground">
                    {reasons.map((reason) => <li key={reason}>{reason}</li>)}
                  </ul>
                </AssessmentSection>
              ) : null}

              {sourceFindings.length > 0 ? (
                <AssessmentSection title="Reviewer findings" count={sourceFindings.length}>
                  {value.review_scope === "evidence_only" ? <p className="text-muted-foreground">Original reviewer findings are preserved below, with current evidence reassessments where available.</p> : null}
                  <p className="text-xs text-muted-foreground">P0 and P1 findings block approval. P2 and P3 findings are advisory and are not posted as inline GitHub comments.</p>
                  <div className="divide-y divide-border">
                    {sourceFindings.map((finding) => {
                      const reassessment = reassessments.get(finding.id);
                      return (
                        <div key={finding.id} className="space-y-2 py-4 first:pt-0 last:pb-0">
                          <div className="flex flex-wrap items-start gap-2">
                            <h4 className="min-w-0 flex-1 font-medium">{finding.summary}</h4>
                            <Badge variant={finding.severity === "critical" || finding.severity === "high" ? "destructive" : "outline"}>{{ critical: "P0 · Blocking", high: "P1 · Blocking", medium: "P2 · Advisory", low: "P3 · Advisory", info: "P3 · Advisory" }[finding.severity]}</Badge>
                            {reassessment ? <Badge variant={reassessment.status === "resolved" ? "secondary" : "destructive"}>{sentenceCase(reassessment.status)}</Badge> : null}
                          </div>
                          {finding.path ? <p className="font-mono text-xs text-muted-foreground">{finding.path}{finding.start_line ? `:${finding.start_line}${finding.end_line && finding.end_line !== finding.start_line ? `–${finding.end_line}` : ""}` : ""}</p> : null}
                          <p className="whitespace-pre-wrap leading-relaxed text-muted-foreground">{finding.body}</p>
                          {reassessment ? <><p>{reassessment.reason}</p><EvidenceCitations citations={reassessment.evidence_citations} /></> : value.review_scope === "evidence_only" ? <p className="text-xs text-muted-foreground">No current reassessment recorded for this finding.</p> : null}
                        </div>
                      );
                    })}
                  </div>
                </AssessmentSection>
              ) : null}

              {requirements.length > 0 ? (
                <AssessmentSection title="Description requirements" count={requirements.length}>
                  <div className="divide-y divide-border">
                    {requirements.map((requirement) => (
                      <div key={requirement.key} className="space-y-2 py-4 first:pt-0 last:pb-0">
                        <div className="flex flex-wrap items-center gap-2">
                          <h4 className="font-medium">{sentenceCase(requirement.key)}</h4>
                          <Badge variant={requirement.status === "satisfied" ? "secondary" : "destructive"}>{sentenceCase(requirement.status)}</Badge>
                        </div>
                        <p className="text-muted-foreground">{requirement.reason}</p>
                        <EvidenceCitations citations={requirement.evidence_citations} />
                      </div>
                    ))}
                  </div>
                </AssessmentSection>
              ) : null}

              {textSources.length > 0 ? (
                <AssessmentSection title="Captured text sources" count={textSources.length}>
                  <div className="divide-y divide-border">
                    {textSources.map((item) => (
                      <div key={item.evidence_id} className="space-y-2 py-4 first:pt-0 last:pb-0">
                        <div className="flex flex-wrap items-center gap-2">
                          <Badge variant="outline" className="max-w-full whitespace-normal">{item.evidence_id}</Badge>
                          <span className="text-xs text-muted-foreground">{item.section || sentenceCase(item.surface)}</span>
                        </div>
                        <p className="text-xs text-muted-foreground">{item.author_login || "Unknown author"} · untrusted PR content</p>
                        <p className="max-h-48 overflow-auto whitespace-pre-wrap leading-relaxed">{item.content}</p>
                        {safeSourceURL(item.source_url) ? <Button variant="link" size="sm" className="h-auto p-0" asChild><a href={safeSourceURL(item.source_url) ?? undefined} target="_blank" rel="noreferrer">Open text source</a></Button> : null}
                      </div>
                    ))}
                  </div>
                </AssessmentSection>
              ) : null}

              {images.length + omittedImageCount > 0 || (evidenceDetail?.cited_visual_evidence_ids?.length ?? 0) > 0 ? (
                <AssessmentSection title="Visual evidence" count={images.length + omittedImageCount}>
                  {omittedImageCount > 0 ? <p className="text-xs text-muted-foreground">{omittedImageCount} additional {omittedImageCount === 1 ? "image was" : "images were"} omitted after the 32-image capture limit.</p> : null}
                  {evidenceDetail?.cited_visual_evidence_ids?.length ? <p className="text-muted-foreground">Cited images: {evidenceDetail.cited_visual_evidence_ids.join(", ")}</p> : null}
                  <div className="divide-y divide-border">
                    {images.map((item) => (
                      <div key={item.evidence_id} className="flex flex-col gap-3 py-4 first:pt-0 last:pb-0 sm:flex-row">
                        {item.status === "available" && item.stored_url ? <Image src={item.stored_url} alt={`Captured visual evidence ${item.evidence_id}`} width={96} height={96} unoptimized referrerPolicy="no-referrer" className="size-24 shrink-0 rounded-md object-contain" /> : null}
                        <div className="min-w-0 space-y-2">
                          <div className="flex flex-wrap gap-2">
                            <Badge variant="outline" className="max-w-full whitespace-normal">{item.evidence_id}</Badge>
                            <Badge variant="secondary">{sentenceCase(item.status)}</Badge>
                            {evidenceDetail?.cited_visual_evidence_ids?.includes(item.evidence_id) ? <Badge>Cited</Badge> : null}
                          </div>
                          <p className="text-xs text-muted-foreground">{item.source?.surface === "issue_comment" ? "PR comment" : item.source?.surface === "description" ? "PR description" : sentenceCase(item.source?.surface ?? "Unknown source")} · untrusted PR content</p>
                          {item.source?.author_login ? <p className="text-xs text-muted-foreground">@{item.source.author_login}</p> : null}
                          {item.source?.created_at || item.source?.updated_at ? <p className="text-xs text-muted-foreground">{new Date(item.source.created_at ?? item.source.updated_at!).toLocaleString()}</p> : null}
                          {item.width && item.height ? <p className="text-xs text-muted-foreground">{item.width}×{item.height}</p> : null}
                          {item.duplicate_of_evidence_id ? <p className="text-xs text-muted-foreground">Duplicate of {item.duplicate_of_evidence_id}</p> : null}
                          {item.failure_reason ? <p className="text-xs text-muted-foreground">{item.failure_reason}</p> : null}
                          {item.source?.source_url && safeSourceURL(item.source.source_url) ? <ExternalLink href={safeSourceURL(item.source.source_url)!}>View source</ExternalLink> : null}
                        </div>
                      </div>
                    ))}
                  </div>
                </AssessmentSection>
              ) : null}

              {agentResults.length > 0 ? <AssessmentSection title="Reviewer outputs" count={agentResults.length}>
                {agentResults.map((result) => <div key={result.id} className="space-y-2 border-b border-border pb-4 last:border-0 last:pb-0">
                  <div className="flex flex-wrap items-center gap-2"><h4 className="font-medium">{result.agent_provider}</h4><Badge variant="outline">{sentenceCase(result.status)}</Badge></div>
                  <p className="text-xs text-muted-foreground">{sentenceCase(result.role)}{result.agent_model ? ` · ${result.agent_model}` : ""}</p>
                  {result.raw_output ? <pre className="max-h-64 overflow-auto whitespace-pre-wrap font-mono text-xs leading-relaxed">{result.raw_output}</pre> : null}
                  {result.structured_result ? <pre className="max-h-64 overflow-auto whitespace-pre-wrap font-mono text-xs leading-relaxed">{formatEvidenceJSON(result.structured_result)}</pre> : null}
                </div>)}
              </AssessmentSection> : null}
              {promptRecords.length > 0 ? <AssessmentSection title="Prompt records" count={promptRecords.length}>
                {promptRecords.map((record) => <div key={record.id} className="space-y-2 border-b border-border pb-4 last:border-0 last:pb-0">
                  <h4 className="font-mono text-xs">{record.record_key ?? record.artifact_key}</h4>
                  <p className="text-xs text-muted-foreground">{sentenceCase(record.role)}{record.agent_provider ? ` · ${record.agent_provider}` : ""}</p>
                  <pre className="max-h-64 overflow-auto whitespace-pre-wrap font-mono text-xs leading-relaxed">{record.content}</pre>
                </div>)}
              </AssessmentSection> : null}
              {canManage && sessionReview?.status === "completed" && sessionReview.decision ? <AssessmentSection title="Decision feedback">
                <DecisionFeedback review={sessionReview} reasonCodes={!assessmentID ? legacyEvidence?.risk_reason_codes ?? [] : value.risk_reason_details?.map(reason => reason.code) ?? []} canManagePolicy={user?.role === "admin"} />
              </AssessmentSection> : null}
              <AssessmentSection title="Assessment details">
                <div className="flex flex-wrap gap-2">
                  <Badge variant="outline">{value.review_scope === "evidence_only" ? "Evidence re-check" : "Full review"}</Badge>
                  <Badge variant="secondary">{sentenceCase(value.status)}</Badge>
                </div>
                {value.review_scope === "evidence_only" ? <p className="text-muted-foreground">Reviewer findings remain attached to the completed full assessment. This assessment records how current evidence affects each finding and requirement.</p> : null}
                {evidenceDetail ? <p className="text-muted-foreground">{evidenceDetail.agent_results.length} reviewer results · {sourceFindings.length} findings · {images.length} captured images</p> : null}
                {relatedFailedAssessment ? <p><Link className="text-primary hover:underline" href={`/code-reviews?assessment=${relatedFailedAssessment.id}`} onClick={() => onOpenChange(false)}>View latest failed attempt</Link></p> : null}
                <dl className="space-y-3">
                  <div className="space-y-1"><dt className="text-muted-foreground">Commit</dt><dd className="font-mono text-xs">{value.head_sha}</dd></div>
                  {value.source_assessment_id ? <div className="space-y-1"><dt className="text-muted-foreground">Source full assessment</dt><dd><Link className="font-mono text-xs text-primary underline-offset-4 hover:underline" href={`/code-reviews?assessment=${value.source_assessment_id}`} onClick={() => onOpenChange(false)}>{value.source_assessment_id}</Link></dd></div> : null}
                  {value.previous_assessment_id && value.previous_assessment_id !== value.source_assessment_id ? <div className="space-y-1"><dt className="text-muted-foreground">Previous assessment</dt><dd><Link className="font-mono text-xs text-primary underline-offset-4 hover:underline" href={`/code-reviews?assessment=${value.previous_assessment_id}`} onClick={() => onOpenChange(false)}>{value.previous_assessment_id}</Link></dd></div> : null}
                </dl>
                {assessmentEvidence?.execution?.status === "completed" && assessmentEvidence.execution.native_context != null ? <p className="text-xs text-muted-foreground">Review context: {assessmentEvidence.execution.native_context ? "native provider continuation" : "reconstructed from recorded assessment evidence"}.</p> : null}
              </AssessmentSection>
            </>
          ) : null}
        </div>
        {value && validID ? <div className="shrink-0 border-t border-border px-6 py-4">
          <RecheckActions prID={prID ?? ""} canManage={canManage && Boolean(prID)} completed={value.status === "completed"} presentation="footer" sessionID={value.session_id ?? undefined} onViewAssessment={() => onOpenChange(false)} />
        </div> : null}
      </DialogContent>
    </Dialog>
  );
}
