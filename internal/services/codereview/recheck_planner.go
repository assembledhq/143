package codereview

import (
	"errors"
	"github.com/assembledhq/143/internal/models"
	"reflect"
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
	RecheckReasonForceFresh        RecheckReason = "force_fresh"
	RecheckReasonDispute           RecheckReason = "dispute"
	RecheckReasonNoBaseline        RecheckReason = "no_complete_baseline"
	RecheckReasonCodeChanged       RecheckReason = "code_changed"
	RecheckReasonContractChanged   RecheckReason = "contract_changed"
	RecheckReasonIntentChanged     RecheckReason = "intent_changed"
	RecheckReasonRequestChanged    RecheckReason = "request_changed"
	RecheckReasonGatesChanged      RecheckReason = "gates_changed"
	RecheckReasonBaselineBlocked   RecheckReason = "baseline_not_visual_only"
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
// caller's PR lock. This function never infers continuity from a commit SHA or
// policy ID alone and never authorizes publication.
func PlanReviewRecheck(in RecheckPlanInput) RecheckPlan {
	if in.CaptureError != nil || in.Current == nil || !validManifest(*in.Current) {
		return RecheckPlan{RecheckRouteWait, RecheckReasonInputsUnavailable}
	}
	c := *in.Current
	if in.ForceFresh {
		return RecheckPlan{RecheckRouteFull, RecheckReasonForceFresh}
	}
	if in.DisputeRouted || c.Request.DisputeRouted {
		return RecheckPlan{RecheckRouteFull, RecheckReasonDispute}
	}
	if in.Previous != nil && in.Previous.Completed && !in.Previous.EvidenceValidated {
		return RecheckPlan{RecheckRouteFull, RecheckReasonEvidenceInvalid}
	}
	if !c.ReuseEligible {
		return RecheckPlan{RecheckRouteFull, RecheckReasonIntentChanged}
	}
	if in.Previous != nil && in.Previous.Completed && in.Previous.EvidenceValidated &&
		validManifest(in.Previous.Inputs) && in.Previous.Inputs.ReuseEligible &&
		c.InputDigest == in.Previous.Inputs.InputDigest {
		return RecheckPlan{RecheckRouteReuse, RecheckReasonUnchanged}
	}
	b := in.Baseline
	if b == nil || !b.CompletedFull || !b.CoverageComplete || !validManifest(b.Inputs) || !b.Inputs.ReuseEligible {
		return RecheckPlan{RecheckRouteFull, RecheckReasonNoBaseline}
	}
	if c.CodeDigest != b.Inputs.CodeDigest {
		return RecheckPlan{RecheckRouteFull, RecheckReasonCodeChanged}
	}
	if c.ContractDigest != b.Inputs.ContractDigest {
		return RecheckPlan{RecheckRouteFull, RecheckReasonContractChanged}
	}
	if c.IntentDigest != b.Inputs.IntentDigest {
		return RecheckPlan{RecheckRouteFull, RecheckReasonIntentChanged}
	}
	if c.RequestDigest != b.Inputs.RequestDigest {
		return RecheckPlan{RecheckRouteFull, RecheckReasonRequestChanged}
	}
	if c.GateDigest != b.Inputs.GateDigest {
		return RecheckPlan{RecheckRouteFull, RecheckReasonGatesChanged}
	}
	if len(b.RiskReasons) != 1 || b.RiskReasons[0] != models.CodeReviewRiskReasonDescriptionFailed ||
		len(b.MissingRequirements) == 0 {
		return RecheckPlan{RecheckRouteFull, RecheckReasonBaselineBlocked}
	}
	seen := make(map[string]bool, len(b.MissingRequirements))
	for _, req := range b.MissingRequirements {
		if req.ID == "" || req.EvidenceKind != "visual" || seen[req.ID] {
			return RecheckPlan{RecheckRouteFull, RecheckReasonBaselineBlocked}
		}
		seen[req.ID] = true
	}
	if c.VisualDigest == b.Inputs.VisualDigest {
		return RecheckPlan{RecheckRouteFull, RecheckReasonBaselineBlocked}
	}
	return RecheckPlan{RecheckRouteEvidenceOnly, RecheckReasonVisualChanged}
}

func validManifest(m ReviewInputManifest) bool {
	if m.InputVersion != ReviewInputManifestVersion {
		return false
	}
	expected, err := BuildReviewInputManifest(ReviewInputCapture{
		Code: m.Code, Contract: m.Contract, Title: m.Title, Description: m.Description,
		Visual: m.Visual, Request: m.Request, Gates: m.Gates,
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
