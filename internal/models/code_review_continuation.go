package models

// CodeReviewContinuationPolicy is versioned with the rest of the review
// policy. Nil on a legacy record resolves to both capabilities disabled.
type CodeReviewContinuationPolicy struct {
	Enabled                   bool `json:"enabled"`
	AutomaticEvidenceRechecks bool `json:"automatic_evidence_rechecks"`
}

func (p *CodeReviewContinuationPolicy) Effective() CodeReviewContinuationPolicy {
	if p == nil {
		return CodeReviewContinuationPolicy{}
	}
	return *p
}

func (p *CodeReviewContinuationPolicy) Validate() error {
	if p == nil {
		return nil
	}
	if p.AutomaticEvidenceRechecks && !p.Enabled {
		return codeReviewPolicyFieldError("continuation_policy", "automatic_evidence_rechecks requires continuation enabled")
	}
	return nil
}
