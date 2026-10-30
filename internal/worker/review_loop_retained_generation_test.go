package worker

import (
	"context"
	"testing"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	reviewloopsvc "github.com/assembledhq/143/internal/services/reviewloop"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func TestRetainedPublicationDetachedGeneration(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name            string
		admission       models.ReviewLoopAdmissionState
		restartMarker   bool
		linked          bool
		otherLink       bool
		running         bool
		expectedHandled bool
	}{
		{name: "durably restarted accepted history permits ordinary generation", admission: models.ReviewLoopAdmissionAccepted, restartMarker: true},
		{name: "ordinary accepted failure before first link remains finite", admission: models.ReviewLoopAdmissionAccepted, expectedHandled: true},
		{name: "detached refused startup remains finite", admission: models.ReviewLoopAdmissionRefused, restartMarker: true, expectedHandled: true},
		{name: "detached cancelled startup remains finite", admission: models.ReviewLoopAdmissionCancelled, restartMarker: true, expectedHandled: true},
		{name: "linked resolved terminal still drives its current gate", admission: models.ReviewLoopAdmissionAccepted, restartMarker: true, linked: true, expectedHandled: true},
		{name: "a different existing link grants no fresh authority", admission: models.ReviewLoopAdmissionAccepted, restartMarker: true, otherLink: true, expectedHandled: true},
		{name: "detached running owner is still current", admission: models.ReviewLoopAdmissionAccepted, restartMarker: true, running: true, expectedHandled: true},
		{name: "detached unknown startup retains original", admission: models.ReviewLoopAdmissionUnknown, restartMarker: true, expectedHandled: true},
		{name: "detached attention startup retains original", admission: models.ReviewLoopAdmissionAttention, restartMarker: true, expectedHandled: true},
		{name: "missing admission projection grants no fresh authority", restartMarker: true, expectedHandled: true},
		{name: "unsupported admission projection grants no fresh authority", admission: "synthetic-future-state", restartMarker: true, expectedHandled: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			stores, mock := newTestStores(t)
			t.Cleanup(mock.Close)
			stores.SessionPublications = db.NewSessionPublicationStore(mock)
			org, session, changeset, id := uuid.New(), uuid.New(), uuid.New(), uuid.New()
			revision, head := int64(3), "synthetic-retained-head"
			loop := &models.SessionReviewLoop{ID: id, OrgID: org, SessionID: session, Source: models.ReviewLoopSourcePublication,
				ChangesetID: &changeset, Status: models.ReviewLoopStatusFailed, WorkspaceRevision: &revision, DesiredHeadSHA: &head, MaxPasses: 2}
			if tt.admission != "" {
				loop.AdmissionState = &tt.admission
			}
			publication := &models.SessionPublication{OrgID: org, SessionID: session, ChangesetID: changeset, ReviewStartID: &id, State: models.SessionPublicationStateReviewPending, ReviewGateState: models.SessionPublicationReviewGatePending}
			summary := "Synthetic ordinary failure before first loop link"
			if tt.restartMarker {
				summary = "Publication review became idle without a live continuation job; restarting review from the current workspace."
			}
			loop.LatestSummary = &summary
			if tt.restartMarker && !tt.running && !tt.linked && !tt.otherLink && tt.admission == models.ReviewLoopAdmissionAccepted {
				require.True(t, reviewloopsvc.IsStrandedPublicationRestart(*loop, *publication), "positive worker control must carry the exact existing durable restart shape")
			}
			if tt.linked {
				publication.ReviewLoopID = &id
			}
			if tt.otherLink {
				other := uuid.New()
				publication.ReviewLoopID = &other
			}
			if tt.running {
				loop.Status = models.ReviewLoopStatusRunning
				mock.ExpectExec("UPDATE session_publications[\\s\\S]+review_start_id=@loop_id").WithArgs(anyArgs(7)...).WillReturnResult(pgxmock.NewResult("UPDATE", 1))
			} else if tt.expectedHandled {
				rows := int64(1)
				if tt.otherLink {
					rows = 0
				}
				expectRetainedReviewGateCAS(mock, org, session, changeset, id, nil, models.SessionPublicationReviewGateFailed, rows)
			}
			recovery := &retainedCallerReviewStub{loop: loop}
			handled, err := recoverPublicationReviewStart(context.Background(), stores, &Services{ReviewLoops: recovery}, models.Session{ID: session, OrgID: org}, publication)
			require.NoError(t, err, "exact retained identity must preserve the current owner or yield only resolved detached history")
			require.Equal(t, tt.expectedHandled, handled, "only the exact durable stranded restart shape may reach ordinary generation work")
			require.Equal(t, [][3]uuid.UUID{{org, session, id}}, recovery.calls, "generation decision must follow exact original recovery")
			require.Empty(t, recovery.starts, "the generation filter itself must never invoke a new Start")
			require.NoError(t, mock.ExpectationsWereMet(), "linked, uncertain and incomplete owners must retain only their expected current-generation writes")
		})
	}
}
