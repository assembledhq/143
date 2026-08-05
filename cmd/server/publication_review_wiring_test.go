package main

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/models"
	reviewloopservice "github.com/assembledhq/143/internal/services/reviewloop"
	"github.com/assembledhq/143/internal/worker"
)

func TestWirePublicationReviewEvidenceRefresherConfiguresReviewRuntime(t *testing.T) {
	t.Parallel()

	reviews := &wiringReviewLoops{}
	services := &worker.Services{ReviewLoops: reviews}
	stores := &worker.Stores{}

	wirePublicationReviewEvidenceRefresher(services, stores)

	require.NotNil(t, reviews.refresher, "worker and session-executor runtimes should receive publication evidence refresh dependencies")
}

type wiringReviewLoops struct {
	refresher reviewloopservice.PublicationEvidenceRefresher
}

func (w *wiringReviewLoops) SetPublicationEvidenceRefresher(refresher reviewloopservice.PublicationEvidenceRefresher) {
	w.refresher = refresher
}

func (w *wiringReviewLoops) OnThreadTurnComplete(context.Context, uuid.UUID, uuid.UUID, string) error {
	return nil
}

func (w *wiringReviewLoops) OnThreadTurnFailed(context.Context, uuid.UUID, uuid.UUID, string) error {
	return nil
}

func (w *wiringReviewLoops) Start(context.Context, uuid.UUID, uuid.UUID, reviewloopservice.StartReviewLoopRequest) (*models.SessionReviewLoop, error) {
	return nil, nil
}

func (w *wiringReviewLoops) ReconcileStrandedPublicationLoops(context.Context, uuid.UUID, time.Time, int) (int, error) {
	return 0, nil
}
