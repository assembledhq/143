package worker

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
)

type reviewRequestPRService struct {
	*stubPRService
	request func(*models.Session, *models.PullRequest, *models.SessionPublication, []string) error
}

func (s *reviewRequestPRService) RequestPublicationTeamReviews(_ context.Context, run *models.Session, pr *models.PullRequest, publication *models.SessionPublication, teams []string) error {
	return s.request(run, pr, publication, teams)
}

type teamReviewPRWriteback struct{ calls int }

func (s *teamReviewPRWriteback) OnPROpened(context.Context, models.Session, models.PullRequest) error {
	s.calls++
	return nil
}

func TestCompleteOpenPRJobReplaysFrozenTeamReviewFollowThrough(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		reviewErr error
		firstOpen bool
		merge     bool
	}{
		{name: "completed publication still follows through"},
		{name: "review request failure retains created PR checkpoint", reviewErr: errors.New("review request unavailable")},
		{name: "failed team preserves first-open effects and holds automatic merge", reviewErr: errors.New("team request rejected"), firstOpen: true, merge: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			stores, mock := newTestStores(t)
			t.Cleanup(mock.Close)
			stores.SessionPublications = db.NewSessionPublicationStore(mock)
			orgID, sessionID, changesetID := uuid.New(), uuid.New(), uuid.New()
			payload, err := json.Marshal(openPRJobInput{OrgID: orgID.String(), SessionID: sessionID.String(), ChangesetID: changesetID.String(), TeamReviewers: []string{"acme/team"}})
			require.NoError(t, err, "frozen publication fixture should encode")
			publication := models.SessionPublication{ID: uuid.New(), OrgID: orgID, SessionID: sessionID, ChangesetID: changesetID, State: models.SessionPublicationStateCompleted, ReviewGateState: models.SessionPublicationReviewGatePassed, RequestPayload: payload}
			for range 2 {
				mock.ExpectQuery("SELECT .+ FROM session_publications").WithArgs(orgID, sessionID, changesetID).WillReturnRows(pgxmock.NewRows(workerPublicationTestColumns).AddRow(workerPublicationTestRow(publication)...))
			}
			mock.ExpectQuery("INSERT INTO session_publish_state").WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), prCreationStateArg{state: models.PRCreationStateSucceeded}, pgxmock.AnyArg()).WillReturnRows(pgxmock.NewRows(workerSessionColumns))
			calls := 0
			writeback := &teamReviewPRWriteback{}
			services := &Services{PagerDutyWrites: writeback, PR: &reviewRequestPRService{stubPRService: &stubPRService{queueMergeWhenReadyFn: func(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (*models.PullRequestMergeWhenReadyStatus, error) {
				t.Fatal("automatic merge must wait until explicitly requested team reviews are handed off")
				return nil, nil
			}}, request: func(run *models.Session, pr *models.PullRequest, got *models.SessionPublication, teams []string) error {
				calls++
				require.Equal(t, sessionID, run.ID, "follow-through should preserve finalized session")
				require.Equal(t, &changesetID, pr.ChangesetID, "follow-through should target the finalized changeset PR")
				require.Equal(t, publication, *got, "review requests should observe current finalized durable state")
				require.Equal(t, []string{"acme/team"}, teams, "review requests must use frozen payload rather than replay caller choices")
				return tt.reviewErr
			}}}
			priorState, expectedWritebacks := models.PRCreationStateSucceeded, 0
			if tt.firstOpen {
				priorState, expectedWritebacks = models.PRCreationStatePushing, 1
			}
			err = completeOpenPRJob(context.Background(), stores, services, zerolog.Nop(), models.Session{ID: sessionID, OrgID: orgID}, openPRJobInput{TeamReviewers: []string{"acme/untrusted-replay"}, MergeWhenReady: tt.merge}, &changesetID, &models.PullRequest{OrgID: orgID, SessionID: &sessionID, ChangesetID: &changesetID}, &publication, priorState)
			if tt.reviewErr != nil {
				require.ErrorIs(t, err, tt.reviewErr, "follow-through failure should remain visible to the durable job retry")
				require.ErrorIs(t, err, errPublicationTeamReviewsIncomplete, "post-publication failures must not be classified as publication failures")
			} else {
				require.NoError(t, err, "completed publication replay should finish team review follow-through")
			}
			require.Equal(t, 1, calls, "completed replay suppression must not skip durable review requests")
			require.Equal(t, expectedWritebacks, writeback.calls, "failed review requests must not suppress first-open effects or duplicate them on replay")
			require.NoError(t, mock.ExpectationsWereMet(), "already-created PR must retain its success checkpoint even while reviewer follow-through retries")
		})
	}
}
