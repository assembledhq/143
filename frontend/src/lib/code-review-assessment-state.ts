import type { OperationalStatePresentation } from "@/lib/operational-state";
import type { CodeReviewAssessmentSummary } from "@/lib/types";

export function activeAssessmentState(assessment: CodeReviewAssessmentSummary | null | undefined): OperationalStatePresentation | null {
  if (!assessment) return null;
  const scope = assessment.review_scope === "evidence_only" ? "Evidence re-check" : "Full review";
  switch (assessment.status) {
    case "reserved":
      return { label: `${scope} queued`, tone: "primary", activity: "none", attention: "none" };
    case "running":
      return { label: `${scope} running`, tone: "primary", activity: "breathing", attention: "none" };
    case "publishing":
      return { label: `${scope} publishing`, tone: "primary", activity: "breathing", attention: "none" };
    default:
      return null;
  }
}
