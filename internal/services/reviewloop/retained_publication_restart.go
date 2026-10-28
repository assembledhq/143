package reviewloop

import (
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
)

// IsStrandedPublicationRestart recognizes the existing durable restart marker
// written atomically with clearing a publication's old loop/evidence link.
// A detached terminal loop alone is not authority to create a replacement.
func IsStrandedPublicationRestart(loop models.SessionReviewLoop, publication models.SessionPublication) bool {
	return loop.ID != uuid.Nil && loop.OrgID != uuid.Nil && loop.SessionID != uuid.Nil &&
		loop.Source == models.ReviewLoopSourcePublication && loop.Status == models.ReviewLoopStatusFailed &&
		loop.LatestSummary != nil && *loop.LatestSummary == strandedPublicationReviewSummary &&
		loop.AdmissionState != nil && *loop.AdmissionState == models.ReviewLoopAdmissionAccepted &&
		publication.OrgID == loop.OrgID && publication.SessionID == loop.SessionID &&
		loop.ChangesetID != nil && *loop.ChangesetID != uuid.Nil && publication.ChangesetID == *loop.ChangesetID &&
		publication.ReviewStartID != nil && *publication.ReviewStartID == loop.ID &&
		publication.State == models.SessionPublicationStateReviewPending && publication.ReviewGateState == models.SessionPublicationReviewGatePending &&
		publication.ReviewLoopID == nil && publication.ReviewWorkspaceRevision == nil && publication.ReviewDesiredHeadSHA == nil
}
