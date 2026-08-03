package main

import (
	reviewloopservice "github.com/assembledhq/143/internal/services/reviewloop"
	"github.com/assembledhq/143/internal/worker"
)

type publicationEvidenceRefresherSetter interface {
	SetPublicationEvidenceRefresher(reviewloopservice.PublicationEvidenceRefresher)
}

// wirePublicationReviewEvidenceRefresher keeps long-lived workers and
// isolated session executors on the same publication-review runtime contract.
// Both processes can finish a review turn, so both need the push/checkpoint
// dependencies used to bind the next pass to the updated workspace revision.
func wirePublicationReviewEvidenceRefresher(services *worker.Services, stores *worker.Stores) {
	if services == nil || stores == nil {
		return
	}
	reviewLoops, ok := services.ReviewLoops.(publicationEvidenceRefresherSetter)
	if !ok {
		return
	}
	reviewLoops.SetPublicationEvidenceRefresher(
		worker.NewPublicationReviewEvidenceRefresher(stores, services.PR),
	)
}
