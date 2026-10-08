package codereview

import (
	"encoding/json"
	"fmt"

	"github.com/assembledhq/143/internal/models"
)

// BuildAssessmentCapture converts verified review inputs into the immutable
// persistence record. Callers supply identity, lineage, route and publication
// key in identity; code/review input fields always come from captured sources.
// Incomplete provenance is an error, never a synthetic reusable baseline.
func BuildAssessmentCapture(identity models.CodeReviewAssessmentCapture, input ReviewInputCapture) (models.CodeReviewAssessmentCapture, error) {
	manifest, err := BuildReviewInputManifest(input)
	if err != nil {
		return models.CodeReviewAssessmentCapture{}, fmt.Errorf("capture code review assessment inputs: %w", err)
	}
	return AssessmentCaptureFromManifest(identity, manifest)
}

// AssessmentCaptureFromManifest is used when an authoritative capture service
// has already fetched and hashed the immutable sources.
func AssessmentCaptureFromManifest(identity models.CodeReviewAssessmentCapture, manifest ReviewInputManifest) (models.CodeReviewAssessmentCapture, error) {
	if identity.OrgID != manifest.Code.OrgID || identity.RepositoryID != manifest.Code.RepositoryID || identity.PullRequestID != manifest.Code.PullRequestID || identity.PolicyID != manifest.Contract.PolicyID {
		return models.CodeReviewAssessmentCapture{}, fmt.Errorf("assessment identity differs from captured review inputs")
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		return models.CodeReviewAssessmentCapture{}, fmt.Errorf("encode review input manifest: %w", err)
	}
	identity.HeadSHA = manifest.Code.HeadSHA
	identity.BaseSHA = manifest.Code.BaseSHA
	identity.BaseRef = manifest.Code.BaseRef
	identity.InputVersion = manifest.InputVersion
	identity.CodeDigest = manifest.CodeDigest
	identity.ContractDigest = manifest.ContractDigest
	identity.IntentDigest = manifest.IntentDigest
	identity.VisualDigest = manifest.VisualDigest
	identity.RequestDigest = manifest.RequestDigest
	identity.GateDigest = manifest.GateDigest
	identity.InputDigest = manifest.InputDigest
	identity.InputManifest = raw
	return identity, nil
}
