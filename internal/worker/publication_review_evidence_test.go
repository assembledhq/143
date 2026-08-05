package worker

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/models"
)

func TestPublicationReviewPushParamsBindsReviewedCheckpoint(t *testing.T) {
	t.Parallel()

	changesetID := uuid.New()
	desiredHeadSHA := "abc1234567890abcdef1234567890abcdef12345"
	params := publicationReviewPushParams(models.SessionReviewLoop{
		ChangesetID:    &changesetID,
		DesiredHeadSHA: &desiredHeadSHA,
	})

	require.Equal(t, &changesetID, params.ChangesetID, "review evidence refresh should retain the reviewed changeset")
	require.Equal(t, desiredHeadSHA, params.ExpectedRemoteHeadSHA, "review evidence refresh should bind the exact reviewed remote checkpoint")
}
