package codereview

import (
	"errors"
	"reflect"

	"github.com/assembledhq/143/internal/models"
)

type RecheckRoute string

const (
	RecheckRouteWait         RecheckRoute = "wait"
	RecheckRouteReuse        RecheckRoute = "reuse"
	RecheckRouteEvidenceOnly RecheckRoute = "evidence_only"
	RecheckRouteFull         RecheckRoute = "full"
)

type RecheckReason string

const (
	RecheckReasonInputsUnavailable RecheckReason = "inputs_unavailable"
	RecheckReasonUnchanged         RecheckReason = "unchanged"
	RecheckReasonVisualChanged     RecheckReason = "visual_changed"
	RecheckReasonEvidenceChanged   RecheckReason = "evidence_changed"
	RecheckReasonChecksChanged     RecheckReason = "checks_changed"
	RecheckReasonForceFresh        RecheckReason = "force_fresh"
	RecheckReasonDispute           RecheckReason = "dispute"
	RecheckReasonNoBaseline        RecheckReason = "no_complete_baseline"
	RecheckReasonCodeChanged       RecheckReason = "code_changed"
	RecheckReasonContractChanged   RecheckReason = "contract_changed"
	RecheckReasonIntentChanged     RecheckReason = "intent_changed"
	RecheckReasonRequestChanged    RecheckReason = "request_changed"
	RecheckReasonGatesChanged      RecheckReason = "gates_changed"
	RecheckReasonBaselineBlocked   RecheckReason = "baseline_not_visual_only"
	RecheckReasonNoEvidenceChange  RecheckReason = "no_evidence_change"
	RecheckReasonEvidenceInvalid   RecheckReason = "evidence_validation_failed"
)

type RecheckPlan struct {
	Route  RecheckRoute  `json:"route"`
	Reason RecheckReason `json:"reason"`
}

// RecheckBaseline is supplied by the assessment reader. CoverageComplete must
// mean every required reviewer returned valid, read-only coverage, quorum was
// satisfied, and synthesis was validated. MissingRequirements is the complete
// structured set from that assessment, not a subset inferred from prose.
type RecheckBaseline struct {
	Inputs              ReviewInputManifest
	CompletedFull       bool
	CoverageComplete    bool
	RiskReasons         []models.CodeReviewRiskReasonCode
	MissingRequirements []RecheckMissingRequirement
}

type RecheckMissingRequirement struct {
	ID           string
	EvidenceKind string
}

type RecheckPrevious struct {
	Inputs            ReviewInputManifest
	Completed         bool
	EvidenceValidated bool
}

type RecheckPlanInput struct {
	Current       *ReviewInputManifest
	CaptureError  error
	Baseline      *RecheckBaseline
	Previous      *RecheckPrevious
	ForceFresh    bool
	DisputeRouted bool
}

// PlanReviewRecheck is pure admission classification. Duplicate request IDs,
// equivalent active work, eligibility, and authorization are checked under the
// caller's PR lock. A complete code fingerprint and versioned policy must match;
// a commit SHA or policy ID alone is insufficient. This function never authorizes
// publication.
func PlanReviewRecheck(in RecheckPlanInput) RecheckPlan {
	if in.CaptureError != nil || in.Current == nil || !validManifest(*in.Current) {
		return RecheckPlan{RecheckRouteWait, RecheckReasonInputsUnavailable}
	}
	c := *in.Current
	if in.ForceFresh {
		return RecheckPlan{RecheckRouteFull, RecheckReasonForceFresh}
	}
	b := in.Baseline
	if b == nil || !b.CompletedFull || !b.CoverageComplete || !validManifest(b.Inputs) || !b.Inputs.ReuseEligible {
		return RecheckPlan{RecheckRouteFull, RecheckReasonNoBaseline}
	}
	if reason := RecheckBaselineChange(c, b.Inputs); reason != "" {
		return RecheckPlan{RecheckRouteFull, reason}
	}
	if in.Previous != nil && in.Previous.Completed && in.Previous.EvidenceValidated &&
		validManifest(in.Previous.Inputs) && in.Previous.Inputs.ReuseEligible &&
		c.InputDigest == in.Previous.Inputs.InputDigest {
		return RecheckPlan{RecheckRouteReuse, RecheckReasonUnchanged}
	}
	// All mutable review inputs are reassessed against the same reviewed code
	// and policy. Their fingerprints still fence dispatch and publication;
	// they do not invalidate the completed code review.
	if c.Gates.ChecksDigest != b.Inputs.Gates.ChecksDigest {
		return RecheckPlan{RecheckRouteEvidenceOnly, RecheckReasonChecksChanged}
	}
	if c.GateDigest != b.Inputs.GateDigest {
		return RecheckPlan{RecheckRouteEvidenceOnly, RecheckReasonGatesChanged}
	}
	if c.TextDigest != b.Inputs.TextDigest || c.IntentDigest != b.Inputs.IntentDigest || c.RequestDigest != b.Inputs.RequestDigest {
		return RecheckPlan{RecheckRouteEvidenceOnly, RecheckReasonEvidenceChanged}
	}
	if c.VisualDigest == b.Inputs.VisualDigest {
		return RecheckPlan{RecheckRouteEvidenceOnly, RecheckReasonNoEvidenceChange}
	}
	return RecheckPlan{RecheckRouteEvidenceOnly, RecheckReasonVisualChanged}
}

// RecheckBaselineChange compares valid manifests for code-review reuse. The
// resolved, versioned policy includes review instructions, approval rules, and
// the reviewer roster. Auxiliary prompt/runtime fingerprints remain provenance
// and freshness inputs, not reasons to rerun an unchanged code review.
func RecheckBaselineChange(current, baseline ReviewInputManifest) RecheckReason {
	if current.CodeDigest != baseline.CodeDigest {
		return RecheckReasonCodeChanged
	}
	if current.Contract.PolicyID != baseline.Contract.PolicyID ||
		current.Contract.PolicyVersion != baseline.Contract.PolicyVersion ||
		current.Contract.PolicyDigest != baseline.Contract.PolicyDigest {
		return RecheckReasonContractChanged
	}
	return ""
}

func validManifest(m ReviewInputManifest) bool {
	if m.InputVersion != ReviewInputManifestVersion {
		return false
	}
	expected, err := BuildReviewInputManifest(ReviewInputCapture{
		Code: m.Code, Contract: m.Contract, Title: m.Title, Description: m.Description,
		Visual: m.Visual, TextEvidence: m.TextEvidence, Request: m.Request, Gates: m.Gates,
	})
	return err == nil && reflect.DeepEqual(m, expected)
}

// ValidateReviewInputManifest checks persisted content and its component
// digests before it is considered for reuse. It does not refresh live inputs;
// the caller must capture an authoritative current snapshot separately.
func ValidateReviewInputManifest(m ReviewInputManifest) error {
	if !validManifest(m) {
		return errors.New("invalid or incomplete review input manifest")
	}
	return nil
}
